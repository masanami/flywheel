package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// 区切り S4c-3 の 1 のスキーマ（版 38）。hold.scope と、開いている保留の部分一意索引。

const migrationHoldScopeVersion = 38

func TestHoldScopeMigration(t *testing.T) {
	const ts = "'2026-01-01T00:00:00Z'"
	path := filepath.Join(t.TempDir(), "flywheel.db")
	old, err := Open(path, migrationsUpTo(t, migrationHoldScopeVersion-1))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tableColumnInfo(t, old, "hold")["scope"]; ok {
		t.Fatal("hold.scope exists before the migration")
	}
	insertChallenge(t, old, "a")
	if err := old.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at) VALUES (1, 'q', 'in_progress', ` + ts + `)`)
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

	t.Run("AC-132_upgrade_from_37_sets_the_latest_version", func(t *testing.T) {
		if got := userVersion(t, db); got != latestProdVersion(t) || got < migrationHoldScopeVersion {
			t.Errorf("user_version = %d, want the latest %d (>= %d)", got, latestProdVersion(t), migrationHoldScopeVersion)
		}
	})
	t.Run("AC-133_scope_column_exists_and_is_nullable", func(t *testing.T) {
		if nn, ok := tableColumnInfo(t, db, "hold")["scope"]; !ok || nn {
			t.Errorf("hold.scope: present=%v notnull=%v; want present and nullable", ok, nn)
		}
	})
	t.Run("AC-135_existing_hold_scope_stays_null", func(t *testing.T) {
		if err := db.Read(context.Background(), func(tx *sql.Tx) error {
			var scope sql.NullString
			if err := tx.QueryRow(`SELECT scope FROM hold WHERE id = 1`).Scan(&scope); err != nil {
				return err
			}
			if scope.Valid {
				t.Errorf("scope = %q, want NULL", scope.String)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})

	exec := func(q string) error {
		return db.Write(context.Background(), func(tx *sql.Tx) error { _, err := tx.Exec(q); return err })
	}
	taskIns := func(key string) string {
		return `INSERT INTO task (challenge_id, plan_version, key, title, body, done_criteria, repo, operation, status, created_at, updated_at)
		 VALUES (1, 1, '` + key + `', 't', 'b', 'd', 'o/r', 'pull_request', 'pending', ` + ts + `, ` + ts + `)`
	}
	if err := exec(taskIns("t1")); err != nil {
		t.Fatal(err)
	}
	if err := exec(taskIns("t2")); err != nil {
		t.Fatal(err)
	}
	// 課題 1 の開いている保留（id 1）を閉じて、以降の試験の邪魔にならないようにする。
	if err := exec(`UPDATE hold SET closed_at = ` + ts + ` WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	holdIns := func(scope string, taskID string, extra string) string {
		return `INSERT INTO hold (challenge_id, question, from_status, raised_at, scope, task_id` + extra + `) VALUES (1, 'q', 'in_progress', ` + ts + `, ` + scope + `, ` + taskID + `)`
	}

	t.Run("AC-134_scope_task_requires_task_id", func(t *testing.T) {
		err := exec(holdIns("'task'", "NULL", ""))
		if err == nil || !strings.Contains(err.Error(), "CHECK") {
			t.Errorf("err = %v, want a CHECK constraint failure", err)
		}
		if err := exec(holdIns("'bogus'", "NULL", "")); err == nil {
			t.Error("unknown scope was accepted")
		}
	})
	t.Run("AC-136_one_open_task_hold_per_task", func(t *testing.T) {
		if err := exec(holdIns("'task'", "1", "")); err != nil {
			t.Fatal(err)
		}
		err := exec(holdIns("'task'", "1", ""))
		if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
			t.Errorf("second open hold for the same task: err = %v, want a UNIQUE failure", err)
		}
		if err := exec(holdIns("'task'", "2", "")); err != nil {
			t.Errorf("a different task must be allowed: %v", err)
		}
		// 回答済みなら別の保留を開ける。
		if err := exec(`UPDATE hold SET answer = 'a', answered_at = ` + ts + ` WHERE challenge_id = 1 AND task_id = 1`); err != nil {
			t.Fatal(err)
		}
		if err := exec(holdIns("'task'", "1", "")); err != nil {
			t.Errorf("after answering, a new open hold must be allowed: %v", err)
		}
	})
	t.Run("AC-137_one_open_challenge_hold_per_challenge", func(t *testing.T) {
		if err := exec(holdIns("NULL", "NULL", "")); err != nil {
			t.Fatal(err)
		}
		err := exec(holdIns("NULL", "NULL", ""))
		if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
			t.Errorf("second open challenge hold: err = %v, want a UNIQUE failure", err)
		}
		// タスクに結び付くが課題を止める保留（scope NULL）も、課題を止める保留として 1 つに数える。
		if err := exec(holdIns("NULL", "1", "")); err == nil {
			t.Error("a challenge-blocking hold with task_id must also count as the challenge's one open hold")
		}
	})
}
