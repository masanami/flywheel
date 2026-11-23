package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// 区切り S4b-2 の 1 のスキーマ（版 35）。保留の止まり方と処理の状態・対処の記録の表・フォローアップのタスク・
// タスクの検証の連続の数え・課題の照合のやり直しの印・契約の実行の上流を読めなかった回りの数を足す。

const migrationSupervisorVersion = 35

func TestSupervisorMigration(t *testing.T) {
	t.Run("fresh_store_has_the_columns_and_the_table", func(t *testing.T) {
		db := openFresh(t)
		if got := userVersion(t, db); got != latestProdVersion(t) || got < migrationSupervisorVersion {
			t.Errorf("user_version = %d, want the latest %d (>= %d)", got, latestProdVersion(t), migrationSupervisorVersion)
		}
		hold := tableColumnInfo(t, db, "hold")
		for _, col := range []string{"stop_kind", "supervisor_state", "supervisor_findings", "supervisor_recommendation", "supervisor_action_id", "deferred_cycle_id"} {
			if nn, ok := hold[col]; !ok || nn {
				t.Errorf("hold.%s: present=%v notnull=%v; want present and nullable", col, ok, nn)
			}
		}
		if nn, ok := hold["deferred_count"]; !ok || !nn {
			t.Errorf("hold.deferred_count: present=%v notnull=%v; want present and NOT NULL", ok, nn)
		}
		for table, col := range map[string]string{"followup": "task_id", "challenge": "reconcile_supervisor_action_id"} {
			if nn, ok := tableColumnInfo(t, db, table)[col]; !ok || nn {
				t.Errorf("%s.%s: present=%v notnull=%v; want present and nullable", table, col, ok, nn)
			}
		}
		for table, col := range map[string]string{"task": "verify_not_met_streak", "contract_run": "upstream_unreadable_rounds"} {
			if nn, ok := tableColumnInfo(t, db, table)[col]; !ok || !nn {
				t.Errorf("%s.%s: present=%v notnull=%v; want present and NOT NULL", table, col, ok, nn)
			}
		}
		action := tableColumnInfo(t, db, "supervisor_action")
		for _, col := range []string{"id", "cycle_id", "challenge_id", "task_id", "hold_id", "run_id", "stop_kind", "hand", "findings", "note", "basis", "conditions", "result", "reject_reason", "created_at"} {
			if _, ok := action[col]; !ok {
				t.Errorf("supervisor_action.%s is missing", col)
			}
		}
		// questions は版 36（区切り S4b-3）で足す。
	})
	t.Run("upgrade_from_34_keeps_rows_with_NULL_state_NULL_task_and_streak_0", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "flywheel.db")
		old, err := Open(path, migrationsUpTo(t, migrationSupervisorVersion-1))
		if err != nil {
			t.Fatal(err)
		}
		insertChallenge(t, old, "a")
		if err := old.Write(context.Background(), func(tx *sql.Tx) error {
			if _, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at) VALUES (1, 'q', 'classified', '2026-01-01T00:00:00Z')`); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO followup (challenge_id, title, source, status, created_at, updated_at) VALUES (1, 'f', 'learn', 'open', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
				return err
			}
			_, err := tx.Exec(`INSERT INTO task (challenge_id, plan_version, title, body, done_criteria, repo, operation, status, created_at, updated_at)
				VALUES (1, 1, 't', 'b', 'd', 'o/r', 'pull_request', 'pending', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
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
		if err := db.Read(context.Background(), func(tx *sql.Tx) error {
			var kind, state sql.NullString
			var deferred int
			if err := tx.QueryRow(`SELECT stop_kind, supervisor_state, deferred_count FROM hold`).Scan(&kind, &state, &deferred); err != nil {
				return err
			}
			if kind.Valid || state.Valid || deferred != 0 {
				t.Errorf("old hold row: stop_kind=%v supervisor_state=%v deferred_count=%d; want NULL, NULL, 0", kind, state, deferred)
			}
			var taskID sql.NullInt64
			if err := tx.QueryRow(`SELECT task_id FROM followup`).Scan(&taskID); err != nil {
				return err
			}
			if taskID.Valid {
				t.Errorf("old followup row: task_id = %v, want NULL", taskID)
			}
			var streak int
			if err := tx.QueryRow(`SELECT verify_not_met_streak FROM task`).Scan(&streak); err != nil {
				return err
			}
			if streak != 0 {
				t.Errorf("old task row: verify_not_met_streak = %d, want 0", streak)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}
