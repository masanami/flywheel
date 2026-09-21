// Package coretest はテスト支援専用のヘルパーを持つ。internal/core 配下の
// パッケージ（Go の internal 規則で internal/core からしか import できない
// internal/core/internal/store を含む）だけが持つ知識に、internal/cli の
// テストからアクセスするために存在する。
//
// このパッケージは本番バイナリ（cmd/flywheel）からは import してはならない
// （internal/cli/depcheck_test.go が go list -deps で検査する）。*_test.go
// からだけ import すること。
package coretest

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/masanami/flywheel/internal/core/internal/store"
	_ "modernc.org/sqlite" // HoldRawWriteLock がテスト専用の生の接続を開くために使う
)

func dbPathFor(workspace string) string {
	return filepath.Join(workspace, ".flywheel", "flywheel.db")
}

func openExisting(t *testing.T, workspace string) *store.DB {
	t.Helper()
	migrations, err := store.Migrations()
	if err != nil {
		t.Fatalf("coretest: store.Migrations(): %v", err)
	}
	dbPath := dbPathFor(workspace)
	db, err := store.OpenExisting(dbPath, migrations)
	if err != nil {
		t.Fatalf("coretest: store.OpenExisting(%s): %v", dbPath, err)
	}
	return db
}

// SetStoreVersion は本番 API を経由せず、workspace 配下のストアへ直接
// PRAGMA user_version を書き込む。store_too_new のフィクスチャ（バイナリが
// 知らない将来の版）を作るためだけに使う。ワークスペースには既に
// core.Init 済みのストアがあること。
func SetStoreVersion(t *testing.T, workspace string, version int) {
	t.Helper()
	db := openExisting(t, workspace)
	defer func() { _ = db.Close() }()
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", version))
		return err
	}); err != nil {
		t.Fatalf("coretest: SetStoreVersion(%d): %v", version, err)
	}
}

// InsertChallenge はテスト専用のフィクスチャとして課題を 1 件挿入する
// （internal/core の課題 CRUD の公開 API がまだ無い時点のテストが使う）。
func InsertChallenge(t *testing.T, workspace, title string) {
	t.Helper()
	db := openExisting(t, workspace)
	defer func() { _ = db.Close() }()
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			"INSERT INTO challenge (title, status, reporter, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
			title, "unclassified", "tester", "2026-09-21T00:00:00.000Z", "2026-09-21T00:00:00.000Z",
		)
		return err
	}); err != nil {
		t.Fatalf("coretest: InsertChallenge(%q): %v", title, err)
	}
}

// CountChallenges は workspace 配下のストアにある課題の件数を返す。
func CountChallenges(t *testing.T, workspace string) int {
	t.Helper()
	db := openExisting(t, workspace)
	defer func() { _ = db.Close() }()
	var count int
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow("SELECT COUNT(*) FROM challenge").Scan(&count)
	}); err != nil {
		t.Fatalf("coretest: CountChallenges: %v", err)
	}
	return count
}

// HoldRawWriteLock は dbPath を生の database/sql 接続で開き（WAL・
// foreign_keys 等は設定しない。store.Open を経由しない）、BEGIN IMMEDIATE の
// トランザクションを stdin が閉じられるまで保持し続ける。
//
// internal/cli の busy テスト（別プロセスが書き込みロックを保持している間の
// store_busy の検証）が、modernc.org/sqlite を直接 import せずに「初めて
// 開かれる（journal_mode がまだ WAL 化されていない）ファイルへ、他プロセスが
// 先に BEGIN IMMEDIATE で書き込みロックを取っている」状況を再現するために使う。
// 標準出力へ "ready\n" を書いた時点でロックを取得済みであることを示す。
// 戻り値はプロセスの終了コードとして使うことを想定する。
func HoldRawWriteLock(dbPath string, stdin io.Reader, stdout, stderr io.Writer) int {
	db, err := sql.Open("sqlite", dbPath+"?_txlock=immediate")
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "coretest.HoldRawWriteLock: open:", err)
		return 1
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	tx, err := db.Begin()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "coretest.HoldRawWriteLock: begin immediate:", err)
		return 1
	}

	if _, err := fmt.Fprint(stdout, "ready\n"); err != nil {
		return 1
	}

	// 親プロセスが停止させる（stdin を閉じる）まで、書き込みロックを保持し続ける。
	_, _ = io.Copy(io.Discard, stdin)

	_ = tx.Rollback()
	return 0
}
