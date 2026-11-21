package core

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// 反映の段の、衝突の解消の確かめ（区切り S3c。AC-34〜AC-44・AC-47〜AC-81・AC-101）を、実ストア・偽の契約のコマンド・偽の上流で固定する。
// 足場は、統合先を使わない完了の課題の、レビュー待ちのタスクの実行に対応する承認済みの反映の操作（landFixture）。

type fixCheckFixture struct {
	*landFixture
	cmp *fakeCompare
}

func newFixCheckFixture(t *testing.T) *fixCheckFixture {
	t.Helper()
	f := &fixCheckFixture{landFixture: newLandFixture(t), cmp: newFakeCompare()}
	f.fake.pr.Checks = []UpstreamCheck{{Status: "completed", Conclusion: "success"}}
	return f
}

// conflictFixMove は from から to へ、反映先（main）の最新の取り込みのマージで動き、衝突を解消するために直したファイルがある履歴を置く。
func (f *fixCheckFixture) conflictFixMove(from, to string) {
	f.cmp.set(from, to, withFiles(ahead(commit("m-"+to, "main", "m0"), commit(to, "Merge", from, "m-"+to)), UpstreamFile{Filename: "a.go", Patch: "RESOLVED"}))
	f.cmp.set("main", to, ahead(commit(from, "fix", "p0"), commit(to, "Merge", from, "m-"+to)))
	f.cmp.set("m-"+to, to, withFiles(ahead(commit(from, "fix", "p0")), UpstreamFile{Filename: "a.go", Patch: "RESOLVED"}))
}

// mergeInMove は from から to へ、取り込みだけで動いた履歴を置く（直したファイルは無い）。
func (f *fixCheckFixture) mergeInMove(from, to string) {
	f.cmp.set(from, to, withFiles(ahead(commit("m-"+to, "main", "m0"), commit(to, "Merge", from, "m-"+to)), UpstreamFile{Filename: "a", Patch: "p"}))
	f.cmp.set("main", to, ahead(commit(from, "fix", "p0"), commit(to, "Merge", from, "m-"+to)))
	f.cmp.set("m-"+to, to, ahead(commit(from, "fix", "p0")))
}

// contentMove は from から to へ、取り込み以外の変更が入って動いた履歴を置く。
func (f *fixCheckFixture) contentMove(from, to string) {
	f.cmp.set(from, to, ahead(commit(to, "new work", from)))
	f.cmp.set("main", to, ahead(commit(from, "fix", "p0"), commit(to, "new work", from)))
}

func (f *fixCheckFixture) moveHeadTo(h string) {
	f.fake.pr.HeadSHA = h
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateWaiting, nil, func(st *ContractStatus) {
		st.RequestedAction = &ContractRequestedAction{Kind: "review", Decider: "any", Text: "t", Options: []string{"respond", "land"}}
		st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: reviewPRURL}, {Kind: "commit", Ref: h}}
	})}
}

func (f *fixCheckFixture) exec(t *testing.T, withCompare bool) *ReleaseExecResult {
	t.Helper()
	opts := ReleaseExecOptions{Checks: f.fake, Connectors: f.conn, Agent: &AgentDeclaration{FailureLimit: 3}, Contract: f.client, Trigger: ReleaseExecTriggerOpExec}
	if withCompare {
		opts.Compare = f.cmp
	}
	res, err := f.s.ExecuteReleases(context.Background(), []string{"OP-1"}, opts)
	if err != nil {
		t.Fatalf("ExecuteReleases: %v", err)
	}
	return res
}

func (f *fixCheckFixture) one(t *testing.T) ReleaseExecItem {
	t.Helper()
	res := f.exec(t, true)
	if len(res.Items) != 1 {
		t.Fatalf("ExecuteReleases = %+v, want one item", res)
	}
	return res.Items[0]
}

func (f *fixCheckFixture) refs(t *testing.T) contractFixRefs {
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

func (f *fixCheckFixture) setRefs(t *testing.T, pending, verified, rejected string) {
	t.Helper()
	nz := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE contract_run SET fix_pending_ref = ?, fix_verified_ref = ?, fix_rejected_ref = ?`, nz(pending), nz(verified), nz(rejected))
		return err
	})
}

func (f *fixCheckFixture) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(q, args...).Scan(&n) }); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *fixCheckFixture) challengeStatus(t *testing.T) string {
	t.Helper()
	var s string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(`SELECT status FROM challenge`).Scan(&s) }); err != nil {
		t.Fatal(err)
	}
	return s
}

// AC-34〜AC-38・AC-41・AC-42: 衝突の解消あり → 確かめ待ちの中身はいまの中身になり、承認は取り消されず、課題は完了のまま、resume は呼ばれず、
// 反映の試行の記録が 1 件増えて結果は waiting_fix_check。
func TestReleaseFixCheck_ConflictFixRecordsPendingWithoutCancellingTheApproval(t *testing.T) {
	t.Parallel()
	f := newFixCheckFixture(t)
	f.conflictFixMove(f.head, "h1")
	f.moveHeadTo("h1")
	item := f.one(t)
	if item.Result != ExecutionResultWaitingFixCheck || item.ObservedHead != "h1" || item.ApprovedHead != f.head {
		t.Fatalf("item = %+v, want waiting_fix_check seeing h1", item)
	}
	if got := f.refs(t); got.Pending != "h1" || got.Verified != "" || got.Rejected != "" { // AC-34
		t.Fatalf("fix refs = %+v", got)
	}
	if got := f.opState(t); got != "approved" { // AC-35
		t.Fatalf("operation = %s", got)
	}
	if got := f.challengeStatus(t); got != "done" { // AC-36
		t.Fatalf("challenge = %s", got)
	}
	if len(f.client.resumes) != 0 { // AC-37
		t.Fatalf("resumes = %v", f.client.resumes)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM operation_execution WHERE result = 'waiting_fix_check'`); n != 1 { // AC-38
		t.Fatalf("waiting_fix_check attempts = %d", n)
	}
	if item.ConsecutiveFailures != 0 { // AC-40
		t.Fatalf("consecutive failures = %d", item.ConsecutiveFailures)
	}
}

// AC-39・AC-40: 同じ確かめ待ちの中身のまま、もう一度通しても、試行の記録は増えず、連続の失敗に数えない。
func TestReleaseFixCheck_SamePendingContentDoesNotAddAnAttempt(t *testing.T) {
	t.Parallel()
	f := newFixCheckFixture(t)
	f.conflictFixMove(f.head, "h1")
	f.moveHeadTo("h1")
	f.one(t)
	f.moveHeadTo("h1")
	res := f.exec(t, true)
	if len(res.Items) != 0 || len(res.NotStarted) != 1 {
		t.Fatalf("result = %+v, want no new attempt", res)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM operation_execution WHERE result = 'waiting_fix_check'`); n != 1 {
		t.Fatalf("waiting_fix_check attempts = %d, want 1", n)
	}
	if f.refs(t).Pending != "h1" || f.opState(t) != "approved" || len(f.client.resumes) != 0 {
		t.Fatalf("refs = %+v op = %s resumes = %v", f.refs(t), f.opState(t), f.client.resumes)
	}
}

// AC-47〜AC-51: 確かめを通した中身（fix_verified_ref）と同じで、チェックが通っていれば、いまの中身を付けた land が返る。試行は承認した中身と入れた中身の両方を持つ。
func TestReleaseFixCheck_VerifiedContentLandsWithBothHeadsInTheAttempt(t *testing.T) {
	t.Parallel()
	f := newFixCheckFixture(t)
	f.setRefs(t, "", "h1", "")
	f.moveHeadTo("h1")
	item := f.one(t)
	if item.Result != ExecutionResultLandSent || item.ApprovedHead != f.head || item.ObservedHead != "h1" { // AC-51
		t.Fatalf("item = %+v", item)
	}
	if len(f.client.resumes) != 1 || f.client.resumes[0][len(f.client.resumes[0])-1] != "h1" { // AC-50
		t.Fatalf("resumes = %v", f.client.resumes)
	}
	if got := f.refs(t); got.Verified != "h1" || got.Pending != "" { // AC-49 周辺
		t.Fatalf("fix refs = %+v", got)
	}
	if f.challengeStatus(t) != "done" {
		t.Fatalf("challenge = %s", f.challengeStatus(t))
	}
}

// AC-52: チェックが落ちていれば resume は呼ばれない。AC-53: まだ終わっていなければ resume は呼ばれず waiting_checks。
func TestReleaseFixCheck_VerifiedContentStillNeedsTheChecks(t *testing.T) {
	t.Parallel()
	f := newFixCheckFixture(t)
	f.setRefs(t, "", "h1", "")
	f.fake.pr.Checks = []UpstreamCheck{{Status: "completed", Conclusion: "failure"}}
	f.moveHeadTo("h1")
	if item := f.one(t); item.Result != ExecutionResultChecksFailed || len(f.client.resumes) != 0 { // AC-52
		t.Fatalf("item = %+v resumes = %v", item, f.client.resumes)
	}
	g := newFixCheckFixture(t)
	g.setRefs(t, "", "h1", "")
	g.fake.pr.Checks = []UpstreamCheck{{Status: "queued"}}
	g.moveHeadTo("h1")
	if item := g.one(t); item.Result != ExecutionResultWaitingChecks || len(g.client.resumes) != 0 { // AC-53
		t.Fatalf("item = %+v resumes = %v", item, g.client.resumes)
	}
}

// AC-54・AC-55: 通した中身からさらに動き、変わり方が取り込みだけで、チェックが通っていれば、いまの中身を付けた land が返り、fix_verified_ref は変わらない。
// AC-72: 取り込みだけなら fix_pending_ref は NULL のまま。
func TestReleaseFixCheck_MergeInOnlyAfterVerifiedLandsAndKeepsVerified(t *testing.T) {
	t.Parallel()
	f := newFixCheckFixture(t)
	f.setRefs(t, "", "h1", "")
	f.mergeInMove("h1", "h2")
	f.moveHeadTo("h2")
	item := f.one(t)
	if item.Result != ExecutionResultLandSent || f.client.resumes[0][len(f.client.resumes[0])-1] != "h2" {
		t.Fatalf("item = %+v resumes = %v", item, f.client.resumes)
	}
	if got := f.refs(t); got.Verified != "h1" || got.Pending != "" { // AC-55・AC-72
		t.Fatalf("fix refs = %+v", got)
	}
}

// AC-56: 通した中身からさらに動き、衝突の解消あり → 確かめ待ちの中身はいまの中身になる（通した中身は残る）。
func TestReleaseFixCheck_ConflictFixAfterVerifiedRecordsPendingAndKeepsVerified(t *testing.T) {
	t.Parallel()
	f := newFixCheckFixture(t)
	f.setRefs(t, "", "h1", "")
	f.conflictFixMove("h1", "h2")
	f.moveHeadTo("h2")
	if item := f.one(t); item.Result != ExecutionResultWaitingFixCheck {
		t.Fatalf("item = %+v", item)
	}
	if got := f.refs(t); got.Pending != "h2" || got.Verified != "h1" {
		t.Fatalf("fix refs = %+v", got)
	}
	if f.opState(t) != "approved" || len(f.client.resumes) != 0 {
		t.Fatalf("operation = %s resumes = %v", f.opState(t), f.client.resumes)
	}
}

// AC-58・AC-75: 通した中身からさらに動き、関係のない変更あり → 承認が取り消され、3 列は NULL。
func TestReleaseFixCheck_ContentChangeAfterVerifiedCancelsTheApprovalAndClearsTheRefs(t *testing.T) {
	t.Parallel()
	f := newFixCheckFixture(t)
	f.setRefs(t, "", "h1", "")
	f.contentMove("h1", "h2")
	f.moveHeadTo("h2")
	if item := f.one(t); item.Result != ExecutionResultHeadChanged {
		t.Fatalf("item = %+v", item)
	}
	if f.opState(t) != "pending" || f.refs(t) != (contractFixRefs{}) || len(f.client.resumes) != 0 {
		t.Fatalf("operation = %s refs = %+v resumes = %v", f.opState(t), f.refs(t), f.client.resumes)
	}
}

// AC-59: 確かめ待ちの中身がある間にさらに動き、承認した中身からの変わり方が衝突の解消あり → 確かめ待ちの中身はいまの中身になる。
func TestReleaseFixCheck_MovingAgainWhilePendingRewritesThePending(t *testing.T) {
	t.Parallel()
	f := newFixCheckFixture(t)
	f.setRefs(t, "h1", "", "")
	f.conflictFixMove(f.head, "h2")
	f.moveHeadTo("h2")
	if item := f.one(t); item.Result != ExecutionResultWaitingFixCheck {
		t.Fatalf("item = %+v", item)
	}
	if got := f.refs(t); got.Pending != "h2" {
		t.Fatalf("fix refs = %+v", got)
	}
}

// 見分けの結果が衝突の解消ありでない（起点と同じ・取り込みだけ）のに、確かめ待ちの中身が残っているときは、その試行の結果を書く書き込みで
// 確かめ待ちの中身を消す（もう確かめる中身ではない。J5 が古い中身を確かめない）。通した中身は変えない。
func TestReleaseFixCheck_StalePendingIsClearedWhenTheMoveIsNotAConflictFix(t *testing.T) {
	t.Parallel()
	// waiting は from から to への衝突の解消ありで、この操作の試行が確かめ待ちの中身 to を書いた状態にする。
	waiting := func(t *testing.T, verified, from, to string) *fixCheckFixture {
		t.Helper()
		f := newFixCheckFixture(t)
		f.setRefs(t, "", verified, "")
		f.conflictFixMove(from, to)
		f.moveHeadTo(to)
		if item := f.one(t); item.Result != ExecutionResultWaitingFixCheck {
			t.Fatalf("item = %+v, want waiting_fix_check", item)
		}
		if got := f.refs(t); got.Pending != to {
			t.Fatalf("fix refs = %+v, want pending %s", got, to)
		}
		return f
	}
	t.Run("same as the approved content", func(t *testing.T) {
		t.Parallel()
		f := newFixCheckFixture(t)
		head := f.head
		f = waiting(t, "", head, "h1")
		f.moveHeadTo(f.head)
		if item := f.one(t); item.Result != ExecutionResultLandSent {
			t.Fatalf("item = %+v, want land_sent", item)
		}
		if got := f.refs(t); got.Pending != "" {
			t.Fatalf("fix refs = %+v, want no pending", got)
		}
	})
	t.Run("same as the verified content", func(t *testing.T) {
		t.Parallel()
		f := waiting(t, "h1", "h1", "h2")
		f.moveHeadTo("h1")
		if item := f.one(t); item.Result != ExecutionResultLandSent {
			t.Fatalf("item = %+v, want land_sent", item)
		}
		if got := f.refs(t); got.Pending != "" || got.Verified != "h1" {
			t.Fatalf("fix refs = %+v, want no pending and verified h1", got)
		}
	})
	t.Run("merge-in only", func(t *testing.T) {
		t.Parallel()
		f := newFixCheckFixture(t)
		f = waiting(t, "", f.head, "h1")
		f.mergeInMove(f.head, "h2")
		f.moveHeadTo("h2")
		if item := f.one(t); item.Result != ExecutionResultLandSent {
			t.Fatalf("item = %+v, want land_sent", item)
		}
		if got := f.refs(t); got.Pending != "" {
			t.Fatalf("fix refs = %+v, want no pending", got)
		}
	})
	t.Run("merge-in only while the checks are pending", func(t *testing.T) {
		t.Parallel()
		f := newFixCheckFixture(t)
		f = waiting(t, "", f.head, "h1")
		f.mergeInMove(f.head, "h2")
		f.moveHeadTo("h2")
		f.fake.pr.Checks = []UpstreamCheck{{Status: "in_progress"}}
		if item := f.one(t); item.Result != ExecutionResultWaitingChecks {
			t.Fatalf("item = %+v, want waiting_checks", item)
		}
		if got := f.refs(t); got.Pending != "" {
			t.Fatalf("fix refs = %+v, want no pending", got)
		}
	})
	// この操作の試行が書いたのでない確かめ待ちの中身（1 本の実行に対応する別の反映の操作が書いたもの）は消さない。
	t.Run("a pending content this operation did not write is kept", func(t *testing.T) {
		t.Parallel()
		f := newFixCheckFixture(t)
		f.setRefs(t, "other1", "", "")
		if item := f.one(t); item.Result != ExecutionResultLandSent {
			t.Fatalf("item = %+v, want land_sent", item)
		}
		if got := f.refs(t); got.Pending != "other1" {
			t.Fatalf("fix refs = %+v, want pending other1 kept", got)
		}
	})
}

// AC-64〜AC-68: いまの中身が通らなかった中身と同じ → head_changed、承認が取り消され、resume は呼ばれず、詳細に確かめが通らなかった旨があり、
// 通らなかった中身と通した中身は NULL になる。
func TestReleaseFixCheck_RejectedContentTakesTheHeadChangedPath(t *testing.T) {
	t.Parallel()
	f := newFixCheckFixture(t)
	f.setRefs(t, "", "h0", "h1")
	f.moveHeadTo("h1")
	item := f.one(t)
	if item.Result != ExecutionResultHeadChanged { // AC-64
		t.Fatalf("item = %+v", item)
	}
	if f.opState(t) != "pending" || len(f.client.resumes) != 0 { // AC-65・AC-66
		t.Fatalf("operation = %s resumes = %v", f.opState(t), f.client.resumes)
	}
	var detail string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT detail FROM operation_execution WHERE result = 'head_changed'`).Scan(&detail)
	}); err != nil || !strings.Contains(detail, "conflict-fix check did not pass") { // AC-67
		t.Fatalf("detail = %q err %v", detail, err)
	}
	if f.refs(t) != (contractFixRefs{}) { // AC-68
		t.Fatalf("fix refs = %+v", f.refs(t))
	}
}

// AC-69: 通らなかった中身と違う中身 → 通らなかった中身は NULL になり、起点からの変わり方で分ける。
func TestReleaseFixCheck_ADifferentContentClearsTheRejectedAndClassifiesAgain(t *testing.T) {
	t.Parallel()
	f := newFixCheckFixture(t)
	f.setRefs(t, "", "", "h1")
	f.conflictFixMove(f.head, "h2")
	f.moveHeadTo("h2")
	if item := f.one(t); item.Result != ExecutionResultWaitingFixCheck {
		t.Fatalf("item = %+v", item)
	}
	if got := f.refs(t); got.Rejected != "" || got.Pending != "h2" {
		t.Fatalf("fix refs = %+v", got)
	}
	// 取り込みだけの変わり方でも、通らなかった中身は消える。
	g := newFixCheckFixture(t)
	g.setRefs(t, "", "", "h1")
	g.mergeInMove(g.head, "h3")
	g.moveHeadTo("h3")
	if item := g.one(t); item.Result != ExecutionResultLandSent {
		t.Fatalf("item = %+v", item)
	}
	if got := g.refs(t); got.Rejected != "" {
		t.Fatalf("fix refs = %+v", got)
	}
}

// AC-73・AC-74・AC-76: 関係のない変更あり・判定できない（比較の口が無い）→ 承認が取り消され（S3a のまま）、確かめ待ちの中身は NULL になる。
func TestReleaseFixCheck_ContentChangeOrUndeterminableCancelsTheApproval(t *testing.T) {
	t.Parallel()
	f := newFixCheckFixture(t)
	f.setRefs(t, "hp", "", "")
	f.contentMove(f.head, "h1")
	f.moveHeadTo("h1")
	if item := f.one(t); item.Result != ExecutionResultHeadChanged || f.opState(t) != "pending" || f.refs(t) != (contractFixRefs{}) { // AC-73
		t.Fatalf("item = %+v op = %s refs = %+v", item, f.opState(t), f.refs(t))
	}
	g := newFixCheckFixture(t)
	g.setRefs(t, "hp", "", "")
	g.moveHeadTo("h1")
	res := g.exec(t, false)                                                                                                                        // 比較の口が無い: 判定できない
	if len(res.Items) != 1 || res.Items[0].Result != ExecutionResultHeadChanged || g.opState(t) != "pending" || g.refs(t) != (contractFixRefs{}) { // AC-74・AC-76
		t.Fatalf("res = %+v op = %s refs = %+v", res, g.opState(t), g.refs(t))
	}
}

// AC-79: 課題が完了から戻った（T18）とき、契約の実行の 3 列は NULL になる。
func TestReleaseFixCheck_ReopeningTheChallengeClearsTheThreeColumns(t *testing.T) {
	t.Parallel()
	f := newFixCheckFixture(t)
	f.setRefs(t, "a", "b", "c")
	writeForTest(t, f.s, func(tx *sql.Tx) error { return clearLandRefs(context.Background(), tx, 1) })
	if f.refs(t) != (contractFixRefs{}) {
		t.Fatalf("fix refs = %+v", f.refs(t))
	}
}

// AC-80・AC-81: 契約の実行が終わったとき（反映先へ入った・委譲先が失敗で終わった）、確かめ待ちの中身と通した中身は NULL になる。
func TestReleaseFixCheck_EndingTheContractRunClearsTheRefs(t *testing.T) {
	t.Parallel()
	f := newFixCheckFixture(t)
	f.setRefs(t, "a", "b", "c")
	f.fake.pr = openPR()
	f.fake.pr.URL = reviewPRURL
	f.fake.pr.State = "merged"
	f.fake.pr.HeadSHA = f.head
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateSucceeded, nil, nil)}
	if item := f.one(t); item.Result != ExecutionResultLanded { // AC-80
		t.Fatalf("item = %+v", item)
	}
	if got := f.refs(t); got != (contractFixRefs{}) {
		t.Fatalf("fix refs = %+v", got)
	}
	g := newFixCheckFixture(t)
	g.setRefs(t, "a", "b", "")
	g.client.queue = []ContractLaunchOutput{contractOut(ContractStateFailed, nil, nil)}
	if item := g.one(t); item.Result != ExecutionResultDelegateEnded { // AC-81
		t.Fatalf("item = %+v", item)
	}
	if got := g.refs(t); got != (contractFixRefs{}) {
		t.Fatalf("fix refs = %+v", got)
	}
}

// 確かめを通した中身で反映先へ入った後は、merged_unapproved ではなく landed になる。通した中身が承認した中身へ戻ったときも、承認を取り消さない。
func TestReleaseFixCheck_LandedWithTheVerifiedContentIsLandedAndTheApprovedHeadStillProceeds(t *testing.T) {
	t.Parallel()
	f := newFixCheckFixture(t)
	f.setRefs(t, "", "h1", "")
	f.conflictFixMove(f.head, "h1")
	f.fake.pr.State = "merged"
	f.fake.pr.HeadSHA = "h1"
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateSucceeded, nil, nil)}
	if item := f.one(t); item.Result != ExecutionResultLanded {
		t.Fatalf("item = %+v, want landed", item)
	}
	g := newFixCheckFixture(t)
	g.setRefs(t, "", "h1", "")
	g.moveHeadTo(g.head)
	if item := g.one(t); item.Result != ExecutionResultLandSent {
		t.Fatalf("item = %+v, want land_sent", item)
	}
}
