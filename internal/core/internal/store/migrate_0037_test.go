package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// 区切り S4c-2 の 1 のスキーマ（版 37）。task_addition.followup_id・budget_impl_micros・budget_review_micros・followup.promoted_task_id。

const migrationFollowupTaskVersion = 37

func TestFollowupTaskMigration(t *testing.T) {
	t.Run("AC-87_upgrade_from_36_runs_one_migration_and_keeps_rows_referenced_by_task_and_hold", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "flywheel.db")
		old, err := Open(path, migrationsUpTo(t, migrationFollowupTaskVersion-1))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := tableColumnInfo(t, old, "followup")["promoted_task_id"]; ok {
			t.Fatal("followup.promoted_task_id exists before the migration")
		}
		insertChallenge(t, old, "a")
		if err := old.Write(context.Background(), func(tx *sql.Tx) error {
			for _, q := range []string{
				`INSERT INTO task (challenge_id, plan_version, key, title, body, done_criteria, repo, operation, status, created_at, updated_at)
				 VALUES (1, 1, 'a', 't', 'b', 'd', 'o/r', 'pull_request', 'pending', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
				`INSERT INTO task_addition (challenge_id, origin, origin_task_id, state, created_at) VALUES (1, 'expand', 1, 'applied', '2026-01-01T00:00:00Z')`,
				`UPDATE task SET addition_id = 1 WHERE id = 1`,
				`INSERT INTO hold (challenge_id, question, from_status, raised_at, task_addition_id) VALUES (1, 'q', 'in_progress', '2026-01-01T00:00:00Z', 1)`,
				`UPDATE task_addition SET hold_id = 1 WHERE id = 1`,
				// 採番を最大の id より進めておく（作り直しで採番の記録が戻ることを確かめるため）。
				`INSERT INTO task_addition (challenge_id, origin, state, created_at) VALUES (1, 'expand', 'pending', '2026-01-01T00:00:00Z')`,
				`DELETE FROM task_addition WHERE id = 2`,
				`INSERT INTO followup (challenge_id, title, source, status, created_at, updated_at) VALUES (1, 'f', 'learn', 'open', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			} {
				if _, err := tx.Exec(q); err != nil {
					return err
				}
			}
			return nil
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
		if got := userVersion(t, db); got != latestProdVersion(t) || got < migrationFollowupTaskVersion {
			t.Errorf("user_version = %d, want the latest %d (>= %d)", got, latestProdVersion(t), migrationFollowupTaskVersion)
		}
		for _, c := range []struct{ table, col string }{
			{"task_addition", "followup_id"}, {"task_addition", "budget_impl_micros"}, {"task_addition", "budget_review_micros"}, {"followup", "promoted_task_id"},
		} {
			if nn, ok := tableColumnInfo(t, db, c.table)[c.col]; !ok || nn {
				t.Errorf("%s.%s: present=%v notnull=%v; want present and nullable", c.table, c.col, ok, nn)
			}
		}
		if err := db.Read(context.Background(), func(tx *sql.Tx) error {
			var origin, state string
			var fid, bi, br sql.NullInt64
			if err := tx.QueryRow(`SELECT origin, state, followup_id, budget_impl_micros, budget_review_micros FROM task_addition WHERE id = 1`).Scan(&origin, &state, &fid, &bi, &br); err != nil {
				return err
			}
			if origin != "expand" || state != "applied" || fid.Valid || bi.Valid || br.Valid {
				t.Errorf("task_addition row changed: %s %s %v %v %v", origin, state, fid, bi, br)
			}
			var promoted sql.NullInt64
			if err := tx.QueryRow(`SELECT promoted_task_id FROM followup WHERE id = 1`).Scan(&promoted); err != nil {
				return err
			}
			if promoted.Valid {
				t.Errorf("AC-88: followup.promoted_task_id = %v, want NULL", promoted)
			}
			var n int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM task t JOIN task_addition a ON a.id = t.addition_id JOIN hold h ON h.task_addition_id = a.id`).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				t.Errorf("task→task_addition←hold links = %d, want 1", n)
			}
			var seq int64
			if err := tx.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name = 'task_addition'`).Scan(&seq); err != nil || seq != 2 {
				t.Errorf("task_addition sequence = %d (err %v), want 2 (kept across the table rebuild, above the max id 1)", seq, err)
			}
			rows, err := tx.Query(`SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'task_addition' AND name LIKE 'task_addition_%' ORDER BY name`)
			if err != nil {
				return err
			}
			var idx []string
			for rows.Next() {
				var n string
				if err := rows.Scan(&n); err != nil {
					_ = rows.Close()
					return err
				}
				idx = append(idx, n)
			}
			_ = rows.Close()
			if got, want := strings.Join(idx, ","), "task_addition_challenge,task_addition_expand_origin,task_addition_followup,task_addition_hold"; got != want {
				t.Errorf("task_addition indexes = %s, want %s", got, want)
			}
			var bad int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&bad); err != nil {
				return err
			}
			if bad != 0 {
				t.Errorf("foreign_key_check reports %d violations", bad)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO task_addition (challenge_id, origin, state, followup_id, budget_impl_micros, budget_review_micros, created_at) VALUES (1, 'followup', 'pending', 1, 1000000, 500000, '2026-01-01T00:00:00Z')`)
			return err
		}); err != nil {
			t.Errorf("inserting an origin=followup row: %v", err)
		}
		if err := db.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO task_addition (challenge_id, origin, state, created_at) VALUES (1, 'bogus', 'pending', '2026-01-01T00:00:00Z')`)
			return err
		}); err == nil {
			t.Error("origin=bogus was accepted")
		}
	})
}
