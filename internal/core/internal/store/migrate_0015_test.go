package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// スキーマ版 14 のストアを開くと 0015 が当たり、既存の課題の行は照合の取得の失敗の連続回数が 0・
// 調べたブランチが NULL で埋まり、他の列は変わらない。版は 15 になる。
func TestOpen_AppliesReconcileFetchFailuresMigrationToAVersion14Store(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flywheel.db")
	var upTo14 []Migration
	for _, m := range prodMigrations(t) {
		if m.Version <= 14 {
			upTo14 = append(upTo14, m)
		}
	}
	old, err := Open(path, upTo14)
	if err != nil {
		t.Fatalf("Open(v14) error = %v", err)
	}
	if got := userVersion(t, old); got != 14 {
		t.Fatalf("user_version = %d, want 14", got)
	}
	insertChallenge(t, old, "a")
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path, prodMigrations(t))
	if err != nil {
		t.Fatalf("Open(latest) error = %v", err)
	}
	defer func() { _ = db.Close() }()
	if got := userVersion(t, db); got != 15 {
		t.Fatalf("user_version = %d, want 15", got)
	}
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		var title string
		var n int
		var branch sql.NullString
		if err := tx.QueryRow(`SELECT title, reconcile_fetch_failures, reconcile_fetch_branch FROM challenge WHERE id = 1`).Scan(&title, &n, &branch); err != nil {
			return err
		}
		if title != "a" || n != 0 || branch.Valid {
			t.Errorf("challenge after migration: title=%q failures=%d branch=%v, want a/0/NULL", title, n, branch)
		}
		return nil
	}); err != nil {
		t.Fatalf("read after migration: %v", err)
	}
}
