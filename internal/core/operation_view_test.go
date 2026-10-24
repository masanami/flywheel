package core

import (
	"context"
	"database/sql"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは M4.5 S1 の「show・status の不可逆操作の要素に足す 6 つのキー」
// （AC-118・AC-131〜AC-137）と、status の食い違い merged_unapproved（AC-103）の core 側を
// 検証する。実ストアで、試行の行は coretest のフィクスチャで作る。

func attempt(t *testing.T, s *Store, opID int64, startSec int, result ExecutionResult, head string) {
	t.Helper()
	a := coretest.ExecutionAttempt{StartedAt: execTS(startSec), ApprovedHead: head}
	if result != "" {
		a.EndedAt, a.Result = execTS(startSec+1), string(result)
	}
	coretest.InsertOperationExecution(t, s.workspace, opID, a)
}

func setOpState(t *testing.T, s *Store, opID int64, state OperationState) {
	t.Helper()
	writeForTest(t, s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE operation SET state = ? WHERE id = ?`, string(state), opID)
		return err
	})
}

func approvedOpOf(t *testing.T, s *Store, opID int64) IrreversibleOperation {
	t.Helper()
	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	for _, op := range ov.ApprovedOperations {
		if op.ID == formatOperationID(opID) {
			return op
		}
	}
	t.Fatalf("operation %d not in ApprovedOperations: %+v", opID, ov.ApprovedOperations)
	return IrreversibleOperation{}
}

func showOpOf(t *testing.T, s *Store, cid, opID int64) IrreversibleOperation {
	t.Helper()
	d, err := s.GetChallenge(context.Background(), formatChallengeID(cid))
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	for _, op := range d.Operations {
		if op.ID == formatOperationID(opID) {
			return op
		}
	}
	t.Fatalf("operation %d not in show operations", opID)
	return IrreversibleOperation{}
}

// AC-134: 試行がまだ無い承認済みの release。
func TestOperationView_ApprovedWithoutAttempts(t *testing.T) {
	s, cid, opID := releaseFixtureForTest(t)
	for name, op := range map[string]IrreversibleOperation{"status": approvedOpOf(t, s, opID), "show": showOpOf(t, s, cid, opID)} {
		if op.ApprovedHead == nil || *op.ApprovedHead != "a1b2c3" {
			t.Errorf("%s: ApprovedHead = %v, want a1b2c3", name, op.ApprovedHead)
		}
		if op.LastExecutionResult != nil || op.ConsecutiveFailures != 0 || op.WaitingSince != nil || op.ExecutedAt != nil || op.MergeCommit != nil {
			t.Errorf("%s: want no execution info, got %+v", name, op)
		}
	}
}

// AC-135: 承認の行が無い release は approved_head が null。
func TestOperationView_NoApprovalRowHasNilApprovedHead(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	cid := mustParseChallengeID(t, c.ID)
	opID := coretest.InsertReleaseOperation(t, s.workspace, int(cid), "https://github.com/o/r/pull/1", string(OperationStatePending))
	if op := showOpOf(t, s, cid, opID); op.ApprovedHead != nil {
		t.Errorf("ApprovedHead = %v, want nil", *op.ApprovedHead)
	}
}

// AC-118: checks_failed → interrupted → not_mergeable の後は 2。直前の試行の結果と待ちの開始も出る。
func TestOperationView_ConsecutiveFailuresAndLastResultAndWaitingSince(t *testing.T) {
	s, cid, opID := releaseFixtureForTest(t)
	attempt(t, s, opID, 10, ExecutionResultChecksFailed, "a1b2c3")
	attempt(t, s, opID, 20, ExecutionResultInterrupted, "a1b2c3")
	attempt(t, s, opID, 30, ExecutionResultNotMergeable, "a1b2c3")
	for name, op := range map[string]IrreversibleOperation{"status": approvedOpOf(t, s, opID), "show": showOpOf(t, s, cid, opID)} {
		if op.ConsecutiveFailures != 2 {
			t.Errorf("%s: ConsecutiveFailures = %d, want 2 (AC-118)", name, op.ConsecutiveFailures)
		}
		if op.LastExecutionResult == nil || *op.LastExecutionResult != ExecutionResultNotMergeable {
			t.Errorf("%s: LastExecutionResult = %v, want not_mergeable", name, op.LastExecutionResult)
		}
		if op.WaitingSince == nil || !op.WaitingSince.Equal(execTime(10)) {
			t.Errorf("%s: WaitingSince = %v, want first attempt start %v", name, op.WaitingSince, execTime(10))
		}
	}
}

// 終了していない試行は last_execution_result が null、待ちの開始は持つ。
func TestOperationView_OpenAttemptHasNilLastResult(t *testing.T) {
	s, _, opID := releaseFixtureForTest(t)
	attempt(t, s, opID, 10, "", "a1b2c3")
	op := approvedOpOf(t, s, opID)
	if op.LastExecutionResult != nil {
		t.Errorf("LastExecutionResult = %v, want nil for an open attempt", *op.LastExecutionResult)
	}
	if op.WaitingSince == nil || !op.WaitingSince.Equal(execTime(10)) {
		t.Errorf("WaitingSince = %v, want %v", op.WaitingSince, execTime(10))
	}
}

// 取り消して承認し直した後の待ちの開始は、承認し直した後の最初の試行。取り消した（pending の）間は持たない。
func TestOperationView_WaitingSinceIsAfterLatestApprovalAndOnlyWhileApproved(t *testing.T) {
	s, cid, opID := releaseFixtureForTest(t)
	attempt(t, s, opID, 10, ExecutionResultChecksFailed, "a1b2c3")
	attempt(t, s, opID, 20, ExecutionResultHeadChanged, "a1b2c3")
	setOpState(t, s, opID, OperationStatePending)
	if op := showOpOf(t, s, cid, opID); op.WaitingSince != nil || op.ConsecutiveFailures != 0 {
		t.Errorf("pending after cancel: WaitingSince=%v ConsecutiveFailures=%d, want nil/0", op.WaitingSince, op.ConsecutiveFailures)
	}
	coretest.InsertReleaseApproval(t, s.workspace, opID, "d4e5f6", execTS(25))
	setOpState(t, s, opID, OperationStateApproved)
	if op := approvedOpOf(t, s, opID); op.WaitingSince != nil || op.ApprovedHead == nil || *op.ApprovedHead != "d4e5f6" {
		t.Errorf("re-approved before any attempt: WaitingSince=%v ApprovedHead=%v, want nil / d4e5f6", op.WaitingSince, op.ApprovedHead)
	}
	attempt(t, s, opID, 30, ExecutionResultWaitingChecks, "d4e5f6")
	attempt(t, s, opID, 40, ExecutionResultMergeFailed, "d4e5f6")
	op := approvedOpOf(t, s, opID)
	if op.WaitingSince == nil || !op.WaitingSince.Equal(execTime(30)) {
		t.Errorf("WaitingSince = %v, want %v", op.WaitingSince, execTime(30))
	}
	if op.ConsecutiveFailures != 1 {
		t.Errorf("ConsecutiveFailures = %d, want 1", op.ConsecutiveFailures)
	}
}

// AC-131・AC-136・AC-137: executed_at・merge_commit は show に出る。executed の操作は status の一覧に出ない。
func TestOperationView_ExecutedShowsExecutedAtAndMergeCommit(t *testing.T) {
	s, cid, opID := releaseFixtureForTest(t)
	attempt(t, s, opID, 10, ExecutionResultMerged, "a1b2c3")
	writeForTest(t, s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE operation SET state = 'executed', executed_at = ?, merge_commit = 'deadbeef' WHERE id = ?`, execTS(11), opID)
		return err
	})
	op := showOpOf(t, s, cid, opID)
	if op.ExecutedAt == nil || !op.ExecutedAt.Equal(execTime(11)) {
		t.Errorf("ExecutedAt = %v, want %v", op.ExecutedAt, execTime(11))
	}
	if op.MergeCommit == nil || *op.MergeCommit != "deadbeef" {
		t.Errorf("MergeCommit = %v, want deadbeef", op.MergeCommit)
	}
	if op.WaitingSince != nil {
		t.Errorf("WaitingSince = %v, want nil once executed", op.WaitingSince)
	}
	if op.LastExecutionResult == nil || *op.LastExecutionResult != ExecutionResultMerged {
		t.Errorf("LastExecutionResult = %v, want merged", op.LastExecutionResult)
	}
}

// AC-76・AC-77: 取り消した release は needs_human.operations に出て、last_execution_result を持つ。
func TestOperationView_CancelledPendingReleaseShowsLastResultInNeedsHuman(t *testing.T) {
	s, _, opID := releaseFixtureForTest(t)
	attempt(t, s, opID, 10, ExecutionResultHeadChanged, "a1b2c3")
	setOpState(t, s, opID, OperationStatePending)
	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	if len(ov.NeedsHumanOperations) != 1 {
		t.Fatalf("NeedsHumanOperations = %+v, want 1", ov.NeedsHumanOperations)
	}
	op := ov.NeedsHumanOperations[0]
	if op.LastExecutionResult == nil || *op.LastExecutionResult != ExecutionResultHeadChanged {
		t.Errorf("LastExecutionResult = %v, want head_changed", op.LastExecutionResult)
	}
}

// AC-103: 直前の試行が merged_unapproved の release は discrepancies に出る。
func TestOverview_Discrepancies_MergedUnapproved(t *testing.T) {
	s, cid, opID := releaseFixtureForTest(t)
	attempt(t, s, opID, 10, ExecutionResultMergedUnapproved, "a1b2c3")
	setOpState(t, s, opID, OperationStatePending)
	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	d := findDiscrepancy(ov.Discrepancies, formatChallengeID(cid))
	if d == nil || !kindsEqual(d.Kinds, []DiscrepancyKind{DiscrepancyKindMergedUnapproved}) || d.OperationID != formatOperationID(opID) {
		t.Fatalf("discrepancy = %+v, want kinds=[merged_unapproved] operation_id=%s", d, formatOperationID(opID))
	}
}

// 直前の試行が merged_unapproved でなければ出さない。差し戻し済みも出さない。上流の食い違いだけの要素は operation_id を持たない。
func TestOverview_Discrepancies_MergedUnapprovedBoundaries(t *testing.T) {
	s, cid, opID := releaseFixtureForTest(t)
	up := bindDiscrepancyFixture(t, s, "closed", "o/r#1", upstreamStateClosed, policyStateInPolicy)
	attempt(t, s, opID, 10, ExecutionResultMergedUnapproved, "a1b2c3")
	attempt(t, s, opID, 20, ExecutionResultChecksFailed, "a1b2c3") // 直前の試行が違う
	ov, _ := s.GetOverview(context.Background())
	if d := findDiscrepancy(ov.Discrepancies, formatChallengeID(cid)); d != nil {
		t.Errorf("discrepancy = %+v, want none when the latest attempt is not merged_unapproved", d)
	}
	d := findDiscrepancy(ov.Discrepancies, up.ID)
	if d == nil || d.OperationID != "" {
		t.Errorf("upstream-only discrepancy = %+v, want no operation_id", d)
	}

	s2, cid2, op2 := releaseFixtureForTest(t)
	attempt(t, s2, op2, 10, ExecutionResultMergedUnapproved, "a1b2c3")
	setOpState(t, s2, op2, OperationStateRejected)
	ov2, _ := s2.GetOverview(context.Background())
	if d := findDiscrepancy(ov2.Discrepancies, formatChallengeID(cid2)); d != nil {
		t.Errorf("rejected release discrepancy = %+v, want none", d)
	}
}

// 同じ課題に上流の食い違いがあれば、その要素の kinds に足して operation_id を持たせる。
func TestOverview_Discrepancies_MergedUnapprovedJoinsUpstreamElement(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := bindDiscrepancyFixture(t, s, "closed", "o/r#1", upstreamStateClosed, policyStateInPolicy)
	cid := mustParseChallengeID(t, ch.ID)
	opID := coretest.InsertReleaseOperation(t, s.workspace, int(cid), "https://github.com/o/r/pull/1", string(OperationStatePending))
	attempt(t, s, opID, 10, ExecutionResultMergedUnapproved, "a1b2c3")
	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	if len(ov.Discrepancies) != 1 {
		t.Fatalf("Discrepancies = %+v, want 1 element", ov.Discrepancies)
	}
	d := ov.Discrepancies[0]
	if !kindsEqual(d.Kinds, []DiscrepancyKind{DiscrepancyKindUpstreamClosed, DiscrepancyKindMergedUnapproved}) || d.OperationID != formatOperationID(opID) {
		t.Errorf("discrepancy = %+v", d)
	}
}

// approve の応答の操作も、承認した head を approved_head に持つ。
func TestExecuteOperationApproval_ResponseCarriesApprovedHead(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	cid := mustParseChallengeID(t, c.ID)
	opID := coretest.InsertReleaseOperation(t, s.workspace, int(cid), "https://github.com/o/r/pull/1", string(OperationStatePending))
	ctx := context.Background()
	prev, err := s.PrepareOperationApproval(ctx, formatOperationID(opID))
	if err != nil {
		t.Fatalf("Prepare error = %v", err)
	}
	op, _, err := s.ExecuteOperationApproval(ctx, OperationApprovalRequest{
		OperationID: prev.OperationID, Decision: ApprovalDecisionApproved, ExpectedVersion: prev.Version,
		ExpectedChallengeVersion: prev.ChallengeVersion, HeadSHA: "abc123",
	}, verifiedAttestationForTest(t, prev.OperationID))
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if op.ApprovedHead == nil || *op.ApprovedHead != "abc123" {
		t.Errorf("ApprovedHead = %v, want abc123", op.ApprovedHead)
	}
}
