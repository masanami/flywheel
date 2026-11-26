package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// 下書きづくりの記録の表（docs/features/request-to-plan-s2b.md「DB スキーマ: 下書きづくりの記録の表」）のスキーマ（版 44）。

const migrationSpecDraftRecordVersion = 44

func TestSpecDraftRecordMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "flywheel.db")
	old, err := Open(path, migrationsUpTo(t, migrationSpecDraftRecordVersion-1))
	if err != nil {
		t.Fatal(err)
	}
	insertChallenge(t, old, "a")
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path, prodMigrations(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	if got := userVersion(t, db); got != latestProdVersion(t) || got < migrationSpecDraftRecordVersion {
		t.Errorf("user_version = %d, want the latest %d (>= %d)", got, latestProdVersion(t), migrationSpecDraftRecordVersion)
	}
	cols := tableColumnInfo(t, db, "spec_draft")
	notNull := map[string]bool{"path": true, "base_path": false, "repo": true, "reason": true, "state": true, "attempts": true,
		"questions": false, "answers": false, "hold_id": false, "hold_reason": false, "created_at": true, "updated_at": true}
	for name, want := range notNull {
		if nn, ok := cols[name]; !ok || nn != want {
			t.Errorf("spec_draft.%s: present=%v notnull=%v, want notnull=%v", name, ok, nn, want)
		}
	}
	if len(cols) != len(notNull)+1 {
		t.Errorf("spec_draft columns = %v, want challenge_id and %v", cols, notNull)
	}
	insert := func(cid int) error {
		return db.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO spec_draft (challenge_id, path, repo, reason, state, created_at, updated_at)
				VALUES (?, 'p', 'r', 'why', 'pending', 't', 't')`, cid)
			return err
		})
	}
	if err := insert(1); err != nil {
		t.Fatal(err)
	}
	if err := insert(1); err == nil {
		t.Error("a second record of the same challenge was accepted")
	}
	if err := insert(99); err == nil {
		t.Error("a record of a missing challenge was accepted")
	}
	var attempts int
	if err := db.Read(ctx, func(tx *sql.Tx) error { return tx.QueryRow(`SELECT attempts FROM spec_draft`).Scan(&attempts) }); err != nil || attempts != 0 {
		t.Errorf("attempts = %d, %v, want the default 0", attempts, err)
	}
}
