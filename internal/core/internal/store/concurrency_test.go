package store

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

// counterHelperEnvVar が "1" のとき、このテストバイナリは TestMain から
// counterWorkerMain へ分岐し、同じストアの同じ行へ read-modify-write を
// 指定回数だけ繰り返す「別プロセス」として振る舞う（busy_test.go の
// busyHolderMain と同じ再実行の手法）。
const (
	counterHelperEnvVar     = "FLYWHEEL_STORE_TEST_COUNTER_WORKER"
	counterHelperDBPath     = "FLYWHEEL_STORE_TEST_COUNTER_DB_PATH"
	counterHelperIncrements = "FLYWHEEL_STORE_TEST_COUNTER_INCREMENTS"
	counterMigrationSQL     = "CREATE TABLE test_counter (id INTEGER PRIMARY KEY CHECK (id = 1), value INTEGER NOT NULL);\n" +
		"INSERT INTO test_counter (id, value) VALUES (1, 0);"
	counterMigrationVersion = 2 // テスト専用の架空の追加版（本番のスキーマ版は 1 のまま）。
)

// counterMigrations は本番マイグレーションに、テスト専用のカウンターテーブルを
// 足した集合を返す（本番スキーマ・migrations/*.sql は変えない）。
func counterMigrations() ([]Migration, error) {
	prod, err := Migrations()
	if err != nil {
		return nil, err
	}
	return append(append([]Migration(nil), prod...), Migration{
		Version: counterMigrationVersion,
		SQL:     counterMigrationSQL,
	}), nil
}

func counterWorkerMain() {
	path := os.Getenv(counterHelperDBPath)
	n, err := strconv.Atoi(os.Getenv(counterHelperIncrements))
	if err != nil {
		fmt.Fprintln(os.Stderr, "counterWorkerMain: bad increment count:", err)
		os.Exit(1)
	}

	migrations, err := counterMigrations()
	if err != nil {
		fmt.Fprintln(os.Stderr, "counterWorkerMain: migrations:", err)
		os.Exit(1)
	}

	db, err := OpenExisting(path, migrations)
	if err != nil {
		fmt.Fprintln(os.Stderr, "counterWorkerMain: open:", err)
		os.Exit(1)
	}
	defer func() { _ = db.Close() }()

	for i := 0; i < n; i++ {
		if err := db.Write(context.Background(), func(tx *sql.Tx) error {
			var v int
			if err := tx.QueryRow("SELECT value FROM test_counter WHERE id = 1").Scan(&v); err != nil {
				return err
			}
			_, err := tx.Exec("UPDATE test_counter SET value = ? WHERE id = 1", v+1)
			return err
		}); err != nil {
			// リトライで吸収しない: ErrBusy を含むあらゆる失敗をそのまま
			// プロセスの失敗として報告する。
			fmt.Fprintf(os.Stderr, "counterWorkerMain: write %d/%d: %v\n", i+1, n, err)
			os.Exit(1)
		}
	}
	os.Exit(0)
}

func runCounterWorkerProcess(t *testing.T, path string, increments int) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		counterHelperEnvVar+"=1",
		counterHelperDBPath+"="+path,
		counterHelperIncrements+"="+strconv.Itoa(increments),
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Errorf("counter worker process failed: %v (stderr=%s)", err, stderr.String())
	}
}

// TestOpen_ConcurrentWritesFromMultipleProcessesDoNotLoseUpdates は Issue #7 の
// 完了条件: 複数の別プロセスが同じストアの同じ行へ read-modify-write を並行に
// 行っても、変更が失われない（最終値 == 総加算回数）ことを検証する。
// クリティカル設計決定 2 の BEGIN IMMEDIATE による直列化がこれを保証する。
func TestOpen_ConcurrentWritesFromMultipleProcessesDoNotLoseUpdates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flywheel.db")

	migrations, err := counterMigrations()
	if err != nil {
		t.Fatalf("counterMigrations(): %v", err)
	}

	// ワーカーがマイグレーションの適用そのものを競合しないよう、先に 1 回だけ
	// 開いてテスト専用のカウンターテーブルを作っておく。
	setup, err := Open(path, migrations)
	if err != nil {
		t.Fatalf("Open() setup error = %v", err)
	}
	if err := setup.Close(); err != nil {
		t.Fatalf("close setup: %v", err)
	}

	const numProcs = 4
	const incrementsPerProc = 25

	var wg sync.WaitGroup
	for i := 0; i < numProcs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runCounterWorkerProcess(t, path, incrementsPerProc)
		}()
	}
	wg.Wait()

	final, err := Open(path, migrations)
	if err != nil {
		t.Fatalf("Open() 検証用の再オープン error = %v", err)
	}
	defer func() { _ = final.Close() }()

	var got int
	if err := final.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow("SELECT value FROM test_counter WHERE id = 1").Scan(&got)
	}); err != nil {
		t.Fatalf("read final counter value: %v", err)
	}

	want := numProcs * incrementsPerProc
	if got != want {
		t.Fatalf("test_counter value = %d, want %d (a concurrent write was lost)", got, want)
	}
}
