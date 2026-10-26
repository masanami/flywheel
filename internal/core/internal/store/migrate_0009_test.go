package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// スキーマ版 8 のストアを開くと 0009 が当たり、既存の operation・approval・run_artifact の
// 値は変わらず新しい列は NULL のまま、版は 9 になる。マイグレーションの前に承認された
// release の承認の行は head_sha が NULL のまま残る。
func TestOpen_AppliesReleaseExecutionMigrationToAVersion8Store(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flywheel.db")

	var upTo8 []Migration
	for _, m := range prodMigrations(t) {
		if m.Version <= 8 {
			upTo8 = append(upTo8, m)
		}
	}
	old, err := Open(path, upTo8)
	if err != nil {
		t.Fatalf("Open(v8) error = %v", err)
	}
	if got := userVersion(t, old); got != 8 {
		t.Fatalf("user_version = %d, want 8", got)
	}
	insertChallenge(t, old, "t")
	if err := old.Write(context.Background(), func(tx *sql.Tx) error {
		stmts := []string{
			`INSERT INTO operation (challenge_id, kind, summary, ref, state, version, created_at)
				VALUES (1, 'release', 's', 'https://github.com/o/r/pull/1', 'approved', 2, '2026-09-21T00:00:00.000Z')`,
			`INSERT INTO run (kind, challenge_id, challenge_version, session_id, pid, host, heartbeat_at, started_at, max_budget_usd, budget_bucket)
				VALUES ('delegate', 1, 1, 's', 1, 'h', '2026-09-21T00:00:00.000Z', '2026-09-21T00:00:00.000Z', 1, 'impl')`,
			`INSERT INTO run_artifact (run_id, kind, ref, state, base, verified_at)
				VALUES (1, 'pr', 'https://github.com/o/r/pull/1', 'open', 'main', '2026-09-21T00:00:00.000Z')`,
			`INSERT INTO approval (challenge_id, operation_id, kind, decision, target_version, actor, channel, verification, decided_at)
				VALUES (1, 1, 'release', 'approved', 1, 'alice', 'cli', 'tty_confirm', '2026-09-21T00:00:00.000Z')`,
		}
		for _, s := range stmts {
			if _, err := tx.Exec(s); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed v8 rows: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	db, err := Open(path, prodMigrations(t))
	if err != nil {
		t.Fatalf("Open(latest) error = %v", err)
	}
	defer func() { _ = db.Close() }()
	if got := userVersion(t, db); got != 12 {
		t.Fatalf("user_version = %d, want 12", got)
	}
	if err := db.Read(context.Background(), func(tx *sql.Tx) error {
		var state, ref string
		var version int
		var executedAt, mergeCommit sql.NullString
		if err := tx.QueryRow(`SELECT state, ref, version, executed_at, merge_commit FROM operation`).
			Scan(&state, &ref, &version, &executedAt, &mergeCommit); err != nil {
			return err
		}
		if state != "approved" || ref != "https://github.com/o/r/pull/1" || version != 2 {
			t.Errorf("operation changed: state=%q ref=%q version=%d", state, ref, version)
		}
		if executedAt.Valid || mergeCommit.Valid {
			t.Errorf("operation.executed_at/merge_commit not NULL: %v %v", executedAt, mergeCommit)
		}
		var decision string
		var head sql.NullString
		if err := tx.QueryRow(`SELECT decision, head_sha FROM approval`).Scan(&decision, &head); err != nil {
			return err
		}
		if decision != "approved" || head.Valid {
			t.Errorf("approval changed: decision=%q head_sha=%v (want NULL)", decision, head)
		}
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM operation_execution`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Errorf("operation_execution rows = %d, want 0", n)
		}
		var artRef, artState string
		var verifiedHead sql.NullString
		if err := tx.QueryRow(`SELECT ref, state, verified_head_sha FROM run_artifact`).Scan(&artRef, &artState, &verifiedHead); err != nil {
			return err
		}
		if artRef != "https://github.com/o/r/pull/1" || artState != "open" || verifiedHead.Valid {
			t.Errorf("run_artifact changed: artRef=%q state=%q verified_head_sha=%v (want NULL)", artRef, artState, verifiedHead)
		}
		return nil
	}); err != nil {
		t.Fatalf("read after migration: %v", err)
	}
}

// 終了していない試行（ended_at IS NULL）は操作ごとに 1 つに限る。閉じた試行は何件あってもよく、
// 別の操作の試行とは独立。operation.state には CHECK 制約が無い（executed を core が検査する）。
func TestOperationExecution_ActiveAttemptIsUniquePerOperation(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "flywheel.db"), prodMigrations(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()
	insertChallenge(t, db, "t")
	ctx := context.Background()
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		for _, state := range []string{"executed", "anything"} {
			if _, err := tx.Exec(`INSERT INTO operation (challenge_id, kind, summary, state, created_at)
				VALUES (1, 'release', 's', ?, '2026-10-22T00:00:00.000Z')`, state); err != nil {
				t.Errorf("insert operation state=%q: %v (no CHECK expected)", state, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed operations: %v", err)
	}
	insert := func(opID int, ended any) error {
		return db.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO operation_execution (operation_id, started_at, ended_at, pid, host)
				VALUES (?, '2026-10-22T00:00:00.000Z', ?, 1, 'h')`, opID, ended)
			return err
		})
	}
	if err := insert(1, nil); err != nil {
		t.Fatalf("first active attempt: %v", err)
	}
	if err := insert(1, nil); !IsUniqueViolation(err) {
		t.Errorf("second active attempt error = %v, want unique violation", err)
	}
	if err := insert(2, nil); err != nil {
		t.Errorf("active attempt of another operation: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := insert(1, "2026-10-22T00:00:01.000Z"); err != nil {
			t.Errorf("closed attempt #%d: %v", i, err)
		}
	}
}
