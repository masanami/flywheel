package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// headsForAll は prev の未承認の release のすべてを、head "head-<ID>" つきで承認する組にする。
func headsForAll(prev *ApprovalPreview) []ReleaseHead {
	out := make([]ReleaseHead, 0, len(prev.PendingReleases))
	for _, op := range prev.PendingReleases {
		h := "head-" + op.ID
		out = append(out, ReleaseHead{Operation: op, HeadSHA: &h})
	}
	return out
}

// --- approval_head_test.go が共有するフィクスチャ ---

const (
	hdrefA = "https://github.com/o/r/pull/1"
	hdrefB = "https://github.com/o/r/pull/2"
)

// headFixture は完了確認待ちの課題 1 件と、参照つきの未承認の release 2 件（PR 1・PR 2）。
type headFixture struct {
	s     *Store
	c     *Challenge
	relA  *IrreversibleOperation
	relB  *IrreversibleOperation
	runID int64
}

func newHeadFixture(t *testing.T) *headFixture {
	t.Helper()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	mk := func(ref string) *IrreversibleOperation {
		r := ref
		op, err := s.CreateOperation(context.Background(), ChannelCLI, OperationInput{ChallengeID: c.ID, Kind: "release", Summary: "s", Ref: &r})
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	f := &headFixture{s: s, c: c, relA: mk(hdrefA), relB: mk(hdrefB)}
	cid := mustParseChallengeID(t, c.ID)
	setChallengeStatus(t, s, cid, StatusAwaitingCompletionApproval)
	f.runID = coretest.InsertRun(t, s.workspace, coretest.InsertRunInput{
		ChallengeID: int(cid), Kind: "delegate", ChallengeVersion: 1, SessionID: "22222222-2222-4222-8222-222222222222",
		PID: 1, Host: "h", HeartbeatAt: "2026-10-22T00:00:00.000Z", StartedAt: "2026-10-22T00:00:00.000Z",
		MaxBudgetUSD: 1_000_000, BudgetBucket: "impl",
	})
	return f
}

// verified は run の成果物（kind=pr）に ref の行を足し、検証の head を設定する。
func (f *headFixture) verified(t *testing.T, ref, head string) {
	t.Helper()
	coretest.InsertRunArtifact(t, f.s.workspace, f.runID, "pr", ref, "open", "develop")
	coretest.SetRunArtifactVerifiedHead(t, f.s.workspace, f.runID, ref, head)
}

func (f *headFixture) rh(op *IrreversibleOperation, head string) ReleaseHead {
	r := ReleaseHead{Operation: *op}
	if head != "" {
		h := head
		r.HeadSHA = &h
	}
	return r
}

// approve は完了の承認を、①の直後の版で要求する。
func (f *headFixture) approve(t *testing.T, heads []ReleaseHead, excluded []string) error {
	t.Helper()
	prev, err := f.s.PrepareApproval(context.Background(), f.c.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: f.c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
		ReleaseHeads: heads, ExcludedReleaseIDs: excluded,
	}, verifiedAttestationForTest(t, f.c.ID))
	return err
}

// state は release の状態を、approval の head_sha（kind=release・approved の行。無ければ nil）は
// headOf で返す。
func (f *headFixture) state(t *testing.T, op *IrreversibleOperation) OperationState {
	t.Helper()
	oid, _ := parseOperationID(op.ID)
	var st OperationState
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT state FROM operation WHERE id = ?`, oid).Scan(&st)
	})
	return st
}

func (f *headFixture) headOf(t *testing.T, op *IrreversibleOperation) *string {
	t.Helper()
	oid, _ := parseOperationID(op.ID)
	var h sql.NullString
	readForTest(t, f.s, func(tx *sql.Tx) error {
		err := tx.QueryRow(`SELECT head_sha FROM approval WHERE operation_id = ? AND decision = 'approved' ORDER BY id DESC LIMIT 1`, oid).Scan(&h)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	if !h.Valid {
		return nil
	}
	return &h.String
}

func (f *headFixture) dump(t *testing.T) string {
	t.Helper()
	return coretest.DumpTables(t, f.s.workspace, "challenge", "operation", "approval", "activity")
}
