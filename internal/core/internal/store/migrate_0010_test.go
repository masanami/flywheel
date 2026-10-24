package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// スキーマ版 9 のストアを開くと 0010 が当たり、既存の課題・不可逆操作の行の値は変わらず、
// 空の challenge_dependency ができて版は 10 になる。
func TestOpen_AppliesChallengeDependencyMigrationToAVersion9Store(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flywheel.db")

	var upTo9 []Migration
	for _, m := range prodMigrations(t) {
		if m.Version <= 9 {
			upTo9 = append(upTo9, m)
		}
	}
	old, err := Open(path, upTo9)
	if err != nil {
		t.Fatalf("Open(v9) error = %v", err)
	}
	if got := userVersion(t, old); got != 9 {
		t.Fatalf("user_version = %d, want 9", got)
	}
	insertChallenge(t, old, "t")
	if err := old.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO operation (challenge_id, kind, summary, ref, state, version, created_at)
			VALUES (1, 'release', 's', 'https://github.com/o/r/pull/1', 'approved', 2, '2026-10-24T00:00:00.000Z')`)
		return err
	}); err != nil {
		t.Fatalf("seed v9 rows: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	db, err := Open(path, prodMigrations(t))
	if err != nil {
		t.Fatalf("Open(latest) error = %v", err)
	}
	defer func() { _ = db.Close() }()
	if got := userVersion(t, db); got != 10 {
		t.Fatalf("user_version = %d, want 10", got)
	}
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		var title, status string
		var version int
		if err := tx.QueryRow(`SELECT title, status, version FROM challenge`).Scan(&title, &status, &version); err != nil {
			return err
		}
		if title != "t" || version != 1 {
			t.Errorf("challenge changed: title=%q status=%q version=%d", title, status, version)
		}
		var state string
		var opVersion int
		if err := tx.QueryRow(`SELECT state, version FROM operation`).Scan(&state, &opVersion); err != nil {
			return err
		}
		if state != "approved" || opVersion != 2 {
			t.Errorf("operation changed: state=%q version=%d", state, opVersion)
		}
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM challenge_dependency`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Errorf("challenge_dependency rows = %d, want 0", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("read after migration: %v", err)
	}
}

// 主キーは (challenge_id, depends_on_id) の組。同じ組は 2 度入らず、向きが逆の組は別。
func TestChallengeDependency_PrimaryKeyIsThePair(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "flywheel.db"), prodMigrations(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()
	insertChallenge(t, db, "a")
	insertChallenge(t, db, "b")
	ins := `INSERT INTO challenge_dependency (challenge_id, depends_on_id, created_at) VALUES (?, ?, '2026-10-24T00:00:00.000Z')`
	err = db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(ins, 1, 2); err != nil {
			t.Errorf("insert (1,2): %v", err)
		}
		if _, err := tx.Exec(ins, 2, 1); err != nil {
			t.Errorf("insert (2,1): %v", err)
		}
		if _, err := tx.Exec(ins, 1, 2); err == nil {
			t.Error("duplicate (1,2) inserted, want a primary key violation")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
