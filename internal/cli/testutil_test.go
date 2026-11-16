package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

// repoRoot はテスト実行時のカレントディレクトリから go.mod を上方へ探し、
// リポジトリのルートディレクトリを返す。実ソースの静的検査系のテスト
// （エラーコードの閉包・絶対パスの検査など）が起点に使う。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found while walking up from test working directory")
		}
		dir = parent
	}
}

// initializedWorkspace は t.TempDir() に core.Init 済みのワークスペースを作り、
// その絶対パスを返す。RequiresStore なコマンドを --workspace 付きで叩く
// テストの共通セットアップとして使う。
func initializedWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	copyInitTemplate(t, dir)
	return dir
}

// combinedOutputRetrySignal は mk が作るコマンドを実行する。t.Parallel のテストが同時に子プロセスを起こす負荷の下で、
// macOS では子プロセス（git・sh）が稀に SIGSEGV で終わることがあるため、シグナルでの終了に限って作り直して数回やり直す。
// 終了コードでの失敗はやり直さない。
func combinedOutputRetrySignal(mk func() *exec.Cmd) ([]byte, error) {
	var out []byte
	var err error
	for i := 0; i < 3; i++ {
		out, err = mk().CombinedOutput()
		var ee *exec.ExitError
		if err == nil || !errors.As(err, &ee) {
			return out, err
		}
		if ws, ok := ee.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() {
			return out, err
		}
	}
	return out, err
}
