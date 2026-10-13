package core

import (
	"context"
	"errors"
	"testing"
)

func uiAttestationForTest(t *testing.T, id string) Attestation {
	t.Helper()
	att, err := Verify(ChannelUI, &fakeVerifier{method: VerificationNone, actor: "alice"}, "summary", id)
	if err != nil {
		t.Fatalf("Verify(ui, none) error = %v", err)
	}
	return att
}

func TestRegistryPermits_PerEntryActionSets(t *testing.T) {
	all := []attestedAction{
		actionApprovePlan, actionApproveCompletion, actionApproveCompletionHold, actionReject,
		actionAnswer, actionBudget, actionOperationApprove, actionOperationReject,
	}
	for _, a := range all {
		if !registryPermits(ChannelCLI, VerificationTTYConfirm, a) {
			t.Errorf("(cli, tty_confirm) must permit %q", a)
		}
		want := a == actionApprovePlan || a == actionApproveCompletionHold
		if got := registryPermits(ChannelUI, VerificationNone, a); got != want {
			t.Errorf("(ui, none) permit %q = %v, want %v", a, got, want)
		}
		if registryPermits(ChannelCLI, VerificationNone, a) || registryPermits(ChannelUI, VerificationTTYConfirm, a) || registryPermits(ChannelInvoker, VerificationNone, a) {
			t.Errorf("unregistered pair must not permit %q", a)
		}
	}
}

func TestExecuteApproval_UIApprovesPlan(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c.ID, 1, "x")
	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, ap, err := s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
	}, uiAttestationForTest(t, c.ID))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.Status != StatusInProgress {
		t.Fatalf("status = %s, want in_progress", got.Status)
	}
	if ap.Kind != ApprovalKindPlan || ap.PlanVersion == nil || *ap.PlanVersion != 1 {
		t.Fatalf("approval = %+v, want plan approval with plan_version 1", ap)
	}
	if ap.Channel != "ui" || ap.Verification != "none" {
		t.Fatalf("approval = %+v, want channel ui / verification none", ap)
	}
}

func TestExecuteApproval_UICompletionHoldOKButReleaseApprovalAndRejectRefused(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c, _, _ := setUpChallengeAwaitingCompletionWithOperations(t, s)
	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	att := uiAttestationForTest(t, c.ID)

	// 保留を外した完了の承認は受けない。
	_, _, err = s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
	}, att)
	if !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("no-hold completion: err = %v, want ErrVerificationRejected", err)
	}
	// 差し戻しは受けない。
	reason := "no"
	_, _, err = s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionRejected, Reason: &reason,
	}, att)
	if !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("reject: err = %v, want ErrVerificationRejected", err)
	}
	after, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != prev.Version || after.Status != StatusAwaitingCompletionApproval {
		t.Fatalf("state changed by refused ops: %+v", after)
	}

	// 保留つきは成立し、release は承認されない。
	got, ap, err := s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved, HoldRelease: true,
	}, att)
	if err != nil {
		t.Fatalf("hold completion: err = %v", err)
	}
	if got.Status != StatusDone || ap.Channel != "ui" || ap.Verification != "none" {
		t.Fatalf("got = %+v approval = %+v", got, ap)
	}
	detail, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range detail.Operations {
		if op.State != OperationStatePending {
			t.Fatalf("operation %s state = %s, want pending (ui must not approve releases)", op.ID, op.State)
		}
	}
	for _, a := range detail.Approvals {
		if a.Kind == ApprovalKindRelease {
			t.Fatalf("unexpected release approval via ui: %+v", a)
		}
	}
}

func TestExecuteAnswer_UIRefused(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	if _, err := s.HoldChallenge(context.Background(), ChannelCLI, c.ID, HoldInput{Question: "why?"}); err != nil {
		t.Fatal(err)
	}
	prev, err := s.PrepareAnswer(context.Background(), c.ID, "because")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.ExecuteAnswer(context.Background(), AnswerRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Answer: "because",
	}, uiAttestationForTest(t, c.ID))
	if !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("err = %v, want ErrVerificationRejected", err)
	}
	after, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusAwaitingHuman || after.Version != prev.Version {
		t.Fatalf("state changed: %+v", after)
	}
}

func TestExecuteBudget_UIRefused(t *testing.T) {
	s := newStoreForTest(t)
	_, err := s.ExecuteBudget(context.Background(), BudgetRequest{ChallengeID: "C-1", ExpectedVersion: 1, PlanVersion: 1, ImplUSD: 80}, uiAttestationForTest(t, "C-1"))
	if !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("err = %v, want ErrVerificationRejected", err)
	}
}

func TestExecuteOperationApproval_UIRefused(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	_, releases, _ := setUpChallengeAwaitingCompletionWithOperations(t, s)
	id := releases[0].ID
	prev, err := s.PrepareOperationApproval(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	reason := "no"
	for _, req := range []OperationApprovalRequest{
		{OperationID: id, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion, Decision: ApprovalDecisionApproved},
		{OperationID: id, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion, Decision: ApprovalDecisionRejected, Reason: &reason},
	} {
		_, _, err := s.ExecuteOperationApproval(context.Background(), req, uiAttestationForTest(t, id))
		if !errors.Is(err, ErrVerificationRejected) {
			t.Fatalf("decision %s: err = %v, want ErrVerificationRejected", req.Decision, err)
		}
	}
}

func TestActionForTransition_CoversExactlyVerificationRows(t *testing.T) {
	for _, tr := range Table {
		_, ok := actionForTransition(tr)
		if ok != tr.RequiresVerification {
			t.Errorf("%s (%s): actionForTransition ok = %v, RequiresVerification = %v", tr.ID, tr.Op, ok, tr.RequiresVerification)
		}
	}
}
