package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// 検証の後に head が動き、取り込みだけと判定された release の承認（#485 の D）。② は、CLI が渡した検証の head が
// 最新の検証の head と一致するときだけ、head の不一致でも承認し、検証の head を承認の記録に残す。

func (f *headFixture) mergeInOf(t *testing.T, op *IrreversibleOperation) sql.NullString {
	t.Helper()
	oid, _ := parseOperationID(op.ID)
	var h sql.NullString
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT merge_in_verified_head FROM approval WHERE operation_id = ? AND decision = 'approved' ORDER BY id DESC LIMIT 1`, oid).Scan(&h)
	})
	return h
}

func (f *headFixture) rhMergeIn(op *IrreversibleOperation, head, verified string) ReleaseHead {
	r := f.rh(op, head)
	r.MergeInVerifiedHead = &verified
	return r
}

func TestExecuteApproval_MergeInOnlyApprovesAHeadThatDiffersFromTheVerifiedHead(t *testing.T) {
	f := newHeadFixture(t)
	f.verified(t, hdrefA, "verifiedA")
	if err := f.approve(t, []ReleaseHead{f.rhMergeIn(f.relA, "mergedHead", "verifiedA"), f.rh(f.relB, "anything")}, nil); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if f.state(t, f.relA) != OperationStateApproved {
		t.Errorf("relA = %s, want approved", f.state(t, f.relA))
	}
	if h := f.headOf(t, f.relA); h == nil || *h != "mergedHead" {
		t.Errorf("head_sha = %v, want the current head (mergedHead)", h)
	}
	if got := f.mergeInOf(t, f.relA); got.String != "verifiedA" {
		t.Errorf("merge_in_verified_head = %v, want verifiedA", got)
	}
	// 取り込みの判定を伴わない承認は、検証の head を残さない。
	if got := f.mergeInOf(t, f.relB); got.Valid {
		t.Errorf("merge_in_verified_head of an ordinary approval = %v, want NULL", got)
	}
	// 作業ログの after にも残る。
	var after sql.NullString
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT after FROM activity WHERE entity = 'operation' AND action = 'approve' ORDER BY id ASC LIMIT 1`).Scan(&after)
	})
	if !after.Valid || !strings.Contains(after.String, `"merge_in_verified_head":"verifiedA"`) {
		t.Errorf("activity after = %v, want merge_in_verified_head", after)
	}
}

func TestExecuteApproval_MergeInClaimMustNameTheLatestVerifiedHead(t *testing.T) {
	cases := []struct {
		name     string
		verified string // 空なら検証の head の記録なし
		claim    string
	}{
		{"claim names an older verified head", "verifiedA", "olderHead"},
		{"claim without any verified head", "", "verifiedA"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newHeadFixture(t)
			if tc.verified != "" {
				f.verified(t, hdrefA, tc.verified)
			}
			before := f.dump(t)
			err := f.approve(t, []ReleaseHead{f.rhMergeIn(f.relA, "mergedHead", tc.claim), f.rh(f.relB, "x")}, nil)
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("err = %v, want ErrConflict", err)
			}
			if f.dump(t) != before {
				t.Error("the store changed on a rejected approval")
			}
		})
	}
}

// 取り込みの主張がなければ、head の不一致は今までどおり conflict。
func TestExecuteApproval_WithoutAMergeInClaimTheMismatchStillConflicts(t *testing.T) {
	f := newHeadFixture(t)
	f.verified(t, hdrefA, "verifiedA")
	if err := f.approve(t, []ReleaseHead{f.rh(f.relA, "mergedHead"), f.rh(f.relB, "x")}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

// head が検証の head と一致していれば、取り込みの主張は要らない（あっても取り込みの承認としては残さない）。
func TestExecuteApproval_MatchingHeadIgnoresAMergeInClaim(t *testing.T) {
	f := newHeadFixture(t)
	f.verified(t, hdrefA, "verifiedA")
	if err := f.approve(t, []ReleaseHead{f.rhMergeIn(f.relA, "verifiedA", "verifiedA"), f.rh(f.relB, "x")}, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.mergeInOf(t, f.relA); got.Valid {
		t.Errorf("merge_in_verified_head = %v, want NULL when the head matches", got)
	}
}

func (f *headFixture) approveOpMergeIn(t *testing.T, op *IrreversibleOperation, head, verified string) error {
	t.Helper()
	prev, err := f.s.PrepareOperationApproval(context.Background(), op.ID)
	if err != nil {
		return err
	}
	_, _, err = f.s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: op.ID, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion,
		Decision: ApprovalDecisionApproved, HeadSHA: head, MergeInVerifiedHead: verified,
	}, verifiedAttestationForTest(t, op.ID))
	return err
}

func TestExecuteOperationApproval_MergeInOnly(t *testing.T) {
	f := newHeadFixture(t)
	f.verified(t, hdrefA, "verifiedA")
	if err := f.approveOpMergeIn(t, f.relA, "olderClaim", "other"); !errors.Is(err, ErrConflict) {
		t.Fatalf("claim naming another head: err = %v, want ErrConflict", err)
	}
	if err := f.approveOp(t, f.relA, "mergedHead"); !errors.Is(err, ErrConflict) {
		t.Fatalf("no claim: err = %v, want ErrConflict", err)
	}
	if err := f.approveOpMergeIn(t, f.relA, "mergedHead", "verifiedA"); err != nil {
		t.Fatalf("approve with the claim: %v", err)
	}
	if h := f.headOf(t, f.relA); h == nil || *h != "mergedHead" {
		t.Errorf("head_sha = %v, want mergedHead", h)
	}
	if got := f.mergeInOf(t, f.relA); got.String != "verifiedA" {
		t.Errorf("merge_in_verified_head = %v, want verifiedA", got)
	}
}

// show の承認待ちの release は、J5 の検証の head（記録があるとき）を持つ。承認済み以降・記録なしでは持たない。
func TestGetChallenge_PendingReleaseCarriesTheVerifiedHead(t *testing.T) {
	f := newHeadFixture(t)
	f.verified(t, hdrefA, "verifiedA")
	d, err := f.s.GetChallenge(context.Background(), f.c.ID)
	if err != nil {
		t.Fatal(err)
	}
	var a, b *IrreversibleOperation
	for i := range d.Operations {
		switch d.Operations[i].ID {
		case f.relA.ID:
			a = &d.Operations[i]
		case f.relB.ID:
			b = &d.Operations[i]
		}
	}
	if a == nil || a.VerifiedHeadSHA == nil || *a.VerifiedHeadSHA != "verifiedA" {
		t.Errorf("relA VerifiedHeadSHA = %v, want verifiedA", a)
	}
	if b == nil || b.VerifiedHeadSHA != nil {
		t.Errorf("relB (no record) VerifiedHeadSHA = %v, want nil", b)
	}
}
