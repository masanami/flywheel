package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// newChildCmd は bin を args で実行する *exec.Cmd を、FLYWHEEL_WORKSPACE を
// 明示的に空へ中和した環境で組み立てる。開発者・CI の実行環境に
// FLYWHEEL_WORKSPACE がたまたま設定されていても、--workspace を明示するケース
// （env より優先されるので影響しない）・cwd 起点の探索に頼るケース（env が
// 設定されていると親へ遡らず即座に別の場所を見てしまう）のどちらもこの汚染を
// 受けないようにする（レビュー指摘 item 8(e)）。
func newChildCmd(bin string, args ...string) *exec.Cmd {
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "FLYWHEEL_WORKSPACE=")
	return cmd
}

// buildBinary は ./cmd/flywheel を CGO_ENABLED=0 でビルドし、生成物のパスを返す。
// AC-78・AC-74・AC-77 の一部は、実際にビルドしたバイナリへの子プロセス起動で検証する。
func buildBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "flywheel")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build ./cmd/flywheel failed: %v\n%s", err, out)
	}
	return bin
}

func TestBinary_HelpExitsZeroWithUsageOnStdout(t *testing.T) {
	bin := buildBinary(t)
	cmd := newChildCmd(bin, "--help")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("--help failed: %v (stderr=%s)", err, stderr.String())
	}
	if stdout.Len() == 0 {
		t.Fatal("--help produced no stdout")
	}
	if stderr.Len() != 0 {
		t.Fatalf("--help produced stderr: %q", stderr.String())
	}
}

func TestBinary_UnknownFlagExitsTwoWithJSONUsageError(t *testing.T) {
	bin := buildBinary(t)
	cmd := newChildCmd(bin, "create", "--nope", "x", "--json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *exec.ExitError, got %v", err)
	}
	if exitErr.ExitCode() != 2 {
		t.Fatalf("exit code = %d, want 2", exitErr.ExitCode())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout should be empty on failure, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), `"code":"usage_error"`) {
		t.Fatalf("stderr = %q, want usage_error envelope", stderr.String())
	}
}

func TestBinary_StubCommandExitsTwoWithJSONInternalError(t *testing.T) {
	bin := buildBinary(t)
	// status は RequiresStore（本チケットで init 以外の全コマンドに適用した
	// ストア事前チェック）のため、先に init でワークスペースを用意する。
	ws := t.TempDir()
	runOK(t, bin, "init", "--workspace", ws, "--json")

	cmd := newChildCmd(bin, "status", "--workspace", ws, "--json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *exec.ExitError, got %v", err)
	}
	if exitErr.ExitCode() != 2 {
		t.Fatalf("exit code = %d, want 2", exitErr.ExitCode())
	}
	if !strings.Contains(stderr.String(), `"code":"internal_error"`) {
		t.Fatalf("stderr = %q, want internal_error envelope", stderr.String())
	}
}

// runOK は bin を args で実行し、終了コード 0 でなければテストを失敗させる。
func runOK(t *testing.T, bin string, args ...string) {
	t.Helper()
	cmd := newChildCmd(bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %v failed: %v (stdout=%s stderr=%s)", bin, args, err, stdout.String(), stderr.String())
	}
}

// TestBinary_InitCreatesStoreAndStatusSeesIt は init → status を実バイナリの
// 子プロセスとして通しで実行し、AC「flywheel init を実行すると…終了コード 0」と
// 「init 以外のコマンドは…上位の .flywheel/flywheel.db を使う」の一部を、
// CGO_ENABLED=0 のビルド成果物で検証する。
func TestBinary_InitCreatesStoreAndStatusSeesIt(t *testing.T) {
	bin := buildBinary(t)
	ws := t.TempDir()
	runOK(t, bin, "init", "--workspace", ws, "--json")
	if _, err := os.Stat(filepath.Join(ws, ".flywheel", "flywheel.db")); err != nil {
		t.Fatalf("store not created by the built binary: %v", err)
	}

	child := filepath.Join(ws, "sub", "dir")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cmd := newChildCmd(bin, "status", "--json")
	cmd.Dir = child
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *exec.ExitError (status is still a stub), got %v (stdout=%s)", err, stdout.String())
	}
	if !strings.Contains(stderr.String(), `"code":"internal_error"`) {
		t.Fatalf("stderr = %q, want internal_error (store found upward, stub reached)", stderr.String())
	}
}

func TestBinary_NoArgsExitsTwo(t *testing.T) {
	bin := buildBinary(t)
	cmd := newChildCmd(bin)
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *exec.ExitError, got %v", err)
	}
	if exitErr.ExitCode() != 2 {
		t.Fatalf("exit code = %d, want 2", exitErr.ExitCode())
	}
}
