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
	cmd := exec.Command(bin, "--help")
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
	cmd := exec.Command(bin, "create", "--nope", "x", "--json")
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
	cmd := exec.Command(bin, "status", "--json")
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

func TestBinary_NoArgsExitsTwo(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin)
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *exec.ExitError, got %v", err)
	}
	if exitErr.ExitCode() != 2 {
		t.Fatalf("exit code = %d, want 2", exitErr.ExitCode())
	}
}
