package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// 課題の仕様の文書の場所と、タスクの受け持つ受入基準の識別（docs/features/request-to-plan.md「DB スキーマ」）のスキーマ（版 42）。

const migrationSpecPathVersion = 42

func TestSpecPathCoversMigration(t *testing.T) {
	const ts = "'2026-01-01T00:00:00Z'"
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "flywheel.db")
	old, err := Open(path, migrationsUpTo(t, migrationSpecPathVersion-1))
	if err != nil {
		t.Fatal(err)
	}
	insertChallenge(t, old, "a")
	if err := old.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO task (challenge_id, plan_version, key, title, body, done_criteria, repo, operation, status, created_at, updated_at)
			VALUES (1, 1, 'k', 't', 'b', 'd', 'o/r', 'impl', 'pending', ` + ts + `, ` + ts + `)`)
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

	if got := userVersion(t, db); got != latestProdVersion(t) || got < migrationSpecPathVersion {
		t.Errorf("user_version = %d, want the latest %d (>= %d)", got, latestProdVersion(t), migrationSpecPathVersion)
	}
	for _, c := range []struct{ table, col string }{{"challenge", "spec_path"}, {"task", "covers"}} {
		if nn, ok := tableColumnInfo(t, db, c.table)[c.col]; !ok || nn {
			t.Errorf("%s.%s: present=%v notnull=%v; want present and nullable", c.table, c.col, ok, nn)
		}
	}
	var specPath, covers sql.NullString
	if err := db.Read(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT spec_path FROM challenge WHERE id = 1`).Scan(&specPath); err != nil {
			return err
		}
		return tx.QueryRow(`SELECT covers FROM task WHERE id = 1`).Scan(&covers)
	}); err != nil {
		t.Fatal(err)
	}
	if specPath.Valid || covers.Valid {
		t.Errorf("existing rows: spec_path=%v covers=%v, want NULL", specPath, covers)
	}
}
