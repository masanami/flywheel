package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// busyHelperEnvVar が "1" のとき、このテストバイナリは通常のテストを実行せず、
// TestMain から busyHolderMain へ分岐し、書き込みロックを保持し続ける
// 「別のプロセス」として振る舞う。os/exec を使ってテストバイナリ自身を
// 再実行するのは、Go の標準ライブラリのテスト（例: os/exec 自身のテスト）でも
// 使われる手法であり、macOS・Linux で同じに振る舞う。
const (
	busyHelperEnvVar   = "FLYWHEEL_STORE_TEST_BUSY_HOLDER"
	busyHelperDBPath   = "FLYWHEEL_STORE_TEST_DB_PATH"
	busyHelperReadyMsg = "ready\n"
)

func TestMain(m *testing.M) {
	if os.Getenv(busyHelperEnvVar) == "1" {
		busyHolderMain()
		return
	}
	if os.Getenv(counterHelperEnvVar) == "1" {
		counterWorkerMain()
		return
	}
	os.Exit(m.Run())
}

// busyHolderMain は書き込みトランザクション（BEGIN IMMEDIATE）を開始して
// 標準入力が閉じられるまで保持し続ける。準備できたら標準出力へ "ready" を書く。
func busyHolderMain() {
	path := os.Getenv(busyHelperDBPath)
	db, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		fmt.Fprintln(os.Stderr, "busyHolderMain: open:", err)
		os.Exit(1)
	}
	db.SetMaxOpenConns(1)

	tx, err := db.Begin()
	if err != nil {
		fmt.Fprintln(os.Stderr, "busyHolderMain: begin immediate:", err)
		os.Exit(1)
	}

	if _, err := fmt.Fprint(os.Stdout, busyHelperReadyMsg); err != nil {
		os.Exit(1)
	}

	// 親プロセスが停止させる（stdin を閉じる）まで、書き込みロックを保持し続ける。
	_, _ = io.Copy(io.Discard, os.Stdin)

	_ = tx.Rollback()
	_ = db.Close()
	os.Exit(0)
}

// TestOpen_WriteTimesOutWithErrBusyWhileAnotherProcessHoldsAWriteLock は
// クリティカル設計決定 2・完了条件「別のプロセスが書き込みトランザクションを
// 保持し続けている間の書き込みは、待ちの上限を超えた時点で store_busy」の検証。
func TestOpen_WriteTimesOutWithErrBusyWhileAnotherProcessHoldsAWriteLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flywheel.db")

	// 先にストアを作っておく（ヘルパーが書き込みロックを取れるように）。
	db, err := Open(path, prodMigrations(t))
	if err != nil {
		t.Fatalf("Open() setup error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close setup db: %v", err)
	}

	holder := startBusyHolderProcess(t, path)
	defer holder.stop(t)
	holder.waitUntilReady(t)

	// busy_timeout を短縮した DB を、本番の DSN 組み立て（dsnFor）を通したまま
	// 作る（本番の 5000ms を待つとテストが遅くなるため、テストだけ短縮する。
	// 独自に DSN 文字列を組み立てて迂回すると、dsnFor 自体の回帰を検出できない
	// ため openWithOptions を経由する。レビュー指摘）。
	testDB, err := openWithOptions(path, prodMigrations(t), openOptions{
		mode:              modeExistingOnly,
		busyTimeoutMillis: 300,
	})
	if err != nil {
		t.Fatalf("openWithOptions() error = %v", err)
	}
	defer func() { _ = testDB.Close() }()

	start := time.Now()
	writeErr := testDB.Write(context.Background(), func(*sql.Tx) error {
		return nil
	})
	elapsed := time.Since(start)
	if writeErr == nil {
		t.Fatal("Write() while another process holds the write lock should fail, got nil error")
	}
	if !errors.Is(writeErr, ErrBusy) {
		t.Fatalf("Write() error = %v, want ErrBusy", writeErr)
	}
	// busy_timeout=300ms 待ってから失敗しているはず（即座の失敗ではないこと）。
	if elapsed < 250*time.Millisecond {
		t.Fatalf("Begin() returned too fast (%v); expected it to wait close to the busy_timeout", elapsed)
	}
}

type busyHolder struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stderr  *bytes.Buffer
	readyCh chan error
	done    chan struct{}
	waitErr error
}

func startBusyHolderProcess(t *testing.T, dbPath string) *busyHolder {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), busyHelperEnvVar+"=1", busyHelperDBPath+"="+dbPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		t.Fatalf("start busy-holder process: %v", err)
	}

	h := &busyHolder{cmd: cmd, stdin: stdin, stderr: &stderrBuf, readyCh: make(chan error, 1), done: make(chan struct{})}
	go func() {
		buf := make([]byte, len(busyHelperReadyMsg))
		_, readErr := io.ReadFull(stdout, buf)
		if readErr == nil && string(buf) != busyHelperReadyMsg {
			readErr = fmt.Errorf("unexpected stdout from busy-holder: %q", buf)
		}
		h.readyCh <- readErr
		// cmd.Wait は、パイプからの読み取りをすべて終えてから呼ぶ（os/exec の
		// 規約: "it is incorrect to call Wait before all reads from the pipe
		// have completed"）。ready メッセージの後は何も書かれない想定だが、
		// 念のため EOF まで読み切ってから Wait する。
		_, _ = io.Copy(io.Discard, stdout)
		h.waitErr = cmd.Wait()
		close(h.done)
	}()
	return h
}

func (h *busyHolder) waitUntilReady(t *testing.T) {
	t.Helper()
	select {
	case err := <-h.readyCh:
		if err != nil {
			// 子が ready を出す前に終了した場合は 10 秒待たず、stderr を添えて
			// 即失敗させる（プロセスは stdout を閉じて EOF を返した時点で終了
			// しているはずなので、done の待ちは短い上限で十分）。
			select {
			case <-h.done:
			case <-time.After(2 * time.Second):
				// done 前は waitErr・stderr を別 goroutine が書きうるため読まない（データ競合）。
				t.Fatalf("busy-holder process failed before signaling ready and did not exit: %v", err)
			}
			t.Fatalf("busy-holder process exited before signaling ready: %v (wait err=%v, stderr=%s)", err, h.waitErr, h.stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the busy-holder process to acquire the write lock")
	}
}

func (h *busyHolder) stop(t *testing.T) {
	t.Helper()
	_ = h.stdin.Close()
	select {
	case <-h.done:
	case <-time.After(10 * time.Second):
		_ = h.cmd.Process.Kill()
		t.Fatal("busy-holder process did not exit after stdin closed")
	}
}

// 別のプロセスが書き込みトランザクションを保持していても、Read（読み取り専用の
// トランザクション）は書き込みロックを要求せず、待たずに成功する（WAL）。
func TestOpen_ReadSucceedsWhileAnotherProcessHoldsAWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flywheel.db")
	db, err := Open(path, prodMigrations(t))
	if err != nil {
		t.Fatalf("Open() setup error = %v", err)
	}
	defer func() { _ = db.Close() }()

	holder := startBusyHolderProcess(t, path)
	defer holder.stop(t)
	holder.waitUntilReady(t)

	var count int
	readErr := db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow("SELECT COUNT(*) FROM challenge").Scan(&count)
	})
	if readErr != nil {
		t.Fatalf("Read() while another process holds the write lock error = %v, want nil", readErr)
	}
}
