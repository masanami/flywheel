package core

import (
	"context"
	"database/sql"
	"testing"
)

// S3a AC-324〜AC-326（見直し前と同じ）: 完了の承認の後の反映の操作の状態と、承認の記録に残る承認した中身。
// 実ストアで、J5 の met → 完了の承認（端末の形。反映を保留にしない）を通す。
func TestS3a_AC325_AC326_TerminalCompletionApprovalApprovesTheReleaseAndRecordsTheApprovedContent(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	f.toVerifying(t)
	ctx := context.Background()
	ref := reviewPRURL
	if _, err := f.s.CreateOperation(ctx, ChannelCLI, OperationInput{ChallengeID: f.cid, Kind: "release", Summary: "PR 1 を反映先へ", Ref: &ref}); err != nil {
		t.Fatal(err)
	}
	f.j5.j5.result = j5Out("met", nil, nil)
	if _, err := f.s.VerifyAutoJ5(ctx, f.j5.input(f.cycle(t, 300), &f.cid)); err != nil {
		t.Fatal(err)
	}
	prev, err := f.s.PrepareApproval(ctx, f.cid)
	if err != nil || len(prev.PendingReleases) != 1 {
		t.Fatalf("PrepareApproval = %+v err %v, want one pending release", prev, err)
	}
	head := "abc123"
	if _, _, err := f.s.ExecuteApproval(ctx, ApprovalRequest{
		ChallengeID: f.cid, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
		ReleaseHeads: []ReleaseHead{{Operation: prev.PendingReleases[0], HeadSHA: &head}},
	}, verifiedAttestationForTest(t, f.cid)); err != nil {
		t.Fatalf("ExecuteApproval: %v", err)
	}
	var state, approvedHead string
	readForTest(t, f.s, func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT state FROM operation WHERE kind = 'release'`).Scan(&state); err != nil {
			return err
		}
		return tx.QueryRow(`SELECT head_sha FROM approval WHERE kind = 'release' AND decision = 'approved'`).Scan(&approvedHead)
	})
	if state != string(OperationStateApproved) {
		t.Errorf("AC-325: release state = %s, want approved", state)
	}
	if approvedHead != head {
		t.Errorf("AC-326: approval head_sha = %q, want %q", approvedHead, head)
	}
}
