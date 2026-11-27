package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// フローのカスタマイズ S1 のスキーマ（版 46）。contract_run.definition_source・definition_sha256。
const migrationContractRunDefinitionVersion = 46

func TestMigrations_0046_ContractRunDefinition(t *testing.T) {
	const ts = "'2026-01-01T00:00:00Z'"
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "flywheel.db")
	old, err := Open(path, migrationsUpTo(t, migrationContractRunDefinitionVersion-1))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"definition_source", "definition_sha256"} {
		if _, ok := tableColumnInfo(t, old, "contract_run")[c]; ok {
			t.Fatalf("contract_run.%s exists before the migration", c)
		}
	}
	insertChallenge(t, old, "a")
	if err := old.Write(ctx, func(tx *sql.Tx) error {
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
	db, err := Open(path, migrationsUpTo(t, migrationContractRunDefinitionVersion))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	if got := userVersion(t, db); got != migrationContractRunDefinitionVersion {
		t.Errorf("user_version = %d, want %d", got, migrationContractRunDefinitionVersion)
	}
	cols := tableColumnInfo(t, db, "contract_run")
	for _, c := range []string{"definition_source", "definition_sha256"} {
		if nn, ok := cols[c]; !ok || nn {
			t.Errorf("contract_run.%s: present=%v notnull=%v; want present and nullable", c, ok, nn)
		}
	}
	if got := dumpContractRun(t, db); got != rowBefore {
		t.Errorf("contract_run row = %q, want %q", got, rowBefore)
	}
	var src, sum sql.NullString
	if err := db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT definition_source, definition_sha256 FROM contract_run WHERE id = 1`).Scan(&src, &sum)
	}); err != nil {
		t.Fatal(err)
	}
	if src.Valid || sum.Valid {
		t.Errorf("existing row: definition_source=%v definition_sha256=%v, want both NULL", src, sum)
	}
}
