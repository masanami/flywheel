package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

// このファイルのテスト名の AC-<n> は、core の見直しの区切り S1（docs/features/core-redesign.md。番号は masanami/flywheel#732 の
// 受入基準の並び順）の受入基準の番号。同じファイルの別の仕様の番号とは無関係。

// challengeRowsSnapshot は challenge の行の主な列（id・title・status・reporter・created_at・updated_at）を id 順の文字列で返す。
func challengeRowsSnapshot(t *testing.T, db *DB) []string {
	t.Helper()
	var rows []string
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		r, err := tx.Query(`SELECT id, title, status, reporter, created_at, updated_at FROM challenge ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = r.Close() }()
		for r.Next() {
			var id int64
			var title, status, reporter, createdAt, updatedAt string
			if err := r.Scan(&id, &title, &status, &reporter, &createdAt, &updatedAt); err != nil {
				return err
			}
			rows = append(rows, fmt.Sprintf("%d|%s|%s|%s|%s|%s", id, title, status, reporter, createdAt, updatedAt))
		}
		return r.Err()
	}); err != nil {
		t.Fatalf("snapshot challenge: %v", err)
	}
	return rows
}

// openV24WithConflictRows は challenge_conflict に行があるスキーマ版 24 のストアを作って閉じ、そのパスを返す。
// 移行の前の challenge の行（challengeRowsSnapshot）も返す。
func openV24WithConflictRows(t *testing.T) (string, []string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "flywheel.db")
	old, err := Open(path, migrationsUpTo(t, 24))
	if err != nil {
		t.Fatalf("Open(v24): %v", err)
	}
	if got := userVersion(t, old); got != 24 {
		t.Fatalf("user_version = %d, want 24", got)
	}
	insertChallenge(t, old, "a")
	insertChallenge(t, old, "b")
	insertChallenge(t, old, "c")
	if err := old.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO challenge_conflict (challenge_id, conflicts_with_id, directed, created_at) VALUES (2, 1, 1, '2026-11-01T00:00:00.000Z')`); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO challenge_conflict (challenge_id, conflicts_with_id, directed, created_at) VALUES (1, 3, 0, '2026-11-01T00:00:00.000Z')`)
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if n := countRows(t, old, "challenge_conflict"); n != 2 {
		t.Fatalf("seeded conflict rows = %d, want 2", n)
	}
	before := challengeRowsSnapshot(t, old)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	return path, before
}

func TestChallengeConflictDropped(t *testing.T) {
	t.Run("AC-30_fresh_store_has_no_challenge_conflict_table", func(t *testing.T) {
		db, err := Open(filepath.Join(t.TempDir(), "flywheel.db"), prodMigrations(t))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		if tableExists(t, db, "challenge_conflict") {
			t.Error("challenge_conflict exists in a fresh store")
		}
		var idx int
		if err := db.Read(context.Background(), func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'challenge_conflict_with'`).Scan(&idx)
		}); err != nil || idx != 0 {
			t.Errorf("challenge_conflict_with index count = %d, err = %v; want 0", idx, err)
		}
	})
	t.Run("AC-31_version_24_store_becomes_version_25", func(t *testing.T) {
		path, _ := openV24WithConflictRows(t)
		db, err := Open(path, prodMigrations(t))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		if got := userVersion(t, db); got != latestProdVersion(t) {
			t.Errorf("user_version = %d, want %d", got, latestProdVersion(t))
		}
	})
	t.Run("AC-32_version_24_store_with_conflict_rows_loses_the_table", func(t *testing.T) {
		path, _ := openV24WithConflictRows(t)
		db, err := Open(path, prodMigrations(t))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		if tableExists(t, db, "challenge_conflict") {
			t.Error("challenge_conflict still exists after opening a version 24 store")
		}
	})
	t.Run("AC-33_challenge_rows_are_unchanged_by_the_migration", func(t *testing.T) {
		path, before := openV24WithConflictRows(t)
		db, err := Open(path, prodMigrations(t))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		if n := countRows(t, db, "challenge"); n != 3 {
			t.Errorf("challenge rows = %d, want 3 (same as before opening)", n)
		}
		if after := challengeRowsSnapshot(t, db); !reflect.DeepEqual(after, before) {
			t.Errorf("challenge rows changed by the migration:\n before = %q\n after  = %q", before, after)
		}
	})
}
