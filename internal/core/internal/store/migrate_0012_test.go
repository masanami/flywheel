package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// スキーマ版 11 のストアを開くと 0012 が当たり、既存の行の値は変わらず、
// 既存の課題の改修の鍵と既存の保留の closed_at は NULL で、版は 12 になる。
func TestOpen_AppliesImprovementMigrationToAVersion11Store(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flywheel.db")
	var upTo11 []Migration
	for _, m := range prodMigrations(t) {
		if m.Version <= 11 {
			upTo11 = append(upTo11, m)
		}
	}
	old, err := Open(path, upTo11)
	if err != nil {
		t.Fatalf("Open(v11) error = %v", err)
	}
	if got := userVersion(t, old); got != 11 {
		t.Fatalf("user_version = %d, want 11", got)
	}
	insertChallenge(t, old, "kept")
	if err := old.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at) VALUES (1, 'q', 'in_progress', '2026-10-26T00:00:00.000Z')`)
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
	if got := userVersion(t, db); got != 12 {
		t.Fatalf("user_version = %d, want 12", got)
	}
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		var title, origin string
		var layer, target sql.NullString
		if err := tx.QueryRow(`SELECT title, origin, improvement_layer, improvement_target FROM challenge WHERE id = 1`).Scan(&title, &origin, &layer, &target); err != nil {
			return err
		}
		if title != "kept" || origin != "manual" || layer.Valid || target.Valid {
			t.Errorf("challenge = %q %q %v %v", title, origin, layer, target)
		}
		var question string
		var closedAt sql.NullString
		if err := tx.QueryRow(`SELECT question, closed_at FROM hold WHERE id = 1`).Scan(&question, &closedAt); err != nil {
			return err
		}
		if question != "q" || closedAt.Valid {
			t.Errorf("hold = %q %v", question, closedAt)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
