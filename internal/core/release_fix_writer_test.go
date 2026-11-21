package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// 反映先宛ての衝突の解消の確かめ（J5）の結果の書き込み（writeReflectFix。AC-47〜AC-49・AC-60〜AC-63・AC-70・AC-71・AC-77・AC-78・AC-101）を、
// 実ストア・本番の書き込みの口で固定する。

func newReflectWriterFixture(t *testing.T) *fixFixture {
	t.Helper()
	f := newFixReflectFixture(t)
	f.s.fixWriters = f.s.defaultFixWriters()
	return f
}

func (f *fixFixture) refsOf(t *testing.T) contractFixRefs {
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

func (f *fixFixture) challengeStatusOf(t *testing.T) string {
	t.Helper()
	var s string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(`SELECT status FROM challenge`).Scan(&s) }); err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *fixFixture) opStateOf(t *testing.T) string {
	t.Helper()
	var s string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(`SELECT state FROM operation`).Scan(&s) }); err != nil {
		t.Fatal(err)
	}
	return s
}

// AC-47〜AC-49・AC-101: 「満たした」→ fix_verified_ref は確かめ待ちだった中身になり、fix_pending_ref は NULL、課題は done のまま。
// 作業ログ conflict_fix_passed の entity は反映の操作。
func TestReflectFixWriter_MetRecordsVerifiedAndLogsOnTheOperation(t *testing.T) {
	t.Parallel()
	f := newReflectWriterFixture(t)
	f.setVerdict("met")
	f.verify(t)
	if got := f.refsOf(t); got.Verified != fixPending || got.Pending != "" || got.Rejected != "" {
		t.Fatalf("fix refs = %+v", got)
	}
	if f.challengeStatusOf(t) != "done" || f.opStateOf(t) != "approved" {
		t.Fatalf("challenge = %s operation = %s", f.challengeStatusOf(t), f.opStateOf(t))
	}
	if n := f.count(t, `SELECT COUNT(*) FROM activity WHERE action = 'conflict_fix_passed' AND entity = 'operation'`); n != 1 { // AC-101
		t.Fatalf("conflict_fix_passed on the operation = %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM activity WHERE action = 'conflict_fix_passed'`); n != 1 {
		t.Fatalf("conflict_fix_passed = %d", n)
	}
}

// AC-102〜AC-105（反映先宛て）: 作業ログ conflict_fix_passed の before.ref は通す前の中身（承認した中身）、after.ref は確かめ待ちだった中身、
// after.files は直したファイル名、after.run_id は確かめの run（"R-<n>"）。
func TestReflectFixWriter_MetLogsTheRefsFilesAndRunOnTheOperation(t *testing.T) {
	t.Parallel()
	f := newReflectWriterFixture(t)
	f.setVerdict("met")
	f.verify(t)
	var entity string
	var entityID int64
	var before, after string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT entity, entity_id, before, after FROM activity WHERE action = 'conflict_fix_passed'`).Scan(&entity, &entityID, &before, &after)
	}); err != nil {
		t.Fatal(err)
	}
	if entity != "operation" || entityID != f.count(t, `SELECT id FROM operation`) {
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
	if b.Ref != fixPassed || a.Ref != fixPending || len(a.Files) != 2 || a.Files[0] != "a.go" || a.Files[1] != "b.go" || a.RunID != lastJ5 {
		t.Fatalf("before = %s, after = %s (J5 run %s)", before, after, lastJ5)
	}
}

// AC-57: 通した中身（fix_verified_ref）が h1 のところへ、確かめ待ちの中身 h2 に「満たした」を書くと、fix_verified_ref は h2 に改まり、fix_pending_ref は空になる。
func TestReflectFixWriter_MetReplacesAnExistingVerifiedRef(t *testing.T) {
	t.Parallel()
	f := newReflectWriterFixture(t)
	f.sql(t, `UPDATE contract_run SET fix_verified_ref = 'h1'`)
	// 見分けの起点は通した中身 h1。h1 から確かめ待ちの中身へ、宛先の最新の取り込みで動き、衝突の解消がある。
	files := []UpstreamFile{{Filename: "a.go", Patch: "@@ -1 +1 @@\n-old\n+RESOLVED-LINE"}}
	f.cmp.set("h1", fixPending, withFiles(ahead(commit("m1", "main change", "m0"), commit(fixPending, "Merge branch 'main'", "h1", "m1")), files...))
	f.cmp.set("m1", fixPending, UpstreamComparison{Status: UpstreamCompareAhead, TotalCommits: 1, Commits: []UpstreamCommit{commit("h1", "fix", "p0")}, Files: files})
	f.setVerdict("met")
	f.verify(t)
	if got := f.refsOf(t); got != (contractFixRefs{Verified: fixPending}) {
		t.Fatalf("fix refs = %+v, want verified %s only", got, fixPending)
	}
}

// AC-60〜AC-63・AC-70: 「満たしていない」「判断できない」→ fix_rejected_ref は確かめ待ちだった中身になり、fix_pending_ref は NULL。
// 反映の承認・反映の操作・課題の状態は変えず、conflict_fix_passed は残さない。
func TestReflectFixWriter_NotMetAndUncertainRecordRejectedOnly(t *testing.T) {
	t.Parallel()
	for _, verdict := range []string{"not_met", "uncertain"} {
		verdict := verdict
		t.Run(verdict, func(t *testing.T) {
			t.Parallel()
			f := newReflectWriterFixture(t)
			f.setVerdict(verdict)
			f.verify(t)
			if got := f.refsOf(t); got.Rejected != fixPending || got.Pending != "" || got.Verified != "" {
				t.Fatalf("fix refs = %+v", got)
			}
			if f.challengeStatusOf(t) != "done" || f.opStateOf(t) != "approved" {
				t.Fatalf("challenge = %s operation = %s", f.challengeStatusOf(t), f.opStateOf(t))
			}
			if n := f.count(t, `SELECT COUNT(*) FROM activity WHERE action = 'conflict_fix_passed'`); n != 0 {
				t.Fatalf("conflict_fix_passed = %d", n)
			}
		})
	}
}

// AC-71: J5 の呼び出しが連続の失敗の上限まで失敗したら、fix_rejected_ref は確かめ待ちだった中身になる。
func TestReflectFixWriter_FailureLimitRecordsRejected(t *testing.T) {
	t.Parallel()
	f := newReflectWriterFixture(t)
	f.j5.j5.result = JudgmentLaunchOutput{Result: RunResultErrored, ErrorSummary: "boom"}
	for i := 0; i <= judgmentFailureLimit(f.j5.agent); i++ {
		f.verify(t)
	}
	if got := f.refsOf(t); got.Rejected != fixPending || got.Pending != "" {
		t.Fatalf("fix refs = %+v", got)
	}
}

// AC-77・AC-78: 結果を書く直前に、確かめ待ちの中身が確かめを始めたときと違う・課題が done でないなら、fix_verified_ref は書かれない
// （確かめ待ちの中身も書き換えない）。反映先宛ての確かめ待ちの中身は、反映の段が上流から読んだ中身。
func TestReflectFixWriter_DiscardedWhenThePendingMovedOrTheChallengeLeftDone(t *testing.T) {
	t.Parallel()
	cases := map[string]struct{ q, wantPending string }{
		"pending moved on": {`UPDATE contract_run SET fix_pending_ref = 'newer01'`, "newer01"},
		"not done":         {`UPDATE challenge SET status = 'verifying'`, fixPending},
	}
	for name, c := range cases {
		c := c
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newReflectWriterFixture(t)
			f.setVerdict("met")
			f.j5.j5.onInvoke = func(JudgmentLaunchInput) { f.sql(t, c.q) }
			f.verify(t)
			if got := f.refsOf(t); got.Verified != "" || got.Rejected != "" || got.Pending != c.wantPending {
				t.Fatalf("fix refs = %+v, want no result written and pending %q", got, c.wantPending)
			}
		})
	}
}

// 反映先宛ての書く直前の読み直しは、委譲先が言う中身の識別（content_ref）を条件にしない: content_ref が空でも、確かめ待ちの中身と違う値でも、
// 「満たした」は fix_verified_ref に書かれる（確かめ待ちの中身は、反映の段が上流から読んだ中身で、content_ref とは別に動く）。
func TestReflectFixWriter_DoesNotRequireTheContentRefToMatchThePending(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"content_ref empty":     `UPDATE contract_run SET content_ref = NULL`,
		"content_ref different": `UPDATE contract_run SET content_ref = 'other99'`,
	}
	for name, q := range cases {
		q := q
		for _, when := range []string{"before the check", "during the check"} {
			when := when
			t.Run(name+"/"+when, func(t *testing.T) {
				t.Parallel()
				f := newReflectWriterFixture(t)
				f.setVerdict("met")
				if when == "before the check" {
					f.sql(t, q)
				} else {
					f.j5.j5.onInvoke = func(JudgmentLaunchInput) { f.sql(t, q) }
				}
				f.verify(t)
				if got := f.refsOf(t); got.Verified != fixPending || got.Pending != "" || got.Rejected != "" {
					t.Fatalf("fix refs = %+v, want fix_verified_ref = %s", got, fixPending)
				}
			})
		}
	}
}

// 統合先を使わない課題のタスクの実行に対応する反映の操作は、反映の段と同じ決め方（その実行が取り込んだ成果物の参照と同じ参照の操作）で選ぶ。
// 課題に、別の成果物の、より新しい終わっていない反映の操作があっても、そちらを起点にしない。
func TestReflectFix_TheTaskRunTargetsTheOperationOfItsOwnArtifact(t *testing.T) {
	t.Parallel()
	f := newReflectWriterFixture(t)
	cid, _ := parseChallengeID(f.cid)
	f.sql(t, `INSERT INTO operation (challenge_id, kind, summary, ref, state, version, created_at) VALUES (?, 'release', 's', 'https://github.com/o/r/pull/99', 'approved', 1, '2026-09-25T00:00:00.000Z')`, cid)
	f.sql(t, `INSERT INTO approval (challenge_id, operation_id, kind, decision, target_version, actor, channel, verification, head_sha, decided_at)
		SELECT ?, MAX(id), 'release', 'approved', 1, 'alice', 'cli', 'tty_confirm', 'ffff999', '2026-09-25T00:00:00.000Z' FROM operation`, cid)
	want := f.count(t, `SELECT MIN(id) FROM operation`)
	got := f.fixTargets(t)
	if len(got) != 1 || got[0].OperationID == nil || *got[0].OperationID != want {
		t.Fatalf("fix targets = %+v, want the operation %d of the run's own artifact", got, want)
	}
	f.setVerdict("met")
	f.verify(t)
	if n := f.count(t, fmt.Sprintf(`SELECT COUNT(*) FROM activity WHERE action = 'conflict_fix_passed' AND entity = 'operation' AND entity_id = %d`, want)); n != 1 {
		t.Fatalf("conflict_fix_passed on the operation %d = %d", want, n)
	}
	// 実行が取り込んだ成果物と同じ参照の操作が無ければ、対象にしない（別の成果物の操作を起点にしない）。
	f2 := newReflectWriterFixture(t)
	f2.sql(t, `UPDATE operation SET ref = 'https://github.com/o/r/pull/99'`)
	if got := f2.fixTargets(t); len(got) != 0 {
		t.Fatalf("fix targets = %+v, want none", got)
	}
}

// 本番の Store の組み立て（作って開く道・既存を開く道）が、反映先宛ての確かめ結果の口を必ず持つ（nil のままにならない）。
func TestProductionStoreHasTheReflectFixWriter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	created, _, err := openAtForInit(dir)
	if err != nil {
		t.Fatal(err)
	}
	if created.fixWriters.Reflect == nil {
		t.Error("openAtForInit: the reflect writer is nil")
	}
	_ = created.Close()
	opened, err := OpenWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = opened.Close() }()
	if opened.fixWriters.Reflect == nil {
		t.Error("OpenWorkspace: the reflect writer is nil")
	}
}
