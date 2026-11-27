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

// AC-101〜AC-104・AC-107・AC-108: ticket の implement・fix・self-review-fix（feature-implementer のステップ）は model・effort を持たず、ほかの llm のステップは $models.main・$efforts.main を指す。
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
			free := id == "ticket" && (st.ID == "implement" || st.ID == "fix" || st.ID == "self-review-fix")
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
	// 取りこぼし（llm のステップの増減）に気付くための件数。ticket は analyze・implement・self-review・self-review-fix・fix・commit・sync-resolve・e2e・land-sync-resolve・respond・merge、reflect は describe・sync-resolve・land-sync-resolve。
	if seen["ticket"] != 11 || seen["reflect"] != 3 {
		t.Errorf("llm steps: ticket %d (want 11), reflect %d (want 3)", seen["ticket"], seen["reflect"])
	}
}

// レビューのステップの入力（with）の既定: 標準はレビュアー 2 体・指摘を疑う役・Codex を使う、簡略はレビュアー 2 体・疑う役なし・Codex を使わない。
// 境目はどちらも medium。実装のステップの with には codex_review が無い（Codex はレビューのステップへ移した）。
func TestEmbeddedSelfReviewWithDefaults(t *testing.T) {
	want := map[string]map[string]string{
		"ticket": {"reviewers": "claude-harness:code-reviewer,claude-harness:design-reviewer", "verifier": "claude-harness:finding-verifier",
			"fix_threshold": "medium", "codex_review": "on", "base": "$steps.resolve.base", "critical_design": "$steps.analyze.critical_design"},
		"ticket-light": {"reviewers": "claude-harness:code-reviewer,claude-harness:design-reviewer", "verifier": "none",
			"fix_threshold": "medium", "codex_review": "off", "base": "$steps.resolve.base"},
	}
	for id, w := range want {
		wf := parseEmbedded(t, id)
		got := map[string]string{}
		for _, b := range wf.Step("self-review").With {
			if b.Value.Ref != nil {
				got[b.Name] = b.Value.Ref.Raw
			} else if s, ok := b.Value.Literal.(string); ok {
				got[b.Name] = s
			}
		}
		for k, v := range w {
			if got[k] != v {
				t.Errorf("%s self-review with %s = %q, want %q", id, k, got[k], v)
			}
		}
		if _, ok := got["critical_design"]; ok && id == "ticket-light" {
			t.Errorf("ticket-light self-review with has critical_design")
		}
		for _, b := range wf.Step("implement").With {
			if b.Name == "codex_review" {
				t.Errorf("%s implement with still has codex_review", id)
			}
		}
	}
	// 戻る回数の上限: 標準 3・簡略 1。
	for id, n := range map[string]float64{"ticket": 3, "ticket-light": 1} {
		var got float64
		for _, l := range parseEmbedded(t, id).Limits {
			if l.Name == "self_review" {
				got = l.Value
			}
		}
		if got != n {
			t.Errorf("%s limits.self_review = %v, want %v", id, got, n)
		}
	}
}

// レビューのステップ化の遷移: 実装（と直し）からレビューへ、指摘ありは直すステップへ（上限で commit）、なしは commit、
// 行えなかったは止める。直すステップはレビューへ戻る。self-review-route は無い。facts.self_review はレビューのステップから出す。
func TestEmbeddedSelfReviewTransitions(t *testing.T) {
	for _, id := range []string{"ticket", "ticket-light"} {
		wf := parseEmbedded(t, id)
		if wf.Step("self-review-route") != nil {
			t.Errorf("%s still has self-review-route", id)
		}
		on := func(step, outcome string) *workflow.Transition {
			for _, e := range wf.Step(step).On {
				if e.Outcome == outcome {
					return e.T
				}
			}
			t.Errorf("%s: %s has no on.%s", id, step, outcome)
			return &workflow.Transition{}
		}
		for _, c := range [][3]string{
			{"implement", "pass", "self-review"}, {"implement", "skip", "self-review"},
			{"self-review", "clean", "commit"}, {"self-review-fix", "pass", "self-review"}, {"fix", "pass", "self-review"},
		} {
			if got := on(c[0], c[1]).Target; got != c[2] {
				t.Errorf("%s: %s.%s -> %q, want %q", id, c[0], c[1], got, c[2])
			}
		}
		for _, e := range wf.Step("implement").On {
			if e.Outcome == "review_incomplete" {
				t.Errorf("%s: implement still has on.review_incomplete", id)
			}
		}
		f := on("self-review", "findings")
		if f.Kind != workflow.TGoto || f.Target != "self-review-fix" || f.Limit != "self_review" || f.Exhausted == nil || f.Exhausted.Kind != workflow.TStep || f.Exhausted.Target != "commit" {
			t.Errorf("%s: self-review.findings = %s", id, transitionText(f))
		}
		if inc := on("self-review", "incomplete"); inc.Kind != workflow.TFail || inc.Reason != "self_review_incomplete" {
			t.Errorf("%s: self-review.incomplete = %s", id, transitionText(inc))
		}
		if len(wf.Facts) != 1 || wf.Facts[0].Name != "self_review" || len(wf.Facts[0].Refs) != 1 || wf.Facts[0].Refs[0].Raw != "$steps.self-review.self_review" {
			t.Errorf("%s: facts = %+v", id, wf.Facts)
		}
		var pub []string
		for _, b := range wf.Step("publish").With {
			if b.Name == "residual_findings" || b.Name == "review_unverified" {
				pub = append(pub, b.Name+"="+b.Value.Ref.Raw)
			}
		}
		if got := strings.Join(pub, ","); got != "residual_findings=$steps.self-review.residual_findings,review_unverified=$steps.self-review.unverified" {
			t.Errorf("%s: publish with = %s", id, got)
		}
	}
}

// レビューのステップの出力の型: outcome は 3 つ、codex_review は必須で 5 つ、指摘の一覧はファイル・行・重さ・主張・根拠を持つ。
// 実装の出力の型は self_review・codex_review・residual_findings を持たない。
func TestSelfReviewResultSchema(t *testing.T) {
	type prop struct {
		Type  string   `json:"type"`
		Enum  []string `json:"enum"`
		Items struct {
			Required []string `json:"required"`
		} `json:"items"`
	}
	read := func(name string) (req []string, props map[string]prop) {
		data, err := fs.ReadFile(assets.FS, "workflows/schemas/"+name)
		if err != nil {
			t.Fatal(err)
		}
		var sc struct {
			Required   []string        `json:"required"`
			Properties map[string]prop `json:"properties"`
		}
		if err := json.Unmarshal(data, &sc); err != nil {
			t.Fatal(err)
		}
		return sc.Required, sc.Properties
	}
	req, props := read("self-review-result.json")
	for _, f := range []string{"outcome", "self_review", "codex_review", "findings", "residual_findings", "unverified"} {
		if !slices.Contains(req, f) {
			t.Errorf("self-review-result: %s is not required", f)
		}
	}
	if got := props["outcome"].Enum; !slices.Equal(got, []string{"findings", "clean", "incomplete"}) {
		t.Errorf("outcome enum = %v", got)
	}
	if got := props["self_review"].Enum; !slices.Equal(got, []string{"complete", "incomplete"}) {
		t.Errorf("self_review enum = %v", got)
	}
	if got := props["codex_review"].Enum; !slices.Equal(got, []string{"off", "complete", "partial", "failed", "skipped"}) {
		t.Errorf("codex_review enum = %v", got)
	}
	for _, f := range []string{"file", "line", "severity", "claim", "evidence"} {
		if !slices.Contains(props["findings"].Items.Required, f) {
			t.Errorf("findings items lack %s", f)
		}
	}
	_, impl := read("implement-result.json")
	for _, f := range []string{"self_review", "codex_review", "residual_findings"} {
		if _, ok := impl[f]; ok {
			t.Errorf("implement-result still has %s", f)
		}
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

// レビューのステップの指示文（ticket-self-review.md）: レビュアーと疑う役を Task ツールで並べて呼び、Codex のレビューを並べ、直さずに返す。
func TestSelfReviewPromptInstructsReviewersAndCodex(t *testing.T) {
	p := readPrompt(t, "ticket-self-review.md")
	cases := [][]string{
		{"`inputs.reviewers`", "Task ツール", "同時に起動"},
		{"`{findings: [{file, line, severity, claim, evidence}]}`"},
		{"`inputs.verifier`", "`none`", "反証"},
		{"`inputs.fix_threshold`", "以上", "直すべき指摘"},
		{"`inputs.codex_review` が `on`", "裏で起動"},
		{"claude-harness-run codex-review-runner", "裏で起動"},
		{"終わるまで", "返さない"},
		{"`[Codex] `", "claim"},
		{"完了しなかった", "`unverified`"},
		{"使えないことだけでは止め"},
		{"claude-harness-run cleanup-review-diff", "成否に関わらず"},
		{"`codex_review`", "`off`"},
		{"`findings`", "`clean`", "`incomplete`"},
		{"`residual_findings`", "全件"},
		{"直さない"},
	}
	for _, terms := range cases {
		if !hasSentenceWith(p, terms...) {
			t.Errorf("no single sentence of ticket-self-review.md has all of %q", terms)
		}
	}
	for _, term := range []string{"claude-harness-run collect-review-diff", "codex-review-context.XXXXXX"} {
		if !strings.Contains(p, term) {
			t.Errorf("ticket-self-review.md lacks %q", term)
		}
	}
}

// 実装・直し・レビュー対応・直すステップの指示文は、Codex を起動する語を一切持たない（Codex はレビューのステップだけが起動する）。
// 直すステップの指示文は、渡された指摘だけを直して必須ゲートを通す。
func TestOtherPromptsDoNotRunCodexReview(t *testing.T) {
	for _, name := range []string{"ticket-implement.md", "ticket-light-implement.md", "ticket-fix.md", "ticket-light-fix.md", "ticket-respond.md", "ticket-self-review-fix.md", "ticket-light-self-review-fix.md"} {
		p := readPrompt(t, name)
		for _, w := range []string{"codex-review-runner", "collect-review-diff"} {
			if strings.Contains(p, w) {
				t.Errorf("%s mentions %s", name, w)
			}
		}
		if !strings.Contains(p, "レビューのステップ") {
			t.Errorf("%s does not say that the review step reviews", name)
		}
	}
	for _, name := range []string{"ticket-self-review-fix.md", "ticket-light-self-review-fix.md"} {
		p := readPrompt(t, name)
		if !hasSentenceWith(p, "`inputs.findings`") || !hasSentenceWith(p, "渡された指摘だけを直して") || !hasSentenceWith(p, "`/quality-check`", "必須ゲート") {
			t.Errorf("%s does not instruct to fix only the given findings and pass the gate", name)
		}
	}
	// 標準のフローの実装のステップは、クリティカル設計の逸脱の確かめ（Step e-3）を残す。
	if !hasSentenceWith(readPrompt(t, "ticket-implement.md"), "Step e-3", "行う") {
		t.Errorf("ticket-implement.md does not keep Step e-3")
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

// AC-133: 埋め込んだ ticket・ticket-light・reflect のどのステップも tools を持たない。
func TestEmbeddedFlowsDeclareNoTools(t *testing.T) {
	l, err := assets.Ensure(assets.FS, filepath.Join(t.TempDir(), "runtime", "test"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ticket", "ticket-light", "reflect"} {
		wf, err := workflow.Load(filepath.Join(l.WorkflowDir, id+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if len(wf.Steps) == 0 {
			t.Fatalf("%s has no steps", id)
		}
		for _, s := range wf.Steps {
			if s.Tools != nil {
				t.Errorf("%s: step %s has tools %v", id, s.ID, s.Tools)
			}
		}
	}
}
