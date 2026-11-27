package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

// フローの移植 S2 のスキーマ（版 40）。task.planned_flow・task.flow_reason・contract_run_step.effort。

const migrationFlowChoiceVersion = 40

func TestFlowChoiceMigration(t *testing.T) {
	const ts = "'2026-01-01T00:00:00Z'"
	path := filepath.Join(t.TempDir(), "flywheel.db")
	old, err := Open(path, migrationsUpTo(t, migrationFlowChoiceVersion-1))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ table, col string }{{"task", "planned_flow"}, {"task", "flow_reason"}, {"contract_run_step", "effort"}} {
		if _, ok := tableColumnInfo(t, old, c.table)[c.col]; ok {
			t.Fatalf("%s.%s exists before the migration", c.table, c.col)
		}
	}
	insertChallenge(t, old, "a")
	if err := old.Write(context.Background(), func(tx *sql.Tx) error {
		for _, q := range []string{
			`INSERT INTO task (challenge_id, plan_version, key, title, body, done_criteria, repo, operation, status, created_at, updated_at)
			 VALUES (1, 1, 'k', 't', 'b', 'd', 'o/r', 'impl', 'pending', ` + ts + `, ` + ts + `)`,
			`INSERT INTO cycle (trigger, started_at, budget_usd, spent_usd) VALUES ('manual', ` + ts + `, 1, 0)`,
			`INSERT INTO run (cycle_id, kind, challenge_id, challenge_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket)
			 VALUES (1, 'delegation', 1, 1, 's1', 1, 'h', ` + ts + `, ` + ts + `, ` + ts + `, 'succeeded', 1, 'impl')`,
			`INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, cost_usd, max_budget_usd, started_at, observed_at)
			 VALUES (1, 'tool', 'ext-1', 1, 'running', 's', 1500000, 5000000, ` + ts + `, ` + ts + `)`,
			`INSERT INTO contract_run_step (contract_run_id, seq, step, kind, attempt, state, started_at) VALUES (1, 1, 's', 'agent', 1, 'finished', ` + ts + `)`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// 版 39 の列（新しい列を除く）を全部並べて比べる。
	var challengeColumns, contractRunColumns string
	if err := old.Read(context.Background(), func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT group_concat(name, ', ') FROM (SELECT name FROM pragma_table_info('contract_run') ORDER BY cid)`).Scan(&contractRunColumns); err != nil {
			return err
		}
		return tx.QueryRow(`SELECT group_concat(name, ', ') FROM (SELECT name FROM pragma_table_info('challenge') ORDER BY cid)`).Scan(&challengeColumns)
	}); err != nil {
		t.Fatal(err)
	}
	dump := func(db *DB) string {
		var out string
		if err := db.Read(context.Background(), func(tx *sql.Tx) error {
			for _, q := range []string{
				`SELECT id, challenge_id, plan_version, key, title, body, done_criteria, non_goals, repo, operation, status, source_key, created_at, updated_at FROM task`,
				`SELECT ` + challengeColumns + ` FROM challenge`, `SELECT * FROM cycle`, `SELECT * FROM run`, `SELECT ` + contractRunColumns + ` FROM contract_run`,
				`SELECT id, contract_run_id, seq, step, kind, attempt, state, outcome, started_at, ended_at, cost_usd, cost_unknown, model, models_used FROM contract_run_step`,
			} {
				rows, err := tx.Query(q)
				if err != nil {
					return err
				}
				cols, _ := rows.Columns()
				for rows.Next() {
					vals := make([]any, len(cols))
					ptrs := make([]any, len(cols))
					for i := range vals {
						ptrs[i] = &vals[i]
					}
					if err := rows.Scan(ptrs...); err != nil {
						_ = rows.Close()
						return err
					}
					out += fmt.Sprintf("%v\n", vals)
				}
				if err := rows.Close(); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := dump(old)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path, prodMigrations(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	t.Run("AC-123_fresh_store_is_version_40_or_later", func(t *testing.T) {
		fresh, err := Open(filepath.Join(t.TempDir(), "fresh.db"), prodMigrations(t))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fresh.Close() }()
		if got := userVersion(t, fresh); got != latestProdVersion(t) || got < migrationFlowChoiceVersion {
			t.Errorf("fresh user_version = %d, want the latest %d (>= %d)", got, latestProdVersion(t), migrationFlowChoiceVersion)
		}
	})
	t.Run("AC-124_upgrade_from_39_sets_version_40", func(t *testing.T) {
		if got := userVersion(t, db); got != latestProdVersion(t) || got < migrationFlowChoiceVersion {
			t.Errorf("user_version = %d, want the latest %d (>= %d)", got, latestProdVersion(t), migrationFlowChoiceVersion)
		}
	})
	t.Run("AC-125_existing_rows_are_unchanged", func(t *testing.T) {
		if got := dump(db); got != before {
			t.Errorf("rows = %q, want %q", got, before)
		}
	})
	t.Run("AC-126_to_128_columns_exist_and_are_nullable", func(t *testing.T) {
		for _, c := range []struct{ table, col string }{{"task", "planned_flow"}, {"task", "flow_reason"}, {"contract_run_step", "effort"}} {
			if nn, ok := tableColumnInfo(t, db, c.table)[c.col]; !ok || nn {
				t.Errorf("%s.%s: present=%v notnull=%v; want present and nullable", c.table, c.col, ok, nn)
			}
		}
	})
	t.Run("existing_values_are_null", func(t *testing.T) {
		var a, b, c sql.NullString
		if err := db.Read(context.Background(), func(tx *sql.Tx) error {
			if err := tx.QueryRow(`SELECT planned_flow, flow_reason FROM task WHERE id = 1`).Scan(&a, &b); err != nil {
				return err
			}
			return tx.QueryRow(`SELECT effort FROM contract_run_step WHERE id = 1`).Scan(&c)
		}); err != nil {
			t.Fatal(err)
		}
		if a.Valid || b.Valid || c.Valid {
			t.Errorf("planned_flow=%v flow_reason=%v effort=%v, want all NULL", a, b, c)
		}
	})
}
