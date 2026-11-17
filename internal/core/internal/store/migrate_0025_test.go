package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// openV24WithConflictRows は challenge_conflict に行があるスキーマ版 24 のストアを作って閉じ、そのパスを返す。
func openV24WithConflictRows(t *testing.T) string {
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
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	return path
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
		path := openV24WithConflictRows(t)
		db, err := Open(path, prodMigrations(t))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		if got := userVersion(t, db); got != 25 {
			t.Errorf("user_version = %d, want 25", got)
		}
	})
	t.Run("AC-32_version_24_store_with_conflict_rows_loses_the_table", func(t *testing.T) {
		path := openV24WithConflictRows(t)
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
		path := openV24WithConflictRows(t)
		db, err := Open(path, prodMigrations(t))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		if n := countRows(t, db, "challenge"); n != 3 {
			t.Errorf("challenge rows = %d, want 3 (same as before opening)", n)
		}
	})
}
