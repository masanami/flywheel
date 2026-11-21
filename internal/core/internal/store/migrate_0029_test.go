package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// 区切り S3b のスキーマ（版 29）。終わっていない実行の一意の索引を課題とタスクの組ごとに作り直し、列を足す。

const migrationS3bVersion = 29

func insertRunRow(tx *sql.Tx, taskID any, operationID any) error {
	_, err := tx.Exec(`INSERT INTO run (kind, challenge_id, challenge_version, session_id, pid, host, heartbeat_at, started_at, max_budget_usd, budget_bucket, task_id, operation_id)
		VALUES ('task', 1, 1, 's', 1, 'h', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z', 1, 'impl', ?, ?)`, taskID, operationID)
	return err
}

func insertContractRunRow(tx *sql.Tx, ext string, taskID any) error {
	// 契約の実行の開始の run は完了済みにして、run の一意の索引に触れないようにする。
	if _, err := tx.Exec(`INSERT INTO run (kind, challenge_id, challenge_version, session_id, pid, host, heartbeat_at, started_at, max_budget_usd, budget_bucket, result)
		VALUES ('contract', 1, 1, 's', 1, 'h', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z', 1, 'impl', 'completed')`); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, cost_usd, max_budget_usd, started_at, observed_at, task_id)
		VALUES (1, 'tool', ?, (SELECT MAX(id) FROM run), 'running', 's', 0, 1, '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z', ?)`, ext, taskID)
	return err
}

func TestS3bMigration(t *testing.T) {
	write := func(db *DB, f func(tx *sql.Tx) error) error { return db.Write(context.Background(), f) }
	t.Run("fresh_store_is_at_the_latest_version_29_or_later", func(t *testing.T) {
		db := openFresh(t)
		if got := userVersion(t, db); got != latestProdVersion(t) || got < migrationS3bVersion {
			t.Errorf("user_version = %d, want the latest %d (>= %d)", got, latestProdVersion(t), migrationS3bVersion)
		}
	})
	t.Run("added_columns_have_the_decided_nullability_and_defaults", func(t *testing.T) {
		db := openFresh(t)
		for _, c := range []struct {
			table, col string
			notNull    bool
		}{
			{"task", "budget_impl_micros", false}, {"task", "budget_review_micros", false},
			{"challenge", "integration_ref", false}, {"contract_run", "verified_ref", false},
			{"approval", "task_id", false}, {"run", "reflect", true},
		} {
			if nn, ok := tableColumnInfo(t, db, c.table)[c.col]; !ok || nn != c.notNull {
				t.Errorf("%s.%s: present=%v notnull=%v; want notnull=%v", c.table, c.col, ok, nn, c.notNull)
			}
		}
		if got := columnDefault(t, db, "run", "reflect"); !got.Valid || got.String != "0" {
			t.Errorf("run.reflect default = %v, want 0", got)
		}
	})
	t.Run("active_run_is_unique_per_challenge_and_task", func(t *testing.T) {
		db := openFresh(t)
		insertChallenge(t, db, "a")
		for _, k := range []string{"a", "b"} {
			if err := insertTaskRow(db, 1, k, 1); err != nil {
				t.Fatal(err)
			}
		}
		ins := func(task any) error { return write(db, func(tx *sql.Tx) error { return insertRunRow(tx, task, nil) }) }
		if err := ins(1); err != nil {
			t.Fatal(err)
		}
		if err := ins(2); err != nil {
			t.Errorf("an unfinished run for a different task of the same challenge was rejected: %v", err)
		}
		if err := ins(1); err == nil {
			t.Error("a second unfinished run for the same challenge and task was accepted")
		}
		if err := ins(nil); err != nil {
			t.Errorf("an unfinished run without a task was rejected: %v", err)
		}
		if err := ins(nil); err == nil {
			t.Error("a second unfinished run without a task for the same challenge was accepted")
		}
	})
	t.Run("active_contract_run_is_unique_per_challenge_and_task", func(t *testing.T) {
		db := openFresh(t)
		insertChallenge(t, db, "a")
		for _, k := range []string{"a", "b"} {
			if err := insertTaskRow(db, 1, k, 1); err != nil {
				t.Fatal(err)
			}
		}
		ins := func(ext string, task any) error {
			return write(db, func(tx *sql.Tx) error { return insertContractRunRow(tx, ext, task) })
		}
		if err := ins("e1", 1); err != nil {
			t.Fatal(err)
		}
		if err := ins("e2", 2); err != nil {
			t.Errorf("an unfinished contract_run for a different task was rejected: %v", err)
		}
		if err := ins("e3", 1); err == nil {
			t.Error("a second unfinished contract_run for the same challenge and task was accepted")
		}
		if err := ins("e4", nil); err != nil {
			t.Errorf("an unfinished contract_run without a task was rejected: %v", err)
		}
		if err := ins("e5", nil); err == nil {
			t.Error("a second unfinished contract_run without a task for the same challenge was accepted")
		}
	})
	t.Run("upgrade_from_28_keeps_rows_and_sets_reflect_from_operation_id", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "flywheel.db")
		old, err := Open(path, migrationsUpTo(t, 28))
		if err != nil {
			t.Fatal(err)
		}
		insertChallenge(t, old, "a")
		if err := insertTaskRow(old, 1, "k", 1); err != nil {
			t.Fatal(err)
		}
		if err := write(old, func(tx *sql.Tx) error {
			if _, err := tx.Exec(`INSERT INTO operation (challenge_id, kind, summary, state, created_at) VALUES (1, 'merge', 's', 'pending', '2026-11-01T00:00:00.000Z')`); err != nil {
				return err
			}
			if err := insertRunRow(tx, nil, 1); err != nil { // 反映の操作を持つ行
				return err
			}
			if _, err := tx.Exec(`UPDATE run SET result = 'completed' WHERE id = 1`); err != nil {
				return err
			}
			if err := insertRunRow(tx, nil, nil); err != nil {
				return err
			}
			_, err := tx.Exec(`INSERT INTO approval (challenge_id, kind, decision, target_version, actor, channel, verification, decided_at)
				VALUES (1, 'plan', 'approved', 1, 'o', 'cli', 'tty_confirm', '2026-11-01T00:00:00.000Z')`)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := old.Close(); err != nil {
			t.Fatal(err)
		}
		db, err := Open(path, prodMigrations(t))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		if got := userVersion(t, db); got != latestProdVersion(t) || got < migrationS3bVersion {
			t.Errorf("user_version after upgrading from 28 = %d, want the latest %d (>= %d)", got, latestProdVersion(t), migrationS3bVersion)
		}
		if err := db.Read(context.Background(), func(tx *sql.Tx) error {
			var withOp, withoutOp, nulls, integ, appr int
			if err := tx.QueryRow(`SELECT reflect FROM run WHERE operation_id IS NOT NULL`).Scan(&withOp); err != nil {
				return err
			}
			if err := tx.QueryRow(`SELECT reflect FROM run WHERE operation_id IS NULL`).Scan(&withoutOp); err != nil {
				return err
			}
			if withOp != 1 || withoutOp != 0 {
				t.Errorf("run.reflect = %d (with operation), %d (without); want 1, 0", withOp, withoutOp)
			}
			if err := tx.QueryRow(`SELECT (budget_impl_micros IS NULL) + (budget_review_micros IS NULL) FROM task`).Scan(&nulls); err != nil {
				return err
			}
			if nulls != 2 {
				t.Errorf("task budgets of an old row: %d NULLs, want 2", nulls)
			}
			if err := tx.QueryRow(`SELECT (integration_ref IS NULL) FROM challenge`).Scan(&integ); err != nil {
				return err
			}
			if err := tx.QueryRow(`SELECT (task_id IS NULL) FROM approval`).Scan(&appr); err != nil {
				return err
			}
			if integ != 1 || appr != 1 {
				t.Errorf("challenge.integration_ref NULL=%d approval.task_id NULL=%d; want 1, 1", integ, appr)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}
