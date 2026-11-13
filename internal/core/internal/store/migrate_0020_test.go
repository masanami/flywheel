package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// スキーマ版 19 のストアを開くと 0020 が当たり、既存の契約の run の行の値は変わらず、wait_hold_id が NULL の列として増える。
// wait_hold_id は存在する保留だけを指せる（外部キー）。
func TestOpen_AppliesContractWaitHoldMigrationToAVersion19Store(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flywheel.db")
	var upTo19 []Migration
	for _, m := range prodMigrations(t) {
		if m.Version <= 19 {
			upTo19 = append(upTo19, m)
		}
	}
	old, err := Open(path, upTo19)
	if err != nil {
		t.Fatalf("Open(v19) error = %v", err)
	}
	if got := userVersion(t, old); got != 19 {
		t.Fatalf("user_version = %d, want 19", got)
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
	if got := userVersion(t, db); got != 21 {
		t.Fatalf("user_version = %d, want 21", got)
	}
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		var state string
		var cost int64
		var hold sql.NullInt64
		if err := tx.QueryRow(`SELECT state, cost_usd, wait_hold_id FROM contract_run WHERE external_run_id = 'ext-1'`).Scan(&state, &cost, &hold); err != nil {
			return err
		}
		if state != "running" || cost != 1500000 || hold.Valid {
			t.Errorf("row = state %q, cost %d, wait_hold_id %v; want running, 1500000, NULL", state, cost, hold)
		}
		return nil
	}); err != nil {
		t.Fatalf("read after migration: %v", err)
	}

	err = db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE contract_run SET wait_hold_id = 999 WHERE external_run_id = 'ext-1'`)
		return err
	})
	if err == nil {
		t.Error("wait_hold_id pointing at a missing hold must be rejected by the foreign key")
	}
}
