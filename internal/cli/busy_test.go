package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// busyHelperEnvVar が "1" のとき、このテストバイナリは通常のテストを実行せず、
// TestMain から分岐し、書き込みロックを保持し続ける「別のプロセス」として
// 振る舞う（internal/core/internal/store/busy_test.go と同じ手法。os/exec で
// テストバイナリ自身を再実行するのは標準ライブラリのテストでも使われる）。
const (
	busyHelperEnvVar   = "FLYWHEEL_CLI_TEST_BUSY_HOLDER"
	busyHelperDBPath   = "FLYWHEEL_CLI_TEST_DB_PATH"
	busyHelperReadyMsg = "ready\n"
)

func TestMain(m *testing.M) {
	if os.Getenv(busyHelperEnvVar) == "1" {
		busyHolderMain()
		return
	}
	if os.Getenv(confirmHelperEnvVar) == "1" {
		confirmHelperMain()
		return
	}
	os.Exit(m.Run())
}

func busyHolderMain() {
	path := os.Getenv(busyHelperDBPath)
	// coretest.HoldRawWriteLock は、internal/cli が modernc.org/sqlite を
	// 直接 import しなくて済むように internal/core/coretest（internal/core
	// 配下だけが持つ知識にアクセスできるテスト支援パッケージ）へ委譲する
	// （internal/cli/depcheck_test.go が直接 import が無いことを検査する）。
	os.Exit(coretest.HoldRawWriteLock(path, os.Stdin, os.Stdout, os.Stderr))
}

// TestRun_InitReturnsStoreBusyWhileAnotherProcessHoldsTheWriteLock は、実際の
// CLI 入口（cli.Run）を通して store_busy が正しく写像されることを検証する
// end-to-end のテスト。init はこの時点で唯一「実際に書き込む」コマンドである
// （マイグレーションの適用・一度も WAL にしたことが無いファイルの
// journal_mode=WAL への変換）。
//
// 挙動メモ（macOS・modernc.org/sqlite v1.59.0 で実測）: 一度も WAL にしていない
// ファイルに対する `PRAGMA journal_mode=WAL` は、他プロセスが書き込みロックを
// 保持していると busy_timeout を待たず ほぼ即座に SQLITE_BUSY で失敗する
// （実測 3ms 前後）。一方、既に WAL 化されたストアへの再オープン時の
// `PRAGMA journal_mode=WAL`（値が変わらないので no-op）や `PRAGMA user_version`
// の読み取りは、他プロセスが書き込みロックを保持していても一切ブロックされない
// （WAL の設計どおり、読み取りは書き込みと競合しない）。busy_timeout が実際に
// 効く（待ってから失敗する）のは `BEGIN IMMEDIATE`（書き込みトランザクションの
// 開始）であり、これは internal/core/internal/store/busy_test.go の
// TestOpen_WriteTimesOutWithErrBusyWhileAnotherProcessHoldsAWriteLock で
// 別途、短縮した busy_timeout により検証している。そのためこのテストでは
// 「待ってから失敗する」時間の長さを assert しない（fail-closed の結果
// store_busy になること自体を検証する）。
func TestRun_InitReturnsStoreBusyWhileAnotherProcessHoldsTheWriteLock(t *testing.T) {
	dir := t.TempDir()
	flywheelDir := filepath.Join(dir, ".flywheel")
	if err := os.MkdirAll(flywheelDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dbPath := filepath.Join(flywheelDir, "flywheel.db")

	holder := startCLIBusyHolderProcess(t, dbPath)
	defer holder.stop(t)
	holder.waitUntilReady(t)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"init", "--workspace", dir, "--json"}, strings.NewReader(""), &stdout, &stderr)

	if code != 2 {
		t.Fatalf("exit=%d, want 2 (stdout=%s stderr=%s)", code, stdout.String(), stderr.String())
	}
	var doc struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &doc); err != nil {
		t.Fatalf("stderr is not valid JSON: %v (%q)", err, stderr.String())
	}
	if doc.Error.Code != string(CodeStoreBusy) {
		t.Fatalf("error code = %q, want %q (stderr=%s)", doc.Error.Code, CodeStoreBusy, stderr.String())
	}
}

// TestRun_CreateReturnsStoreBusyWhileAnotherProcessHoldsTheWriteLock は、
// レビュー指摘（internal/core.mutate が store.DB.Write のエラーを
// classifyReadWriteErr に通していなかったため、書き込みロック競合時の create
// が store_busy ではなく internal_error になっていた）の再発防止テストである。
// init と異なり、事前に正常な init で WAL 化済みのストアを用意してから
// ロックを取らせる（未 WAL 化ファイルへの初回 PRAGMA journal_mode=WAL は
// busy_timeout を待たず即座に失敗するため、BEGIN IMMEDIATE が実際に
// busy_timeout の対象になるのは既に WAL 化されたストアに対してだけ、という
// 上のコメントの挙動メモに従う）。
func TestRun_CreateReturnsStoreBusyWhileAnotherProcessHoldsTheWriteLock(t *testing.T) {
	dir := t.TempDir()
	var initStdout, initStderr bytes.Buffer
	if code := Run([]string{"init", "--workspace", dir, "--json"}, strings.NewReader(""), &initStdout, &initStderr); code != 0 {
		t.Fatalf("init setup failed: exit=%d stderr=%s", code, initStderr.String())
	}
	dbPath := filepath.Join(dir, ".flywheel", "flywheel.db")

	holder := startCLIBusyHolderProcess(t, dbPath)
	defer holder.stop(t)
	holder.waitUntilReady(t)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"create", "--title", "t", "--workspace", dir, "--json"}, strings.NewReader(""), &stdout, &stderr)

	if code != 2 {
		t.Fatalf("exit=%d, want 2 (stdout=%s stderr=%s)", code, stdout.String(), stderr.String())
	}
	var doc struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &doc); err != nil {
		t.Fatalf("stderr is not valid JSON: %v (%q)", err, stderr.String())
	}
	if doc.Error.Code != string(CodeStoreBusy) {
		t.Fatalf("error code = %q, want %q (stderr=%s)", doc.Error.Code, CodeStoreBusy, stderr.String())
	}
}

type cliBusyHolder struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stderr  *bytes.Buffer
	readyCh chan error
	done    chan struct{}
	waitErr error
}

func startCLIBusyHolderProcess(t *testing.T, dbPath string) *cliBusyHolder {
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

	h := &cliBusyHolder{cmd: cmd, stdin: stdin, stderr: &stderrBuf, readyCh: make(chan error, 1), done: make(chan struct{})}
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

func (h *cliBusyHolder) waitUntilReady(t *testing.T) {
	t.Helper()
	select {
	case err := <-h.readyCh:
		if err != nil {
			// 子が ready を出す前に終了した場合は 10 秒待たず、stderr を添えて
			// 即失敗させる。
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

func (h *cliBusyHolder) stop(t *testing.T) {
	t.Helper()
	_ = h.stdin.Close()
	select {
	case <-h.done:
	case <-time.After(10 * time.Second):
		_ = h.cmd.Process.Kill()
		t.Fatal("busy-holder process did not exit after stdin closed")
	}
}
