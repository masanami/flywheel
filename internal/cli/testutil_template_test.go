package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// initTemplate は Init 済みのワークスペースを 1 度だけ作って保持する（テストの
// 用意だけを速くする。マイグレーションの適用は -race で 1 件あたり秒単位かかるため、
// 1,000 件超のテストで毎回やると全体の大半を占める）。各テストへは copyInitTemplate
// で複製して渡す。Init 自体を検証するテストは、このヘルパーを使わず Init を呼ぶ。
var initTemplate struct {
	once sync.Once
	dir  string
	err  error
}

// copyInitTemplate は Init 済みのワークスペースの複製を dst（既存の空のディレクトリ）へ作る。
func copyInitTemplate(t *testing.T, dst string) {
	t.Helper()
	initTemplate.once.Do(func() {
		dir, err := os.MkdirTemp("", "flywheel-cli-template-")
		if err != nil {
			initTemplate.err = err
			return
		}
		initTemplate.dir = dir
		_, initTemplate.err = core.Init(dir)
	})
	if initTemplate.err != nil {
		t.Fatalf("Init() template setup error = %v", initTemplate.err)
	}
	src := initTemplate.dir
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatalf("copy init template error = %v", err)
	}
}

// removeInitTemplate は雛形のディレクトリを消す。TestMain が m.Run の後に呼ぶ
// （テストの実行で TMPDIR に雛形を残さない）。
func removeInitTemplate() {
	if initTemplate.dir != "" {
		_ = os.RemoveAll(initTemplate.dir)
	}
}
