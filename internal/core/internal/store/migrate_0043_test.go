package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// 課題の置き換える元の文書の場所と、タスクのリポジトリへ入れるタスクの印（docs/features/request-to-plan.md
// 「DB スキーマ」）のスキーマ（版 43）。

const migrationSpecDraftVersion = 43

func TestSpecBaseLandsSpecMigration(t *testing.T) {
	const ts = "'2026-01-01T00:00:00Z'"
	const insertTask = `INSERT INTO task (challenge_id, plan_version, key, title, body, done_criteria, repo, operation, status, created_at, updated_at)
		VALUES (1, 1, 'k', 't', 'b', 'd', 'o/r', 'impl', 'pending', ` + ts + `, ` + ts + `)`
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "flywheel.db")
	old, err := Open(path, migrationsUpTo(t, migrationSpecDraftVersion-1))
	if err != nil {
		t.Fatal(err)
	}
	insertChallenge(t, old, "a")
	if err := old.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(insertTask)
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

	if got := userVersion(t, db); got != latestProdVersion(t) || got < migrationSpecDraftVersion {
		t.Errorf("user_version = %d, want the latest %d (>= %d)", got, latestProdVersion(t), migrationSpecDraftVersion)
	}
	if nn, ok := tableColumnInfo(t, db, "challenge")["spec_base_path"]; !ok || nn {
		t.Errorf("challenge.spec_base_path: present=%v notnull=%v; want present and nullable", ok, nn)
	}
	if nn, ok := tableColumnInfo(t, db, "task")["lands_spec"]; !ok || !nn {
		t.Errorf("task.lands_spec: present=%v notnull=%v; want present and not null", ok, nn)
	}
	var base sql.NullString
	var lands int
	if err := db.Read(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT spec_base_path FROM challenge WHERE id = 1`).Scan(&base); err != nil {
			return err
		}
		return tx.QueryRow(`SELECT lands_spec FROM task WHERE id = 1`).Scan(&lands)
	}); err != nil {
		t.Fatal(err)
	}
	if base.Valid || lands != 0 {
		t.Errorf("existing rows: spec_base_path=%v lands_spec=%d, want NULL and 0", base, lands)
	}
	// 値は 0 か 1 だけ。
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task SET lands_spec = 2 WHERE id = 1`)
		return err
	}); err == nil {
		t.Error("lands_spec = 2 was accepted")
	}
}
