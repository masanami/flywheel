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

// 区切り S4c-1 の 9: 「足す」の回答を受けたときの見込みの計算し直しと、問いの出し直し。

// raiseExistingEstimate は、保留の間のタスクの枠の増額（足した後の見込みが 1 USD 増える）を作る。
func (f *paFixture) raiseExistingEstimate(t *testing.T) {
	t.Helper()
	coretest.Exec(t, f.s.Workspace(), `UPDATE task SET budget_impl_micros = budget_impl_micros + 1000000 WHERE challenge_id = ? AND key = 'b'`, f.cid())
}

func (f *paFixture) answerHoldFull(t *testing.T, version int, req AnswerRequest) (*Challenge, *Hold, error) {
	t.Helper()
	req.ChallengeID, req.ExpectedVersion, req.Agent, req.Conn = f.id, version, f.agent, f.conn
	return f.s.ExecuteAnswer(context.Background(), req, relayedAttestation(t, f.id, "sess", "quote"))
}

func TestHoldAnswerTaskAddition_AddAfterTheEstimateGrewReissuesTheQuestionAndChangesNothingElse(t *testing.T) {
	t.Parallel()
	f := overLimitHeld(t)
	before := f.detail(t, f.id).Version
	f.raiseExistingEstimate(t)
	approvals := f.budgetApprovals(t)
	c, hold, err := f.answerHoldFull(t, before, AnswerRequest{Option: taskAddOptionAdd})
	if err != nil {
		t.Fatalf("err = %v, want a successful write that reissues the question", err)
	}
	if !hold.Reissued || hold.Answer != nil {
		t.Errorf("hold = %+v, want Reissued and unanswered", hold)
	}
	if c.Version != before+1 || c.Status != StatusAwaitingHuman {
		t.Errorf("version=%d status=%s, want %d awaiting_human", c.Version, c.Status, before+1)
	}
	h, reason := f.lastHold(t)
	if !h.Open() || reason != string(holdReasonTaskAddOverLimit) {
		t.Errorf("hold = %+v reason=%s, want open task_add_over_limit", h, reason)
	}
	wantLastHoldScopeNull(t, f.s) // AC-84: 出し直しの相手は、課題を止める保留（足す依頼の問い）
	if !strings.Contains(h.Question, "足した後の見込み: $16") || strings.Contains(h.Question, "足した後の見込み: $15\n") {
		t.Errorf("question = %q, want the recomputed estimate $16", h.Question)
	}
	if d := addOptionDescription(h.Options); !strings.Contains(d, "$16") {
		t.Errorf("add description = %q, want $16", d)
	}
	if !strings.Contains(hold.Question, "$16") || !strings.Contains(addOptionDescription(hold.Options), "$16") {
		t.Errorf("returned hold = %+v, want the new amount", hold)
	}
	if f.limitMicros(t) != 14_000_000 || f.taskByKey(t, "c") != nil || f.budgetApprovals(t) != approvals {
		t.Errorf("limit=%d task=%v approvals=%d, want unchanged", f.limitMicros(t), f.taskByKey(t, "c"), f.budgetApprovals(t))
	}
	if adds := f.additions(t); adds[0].State != TaskAdditionAwaitingOwner {
		t.Errorf("addition = %s, want awaiting_owner", adds[0].State)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM activity WHERE entity = 'challenge' AND entity_id = ? AND action = ?`, f.cid(), ActivityActionHoldEdit); n != 1 {
		t.Errorf("hold_edit activities = %d, want 1", n)
	}
	// 書き換えの作業ログの hold_id は、ほかの作業ログ（回答・スーパーバイザーの手）と同じ H-<番号> の文字列（整数にしない）。
	if n := f.count(t, `SELECT COUNT(*) FROM activity WHERE entity = 'challenge' AND entity_id = ? AND action = ?
		AND json_type(after, '$.hold_id') = 'text' AND json_extract(after, '$.hold_id') = ?`, f.cid(), ActivityActionHoldEdit, h.HoldID); n != 1 {
		t.Errorf("hold_edit activities with hold_id %q as text = %d, want 1", h.HoldID, n)
	}
	// 出し直して返す保留も、保留の一覧の読みと同じ ID・止める範囲・タスクを持つ（足す依頼の問いは課題を止める保留）。
	if hold.HoldID == "" || hold.HoldID != h.HoldID || hold.Scope != h.Scope || (hold.TaskID == nil) != (h.TaskID == nil) {
		t.Errorf("returned hold id=%q scope=%q task=%v, want the same as the listed hold id=%q scope=%q task=%v", hold.HoldID, hold.Scope, hold.TaskID, h.HoldID, h.Scope, h.TaskID)
	}
	// 古い版を持った再送は、版の不一致で断られる。
	if _, _, err := f.answerHoldFull(t, before, AnswerRequest{Option: taskAddOptionAdd}); !errors.Is(err, ErrConflict) {
		t.Errorf("stale resend err = %v, want ErrConflict", err)
	}
}

func TestHoldAnswerTaskAddition_AnsweringAddAgainAfterTheReissueAddsTheTasks(t *testing.T) {
	t.Parallel()
	f := overLimitHeld(t)
	f.raiseExistingEstimate(t)
	c, _, err := f.answerHoldFull(t, f.detail(t, f.id).Version, AnswerRequest{Option: taskAddOptionAdd})
	if err != nil {
		t.Fatal(err)
	}
	c, hold, err := f.answerHoldFull(t, c.Version, AnswerRequest{Option: taskAddOptionAdd})
	if err != nil {
		t.Fatal(err)
	}
	if hold.Reissued || c.Status != StatusInProgress || f.taskByKey(t, "c") == nil {
		t.Errorf("reissued=%v status=%s task=%v, want the tasks added", hold.Reissued, c.Status, f.taskByKey(t, "c"))
	}
	if got := f.limitMicros(t); got != 16_000_000 {
		t.Errorf("limit = %d, want 16000000 (the amount the reissued option showed)", got)
	}
}

func TestHoldAnswerTaskAddition_RefusalsChangeNothing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		req  AnswerRequest
		want error
		text string
	}{
		{"sentence only", AnswerRequest{Answer: "足してください"}, ErrValidation, "add (足す)"},
		{"replan flag only", AnswerRequest{Replan: true}, ErrValidation, "add (足す)"},
		{"replan flag with a sentence", AnswerRequest{Answer: "x", Replan: true}, ErrValidation, "skip (足さずに進める)"},
		{"unknown option", AnswerRequest{Option: "yes"}, ErrValidation, "replan (計画を作り直す)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := overLimitHeld(t)
			_, _, err := f.answerHoldFull(t, f.detail(t, f.id).Version, tc.req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if tc.text != "" && !strings.Contains(err.Error(), tc.text) {
				t.Errorf("err = %q, want it to list the options (%s)", err, tc.text)
			}
			if h, _ := f.lastHold(t); !h.Open() {
				t.Errorf("hold was closed")
			}
			if adds := f.additions(t); adds[0].State != TaskAdditionAwaitingOwner || f.limitMicros(t) != 14_000_000 || f.detail(t, f.id).Status != StatusAwaitingHuman {
				t.Errorf("addition = %s limit = %d, want unchanged", adds[0].State, f.limitMicros(t))
			}
		})
	}
}

func TestHoldAnswerTaskAddition_AddWithoutAVerifiedAttestationIsRefusedAndKeepsTheLimit(t *testing.T) {
	t.Parallel()
	f := overLimitHeld(t)
	f.raiseExistingEstimate(t)
	v := f.detail(t, f.id).Version
	_, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: f.id, ExpectedVersion: v, Option: taskAddOptionAdd, Agent: f.agent, Conn: f.conn}, Attestation{})
	if !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("err = %v, want ErrVerificationRejected", err)
	}
	if f.limitMicros(t) != 14_000_000 || f.detail(t, f.id).Version != v {
		t.Errorf("limit = %d version = %d, want unchanged (no reissue either)", f.limitMicros(t), f.detail(t, f.id).Version)
	}
}

// 足す依頼の保留を --hold で指して「足す」で答えるとき、タスクを止める保留がより新しく開いていても、見込みの計算し直しと問いの出し直しは
// 指した保留（足す依頼の保留）に対して行い、返す保留も指した保留である（最新の開いている保留を相手にしない）。
func TestHoldAnswerTaskAddition_AddWithHoldFlagReissuesThePointedHoldEvenIfATaskHoldIsNewer(t *testing.T) {
	t.Parallel()
	f := overLimitHeld(t)
	var additionHold int64
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT id FROM hold WHERE challenge_id = ? AND answer IS NULL AND closed_at IS NULL`, f.cid()).Scan(&additionHold)
	})
	coretest.InsertTaskHold(t, f.s.Workspace(), int(f.cid()), f.taskByKey(t, "b").Title, "別のタスクの問い", "in_progress", "2026-11-23T00:00:00.000Z", "")
	f.raiseExistingEstimate(t)
	prev, err := f.s.PrepareAnswerHold(context.Background(), f.id, formatHoldID(additionHold), "", taskAddOptionAdd, false)
	if err != nil {
		t.Fatalf("PrepareAnswerHold: %v", err)
	}
	c, hold, err := f.answerHoldFull(t, prev.Version, AnswerRequest{
		Option: taskAddOptionAdd, HoldID: prev.HoldID, HoldQuestion: prev.Question, HoldOptions: prev.Options, HoldOptionRequired: prev.OptionRequired,
	})
	if err != nil {
		t.Fatalf("err = %v, want a successful write that reissues the question", err)
	}
	if !hold.Reissued || hold.Answer != nil || !strings.Contains(hold.Question, "$16") || strings.Contains(hold.Question, "別のタスクの問い") {
		t.Errorf("returned hold = %+v, want the reissued addition hold ($16)", hold)
	}
	if c.Status != StatusAwaitingHuman || f.limitMicros(t) != 14_000_000 {
		t.Errorf("status=%s limit=%d, want unchanged awaiting_human and 14 USD", c.Status, f.limitMicros(t))
	}
	var q string
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT question FROM hold WHERE id = ?`, additionHold).Scan(&q)
	})
	if !strings.Contains(q, "$16") {
		t.Errorf("addition hold question = %q, want $16", q)
	}
}
