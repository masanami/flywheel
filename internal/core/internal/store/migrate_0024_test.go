package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// スキーマ版 23 のストアを開くと 0024 が当たり、既存の契約の run の requested_resolvable_by は NULL のまま（項目なし＝answer として扱う）。
func TestOpen_AppliesContractResolvableByMigrationToAVersion23Store(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flywheel.db")
	var upTo23 []Migration
	for _, m := range prodMigrations(t) {
		if m.Version <= 23 {
			upTo23 = append(upTo23, m)
		}
	}
	old, err := Open(path, upTo23)
	if err != nil {
		t.Fatalf("Open(v23) error = %v", err)
	}
	insertChallenge(t, old, "t")
	if err := old.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO run (kind, challenge_id, challenge_version, session_id, pid, host, heartbeat_at, started_at, max_budget_usd, budget_bucket)
			VALUES ('contract', 1, 1, 's', 1, 'h', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z', 1, 'impl')`); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, cost_usd, max_budget_usd, started_at, observed_at)
			VALUES (1, 'tool', 'ext-1', 1, 'running', '実行中', 1500000, 5000000, '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z')`)
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
	if got := userVersion(t, db); got != 25 {
		t.Fatalf("user_version = %d, want 25", got)
	}
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		var rb sql.NullString
		if err := tx.QueryRow(`SELECT requested_resolvable_by FROM contract_run WHERE external_run_id = 'ext-1'`).Scan(&rb); err != nil {
			return err
		}
		if rb.Valid {
			t.Errorf("requested_resolvable_by = %q, want NULL", rb.String)
		}
		return nil
	}); err != nil {
		t.Fatalf("read after migration: %v", err)
	}
}
