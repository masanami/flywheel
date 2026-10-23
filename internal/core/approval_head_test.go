package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// S1 の承認の時点の head（M45H2・M45H4・M45H5・M45H15）を、core の公開 API だけで検証する。

// AC-16・AC-17: 承認する組と外した組で未承認の release の全体をそろえると、承認する組だけが
// 承認され、承認の行の head_sha に入力の head が残る。外した release は pending のまま。
func TestExecuteApproval_ApprovesOnlyTheGivenReleasesAndRecordsTheirHeads(t *testing.T) {
	f := newHeadFixture(t)
	if err := f.approve(t, []ReleaseHead{f.rh(f.relA, "headA")}, []string{f.relB.ID}); err != nil {
		t.Fatal(err)
	}
	if got := f.state(t, f.relA); got != OperationStateApproved {
		t.Errorf("relA state = %s, want approved", got)
	}
	if h := f.headOf(t, f.relA); h == nil || *h != "headA" {
		t.Errorf("relA head_sha = %v, want headA", h)
	}
	if got := f.state(t, f.relB); got != OperationStatePending {
		t.Errorf("relB (excluded) state = %s, want pending", got)
	}
	if h := f.headOf(t, f.relB); h != nil {
		t.Errorf("relB head_sha = %v, want none", *h)
	}
	d, err := f.s.GetChallenge(context.Background(), f.c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != StatusDone {
		t.Errorf("challenge status = %s, want done", d.Status)
	}
}

func TestExecuteApproval_ApprovesAllGivenReleasesWithEachHead(t *testing.T) {
	f := newHeadFixture(t)
	if err := f.approve(t, []ReleaseHead{f.rh(f.relA, "headA"), f.rh(f.relB, "headB")}, nil); err != nil {
		t.Fatal(err)
	}
	if h := f.headOf(t, f.relA); h == nil || *h != "headA" {
		t.Errorf("relA head = %v", h)
	}
	if h := f.headOf(t, f.relB); h == nil || *h != "headB" {
		t.Errorf("relB head = %v", h)
	}
}

// AC-18・AC-19・AC-20・AC-33: 和集合が全体と一致しない・重複・未知の ID・head が空の入力は、
// 何も書かずに conflict。
func TestExecuteApproval_InvalidReleaseInputIsConflictAndWritesNothing(t *testing.T) {
	cases := []struct {
		name     string
		heads    func(f *headFixture) []ReleaseHead
		excluded func(f *headFixture) []string
	}{
		{"a release is in neither set", func(f *headFixture) []ReleaseHead { return []ReleaseHead{f.rh(f.relA, "h")} }, func(*headFixture) []string { return nil }},
		{"no release is given at all", func(*headFixture) []ReleaseHead { return nil }, func(*headFixture) []string { return nil }},
		{"a release is both approved and excluded",
			func(f *headFixture) []ReleaseHead { return []ReleaseHead{f.rh(f.relA, "h"), f.rh(f.relB, "h")} },
			func(f *headFixture) []string { return []string{f.relA.ID} }},
		{"an approved release is listed twice",
			func(f *headFixture) []ReleaseHead { return []ReleaseHead{f.rh(f.relA, "h"), f.rh(f.relA, "h")} },
			func(f *headFixture) []string { return []string{f.relB.ID} }},
		{"an unknown release id",
			func(f *headFixture) []ReleaseHead { return []ReleaseHead{f.rh(f.relA, "h")} },
			func(f *headFixture) []string { return []string{f.relB.ID, "OP-999"} }},
		{"an empty head", func(f *headFixture) []ReleaseHead { return []ReleaseHead{f.rh(f.relA, ""), f.rh(f.relB, "h")} }, func(*headFixture) []string { return nil }},
		{"a blank head",
			func(f *headFixture) []ReleaseHead { return []ReleaseHead{f.rh(f.relA, "  "), f.rh(f.relB, "h")} },
			func(*headFixture) []string { return nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newHeadFixture(t)
			before := f.dump(t)
			err := f.approve(t, tc.heads(f), tc.excluded(f))
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("err = %v, want ErrConflict", err)
			}
			if after := f.dump(t); after != before {
				t.Errorf("the store changed on a rejected approval")
			}
		})
	}
}

// 未承認の release が無い課題では、空の入力で完了の承認が成立する（和集合は空どうしで一致する）。
func TestExecuteApproval_NoPendingReleasesNeedsNoInput(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	setChallengeStatus(t, s, mustParseChallengeID(t, c.ID), StatusAwaitingCompletionApproval)
	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved},
		verifiedAttestationForTest(t, c.ID)); err != nil {
		t.Fatal(err)
	}
}

// 承認する組の head が最新の検証の head と異なれば conflict（M45H11）。一致、または記録なしなら承認できる。
func TestExecuteApproval_HeadMustMatchTheLatestVerifiedHead(t *testing.T) {
	f := newHeadFixture(t)
	f.verified(t, hdrefA, "verifiedA")
	before := f.dump(t)
	err := f.approve(t, []ReleaseHead{f.rh(f.relA, "other"), f.rh(f.relB, "anything")}, nil)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if f.dump(t) != before {
		t.Error("the store changed on a rejected approval")
	}
	// relB は検証の head の記録なし → 照合せず承認できる。relA は一致。
	if err := f.approve(t, []ReleaseHead{f.rh(f.relA, "verifiedA"), f.rh(f.relB, "anything")}, nil); err != nil {
		t.Fatal(err)
	}
}

// 外した release の head は照合しない（承認しないため）。
func TestExecuteApproval_ExcludedReleaseIsNotComparedWithTheVerifiedHead(t *testing.T) {
	f := newHeadFixture(t)
	f.verified(t, hdrefB, "verifiedB")
	if err := f.approve(t, []ReleaseHead{f.rh(f.relA, "x")}, []string{f.relB.ID}); err != nil {
		t.Fatal(err)
	}
}

// 完了の承認の以外（--hold-release・差し戻し）は release を承認せず、head の入力を要さない。
func TestExecuteApproval_HoldReleaseNeedsNoReleaseInput(t *testing.T) {
	f := newHeadFixture(t)
	prev, err := f.s.PrepareApproval(context.Background(), f.c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: f.c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved, HoldRelease: true,
	}, verifiedAttestationForTest(t, f.c.ID)); err != nil {
		t.Fatal(err)
	}
	if f.state(t, f.relA) != OperationStatePending || f.state(t, f.relB) != OperationStatePending {
		t.Error("releases must stay pending")
	}
}

// --- ① の検証の head ---

// 比べる相手は、(repo, 番号) が一致し verified_head_sha が NULL でない行で run_artifact.id が最大のもの。
// release の参照の末尾の /files は無視する。
func TestApprovalPreview_VerifiedHeadSHAIsTheLatestNonNullRowByID(t *testing.T) {
	f := newHeadFixture(t)
	f.verified(t, hdrefA, "old")
	// 別の PR（番号違い）と、同じ PR の新しい行（id が大きい）。
	f.verified(t, hdrefB, "other-pr")
	f.verified(t, hdrefA, "new")
	// NULL の新しい行は無視される。
	coretest.InsertRunArtifact(t, f.s.workspace, f.runID, "pr", hdrefA, "open", "develop")

	prev, err := f.s.PrepareApproval(context.Background(), f.c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := prev.VerifiedHeadSHAs[f.relA.ID]; got != "new" {
		t.Errorf("relA verified head = %q, want new", got)
	}
	if got := prev.VerifiedHeadSHAs[f.relB.ID]; got != "other-pr" {
		t.Errorf("relB verified head = %q, want other-pr", got)
	}
}

func TestApprovalPreview_NoRecordMeansNoVerifiedHead(t *testing.T) {
	f := newHeadFixture(t)
	prev, err := f.s.PrepareApproval(context.Background(), f.c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(prev.VerifiedHeadSHAs) != 0 {
		t.Errorf("VerifiedHeadSHAs = %v, want none", prev.VerifiedHeadSHAs)
	}
}

func TestOperationApprovalPreview_VerifiedHeadSHA(t *testing.T) {
	f := newHeadFixture(t)
	ref := hdrefA + "/files"
	op, err := f.s.CreateOperation(context.Background(), ChannelCLI, OperationInput{ChallengeID: f.c.ID, Kind: "release", Summary: "s", Ref: &ref})
	if err != nil {
		t.Fatal(err)
	}
	f.verified(t, hdrefA, "vh")
	prev, err := f.s.PrepareOperationApproval(context.Background(), op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prev.VerifiedHeadSHA == nil || *prev.VerifiedHeadSHA != "vh" {
		t.Errorf("VerifiedHeadSHA = %v, want vh", prev.VerifiedHeadSHA)
	}
	none := createPendingOperation(t, f.s, f.c.ID, "release")
	prev2, err := f.s.PrepareOperationApproval(context.Background(), none.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prev2.VerifiedHeadSHA != nil {
		t.Errorf("VerifiedHeadSHA of an operation without a ref = %v, want nil", *prev2.VerifiedHeadSHA)
	}
}

// --- 単独の承認 ---

func (f *headFixture) approveOp(t *testing.T, op *IrreversibleOperation, head string) error {
	t.Helper()
	prev, err := f.s.PrepareOperationApproval(context.Background(), op.ID)
	if err != nil {
		return err
	}
	_, _, err = f.s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: op.ID, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion,
		Decision: ApprovalDecisionApproved, HeadSHA: head,
	}, verifiedAttestationForTest(t, op.ID))
	return err
}

// AC-20: head が空の release の承認は conflict。何も書かない。
func TestExecuteOperationApproval_EmptyHeadIsConflict(t *testing.T) {
	f := newHeadFixture(t)
	before := f.dump(t)
	for _, head := range []string{"", "   "} {
		if err := f.approveOp(t, f.relA, head); !errors.Is(err, ErrConflict) {
			t.Fatalf("head %q: err = %v, want ErrConflict", head, err)
		}
	}
	if f.dump(t) != before {
		t.Error("the store changed on a rejected approval")
	}
}

func TestExecuteOperationApproval_RecordsTheInputHead(t *testing.T) {
	f := newHeadFixture(t)
	if err := f.approveOp(t, f.relA, "headA"); err != nil {
		t.Fatal(err)
	}
	if h := f.headOf(t, f.relA); h == nil || *h != "headA" {
		t.Errorf("head_sha = %v, want headA", h)
	}
}

func TestExecuteOperationApproval_HeadMustMatchTheLatestVerifiedHead(t *testing.T) {
	f := newHeadFixture(t)
	f.verified(t, hdrefA, "verifiedA")
	if err := f.approveOp(t, f.relA, "stale"); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if err := f.approveOp(t, f.relA, "verifiedA"); err != nil {
		t.Fatal(err)
	}
}

// 差し戻しは head を要さず、head_sha は残らない。release 以外の操作の承認も head を要さない。
func TestExecuteOperationApproval_RejectionAndNonReleaseNeedNoHead(t *testing.T) {
	f := newHeadFixture(t)
	reason := "no"
	prev, err := f.s.PrepareOperationRejection(context.Background(), f.relA.ID, reason)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: f.relA.ID, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion,
		Decision: ApprovalDecisionRejected, Reason: &reason,
	}, verifiedAttestationForTest(t, f.relA.ID)); err != nil {
		t.Fatal(err)
	}
	del := createPendingOperation(t, f.s, f.c.ID, "delete")
	if err := f.approveOp(t, del, ""); err != nil {
		t.Fatal(err)
	}
	if h := f.headOf(t, del); h != nil {
		t.Errorf("non-release head_sha = %v, want none", *h)
	}
}

// --- merged_unapproved の承認し直しの拒否（M45H14・M45P13。AC-106・AC-107） ---

func (f *headFixture) revokeAsMergedUnapproved(t *testing.T, op *IrreversibleOperation) {
	t.Helper()
	if err := f.approveOp(t, op, "h"); err != nil {
		t.Fatal(err)
	}
	oid, _ := parseOperationID(op.ID)
	coretest.InsertOperationExecution(t, f.s.workspace, oid, coretest.ExecutionAttempt{
		StartedAt: execTS(0), EndedAt: execTS(1), Result: string(ExecutionResultMergedUnapproved), ApprovedHead: "h",
	})
	// 取り消し: pending へ戻す（取り消しの書き込みは別のチケット。ここでは状態だけを再現する）。
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE operation SET state = 'pending', version = version + 1 WHERE id = ?`, oid)
		return err
	})
}

func assertReapprovalForbidden(t *testing.T, err error) {
	t.Helper()
	var re *ReapprovalForbiddenError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *ReapprovalForbiddenError", err)
	}
	if !errors.Is(err, ErrVerificationRejected) {
		t.Errorf("err must wrap ErrVerificationRejected")
	}
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrInvalidTransition) {
		t.Errorf("err must not be conflict/invalid_transition")
	}
	if msg := err.Error(); !strings.Contains(msg, "re-approval is forbidden") {
		t.Errorf("message %q must state the cause", msg)
	}
}

func TestPrepareOperationApproval_RevokedAsMergedUnapprovedIsRejectedButRejectionPasses(t *testing.T) {
	f := newHeadFixture(t)
	f.revokeAsMergedUnapproved(t, f.relA)
	_, err := f.s.PrepareOperationApproval(context.Background(), f.relA.ID)
	assertReapprovalForbidden(t, err)
	if _, err := f.s.PrepareOperationRejection(context.Background(), f.relA.ID, "r"); err != nil {
		t.Errorf("PrepareOperationRejection() error = %v, want nil (sending back passes)", err)
	}
}

func TestExecuteOperationApproval_RevokedAsMergedUnapprovedIsRejectedEvenWithAStaleVersion(t *testing.T) {
	f := newHeadFixture(t)
	f.revokeAsMergedUnapproved(t, f.relA)
	before := f.dump(t)
	// 版が古くても、判定の順は merged_unapproved が先（conflict にならない）。
	_, _, err := f.s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: f.relA.ID, ExpectedVersion: 1, ExpectedChallengeVersion: 1, Decision: ApprovalDecisionApproved, HeadSHA: "h",
	}, verifiedAttestationForTest(t, f.relA.ID))
	assertReapprovalForbidden(t, err)
	if f.dump(t) != before {
		t.Error("the store changed on a rejected approval")
	}
}

func TestExecuteOperationApproval_RevokedAsMergedUnapprovedCanStillBeSentBack(t *testing.T) {
	f := newHeadFixture(t)
	f.revokeAsMergedUnapproved(t, f.relA)
	reason := "send back"
	prev, err := f.s.PrepareOperationRejection(context.Background(), f.relA.ID, reason)
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := f.s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: f.relA.ID, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion,
		Decision: ApprovalDecisionRejected, Reason: &reason,
	}, verifiedAttestationForTest(t, f.relA.ID))
	if err != nil {
		t.Fatal(err)
	}
	if op.State != OperationStateRejected {
		t.Errorf("state = %s, want rejected", op.State)
	}
}

// 順は 存在 → pending → merged_unapproved → 版。pending でなければ invalid_transition が先。
func TestOperationApproval_CheckOrderNotFoundThenPendingThenMergedUnapproved(t *testing.T) {
	f := newHeadFixture(t)
	if _, err := f.s.PrepareOperationApproval(context.Background(), "OP-999"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing op: err = %v, want ErrNotFound", err)
	}
	// 承認済み（pending でない）で、直前の試行が merged_unapproved の操作: invalid_transition。
	if err := f.approveOp(t, f.relB, "h"); err != nil {
		t.Fatal(err)
	}
	oid, _ := parseOperationID(f.relB.ID)
	coretest.InsertOperationExecution(t, f.s.workspace, oid, coretest.ExecutionAttempt{
		StartedAt: execTS(0), EndedAt: execTS(1), Result: string(ExecutionResultMergedUnapproved),
	})
	if _, err := f.s.PrepareOperationApproval(context.Background(), f.relB.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("Prepare: err = %v, want ErrInvalidTransition", err)
	}
	_, _, err := f.s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: f.relB.ID, ExpectedVersion: 1, ExpectedChallengeVersion: 1, Decision: ApprovalDecisionApproved, HeadSHA: "h",
	}, verifiedAttestationForTest(t, f.relB.ID))
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("Execute: err = %v, want ErrInvalidTransition", err)
	}
}

// 直前の試行の結果が merged_unapproved でなければ（他の結果が最新）、承認し直せる。
func TestOperationApproval_OnlyTheLatestAttemptDecides(t *testing.T) {
	f := newHeadFixture(t)
	f.revokeAsMergedUnapproved(t, f.relA)
	oid, _ := parseOperationID(f.relA.ID)
	coretest.InsertOperationExecution(t, f.s.workspace, oid, coretest.ExecutionAttempt{
		StartedAt: execTS(2), EndedAt: execTS(3), Result: string(ExecutionResultChecksFailed),
	})
	if err := f.approveOp(t, f.relA, "h2"); err != nil {
		t.Fatalf("approve after a later attempt: %v", err)
	}
}

// --- 完了の承認での merged_unapproved の release（M45H16。AC-107a〜AC-107c） ---

// AC-107a: ① は release ごとに、直前の試行の結果を返す（試行のない release は載せない）。
func TestApprovalPreview_LastExecutionResultsIsTheLatestAttemptOfEachRelease(t *testing.T) {
	f := newHeadFixture(t)
	f.revokeAsMergedUnapproved(t, f.relA)
	prev, err := f.s.PrepareApproval(context.Background(), f.c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := prev.LastExecutionResults[f.relA.ID]; got != ExecutionResultMergedUnapproved {
		t.Errorf("relA last result = %q, want merged_unapproved", got)
	}
	if got, ok := prev.LastExecutionResults[f.relB.ID]; ok {
		t.Errorf("relB (no attempt) last result = %q, want none", got)
	}
}

// 直前の試行（id が最大の行）だけが結果になる。
func TestApprovalPreview_LastExecutionResultsUsesOnlyTheLatestAttempt(t *testing.T) {
	f := newHeadFixture(t)
	oid, _ := parseOperationID(f.relA.ID)
	for i, r := range []ExecutionResult{ExecutionResultMergedUnapproved, ExecutionResultHeadChanged} {
		coretest.InsertOperationExecution(t, f.s.workspace, oid, coretest.ExecutionAttempt{
			StartedAt: execTS(2 * i), EndedAt: execTS(2*i + 1), Result: string(r),
		})
	}
	prev, err := f.s.PrepareApproval(context.Background(), f.c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := prev.LastExecutionResults[f.relA.ID]; got != ExecutionResultHeadChanged {
		t.Errorf("relA last result = %q, want head_changed", got)
	}
}

// AC-107b: 承認する組に merged_unapproved の release が入っていたら、何も書かず verification_rejected。
func TestExecuteApproval_RevokedAsMergedUnapprovedInTheApprovedSetIsRejectedAndWritesNothing(t *testing.T) {
	f := newHeadFixture(t)
	f.revokeAsMergedUnapproved(t, f.relA)
	before := f.dump(t)
	err := f.approve(t, []ReleaseHead{f.rh(f.relA, "h"), f.rh(f.relB, "h")}, nil)
	assertReapprovalForbidden(t, err)
	if f.dump(t) != before {
		t.Error("the store changed on a rejected approval")
	}
}

// AC-107c: 外した組に入っていれば受け付け、その release は pending のまま残る。
func TestExecuteApproval_RevokedAsMergedUnapprovedInTheExcludedSetIsAccepted(t *testing.T) {
	f := newHeadFixture(t)
	f.revokeAsMergedUnapproved(t, f.relA)
	if err := f.approve(t, []ReleaseHead{f.rh(f.relB, "h")}, []string{f.relA.ID}); err != nil {
		t.Fatal(err)
	}
	if got := f.state(t, f.relA); got != OperationStatePending {
		t.Errorf("relA state = %s, want pending", got)
	}
	if got := f.state(t, f.relB); got != OperationStateApproved {
		t.Errorf("relB state = %s, want approved", got)
	}
}
