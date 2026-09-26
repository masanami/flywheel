package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func prodMigrations(t *testing.T) []Migration {
	t.Helper()
	migrations, err := Migrations()
	if err != nil {
		t.Fatalf("Migrations() error = %v", err)
	}
	return migrations
}

// v1OnlyMigrations は本番マイグレーション集合から版 1（M1 の初期スキーマ）だけを
// 取り出す。0002 を足した後の Migrations() は版 1・2 の両方を返すため、
// 「版 1 のストア」を再現するテスト（前方マイグレーションの適用・アップグレードの
// 検証）は本番の Migrations() をそのまま v1 のフィクスチャとして使えなくなった
// （0002_source_binding.sql を足すチケットでの更新）。
func v1OnlyMigrations(t *testing.T) []Migration {
	t.Helper()
	var v1 []Migration
	for _, m := range prodMigrations(t) {
		if m.Version == 1 {
			v1 = append(v1, m)
		}
	}
	if len(v1) != 1 {
		t.Fatalf("v1OnlyMigrations: found %d migrations with Version == 1, want 1", len(v1))
	}
	return v1
}

// insertChallenge / countChallenges / tableExists は、Conn() が廃止された後の
// テストが Write / Read を経由してストアへアクセスするための小さなヘルパー。

func insertChallenge(t *testing.T, db *DB, title string) {
	t.Helper()
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			"INSERT INTO challenge (title, status, reporter, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
			title, "unclassified", "alice", "2026-09-21T00:00:00.000Z", "2026-09-21T00:00:00.000Z",
		)
		return err
	}); err != nil {
		t.Fatalf("insert challenge: %v", err)
	}
}

func countChallenges(t *testing.T, db *DB) int {
	t.Helper()
	var count int
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow("SELECT COUNT(*) FROM challenge").Scan(&count)
	}); err != nil {
		t.Fatalf("count challenges: %v", err)
	}
	return count
}

func tableExists(t *testing.T, db *DB, table string) bool {
	t.Helper()
	var name string
	err := db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
	})
	switch {
	case err == nil:
		return true
	case errors.Is(err, sql.ErrNoRows):
		return false
	default:
		t.Fatalf("tableExists(%s): %v", table, err)
		return false
	}
}

func userVersion(t *testing.T, db *DB) int {
	t.Helper()
	var version int
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow("PRAGMA user_version").Scan(&version)
	}); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	return version
}

func TestOpen_CreatesFreshStoreAtLatestVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flywheel.db")

	db, err := Open(path, prodMigrations(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	if got, want := userVersion(t, db), latestVersion(prodMigrations(t)); got != want {
		t.Fatalf("user_version = %d, want %d", got, want)
	}

	// スキーマ版 1 のテーブルが揃っていることを確認する（§データモデルの 6 エンティティ）。
	for _, table := range []string{"challenge", "task_plan", "approval", "hold", "operation", "activity"} {
		if !tableExists(t, db, table) {
			t.Errorf("table %s not found", table)
		}
	}
}

func TestOpen_ReopeningIsIdempotentAndPreservesData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flywheel.db")

	db1, err := Open(path, prodMigrations(t))
	if err != nil {
		t.Fatalf("Open() 1回目 error = %v", err)
	}
	insertChallenge(t, db1, "t")
	if err := db1.Close(); err != nil {
		t.Fatalf("close 1回目: %v", err)
	}

	db2, err := Open(path, prodMigrations(t))
	if err != nil {
		t.Fatalf("Open() 2回目 error = %v", err)
	}
	defer func() { _ = db2.Close() }()

	if count := countChallenges(t, db2); count != 1 {
		t.Fatalf("challenge count = %d, want 1 (再オープンでデータが保持されること)", count)
	}
}

func TestOpen_SchemaTooNewIsRejectedWithoutChangingTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flywheel.db")

	// バイナリの知らない将来の版を持つストアを直接作る（本番 API を経由しない
	// テスト専用のフィクスチャ）。
	setUserVersion(t, path, 999999)

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture before Open: %v", err)
	}

	_, err = Open(path, prodMigrations(t))
	if err == nil {
		t.Fatal("Open() with a too-new schema version should fail, got nil error")
	}
	if !errors.Is(err, ErrTooNew) {
		t.Fatalf("Open() error = %v, want ErrTooNew", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture after Open: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("store_too_new must not change the store file, but its bytes changed")
	}
}

// TestOpen_FailedMigrationLeavesVersionAndContentUnchanged は、1 つの版の適用が
// 途中で失敗したとき、版も内容も適用前のまま残ることを検証する（AC）。
func TestOpen_FailedMigrationLeavesVersionAndContentUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flywheel.db")

	v1 := v1OnlyMigrations(t) // version 1 のみ

	// まず version 1 まで正常に作る。
	db, err := Open(path, v1)
	if err != nil {
		t.Fatalf("Open() 初回 error = %v", err)
	}
	insertChallenge(t, db, "t")
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// version 2 として、途中で失敗する不正な SQL を追加した集合で再オープンする。
	broken := append(append([]Migration(nil), v1...), Migration{
		Version: 2,
		SQL:     "ALTER TABLE challenge ADD COLUMN note TEXT; THIS IS NOT VALID SQL;",
	})

	_, err = Open(path, broken)
	if err == nil {
		t.Fatal("Open() with a broken migration should fail, got nil error")
	}

	// 版が 1 のまま、データも残っていることを検証する。
	recovered, err := Open(path, v1)
	if err != nil {
		t.Fatalf("Open() 検証用の再オープン error = %v", err)
	}
	defer func() { _ = recovered.Close() }()

	if version := userVersion(t, recovered); version != 1 {
		t.Fatalf("user_version = %d, want 1 (失敗した版は適用されない)", version)
	}
	if count := countChallenges(t, recovered); count != 1 {
		t.Fatalf("challenge count = %d, want 1 (失敗した版の適用前のデータが残ること)", count)
	}
	// 失敗した版が一部だけ適用された痕跡（note 列）が残っていないことも確認する
	// （「ALTER TABLE ... ADD COLUMN note」自体は実行された可能性があるが、
	// トランザクション全体がロールバックされていれば note 列は存在しない）。
	if err := recovered.Read(context.Background(), func(tx *sql.Tx) error {
		var name string
		return tx.QueryRow("SELECT name FROM pragma_table_info('challenge') WHERE name = 'note'").Scan(&name)
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("note column check: err = %v, want sql.ErrNoRows (note column must not exist after rollback)", err)
	}
}

// TestOpen_UpgradesOldVersionStoreWithInjectedMigration は AC-10 の検証:
// テスト専用の旧版ストア（version 1）に、実行器へ差し込んだ追加のマイグレーション
// （version 2, テスト専用）を適用すると、版が最新になり既存の課題が保持される。
// ここでの version 2 はテスト専用の架空の版（本番の 0002 は使わない）。
func TestOpen_UpgradesOldVersionStoreWithInjectedMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flywheel.db")

	v1 := v1OnlyMigrations(t)
	db, err := Open(path, v1)
	if err != nil {
		t.Fatalf("Open() 旧版の作成 error = %v", err)
	}
	insertChallenge(t, db, "legacy")
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	withExtra := append(append([]Migration(nil), v1...), Migration{
		Version: 2,
		SQL:     "ALTER TABLE challenge ADD COLUMN note TEXT NOT NULL DEFAULT '';",
	})

	upgraded, err := Open(path, withExtra)
	if err != nil {
		t.Fatalf("Open() アップグレード error = %v", err)
	}
	defer func() { _ = upgraded.Close() }()

	if version := userVersion(t, upgraded); version != 2 {
		t.Fatalf("user_version = %d, want 2", version)
	}

	var title, note string
	if err := upgraded.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow("SELECT title, note FROM challenge WHERE title = 'legacy'").Scan(&title, &note)
	}); err != nil {
		t.Fatalf("legacy row / new column not accessible after upgrade: %v", err)
	}
	if title != "legacy" {
		t.Fatalf("title = %q, want %q (既存の課題が保持されること)", title, "legacy")
	}
}

func TestOpen_SingleWriteConnectionPerProcess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flywheel.db")
	db, err := Open(path, prodMigrations(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()
	if maxConns := db.Stats().MaxOpenConnections; maxConns != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1 (プロセス内の書き込み接続は1本)", maxConns)
	}
}

// TestOpen_PragmasAreVerifiedByReadback は、Open が要求する PRAGMA
// （journal_mode=WAL・foreign_keys=ON・busy_timeout=5000ms）が実際に効いている
// ことを、Open が返した DB に対して読み戻して検証する（レビュー指摘: 書き込むだけで
// 読み戻して確認していなかった）。
func TestOpen_PragmasAreVerifiedByReadback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flywheel.db")
	db, err := Open(path, prodMigrations(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	var journalMode string
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow("PRAGMA journal_mode").Scan(&journalMode)
	}); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	var foreignKeys int
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys)
	}); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}

	var busyTimeout int
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout)
	}); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if busyTimeout != busyTimeoutMillis {
		t.Errorf("busy_timeout = %d, want %d", busyTimeout, busyTimeoutMillis)
	}
}

// TestOpen_WriteTransactionsUseBeginImmediate は、Open が返した DB の Write が
// 実際に BEGIN IMMEDIATE を使うことを検証する: 別の接続が書き込みロックを
// 保持している間は、Write の開始（BeginTx）自体が busy_timeout の上限で
// ErrBusy になる（読みトランザクションへ降格して後から昇格に失敗する、という
// 経路は取らない）。
func TestOpen_WriteTransactionsUseBeginImmediate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flywheel.db")

	setup, err := Open(path, prodMigrations(t))
	if err != nil {
		t.Fatalf("Open() setup error = %v", err)
	}
	if err := setup.Close(); err != nil {
		t.Fatalf("close setup: %v", err)
	}

	// busy_timeout を短縮した DB を、本番の DSN 組み立て（dsnFor）を通したまま作る。
	db, err := openWithOptions(path, prodMigrations(t), openOptions{
		mode:              modeExistingOnly,
		busyTimeoutMillis: 300,
	})
	if err != nil {
		t.Fatalf("openWithOptions() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	holder := startBusyHolderProcess(t, path)
	defer holder.stop(t)
	holder.waitUntilReady(t)

	start := time.Now()
	writeErr := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			"INSERT INTO challenge (title, status, reporter, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
			"blocked", "unclassified", "alice", "2026-09-21T00:00:00.000Z", "2026-09-21T00:00:00.000Z",
		)
		return err
	})
	elapsed := time.Since(start)

	if !errors.Is(writeErr, ErrBusy) {
		t.Fatalf("Write() error = %v, want ErrBusy while another process holds the write lock", writeErr)
	}
	if elapsed < 250*time.Millisecond {
		t.Fatalf("Write() returned too fast (%v); expected it to wait close to the busy_timeout", elapsed)
	}
}

// TestOpen_WorkspacePathContainingQuestionMarkAndSpaceOpensTheExactFile は
// レビュー指摘の回帰テスト: 旧 dsnFor は "path + \"?\" + query" を連結していたため、
// path 自身がリテラルの '?' を含むと最初の '?' で DSN が切り詰められ、
// 別ファイルを開いたうえ busy_timeout 等のクエリパラメータも失われていた
// （実機で再現済み）。file: URI 化した dsnFor がこれを正しく扱うことを確認する。
func TestOpen_WorkspacePathContainingQuestionMarkAndSpaceOpensTheExactFile(t *testing.T) {
	dir := t.TempDir()
	weird := filepath.Join(dir, "has space and ? question", "flywheel.db")
	if err := os.MkdirAll(filepath.Dir(weird), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	db, err := Open(weird, prodMigrations(t))
	if err != nil {
		t.Fatalf("Open(%q) error = %v", weird, err)
	}
	insertChallenge(t, db, "t")
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 実ファイルが意図したそのままの名前で作られていること（'?' の手前で
	// 切り詰められた別名のファイルが誤って作られていないこと）を確認する。
	if _, statErr := os.Stat(weird); statErr != nil {
		t.Fatalf("expected the store file to exist at the exact literal path %q: %v", weird, statErr)
	}
	truncated := filepath.Join(dir, "has space and ")
	if _, statErr := os.Stat(truncated); statErr == nil {
		t.Fatalf("a truncated file/dir was created at %q; dsnFor is splitting on the first '?' in the path", truncated)
	}

	reopened, err := Open(weird, prodMigrations(t))
	if err != nil {
		t.Fatalf("Open(%q) 再オープン error = %v", weird, err)
	}
	defer func() { _ = reopened.Close() }()
	if count := countChallenges(t, reopened); count != 1 {
		t.Fatalf("challenge count = %d, want 1 (同じファイルが再オープンされること)", count)
	}
}

// TestOpenExisting_DoesNotCreateAMissingFile は item 4 の完了条件:
// 「既存のみ開く」モードで存在しないパスを開くと、ファイルを作らずに失敗する
// （fail-closed。core.OpenWorkspace が使う）。
func TestOpenExisting_DoesNotCreateAMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing.db")

	_, err := OpenExisting(path, prodMigrations(t))
	if err == nil {
		t.Fatal("OpenExisting() on a missing file should fail, got nil error")
	}
	if errors.Is(err, ErrTooNew) {
		t.Fatalf("OpenExisting() on a missing file returned ErrTooNew, want a plain open failure: %v", err)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Fatal("OpenExisting() created the file despite modeExistingOnly (fail-closed 違反)")
	}
}

// TestOpenExisting_OpensAndPreservesDataWhenTheFileAlreadyExists は
// OpenExisting が通常のストアに対しては Open と同様に振る舞うこと（既存の
// データを保持したまま開けること）を確認する。
func TestOpenExisting_OpensAndPreservesDataWhenTheFileAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flywheel.db")

	created, err := Open(path, prodMigrations(t))
	if err != nil {
		t.Fatalf("Open() setup error = %v", err)
	}
	insertChallenge(t, created, "t")
	if err := created.Close(); err != nil {
		t.Fatalf("close setup: %v", err)
	}

	db, err := OpenExisting(path, prodMigrations(t))
	if err != nil {
		t.Fatalf("OpenExisting() error = %v", err)
	}
	defer func() { _ = db.Close() }()
	if count := countChallenges(t, db); count != 1 {
		t.Fatalf("challenge count = %d, want 1", count)
	}
}

// TestApplyMigrations_ValidatesTheFullSequenceBeforeApplyingAnyVersion は
// item 11 の完了条件: {1,2,4} のような欠番を含む集合を版 0 から適用しようとすると、
// バージョン 1・2 も含めて何も適用されずにエラーになる（版 1・2 を適用してから
// 4 の欠番でエラーにする、という部分適用は起きない）。
func TestApplyMigrations_ValidatesTheFullSequenceBeforeApplyingAnyVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flywheel.db")

	withGap := []Migration{
		{Version: 1, SQL: "CREATE TABLE a (id INTEGER PRIMARY KEY);"},
		{Version: 2, SQL: "CREATE TABLE b (id INTEGER PRIMARY KEY);"},
		{Version: 4, SQL: "CREATE TABLE d (id INTEGER PRIMARY KEY);"},
	}

	_, err := Open(path, withGap)
	if err == nil {
		t.Fatal("Open() with a migration sequence gap should fail, got nil error")
	}

	// 何も適用されていないこと（version 0 のまま、テーブルも作られていない）を、
	// 欠番の無い集合で改めて開いて確認する。
	recovered, openErr := openWithOptions(path, nil, openOptions{mode: modeExistingOnly, busyTimeoutMillis: busyTimeoutMillis})
	if openErr != nil {
		t.Fatalf("re-open to verify nothing was applied: %v", openErr)
	}
	defer func() { _ = recovered.Close() }()
	if version := userVersion(t, recovered); version != 0 {
		t.Fatalf("user_version = %d, want 0 (何も適用されていないこと)", version)
	}
	for _, table := range []string{"a", "b", "d"} {
		if tableExists(t, recovered, table) {
			t.Errorf("table %s exists, want it absent (欠番検査は適用前に行われるべき)", table)
		}
	}
}

// TestOpen_AppliesSourceBindingMigrationAndReachesVersion2 は 0002 の完了条件:
// 本番マイグレーション集合で開いた新規ストアは版 2 に達し、source_binding 表が
// 期待する列を持つ（親要件チケット #51 §クリティカル設計決定 1）。
func TestOpen_AppliesSourceBindingMigrationAndReachesVersion2(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flywheel.db")

	db, err := Open(path, prodMigrations(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	if got, want := userVersion(t, db), 2; got != want {
		t.Fatalf("user_version = %d, want %d", got, want)
	}
	if !tableExists(t, db, "source_binding") {
		t.Fatal("table source_binding not found")
	}

	wantColumns := []string{
		"challenge_id", "source_id", "external_key", "url", "fingerprint",
		"upstream_state", "policy_state", "created_at", "updated_at",
	}
	for _, col := range wantColumns {
		if !columnExists(t, db, "source_binding", col) {
			t.Errorf("source_binding.%s not found", col)
		}
	}
	if columnExists(t, db, "source_binding", "last_synced_at") {
		t.Error("source_binding.last_synced_at exists, want absent (QH1: last_synced_at は持たない)")
	}
}

// TestSourceBinding_ConstraintsAndIsUniqueViolation は 0002 の制約の検証:
// external_key の重複は UNIQUE 制約で拒否され IsUniqueViolation が true になる
// （AC-104 の下敷き。AC-104 自体は core のテストで検証する）。存在しない課題への
// 対応（外部キー違反）・同じ課題への 2 つ目の対応（主キー違反）も拒否され、
// どちらも IsUniqueViolation は false（拡張結果コードの完全一致で判別する）。
func TestSourceBinding_ConstraintsAndIsUniqueViolation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flywheel.db")

	db, err := Open(path, prodMigrations(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	insertChallenge(t, db, "first")
	insertChallenge(t, db, "second")

	// insertBinding は fn の中（コミット前）で INSERT が返した生のエラーを返す。
	insertBinding := func(challengeID int64, externalKey string) error {
		var execErr error
		_ = db.Write(context.Background(), func(tx *sql.Tx) error {
			_, execErr = tx.Exec(
				`INSERT INTO source_binding (challenge_id, source_id, external_key, url, fingerprint, upstream_state, policy_state, created_at, updated_at)
				 VALUES (?, 'src', ?, 'https://example.invalid/1', '2:abc', 'open', 'in_policy', ?, ?)`,
				challengeID, externalKey, "2026-09-21T00:00:00.000Z", "2026-09-21T00:00:00.000Z",
			)
			return execErr
		})
		return execErr
	}

	if err := insertBinding(1, "owner/repo#1"); err != nil {
		t.Fatalf("insertBinding(1) error = %v", err)
	}

	cases := []struct {
		name        string
		challengeID int64
		externalKey string
		wantUnique  bool
	}{
		{"external_key の重複（UNIQUE）", 2, "owner/repo#1", true},
		{"存在しない課題（外部キー）", 999, "owner/repo#2", false},
		{"同じ課題に 2 つ目（主キー）", 1, "owner/repo#3", false},
	}
	for _, tc := range cases {
		err := insertBinding(tc.challengeID, tc.externalKey)
		if err == nil {
			t.Errorf("%s: INSERT error = nil, want a constraint violation", tc.name)
			continue
		}
		if got := IsUniqueViolation(err); got != tc.wantUnique {
			t.Errorf("%s: IsUniqueViolation(%v) = %v, want %v", tc.name, err, got, tc.wantUnique)
		}
	}
	if got := countRows(t, db, "source_binding"); got != 1 {
		t.Fatalf("source_binding rows = %d, want 1（拒否された対応は残らない）", got)
	}
}

// --- テスト用フィクスチャのヘルパー ---

// countRows は table の行数を返す（table はテストが渡す固定の名前）。
func countRows(t *testing.T, db *DB, table string) int {
	t.Helper()
	var count int
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count)
	}); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

// columnExists は table の列 column が存在するかを返す。
func columnExists(t *testing.T, db *DB, table, column string) bool {
	t.Helper()
	var name string
	err := db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow("SELECT name FROM pragma_table_info(?) WHERE name = ?", table, column).Scan(&name)
	})
	switch {
	case err == nil:
		return true
	case errors.Is(err, sql.ErrNoRows):
		return false
	default:
		t.Fatalf("columnExists(%s, %s): %v", table, column, err)
		return false
	}
}

// setUserVersion は本番 API を経由せず、直接 PRAGMA user_version を書き込む。
// store_too_new のフィクスチャ（バイナリが知らない将来の版）を作るためだけに使う。
func setUserVersion(t *testing.T, path string, version int) {
	t.Helper()
	db, err := sql.Open("sqlite", dsnFor(path, openOptions{mode: modeCreateIfMissing, busyTimeoutMillis: busyTimeoutMillis}))
	if err != nil {
		t.Fatalf("setUserVersion: open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		t.Fatalf("setUserVersion: exec: %v", err)
	}
}
