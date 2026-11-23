package core

// このファイルは、区切り S4c-3 の「スーパーバイザー待ちの保留を保留ごとに扱う」（AC-26・56〜59・86〜93・116・131）を、実ストアで検証する。

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// svPending は保留 hold をスーパーバイザー待ち（止まり方 kind）にする。
func (f *holdScopeFixture) svPending(t *testing.T, hold int64, kind StopKind) {
	t.Helper()
	f.mustExec(t, `UPDATE hold SET supervisor_state = 'pending', stop_kind = ? WHERE id = ?`, string(kind), hold)
}

// svHand は保留 hold を対象に手 out を実行する（スーパーバイザーの段が呼ぶ入口）。
func (f *holdScopeFixture) svHand(t *testing.T, hold int64, kind StopKind, taskID *int64, out SVOutput) supervisorHandOutcome {
	t.Helper()
	var cyc int64
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO cycle (trigger, budget_usd, started_at) VALUES ('manual', 0, '2026-10-24T00:00:00.000Z')`)
		if err != nil {
			return err
		}
		cyc, _ = res.LastInsertId()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	o, err := f.s.applySupervisorHand(context.Background(), supervisorHandRequest{
		CycleID: formatCycleID(cyc), Target: supervisorTarget{HoldID: hold, ChallengeID: f.cid, TaskID: taskID, StopKind: kind},
		Output: out, Decl: f.agent,
	})
	if err != nil {
		t.Fatalf("applySupervisorHand: %v", err)
	}
	return o
}

// AC-26: タスク a を止める保留が開いている間、タスク b の委譲先の問いは、スーパーバイザーが受ける（判断点が呼ばれる）。
func TestSupervisorHoldEach_AC26_ChildQuestionOfAnotherTaskGoesToTheSupervisor(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	f.waitHold(t, f.runA)
	if got := f.challengeStatus(t); got != string(StatusAwaitingHuman) {
		t.Fatalf("status = %q, want awaiting_human", got)
	}
	f.agent.Supervisor = true
	r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(svEscalateJSON())}}
	route, err := f.s.routeChildQuestions(context.Background(), childRouteParams{
		Decl: f.agent, Judgment: r, CycleID: f.cycle(t, 300), ChallengeID: f.cid, TaskID: &f.taskB,
		Question: "Q", Questions: []ChildQuestion{{Question: "Q"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.svSeen) != 1 || route.Escalation == nil {
		t.Errorf("supervisor calls = %d, route = %+v, want 1 call and an escalation", len(r.svSeen), route)
	}
	// 保留が開いているタスク a の問いは受けない（そのタスクは進めてよくない）。
	r2 := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(svEscalateJSON())}}
	if _, err := f.s.routeChildQuestions(context.Background(), childRouteParams{
		Decl: f.agent, Judgment: r2, CycleID: f.cycle(t, 301), ChallengeID: f.cid, TaskID: &f.taskA,
		Question: "Q", Questions: []ChildQuestion{{Question: "Q"}},
	}); err != nil {
		t.Fatal(err)
	}
	if len(r2.svSeen) != 0 {
		t.Errorf("supervisor calls for the held task = %d, want 0", len(r2.svSeen))
	}
}

// AC-56・57・131: 保留が 2 つ開いた課題で片方を閉じても人間対応待ちのまま。最後の 1 つを閉じると戻り先へ戻る。作業ログに保留の ID が残る。
func TestSupervisorHoldEach_AC56_AC57_AC131_ResolveKeepsAwaitingHumanUntilTheLastHold(t *testing.T) {
	t.Parallel()
	f := newTwoHoldsFixture(t, HoldOptionsInput{})
	f.svPending(t, 1, StopKindDelegateStopped)
	f.svPending(t, 2, StopKindDelegateStopped)
	if o := f.svHand(t, 1, StopKindDelegateStopped, &f.taskA, SVOutput{Hand: HandRetry, Findings: "f"}); o.Result != ActionExecuted {
		t.Fatalf("outcome = %+v, want executed", o)
	}
	if got := f.challengeStatus(t); got != string(StatusAwaitingHuman) {
		t.Errorf("status = %q, want awaiting_human while the other hold is open (AC-56)", got)
	}
	if o := f.svHand(t, 2, StopKindDelegateStopped, &f.taskB, SVOutput{Hand: HandRetry, Findings: "f"}); o.Result != ActionExecuted {
		t.Fatalf("outcome = %+v, want executed", o)
	}
	if got := f.challengeStatus(t); got != string(StatusInProgress) {
		t.Errorf("status = %q, want in_progress after the last hold (AC-57)", got)
	}
	for _, id := range []string{"H-1", "H-2"} {
		if n := f.q(t, `SELECT COUNT(*) FROM activity WHERE action = 'supervisor_resolve' AND after LIKE ?`, `%"hold_id":"`+id+`"%`); n != "1" {
			t.Errorf("supervisor_resolve log lines with hold_id %s = %s, want 1 (AC-131)", id, n)
		}
	}
}

// AC-58: 課題が人間対応待ちで保留が 2 つ開いていても、スーパーバイザー待ちの保留への手は実行される（reverify・send_back 以外の retry で確かめる）。
func TestSupervisorHoldEach_AC58_HandRunsWhileTheChallengeIsAwaitingHuman(t *testing.T) {
	t.Parallel()
	f := newTwoHoldsFixture(t, HoldOptionsInput{})
	f.svPending(t, 2, StopKindDelegateStopped)
	if got := f.challengeStatus(t); got != string(StatusAwaitingHuman) {
		t.Fatalf("status = %q, want awaiting_human", got)
	}
	o := f.svHand(t, 2, StopKindDelegateStopped, &f.taskB, SVOutput{Hand: HandRetry, Findings: "f"})
	if o.Result != ActionExecuted {
		t.Errorf("outcome = %+v, want executed", o)
	}
	if got := f.q(t, `SELECT result FROM supervisor_action WHERE hold_id = 2`); got != "executed" {
		t.Errorf("recorded result = %q, want executed", got)
	}
}

// AC-59: 判断の間に対象の保留が閉じていたら、手は実行されず、対処の記録は rejected になる。
func TestSupervisorHoldEach_AC59_HoldClosedDuringTheJudgmentIsRejected(t *testing.T) {
	t.Parallel()
	for _, hand := range []SupervisorHand{HandRetry, HandEscalate, HandDefer} {
		t.Run(string(hand), func(t *testing.T) {
			f := newTwoHoldsFixture(t, HoldOptionsInput{})
			f.svPending(t, 2, StopKindDelegateStopped)
			f.mustExec(t, `UPDATE hold SET closed_at = '2026-10-24T00:00:00.000Z' WHERE id = 2`)
			o := f.svHand(t, 2, StopKindDelegateStopped, &f.taskB, SVOutput{Hand: hand, Findings: "f", Recommendation: "r"})
			if o.Result != ActionRejected || o.Reject != RejectStateChanged {
				t.Errorf("outcome = %+v, want rejected state_changed", o)
			}
			if got := f.q(t, `SELECT result FROM supervisor_action WHERE hold_id = 2`); got != "rejected" {
				t.Errorf("recorded result = %q, want rejected", got)
			}
			if got := f.challengeStatus(t); got != string(StatusAwaitingHuman) {
				t.Errorf("status = %q, want untouched", got)
			}
		})
	}
}

// AC-86〜88: タスク b の実行が動いている間、タスク a の保留への計画を作り直す回答は、検証の誤りで断られ、動いているタスクの題名と ID が出る。
func TestSupervisorHoldEach_AC86_AC87_AC88_ReplanAnswerIsRefusedWhileAnotherTaskRuns(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	f.waitHold(t, f.runA)
	p, err := f.s.PrepareAnswerHold(context.Background(), f.id, "H-1", "作り直す", "", true)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.s.ExecuteAnswer(context.Background(), AnswerRequest{
		ChallengeID: f.id, ExpectedVersion: p.Version, Answer: "作り直す", Replan: true,
		HoldID: p.HoldID, HoldQuestion: p.Question, HoldOptions: p.Options, HoldOptionRequired: p.OptionRequired,
	}, verifiedAttestationForTest(t, f.id))
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want a validation error", err)
	}
	title := f.q(t, `SELECT title FROM task WHERE id = ?`, f.taskB)
	if !strings.Contains(err.Error(), title) {
		t.Errorf("err %q lacks the running task title %q (AC-87)", err, title)
	}
	if !strings.Contains(err.Error(), formatTaskID(f.taskB)) {
		t.Errorf("err %q lacks the running task id %s (AC-88)", err, formatTaskID(f.taskB))
	}
	if got := f.openHolds(t); got != "1" {
		t.Errorf("open holds = %s, want 1 (nothing written)", got)
	}
}

// stopRuns は、課題の契約の実行を、動いていない（終わった）状態にする。
func (f *holdScopeFixture) stopRuns(t *testing.T) {
	t.Helper()
	f.mustExec(t, `UPDATE contract_run SET state = 'cancelled', ended_at = '2026-10-24T00:00:00.000Z' WHERE challenge_id = ?`, f.cid)
}

// AC-89〜91: 動いている作業が無く保留が 2 つ開いた課題で、片方へ計画を作り直す回答をすると、課題は分類済みへ戻り、もう片方は回答なしで閉じ、理由が作業ログに残る。
func TestSupervisorHoldEach_AC89_AC90_AC91_ReplanAnswerClosesTheOtherHold(t *testing.T) {
	t.Parallel()
	f := newTwoHoldsFixture(t, HoldOptionsInput{})
	f.stopRuns(t)
	p, err := f.s.PrepareAnswerHold(context.Background(), f.id, "H-1", "作り直す", "", true)
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{
		ChallengeID: f.id, ExpectedVersion: p.Version, Answer: "作り直す", Replan: true,
		HoldID: p.HoldID, HoldQuestion: p.Question, HoldOptions: p.Options, HoldOptionRequired: p.OptionRequired,
	}, verifiedAttestationForTest(t, f.id))
	if err != nil {
		t.Fatalf("ExecuteAnswer: %v", err)
	}
	if c.Status != StatusClassified {
		t.Errorf("status = %q, want classified (AC-89)", c.Status)
	}
	if got := f.q(t, `SELECT COALESCE(answer, '<none>') || '/' || (closed_at IS NOT NULL) FROM hold WHERE id = 2`); got != "<none>/1" {
		t.Errorf("hold 2 = %q, want closed without an answer (AC-90)", got)
	}
	if got := f.openHolds(t); got != "0" {
		t.Errorf("open holds = %s, want 0", got)
	}
	log := f.q(t, `SELECT after FROM activity WHERE action = 'answer' ORDER BY id DESC LIMIT 1`)
	if !strings.Contains(log, `"closed_holds":["H-2"]`) || !strings.Contains(log, "closed_holds_reason") {
		t.Errorf("answer log = %s, want closed_holds H-2 and a reason (AC-91)", log)
	}
}

// AC-92: タスク b の実行が動いている間、スーパーバイザーの手「計画を作り直す」は実行できない状態として扱われる（rejected）。
func TestSupervisorHoldEach_AC92_ReplanHandIsRejectedWhileAnotherTaskRuns(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	f.waitHold(t, f.runA)
	f.svPending(t, 1, StopKindReworkLimit)
	o := f.svHand(t, 1, StopKindReworkLimit, &f.taskA, SVOutput{Hand: HandReplan, Findings: "f"})
	if o.Result != ActionRejected || o.Reject != RejectStateChanged {
		t.Fatalf("outcome = %+v, want rejected state_changed", o)
	}
	if got := f.q(t, `SELECT result FROM supervisor_action WHERE hold_id = 1 ORDER BY id LIMIT 1`); got != "rejected" {
		t.Errorf("recorded result = %q, want rejected", got)
	}
	if got := f.challengeStatus(t); got != string(StatusAwaitingHuman) {
		t.Errorf("status = %q, want awaiting_human (untouched)", got)
	}
}

// AC-93: 動いている作業が無く保留が 2 つ開いた課題で、手「計画を作り直す」が成立すると、もう片方の保留は回答なしで閉じる。
func TestSupervisorHoldEach_AC93_ReplanHandClosesTheOtherHold(t *testing.T) {
	t.Parallel()
	f := newTwoHoldsFixture(t, HoldOptionsInput{})
	f.stopRuns(t)
	f.svPending(t, 1, StopKindReworkLimit)
	o := f.svHand(t, 1, StopKindReworkLimit, &f.taskA, SVOutput{Hand: HandReplan, Findings: "f"})
	if o.Result != ActionExecuted {
		t.Fatalf("outcome = %+v, want executed", o)
	}
	if got := f.challengeStatus(t); got != string(StatusClassified) {
		t.Errorf("status = %q, want classified", got)
	}
	if got := f.q(t, `SELECT COALESCE(answer, '<none>') || '/' || (closed_at IS NOT NULL) FROM hold WHERE id = 2`); got != "<none>/1" {
		t.Errorf("hold 2 = %q, want closed without an answer", got)
	}
	log := f.q(t, `SELECT after FROM activity WHERE action = 'supervisor_resolve' ORDER BY id DESC LIMIT 1`)
	if !strings.Contains(log, `"closed_holds":["H-2"]`) || !strings.Contains(log, "closed_holds_reason") {
		t.Errorf("resolve log = %s, want closed_holds H-2 and a reason", log)
	}
}

// AC-116: スーパーバイザーの対処の一覧の要素に、対象の保留の ID（H-<番号>）がある。
func TestSupervisorHoldEach_AC116_ActionListCarriesTheHoldID(t *testing.T) {
	t.Parallel()
	f := newTwoHoldsFixture(t, HoldOptionsInput{})
	f.svPending(t, 2, StopKindDelegateStopped)
	f.svHand(t, 2, StopKindDelegateStopped, &f.taskB, SVOutput{Hand: HandRetry, Findings: "f"})
	var list []SupervisorAction
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		list, err = querySupervisorActions(ctx0, tx, ``)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].HoldID == nil || FormatHoldID(*list[0].HoldID) != "H-2" {
		t.Errorf("actions = %+v, want one with hold H-2", list)
	}
}
