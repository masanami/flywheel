package core

import (
	"context"
	"database/sql"
	"testing"
)

// 委譲先のフローを通らない成果物の反映の依頼でも、コンフリクトの解消あり（反映の段。AC-43・AC-44）は確かめ待ちになる。
// 足場は反映の依頼（reflFixture。反映の依頼を始めた状態）。

func (f *reflFixture) fixRefs(t *testing.T) contractFixRefs {
	t.Helper()
	var r contractFixRefs
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
		r, err = readContractFixRefs(context.Background(), tx, 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return r
}

// conflictFixMovedTo は PR の head を h にし、承認した中身から h への変わり方が、コンフリクトの解消あり（取り込みのマージで、直したファイルがある）になるよう比較を用意する。
func (f *reflFixture) conflictFixMovedTo(h string) {
	f.fake.pr.HeadSHA = h
	f.cmp.set(relHead, h, withFiles(ahead(commit("m-"+h, "main", "m0"), commit(h, "Merge", relHead, "m-"+h)), UpstreamFile{Filename: "a.go", Patch: "RESOLVED"}))
	f.cmp.set("main", h, ahead(commit(relHead, "fix", "p0"), commit(h, "Merge", relHead, "m-"+h)))
	f.cmp.set("m-"+h, h, withFiles(ahead(commit(relHead, "fix", "p0")), UpstreamFile{Filename: "a.go", Patch: "RESOLVED"}))
}

// AC-43: 反映の依頼で、変わり方がコンフリクトの解消あり → 結果は waiting_fix_check で、契約の実行の fix_pending_ref はいまの中身になる。
// AC-44: 反映の操作は approved のまま（承認は取り消されない）で、resume は呼ばれない。
func TestReflectFixCheck_AC43_AC44_ConflictFixRecordsPendingAndKeepsTheApproval(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	f.conflictFixMovedTo("h1")
	f.fake.pr.Checks = []UpstreamCheck{{Status: "completed", Conclusion: "success"}}
	f.queue(reviewFor("h1"))
	item := f.one(t)
	if item.Result != ExecutionResultWaitingFixCheck || item.ObservedHead != "h1" || item.ApprovedHead != relHead { // AC-43
		t.Fatalf("item = %+v, want waiting_fix_check seeing h1", item)
	}
	if got := f.fixRefs(t); got != (contractFixRefs{Pending: "h1"}) { // AC-43
		t.Fatalf("fix refs = %+v, want only pending h1", got)
	}
	f.wantOpState(t, "approved")    // AC-44
	if len(f.client.resumes) != 0 { // AC-44
		t.Fatalf("resumes = %v, want none", f.client.resumes)
	}
	if r := f.only(t); r.kind != "reflect" || r.ended {
		t.Fatalf("run = %+v, want the reflect run still open", r)
	}
}
