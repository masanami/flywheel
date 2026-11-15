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

// 取り込みだけの承認は、承認の記録と作業ログの after に検証の head を残す（core が自分で比較して確かめた結果。
// 確かめ方の表は release_head_verify_test.go）。取り込みの判定を伴わない承認は残さない。
func TestExecuteApproval_MergeInOnlyApprovalRecordsTheVerifiedHead(t *testing.T) {
	f := newHeadFixture(t)
	f.verified(t, hdrefA, "v")
	src := sourcesOf(openPRAt("h1", passingChecks()), compareMergeIn())
	if err := f.approveWith(t, []ReleaseHead{f.rh(f.relA, "h1"), f.rh(f.relB, "anything")}, src); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if h := f.headOf(t, f.relA); h == nil || *h != "h1" {
		t.Errorf("head_sha = %v, want the current head (h1)", h)
	}
	if got := f.mergeInOf(t, f.relA); got.String != "v" {
		t.Errorf("merge_in_verified_head = %v, want v", got)
	}
	if got := f.mergeInOf(t, f.relB); got.Valid {
		t.Errorf("merge_in_verified_head of an ordinary approval = %v, want NULL", got)
	}
	var after sql.NullString
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT after FROM activity WHERE entity = 'operation' AND action = 'approve' ORDER BY id ASC LIMIT 1`).Scan(&after)
	})
	if !after.Valid || !strings.Contains(after.String, `"merge_in_verified_head":"v"`) {
		t.Errorf("activity after = %v, want merge_in_verified_head", after)
	}
}

// head が検証の head と一致していれば、比較を読まない（口が無くても承認でき、取り込みの承認としては残さない）。
func TestExecuteApproval_MatchingHeadNeedsNoComparison(t *testing.T) {
	f := newHeadFixture(t)
	f.verified(t, hdrefA, "verifiedA")
	if err := f.approveWith(t, []ReleaseHead{f.rh(f.relA, "verifiedA"), f.rh(f.relB, "x")}, ReleaseHeadSources{}); err != nil {
		t.Fatal(err)
	}
	if got := f.mergeInOf(t, f.relA); got.Valid {
		t.Errorf("merge_in_verified_head = %v, want NULL when the head matches", got)
	}
}

// 取り込みの判定が無ければ、head の不一致は今までどおり conflict。
func TestExecuteApproval_WithoutSourcesTheMismatchStillConflicts(t *testing.T) {
	f := newHeadFixture(t)
	f.verified(t, hdrefA, "verifiedA")
	if err := f.approve(t, []ReleaseHead{f.rh(f.relA, "mergedHead"), f.rh(f.relB, "x")}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if err := f.approveOp(t, f.relA, "mergedHead"); !errors.Is(err, ErrConflict) {
		t.Fatalf("single approval: err = %v, want ErrConflict", err)
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
