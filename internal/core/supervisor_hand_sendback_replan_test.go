package core

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// このファイルは区切り S4b-2 の手 send_back・replan（AC-132〜140・147〜151・184〜189）を、実ストアで検証する。
// 手は applySupervisorHand（対処の記録・作業ログ・手の書き込みを 1 つの書き込みで行う入口）を通して実行する。

const (
	sbOldTime  = "2026-11-01T00:00:00.000Z"
	sbActTime  = "2026-11-22T00:00:00.000Z"
	sbNextTime = "2026-11-22T00:00:01.000Z"
)

// sbFixture は、課題・タスク・開いているレビュー待ちの契約の実行を 1 つずつ持つ。
type sbFixture struct {
	*offeredFixture
}

func newSBFixture(t *testing.T) *sbFixture {
	t.Helper()
	f := &sbFixture{offeredFixture: newOfferedFixture(t)}
	f.exec2(t, `UPDATE run SET started_at = ? WHERE kind = 'contract'`, sbOldTime)
	return f
}

// apply は、保留 tg に手 hand を、判断の出力 note つきで実行する。
func (f *sbFixture) apply(t *testing.T, tg supervisorTarget, hand SupervisorHand, note string) supervisorHandOutcome {
	t.Helper()
	out, err := f.s.applySupervisorHand(context.Background(), supervisorHandRequest{
		CycleID: f.cycle(t, 300), Target: tg,
		Output: SVOutput{Hand: hand, Findings: "FINDINGS-SB", Note: note, Recommendation: "RECOMMEND"}, Decl: f.agent,
	})
	if err != nil {
		t.Fatalf("applySupervisorHand: %v", err)
	}
	return out
}

func (f *sbFixture) str(t *testing.T, q string, args ...any) string {
	t.Helper()
	var v sql.NullString
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(q, args...).Scan(&v) }); err != nil {
		t.Fatal(err)
	}
	return v.String
}

func (f *sbFixture) reply(t *testing.T) *reviewReply {
	t.Helper()
	id := f.count(t, `SELECT id FROM contract_run`)
	row, err := f.s.loadContractRunRow(context.Background(), id)
	if err != nil || row == nil {
		t.Fatalf("loadContractRunRow: %v", err)
	}
	pv := int64(1)
	rr, err := f.s.pendingReviewReply(context.Background(), row, &pv, f.agent)
	if err != nil {
		t.Fatalf("pendingReviewReply: %v", err)
	}
	return rr
}

// リプライが 1 回の respond で、指摘に want を含むこと。
func (f *sbFixture) wantRespond(t *testing.T, want ...string) {
	t.Helper()
	rr := f.reply(t)
	if rr == nil || rr.Answer == nil || rr.Answer.Choice != contractChoiceRespond {
		t.Fatalf("reply = %+v, want a respond", rr)
	}
	for _, w := range want {
		if !strings.Contains(rr.Answer.Note, w) {
			t.Errorf("respond note %q lacks %q", rr.Answer.Note, w)
		}
	}
}

// AC-132〜135・138: send_back は保留を閉じ、課題とタスクを着手中にし、次の回りで note つきの respond が 1 回だけ返る。
func TestSupervisorSendBack_ClosesTheHoldAndDeliversTheNoteOnce(t *testing.T) {
	t.Parallel()
	for _, kind := range []StopKind{StopKindVerifyUncertain, StopKindFactsUnmet, StopKindReworkLimit} {
		kind := kind
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			f := newSBFixture(t)
			if kind == StopKindReworkLimit {
				for i := 0; i < f.agent.ReworkLimit; i++ {
					notMetRun(t, f.s, f.cidInt, f.taskID, "J5-FEEDBACK")
				}
			}
			tg := f.hold(t, StatusInProgress, kind, true)
			f.exec2(t, `UPDATE task SET status = 'verifying' WHERE id = ?`, f.taskID)
			if out := f.apply(t, tg, HandSendBack, "NOTE-SB"); out.Result != ActionExecuted {
				t.Fatalf("outcome = %+v, want executed", out)
			}
			if got := f.status(t, f.cid); got != StatusInProgress {
				t.Errorf("challenge status = %v, want in_progress", got) // AC-132
			}
			if got := f.str(t, `SELECT status FROM task WHERE id = ?`, f.taskID); got != string(TaskStatusInProgress) {
				t.Errorf("task status = %q, want in_progress", got) // AC-133
			}
			if got := f.str(t, `SELECT supervisor_state FROM hold WHERE id = ?`, tg.HoldID); got != "handled" {
				t.Errorf("hold state = %q, want handled", got)
			}
			f.exec2(t, `UPDATE supervisor_action SET created_at = ?`, sbActTime)
			f.wantRespond(t, "NOTE-SB") // AC-134・135・136（rework_limit は上限に当たっていても 1 回返る）
			if kind == StopKindReworkLimit {
				rr := f.reply(t)
				if rr == nil || rr.Answer == nil {
					t.Fatalf("reply = %+v, want a respond", rr)
				}
				if !strings.Contains(rr.Answer.Note, "J5-FEEDBACK") || strings.Count(rr.Answer.Note, "NOTE-SB") != 1 || strings.Contains(rr.Answer.Note, "FINDINGS-SB") {
					t.Errorf("note = %q, want the J5 feedback and the supervisor note once", rr.Answer.Note)
				}
			}
			// 返した後（契約のコマンドが、対処の記録より後に始まった）は、同じ note では返さない。AC-138
			f.exec2(t, `INSERT INTO run (challenge_id, kind, challenge_version, plan_version, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket, cost_usd, task_id)
				VALUES (?, 'contract', 1, 1, 1, 'h', ?, ?, ?, 'succeeded', 0, 'impl', 0, ?)`, f.cidInt, sbNextTime, sbNextTime, sbNextTime, f.taskID)
			rr := f.reply(t)
			if rr != nil && rr.Answer != nil && strings.Contains(rr.Answer.Note, "NOTE-SB") {
				t.Errorf("reply = %+v, want the same note not to be returned twice", rr)
			}
		})
	}
}

// 契約の実行の開始と send_back の記録が同じ時刻（ミリ秒）のときは、順序が決まらないので「まだ届けていない」側に倒し、文を返す
// （届け漏れより、同じ文をもう 1 回届けるほうが安全）。
func TestSupervisorSendBack_SameTimeAsTheContractRunCountsAsUndelivered(t *testing.T) {
	t.Parallel()
	f := newSBFixture(t)
	tg := f.hold(t, StatusInProgress, StopKindVerifyUncertain, true)
	f.exec2(t, `UPDATE task SET status = 'verifying' WHERE id = ?`, f.taskID)
	if out := f.apply(t, tg, HandSendBack, "NOTE-SB"); out.Result != ActionExecuted {
		t.Fatalf("outcome = %+v, want executed", out)
	}
	f.exec2(t, `UPDATE supervisor_action SET created_at = ?`, sbActTime)
	f.exec2(t, `INSERT INTO run (challenge_id, kind, challenge_version, plan_version, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket, cost_usd, task_id)
		VALUES (?, 'contract', 1, 1, 1, 'h', ?, ?, ?, 'succeeded', 0, 'impl', 0, ?)`, f.cidInt, sbActTime, sbActTime, sbActTime, f.taskID)
	f.wantRespond(t, "NOTE-SB")
}

// AC-137・139: rework_limit を send_back で閉じた後も、差し戻しの数えは数え直されない（send_back は 1 回に数える）。
// 差し戻しを 1 回通した後の次の「満たしていない」は、差し戻さず、また rework_limit の保留になる。
func TestSupervisorSendBack_ReworkLimitIsNotRecountedAndTheSendBackCounts(t *testing.T) {
	t.Parallel()
	f := newSBFixture(t)
	for i := 0; i < f.agent.ReworkLimit; i++ {
		notMetRun(t, f.s, f.cidInt, f.taskID, "J5-FEEDBACK")
	}
	count := func() int {
		var got int
		if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
			h, err := loadLaunchHistory(context.Background(), tx, f.cidInt)
			if err != nil {
				return err
			}
			_, hit := h.forTask(formatTaskID(f.taskID)).decideRework(1, f.agent.ReworkLimit)
			if hit != nil {
				got = hit.Count
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := count(); got != f.agent.ReworkLimit {
		t.Fatalf("count before = %d, want %d", got, f.agent.ReworkLimit)
	}
	tg := f.hold(t, StatusInProgress, StopKindReworkLimit, true)
	f.apply(t, tg, HandSendBack, "NOTE-SB")
	f.exec2(t, `UPDATE supervisor_action SET created_at = ?`, sbActTime)
	f.wantRespond(t, "NOTE-SB")
	// 返した後に、次の「満たしていない」が来る。
	f.exec2(t, `INSERT INTO run (challenge_id, kind, challenge_version, plan_version, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket, cost_usd, task_id)
		VALUES (?, 'contract', 1, 1, 1, 'h', ?, ?, ?, 'succeeded', 0, 'impl', 0, ?)`, f.cidInt, sbNextTime, sbNextTime, sbNextTime, f.taskID)
	// 差し戻しを返した直後は、次の「満たしていない」が来るまで、上限の保留にしない。
	if rr := f.reply(t); rr != nil && rr.HoldQuestion != "" {
		t.Fatalf("reply = %+v, want no hold before the next not_met", rr)
	}
	notMetRun(t, f.s, f.cidInt, f.taskID, "J5-FEEDBACK-2")
	f.exec2(t, `UPDATE run SET started_at = ? WHERE judgment = 'J5' AND id = (SELECT MAX(id) FROM run)`, sbNextTime)
	// 数えは、J5 の「満たしていない」の 3 回と、その後の 1 回に、send_back の 1 回を足した数（スーパーバイザーの回答では数え直さない）。AC-139
	if got := count(); got != f.agent.ReworkLimit+2 {
		t.Errorf("count = %d, want %d", got, f.agent.ReworkLimit+2)
	}
	rr := f.reply(t)
	if rr == nil || rr.HoldQuestion == "" || rr.Answer != nil {
		t.Fatalf("reply = %+v, want a rework_limit hold, not a send back", rr) // AC-137
	}
}

// AC-140: send_back は、タスクの検証の連続の「満たしていない」の数えを変えない。
func TestSupervisorSendBack_DoesNotChangeTheVerifyNotMetStreak(t *testing.T) {
	t.Parallel()
	f := newSBFixture(t)
	f.exec2(t, `UPDATE task SET verify_not_met_streak = 2 WHERE id = ?`, f.taskID)
	if out := f.apply(t, f.hold(t, StatusInProgress, StopKindVerifyUncertain, true), HandSendBack, "n"); out.Result != ActionExecuted {
		t.Fatalf("outcome = %+v, want executed", out)
	}
	if got := f.str(t, `SELECT verify_not_met_streak FROM task WHERE id = ?`, f.taskID); got != "2" {
		t.Errorf("verify_not_met_streak = %q, want 2", got)
	}
}

// facts_unmet の保留の回答（スーパーバイザーの回答の文を含む）を、facts_unmet の読み手が、send_back の指摘に重ねて返さない。
func TestSupervisorSendBack_FactsUnmetAnswerReaderDoesNotDoubleTheNote(t *testing.T) {
	t.Parallel()
	f := newSBFixture(t)
	f.apply(t, f.hold(t, StatusInProgress, StopKindFactsUnmet, true), HandSendBack, "NOTE-SB")
	f.exec2(t, `UPDATE supervisor_action SET created_at = ?`, sbActTime)
	f.wantRespond(t, "NOTE-SB")
	rr := f.reply(t)
	if n := strings.Count(rr.Answer.Note, "NOTE-SB"); n != 1 {
		t.Errorf("note %q has NOTE-SB %d times, want once", rr.Answer.Note, n)
	}
	// 返した後は、facts_unmet の読み手（undeliveredFactsUnmetAnswer）も、同じ回答を返さない。
	f.exec2(t, `UPDATE hold SET reason = 'facts_unmet'`)
	f.exec2(t, `INSERT INTO run (challenge_id, kind, challenge_version, plan_version, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket, cost_usd, task_id)
		VALUES (?, 'contract', 1, 1, 1, 'h', ?, ?, ?, 'succeeded', 0, 'impl', 0, ?)`, f.cidInt, sbNextTime, sbNextTime, sbNextTime, f.taskID)
	f.exec2(t, `UPDATE hold SET answered_at = ?`, sbActTime)
	got, err := f.s.undeliveredFactsUnmetAnswer(context.Background(), f.cidInt, &f.taskID, "R-99999")
	if err != nil || got != "" {
		t.Errorf("undeliveredFactsUnmetAnswer = %q (%v), want empty after the send back was delivered", got, err)
	}
}

// AC-186・187・188・189: 保留が指すタスクの契約の実行が終わっていた send_back は、手を実行せず、rejected・state_changed で残し、保留を escalated にして findings を残す。
func TestSupervisorSendBack_RejectedWhenTheContractRunEnded(t *testing.T) {
	t.Parallel()
	f := newSBFixture(t)
	tg := f.hold(t, StatusInProgress, StopKindVerifyUncertain, true)
	f.exec2(t, `UPDATE task SET status = 'verifying' WHERE id = ?`, f.taskID)
	f.setRun(t, `state = 'cancelled', ended_at = '2026-11-22T00:00:00.000Z'`)
	out := f.apply(t, tg, HandSendBack, "n")
	f.wantStateChangedEscalated(t, tg, out)
	if got := f.str(t, `SELECT status FROM task WHERE id = ?`, f.taskID); got == string(TaskStatusInProgress) {
		t.Errorf("task status = %q, want untouched", got)
	}
}

func (f *sbFixture) wantStateChangedEscalated(t *testing.T, tg supervisorTarget, out supervisorHandOutcome) {
	t.Helper()
	if out.Result != ActionRejected || out.Reject != RejectStateChanged {
		t.Fatalf("outcome = %+v, want rejected state_changed", out)
	}
	if got := f.str(t, `SELECT answer FROM hold WHERE id = ?`, tg.HoldID); got != "" {
		t.Errorf("hold answer = %q, want none (the hand was not executed)", got)
	}
	if got := f.str(t, `SELECT result FROM supervisor_action WHERE hold_id = ?`, tg.HoldID); got != "rejected" {
		t.Errorf("action result = %q, want rejected", got)
	}
	if got := f.str(t, `SELECT reject_reason FROM supervisor_action WHERE hold_id = ?`, tg.HoldID); got != "state_changed" {
		t.Errorf("reject_reason = %q, want state_changed", got)
	}
	if got := f.str(t, `SELECT supervisor_state FROM hold WHERE id = ?`, tg.HoldID); got != "escalated" {
		t.Errorf("supervisor_state = %q, want escalated", got)
	}
	if got := f.str(t, `SELECT supervisor_findings FROM hold WHERE id = ?`, tg.HoldID); !strings.Contains(got, "FINDINGS-SB") {
		t.Errorf("supervisor_findings = %q, want the findings", got)
	}
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Errorf("challenge status = %v, want awaiting_human (unchanged)", got)
	}
}

// AC-184・185・187・188・189: 作業している契約の実行ができていた課題への replan は、手を実行せず、rejected・state_changed で残し、保留を上げる。
func TestSupervisorReplan_RejectedWhenAContractRunIsWorking(t *testing.T) {
	t.Parallel()
	f := newSBFixture(t)
	tg := f.hold(t, StatusInProgress, StopKindDelegateStopped, true)
	f.setRun(t, `state = 'running', requested_kind = NULL, ended_at = NULL`)
	out := f.apply(t, tg, HandReplan, "n")
	f.wantStateChangedEscalated(t, tg, out)
}

// AC-107・147・150・151: replan は計画を作り直す回答と同じ印で保留を閉じ、課題は分類済になる。承認の記録は作らない。
func TestSupervisorReplan_ClosesTheHoldWithTheReplanMarkWithoutAnApproval(t *testing.T) {
	t.Parallel()
	f := newSBFixture(t)
	tg := f.hold(t, StatusInProgress, StopKindDelegateStopped, true)
	var approvals int64
	approvalCount := func() int64 {
		return f.count(t, `SELECT COUNT(*) FROM approval`)
	}
	approvals = approvalCount()
	if out := f.apply(t, tg, HandReplan, "NOTE-RP"); out.Result != ActionExecuted {
		t.Fatalf("outcome = %+v, want executed", out)
	}
	if got := f.str(t, `SELECT replan FROM hold WHERE id = ?`, tg.HoldID); got != "1" {
		t.Errorf("hold.replan = %q, want 1", got)
	}
	if got := f.str(t, `SELECT answered_by FROM hold WHERE id = ?`, tg.HoldID); got != "supervisor" {
		t.Errorf("answered_by = %q, want supervisor", got)
	}
	// AC-107: replan で閉じた保留も、ほかの閉じる手と同じに supervisor_state は handled になる。
	if got := f.str(t, `SELECT supervisor_state FROM hold WHERE id = ?`, tg.HoldID); got != "handled" {
		t.Errorf("hold state = %q, want handled", got)
	}
	if got := f.status(t, f.cid); got != StatusClassified {
		t.Errorf("status = %v, want classified (the same place as the answer with the replan mark)", got)
	}
	if got := approvalCount(); got != approvals {
		t.Errorf("approvals = %d, want %d (the supervisor does not approve)", got, approvals)
	}
}

// AC-148・149・150・151: replan の後の回りで計画の判断点が呼ばれ、入力の「保留の記録」の区画にスーパーバイザーの note がある。
// 作り直した計画の課題は計画の承認待ちになり、承認の記録は、オーナーが承認するまで増えない。
func TestSupervisorReplan_NextCycleRunsTheJ2WithTheNoteAndWaitsForThePlanApproval(t *testing.T) {
	t.Parallel()
	f := newPAFixture(t, 2, true)
	if err := f.approve(t, ttyAtt(t, f.id)); err != nil {
		t.Fatal(err)
	}
	cid, _ := parseChallengeID(f.id)
	if _, err := f.s.HoldChallenge(ctx0, ChannelCLI, f.id, HoldInput{Question: "進め方が分からない"}); err != nil {
		t.Fatalf("hold: %v", err)
	}
	var hid int64
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE hold SET stop_kind = 'delegate_stopped', supervisor_state = 'pending' WHERE challenge_id = ?`, cid); err != nil {
			return err
		}
		return tx.QueryRow(`SELECT id FROM hold WHERE challenge_id = ?`, cid).Scan(&hid)
	})
	cyc, err := f.s.BeginCycle(ctx0, BeginCycleInput{Trigger: "run", BudgetUSD: 300})
	if err != nil {
		t.Fatal(err)
	}
	approvals := func() (n int) {
		readForTest(t, f.s, func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT COUNT(*) FROM approval WHERE challenge_id = ?`, cid).Scan(&n)
		})
		return
	}
	before := approvals()
	out, err := f.s.applySupervisorHand(ctx0, supervisorHandRequest{
		CycleID: cyc.ID, Target: supervisorTarget{HoldID: hid, ChallengeID: cid, StopKind: StopKindDelegateStopped},
		Output: SVOutput{Hand: HandReplan, Findings: "FINDINGS-RP", Note: "NOTE-RP"}, Decl: f.agent,
	})
	if err != nil || out.Result != ActionExecuted {
		t.Fatalf("replan: %+v (%v)", out, err)
	}
	in, res := f.j2Input(t, paPlanOut(2, true))
	if len(res.Items) != 1 || res.Items[0].Outcome != "plan" {
		t.Fatalf("items = %+v, want the J2 to make a plan", res.Items)
	}
	sec, ok := j2SectionText(in, labelHoldRecord)
	if !ok || !strings.Contains(sec, "NOTE-RP") || !strings.Contains(sec, "FINDINGS-RP") {
		t.Errorf("the hold record section = %q (found %v), want the supervisor's note", sec, ok) // AC-149
	}
	if got := f.detail(t, f.id).Status; got != StatusAwaitingPlanApproval {
		t.Errorf("status = %s, want awaiting_plan_approval", got) // AC-150
	}
	if got := approvals(); got != before {
		t.Errorf("approvals = %d, want %d until the owner approves", got, before) // AC-151
	}
}
