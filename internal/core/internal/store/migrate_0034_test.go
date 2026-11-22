package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// 区切り S4c-1 の 1 のスキーマ（版 34）。task_addition・task.addition_id・task.expand_after・
// challenge.budget_limit_micros・challenge.budget_limit_reason。

const migrationS4c1Version = 34

func TestS4c1Migration(t *testing.T) {
	t.Run("AC-219_upgrade_from_33_runs_one_migration_and_creates_task_addition", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "flywheel.db")
		old, err := Open(path, migrationsUpTo(t, migrationS4c1Version-1))
		if err != nil {
			t.Fatal(err)
		}
		if tableExists(t, old, "task_addition") {
			t.Fatal("task_addition exists before the migration")
		}
		insertChallenge(t, old, "a")
		if err := old.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO task (challenge_id, plan_version, key, title, body, done_criteria, repo, operation, status, created_at, updated_at)
				VALUES (1, 1, 'a', 't', 'b', 'd', 'o/r', 'pull_request', 'pending', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
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
		if got := userVersion(t, db); got != latestProdVersion(t) || got < migrationS4c1Version {
			t.Errorf("user_version = %d, want the latest %d (>= %d)", got, latestProdVersion(t), migrationS4c1Version)
		}
		if !tableExists(t, db, "task_addition") {
			t.Fatal("task_addition table not found after the migration")
		}
		info := tableColumnInfo(t, db, "task_addition")
		for _, c := range []string{"id", "challenge_id", "origin", "state", "total_after_micros", "created_at"} {
			if _, ok := info[c]; !ok {
				t.Errorf("task_addition.%s missing", c)
			}
		}
		for _, c := range []string{"origin_task_id", "spec", "limit_micros", "hold_id", "decided_at"} {
			if nn, ok := info[c]; !ok || nn {
				t.Errorf("task_addition.%s: present=%v notnull=%v; want present and nullable", c, ok, nn)
			}
		}
		// S4c-2 で足す列は、ここでは足さない。
		for _, c := range []string{"followup_id", "budget_impl_micros", "budget_review_micros"} {
			if _, ok := info[c]; ok {
				t.Errorf("task_addition.%s exists; it belongs to S4c-2", c)
			}
		}
		if cols := tableColumnInfo(t, db, "followup"); func() bool { _, ok := cols["promoted_task_id"]; return ok }() {
			t.Error("followup.promoted_task_id exists; it belongs to S4c-2")
		}

		// AC-220: 既存の課題の上限の列は NULL。AC-221: 既存のタスクの expand_after は 0（addition_id は NULL）。
		if err := db.Read(context.Background(), func(tx *sql.Tx) error {
			var limit sql.NullInt64
			var reason sql.NullString
			if err := tx.QueryRow(`SELECT budget_limit_micros, budget_limit_reason FROM challenge WHERE id = 1`).Scan(&limit, &reason); err != nil {
				return err
			}
			if limit.Valid || reason.Valid {
				t.Errorf("AC-220: budget_limit = %v / %v, want NULL / NULL", limit, reason)
			}
			var expandAfter int
			var additionID sql.NullInt64
			if err := tx.QueryRow(`SELECT expand_after, addition_id FROM task WHERE id = 1`).Scan(&expandAfter, &additionID); err != nil {
				return err
			}
			if expandAfter != 0 || additionID.Valid {
				t.Errorf("AC-221: expand_after = %d, addition_id = %v, want 0 / NULL", expandAfter, additionID)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("AC-222_a_second_expand_row_for_the_same_origin_task_is_refused", func(t *testing.T) {
		db := openFresh(t)
		insertChallenge(t, db, "a")
		exec := func(q string, args ...any) error {
			return db.Write(context.Background(), func(tx *sql.Tx) error { _, err := tx.Exec(q, args...); return err })
		}
		if err := exec(`INSERT INTO task (challenge_id, plan_version, key, title, body, done_criteria, repo, operation, status, created_at, updated_at)
			VALUES (1, 1, 'a', 't', 'b', 'd', 'o/r', 'pull_request', 'done', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		const ins = `INSERT INTO task_addition (challenge_id, origin, origin_task_id, state, created_at) VALUES (1, 'expand', 1, ?, '2026-01-01T00:00:00Z')`
		if err := exec(ins, "pending"); err != nil {
			t.Fatalf("first expand row: %v", err)
		}
		err := exec(ins, "none")
		if err == nil || !IsUniqueViolation(err) {
			t.Errorf("second expand row: err = %v, want a unique violation (whatever its state)", err)
		}
		// 出どころのタスクが無い行どうしは重ならない。
		for i := 0; i < 2; i++ {
			if err := exec(`INSERT INTO task_addition (challenge_id, origin, origin_task_id, state, created_at) VALUES (1, 'expand', NULL, 'pending', '2026-01-01T00:00:00Z')`); err != nil {
				t.Errorf("row without an origin task (%d): %v", i, err)
			}
		}
	})

	t.Run("closed_sets_are_enforced", func(t *testing.T) {
		db := openFresh(t)
		insertChallenge(t, db, "a")
		for name, q := range map[string]string{
			"origin": `INSERT INTO task_addition (challenge_id, origin, state, created_at) VALUES (1, 'followup', 'pending', 'x')`,
			"state":  `INSERT INTO task_addition (challenge_id, origin, state, created_at) VALUES (1, 'expand', 'proposed', 'x')`,
		} {
			if err := db.Write(context.Background(), func(tx *sql.Tx) error { _, err := tx.Exec(q); return err }); err == nil {
				t.Errorf("%s outside the closed set was accepted", name)
			}
		}
		for _, st := range []string{"pending", "awaiting_owner", "applied", "none", "declined", "superseded"} {
			if err := db.Write(context.Background(), func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO task_addition (challenge_id, origin, state, created_at) VALUES (1, 'expand', ?, 'x')`, st)
				return err
			}); err != nil {
				t.Errorf("state %q: %v", st, err)
			}
		}
	})
}
