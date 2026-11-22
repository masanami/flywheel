package core

// このファイルは、回数の上限（保留を作る入口。AC-38〜AC-51・AC-197）と、タスクの検証の連続の数え（AC-211〜AC-214）を
// 実ストアで確かめる。対処の記録は、土台の API（recordSupervisorAction）で置く（スーパーバイザーの段を待たない）。

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

type limitCase struct {
	s     *Store
	cid   string
	task  string
	cycID string
}

// newLimitCase は、着手中の課題 C-1 とタスク T-1 と周 Y-1 を持つストアを作る。
func newLimitCase(t *testing.T) *limitCase {
	t.Helper()
	f := newDelegateFixture(t)
	f.newInProgress(t, "t", "P1", planSpec(nil))
	return &limitCase{s: f.s, cid: "C-1", task: "T-1", cycID: f.cycle(t, 300)}
}

func (c *limitCase) act(t *testing.T, kind StopKind, hand SupervisorHand, result SupervisorActionResult, reason SupervisorRejectReason, task string) {
	t.Helper()
	in := SupervisorActionInput{CycleID: c.cycID, ChallengeID: c.cid, TaskID: task, StopKind: kind, Hand: hand, Findings: "f", Result: result, RejectReason: reason}
	if _, err := c.s.recordSupervisorAction(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}

// hold は課題に保留を作り、問い・stop_kind・supervisor_state を返す（課題が着手中でなければ着手中へ戻してから）。
func (c *limitCase) hold(t *testing.T, kind StopKind, withTask bool) (question string, stop, state sql.NullString) {
	t.Helper()
	cid, _ := parseChallengeID(c.cid)
	var taskID *int64
	if withTask {
		id := int64(1)
		taskID = &id
	}
	if _, err := c.s.transition(context.Background(), ChannelCLI, c.cid, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		tc.taskID = taskID
		_, err := insertHoldWithOptions(ctx, tc, "もとの問い\n2 行目", HoldKindHuman, tc.current.Status, taskID, HoldOptionsInput{StopKind: kind, SupervisorEnabled: true})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT question, stop_kind, supervisor_state FROM hold WHERE challenge_id = ? ORDER BY id DESC LIMIT 1`, cid).Scan(&question, &stop, &state)
	}); err != nil {
		t.Fatal(err)
	}
	return
}

// release は保留を閉じて課題を着手中へ戻す（足場。answeredBy が空なら閉じた者は記録しない）。
func (c *limitCase) release(t *testing.T, answeredBy string) {
	t.Helper()
	if err := c.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE hold SET answer = 'a', answered_at = ?, answered_by = ? WHERE answered_at IS NULL`, formatTimestamp(c.s.currentTime()), answeredBy); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE challenge SET status = 'in_progress' WHERE id = 1`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func (c *limitCase) wantPending(t *testing.T, kind StopKind, withTask, want bool) {
	t.Helper()
	q, stop, state := c.hold(t, kind, withTask)
	if state.Valid != want || (want && state.String != "pending") {
		t.Errorf("supervisor_state = %v, want pending=%v", state, want)
	}
	if stop.String != string(kind) {
		t.Errorf("stop_kind = %q, want %q", stop.String, kind)
	}
	if want && q != "もとの問い\n2 行目" {
		t.Errorf("question = %q, want unchanged", q)
	}
	c.release(t, "supervisor")
}

func TestHoldLimit_AC38_TwoExecutedReverifiesStopTheSupervisor(t *testing.T) {
	t.Parallel()
	c := newLimitCase(t)
	c.act(t, StopKindVerifyUncertain, HandReverify, ActionExecuted, "", c.task)
	c.wantPending(t, StopKindVerifyUncertain, true, true) // 1 件では待つ
	c.act(t, StopKindVerifyUncertain, HandReverify, ActionExecuted, "", c.task)
	c.wantPending(t, StopKindVerifyUncertain, true, false)
}

func TestHoldLimit_AC39_AppliesToOtherHandsAndKinds(t *testing.T) {
	t.Parallel()
	c := newLimitCase(t)
	c.act(t, StopKindDelegateStopped, HandRetry, ActionExecuted, "", c.task)
	c.act(t, StopKindDelegateStopped, HandReplan, ActionExecuted, "", c.task)
	c.wantPending(t, StopKindDelegateStopped, true, false)
}

func TestHoldLimit_AC40_41_42_43_NoticeLineAndColumns(t *testing.T) {
	t.Parallel()
	c := newLimitCase(t)
	c.act(t, StopKindFactsUnmet, HandReverify, ActionExecuted, "", c.task)
	c.act(t, StopKindFactsUnmet, HandSendBack, ActionExecuted, "", c.task)
	q, stop, state := c.hold(t, StopKindFactsUnmet, true)
	want := "スーパーバイザーが 2 回対処したが、進まなかった（手: reverify、send_back）。\nもとの問い\n2 行目"
	if q != want {
		t.Errorf("question = %q, want %q", q, want) // AC-40・AC-41
	}
	if stop.String != "facts_unmet" || state.Valid { // AC-42・AC-43
		t.Errorf("stop_kind=%v supervisor_state=%v, want facts_unmet, NULL", stop, state)
	}
	// 通常の保留（上限に当たらない別の止まり方）の問いは、そのまま。
	c.release(t, "supervisor")
	if q, _, _ := c.hold(t, StopKindChecksFailed, true); q != "もとの問い\n2 行目" {
		t.Errorf("unlimited question = %q", q)
	}
}

func TestHoldLimit_AC44_OtherStopKindsAreNotCounted(t *testing.T) {
	t.Parallel()
	c := newLimitCase(t)
	c.act(t, StopKindFactsUnmet, HandReverify, ActionExecuted, "", c.task)
	c.act(t, StopKindFactsUnmet, HandReverify, ActionExecuted, "", c.task)
	c.wantPending(t, StopKindVerifyUncertain, true, true)
}

func TestHoldLimit_AC45_OwnerAnswerResetsTheCount(t *testing.T) {
	t.Parallel()
	c := newLimitCase(t)
	c.act(t, StopKindVerifyUncertain, HandReverify, ActionExecuted, "", c.task)
	c.act(t, StopKindVerifyUncertain, HandReverify, ActionExecuted, "", c.task)
	c.hold(t, StopKindChecksFailed, true) // 別の保留へオーナーが回答する
	c.release(t, "owner")
	c.wantPending(t, StopKindVerifyUncertain, true, true)
}

func TestHoldLimit_AC46_SupervisorClosingDoesNotAdvanceTheLastOwnerAnswer(t *testing.T) {
	t.Parallel()
	c := newLimitCase(t)
	for i := 0; i < 2; i++ {
		c.wantPending(t, StopKindVerifyUncertain, true, true) // スーパーバイザーが閉じる（release の answered_by = supervisor）
		c.act(t, StopKindVerifyUncertain, HandReverify, ActionExecuted, "", c.task)
	}
	c.wantPending(t, StopKindVerifyUncertain, true, false)
}

func TestHoldLimit_AC47_to_50_AC197_UncountedActions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		hand   SupervisorHand
		result SupervisorActionResult
		reason SupervisorRejectReason
	}{
		{"AC-47 defer", HandDefer, ActionExecuted, ""},
		{"AC-48 escalate", HandEscalate, ActionExecuted, ""},
		{"AC-49 failed", HandReverify, ActionFailed, ""},
		{"AC-50 rejected state_changed", HandReverify, ActionRejected, RejectStateChanged},
		{"AC-197 rejected owner_answered", HandReverify, ActionRejected, RejectOwnerAnswered},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newLimitCase(t)
			c.act(t, StopKindVerifyUncertain, tc.hand, tc.result, tc.reason, c.task)
			c.act(t, StopKindVerifyUncertain, tc.hand, tc.result, tc.reason, c.task)
			c.wantPending(t, StopKindVerifyUncertain, true, true)
		})
	}
}

func TestHoldLimit_AC51_HoldsWithoutATaskCountPerChallenge(t *testing.T) {
	t.Parallel()
	c := newLimitCase(t)
	c.act(t, StopKindVerifyUncertain, HandReverify, ActionExecuted, "", "")
	c.act(t, StopKindVerifyUncertain, HandRetry, ActionExecuted, "", "")
	c.wantPending(t, StopKindVerifyUncertain, false, false)
	// タスクを指す保留は、タスクの無い対処の記録を数えない。
	c.wantPending(t, StopKindVerifyUncertain, true, true)
}

func TestHoldLimit_NoStopKindIsNeverCounted(t *testing.T) {
	t.Parallel()
	c := newLimitCase(t)
	c.act(t, StopKindVerifyUncertain, HandReverify, ActionExecuted, "", "")
	c.act(t, StopKindVerifyUncertain, HandReverify, ActionExecuted, "", "")
	q, stop, state := c.hold(t, "", false)
	if q != "もとの問い\n2 行目" || stop.Valid || state.Valid {
		t.Errorf("question=%q stop=%v state=%v, want untouched", q, stop, state)
	}
}

// --- タスクの検証の連続の数え（AC-211〜AC-214） ---

func taskStreak(t *testing.T, f *reviewFlowFixture) int64 {
	t.Helper()
	return f.count(t, `SELECT verify_not_met_streak FROM task WHERE id = 1`)
}

// reverifyTask は足場: タスクを検証中へ戻して、もう一度タスクの検証を回せるようにする。
func (f *reviewFlowFixture) reverifyTask(t *testing.T) {
	t.Helper()
	f.sql(t, `UPDATE task SET status = 'verifying' WHERE id = 1`)
}

func TestTaskVerifyStreak_AC211_212_CountsNotMetAndResetsOnMet(t *testing.T) {
	t.Parallel()
	f := newTaskVerifyFixture(t)
	f.j5.agent.ReworkLimit = 5
	f.setVerdict("not_met")
	for want := 1; want <= 2; want++ {
		if _, err := f.verifyTasks(t, nil); err != nil {
			t.Fatal(err)
		}
		if got := taskStreak(t, f); got != int64(want) {
			t.Fatalf("streak = %d, want %d", got, want)
		}
		f.reverifyTask(t)
	}
	f.setVerdict("met")
	if _, err := f.verifyTasks(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := taskStreak(t, f); got != 0 {
		t.Errorf("streak after met = %d, want 0", got)
	}
}

func TestTaskVerifyStreak_AC213_214_LimitHoldsWithoutSendingBack(t *testing.T) {
	t.Parallel()
	f := newTaskVerifyFixture(t)
	f.j5.agent.ReworkLimit = 2
	f.setVerdict("not_met")
	if _, err := f.verifyTasks(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t, f.cid); got != StatusInProgress {
		t.Fatalf("after the 1st not_met: status = %s, want in_progress (sent back)", got)
	}
	f.reverifyTask(t)
	if _, err := f.verifyTasks(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Fatalf("at the limit: status = %s, want awaiting_human", got) // AC-213
	}
	if got := f.text(t, `SELECT stop_kind || '/' || supervisor_state || '/' || task_id FROM hold WHERE challenge_id = 1`); got != "rework_limit/pending/1" {
		t.Errorf("hold = %q, want rework_limit/pending/1", got) // AC-214
	}
	if got := f.text(t, `SELECT question FROM hold WHERE challenge_id = 1`); !strings.Contains(got, "差し戻しの上限") {
		t.Errorf("question = %q", got)
	}
	// 委譲先へは差し戻していない（状態の確認で respond が返らない）。
	f.client.echoRunning = true
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f.poll(t)
	if len(f.client.resumes) != 0 {
		t.Errorf("resumes = %v, want none", f.client.resumes)
	}
}

func TestTaskVerifyStreak_SupervisorDisabledLeavesTheHoldForTheOwner(t *testing.T) {
	t.Parallel()
	f := newTaskVerifyFixture(t)
	f.j5.agent.ReworkLimit = 1
	f.j5.agent.Supervisor = false
	f.setVerdict("not_met")
	if _, err := f.verifyTasks(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.text(t, `SELECT stop_kind || '/' || COALESCE(supervisor_state, 'NULL') FROM hold WHERE challenge_id = 1`); got != "rework_limit/NULL" {
		t.Errorf("hold = %q, want rework_limit/NULL", got)
	}
}

// 3 件以上あっても、問いの先頭には先頭の 2 件の手だけを並べる。手が無い記録（判断できなかった）も数え、「判断できなかった」と書く。
func TestHoldLimit_NoticeListsTheFirstTwoHandsAndNamesAMissingHand(t *testing.T) {
	t.Parallel()
	c := newLimitCase(t)
	c.act(t, StopKindFactsUnmet, "", ActionExecuted, "", c.task)
	c.act(t, StopKindFactsUnmet, HandRetry, ActionExecuted, "", c.task)
	c.act(t, StopKindFactsUnmet, HandReplan, ActionExecuted, "", c.task)
	q, _, state := c.hold(t, StopKindFactsUnmet, true)
	if want := "スーパーバイザーが 2 回対処したが、進まなかった（手: 判断できなかった、retry）。\n"; !strings.HasPrefix(q, want) || state.Valid {
		t.Errorf("question = %q state = %v", q, state)
	}
}
