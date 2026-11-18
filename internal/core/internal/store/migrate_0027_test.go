package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// 区切り S2b のスキーマ（版 27）。フォローアップと取り込み元の子の表・task.source_key の列を足し、過去の行は書き換えない。

const migrationFollowupVersion = 27

func TestFollowupMigration(t *testing.T) {
	t.Run("fresh_store_has_followup_source_child_and_source_key", func(t *testing.T) {
		db := openFresh(t)
		if got := userVersion(t, db); got < migrationFollowupVersion {
			t.Errorf("user_version = %d, want >= %d", got, migrationFollowupVersion)
		}
		fu := tableColumnInfo(t, db, "followup")
		for c, notNull := range map[string]bool{
			"challenge_id": false, "title": true, "detail": false, "source": true, "status": true,
			"note": false, "reason": false, "promoted_challenge_id": false, "created_at": true, "updated_at": true,
		} {
			if nn, ok := fu[c]; !ok || nn != notNull {
				t.Errorf("followup.%s: present=%v notnull=%v; want notnull=%v", c, ok, nn, notNull)
			}
		}
		if _, ok := fu["id"]; !ok || len(fu) != 11 {
			t.Errorf("followup columns = %v; want 11 including id", fu)
		}
		if _, ok := fu["task_id"]; ok {
			t.Error("followup must not have task_id")
		}
		sc := tableColumnInfo(t, db, "source_child")
		for c, notNull := range map[string]bool{
			"challenge_id": true, "source_id": true, "external_key": true, "parent_external_key": false,
			"url": false, "title": false, "body": false, "upstream_state": true, "created_at": true, "updated_at": true,
		} {
			if nn, ok := sc[c]; !ok || nn != notNull {
				t.Errorf("source_child.%s: present=%v notnull=%v; want notnull=%v", c, ok, nn, notNull)
			}
		}
		if nn, ok := tableColumnInfo(t, db, "task")["source_key"]; !ok || nn {
			t.Errorf("task.source_key: present=%v notnull=%v; want present and nullable", ok, nn)
		}
	})
	t.Run("external_key_of_source_child_is_unique", func(t *testing.T) {
		db := openFresh(t)
		insertChallenge(t, db, "a")
		ins := func() error {
			return db.Write(context.Background(), func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO source_child (challenge_id, source_id, external_key, upstream_state, created_at, updated_at)
					VALUES (1, 's', 'o/r#2', 'open', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z')`)
				return err
			})
		}
		if err := ins(); err != nil {
			t.Fatal(err)
		}
		if err := ins(); err == nil {
			t.Error("a second source_child with the same external_key was accepted")
		}
	})
	t.Run("upgrade_from_26_keeps_rows_and_leaves_source_key_NULL", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "flywheel.db")
		old, err := Open(path, migrationsUpTo(t, 26))
		if err != nil {
			t.Fatal(err)
		}
		insertChallenge(t, old, "a")
		if err := old.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO task (challenge_id, plan_version, key, title, body, done_criteria, repo, operation, status, created_at, updated_at)
				VALUES (1, 1, 'k', 't', 'b', 'd', 'o/r', 'pull_request', 'pending', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z')`)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		before := challengeRowsSnapshot(t, old)
		if err := old.Close(); err != nil {
			t.Fatal(err)
		}
		db, err := Open(path, prodMigrations(t))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		if got := countRows(t, db, "challenge"); got != 1 {
			t.Errorf("challenge rows = %d, want 1", got)
		}
		if got := countRows(t, db, "task"); got != 1 {
			t.Errorf("task rows = %d, want 1", got)
		}
		if after := challengeRowsSnapshot(t, db); len(after) != len(before) || after[0] != before[0] {
			t.Errorf("challenge row changed: %v -> %v", before, after)
		}
		var nulls int
		if err := db.Read(context.Background(), func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT COUNT(*) FROM task WHERE source_key IS NULL`).Scan(&nulls)
		}); err != nil || nulls != 1 {
			t.Errorf("tasks with NULL source_key = %d, err = %v; want 1", nulls, err)
		}
		if got := countRows(t, db, "followup") + countRows(t, db, "source_child"); got != 0 {
			t.Errorf("new tables hold %d rows after upgrade, want 0", got)
		}
	})
}
