package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// 区切り S4c-1 の 8: 上限を超える足す依頼の保留への回答（add・skip・replan）。実ストアで確かめる。

// overLimitHeld は、上限 14 を超える足す依頼がオーナー待ち（保留つき）の課題。
func overLimitHeld(t *testing.T) *paFixture {
	t.Helper()
	f := expandDoneFixture(t)
	f.expand(t, &expandRecorder{out: overLimitOutput()})
	if st := f.detail(t, f.id).Status; st != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", st)
	}
	return f
}

func (f *paFixture) answerHold(t *testing.T, option string) (*Challenge, error) {
	t.Helper()
	prev, err := f.s.PrepareAnswerOption(context.Background(), f.id, "", option, false)
	if err != nil {
		return nil, err
	}
	c, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: f.id, ExpectedVersion: prev.Version, Option: option, Agent: f.agent, Conn: f.conn},
		relayedAttestation(t, f.id, "sess", "quote"))
	return c, err
}

func (f *paFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	readForTest(t, f.s, func(tx *sql.Tx) error { return tx.QueryRow(query, args...).Scan(&n) })
	return n
}

func (f *paFixture) limitMicros(t *testing.T) int64 {
	t.Helper()
	var l sql.NullInt64
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT budget_limit_micros FROM challenge WHERE id = ?`, f.cid()).Scan(&l)
	})
	return l.Int64
}

func (f *paFixture) budgetApprovals(t *testing.T) int {
	return f.count(t, `SELECT COUNT(*) FROM approval WHERE challenge_id = ? AND kind = 'budget'`, f.cid())
}

func TestHoldAnswerTaskAddition_AddRaisesTheLimitAddsTheTasksAndReturnsToInProgress(t *testing.T) {
	t.Parallel()
	f := overLimitHeld(t)
	approvals := f.budgetApprovals(t)
	c, err := f.answerHold(t, taskAddOptionAdd)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != StatusInProgress {
		t.Errorf("status = %s, want in_progress", c.Status)
	}
	if got := f.limitMicros(t); got != 15_000_000 {
		t.Errorf("limit = %d, want 15000000", got)
	}
	tk := f.taskByKey(t, "c")
	if tk == nil || tk.Status != TaskStatusPending || tk.Title != "続き c" {
		t.Fatalf("task c = %+v, want a pending task from the saved output", tk)
	}
	adds := f.additions(t)
	if len(adds) != 1 || adds[0].State != TaskAdditionApplied || len(adds[0].TaskIDs) != 1 || adds[0].TaskIDs[0] != tk.ID {
		t.Errorf("additions = %+v, want applied pointing at the new task", adds)
	}
	if h, _ := f.lastHold(t); h.Open() || h.AnswerOption == nil || *h.AnswerOption != "add" {
		t.Errorf("hold = %+v, want closed with answer_option add", h)
	}
	if got := f.budgetApprovals(t); got != approvals+1 {
		t.Errorf("budget approvals = %d, want %d", got, approvals+1)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM approval WHERE challenge_id = ? AND kind = 'budget' AND task_id IS NULL AND target_version = (SELECT plan_version FROM task WHERE key = 'c' AND challenge_id = ?)`, f.cid(), f.cid()); n < 1 {
		t.Errorf("no budget approval without a task pointing at the approved plan version")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM activity WHERE entity = 'challenge' AND entity_id = ? AND action = ?`, f.cid(), ActivityActionBudgetLimit); n != 1 {
		t.Errorf("budget_limit activities = %d, want 1", n)
	}
}

func TestHoldAnswerTaskAddition_AddKeepsALimitThatWasAlreadyRaisedAndWritesNoApproval(t *testing.T) {
	t.Parallel()
	f := overLimitHeld(t)
	coretest.Exec(t, f.s.Workspace(), `UPDATE challenge SET budget_limit_micros = 20000000 WHERE id = ?`, f.cid())
	approvals := f.budgetApprovals(t)
	if _, err := f.answerHold(t, taskAddOptionAdd); err != nil {
		t.Fatal(err)
	}
	if got := f.limitMicros(t); got != 20_000_000 {
		t.Errorf("limit = %d, want 20000000 (not lowered)", got)
	}
	if got := f.budgetApprovals(t); got != approvals {
		t.Errorf("budget approvals = %d, want %d (unchanged)", got, approvals)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM activity WHERE entity = 'challenge' AND entity_id = ? AND action = ?`, f.cid(), ActivityActionBudgetLimit); n != 0 {
		t.Errorf("budget_limit activities = %d, want 0", n)
	}
	if f.taskByKey(t, "c") == nil || f.additions(t)[0].State != TaskAdditionApplied {
		t.Errorf("tasks not added or addition not applied")
	}
}

func TestHoldAnswerTaskAddition_SkipDeclinesWithoutAddingOrRaising(t *testing.T) {
	t.Parallel()
	f := overLimitHeld(t)
	approvals := f.budgetApprovals(t)
	c, err := f.answerHold(t, taskAddOptionSkip)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != StatusInProgress || f.taskByKey(t, "c") != nil || f.limitMicros(t) != 14_000_000 || f.budgetApprovals(t) != approvals {
		t.Errorf("status=%s task=%v limit=%d approvals=%d", c.Status, f.taskByKey(t, "c"), f.limitMicros(t), f.budgetApprovals(t))
	}
	if adds := f.additions(t); adds[0].State != TaskAdditionDeclined {
		t.Errorf("addition = %s, want declined", adds[0].State)
	}
}

func TestHoldAnswerTaskAddition_ReplanSupersedesAndReturnsToThePlanningStatus(t *testing.T) {
	t.Parallel()
	f := overLimitHeld(t)
	c, err := f.answerHold(t, taskAddOptionReplan)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != StatusClassified {
		t.Errorf("status = %s, want classified", c.Status)
	}
	if adds := f.additions(t); adds[0].State != TaskAdditionSuperseded {
		t.Errorf("addition = %s, want superseded", adds[0].State)
	}
	if h, _ := f.lastHold(t); !h.Replan {
		t.Errorf("hold.Replan = false, want true")
	}
	if f.taskByKey(t, "c") != nil || f.limitMicros(t) != 14_000_000 {
		t.Errorf("replan must not add tasks or change the limit")
	}
}

func TestHoldAnswerTaskAddition_ReplanWhileATaskRunIsWorkingIsRefusedAndChangesNothing(t *testing.T) {
	t.Parallel()
	f := overLimitHeld(t)
	coretest.InsertTaskContractRun(t, f.s.Workspace(), int(f.cid()), "b", "c", "ext-working", "running")
	before := f.detail(t, f.id)
	_, err := f.answerHold(t, taskAddOptionReplan)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	if tk := f.taskByKey(t, "b"); tk == nil || !strings.Contains(err.Error(), tk.ID) || !strings.Contains(err.Error(), tk.Title) {
		t.Errorf("error %q lacks the working task's id and title", err)
	}
	after := f.detail(t, f.id)
	if after.Status != StatusAwaitingHuman || after.Version != before.Version {
		t.Errorf("status=%s version=%d, want unchanged", after.Status, after.Version)
	}
	if h, _ := f.lastHold(t); !h.Open() {
		t.Errorf("hold was closed")
	}
	if adds := f.additions(t); adds[0].State != TaskAdditionAwaitingOwner {
		t.Errorf("addition = %s, want awaiting_owner", adds[0].State)
	}
}

func TestHoldAnswerTaskAddition_AnAnswerWithoutAnOptionIsRefused(t *testing.T) {
	t.Parallel()
	f := overLimitHeld(t)
	_, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: f.id, ExpectedVersion: f.detail(t, f.id).Version, Answer: "足してください"},
		relayedAttestation(t, f.id, "sess", "quote"))
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	if adds := f.additions(t); adds[0].State != TaskAdditionAwaitingOwner || f.limitMicros(t) != 14_000_000 {
		t.Errorf("addition = %s limit = %d, want unchanged", adds[0].State, f.limitMicros(t))
	}
}

func TestHoldAnswerTaskAddition_AddWhoseSavedOutputNoLongerPassesChangesNothing(t *testing.T) {
	t.Parallel()
	f := overLimitHeld(t)
	// 保存した出力の鍵 c が、いまあるタスクの鍵と重なるようにする。
	coretest.Exec(t, f.s.Workspace(), `UPDATE task SET key = 'c' WHERE challenge_id = ? AND key = 'b'`, f.cid())
	_, err := f.answerHold(t, taskAddOptionAdd)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	if h, _ := f.lastHold(t); !h.Open() {
		t.Errorf("hold was closed")
	}
	if adds := f.additions(t); adds[0].State != TaskAdditionAwaitingOwner || f.limitMicros(t) != 14_000_000 || f.detail(t, f.id).Status != StatusAwaitingHuman {
		t.Errorf("addition = %s limit = %d, want unchanged", adds[0].State, f.limitMicros(t))
	}
}

func TestHoldAnswerTaskAddition_AddWithUnreadableDeclarationsIsRefusedWithoutPanicking(t *testing.T) {
	t.Parallel()
	f := overLimitHeld(t)
	prev, err := f.s.PrepareAnswerOption(context.Background(), f.id, "", taskAddOptionAdd, false)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: f.id, ExpectedVersion: prev.Version, Option: taskAddOptionAdd},
		relayedAttestation(t, f.id, "sess", "quote"))
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	if adds := f.additions(t); adds[0].State != TaskAdditionAwaitingOwner {
		t.Errorf("addition = %s, want unchanged", adds[0].State)
	}
}
