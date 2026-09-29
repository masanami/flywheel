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

// realClaudeGuardExit は番兵の claude の終了コード。偽の claude のどのシナリオとも
// 重ならない値にする。
const realClaudeGuardExit = 98

// installRealClaudeGuard は、呼び出されると失敗するだけの番兵の claude を置いた
// ディレクトリを PATH の先頭に足す（m3-invoker-delegation.md 受入基準「go test ./... は、
// PATH に本物の claude があっても、それを起動しない」。installRealGHGuard の claude 版。
// 偽の claude を置き忘れたテストが本物の claude〈課金される〉へ届かないようにする）。
// 偽の claude を使うテストは t.Setenv でさらにその前へ足すので、番兵より先に解決される。
// PATH に claude が無い環境を作る emptyPATH は PATH ごと差し替えるので影響を受けない。
func installRealClaudeGuard() (cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "flywheel-cli-claude-guard-")
	if err != nil {
		return nil, fmt.Errorf("mkdir claude guard: %w", err)
	}
	script := fmt.Sprintf("#!/bin/sh\necho 'cli tests must not invoke the real claude (put a fake claude on PATH)' >&2\nexit %d\n", realClaudeGuardExit)
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("write claude guard: %w", err)
	}
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("setenv PATH: %w", err)
	}
	return func() { _ = os.RemoveAll(dir) }, nil
}

// 受入基準「go test ./... は、PATH に本物の claude があっても、それを起動しない」の担保:
// 偽の claude を置かないテストでは、PATH の claude が番兵に解決され、起動すると失敗する。
func TestRealClaudeGuard_ShadowsAnyClaudeOnPATHUnlessAFakeIsPlaced(t *testing.T) {
	path, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("LookPath(claude): %v (the guard must be on PATH)", err)
	}
	if !strings.Contains(path, "flywheel-cli-claude-guard-") {
		t.Fatalf("claude resolves to %q, want the guard directory to come first on PATH", path)
	}
	err = exec.Command(path).Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != realClaudeGuardExit {
		t.Fatalf("running the guard: err = %v, want exit code %d", err, realClaudeGuardExit)
	}
}
