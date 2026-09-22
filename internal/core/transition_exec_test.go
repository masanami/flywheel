package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os/user"
	"sync"
	"testing"
	"time"
)

// createAndAdvance は課題を作成し、setChallengeStatus で status まで直接進める
// （#10 のテストは #9 までの CRUD だけを土台に、任意の遷移元状態を用意する）。
func createAndAdvance(t *testing.T, s *Store, status Status) *Challenge {
	t.Helper()
	c, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatalf("CreateChallenge() error = %v", err)
	}
	if status != StatusUnclassified {
		cid, _ := parseChallengeID(c.ID)
		setChallengeStatus(t, s, cid, status)
		c.Status = status
	}
	return c
}

func activitiesFor(t *testing.T, s *Store, id string) []Activity {
	t.Helper()
	acts, err := s.ListActivities(context.Background(), strPtr(id))
	if err != nil {
		t.Fatalf("ListActivities(%q) error = %v", id, err)
	}
	return acts
}

// --- ClassifyChallenge ---

func TestClassifyChallenge_SetsPriorityAndAdvancesStatus(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)

	got, err := s.ClassifyChallenge(context.Background(), ChannelCLI, c.ID, ClassifyInput{Priority: "P1"})
	if err != nil {
		t.Fatalf("ClassifyChallenge() error = %v", err)
	}
	if got.Status != StatusClassified {
		t.Errorf("Status = %q, want %q", got.Status, StatusClassified)
	}
	if got.Priority == nil || *got.Priority != PriorityP1 {
		t.Errorf("Priority = %v, want %q", got.Priority, PriorityP1)
	}
	if got.Version != 2 {
		t.Errorf("Version = %d, want 2", got.Version)
	}

	acts := activitiesFor(t, s, c.ID)
	if len(acts) != 2 {
		t.Fatalf("len(activities) = %d, want 2 (create + classify)", len(acts))
	}
	a := acts[1]
	if a.Action != "classify" {
		t.Errorf("Action = %q, want %q", a.Action, "classify")
	}
	if a.Verification != string(VerificationNone) {
		t.Errorf("Verification = %q, want none", a.Verification)
	}
	var before, after map[string]any
	if err := json.Unmarshal(a.Before, &before); err != nil {
		t.Fatalf("unmarshal Before: %v", err)
	}
	if err := json.Unmarshal(a.After, &after); err != nil {
		t.Fatalf("unmarshal After: %v", err)
	}
	if before["status"] != string(StatusUnclassified) || after["status"] != string(StatusClassified) {
		t.Errorf("before/after status = %v/%v", before["status"], after["status"])
	}
	if _, ok := before["priority"]; !ok {
		t.Errorf("before missing priority key (should be null, not absent)")
	}
	if after["priority"] != "P1" {
		t.Errorf("after[priority] = %v, want P1", after["priority"])
	}
	if after["version"] != float64(2) {
		t.Errorf("after[version] = %v, want 2", after["version"])
	}
}

func TestClassifyChallenge_InvalidPriorityIsValidationErrorAndChangesNothing(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)

	_, err := s.ClassifyChallenge(context.Background(), ChannelCLI, c.ID, ClassifyInput{Priority: "bogus"})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	after, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if after.Version != 1 || after.Status != StatusUnclassified {
		t.Fatalf("challenge changed despite validation failure: version=%d status=%q", after.Version, after.Status)
	}
	if len(activitiesFor(t, s, c.ID)) != 1 {
		t.Fatalf("activity log grew despite validation failure")
	}
}

func TestClassifyChallenge_WrongStateIsInvalidTransition(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusClassified)

	_, err := s.ClassifyChallenge(context.Background(), ChannelCLI, c.ID, ClassifyInput{Priority: "P0"})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v, want ErrInvalidTransition", err)
	}
}

func TestClassifyChallenge_DoneIsTerminalState(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusDone)

	_, err := s.ClassifyChallenge(context.Background(), ChannelCLI, c.ID, ClassifyInput{Priority: "P0"})
	if !errors.Is(err, ErrTerminalState) {
		t.Fatalf("err = %v, want ErrTerminalState", err)
	}
}

func TestClassifyChallenge_NotFoundForMissingOrMalformedID(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	for _, id := range []string{"C-999", "OP-1", "foo"} {
		_, err := s.ClassifyChallenge(context.Background(), ChannelCLI, id, ClassifyInput{Priority: "P0"})
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("id=%q: err = %v, want ErrNotFound", id, err)
		}
	}
}

// --- PlanChallenge ---

func TestPlanChallenge_FirstPlanAdvancesToAwaitingPlanApproval(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusClassified)

	got, plan, err := s.PlanChallenge(context.Background(), ChannelCLI, c.ID, PlanInput{Body: "do the thing"})
	if err != nil {
		t.Fatalf("PlanChallenge() error = %v", err)
	}
	if got.Status != StatusAwaitingPlanApproval {
		t.Errorf("Status = %q, want %q", got.Status, StatusAwaitingPlanApproval)
	}
	if got.Version != 2 {
		t.Errorf("Version = %d, want 2", got.Version)
	}
	if plan.Version != 1 || plan.Body != "do the thing" {
		t.Errorf("Plan = %+v, want version=1 body=%q", plan, "do the thing")
	}

	detail, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if len(detail.Plans) != 1 {
		t.Fatalf("len(Plans) = %d, want 1", len(detail.Plans))
	}
}

// AC-35: 計画承認待ちの課題に plan を再度行うと、計画の版が1増え、以前の版も show で読める。
func TestPlanChallenge_RevisionBumpsPlanVersionAndKeepsPriorVersions(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusClassified)

	_, _, err := s.PlanChallenge(context.Background(), ChannelCLI, c.ID, PlanInput{Body: "v1"})
	if err != nil {
		t.Fatalf("PlanChallenge() 1 error = %v", err)
	}

	got, plan, err := s.PlanChallenge(context.Background(), ChannelCLI, c.ID, PlanInput{Body: "v2"})
	if err != nil {
		t.Fatalf("PlanChallenge() 2 error = %v", err)
	}
	if got.Status != StatusAwaitingPlanApproval {
		t.Errorf("Status = %q, want %q (unchanged)", got.Status, StatusAwaitingPlanApproval)
	}
	if got.Version != 3 {
		t.Errorf("challenge Version = %d, want 3 (1 create + classify-less setup=1 -> plan1=2 -> plan2=3)", got.Version)
	}
	if plan.Version != 2 || plan.Body != "v2" {
		t.Errorf("Plan = %+v, want version=2 body=v2", plan)
	}

	detail, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if len(detail.Plans) != 2 {
		t.Fatalf("len(Plans) = %d, want 2", len(detail.Plans))
	}
	if detail.Plans[0].Version != 1 || detail.Plans[0].Body != "v1" {
		t.Errorf("Plans[0] = %+v, want version=1 body=v1", detail.Plans[0])
	}
	if detail.Plans[1].Version != 2 || detail.Plans[1].Body != "v2" {
		t.Errorf("Plans[1] = %+v, want version=2 body=v2", detail.Plans[1])
	}

	// T4 は状態が変わらないので、作業ログの before/after に status を含めない。
	acts := activitiesFor(t, s, c.ID)
	last := acts[len(acts)-1]
	if last.Action != "plan" {
		t.Fatalf("last action = %q, want plan", last.Action)
	}
	var before, after map[string]any
	if last.Before != nil {
		if err := json.Unmarshal(last.Before, &before); err != nil {
			t.Fatalf("unmarshal Before: %v", err)
		}
	}
	if err := json.Unmarshal(last.After, &after); err != nil {
		t.Fatalf("unmarshal After: %v", err)
	}
	if _, ok := before["status"]; ok {
		t.Errorf("Before contains status despite no status change: %+v", before)
	}
	if _, ok := after["status"]; ok {
		t.Errorf("After contains status despite no status change: %+v", after)
	}
	if after["plan_version"] != float64(2) {
		t.Errorf("After[plan_version] = %v, want 2", after["plan_version"])
	}
	// 改訂の before は置き換えられた版を持つ非 null のオブジェクト（NULL だと
	// 「create 以外は before を持つ」規約に反する。code-reviewer 指摘）。
	if last.Before == nil {
		t.Fatalf("Before = nil for a plan revision, want an object with plan_version")
	}
	if before["plan_version"] != float64(1) {
		t.Errorf("Before[plan_version] = %v, want 1 (the superseded plan version)", before["plan_version"])
	}
}

func TestPlanChallenge_EmptyBodyIsValidationErrorAndChangesNothing(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusClassified)

	for _, body := range []string{"", "   ", "\t\n"} {
		_, _, err := s.PlanChallenge(context.Background(), ChannelCLI, c.ID, PlanInput{Body: body})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("body=%q: err = %v, want ErrValidation", body, err)
		}
	}
	after, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if after.Version != 1 || len(after.Plans) != 0 {
		t.Fatalf("challenge changed despite validation failure: version=%d plans=%d", after.Version, len(after.Plans))
	}
}

func TestPlanChallenge_WrongStateIsInvalidTransition(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)

	_, _, err := s.PlanChallenge(context.Background(), ChannelCLI, c.ID, PlanInput{Body: "x"})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v, want ErrInvalidTransition", err)
	}
}

// --- SubmitChallenge ---

func TestSubmitChallenge_AdvancesToVerifying(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusInProgress)

	got, err := s.SubmitChallenge(context.Background(), ChannelCLI, c.ID)
	if err != nil {
		t.Fatalf("SubmitChallenge() error = %v", err)
	}
	if got.Status != StatusVerifying {
		t.Errorf("Status = %q, want %q", got.Status, StatusVerifying)
	}
	if got.Version != 2 {
		t.Errorf("Version = %d, want 2", got.Version)
	}
}

func TestSubmitChallenge_WrongStateIsInvalidTransition(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusClassified)

	_, err := s.SubmitChallenge(context.Background(), ChannelCLI, c.ID)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v, want ErrInvalidTransition", err)
	}
}

// --- VerifyChallenge ---

func TestVerifyChallenge_MetAdvancesToAwaitingCompletionApproval(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusVerifying)

	got, err := s.VerifyChallenge(context.Background(), ChannelCLI, c.ID, VerifyInput{Result: "met"})
	if err != nil {
		t.Fatalf("VerifyChallenge() error = %v", err)
	}
	if got.Status != StatusAwaitingCompletionApproval {
		t.Errorf("Status = %q, want %q", got.Status, StatusAwaitingCompletionApproval)
	}
}

func TestVerifyChallenge_NotMetReturnsToInProgress(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusVerifying)

	got, err := s.VerifyChallenge(context.Background(), ChannelCLI, c.ID, VerifyInput{Result: "not_met"})
	if err != nil {
		t.Fatalf("VerifyChallenge() error = %v", err)
	}
	if got.Status != StatusInProgress {
		t.Errorf("Status = %q, want %q", got.Status, StatusInProgress)
	}
}

func TestVerifyChallenge_UncertainRecordsHoldAndAdvancesToAwaitingHuman(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusVerifying)

	q := "why uncertain?"
	got, err := s.VerifyChallenge(context.Background(), ChannelCLI, c.ID, VerifyInput{Result: "uncertain", Question: &q})
	if err != nil {
		t.Fatalf("VerifyChallenge() error = %v", err)
	}
	if got.Status != StatusAwaitingHuman {
		t.Errorf("Status = %q, want %q", got.Status, StatusAwaitingHuman)
	}

	detail, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if len(detail.Holds) != 1 {
		t.Fatalf("len(Holds) = %d, want 1", len(detail.Holds))
	}
	h := detail.Holds[0]
	if h.Question != q {
		t.Errorf("Hold.Question = %q, want %q", h.Question, q)
	}
	if h.FromStatus != StatusVerifying {
		t.Errorf("Hold.FromStatus = %q, want %q", h.FromStatus, StatusVerifying)
	}
	if h.Answer != nil {
		t.Errorf("Hold.Answer = %v, want nil", h.Answer)
	}

	acts := activitiesFor(t, s, c.ID)
	last := acts[len(acts)-1]
	if last.Action != "verify_uncertain" {
		t.Errorf("Action = %q, want %q", last.Action, "verify_uncertain")
	}
}

func TestVerifyChallenge_InvalidResultIsValidationErrorAndChangesNothing(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusVerifying)

	_, err := s.VerifyChallenge(context.Background(), ChannelCLI, c.ID, VerifyInput{Result: "bogus"})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	after, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if after.Version != 1 || after.Status != StatusVerifying {
		t.Fatalf("challenge changed despite validation failure")
	}
}

func TestVerifyChallenge_UncertainWithEmptyOrMissingQuestionIsValidationError(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusVerifying)

	empty := "   "
	cases := []*string{nil, &empty}
	for _, q := range cases {
		_, err := s.VerifyChallenge(context.Background(), ChannelCLI, c.ID, VerifyInput{Result: "uncertain", Question: q})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("question=%v: err = %v, want ErrValidation", q, err)
		}
	}
	after, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if after.Version != 1 || len(after.Holds) != 0 {
		t.Fatalf("challenge changed despite validation failure: version=%d holds=%d", after.Version, len(after.Holds))
	}
}

func TestVerifyChallenge_MetOrNotMetWithQuestionIsValidationError(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusVerifying)

	q := "should not be here"
	for _, result := range []string{"met", "not_met"} {
		_, err := s.VerifyChallenge(context.Background(), ChannelCLI, c.ID, VerifyInput{Result: result, Question: &q})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("result=%q: err = %v, want ErrValidation", result, err)
		}
	}
}

// --- HoldChallenge ---

func TestHoldChallenge_AllValidSourcesAdvanceToAwaitingHumanAndRecordFromStatus(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	for _, from := range []Status{StatusUnclassified, StatusClassified, StatusInProgress, StatusVerifying} {
		c := createAndAdvance(t, s, from)
		got, err := s.HoldChallenge(context.Background(), ChannelCLI, c.ID, HoldInput{Question: "why?"})
		if err != nil {
			t.Fatalf("from=%q: HoldChallenge() error = %v", from, err)
		}
		if got.Status != StatusAwaitingHuman {
			t.Errorf("from=%q: Status = %q, want %q", from, got.Status, StatusAwaitingHuman)
		}

		detail, err := s.GetChallenge(context.Background(), c.ID)
		if err != nil {
			t.Fatalf("GetChallenge() error = %v", err)
		}
		if len(detail.Holds) != 1 || detail.Holds[0].FromStatus != from {
			t.Errorf("from=%q: Holds = %+v, want 1 entry with from_status=%q", from, detail.Holds, from)
		}
	}
}

func TestHoldChallenge_EmptyQuestionIsValidationErrorAndChangesNothing(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)

	for _, q := range []string{"", "   ", "\t\n"} {
		_, err := s.HoldChallenge(context.Background(), ChannelCLI, c.ID, HoldInput{Question: q})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("question=%q: err = %v, want ErrValidation", q, err)
		}
	}
	after, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if after.Version != 1 || len(after.Holds) != 0 {
		t.Fatalf("challenge changed despite validation failure")
	}
}

func TestHoldChallenge_WrongStateIsInvalidTransition(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	// 計画承認待ちは T11 の遷移元に含まれない。
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)

	_, err := s.HoldChallenge(context.Background(), ChannelCLI, c.ID, HoldInput{Question: "why?"})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v, want ErrInvalidTransition", err)
	}
}

// --- 終端状態（done）はすべての操作を ErrTerminalState で拒否し、何も変えない ---

func TestAllFiveOperations_DoneChallengeIsTerminalStateAndChangesNothing(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")

	dispatch := map[Operation]func(id string) error{
		OpClassify: func(id string) error {
			_, err := s.ClassifyChallenge(context.Background(), ChannelCLI, id, ClassifyInput{Priority: "P0"})
			return err
		},
		OpPlan: func(id string) error {
			_, _, err := s.PlanChallenge(context.Background(), ChannelCLI, id, PlanInput{Body: "x"})
			return err
		},
		OpSubmit: func(id string) error {
			_, err := s.SubmitChallenge(context.Background(), ChannelCLI, id)
			return err
		},
		OpVerifyMet: func(id string) error {
			_, err := s.VerifyChallenge(context.Background(), ChannelCLI, id, VerifyInput{Result: "met"})
			return err
		},
		OpHold: func(id string) error {
			_, err := s.HoldChallenge(context.Background(), ChannelCLI, id, HoldInput{Question: "why?"})
			return err
		},
	}

	for op, call := range dispatch {
		c := createAndAdvance(t, s, StatusDone)
		if err := call(c.ID); !errors.Is(err, ErrTerminalState) {
			t.Errorf("op=%q: err = %v, want ErrTerminalState", op, err)
		}
		after, err := s.GetChallenge(context.Background(), c.ID)
		if err != nil {
			t.Fatalf("GetChallenge() error = %v", err)
		}
		if after.Version != 1 || after.Status != StatusDone {
			t.Errorf("op=%q: challenge changed despite terminal state: version=%d status=%q", op, after.Version, after.Status)
		}
		if len(activitiesFor(t, s, c.ID)) != 1 {
			t.Errorf("op=%q: activity log grew despite terminal state", op)
		}
	}
}

// --- AC-68 相当: 作業ログへの書き込み失敗はロールバックする ---

func TestClassifyChallenge_ActivityInsertFailureRollsBack(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)

	s.insertActivity = func(_ *sql.Tx, _ activityRow) error {
		return errors.New("boom: injected activity insert failure")
	}

	_, err := s.ClassifyChallenge(context.Background(), ChannelCLI, c.ID, ClassifyInput{Priority: "P0"})
	if err == nil {
		t.Fatal("ClassifyChallenge() error = nil, want an error from the injected failure")
	}

	s.insertActivity = nil
	after, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if after.Version != 1 || after.Status != StatusUnclassified || after.Priority != nil {
		t.Fatalf("classify was not rolled back: version=%d status=%q priority=%v", after.Version, after.Status, after.Priority)
	}
}

// dispatchOp は AC-23/AC-24 の枠組みが使う、Operation → 操作の適用の対応表。
// #12 で T5・T6・T12・T13・T15（approve・reject・answer）を、#13 で T14
// （approve_hold_release）を対象に加えた（フェイク Verifier で確認を成立させて
// 呼ぶ）。
type dispatchResult struct {
	status  Status
	version int
	err     error
}

func dispatchOperation(t *testing.T, s *Store, id string, op Operation) dispatchResult {
	t.Helper()
	switch op {
	case OpClassify:
		c, err := s.ClassifyChallenge(context.Background(), ChannelCLI, id, ClassifyInput{Priority: "P0"})
		return resultOf(c, err)
	case OpPlan:
		c, _, err := s.PlanChallenge(context.Background(), ChannelCLI, id, PlanInput{Body: "x"})
		return resultOf(c, err)
	case OpSubmit:
		c, err := s.SubmitChallenge(context.Background(), ChannelCLI, id)
		return resultOf(c, err)
	case OpVerifyMet:
		c, err := s.VerifyChallenge(context.Background(), ChannelCLI, id, VerifyInput{Result: "met"})
		return resultOf(c, err)
	case OpVerifyNotMet:
		c, err := s.VerifyChallenge(context.Background(), ChannelCLI, id, VerifyInput{Result: "not_met"})
		return resultOf(c, err)
	case OpVerifyUncertain:
		q := "why?"
		c, err := s.VerifyChallenge(context.Background(), ChannelCLI, id, VerifyInput{Result: "uncertain", Question: &q})
		return resultOf(c, err)
	case OpHold:
		c, err := s.HoldChallenge(context.Background(), ChannelCLI, id, HoldInput{Question: "why?"})
		return resultOf(c, err)
	case OpApprove:
		prev, err := s.PrepareApproval(context.Background(), id)
		if err != nil {
			return dispatchResult{err: err}
		}
		att := verifiedAttestationForTest(t, id)
		c, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{
			ChallengeID: id, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
		}, att)
		return resultOf(c, err)
	case OpReject:
		reason := "because"
		prev, err := s.PrepareRejection(context.Background(), id, reason)
		if err != nil {
			return dispatchResult{err: err}
		}
		att := verifiedAttestationForTest(t, id)
		c, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{
			ChallengeID: id, ExpectedVersion: prev.Version, Decision: ApprovalDecisionRejected, Reason: &reason,
		}, att)
		return resultOf(c, err)
	case OpApproveHoldRelease:
		prev, err := s.PrepareApproval(context.Background(), id)
		if err != nil {
			return dispatchResult{err: err}
		}
		att := verifiedAttestationForTest(t, id)
		c, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{
			ChallengeID: id, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved, HoldRelease: true,
		}, att)
		return resultOf(c, err)
	case OpAnswer:
		answer := "ans"
		prev, err := s.PrepareAnswer(context.Background(), id, answer)
		if err != nil {
			return dispatchResult{err: err}
		}
		att := verifiedAttestationForTest(t, id)
		c, _, err := s.ExecuteAnswer(context.Background(), AnswerRequest{
			ChallengeID: id, ExpectedVersion: prev.Version, Answer: answer,
		}, att)
		return resultOf(c, err)
	default:
		t.Fatalf("dispatchOperation: unsupported op %q", op)
		return dispatchResult{}
	}
}

// verifiedAttestationForTest は fakeVerifier（verification_test.go）を通して
// 本人確認を成立させ、Attestation を得る。#12 の dispatchOperation（AC-23・
// AC-24 の列挙テスト）が approve・reject・answer を他の5操作と同じ枠組みで
// 扱うために使う。
func verifiedAttestationForTest(t *testing.T, expectedID string) Attestation {
	t.Helper()
	fv := &fakeVerifier{method: VerificationTTYConfirm, actor: "alice"}
	att, err := Verify(ChannelCLI, fv, "summary", expectedID)
	if err != nil {
		t.Fatalf("Verify() setup error = %v", err)
	}
	return att
}

// insertUnansweredHold は challengeID（内部整数 ID）に、from_status を持つ
// 未回答の保留を 1 行だけ追加する。AC-23 の列挙テストが T12（answer）を
// 他の行と同じ枠組みで検証するために使う（createAndAdvance は生 SQL で
// status を直接書き換えるだけで、保留行までは作らないため）。
func insertUnansweredHold(t *testing.T, s *Store, challengeID int64, fromStatus Status, question string) {
	t.Helper()
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO hold (challenge_id, question, from_status, raised_at, answer, answered_at, answered_by)
			 VALUES (?, ?, ?, ?, NULL, NULL, NULL)`,
			challengeID, question, string(fromStatus), formatTimestamp(s.currentTime()),
		)
		return err
	}); err != nil {
		t.Fatalf("insertUnansweredHold: %v", err)
	}
}

// insertPlanRow は challengeID（内部整数 ID）に task_plan 行を 1 行だけ追加
// する。AC-23 の列挙テストが T5・T6（計画承認待ちからの approve・reject）を
// 検証するために使う（createAndAdvance は生 SQL で status を直接書き換える
// だけで、計画行までは作らないため。PrepareApproval/PrepareRejection は
// 計画承認待ちの課題に計画行が無いと fail-closed にエラーを返す＝
// self-review 指摘の修正）。
func insertPlanRow(t *testing.T, s *Store, challengeID int64, version int, body string) {
	t.Helper()
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO task_plan (challenge_id, version, body, created_at) VALUES (?, ?, ?, ?)`,
			challengeID, version, body, formatTimestamp(s.currentTime()),
		)
		return err
	}); err != nil {
		t.Fatalf("insertPlanRow: %v", err)
	}
}

func resultOf(c *Challenge, err error) dispatchResult {
	if err != nil {
		return dispatchResult{err: err}
	}
	return dispatchResult{status: c.Status, version: c.Version}
}

// dispatchedOperations は、遷移表 Table から導出した「dispatchOperation が
// 扱える操作トークン」の集合（T1 の create は #9 の対象なので除く）。#10 の
// 7 つ（classify・plan・submit・verify_met・verify_not_met・verify_uncertain・
// hold）に #12 の 3 つ（approve・reject・answer）・#13 の 1 つ
// （approve_hold_release）を加えた 11 個に一致することは TestDispatchTable_AC24_*
// が assert する（Table に操作トークンが増えたとき AC-24 の直積が黙って狭いまま
// 通らないようにする。design-reviewer 指摘）。
func dispatchedOperations(t *testing.T) []Operation {
	t.Helper()
	seen := map[Operation]bool{}
	var ops []Operation
	for _, tr := range Table {
		if tr.From == NoStatus || seen[tr.Op] {
			continue
		}
		seen[tr.Op] = true
		ops = append(ops, tr.Op)
	}
	want := map[Operation]bool{
		OpClassify: true, OpPlan: true, OpSubmit: true,
		OpVerifyMet: true, OpVerifyNotMet: true, OpVerifyUncertain: true, OpHold: true,
		OpApprove: true, OpReject: true, OpAnswer: true, OpApproveHoldRelease: true,
	}
	if len(ops) != len(want) {
		t.Fatalf("dispatchable operations derived from Table = %v, want exactly %v (a new operation token needs a dispatch entry)", ops, want)
	}
	for _, op := range ops {
		if !want[op] {
			t.Fatalf("unexpected operation %q in Table (add it to dispatchOperation and this list)", op)
		}
	}
	return ops
}

// allStatuses は状態語彙（StatusVocabulary）から導出した全状態。
func allStatuses() []Status {
	out := make([]Status, 0, len(StatusVocabulary))
	for _, e := range StatusVocabulary {
		out = append(out, e.Code)
	}
	return out
}

// AC-23: 遷移表 T1〜T15 のそれぞれについて、遷移元の状態にある課題へ操作を
// 行うと課題の状態が遷移先になる（全行を列挙して検証する。本人確認が要る行
// （T5・T6・T12・T13・T14・T15）はフェイク Verifier で確認を成立させて検証する）。
func TestDispatchTable_AC23_NonVerificationRowsTransitionToTarget(t *testing.T) {
	tested := 0
	for _, tr := range Table {
		if tr.From == NoStatus {
			continue // T1: create は #9 の対象
		}

		s := newStoreForTest(t)
		fixedActor(t, "alice")
		c := createAndAdvance(t, s, tr.From)

		// T12（answer）は「保留に入る直前の状態」を実行時の保留行から読むため、
		// createAndAdvance（生 SQL で status を直接書き換えるだけ）とは別に
		// 未回答の保留行を用意する必要がある（HoldEntrySources に含まれる
		// 状態ならどれでもよく、ここでは unclassified を使う。4状態すべての
		// 網羅は AC-33 の専用テストが担う）。
		preceding := NoStatus
		if tr.Op == OpAnswer {
			preceding = StatusUnclassified
			cid, _ := parseChallengeID(c.ID)
			insertUnansweredHold(t, s, cid, preceding, "why?")
		}

		// T5・T6（計画承認待ちからの approve・reject）は PrepareApproval/
		// PrepareRejection が計画行を要求する（fail-closed。self-review 指摘:
		// 計画行が無いまま承認が成立してしまう fail-open を塞いだ）ため、
		// createAndAdvance とは別に計画行を用意する。
		if tr.From == StatusAwaitingPlanApproval {
			cid, _ := parseChallengeID(c.ID)
			insertPlanRow(t, s, cid, 1, "plan body")
		}

		wantTarget, err := tr.Target.Resolve(Table, preceding)
		if err != nil {
			t.Fatalf("%s: Resolve: %v", tr.ID, err)
		}

		res := dispatchOperation(t, s, c.ID, tr.Op)
		if res.err != nil {
			t.Errorf("%s: from=%q op=%q err = %v, want nil", tr.ID, tr.From, tr.Op, res.err)
			continue
		}
		if res.status != wantTarget {
			t.Errorf("%s: from=%q op=%q status = %q, want %q", tr.ID, tr.From, tr.Op, res.status, wantTarget)
		}
		if res.version != 2 {
			t.Errorf("%s: from=%q op=%q version = %d, want 2", tr.ID, tr.From, tr.Op, res.version)
		}
		tested++
	}

	if tested == 0 {
		t.Fatal("no transition rows were tested")
	}
}

// TestRunTransition_RequiresVerificationMismatchIsRejected は self-review
// 指摘の再発防止: 遷移表の RequiresVerification と、呼び出しが本人確認つき
// （expectedVersion 非 nil）かどうかが食い違う呼び出しを runTransition が
// fail-closed に拒否すること。以前は runTransition が RequiresVerification
// を一切参照しておらず、内部の transition()（本人確認なし）から
// OpApprove・OpReject・OpAnswer のような本人確認つきの操作を呼んでも、
// 遷移表の検査（Lookup）だけは通ってしまっていた（「承認・差し戻し・保留
// への回答は本人確認つきの操作としてだけ成立する」という不変条件が、
// どの内部ヘルパーを呼んだかだけに依存していた）。
func TestRunTransition_RequiresVerificationMismatchIsRejected(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")

	t.Run("verification-required op via the unverified path", func(t *testing.T) {
		c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
		cid, _ := parseChallengeID(c.ID)
		insertPlanRow(t, s, cid, 1, "x")

		// s.transition は内部でしか呼ばれないヘルパーだが、OpApprove
		// （RequiresVerification）を誤って渡した場合に fail-closed で
		// 拒否されることを直接確認する（同一パッケージのテストだからこそ
		// 検査できる、実装の不変条件）。
		_, err := s.transition(context.Background(), ChannelCLI, c.ID, OpApprove, func(context.Context, *transitionCtx) error {
			return nil
		})
		if err == nil {
			t.Fatal("transition() with a RequiresVerification op unexpectedly succeeded")
		}

		after, getErr := s.GetChallenge(context.Background(), c.ID)
		if getErr != nil {
			t.Fatalf("GetChallenge() error = %v", getErr)
		}
		if after.Version != 1 || after.Status != StatusAwaitingPlanApproval {
			t.Fatalf("challenge changed despite the rejected mismatched call: %+v", after)
		}
	})

	t.Run("non-verification op via the verified path", func(t *testing.T) {
		c := createAndAdvance(t, s, StatusUnclassified)
		att := verifiedAttestationForTest(t, c.ID)

		_, err := s.verifiedTransition(context.Background(), att, c.ID, 1, OpClassify, NoStatus, nil, func(context.Context, *transitionCtx) error {
			return nil
		})
		if err == nil {
			t.Fatal("verifiedTransition() with a non-RequiresVerification op unexpectedly succeeded")
		}
	})
}

// TestTransition_MalformedIDTakesPrecedenceOverActorUnavailable は
// self-review 指摘の再発防止（ラウンド2）: 既存5操作（classify・plan・
// submit・verify・hold）は、不正な ID の検査（ErrNotFound）を actor の解決
// （ErrActorUnavailable）より先に行う。この順序は以前のリファクタで一度
// 入れ替わっていた（actor を解決できない環境で不正な ID を渡すと、
// ErrNotFound〈exit 1〉ではなく internal_error〈exit 2〉になっていた）。
// 順序をコードで戻しただけでは再発を防げないため、テストで固定する。
func TestTransition_MalformedIDTakesPrecedenceOverActorUnavailable(t *testing.T) {
	s := newStoreForTest(t)
	withActorSource(t, actorSourceFuncs{
		userCurrent: func() (*user.User, error) { return nil, errors.New("boom") },
		getenv:      func(string) string { return "" },
	})

	_, err := s.ClassifyChallenge(context.Background(), ChannelCLI, "not-a-valid-id", ClassifyInput{Priority: "P0"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound (malformed ID must be checked before actor resolution)", err)
	}
}

// AC-24: (状態, 操作) の組のうち遷移表に無いものはすべて、invalid_transition
// （完了の課題に対しては terminal_state）で終わり、課題の状態と作業ログが
// 変わらない。dispatchedOperations（Table から導出した dispatchOperation が
// 扱える11操作。T14=approve_hold_release を含む）× allStatuses（語彙 8 状態）
// の直積のうち、Lookup が ok=false の全組を検証する。
func TestDispatchTable_AC24_UndefinedCombinationsAreRejectedAndChangeNothing(t *testing.T) {
	tested := 0
	for _, status := range allStatuses() {
		for _, op := range dispatchedOperations(t) {
			if _, ok := Lookup(status, op); ok {
				continue // 遷移表にある組はAC-23が検証する
			}

			s := newStoreForTest(t)
			fixedActor(t, "alice")
			c := createAndAdvance(t, s, status)

			// approve_hold_release（T14 以外の状態への呼び出し）は
			// PrepareApproval を経由するため、計画承認待ちの状態では
			// AC-23 と同じ理由（PrepareApproval が計画承認待ちに計画行を
			// 要求する）で計画行を用意しておく必要がある（さもないと
			// Lookup の判定に到達する前に「計画が無い」という別のエラーで
			// 失敗し、この AC が検証したい invalid_transition の判定を
			// 確かめられない）。
			if status == StatusAwaitingPlanApproval {
				cid, _ := parseChallengeID(c.ID)
				insertPlanRow(t, s, cid, 1, "plan body")
			}

			res := dispatchOperation(t, s, c.ID, op)
			wantErr := ErrInvalidTransition
			if IsTerminal(Table, StatusVocabulary, status) {
				wantErr = ErrTerminalState
			}
			if !errors.Is(res.err, wantErr) {
				t.Errorf("status=%q op=%q: err = %v, want %v", status, op, res.err, wantErr)
			}

			after, err := s.GetChallenge(context.Background(), c.ID)
			if err != nil {
				t.Fatalf("GetChallenge() error = %v", err)
			}
			if after.Version != 1 || after.Status != status {
				t.Errorf("status=%q op=%q: challenge changed despite rejection: version=%d status=%q", status, op, after.Version, after.Status)
			}
			if len(activitiesFor(t, s, c.ID)) != 1 {
				t.Errorf("status=%q op=%q: activity log grew despite rejection", status, op)
			}
			tested++
		}
	}
	if tested == 0 {
		t.Fatal("no undefined (status, op) combinations were tested")
	}
}

// --- AC-36: 着手中の同じ課題に submit と hold を並行実行すると直列化される ---

// concurrentOpResult は並行に実行した操作 1 件の結果。
type concurrentOpResult struct {
	status Status
	err    error
}

// runConcurrentTransition は 2 つの独立した *Store（別の OpenWorkspace 呼び出し
// ＝別の *sql.DB 接続）を使い、同じ課題への操作 first・second を「first が
// BEGIN IMMEDIATE のトランザクションを保持したまま止まっている間に second を
// 開始する」形で実行し、両方の結果を返す。first が確実に書き込みロックを
// 保持してから second を起動するため、beforeCommit フックで待ち合わせる。
func runConcurrentTransition(t *testing.T, ws string, first, second func(s *Store) (status Status, err error)) (firstRes, secondRes concurrentOpResult) {
	t.Helper()

	storeA, err := OpenWorkspace(ws)
	if err != nil {
		t.Fatalf("OpenWorkspace() A error = %v", err)
	}
	defer func() { _ = storeA.Close() }()
	storeB, err := OpenWorkspace(ws)
	if err != nil {
		t.Fatalf("OpenWorkspace() B error = %v", err)
	}
	defer func() { _ = storeB.Close() }()

	reachedHook := make(chan struct{})
	releaseHook := make(chan struct{})
	var hookOnce, releaseOnce sync.Once
	// t.Fatal で抜ける経路でも first の goroutine を解放する（解放しないと
	// BEGIN IMMEDIATE を保持したまま永遠に待ち、deferred Close の下で
	// トランザクションが開いたままになる。code-reviewer 指摘）。
	defer releaseOnce.Do(func() { close(releaseHook) })
	storeA.beforeCommit = func() {
		hookOnce.Do(func() { close(reachedHook) })
		<-releaseHook
	}

	var wg sync.WaitGroup
	wg.Add(2)

	var aStatus, bStatus Status
	var aErr, bErr error

	go func() {
		defer wg.Done()
		aStatus, aErr = first(storeA)
	}()

	select {
	case <-reachedHook:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for storeA to reach the write lock (beforeCommit hook)")
	}

	secondDone := make(chan struct{})
	go func() {
		defer wg.Done()
		bStatus, bErr = second(storeB)
		close(secondDone)
	}()

	// storeA がロックを保持している短い猶予の間、storeB がまだ返っていない
	// ことを確認する（直列化されていることの傍証）。
	select {
	case <-secondDone:
		t.Fatal("second transition returned before the first released the write lock; not serialized")
	case <-time.After(200 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(releaseHook) })
	wg.Wait()

	return concurrentOpResult{status: aStatus, err: aErr}, concurrentOpResult{status: bStatus, err: bErr}
}

func TestConcurrentSubmitAndHold_HoldFirst_SubmitBecomesInvalidTransition(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusInProgress)
	ws := s.Workspace() // s は setup 専用。以降は独立した2つの Store を使う。

	holdRes, submitRes := runConcurrentTransition(t, ws,
		func(store *Store) (Status, error) {
			c2, err := store.HoldChallenge(context.Background(), ChannelCLI, c.ID, HoldInput{Question: "why?"})
			if err != nil {
				return "", err
			}
			return c2.Status, nil
		},
		func(store *Store) (Status, error) {
			c2, err := store.SubmitChallenge(context.Background(), ChannelCLI, c.ID)
			if err != nil {
				return "", err
			}
			return c2.Status, nil
		},
	)

	if holdRes.err != nil {
		t.Fatalf("hold (first) error = %v, want success", holdRes.err)
	}
	if holdRes.status != StatusAwaitingHuman {
		t.Fatalf("hold (first) status = %q, want %q", holdRes.status, StatusAwaitingHuman)
	}
	if !errors.Is(submitRes.err, ErrInvalidTransition) {
		t.Fatalf("submit (second) error = %v, want ErrInvalidTransition (re-checked against latest state)", submitRes.err)
	}

	final, err := s2Get(t, ws, c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if final.Status != StatusAwaitingHuman {
		t.Fatalf("final status = %q, want %q (only the winning hold applied)", final.Status, StatusAwaitingHuman)
	}
	if final.Version != 2 {
		t.Fatalf("final version = %d, want 2 (only one transition applied)", final.Version)
	}
}

// レビュー指摘・仕様への指摘の対象（ブリーフ§3.4）: submit が先勝ちすると、
// T11 の遷移元に「検証中」が含まれるため、後発の hold は表の上では有効
// （検証中→人間対応待ち）で成功する。AC-36 の文言「他方は invalid_transition」は
// この順では成り立たない。遷移表を正として、hold も成功し最終状態が
// awaiting_human・hold.from_status=verifying になることを検証する。
func TestConcurrentSubmitAndHold_SubmitFirst_HoldAlsoSucceedsBecauseVerifyingIsAValidHoldSource(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusInProgress)
	ws := s.Workspace()

	submitRes, holdRes := runConcurrentTransition(t, ws,
		func(store *Store) (Status, error) {
			c2, err := store.SubmitChallenge(context.Background(), ChannelCLI, c.ID)
			if err != nil {
				return "", err
			}
			return c2.Status, nil
		},
		func(store *Store) (Status, error) {
			c2, err := store.HoldChallenge(context.Background(), ChannelCLI, c.ID, HoldInput{Question: "why?"})
			if err != nil {
				return "", err
			}
			return c2.Status, nil
		},
	)

	if submitRes.err != nil {
		t.Fatalf("submit (first) error = %v, want success", submitRes.err)
	}
	if submitRes.status != StatusVerifying {
		t.Fatalf("submit (first) status = %q, want %q", submitRes.status, StatusVerifying)
	}
	if holdRes.err != nil {
		t.Fatalf("hold (second) error = %v, want success (verifying is a valid hold source per T11)", holdRes.err)
	}
	if holdRes.status != StatusAwaitingHuman {
		t.Fatalf("hold (second) status = %q, want %q", holdRes.status, StatusAwaitingHuman)
	}

	detail, err := s2GetDetail(t, ws, c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if detail.Status != StatusAwaitingHuman {
		t.Fatalf("final status = %q, want %q", detail.Status, StatusAwaitingHuman)
	}
	if detail.Version != 3 {
		t.Fatalf("final version = %d, want 3 (both transitions applied: create-setup=1, submit=2, hold=3)", detail.Version)
	}
	if len(detail.Holds) != 1 || detail.Holds[0].FromStatus != StatusVerifying {
		t.Fatalf("Holds = %+v, want 1 entry with from_status=verifying", detail.Holds)
	}
}

// AC-36 の「両方の実行順を検証する」を、実際に invalid_transition が両順とも
// 観測できる組（verifying の課題への verify --result met × hold）でも検証する。
func TestConcurrentVerifyMetAndHold_BothOrdersProduceOneWinnerAndOneInvalidTransition(t *testing.T) {
	for _, holdFirst := range []bool{true, false} {
		s := newStoreForTest(t)
		fixedActor(t, "alice")
		c := createAndAdvance(t, s, StatusVerifying)
		ws := s.Workspace()

		verify := func(store *Store) (Status, error) {
			c2, err := store.VerifyChallenge(context.Background(), ChannelCLI, c.ID, VerifyInput{Result: "met"})
			if err != nil {
				return "", err
			}
			return c2.Status, nil
		}
		hold := func(store *Store) (Status, error) {
			c2, err := store.HoldChallenge(context.Background(), ChannelCLI, c.ID, HoldInput{Question: "why?"})
			if err != nil {
				return "", err
			}
			return c2.Status, nil
		}

		var firstRes, secondRes concurrentOpResult
		var firstIsHold bool
		if holdFirst {
			firstRes, secondRes = runConcurrentTransition(t, ws, hold, verify)
			firstIsHold = true
		} else {
			firstRes, secondRes = runConcurrentTransition(t, ws, verify, hold)
			firstIsHold = false
		}

		if firstRes.err != nil {
			t.Fatalf("holdFirst=%v: first error = %v, want success", holdFirst, firstRes.err)
		}
		if !errors.Is(secondRes.err, ErrInvalidTransition) {
			t.Fatalf("holdFirst=%v: second error = %v, want ErrInvalidTransition", holdFirst, secondRes.err)
		}

		var wantFinal Status
		if firstIsHold {
			wantFinal = StatusAwaitingHuman
		} else {
			wantFinal = StatusAwaitingCompletionApproval
		}
		final, err := s2Get(t, ws, c.ID)
		if err != nil {
			t.Fatalf("GetChallenge() error = %v", err)
		}
		if final.Status != wantFinal {
			t.Fatalf("holdFirst=%v: final status = %q, want %q", holdFirst, final.Status, wantFinal)
		}
		if final.Version != 2 {
			t.Fatalf("holdFirst=%v: final version = %d, want 2 (only the winner applied)", holdFirst, final.Version)
		}
	}
}

// s2Get / s2GetDetail はテスト検証用に、ws を新たに開いて読み取るだけの小さな
// ヘルパー（runConcurrentTransition が使った2つの Store とは別に、最終状態を
// 読むための3つ目の短命な接続を開く）。
func s2Get(t *testing.T, ws, id string) (*Challenge, error) {
	t.Helper()
	s, err := OpenWorkspace(ws)
	if err != nil {
		return nil, err
	}
	defer func() { _ = s.Close() }()
	detail, err := s.GetChallenge(context.Background(), id)
	if err != nil {
		return nil, err
	}
	return &detail.Challenge, nil
}

func s2GetDetail(t *testing.T, ws, id string) (*ChallengeDetail, error) {
	t.Helper()
	s, err := OpenWorkspace(ws)
	if err != nil {
		return nil, err
	}
	defer func() { _ = s.Close() }()
	return s.GetChallenge(context.Background(), id)
}
