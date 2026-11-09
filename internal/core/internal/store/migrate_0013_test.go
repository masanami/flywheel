package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// スキーマ版 12 のストアを開くと 0013 が当たり、既存の課題・依存の行の値は変わらず、
// 空の challenge_parent ができて版は 13 になる。
func TestOpen_AppliesChallengeParentMigrationToAVersion12Store(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flywheel.db")
	var upTo12 []Migration
	for _, m := range prodMigrations(t) {
		if m.Version <= 12 {
			upTo12 = append(upTo12, m)
		}
	}
	old, err := Open(path, upTo12)
	if err != nil {
		t.Fatalf("Open(v12) error = %v", err)
	}
	if got := userVersion(t, old); got != 12 {
		t.Fatalf("user_version = %d, want 12", got)
	}
	insertChallenge(t, old, "a")
	insertChallenge(t, old, "b")
	if err := old.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO challenge_dependency (challenge_id, depends_on_id, created_at) VALUES (2, 1, '2026-11-01T00:00:00.000Z')`)
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path, prodMigrations(t))
	if err != nil {
		t.Fatalf("Open(latest) error = %v", err)
	}
	defer func() { _ = db.Close() }()
	if got := userVersion(t, db); got != 16 {
		t.Fatalf("user_version = %d, want 16", got)
	}
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		var challenges, deps, parents int
		if err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM challenge), (SELECT COUNT(*) FROM challenge_dependency), (SELECT COUNT(*) FROM challenge_parent)`).Scan(&challenges, &deps, &parents); err != nil {
			return err
		}
		if challenges != 2 || deps != 1 || parents != 0 {
			t.Errorf("rows = challenge %d, dependency %d, parent %d; want 2, 1, 0", challenges, deps, parents)
		}
		return nil
	}); err != nil {
		t.Fatalf("read after migration: %v", err)
	}
}

// 主キーは challenge_id（親は 1 つまで）。同じ課題の 2 つ目の行は入らず、
// 存在しない課題への参照も入らない。
func TestChallengeParent_OneParentPerChallengeAndForeignKeys(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "flywheel.db"), prodMigrations(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()
	insertChallenge(t, db, "a")
	insertChallenge(t, db, "b")
	insertChallenge(t, db, "c")
	ins := `INSERT INTO challenge_parent (challenge_id, parent_id, created_at) VALUES (?, ?, '2026-11-01T00:00:00.000Z')`
	err = db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(ins, 1, 2); err != nil {
			t.Errorf("insert (1,2): %v", err)
		}
		if _, err := tx.Exec(ins, 1, 3); err == nil {
			t.Error("second parent for challenge 1 inserted, want a primary key violation")
		}
		if _, err := tx.Exec(ins, 2, 1); err != nil {
			t.Errorf("insert (2,1): %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(ins, 3, 99); err == nil {
			t.Error("parent 99 (missing challenge) inserted, want a foreign key violation")
		}
		if _, err := tx.Exec(ins, 99, 1); err == nil {
			t.Error("child 99 (missing challenge) inserted, want a foreign key violation")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
