package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// AC-115: CLI のコマンドの一覧と、サーバーの経路の一覧に、supervisor_resolve を起こす口は無い。
// 操作 supervisor_resolve（T20）は、スーパーバイザーの段の手の実行だけが通す。
func TestSupervisorResolve_HasNoCommandAndNoRoute(t *testing.T) {
	t.Parallel()
	for _, c := range defaultCommands() {
		for _, tok := range c.Path {
			if strings.Contains(tok, "supervisor") || strings.Contains(tok, "resolve") {
				t.Errorf("command %v has the token %q; no command may start supervisor_resolve", c.Path, tok)
			}
		}
	}
	for name, op := range operationByCommand {
		if op == core.OpSupervisorResolve {
			t.Errorf("command %q maps to supervisor_resolve", name)
		}
	}
	for _, dir := range []string{".", filepath.Join("..", "server")} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no Go files in %s: %v", dir, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if s := string(b); strings.Contains(s, "OpSupervisorResolve") || strings.Contains(s, "supervisor_resolve") {
				t.Errorf("%s refers to supervisor_resolve; the CLI and the server must not start it", f)
			}
			if strings.Contains(string(b), "ecordSupervisorAction") {
				t.Errorf("%s refers to the supervisor action recorder; only the supervisor stage may write the record", f)
			}
		}
	}
}
