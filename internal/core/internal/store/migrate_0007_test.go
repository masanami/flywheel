package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// AC-253: スキーマ版 6 のストアを開くと 0007 が当たり、既存の approval・hold の
// 追加した列は NULL のまま、版は 7 になる。
func TestOpen_AppliesRelayEvidenceMigrationToAVersion6Store(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flywheel.db")

	var upTo6 []Migration
	for _, m := range prodMigrations(t) {
		if m.Version <= 6 {
			upTo6 = append(upTo6, m)
		}
	}
	old, err := Open(path, upTo6)
	if err != nil {
		t.Fatalf("Open(v6) error = %v", err)
	}
	if got := userVersion(t, old); got != 6 {
		t.Fatalf("user_version = %d, want 6", got)
	}
	insertChallenge(t, old, "t")
	if err := old.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO approval (challenge_id, operation_id, kind, decision, target_version, actor, channel, verification, reason, decided_at)
			VALUES (1, NULL, 'plan', 'approved', 1, 'alice', 'cli', 'tty_confirm', NULL, '2026-09-21T00:00:00.000Z')`); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at, answer, answered_at, answered_by)
			VALUES (1, 'q', 'unclassified', '2026-09-21T00:00:00.000Z', 'a', '2026-09-21T00:00:01.000Z', 'alice')`)
		return err
	}); err != nil {
		t.Fatalf("seed v6 rows: %v", err)
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
		var s, q sql.NullString
		if err := tx.QueryRow(`SELECT relay_session, relay_quote FROM approval`).Scan(&s, &q); err != nil {
			return err
		}
		if s.Valid || q.Valid {
			t.Errorf("approval relay_* = %v/%v, want NULL", s, q)
		}
		var ch, ver sql.NullString
		var hs, hq sql.NullString
		if err := tx.QueryRow(`SELECT answered_channel, answered_verification, relay_session, relay_quote FROM hold`).Scan(&ch, &ver, &hs, &hq); err != nil {
			return err
		}
		if ch.Valid || ver.Valid || hs.Valid || hq.Valid {
			t.Errorf("hold new columns = %v/%v/%v/%v, want NULL", ch, ver, hs, hq)
		}
		return nil
	}); err != nil {
		t.Fatalf("read new columns: %v", err)
	}
}
