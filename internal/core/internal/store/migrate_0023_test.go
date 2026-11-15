package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// スキーマ版 22 のストアを開くと 0023 が当たり、既存の契約の run の work_dir は NULL のまま（ワークスペースで実行する）。
func TestOpen_AppliesContractWorkDirMigrationToAVersion22Store(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flywheel.db")
	var upTo22 []Migration
	for _, m := range prodMigrations(t) {
		if m.Version <= 22 {
			upTo22 = append(upTo22, m)
		}
	}
	old, err := Open(path, upTo22)
	if err != nil {
		t.Fatalf("Open(v22) error = %v", err)
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
	if got := userVersion(t, db); got != 23 {
		t.Fatalf("user_version = %d, want 23", got)
	}
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		var wd sql.NullString
		if err := tx.QueryRow(`SELECT work_dir FROM contract_run WHERE external_run_id = 'ext-1'`).Scan(&wd); err != nil {
			return err
		}
		if wd.Valid {
			t.Errorf("work_dir = %q, want NULL", wd.String)
		}
		return nil
	}); err != nil {
		t.Fatalf("read after migration: %v", err)
	}
}
