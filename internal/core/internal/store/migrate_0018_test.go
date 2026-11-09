package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// スキーマ版 17 のストアを開くと 0018 が当たり、既存の課題・依存・親子の行の値は変わらず、
// 空の challenge_conflict ができて版は 18 になる。
func TestOpen_AppliesChallengeConflictMigrationToAVersion17Store(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flywheel.db")
	var upTo17 []Migration
	for _, m := range prodMigrations(t) {
		if m.Version <= 17 {
			upTo17 = append(upTo17, m)
		}
	}
	old, err := Open(path, upTo17)
	if err != nil {
		t.Fatalf("Open(v17) error = %v", err)
	}
	if got := userVersion(t, old); got != 17 {
		t.Fatalf("user_version = %d, want 17", got)
	}
	insertChallenge(t, old, "a")
	insertChallenge(t, old, "b")
	if err := old.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO challenge_dependency (challenge_id, depends_on_id, created_at) VALUES (2, 1, '2026-11-01T00:00:00.000Z')`); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO challenge_parent (challenge_id, parent_id, created_at) VALUES (2, 1, '2026-11-01T00:00:00.000Z')`)
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
	if got := userVersion(t, db); got != 18 {
		t.Fatalf("user_version = %d, want 18", got)
	}
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		var challenges, deps, parents, conflicts int
		if err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM challenge), (SELECT COUNT(*) FROM challenge_dependency), (SELECT COUNT(*) FROM challenge_parent), (SELECT COUNT(*) FROM challenge_conflict)`).Scan(&challenges, &deps, &parents, &conflicts); err != nil {
			return err
		}
		if challenges != 2 || deps != 1 || parents != 1 || conflicts != 0 {
			t.Errorf("rows = challenge %d, dependency %d, parent %d, conflict %d; want 2, 1, 1, 0", challenges, deps, parents, conflicts)
		}
		return nil
	}); err != nil {
		t.Fatalf("read after migration: %v", err)
	}
}

// 組は主キー（challenge_id, conflicts_with_id）。自己参照・存在しない課題への参照・
// 向きの無い組で challenge_id が相手より大きい行は入らない。
func TestChallengeConflict_ConstraintsAndForeignKeys(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "flywheel.db"), prodMigrations(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()
	insertChallenge(t, db, "a")
	insertChallenge(t, db, "b")
	insertChallenge(t, db, "c")
	ins := `INSERT INTO challenge_conflict (challenge_id, conflicts_with_id, directed, created_at) VALUES (?, ?, ?, '2026-11-01T00:00:00.000Z')`
	err = db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(ins, 2, 1, 1); err != nil {
			t.Errorf("insert directed (2,1): %v", err)
		}
		if _, err := tx.Exec(ins, 2, 1, 0); err == nil {
			t.Error("duplicate (2,1) inserted, want a primary key violation")
		}
		if _, err := tx.Exec(ins, 1, 3, 0); err != nil {
			t.Errorf("insert undirected (1,3): %v", err)
		}
		if _, err := tx.Exec(ins, 3, 2, 0); err == nil {
			t.Error("undirected (3,2) with the larger id first inserted, want a check violation")
		}
		if _, err := tx.Exec(ins, 1, 1, 1); err == nil {
			t.Error("self conflict (1,1) inserted, want a check violation")
		}
		if _, err := tx.Exec(ins, 1, 2, 2); err == nil {
			t.Error("directed = 2 inserted, want a check violation")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(ins, 3, 99, 1); err == nil {
			t.Error("conflicts_with 99 (missing challenge) inserted, want a foreign key violation")
		}
		if _, err := tx.Exec(ins, 99, 1, 1); err == nil {
			t.Error("challenge 99 (missing challenge) inserted, want a foreign key violation")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
