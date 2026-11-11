package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// スキーマ版 15 のストアを開くと 0016 が当たり、既存の課題の行は照合の取得の失敗の連続回数が 0・
// 失敗した照合の run・調べたブランチが NULL で埋まり、他の列は変わらない。版は 16 になる。
func TestOpen_AppliesReconcileFetchFailuresMigrationToAVersion15Store(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flywheel.db")
	var upTo15 []Migration
	for _, m := range prodMigrations(t) {
		if m.Version <= 15 {
			upTo15 = append(upTo15, m)
		}
	}
	old, err := Open(path, upTo15)
	if err != nil {
		t.Fatalf("Open(v15) error = %v", err)
	}
	if got := userVersion(t, old); got != 15 {
		t.Fatalf("user_version = %d, want 15", got)
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
	if got := userVersion(t, db); got != 19 {
		t.Fatalf("user_version = %d, want 19", got)
	}
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		var title string
		var n int
		var branch sql.NullString
		var runID sql.NullInt64
		if err := tx.QueryRow(`SELECT title, reconcile_fetch_failures, reconcile_fetch_run_id, reconcile_fetch_branch FROM challenge WHERE id = 1`).Scan(&title, &n, &runID, &branch); err != nil {
			return err
		}
		if title != "a" || n != 0 || runID.Valid || branch.Valid {
			t.Errorf("challenge after migration: title=%q failures=%d run=%v branch=%v, want a/0/NULL/NULL", title, n, runID, branch)
		}
		return nil
	}); err != nil {
		t.Fatalf("read after migration: %v", err)
	}
}
