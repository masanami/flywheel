package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// AC-254・AC-255: スキーマ版 7 のストアを開くと 0008 が当たり、既存の課題の段階の欄と
// 既存の approval の rejected_fields は NULL のまま、版は 8 になる。
func TestOpen_AppliesCardStagesMigrationToAVersion7Store(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flywheel.db")

	var upTo7 []Migration
	for _, m := range prodMigrations(t) {
		if m.Version <= 7 {
			upTo7 = append(upTo7, m)
		}
	}
	old, err := Open(path, upTo7)
	if err != nil {
		t.Fatalf("Open(v7) error = %v", err)
	}
	if got := userVersion(t, old); got != 7 {
		t.Fatalf("user_version = %d, want 7", got)
	}
	insertChallenge(t, old, "t")
	if err := old.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO approval (challenge_id, operation_id, kind, decision, target_version, actor, channel, verification, reason, decided_at)
			VALUES (1, NULL, 'plan', 'rejected', 1, 'alice', 'cli', 'tty_confirm', 'r', '2026-09-21T00:00:00.000Z')`)
		return err
	}); err != nil {
		t.Fatalf("seed v7 rows: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
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
		var headline, context, issues, expected, actual, nonGoals, figure sql.NullString
		var holdID sql.NullInt64
		if err := tx.QueryRow(`SELECT headline, context, issues, issues_hold_id, expected, actual, non_goals, figure FROM challenge`).
			Scan(&headline, &context, &issues, &holdID, &expected, &actual, &nonGoals, &figure); err != nil {
			return err
		}
		for name, v := range map[string]bool{
			"headline": headline.Valid, "context": context.Valid, "issues": issues.Valid, "issues_hold_id": holdID.Valid,
			"expected": expected.Valid, "actual": actual.Valid, "non_goals": nonGoals.Valid, "figure": figure.Valid,
		} {
			if v {
				t.Errorf("challenge.%s is not NULL after migration", name)
			}
		}
		var rejected sql.NullString
		if err := tx.QueryRow(`SELECT rejected_fields FROM approval`).Scan(&rejected); err != nil {
			return err
		}
		if rejected.Valid {
			t.Errorf("approval.rejected_fields = %v, want NULL", rejected)
		}
		return nil
	}); err != nil {
		t.Fatalf("read new columns: %v", err)
	}
}
