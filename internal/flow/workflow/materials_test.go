package workflow

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"

	"github.com/masanami/flywheel/internal/flow/assets"
)

// 埋め込んだ資材（internal/flow/assets）を試験ごとの一時ディレクトリへ展開し、同梱の定義を読む試験の前提を作る。
// 展開先の版のディレクトリは不変なので、試験ごとに別のディレクトリへ展開する。

// bundledRoot は埋め込んだ資材を展開したディレクトリを返す。
func bundledRoot(t *testing.T) string {
	t.Helper()
	roots.Lock()
	defer roots.Unlock()
	if r, ok := roots.m[t]; ok {
		return r
	}
	root := filepath.Join(t.TempDir(), "runtime", "test")
	if _, err := assets.Ensure(assets.FS, root); err != nil {
		t.Fatal(err)
	}
	roots.m[t] = root
	return root
}

// 同じ試験の中の呼び出しは同じ展開先を返す（試験ごとに 1 回だけ展開する）。
var roots = struct {
	sync.Mutex
	m map[*testing.T]string
}{m: map[*testing.T]string{}}

// bundledPath は埋め込んだ定義（ticket・reflect）の展開先のパスを返す。
func bundledPath(t *testing.T, id string) string {
	t.Helper()
	return filepath.Join(bundledRoot(t), "workflows", id+".yaml")
}

var agentRef = regexp.MustCompile(`agent:\s*claude-harness:([A-Za-z0-9_-]+)`)

// bundledOpts は同梱の定義を検査する Options を返す。プラグインのスクリプト・エージェント定義は埋め込まないので、
// 定義が参照するエージェントは検査のための空の写しを一時ディレクトリへ置く。
func bundledOpts(t *testing.T) Options {
	t.Helper()
	agents := filepath.Join(t.TempDir(), "agents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ticket", "reflect"} {
		data, err := fs.ReadFile(assets.FS, "workflows/"+id+".yaml")
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range agentRef.FindAllSubmatch(data, -1) {
			if err := os.WriteFile(filepath.Join(agents, string(m[1])+".md"), []byte("---\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return Options{ScriptsDir: filepath.Join(filepath.Dir(bundledPath(t, "ticket")), "scripts"), AgentsDir: agents}
}
