package store

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const observerHelperEnv = "FLYWHEEL_STORE_TEST_OBSERVER_WRITER"

// TestObserverWriterHelper は子プロセスとして再実行されたときだけ動く。
// 環境変数 observerHelperEnv の値（"challenge" か "heartbeat"）の書き込みを 1 回して終わる。
func TestObserverWriterHelper(t *testing.T) {
	op := os.Getenv(observerHelperEnv)
	if op == "" {
		t.Skip("helper process only")
	}
	migrations, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	db, err := OpenExisting(os.Getenv(busyHelperDBPath), migrations)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	err = db.Write(context.Background(), func(tx *sql.Tx) error {
		switch op {
		case "challenge":
			_, err := tx.Exec("INSERT INTO challenge (title, status, reporter, created_at, updated_at) VALUES ('t','unclassified','x','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')")
			return err
		case "heartbeat":
			_, err := tx.Exec("UPDATE run SET heartbeat_at = '2026-10-10T00:00:00.000Z'")
			return err
		}
		t.Fatalf("unknown op %q", op)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func runWriterProcess(t *testing.T, dbPath, op string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestObserverWriterHelper$")
	cmd.Env = append(os.Environ(), observerHelperEnv+"="+op, busyHelperDBPath+"="+dbPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("writer process %s: %v\n%s", op, err, out)
	}
}

func newObservedStore(t *testing.T) (string, *DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "flywheel.db")
	migrations, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	db, err := Open(path, migrations)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return path, db
}

// 前提の検証（M4P4）: 別プロセスの commit を、modernc.org/sqlite の専用の 1 本の接続の
// PRAGMA data_version が検出できる。課題の作成と、run の heartbeat だけの更新の両方。
func TestObserver_DetectsCommitsFromAnotherProcess(t *testing.T) {
	ctx := context.Background()
	path, db := newObservedStore(t)
	// heartbeat の更新対象になる run を 1 件用意する（外部キーを満たす課題つき）。
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO challenge (title, status, reporter, created_at, updated_at) VALUES ('c','unclassified','x','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')"); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO run (challenge_id, kind, judgment, challenge_version, session_id, pid, host, heartbeat_at, started_at, max_budget_usd, budget_bucket)
			VALUES (1, 'judgment', 'J1', 1, 's', 1, 'h', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', 1000000, 'judgment')`)
		return err
	}); err != nil {
		t.Fatalf("fixture insert failed: %v", err)
	}

	obs, err := OpenObserver(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = obs.Close() }()

	v0, err := obs.DataVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v := mustDataVersion(t, obs); v != v0 {
		t.Errorf("data_version changed without any commit: %d -> %d", v0, v)
	}

	runWriterProcess(t, path, "challenge")
	v1 := mustDataVersion(t, obs)
	if v1 == v0 {
		t.Fatalf("challenge commit by another process not detected: data_version stayed %d", v0)
	}
	if v := mustDataVersion(t, obs); v != v1 {
		t.Errorf("data_version changed without a commit: %d -> %d", v1, v)
	}

	runWriterProcess(t, path, "heartbeat")
	v2 := mustDataVersion(t, obs)
	if v2 == v1 {
		t.Fatalf("heartbeat-only commit by another process not detected: data_version stayed %d", v1)
	}
}

func TestObserver_OwnCommitsOnOtherConnectionAreDetected(t *testing.T) {
	ctx := context.Background()
	path, db := newObservedStore(t)
	obs, err := OpenObserver(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = obs.Close() }()
	v0 := mustDataVersion(t, obs)
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO challenge (title, status, reporter, created_at, updated_at) VALUES ('t','unclassified','x','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if v := mustDataVersion(t, obs); v == v0 {
		t.Errorf("commit on another connection not detected")
	}
}

func TestOpenObserver_MissingFileFailsWithoutCreating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "none.db")
	if _, err := OpenObserver(context.Background(), path); err == nil {
		t.Fatal("OpenObserver on a missing file succeeded")
	}
	if _, err := os.Stat(path); err == nil {
		t.Errorf("OpenObserver created %s", path)
	}
}

func mustDataVersion(t *testing.T, obs *Observer) int64 {
	t.Helper()
	v, err := obs.DataVersion(context.Background())
	if err != nil {
		t.Fatalf("DataVersion: %v", err)
	}
	return v
}
