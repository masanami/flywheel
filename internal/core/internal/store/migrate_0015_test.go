package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// スキーマ版 14 のストアを開くと 0015 が当たり、既存の不可逆操作・承認の行は新しい列が NULL のまま、
// 他の列の値は変わらない。戻した記録の表は空で、版は 15 になる。
func TestOpen_AppliesReleaseRecoveryMigrationToAVersion14Store(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flywheel.db")
	var upTo14 []Migration
	for _, m := range prodMigrations(t) {
		if m.Version <= 14 {
			upTo14 = append(upTo14, m)
		}
	}
	old, err := Open(path, upTo14)
	if err != nil {
		t.Fatalf("Open(v14) error = %v", err)
	}
	if got := userVersion(t, old); got != 14 {
		t.Fatalf("user_version = %d, want 14", got)
	}
	insertChallenge(t, old, "a")
	const at = "'2026-11-01T00:00:00.000Z'"
	if err := old.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO operation (challenge_id, kind, summary, ref, state, version, created_at) VALUES (1, 'release', 's', 'https://github.com/o/r/pull/1', 'approved', 2, ` + at + `)`); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO approval (challenge_id, operation_id, kind, decision, target_version, actor, channel, verification, decided_at, head_sha)
			VALUES (1, 1, 'release', 'approved', 1, 'owner', 'cli', 'tty_confirm', ` + at + `, 'abc')`)
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path, prodMigrations(t))
	if err != nil {
		t.Fatalf("Open(latest) error = %v", err)
	}
	defer func() { _ = db.Close() }()
	if got := userVersion(t, db); got != 22 {
		t.Fatalf("user_version = %d, want 22", got)
	}
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		var state string
		var reason, closedAt, supersedes sql.NullString
		if err := tx.QueryRow(`SELECT state, closed_reason, closed_at, supersedes_operation_id FROM operation WHERE id = 1`).Scan(&state, &reason, &closedAt, &supersedes); err != nil {
			return err
		}
		if state != "approved" || reason.Valid || closedAt.Valid || supersedes.Valid {
			t.Errorf("operation changed by migration: state=%q reason=%v closed_at=%v supersedes=%v", state, reason, closedAt, supersedes)
		}
		var head string
		var mergeIn, supAt, supReason, supBy sql.NullString
		if err := tx.QueryRow(`SELECT head_sha, merge_in_verified_head, superseded_at, superseded_reason, superseded_by_approval_id FROM approval WHERE id = 1`).Scan(&head, &mergeIn, &supAt, &supReason, &supBy); err != nil {
			return err
		}
		if head != "abc" || mergeIn.Valid || supAt.Valid || supReason.Valid || supBy.Valid {
			t.Errorf("approval changed by migration: head=%q merge_in=%v superseded=%v/%v/%v", head, mergeIn, supAt, supReason, supBy)
		}
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM challenge_reopen`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Errorf("challenge_reopen rows = %d, want 0", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("read after migration: %v", err)
	}
}

// 戻した記録は課題・古い不可逆操作・新しい不可逆操作・置き換えた承認の外部キーを守る。
func TestChallengeReopen_ForeignKeys(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "flywheel.db"), prodMigrations(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()
	insertChallenge(t, db, "a")
	const at = "'2026-11-01T00:00:00.000Z'"
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO operation (challenge_id, kind, summary, ref, state, version, created_at) VALUES (1, 'release', 's', 'r', 'closed', 3, ` + at + `)`); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO operation (challenge_id, kind, summary, ref, state, version, created_at, supersedes_operation_id) VALUES (1, 'release', 's', 'r', 'pending', 1, ` + at + `, 1)`); err != nil {
			t.Errorf("insert operation superseding OP-1: %v", err)
		}
		if _, err := tx.Exec(`INSERT INTO challenge_reopen (challenge_id, reason, operation_id, new_operation_id, superseded_approval_id, created_at) VALUES (1, 'not_mergeable', 1, 2, NULL, ` + at + `)`); err != nil {
			t.Errorf("insert challenge_reopen: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	err = db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO challenge_reopen (challenge_id, reason, operation_id, new_operation_id, superseded_approval_id, created_at) VALUES (99, 'not_mergeable', 1, 2, NULL, ` + at + `)`); err == nil {
			t.Error("challenge_reopen for a missing challenge inserted, want a foreign key violation")
		}
		if _, err := tx.Exec(`INSERT INTO challenge_reopen (challenge_id, reason, operation_id, new_operation_id, superseded_approval_id, created_at) VALUES (1, 'not_mergeable', 99, 2, NULL, ` + at + `)`); err == nil {
			t.Error("challenge_reopen for a missing operation inserted, want a foreign key violation")
		}
		if _, err := tx.Exec(`INSERT INTO challenge_reopen (challenge_id, reason, operation_id, new_operation_id, superseded_approval_id, created_at) VALUES (1, 'not_mergeable', 1, 2, 99, ` + at + `)`); err == nil {
			t.Error("challenge_reopen for a missing approval inserted, want a foreign key violation")
		}
		if _, err := tx.Exec(`INSERT INTO operation (challenge_id, kind, summary, ref, state, version, created_at, supersedes_operation_id) VALUES (1, 'release', 's', 'r', 'pending', 1, ` + at + `, 99)`); err == nil {
			t.Error("operation superseding a missing operation inserted, want a foreign key violation")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
