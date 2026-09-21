package cli

import (
	"os"
	"path/filepath"
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
