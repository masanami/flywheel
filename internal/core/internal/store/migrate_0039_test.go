package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"
)

// フローの移植 S1 の 8 のスキーマ（版 39）。contract_run_step と contract_run.flow。

const migrationContractRunStepVersion = 39

func TestContractRunStepMigration(t *testing.T) {
	const ts = "'2026-01-01T00:00:00Z'"
	path := filepath.Join(t.TempDir(), "flywheel.db")
	old, err := Open(path, migrationsUpTo(t, migrationContractRunStepVersion-1))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tableColumnInfo(t, old, "contract_run")["flow"]; ok {
		t.Fatal("contract_run.flow exists before the migration")
	}
	insertChallenge(t, old, "a")
	if err := old.Write(context.Background(), func(tx *sql.Tx) error {
		for _, q := range []string{
			`INSERT INTO cycle (trigger, started_at, budget_usd, spent_usd) VALUES ('manual', ` + ts + `, 1, 0)`,
			`INSERT INTO run (cycle_id, kind, challenge_id, challenge_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket)
			 VALUES (1, 'delegation', 1, 1, 's1', 1, 'h', ` + ts + `, ` + ts + `, ` + ts + `, 'succeeded', 1, 'impl')`,
			`INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, cost_usd, max_budget_usd, started_at, observed_at)
			 VALUES (1, 'tool', 'ext-1', 1, 'running', 's', 1500000, 5000000, ` + ts + `, ` + ts + `)`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rowBefore := dumpContractRun(t, old)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path, prodMigrations(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	t.Run("AC-115_upgrade_from_38_sets_version_39", func(t *testing.T) {
		if got := userVersion(t, db); got != latestProdVersion(t) || got < migrationContractRunStepVersion {
			t.Errorf("user_version = %d, want the latest %d (>= %d)", got, latestProdVersion(t), migrationContractRunStepVersion)
		}
	})
	t.Run("AC-116_existing_rows_are_unchanged", func(t *testing.T) {
		if got := dumpContractRun(t, db); got != rowBefore {
			t.Errorf("contract_run row = %q, want %q", got, rowBefore)
		}
	})
	t.Run("AC-117_table_and_flow_column_exist", func(t *testing.T) {
		if nn, ok := tableColumnInfo(t, db, "contract_run")["flow"]; !ok || nn {
			t.Errorf("contract_run.flow: present=%v notnull=%v; want present and nullable", ok, nn)
		}
		cols := tableColumnInfo(t, db, "contract_run_step")
		for _, c := range []string{"contract_run_id", "seq", "step", "kind", "attempt", "state", "outcome", "started_at", "ended_at", "cost_usd", "cost_unknown", "model", "models_used"} {
			if _, ok := cols[c]; !ok {
				t.Errorf("contract_run_step.%s missing", c)
			}
		}
	})
	t.Run("existing_flow_is_null", func(t *testing.T) {
		var flow sql.NullString
		if err := db.Read(context.Background(), func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT flow FROM contract_run WHERE id = 1`).Scan(&flow)
		}); err != nil {
			t.Fatal(err)
		}
		if flow.Valid {
			t.Errorf("flow = %q, want NULL", flow.String)
		}
	})

	exec := func(q string) error {
		return db.Write(context.Background(), func(tx *sql.Tx) error { _, err := tx.Exec(q); return err })
	}
	ins := func(seq int, state string) string {
		return `INSERT INTO contract_run_step (contract_run_id, seq, step, kind, attempt, state, started_at) VALUES (1, ` +
			strconv.Itoa(seq) + `, 's', 'agent', 1, '` + state + `', ` + ts + `)`
	}
	t.Run("run_and_seq_pair_is_unique", func(t *testing.T) {
		if err := exec(ins(1, "running")); err != nil {
			t.Fatal(err)
		}
		if err := exec(ins(1, "finished")); err == nil {
			t.Error("duplicate (contract_run_id, seq) was accepted")
		}
		if err := exec(ins(2, "finished")); err != nil {
			t.Errorf("a different seq was rejected: %v", err)
		}
	})
	t.Run("state_outside_the_four_values_is_rejected", func(t *testing.T) {
		if err := exec(ins(3, "bogus")); err == nil {
			t.Error("state 'bogus' was accepted")
		}
	})
}

func dumpContractRun(t *testing.T, db *DB) string {
	t.Helper()
	var s string
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT id || '|' || challenge_id || '|' || connector_id || '|' || external_run_id || '|' || state || '|' || summary || '|' || cost_usd || '|' || started_at FROM contract_run`).Scan(&s)
	}); err != nil {
		t.Fatal(err)
	}
	return s
}
