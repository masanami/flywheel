package core

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core/coretest"
)

func rejectCompletionForTest(t *testing.T, f *relFixture) *Approval {
	t.Helper()
	cid := formatChallengeID(f.cid)
	setChallengeStatus(t, f.s, f.cid, StatusAwaitingCompletionApproval)
	reason := "直す"
	prev, err := f.s.PrepareRejection(context.Background(), cid, reason)
	if err != nil {
		t.Fatalf("PrepareRejection() error = %v", err)
	}
	_, approval, err := f.s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: cid, ExpectedVersion: prev.Version, Decision: ApprovalDecisionRejected, Reason: &reason,
	}, verifiedAttestationForTest(t, cid))
	if err != nil {
		t.Fatalf("ExecuteApproval(reject) error = %v", err)
	}
	return approval
}

func opStateReason(t *testing.T, s *Store, opID int64) (state string, reason sql.NullString) {
	t.Helper()
	readForTest(t, s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT state, closed_reason FROM operation WHERE id = ?`, opID).Scan(&state, &reason)
	})
	return
}

func TestRejectCompletion_ApprovedRelease_IsClosedAndNeverMerged(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	approval := rejectCompletionForTest(t, f)
	opID := formatOperationID(f.opID)
	if len(approval.WithdrawnReleases) != 1 || approval.WithdrawnReleases[0] != opID || len(approval.RunningReleases) != 0 {
		t.Fatalf("Withdrawn = %v, Running = %v, want [%s], []", approval.WithdrawnReleases, approval.RunningReleases, opID)
	}
	if st, r := opStateReason(t, f.s, f.opID); st != "closed" || r.String != string(ClosedReasonChallengeRejected) {
		t.Fatalf("operation = %s/%s, want closed/challenge_rejected", st, r.String)
	}
	ids, err := f.s.ListExecutableReleases(context.Background())
	if err != nil || len(ids) != 0 {
		t.Fatalf("ListExecutableReleases() = %v, %v, want none", ids, err)
	}
	res, err := f.s.ExecuteReleases(context.Background(), []string{opID}, f.opts())
	if err != nil {
		t.Fatalf("ExecuteReleases() error = %v", err)
	}
	if len(res.Items) != 0 || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedNotExecutable {
		t.Fatalf("res = %+v, want only not_executable", res)
	}
	if f.fake.getCalls != 0 {
		t.Errorf("upstream was read: %d calls", f.fake.getCalls)
	}
	var n int
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM activity WHERE entity = 'operation' AND action = ? AND after LIKE '%challenge_rejected%'`, string(ActivityActionOpClose)).Scan(&n)
	})
	if n != 1 {
		t.Errorf("close activity entries = %d, want 1", n)
	}
}

func TestRejectCompletion_PendingRelease_IsClosed(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	pending := coretest.InsertReleaseOperation(t, f.s.workspace, int(f.cid), "https://github.com/o/r/pull/9", "pending")
	approval := rejectCompletionForTest(t, f)
	if len(approval.WithdrawnReleases) != 2 {
		t.Fatalf("Withdrawn = %v, want 2", approval.WithdrawnReleases)
	}
	if st, _ := opStateReason(t, f.s, pending); st != "closed" {
		t.Errorf("pending release state = %s, want closed", st)
	}
}

func TestRejectCompletion_RunningRelease_IsNotStopped(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	coretest.InsertOperationExecution(t, f.s.workspace, f.opID, coretest.ExecutionAttempt{
		StartedAt: formatTimestamp(time.Now().UTC()), ApprovedHead: relHead, PID: int64(os.Getpid()), Host: host,
	})
	approval := rejectCompletionForTest(t, f)
	if len(approval.WithdrawnReleases) != 0 || len(approval.RunningReleases) != 1 {
		t.Fatalf("Withdrawn = %v, Running = %v", approval.WithdrawnReleases, approval.RunningReleases)
	}
	if st, _ := opStateReason(t, f.s, f.opID); st != "approved" {
		t.Errorf("running release state = %s, want approved", st)
	}
}

func TestRejectCompletion_ReapprovalExecutesNewRelease(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	f.fake.pr = UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: relHead, MergeCommitSHA: "m3rg3"}
	rejectCompletionForTest(t, f)

	cid := formatChallengeID(f.cid)
	// 委譲の照合（重複を数える createReleaseForPR）が、取り消した release を重複とみなさず新しい release を作る。
	runID := coretest.InsertRun(t, f.s.workspace, coretest.InsertRunInput{
		ChallengeID: int(f.cid), Kind: "delegate", ChallengeVersion: 1, SessionID: "22222222-2222-4222-8222-222222222222",
		PID: 1, Host: "h", HeartbeatAt: "2026-10-22T00:00:00.000Z", StartedAt: "2026-10-22T00:00:00.000Z",
		MaxBudgetUSD: 1_000_000, BudgetBucket: "impl",
	})
	if err := f.s.createReleaseForPR(context.Background(), cid, runID, UpstreamPullRequest{URL: relRef, Title: "again"}); err != nil {
		t.Fatal(err)
	}
	var newID int64
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT id FROM operation WHERE challenge_id = ? AND state = 'pending'`, f.cid).Scan(&newID)
	})
	op := &IrreversibleOperation{ID: formatOperationID(newID)}
	setChallengeStatus(t, f.s, f.cid, StatusAwaitingCompletionApproval)
	prev, err := f.s.PrepareApproval(context.Background(), cid)
	if err != nil {
		t.Fatal(err)
	}
	h := relHead
	if _, _, err := f.s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: cid, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
		ReleaseHeads: []ReleaseHead{{Operation: *op, HeadSHA: &h}},
	}, verifiedAttestationForTest(t, cid)); err != nil {
		t.Fatalf("ExecuteApproval(approve) error = %v", err)
	}
	res, err := f.s.ExecuteReleases(context.Background(), nil, f.opts())
	if err != nil {
		t.Fatalf("ExecuteReleases() error = %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].OperationID != op.ID || res.Items[0].Result != ExecutionResultAlreadyMerged {
		t.Fatalf("res = %+v, want the new release executed", res)
	}
	if st, r := opStateReason(t, f.s, f.opID); st != "closed" || r.String != string(ClosedReasonChallengeRejected) {
		t.Errorf("old release = %s/%s, want closed kept as history", st, r.String)
	}
}

func TestRejectCompletion_StaleAttempt_IsWithdrawn(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	attempt := coretest.InsertOperationExecution(t, f.s.workspace, f.opID, coretest.ExecutionAttempt{
		StartedAt: execTS(1), ApprovedHead: relHead, PID: 1, Host: "other-host",
	})
	approval := rejectCompletionForTest(t, f)
	if len(approval.WithdrawnReleases) != 1 || len(approval.RunningReleases) != 0 {
		t.Fatalf("Withdrawn = %v, Running = %v, want the stale attempt's release withdrawn", approval.WithdrawnReleases, approval.RunningReleases)
	}
	// 止まった試行は取り下げ（withdraw）と同じく interrupted として閉じてから行を閉じる。
	var ended sql.NullString
	var result sql.NullString
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT ended_at, result FROM operation_execution WHERE id = ?`, attempt).Scan(&ended, &result)
	})
	if !ended.Valid || result.String != string(ExecutionResultInterrupted) {
		t.Errorf("stale attempt = ended %v / result %q, want closed as interrupted", ended, result.String)
	}
	if st, r := opStateReason(t, f.s, f.opID); st != "closed" || r.String != string(ClosedReasonChallengeRejected) {
		t.Errorf("operation = %s/%s, want closed/challenge_rejected", st, r.String)
	}
}

// 差し戻しの確認前の要約（PrepareRejection）は、書き込みと同じ規則で、取り消す release と実行中のため残す release を返す。
func TestPrepareRejection_ListsReleasesToWithdrawAndRunning(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	pending := coretest.InsertReleaseOperation(t, f.s.workspace, int(f.cid), "https://github.com/o/r/pull/9", "pending")
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	coretest.InsertOperationExecution(t, f.s.workspace, pending, coretest.ExecutionAttempt{
		StartedAt: formatTimestamp(time.Now().UTC()), ApprovedHead: relHead, PID: int64(os.Getpid()), Host: host,
	})
	setChallengeStatus(t, f.s, f.cid, StatusAwaitingCompletionApproval)
	prev, err := f.s.PrepareRejection(context.Background(), formatChallengeID(f.cid), "直す")
	if err != nil {
		t.Fatal(err)
	}
	if len(prev.ReleasesToWithdraw) != 1 || prev.ReleasesToWithdraw[0] != formatOperationID(f.opID) {
		t.Errorf("ReleasesToWithdraw = %v, want [%s]", prev.ReleasesToWithdraw, formatOperationID(f.opID))
	}
	if len(prev.RunningReleases) != 1 || prev.RunningReleases[0] != formatOperationID(pending) {
		t.Errorf("RunningReleases = %v, want [%s]", prev.RunningReleases, formatOperationID(pending))
	}
}
