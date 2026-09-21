package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/masanami/flywheel/internal/core"
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
	if _, err := core.Init(dir); err != nil {
		t.Fatalf("core.Init(%q) setup error = %v", dir, err)
	}
	return dir
}
