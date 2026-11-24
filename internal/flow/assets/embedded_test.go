package assets_test

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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

// parseEmbedded は埋め込んだ定義を読み込む（検査は validateEmbedded が行う）。
func parseEmbedded(t *testing.T, id string) *workflow.Workflow {
	t.Helper()
	data, err := fs.ReadFile(assets.FS, "workflows/"+id+".yaml")
	if err != nil {
		t.Fatal(err)
	}
	wf, err := workflow.Parse(id+".yaml", data)
	if err != nil {
		t.Fatal(err)
	}
	return wf
}

func mainVars(t *testing.T, wf *workflow.Workflow) (models, efforts map[string]string) {
	t.Helper()
	return workflow.ResolveModels(wf), workflow.ResolveEfforts(wf)
}

// AC-99・AC-100・AC-105・AC-106: models は main: opus だけ、efforts は main: xhigh だけ。
func TestEmbeddedDefinitionsFixModelAndEffort(t *testing.T) {
	for _, id := range []string{"ticket", "reflect"} {
		models, efforts := mainVars(t, parseEmbedded(t, id))
		if len(models) != 1 || models["main"] != "opus" {
			t.Errorf("%s models = %v, want main: opus only", id, models)
		}
		if len(efforts) != 1 || efforts["main"] != "xhigh" {
			t.Errorf("%s efforts = %v, want main: xhigh only", id, efforts)
		}
	}
}

// AC-101〜AC-104・AC-107・AC-108: ticket の implement と fix は model・effort を持たず、ほかの llm のステップは $models.main・$efforts.main を指す。
// reflect はすべての llm のステップが指す。
func TestEmbeddedLLMStepsReferenceMainModelAndEffort(t *testing.T) {
	seen := map[string]int{}
	for _, id := range []string{"ticket", "reflect"} {
		for _, st := range parseEmbedded(t, id).Steps {
			if st.Kind != "llm" {
				if st.Model != "" || st.Effort != "" {
					t.Errorf("%s.%s is not an llm step but has model/effort", id, st.ID)
				}
				continue
			}
			seen[id]++
			free := id == "ticket" && (st.ID == "implement" || st.ID == "fix")
			wantModel, wantEffort := "main", "main"
			if free {
				wantModel, wantEffort = "", ""
			}
			if st.Model != wantModel {
				t.Errorf("%s.%s model variable = %q, want %q", id, st.ID, st.Model, wantModel)
			}
			if st.Effort != wantEffort {
				t.Errorf("%s.%s effort variable = %q, want %q", id, st.ID, st.Effort, wantEffort)
			}
		}
	}
	// 取りこぼし（llm のステップの増減）に気付くための件数。ticket は analyze・implement・fix・commit・sync-resolve・e2e・land-sync-resolve・respond・merge、reflect は describe・sync-resolve・land-sync-resolve。
	if seen["ticket"] != 9 || seen["reflect"] != 3 {
		t.Errorf("llm steps: ticket %d (want 9), reflect %d (want 3)", seen["ticket"], seen["reflect"])
	}
}

// AC-67: ticket の implement の with の codex_review は、文字列の "on"。
func TestEmbeddedTicketImplementPassesCodexReviewOn(t *testing.T) {
	st := parseEmbedded(t, "ticket").Step("implement")
	var got []any
	for _, b := range st.With {
		if b.Name == "codex_review" {
			got = append(got, b.Value.Literal)
		}
	}
	if len(got) != 1 || got[0] != "on" {
		t.Errorf("implement with codex_review = %#v, want the string \"on\" exactly once", got)
	}
}

// AC-71: 実装の出力の型は codex_review を必須の欄に持ち、値を 5 つに限る。
func TestImplementResultRequiresCodexReviewWithFiveValues(t *testing.T) {
	data, err := fs.ReadFile(assets.FS, "workflows/schemas/implement-result.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type string   `json:"type"`
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &sc); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(sc.Required, "codex_review") {
		t.Errorf("codex_review is not required: %v", sc.Required)
	}
	p := sc.Properties["codex_review"]
	if want := []string{"off", "complete", "partial", "failed", "skipped"}; p.Type != "string" || !slices.Equal(p.Enum, want) {
		t.Errorf("codex_review = %+v, want a string enum %v", p, want)
	}
}

func readPrompt(t *testing.T, name string) string {
	t.Helper()
	data, err := fs.ReadFile(assets.FS, "workflows/prompts/"+name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// sentences は指示文を、行と文（。）の区切りに分ける。ある語どうしが 1 つの文に在るかを見るため。
func sentences(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		for _, s := range strings.SplitAfter(line, "。") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// 1 つの文が、すべての語を含むか。
func hasSentenceWith(text string, terms ...string) bool {
	for _, s := range sentences(text) {
		all := true
		for _, term := range terms {
			if !strings.Contains(s, term) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// AC-72〜AC-80: ticket-implement.md の Codex のレビューの指示。
func TestTicketImplementPromptInstructsCodexReview(t *testing.T) {
	p := readPrompt(t, "ticket-implement.md")
	cases := []struct {
		ac    string
		terms []string
	}{
		{"AC-72", []string{"`inputs.codex_review` が `on`", "claude-harness-run codex-review-runner", "`/self-review` と並べて", "裏で起動"}},
		{"AC-73", []string{"終わるまで", "返さない"}},
		{"AC-74", []string{"中以上", "`refuted`", "直す"}},
		{"AC-75", []string{"直した後", "`/quality-check`"}},
		{"AC-76", []string{"`[Codex] `", "claim", "residual_findings"}},
		{"AC-77", []string{"完了しなかった", "`unverified`"}},
		{"AC-78", []string{"claude-harness-run cleanup-review-diff", "成否に関わらず"}},
		{"AC-79", []string{"変更が無い", "`rerun`", "claude-harness-run codex-review-runner"}},
		{"AC-80", []string{"`codex_review`", "`off`", "`failed`", "`skipped`"}},
	}
	for _, c := range cases {
		if !hasSentenceWith(p, c.terms...) {
			t.Errorf("%s: no single sentence of ticket-implement.md has all of %q", c.ac, c.terms)
		}
	}
	// 逆の意味に書き換えられていないこと（語の有無だけでは、否定や取り違えを見逃す）
	for _, bad := range []string{"codex-review-runner` を走らせない", "refuted` も", "Codex を走らせる。`codex_review` は `skipped"} {
		if strings.Contains(p, bad) {
			t.Errorf("ticket-implement.md contains the inverted instruction %q", bad)
		}
	}
	if i, j := strings.Index(p, "claude-harness-run codex-review-runner"), strings.Index(p, "`/quality-check` を通す"); i < 0 || j < 0 || i > j {
		t.Errorf("codex-review-runner must be introduced before the /quality-check after fixing")
	}
	// 手順 1・2（差分は collect-review-diff で集め、要件の文脈は一時ファイルで渡す）
	for _, term := range []string{"claude-harness-run collect-review-diff", "codex-review-context.XXXXXX"} {
		if !strings.Contains(p, term) {
			t.Errorf("ticket-implement.md lacks %q", term)
		}
	}
}

// AC-83・AC-85: 直しと PR のレビュー対応のステップの指示文は、Codex を起動する語を一切持たない（否定の文にも書かない）。
func TestFixAndRespondPromptsDoNotMentionCodexReviewRunner(t *testing.T) {
	for _, name := range []string{"ticket-fix.md", "ticket-respond.md"} {
		if strings.Contains(readPrompt(t, name), "codex-review-runner") {
			t.Errorf("%s mentions codex-review-runner", name)
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
	if len(scripts) != 4 {
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
