package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// 区切り S3a のスキーマ（版 28）。契約の実行と成果物に列を足し、過去の行は書き換えない。

const migrationS3aVersion = 28

func columnDefault(t *testing.T, db *DB, table, col string) (def sql.NullString) {
	t.Helper()
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT dflt_value FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&def)
	}); err != nil {
		t.Fatalf("default of %s.%s: %v", table, col, err)
	}
	return def
}

func TestS3aMigration(t *testing.T) {
	t.Run("fresh_store_is_at_the_latest_version_28_or_later", func(t *testing.T) {
		db := openFresh(t)
		if got := userVersion(t, db); got != latestProdVersion(t) || got < migrationS3aVersion {
			t.Errorf("user_version = %d, want the latest %d (>= %d)", got, latestProdVersion(t), migrationS3aVersion)
		}
	})
	t.Run("run_artifact_has_a_nullable_role", func(t *testing.T) {
		db := openFresh(t)
		if nn, ok := tableColumnInfo(t, db, "run_artifact")["role"]; !ok || nn {
			t.Errorf("run_artifact.role: present=%v notnull=%v; want present and nullable", ok, nn)
		}
	})
	t.Run("hold_has_a_nullable_reason", func(t *testing.T) {
		db := openFresh(t)
		if nn, ok := tableColumnInfo(t, db, "hold")["reason"]; !ok || nn {
			t.Errorf("hold.reason: present=%v notnull=%v; want present and nullable", ok, nn)
		}
	})
	t.Run("contract_run_has_the_eight_columns_with_the_decided_nullability", func(t *testing.T) {
		db := openFresh(t)
		cr := tableColumnInfo(t, db, "contract_run")
		for c, notNull := range map[string]bool{
			"kind": true, "operation_id": false, "requested_options": false, "content_ref": false,
			"facts": false, "facts_unmet_count": true, "land_ref": false, "land_delivered": true,
		} {
			if nn, ok := cr[c]; !ok || nn != notNull {
				t.Errorf("contract_run.%s: present=%v notnull=%v; want notnull=%v", c, ok, nn, notNull)
			}
		}
		for c, want := range map[string]string{"kind": "'task'", "facts_unmet_count": "0", "land_delivered": "0"} {
			if got := columnDefault(t, db, "contract_run", c); !got.Valid || got.String != want {
				t.Errorf("contract_run.%s default = %v, want %s", c, got, want)
			}
		}
	})
	t.Run("one_unfinished_contract_run_per_challenge_stays_unique", func(t *testing.T) {
		db := openFresh(t)
		insertChallenge(t, db, "a")
		ins := func() error {
			return db.Write(context.Background(), func(tx *sql.Tx) error {
				if _, err := tx.Exec(`INSERT INTO run (kind, challenge_id, challenge_version, session_id, pid, host, heartbeat_at, started_at, max_budget_usd, budget_bucket)
					VALUES ('contract', 1, 1, 's', 1, 'h', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z', 1, 'impl')`); err != nil {
					return err
				}
				_, err := tx.Exec(`INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, cost_usd, max_budget_usd, started_at, observed_at)
					VALUES (1, 'tool', 'ext-' || (SELECT COUNT(*) FROM contract_run), 1, 'running', 's', 0, 1, '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z')`)
				return err
			})
		}
		if err := ins(); err != nil {
			t.Fatal(err)
		}
		if err := ins(); err == nil {
			t.Error("a second unfinished contract_run for the same challenge was accepted")
		}
	})
	t.Run("upgrade_from_27_keeps_rows_and_applies_defaults", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "flywheel.db")
		old, err := Open(path, migrationsUpTo(t, 27))
		if err != nil {
			t.Fatal(err)
		}
		insertChallenge(t, old, "a")
		if err := old.Write(context.Background(), func(tx *sql.Tx) error {
			if _, err := tx.Exec(`INSERT INTO run (kind, challenge_id, challenge_version, session_id, pid, host, heartbeat_at, started_at, max_budget_usd, budget_bucket)
				VALUES ('contract', 1, 1, 's', 1, 'h', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z', 1, 'impl')`); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO run_artifact (run_id, kind, ref) VALUES (1, 'commit', 'abc')`); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at) VALUES (1, 'q', 'in_progress', '2026-11-01T00:00:00.000Z')`); err != nil {
				return err
			}
			_, err := tx.Exec(`INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, cost_usd, max_budget_usd, started_at, observed_at)
				VALUES (1, 'tool', 'ext-1', 1, 'running', '実行中', 1500000, 5000000, '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z')`)
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
		if got := userVersion(t, db); got != latestProdVersion(t) || got < migrationS3aVersion {
			t.Errorf("user_version after upgrading from 27 = %d, want the latest %d (>= %d)", got, latestProdVersion(t), migrationS3aVersion)
		}
		if err := db.Read(context.Background(), func(tx *sql.Tx) error {
			var kind, summary string
			var unmet, delivered, nulls int
			if err := tx.QueryRow(`SELECT kind, summary, facts_unmet_count, land_delivered,
				(operation_id IS NULL) + (requested_options IS NULL) + (content_ref IS NULL) + (facts IS NULL) + (land_ref IS NULL)
				FROM contract_run WHERE external_run_id = 'ext-1'`).Scan(&kind, &summary, &unmet, &delivered, &nulls); err != nil {
				return err
			}
			if kind != "task" || summary != "実行中" || unmet != 0 || delivered != 0 || nulls != 5 {
				t.Errorf("contract_run after upgrade: kind=%q summary=%q unmet=%d delivered=%d nulls=%d", kind, summary, unmet, delivered, nulls)
			}
			var roleNull bool
			if err := tx.QueryRow(`SELECT role IS NULL FROM run_artifact WHERE ref = 'abc'`).Scan(&roleNull); err != nil {
				return err
			}
			if !roleNull {
				t.Error("run_artifact.role of an old row is not NULL")
			}
			var reasonNull bool
			if err := tx.QueryRow(`SELECT reason IS NULL FROM hold WHERE question = 'q'`).Scan(&reasonNull); err != nil {
				return err
			}
			if !reasonNull {
				t.Error("hold.reason of an old row is not NULL")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}
