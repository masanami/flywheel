package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// realHarnessGuardExit は番兵の harness の終了コード。偽の実行ファイルのどのシナリオとも
// 重ならない値にする。
const realHarnessGuardExit = 99

// installRealHarnessGuard は、呼び出されると失敗するだけの番兵の harness を置いたディレクトリを
// PATH の先頭に足す（m3-invoker-delegation.md 受入基準「go test ./... は、PATH に本物の claude・gh・
// harness があっても、それらを起動しない」。installRealGHGuard・installRealClaudeGuard の harness 版。
// 衝突の予測の口は宣言の command を起動する。テストは偽の実行ファイルのパスを command に置く）。
func installRealHarnessGuard() (cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "flywheel-cli-harness-guard-")
	if err != nil {
		return nil, fmt.Errorf("mkdir harness guard: %w", err)
	}
	script := fmt.Sprintf("#!/bin/sh\necho 'cli tests must not invoke the real harness (put a fake executable and name it in the declaration)' >&2\nexit %d\n", realHarnessGuardExit)
	if err := os.WriteFile(filepath.Join(dir, "harness"), []byte(script), 0o755); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("write harness guard: %w", err)
	}
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("setenv PATH: %w", err)
	}
	return func() { _ = os.RemoveAll(dir) }, nil
}

// 受入基準「go test ./... は、PATH に本物の harness があっても、それを起動しない」の担保:
// PATH の harness は番兵に解決され、起動すると失敗する。
func TestRealHarnessGuard_ShadowsAnyHarnessOnPATH(t *testing.T) {
	path, err := exec.LookPath("harness")
	if err != nil {
		t.Fatalf("LookPath(harness): %v (the guard must be on PATH)", err)
	}
	if !strings.Contains(path, "flywheel-cli-harness-guard-") {
		t.Fatalf("harness resolves to %q, want the guard directory to come first on PATH", path)
	}
	err = exec.Command(path).Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != realHarnessGuardExit {
		t.Fatalf("running the guard: err = %v, want exit code %d", err, realHarnessGuardExit)
	}
}
