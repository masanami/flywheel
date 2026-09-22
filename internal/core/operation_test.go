package core

import (
	"context"
	"errors"
	"testing"
)

// --- CreateOperation (`op add`) ---

func TestCreateOperation_RegistersPendingOperationAndReturnsID(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	ref := "https://example.com/pr/1"

	op, err := s.CreateOperation(context.Background(), ChannelCLI, OperationInput{
		ChallengeID: c.ID, Kind: "release", Summary: "ship it", Ref: &ref,
	})
	if err != nil {
		t.Fatalf("CreateOperation() error = %v", err)
	}
	if op.ID != "OP-1" {
		t.Errorf("ID = %q, want OP-1", op.ID)
	}
	if op.ChallengeID != c.ID || op.Kind != OperationKindRelease || op.Summary != "ship it" {
		t.Errorf("operation = %+v, unexpected fields", op)
	}
	if op.Ref == nil || *op.Ref != ref {
		t.Errorf("Ref = %v, want %q", op.Ref, ref)
	}
	if op.State != "pending" {
		t.Errorf("State = %q, want pending", op.State)
	}
	if op.Version != 1 {
		t.Errorf("Version = %d, want 1", op.Version)
	}
}

// AC: 続けて登録した不可逆操作の ID は課題をまたいで作成順に単調増加する。
func TestCreateOperation_IDsAreMonotonicAcrossChallenges(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c1 := createAndAdvance(t, s, StatusUnclassified)
	c2 := createAndAdvance(t, s, StatusUnclassified)

	op1, err := s.CreateOperation(context.Background(), ChannelCLI, OperationInput{ChallengeID: c1.ID, Kind: "release", Summary: "a"})
	if err != nil {
		t.Fatalf("CreateOperation() 1 error = %v", err)
	}
	op2, err := s.CreateOperation(context.Background(), ChannelCLI, OperationInput{ChallengeID: c2.ID, Kind: "delete", Summary: "b"})
	if err != nil {
		t.Fatalf("CreateOperation() 2 error = %v", err)
	}
	if op1.ID != "OP-1" || op2.ID != "OP-2" {
		t.Errorf("IDs = %s, %s, want OP-1, OP-2", op1.ID, op2.ID)
	}
}

// op add はその課題の版を1増やす（親要件チケット #4 §アーキテクチャ決定）。
func TestCreateOperation_BumpsChallengeVersion(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified) // version=1

	if _, err := s.CreateOperation(context.Background(), ChannelCLI, OperationInput{ChallengeID: c.ID, Kind: "other", Summary: "x"}); err != nil {
		t.Fatalf("CreateOperation() error = %v", err)
	}

	detail, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if detail.Version != 2 {
		t.Errorf("Version = %d, want 2", detail.Version)
	}
	if len(detail.Operations) != 1 || detail.Operations[0].ID != "OP-1" {
		t.Errorf("Operations = %+v, want 1 entry OP-1", detail.Operations)
	}
}

func TestCreateOperation_InvalidKindIsValidationError(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)

	_, err := s.CreateOperation(context.Background(), ChannelCLI, OperationInput{ChallengeID: c.ID, Kind: "bogus", Summary: "x"})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
}

func TestCreateOperation_EmptySummaryIsValidationError(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)

	for _, summary := range []string{"", "   ", "\t\n"} {
		_, err := s.CreateOperation(context.Background(), ChannelCLI, OperationInput{ChallengeID: c.ID, Kind: "release", Summary: summary})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("summary=%q: err = %v, want ErrValidation", summary, err)
		}
	}
}

func TestCreateOperation_NotFoundForMissingOrMalformedChallengeID(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")

	for _, id := range []string{"C-999", "OP-1", "foo"} {
		_, err := s.CreateOperation(context.Background(), ChannelCLI, OperationInput{ChallengeID: id, Kind: "release", Summary: "x"})
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("id=%q: err = %v, want ErrNotFound", id, err)
		}
	}
}

func TestCreateOperation_DoneChallengeIsTerminalState(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusDone)

	_, err := s.CreateOperation(context.Background(), ChannelCLI, OperationInput{ChallengeID: c.ID, Kind: "release", Summary: "x"})
	if !errors.Is(err, ErrTerminalState) {
		t.Fatalf("err = %v, want ErrTerminalState", err)
	}

	detail, getErr := s.GetChallenge(context.Background(), c.ID)
	if getErr != nil {
		t.Fatalf("GetChallenge() error = %v", getErr)
	}
	if len(detail.Operations) != 0 {
		t.Errorf("Operations = %+v, want none (rejected op add must not register)", detail.Operations)
	}
}

// --- PrepareOperationApproval / PrepareOperationRejection ---

func createPendingOperation(t *testing.T, s *Store, challengeID string, kind string) *IrreversibleOperation {
	t.Helper()
	op, err := s.CreateOperation(context.Background(), ChannelCLI, OperationInput{ChallengeID: challengeID, Kind: kind, Summary: "s"})
	if err != nil {
		t.Fatalf("CreateOperation() error = %v", err)
	}
	return op
}

func TestPrepareOperationApproval_ReturnsPreviewFields(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	ref := "r"
	op, err := s.CreateOperation(context.Background(), ChannelCLI, OperationInput{ChallengeID: c.ID, Kind: "delete", Summary: "remove x", Ref: &ref})
	if err != nil {
		t.Fatalf("CreateOperation() error = %v", err)
	}

	prev, err := s.PrepareOperationApproval(context.Background(), op.ID)
	if err != nil {
		t.Fatalf("PrepareOperationApproval() error = %v", err)
	}
	if prev.OperationID != op.ID || prev.Kind != OperationKindDelete || prev.Summary != "remove x" {
		t.Errorf("preview = %+v, unexpected", prev)
	}
	if prev.Ref == nil || *prev.Ref != ref {
		t.Errorf("Ref = %v, want %q", prev.Ref, ref)
	}
	if prev.ChallengeID != c.ID || prev.ChallengeTitle != c.Title || prev.ChallengeStatus != c.Status {
		t.Errorf("preview challenge fields = %+v, want %s/%s/%s", prev, c.ID, c.Title, c.Status)
	}
	if prev.Version != 1 {
		t.Errorf("Version = %d, want 1", prev.Version)
	}
}

func TestPrepareOperationApproval_NotFoundForMissingOrMalformedID(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	for _, id := range []string{"OP-999", "C-1", "foo"} {
		_, err := s.PrepareOperationApproval(context.Background(), id)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("id=%q: err = %v, want ErrNotFound", id, err)
		}
	}
}

func TestPrepareOperationApproval_AlreadyDecidedIsInvalidTransition(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	op := createPendingOperation(t, s, c.ID, "release")

	prev, err := s.PrepareOperationApproval(context.Background(), op.ID)
	if err != nil {
		t.Fatalf("PrepareOperationApproval() error = %v", err)
	}
	att := verifiedAttestationForTest(t, op.ID)
	if _, _, err := s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: op.ID, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion, Decision: ApprovalDecisionApproved,
	}, att); err != nil {
		t.Fatalf("ExecuteOperationApproval() error = %v", err)
	}

	_, err = s.PrepareOperationApproval(context.Background(), op.ID)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v, want ErrInvalidTransition (already approved)", err)
	}
}

func TestPrepareOperationRejection_EmptyReasonIsValidationErrorBeforeTouchingStore(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	op := createPendingOperation(t, s, c.ID, "release")

	for _, reason := range []string{"", "   ", "\t\n"} {
		_, err := s.PrepareOperationRejection(context.Background(), op.ID, reason)
		if !errors.Is(err, ErrValidation) {
			t.Errorf("reason=%q: err = %v, want ErrValidation", reason, err)
		}
	}
}

// --- ExecuteOperationApproval ---

func TestExecuteOperationApproval_ApprovedRecordsApprovalAndBumpsChallengeVersion(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified) // version=1
	op := createPendingOperation(t, s, c.ID, "release")

	beforeDetail, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	// create=1 -> op add=2.
	if beforeDetail.Version != 2 {
		t.Fatalf("precondition: challenge version = %d, want 2", beforeDetail.Version)
	}

	prev, err := s.PrepareOperationApproval(context.Background(), op.ID)
	if err != nil {
		t.Fatalf("PrepareOperationApproval() error = %v", err)
	}
	att := verifiedAttestationForTest(t, op.ID)
	opOut, approval, err := s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: op.ID, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion, Decision: ApprovalDecisionApproved,
	}, att)
	if err != nil {
		t.Fatalf("ExecuteOperationApproval() error = %v", err)
	}
	if opOut.State != "approved" || opOut.Version != 2 {
		t.Errorf("operation = %+v, want state=approved version=2", opOut)
	}
	if approval.Kind != ApprovalKindRelease || approval.Decision != ApprovalDecisionApproved {
		t.Errorf("approval = %+v, want kind=release decision=approved", approval)
	}
	if approval.OperationID == nil || *approval.OperationID != op.ID {
		t.Errorf("approval.OperationID = %v, want %q", approval.OperationID, op.ID)
	}
	if approval.Actor != "alice" || approval.Channel != string(ChannelCLI) || approval.Verification != string(VerificationTTYConfirm) {
		t.Errorf("approval actor/channel/verification = %+v, unexpected", approval)
	}

	afterDetail, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if afterDetail.Version != 3 {
		t.Errorf("challenge version after approval = %d, want 3 (op add bumped to 2, approval bumps to 3)", afterDetail.Version)
	}
}

func TestExecuteOperationApproval_RejectedRecordsReason(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	op := createPendingOperation(t, s, c.ID, "external_send")

	prev, err := s.PrepareOperationRejection(context.Background(), op.ID, "not ready")
	if err != nil {
		t.Fatalf("PrepareOperationRejection() error = %v", err)
	}
	att := verifiedAttestationForTest(t, op.ID)
	reason := "not ready"
	opOut, approval, err := s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: op.ID, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion, Decision: ApprovalDecisionRejected, Reason: &reason,
	}, att)
	if err != nil {
		t.Fatalf("ExecuteOperationApproval() error = %v", err)
	}
	if opOut.State != "rejected" {
		t.Errorf("State = %q, want rejected", opOut.State)
	}
	if approval.Reason == nil || *approval.Reason != "not ready" {
		t.Errorf("Reason = %v, want %q", approval.Reason, "not ready")
	}
}

// AC: 課題が完了した後でも、残った未承認の release を単独に承認できる（保留した本番反映のため）。
func TestExecuteOperationApproval_WorksAfterChallengeIsDone(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	op := createPendingOperation(t, s, c.ID, "release")
	setChallengeStatus(t, s, mustParseChallengeID(t, c.ID), StatusDone)

	prev, err := s.PrepareOperationApproval(context.Background(), op.ID)
	if err != nil {
		t.Fatalf("PrepareOperationApproval() error = %v", err)
	}
	att := verifiedAttestationForTest(t, op.ID)
	opOut, _, err := s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: op.ID, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion, Decision: ApprovalDecisionApproved,
	}, att)
	if err != nil {
		t.Fatalf("ExecuteOperationApproval() error = %v", err)
	}
	if opOut.State != "approved" {
		t.Errorf("State = %q, want approved", opOut.State)
	}
}

func TestExecuteOperationApproval_VersionMismatchIsConflict(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	op := createPendingOperation(t, s, c.ID, "release")

	prev, err := s.PrepareOperationApproval(context.Background(), op.ID)
	if err != nil {
		t.Fatalf("PrepareOperationApproval() error = %v", err)
	}

	// 別プロセスが先に承認する。
	att1 := verifiedAttestationForTest(t, op.ID)
	if _, _, err := s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: op.ID, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion, Decision: ApprovalDecisionApproved,
	}, att1); err != nil {
		t.Fatalf("first ExecuteOperationApproval() error = %v", err)
	}

	// 古い版のまま再試行すると conflict。
	att2 := verifiedAttestationForTest(t, op.ID)
	_, _, err = s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: op.ID, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion, Decision: ApprovalDecisionApproved,
	}, att2)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

// TestExecuteOperationApproval_ChallengeChangedIsConflict は、①②の間に
// 「課題だけ」が変わった場合の conflict を固定する（self-review 指摘: 上の
// テストは不可逆操作の版と課題の版を同時に古くするため、operation.version の
// 比較で先に conflict になり、課題側の比較へ到達していなかった）。要約は
// 課題の ID・タイトル・状態も見せるため、その前提が崩れたまま本番反映の承認を
// 成立させてはならない（仕様 §承認の conflict 条項）。
func TestExecuteOperationApproval_ChallengeChangedIsConflict(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	op := createPendingOperation(t, s, c.ID, "release")

	prev, err := s.PrepareOperationApproval(context.Background(), op.ID)
	if err != nil {
		t.Fatalf("PrepareOperationApproval() error = %v", err)
	}

	// 別プロセスが課題だけを変える（不可逆操作の版は変わらない）。
	newTitle := "retitled while the summary was on screen"
	if _, err := s.EditChallenge(context.Background(), ChannelCLI, c.ID, EditInput{Title: &newTitle}); err != nil {
		t.Fatalf("EditChallenge() error = %v", err)
	}

	att := verifiedAttestationForTest(t, op.ID)
	_, _, err = s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: op.ID, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion, Decision: ApprovalDecisionApproved,
	}, att)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}

	// 不可逆操作は未承認のまま（承認が成立していない）。
	detail, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	var found *IrreversibleOperation
	for i, o := range detail.Operations {
		if o.ID == op.ID {
			found = &detail.Operations[i]
		}
	}
	if found == nil {
		t.Fatalf("operation %s not found in %+v", op.ID, detail.Operations)
	}
	if found.State != OperationStatePending {
		t.Errorf("operation %s state = %q, want pending (conflict must not approve it)", found.ID, found.State)
	}
}

func TestExecuteOperationApproval_RejectedWithEmptyReasonIsValidationError(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	op := createPendingOperation(t, s, c.ID, "release")
	att := verifiedAttestationForTest(t, op.ID)

	_, _, err := s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: op.ID, ExpectedVersion: 1, Decision: ApprovalDecisionRejected,
	}, att)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
}

func mustParseChallengeID(t *testing.T, id string) int64 {
	t.Helper()
	n, ok := parseChallengeID(id)
	if !ok {
		t.Fatalf("parseChallengeID(%q) failed", id)
	}
	return n
}
