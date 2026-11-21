package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// このファイルは、統合先宛ての衝突の解消の確かめ（区切り S3c）のうち、中身が動いたときの記録（settleMovedContent）・確かめ結果の書き込み
// （writeIntegrationFix）・確かめ待ちの中身の後始末を、実ストア・偽の ContractClient・偽の上流で固定する。

const (
	otherContent = "dddddddd"
)

func artsOf(ref string) []ContractArtifact {
	return []ContractArtifact{{Kind: "commit", Ref: ref}, integBranchArt, integCommitArt}
}

// setMovesTo は、検証した中身（landContent）から ref へ、宛先の最新（取り込みのコミット m）を取り込むマージで動いた履歴を置く。
// withFixes が真なら、衝突を解消するために直したファイル a.go がある。
func (f *igLandFixture) setMovesTo(ref, m string, withFixes bool) {
	f.compare.set(landContent, ref, ahead(commit(m, "main change", "m0"), commit(ref, "Merge branch 'feat/integration-1'", landContent, m)))
	f.compare.set(integBranch, ref, ahead(commit("p0", "feat", "m0"), commit(landContent, "fix", "p0"), commit(ref, "Merge branch 'feat/integration-1'", landContent, m)))
	files := []UpstreamFile(nil)
	if withFixes {
		files = []UpstreamFile{{Filename: "a.go", Patch: "@@ -1 +1 @@\n-old\n+RESOLVED"}}
		f.compare.set(landContent, ref, withFiles(f.compare.results[[2]string{landContent, ref}], files...))
	}
	f.compare.set(m, ref, UpstreamComparison{Status: UpstreamCompareAhead, TotalCommits: 1, Commits: []UpstreamCommit{commit(landContent, "fix", "p0")}, Files: files})
}

// setContentChangedTo は、検証した中身から ref へ、取り込み以外の変更が入って動いた履歴を置く。
func (f *igLandFixture) setContentChangedTo(ref string) {
	f.compare.set(landContent, ref, ahead(commit(ref, "new work", landContent)))
	f.compare.set(integBranch, ref, ahead(commit(landContent, "fix", "p0"), commit(ref, "new work", landContent)))
}

// fixInput は、確かめの段の入力（比較の口・取り出しの口・宣言を足したもの）。
func (f *igLandFixture) fixInput(t *testing.T) J5AutoInput {
	t.Helper()
	in := f.j5.input(f.cycle(t, 300), nil)
	in.Compare, in.HeadGit, in.Connectors = f.compare, &fixHeadGit{}, f.conn
	return in
}

func (f *igLandFixture) verifyFix(t *testing.T) *JudgmentAutoResult {
	t.Helper()
	res, err := f.s.VerifyAutoJ5(context.Background(), f.fixInput(t))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (f *igLandFixture) conflictFixCount(t *testing.T) int64 {
	return f.count(t, `SELECT COUNT(*) FROM activity WHERE action = 'conflict_fix_passed'`)
}

// newConflictFixed は、検証した中身（landContent）から衝突の解消ありで newContent へ動いたレビュー待ちを読み、確かめ待ちの状態にした足場。
func newConflictFixed(t *testing.T) *igLandFixture {
	t.Helper()
	f := newIgLandFixture(t)
	f.setMovesTo(newContent, "m1", true)
	f.checks.commits[newContent] = igPassingChecks
	f.sql(t, `UPDATE contract_run SET land_ref = ?, land_delivered = 1`, landContent)
	f.read(t, intakeOut(integOptsWithLd, artsOf(newContent), nil))
	return f
}

// AC-1〜AC-6: 変わり方が衝突の解消あり → 確かめ待ちの中身がいまの中身になり、検証した中身は残る。通す中身と届いた記録は消え、resume は呼ばれず、タスクは検証中のまま。
func TestSettleMovedContent_ConflictFixRecordsPendingAndKeepsVerified(t *testing.T) {
	t.Parallel()
	f := newConflictFixed(t)                                   // チェックが通っていても、resume は呼ばない
	if got := f.ref(t, "fix_pending_ref"); got != newContent { // AC-1
		t.Fatalf("fix_pending_ref = %q", got)
	}
	if got := f.ref(t, "verified_ref"); got != landContent { // AC-2
		t.Fatalf("verified_ref = %q", got)
	}
	if got := f.ref(t, "land_ref"); got != "" { // AC-3
		t.Fatalf("land_ref = %q", got)
	}
	if f.count(t, `SELECT land_delivered FROM contract_run`) != 0 { // AC-4
		t.Fatal("land_delivered was kept")
	}
	if len(f.client.resumes) != 0 { // AC-5
		t.Fatalf("resumes = %d", len(f.client.resumes))
	}
	if f.taskStatus(t) != "verifying" { // AC-6
		t.Fatalf("task = %s", f.taskStatus(t))
	}
}

// AC-27: 確かめ待ちの中身がある間にさらに動き、検証した中身からの変わり方が衝突の解消あり → 確かめ待ちの中身はいまの中身に改まる（検証した中身は残る）。
func TestSettleMovedContent_ConflictFixAgainRewritesPending(t *testing.T) {
	t.Parallel()
	f := newConflictFixed(t)
	f.setMovesTo(otherContent, "m2", true)
	f.read(t, intakeOut(integOptsWithLd, artsOf(otherContent), nil))
	if f.ref(t, "fix_pending_ref") != otherContent || f.ref(t, "verified_ref") != landContent || len(f.client.resumes) != 0 {
		t.Fatalf("pending = %q, verified = %q, resumes = %d", f.ref(t, "fix_pending_ref"), f.ref(t, "verified_ref"), len(f.client.resumes))
	}
}

// AC-28: 確かめ待ちの中身がある間にさらに動き、検証した中身からの変わり方が関係のない変更あり → 確かめ待ちの中身と検証した中身は両方 NULL。
func TestSettleMovedContent_ContentChangedWhilePendingClearsBoth(t *testing.T) {
	t.Parallel()
	f := newConflictFixed(t)
	f.setContentChangedTo(otherContent)
	f.read(t, intakeOut(integOptsWithLd, artsOf(otherContent), nil))
	if f.ref(t, "fix_pending_ref") != "" || f.ref(t, "verified_ref") != "" || len(f.client.resumes) != 0 || f.taskStatus(t) != "verifying" {
		t.Fatalf("pending = %q, verified = %q", f.ref(t, "fix_pending_ref"), f.ref(t, "verified_ref"))
	}
}

// AC-24: 取り込みだけ → 確かめ待ちの中身は NULL のまま（あっても消える）。S3b のまま、検証した中身は改まり、「入れてよい」が返る。
func TestSettleMovedContent_MergeInOnlyKeepsPendingNull(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.setMovesTo(newContent, "m1", false)
	f.checks.commits[newContent] = igPassingChecks
	f.sql(t, `UPDATE contract_run SET fix_pending_ref = 'stale01'`)
	f.read(t, intakeOut(integOptsWithLd, artsOf(newContent), nil))
	if f.ref(t, "fix_pending_ref") != "" || f.ref(t, "verified_ref") != newContent || len(f.client.resumes) != 1 {
		t.Fatalf("pending = %q, verified = %q, resumes = %d", f.ref(t, "fix_pending_ref"), f.ref(t, "verified_ref"), len(f.client.resumes))
	}
}

// AC-11〜AC-13・AC-99〜AC-105: 確かめが満たしたまま → 検証した中身は確かめ待ちだった中身になり、確かめ待ちの中身は NULL。
// 作業ログに conflict_fix_passed が 1 件残る。次の状態の確認で、チェックが通っていれば、通した中身を付けた land が返る。
func TestConflictFixCheck_MetPassesTheContentAndLandsNext(t *testing.T) {
	t.Parallel()
	f := newConflictFixed(t)
	f.setVerdict("met")
	f.verifyFix(t)
	if f.ref(t, "verified_ref") != newContent { // AC-11
		t.Fatalf("verified_ref = %q", f.ref(t, "verified_ref"))
	}
	if f.ref(t, "fix_pending_ref") != "" { // AC-12
		t.Fatalf("fix_pending_ref = %q", f.ref(t, "fix_pending_ref"))
	}
	if f.ref(t, "fix_verified_ref") != "" {
		t.Fatalf("fix_verified_ref = %q, the integration destination does not use it", f.ref(t, "fix_verified_ref"))
	}
	if f.taskStatus(t) != "verifying" || f.status(t, f.cid) != StatusInProgress {
		t.Fatal("the check moved the task or the challenge")
	}
	if n := f.conflictFixCount(t); n != 1 { // AC-99
		t.Fatalf("conflict_fix_passed records = %d", n)
	}
	var entity string
	var entityID int64
	var before, after string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT entity, entity_id, before, after FROM activity WHERE action = 'conflict_fix_passed'`).Scan(&entity, &entityID, &before, &after)
	}); err != nil {
		t.Fatal(err)
	}
	if entity != "task" || entityID != f.count(t, `SELECT id FROM task`) { // AC-100
		t.Fatalf("entity = %s/%d", entity, entityID)
	}
	var b struct{ Ref string }
	var a struct {
		Ref   string
		Files []string
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal([]byte(before), &b); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(after), &a); err != nil {
		t.Fatal(err)
	}
	lastJ5 := f.text(t, `SELECT 'R-' || MAX(id) FROM run WHERE judgment = 'J5'`)
	if b.Ref != landContent || a.Ref != newContent || len(a.Files) != 1 || a.Files[0] != "a.go" || a.RunID != lastJ5 { // AC-102〜AC-105
		t.Fatalf("before = %s, after = %s (J5 run %s)", before, after, lastJ5)
	}
	if len(f.client.resumes) != 0 {
		t.Fatal("land was returned by the check itself")
	}
	f.read(t, intakeOut(integOptsWithLd, artsOf(newContent), nil)) // AC-13
	if cmd := strings.Join(f.lastResume(t), " "); len(f.client.resumes) != 1 || !strings.Contains(cmd, "--approved "+newContent) {
		t.Fatalf("resumes = %d, last = %q", len(f.client.resumes), cmd)
	}
}

// AC-14〜AC-17・AC-106: 確かめが満たしていない → タスクは着手中に戻り、両方の中身は NULL。J5 の指摘が respond で委譲先へ返る。記録は増えない。
func TestConflictFixCheck_NotMetReturnsTaskAndRespondsWithFeedback(t *testing.T) {
	t.Parallel()
	f := newConflictFixed(t)
	f.setVerdict("not_met")
	f.verifyFix(t)
	if f.taskStatus(t) != "in_progress" { // AC-14
		t.Fatalf("task = %s", f.taskStatus(t))
	}
	if f.ref(t, "verified_ref") != "" || f.ref(t, "fix_pending_ref") != "" { // AC-15・AC-16
		t.Fatalf("verified = %q, pending = %q", f.ref(t, "verified_ref"), f.ref(t, "fix_pending_ref"))
	}
	if f.conflictFixCount(t) != 0 { // AC-106
		t.Fatal("conflict_fix_passed was recorded")
	}
	// タスクの検証の「満たしていない」と同じ記録（作業ログ task_verify と発生 verification_not_met）。
	if f.count(t, `SELECT COUNT(*) FROM activity WHERE action = 'task_verify'`) != 1 ||
		f.count(t, `SELECT COUNT(*) FROM occurrence WHERE kind = 'verification_not_met'`) != 1 {
		t.Fatal("task_verify / verification_not_met was not recorded once")
	}
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f.read(t, intakeOut(integOptsWithLd, artsOf(newContent), nil))
	if cmd := strings.Join(f.lastResume(t), " "); !strings.Contains(cmd, "respond") || !strings.Contains(cmd, "FEEDBACK-TEXT") { // AC-17
		t.Fatalf("resume = %q, want respond carrying the J5 feedback", cmd)
	}
}

// AC-18〜AC-22・AC-107: 確かめが判断できない → 両方の中身は NULL、タスクは検証中のまま、課題は保留にならない。記録は増えない。
// 次の J5 で、そのタスクはタスクの検証の対象になる。
func TestConflictFixCheck_UncertainClearsBothAndTheTaskIsVerifiedAgain(t *testing.T) {
	t.Parallel()
	f := newConflictFixed(t)
	f.setVerdict("uncertain")
	f.verifyFix(t)
	if f.ref(t, "verified_ref") != "" || f.ref(t, "fix_pending_ref") != "" { // AC-18・AC-19
		t.Fatalf("verified = %q, pending = %q", f.ref(t, "verified_ref"), f.ref(t, "fix_pending_ref"))
	}
	if f.taskStatus(t) != "verifying" || f.status(t, f.cid) != StatusInProgress { // AC-20・AC-21
		t.Fatalf("task = %s, challenge = %s", f.taskStatus(t), f.status(t, f.cid))
	}
	if f.conflictFixCount(t) != 0 { // AC-107
		t.Fatal("conflict_fix_passed was recorded")
	}
	f.setVerdict("met")
	f.verifyFix(t) // AC-22
	if f.ref(t, "verified_ref") != newContent || f.conflictFixCount(t) != 0 {
		t.Fatalf("verified_ref = %q: the next J5 should be the task verification", f.ref(t, "verified_ref"))
	}
}

// AC-23: J5 の呼び出しが連続の失敗の上限まで失敗した → 両方の中身は NULL（タスクは検証中のまま、課題は保留にならない）。
func TestConflictFixCheck_FailureLimitClearsBoth(t *testing.T) {
	t.Parallel()
	f := newConflictFixed(t)
	f.j5.j5.result = JudgmentLaunchOutput{Result: RunResultErrored, ErrorSummary: "boom"}
	limit := judgmentFailureLimit(f.j5.agent)
	for i := 0; i < limit; i++ {
		f.verifyFix(t)
		if f.ref(t, "fix_pending_ref") != newContent {
			t.Fatalf("pending cleared before the limit (attempt %d)", i+1)
		}
	}
	f.verifyFix(t)
	if f.ref(t, "verified_ref") != "" || f.ref(t, "fix_pending_ref") != "" {
		t.Fatalf("verified = %q, pending = %q", f.ref(t, "verified_ref"), f.ref(t, "fix_pending_ref"))
	}
	if f.taskStatus(t) != "verifying" || f.status(t, f.cid) != StatusInProgress || f.conflictFixCount(t) != 0 {
		t.Fatalf("task = %s, challenge = %s", f.taskStatus(t), f.status(t, f.cid))
	}
}

// AC-29・AC-30・AC-32: 結果を書く直前に、中身の識別が確かめ待ちの中身と違う・課題が保留中なら、結果を捨てる（検証した中身も確かめ待ちの中身も変わらない）。
func TestConflictFixCheck_DiscardedResultLeavesBothRefs(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"AC-29 content moved on": `UPDATE contract_run SET content_ref = 'newer01'`,
		"AC-30 challenge held":   `UPDATE challenge SET status = 'awaiting_human'`,
	}
	for name, q := range cases {
		q := q
		for _, verdict := range []string{"met", "not_met", "uncertain"} {
			verdict := verdict
			t.Run(name+"/"+verdict, func(t *testing.T) {
				t.Parallel()
				f := newConflictFixed(t)
				f.setVerdict(verdict)
				f.j5.j5.onInvoke = func(JudgmentLaunchInput) { f.sql(t, q) }
				f.verifyFix(t)
				if f.ref(t, "verified_ref") != landContent || f.ref(t, "fix_pending_ref") != newContent {
					t.Fatalf("verified = %q, pending = %q", f.ref(t, "verified_ref"), f.ref(t, "fix_pending_ref"))
				}
				if f.taskStatus(t) != "verifying" || f.conflictFixCount(t) != 0 {
					t.Fatalf("task = %s", f.taskStatus(t))
				}
			})
		}
	}
}

// AC-31: 確かめ待ちの中身がある契約の実行が終わった（failed）→ 確かめ待ちの中身は NULL。
func TestConflictFixPending_ClearedWhenTheRunFails(t *testing.T) {
	t.Parallel()
	f := newConflictFixed(t)
	f.read(t, contractOut(ContractStateFailed, nil, nil))
	if f.ref(t, "fix_pending_ref") != "" || f.ref(t, "verified_ref") != "" {
		t.Fatalf("pending = %q, verified = %q", f.ref(t, "fix_pending_ref"), f.ref(t, "verified_ref"))
	}
}

// AC-31（終わり）・AC-32: 確かめ待ちの中身は、タスクが着手中へ戻ったときに消える。課題が保留になっても消えない。
func TestConflictFixPending_ClearedWhenTheTaskReturnsToInProgress(t *testing.T) {
	t.Parallel()
	f := newConflictFixed(t)
	f.sql(t, `UPDATE challenge SET status = 'awaiting_human'`)
	if f.ref(t, "fix_pending_ref") != newContent { // AC-32
		t.Fatalf("pending = %q after the hold", f.ref(t, "fix_pending_ref"))
	}
	f.sql(t, `UPDATE challenge SET status = 'in_progress'`)
	tid := f.count(t, `SELECT id FROM task`)
	if err := taskTx(t, f.s, func(tx *sql.Tx, rec *activityRecorder) error {
		_, err := setTaskStatus(context.Background(), tx, rec, tid, TaskStatusInProgress)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if f.ref(t, "fix_pending_ref") != "" {
		t.Fatalf("pending = %q, want cleared", f.ref(t, "fix_pending_ref"))
	}
}

// 確かめ待ちの中身のままの読みでも、毎回、検証した中身からの変わり方を見分け直す。結果が衝突の解消ありのままなら、列は変えない。
func TestSettleMovedContent_SamePendingContentComparesAgainAndKeepsColumns(t *testing.T) {
	t.Parallel()
	f := newConflictFixed(t)
	calls := len(f.compare.calls)
	f.read(t, intakeOut(integOptsWithLd, artsOf(newContent), nil))
	if len(f.compare.calls) == calls {
		t.Fatal("the comparison was not made again")
	}
	if f.ref(t, "verified_ref") != landContent || f.ref(t, "fix_pending_ref") != newContent || len(f.client.resumes) != 0 {
		t.Fatalf("verified = %q, pending = %q, resumes = %d", f.ref(t, "verified_ref"), f.ref(t, "fix_pending_ref"), len(f.client.resumes))
	}
}

// 確かめ待ちの中身のままの読みで、比較の結果が関係のない変更ありに変わったら、両方を消し、タスクの検証の対象に戻る（J5 の対象選びが拾う）。
func TestSettleMovedContent_SamePendingContentNowChangedClearsBoth(t *testing.T) {
	t.Parallel()
	f := newConflictFixed(t)
	f.setContentChangedTo(newContent)
	f.read(t, intakeOut(integOptsWithLd, artsOf(newContent), nil))
	if f.ref(t, "verified_ref") != "" || f.ref(t, "fix_pending_ref") != "" || f.taskStatus(t) != "verifying" || len(f.client.resumes) != 0 {
		t.Fatalf("verified = %q, pending = %q, task = %s", f.ref(t, "verified_ref"), f.ref(t, "fix_pending_ref"), f.taskStatus(t))
	}
}

// 確かめ待ちの中身のままの読みで、比較が判定できなくなったら、両方を消す（検証し直しになるだけ）。
func TestSettleMovedContent_SamePendingContentNowUndeterminableClearsBoth(t *testing.T) {
	t.Parallel()
	f := newConflictFixed(t)
	f.compare.errs[[2]string{landContent, newContent}] = errors.New("temporary failure")
	f.read(t, intakeOut(integOptsWithLd, artsOf(newContent), nil))
	if f.ref(t, "verified_ref") != "" || f.ref(t, "fix_pending_ref") != "" || f.taskStatus(t) != "verifying" || len(f.client.resumes) != 0 {
		t.Fatalf("verified = %q, pending = %q, task = %s", f.ref(t, "verified_ref"), f.ref(t, "fix_pending_ref"), f.taskStatus(t))
	}
}

// 本番の Store の組み立て（作って開く道・既存を開く道）が、統合先宛ての確かめ結果の口を必ず持つ（nil のままにならない）。
func TestProductionStoreHasTheIntegrationFixWriter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	created, _, err := openAtForInit(dir)
	if err != nil {
		t.Fatal(err)
	}
	if created.fixWriters.Integration == nil {
		t.Error("openAtForInit: the integration writer is nil")
	}
	_ = created.Close()
	opened, err := OpenWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = opened.Close() }()
	if opened.fixWriters.Integration == nil {
		t.Error("OpenWorkspace: the integration writer is nil")
	}
}
