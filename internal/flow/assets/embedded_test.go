package assets_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/flow/assets"
	"github.com/masanami/flywheel/internal/flow/workflow"
)

// 展開した資材の定義を検査する。プラグインのエージェント定義は埋め込まないので、参照するエージェントの空の写しを置く。
func validateEmbedded(t *testing.T, id string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "runtime", "test")
	l, err := assets.Ensure(assets.FS, root)
	if err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(t.TempDir(), "agents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := fs.ReadFile(assets.FS, "workflows/"+id+".yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`agent:\s*claude-harness:([A-Za-z0-9_-]+)`).FindAllSubmatch(data, -1) {
		// 参照先は写し元（claude-harness b4d6154）のプラグインに在るエージェントに固定する。打ち間違いは落とす。
		if string(m[1]) != "feature-implementer" {
			t.Fatalf("%s.yaml refers to agent %q, which is not a known claude-harness agent", id, m[1])
		}
		if err := os.WriteFile(filepath.Join(agents, string(m[1])+".md"), []byte("---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// pr-state の観測は engine が登録する（workflow は engine に依存しない）。
	workflow.RegisterObservation("pr-state", []string{"merged", "open", "closed"})
	workflow.RegisterObservationInputs("pr-state", "pr")
	if _, err := workflow.LoadAndValidate(filepath.Join(l.WorkflowDir, id+".yaml"), workflow.Options{ScriptsDir: filepath.Join(l.WorkflowDir, "scripts"), AgentsDir: agents}); err != nil {
		t.Fatalf("%s must pass the workflow validation: %v", id, err)
	}
}

// AC-2
func TestEmbeddedTicketPassesValidation(t *testing.T) { validateEmbedded(t, "ticket") }

// AC-3
func TestEmbeddedReflectPassesValidation(t *testing.T) { validateEmbedded(t, "reflect") }

// AC-138: 定義には models も、model を書いたステップも、モデルの参照も無い。
func TestEmbeddedDefinitionsHaveNoModel(t *testing.T) {
	model := regexp.MustCompile(`\bmodels?\b`)
	for _, id := range []string{"ticket", "reflect"} {
		data, err := fs.ReadFile(assets.FS, "workflows/"+id+".yaml")
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if code, _, _ := strings.Cut(line, "#"); model.MatchString(code) {
				t.Errorf("%s.yaml:%d mentions a model: %q", id, i+1, line)
			}
		}
	}
}

// 資材のスクリプトは、展開すると実行できる（実行ビット）。
func TestExtractedScriptsAreExecutable(t *testing.T) {
	l, err := assets.Ensure(assets.FS, filepath.Join(t.TempDir(), "r"))
	if err != nil {
		t.Fatal(err)
	}
	scripts, _ := filepath.Glob(filepath.Join(l.WorkflowDir, "scripts", "*.sh"))
	if len(scripts) != 3 {
		t.Fatalf("scripts = %v", scripts)
	}
	for _, s := range scripts {
		if fi, _ := os.Stat(s); fi.Mode().Perm()&0o100 == 0 {
			t.Errorf("%s is not executable", s)
		}
	}
}

// internal/flow/assets が import してよいのは標準ライブラリだけ（試験を除く）。
func TestAssetsImportOnlyStandardLibrary(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	if len(files) == 0 {
		t.Fatal("no Go files found to check")
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if first, _, _ := strings.Cut(p, "/"); strings.Contains(first, ".") {
				t.Errorf("%s imports %s (only the standard library is allowed)", f, p)
			}
		}
	}
}

// AC-6: 写した先のディレクトリの文書に、写した元のリポジトリとコミットが書かれている。
func TestReadmeNamesTheSource(t *testing.T) {
	b, err := os.ReadFile("../README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"masanami/claude-harness", "b4d6154"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("internal/flow/README.md does not mention %q", want)
		}
	}
}
