package core

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
)

// このファイルは区切り S4b-2 の「選べる手を絞る」（AC-77〜104）を、実ストアで検証する。
// 絞る規則は offeredSupervisorHands（supervisor_hands.go）。選べない手を返したときの断り（AC-78・82・84・86・88・90・92・94・96）は、
// 段の最小の検査（ParseSVOutput が入力に渡した一覧で検査する）をそのまま通して固定する。

// offeredFixture は課題・タスク・契約の実行（レビュー待ち）を 1 つずつ持つ。
type offeredFixture struct {
	*contractFixture
	taskID int64
	cidInt int64
}

func newOfferedFixture(t *testing.T) *offeredFixture {
	t.Helper()
	f := &offeredFixture{contractFixture: newContractFixture(t)}
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.cidInt, _ = parseChallengeID(f.cid)
	f.taskID = f.count(t, `SELECT task_id FROM contract_run`)
	f.setRun(t, `state = 'waiting', requested_kind = 'review', ended_at = NULL, held_success = 0, land_ref = NULL, land_delivered = 0`)
	return f
}

func (f *offeredFixture) setRun(t *testing.T, set string) {
	t.Helper()
	if _, err := f.exec(`UPDATE contract_run SET ` + set); err != nil {
		t.Fatal(err)
	}
}

// hold は、直前の状態 from・止まり方 kind の保留を課題に作り、その対象を返す（withTask が偽ならタスクを指さない）。
func (f *offeredFixture) hold(t *testing.T, from Status, kind StopKind, withTask bool) supervisorTarget {
	t.Helper()
	setChallengeStatus(t, f.s, f.cidInt, from)
	var task *int64
	if withTask {
		v := f.taskID
		task = &v
	}
	if _, err := f.s.transition(context.Background(), ChannelCLI, f.cid, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		_, err := insertHoldWithOptions(ctx, tc, "どうする？", HoldKindHuman, tc.current.Status, task, HoldOptionsInput{StopKind: kind, SupervisorEnabled: true})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return supervisorTarget{HoldID: f.count(t, `SELECT MAX(id) FROM hold`), ChallengeID: f.cidInt, TaskID: task, StopKind: kind}
}

func (f *offeredFixture) offered(t *testing.T, tg supervisorTarget) []SupervisorHand {
	t.Helper()
	got, err := f.s.offeredSupervisorHands(context.Background(), tg)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (f *offeredFixture) record(t *testing.T, tg supervisorTarget, hand SupervisorHand, result SupervisorActionResult) {
	t.Helper()
	in := SupervisorActionInput{
		CycleID: f.cycle(t, 300), ChallengeID: f.cid, StopKind: tg.StopKind, Hand: hand, Findings: "調べた", Result: result,
	}
	if tg.TaskID != nil {
		in.TaskID = formatTaskID(*tg.TaskID)
	}
	if _, err := f.s.RecordSupervisorAction(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}

func wantOffered(t *testing.T, name string, got []SupervisorHand, want ...SupervisorHand) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: offered = %v, want %v", name, got, want)
	}
}

// AC-77・81・83・85・87・89・91・93・95: 止まり方ごとの一覧を、外す条件に当たらない状態で固定する。
func TestOfferedHands_EachStopKindInAStateWithNothingToRemove(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind StopKind
		run  string // 契約の実行の状態を決める set 句
		want []SupervisorHand
	}{
		{StopKindVerifyUncertain, "", []SupervisorHand{HandReverify, HandSendBack, HandSplitFollowup, HandReplan, HandDefer, HandEscalate}},
		{StopKindArtifactMissing, "", []SupervisorHand{HandReverify, HandRetry, HandDefer, HandEscalate}},
		{StopKindDelegateStopped, "state = 'failed', ended_at = '2026-11-22T00:00:00.000Z'", []SupervisorHand{HandRetry, HandReplan, HandDefer, HandEscalate}},
		{StopKindFactsUnmet, "", []SupervisorHand{HandSendBack, HandEscalate}},
		{StopKindChecksFailed, "", []SupervisorHand{HandEscalate}}, // 開いているレビュー待ちがあっても send_back は外れる
		{StopKindReworkLimit, "", []SupervisorHand{HandSendBack, HandReplan, HandEscalate}},
		{StopKindJudgmentFailed, "", []SupervisorHand{HandRetry, HandEscalate}},
		{StopKindIntakeUnverified, "", []SupervisorHand{HandReverify, HandDefer, HandEscalate}},
		{StopKindUpstreamUnreadable, "", []SupervisorHand{HandDefer, HandEscalate}},
	}
	for _, c := range cases {
		c := c
		t.Run(string(c.kind), func(t *testing.T) {
			t.Parallel()
			f := newOfferedFixture(t)
			if c.run != "" {
				f.setRun(t, c.run)
			}
			wantOffered(t, string(c.kind), f.offered(t, f.hold(t, StatusInProgress, c.kind, true)), c.want...)
		})
	}
}

// AC-97: 同じタスクの同じ止まり方に、結果が executed の retry の記録が 1 件あれば retry が外れる。failed の記録・別の止まり方の記録・
// 最後のオーナーの回答より前の記録では外れない。
func TestOfferedHands_RetryIsRemovedAfterOneExecutedRetryOnTheSameTaskAndStopKind(t *testing.T) {
	t.Parallel()
	f := newOfferedFixture(t)
	tg := f.hold(t, StatusInProgress, StopKindArtifactMissing, true)
	f.record(t, tg, HandRetry, ActionFailed)
	other := tg
	other.StopKind = StopKindJudgmentFailed
	f.record(t, other, HandRetry, ActionExecuted)
	wantOffered(t, "failed と別の止まり方の記録", f.offered(t, tg), HandReverify, HandRetry, HandDefer, HandEscalate)
	f.record(t, tg, HandRetry, ActionExecuted)
	wantOffered(t, "executed の retry が 1 件", f.offered(t, tg), HandReverify, HandDefer, HandEscalate)
	// 最後のオーナーの回答より後でなければ数えない（先行の回数の上限と同じ数え方）。
	f.exec2(t, `UPDATE hold SET answer = 'はい', answered_at = '2999-01-01T00:00:00.000Z', answered_by = 'alice' WHERE id = ?`, tg.HoldID)
	wantOffered(t, "オーナーの回答より前の記録", f.offered(t, tg), HandReverify, HandRetry, HandDefer, HandEscalate)
}

func (f *offeredFixture) exec2(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// AC-98: 作業している契約の実行がある課題の保留では、replan が外れる（判定は contractRunWorkingTx）。
func TestOfferedHands_ReplanIsRemovedWhileAContractRunIsWorking(t *testing.T) {
	t.Parallel()
	f := newOfferedFixture(t)
	f.setRun(t, `state = 'running', requested_kind = NULL, ended_at = NULL`)
	wantOffered(t, "running", f.offered(t, f.hold(t, StatusInProgress, StopKindDelegateStopped, true)), HandRetry, HandDefer, HandEscalate)
}

// AC-99: 保留の直前の状態が着手中でも検証中でもないとき、replan が外れる（判定は ReplanAllowedFrom）。
func TestOfferedHands_ReplanIsRemovedWhenThePriorStatusIsNeitherInProgressNorVerifying(t *testing.T) {
	t.Parallel()
	f := newOfferedFixture(t)
	f.setRun(t, `state = 'failed', ended_at = '2026-11-22T00:00:00.000Z'`)
	for _, from := range []Status{StatusInProgress, StatusVerifying} {
		tg := f.hold(t, from, StopKindDelegateStopped, true)
		wantOffered(t, string(from), f.offered(t, tg), HandRetry, HandReplan, HandDefer, HandEscalate)
		f.exec2(t, `UPDATE hold SET closed_at = '2026-11-22T00:00:00.000Z' WHERE id = ?`, tg.HoldID)
	}
	tg := f.hold(t, StatusInProgress, StopKindDelegateStopped, true)
	f.exec2(t, `UPDATE hold SET from_status = ? WHERE id = ?`, string(StatusUnclassified), tg.HoldID)
	if ReplanAllowedFrom(StatusUnclassified) {
		t.Fatal("the test needs a status that ReplanAllowedFrom refuses")
	}
	wantOffered(t, string(StatusUnclassified), f.offered(t, tg), HandRetry, HandDefer, HandEscalate)
}

// AC-100: タスクを指さない verify_uncertain（課題の単位の完了の検証の「判断できない」）は、send_back と split_followup が外れる。
func TestOfferedHands_VerifyUncertainWithoutATaskDropsSendBackAndSplitFollowup(t *testing.T) {
	t.Parallel()
	f := newOfferedFixture(t)
	f.setRun(t, `state = 'failed', ended_at = '2026-11-22T00:00:00.000Z'`)
	wantOffered(t, "no task", f.offered(t, f.hold(t, StatusInProgress, StopKindVerifyUncertain, false)), HandReverify, HandReplan, HandDefer, HandEscalate)
}

// AC-101: 保留が指すタスクの最後の実行が、開いているレビュー待ちの契約の実行でない verify_uncertain（委譲の報告の形・終わった実行・実行なし）は、
// send_back が外れる。
func TestOfferedHands_SendBackIsRemovedForVerifyUncertainWithoutAnOpenReviewWaitingRun(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"終わった実行":                 `state = 'succeeded', ended_at = '2026-11-22T00:00:00.000Z'`,
		"レビュー待ちでない（問い待ち）":        `requested_kind = 'answer'`,
		"別のタスクのレビュー待ち":           `task_id = NULL`,
		"委譲の報告の形・実行なし（契約の実行が無い）": ``,
	}
	for name, set := range cases {
		name, set := name, set
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newOfferedFixture(t)
			if set != "" {
				f.setRun(t, set)
			} else {
				f.exec2(t, `DELETE FROM contract_run`)
			}
			got := f.offered(t, f.hold(t, StatusInProgress, StopKindVerifyUncertain, true))
			for _, h := range got {
				if h == HandSendBack {
					t.Errorf("offered = %v, want no send_back", got)
				}
			}
		})
	}
}

// AC-102: 保留が指すタスクの契約の実行が終わっている rework_limit は、send_back が外れる（replan は残る）。
func TestOfferedHands_SendBackIsRemovedForReworkLimitWhenTheRunEnded(t *testing.T) {
	t.Parallel()
	f := newOfferedFixture(t)
	f.setRun(t, `state = 'cancelled', ended_at = '2026-11-22T00:00:00.000Z'`)
	wantOffered(t, "ended", f.offered(t, f.hold(t, StatusInProgress, StopKindReworkLimit, true)), HandReplan, HandEscalate)
}

// AC-103: 保留の直前の状態が検証中の保留は、send_back が外れる。
func TestOfferedHands_SendBackIsRemovedWhenThePriorStatusIsVerifying(t *testing.T) {
	t.Parallel()
	f := newOfferedFixture(t)
	wantOffered(t, "verifying", f.offered(t, f.hold(t, StatusVerifying, StopKindFactsUnmet, true)), HandEscalate)
}

// AC-104: 最後のオーナーの回答より後に、そのタスクの rework_limit に、結果が executed の send_back が 1 件あれば、send_back が外れる。
// 回答より前の記録・failed の記録・別のタスクを指さない記録では外れない（verify_uncertain の send_back にこの 1 回の制限は無い）。
func TestOfferedHands_ReworkLimitSendBackIsAllowedOncePerTaskAfterTheLastOwnerAnswer(t *testing.T) {
	t.Parallel()
	f := newOfferedFixture(t)
	tg := f.hold(t, StatusInProgress, StopKindReworkLimit, true)
	vu := f.hold(t, StatusInProgress, StopKindVerifyUncertain, true)
	f.record(t, vu, HandSendBack, ActionExecuted)
	f.record(t, vu, HandSendBack, ActionExecuted)
	wantOffered(t, "verify_uncertain は回数の制限なし", f.offered(t, vu), HandReverify, HandSendBack, HandSplitFollowup, HandReplan, HandDefer, HandEscalate)

	f.record(t, tg, HandSendBack, ActionFailed)
	wantOffered(t, "failed の記録", f.offered(t, tg), HandSendBack, HandReplan, HandEscalate)
	f.record(t, tg, HandSendBack, ActionExecuted)
	wantOffered(t, "executed の send_back が 1 件", f.offered(t, tg), HandReplan, HandEscalate)
	f.exec2(t, `UPDATE hold SET answer = 'はい', answered_at = '2999-01-01T00:00:00.000Z', answered_by = 'alice' WHERE id = ?`, tg.HoldID)
	wantOffered(t, "オーナーの回答より前の記録", f.offered(t, tg), HandSendBack, HandReplan, HandEscalate)
}

// 選べない手を返したとき（AC-78・82・84・86・88・90・92・94・96）: 手は実行されず、対処の記録の結果は failed で、保留はオーナーへ上がる。
func TestOfferedHands_AHandOutsideTheOfferedListIsNotExecutedAndTheActionFails(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ac   string
		kind StopKind
		hand SupervisorHand
	}{
		{"AC-78", StopKindVerifyUncertain, HandRetry},
		{"AC-82", StopKindArtifactMissing, HandSendBack},
		{"AC-84", StopKindDelegateStopped, HandReverify},
		{"AC-86", StopKindFactsUnmet, HandRetry},
		{"AC-88", StopKindChecksFailed, HandSendBack},
		{"AC-90", StopKindReworkLimit, HandRetry},
		{"AC-92", StopKindJudgmentFailed, HandReplan},
		{"AC-94", StopKindIntakeUnverified, HandRetry},
		{"AC-96", StopKindUpstreamUnreadable, HandReverify},
	}
	for _, c := range cases {
		c := c
		t.Run(c.ac+"_"+string(c.kind)+"_"+string(c.hand), func(t *testing.T) {
			t.Parallel()
			f := newSVFixture(t)
			id := f.pendingHold(t, c.kind)
			f.inv.result = svResult(map[string]any{"hand": string(c.hand), "note": "直して"})
			res := f.stage(t)
			if res.SuperviseHands[c.hand] != 0 {
				t.Errorf("hands executed = %v, want none", res.SuperviseHands)
			}
			as := f.actions(t, id)
			// 検査で落ちた経路は手を記録しない（手の実行まで進んで失敗した経路とは区別する）。
			if len(as) != 1 || as[0].Result != ActionFailed || as[0].Hand != nil {
				t.Fatalf("actions = %+v, want one failed action", as)
			}
			if r := f.holdRow(t, id); r.state.String != "escalated" {
				t.Errorf("hold state = %q, want escalated (a failed action goes to the owner)", r.state.String)
			}
			var n int
			if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
				return tx.QueryRow(`SELECT COUNT(*) FROM supervisor_action WHERE result = 'executed'`).Scan(&n)
			}); err != nil || n != 0 {
				t.Errorf("executed actions = %d (%v), want 0", n, err)
			}
		})
	}
}
