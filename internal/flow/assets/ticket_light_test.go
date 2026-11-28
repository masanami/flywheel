package assets_test

import (
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/flow/assets"
	"github.com/masanami/flywheel/internal/flow/workflow"
)

// AC-1: 埋め込んだ ticket-light の定義は、定義の検査を誤りなしで通る。
func TestEmbeddedTicketLightPassesValidation(t *testing.T) { validateEmbedded(t, "ticket-light") }

func stepIDs(wf *workflow.Workflow) []string {
	var ids []string
	for _, s := range wf.Steps {
		ids = append(ids, s.ID)
	}
	slices.Sort(ids)
	return ids
}

func bindingText(bs []*workflow.Binding) string {
	var parts []string
	for _, b := range bs {
		v := fmt.Sprintf("%#v", b.Value.Literal)
		if b.Value.Ref != nil {
			v = b.Value.Ref.Raw
		}
		parts = append(parts, b.Name+"="+v)
	}
	return strings.Join(parts, ",")
}

func transitionText(tr *workflow.Transition) string {
	if tr == nil {
		return "<nil>"
	}
	s := fmt.Sprintf("%s|%s|%s|limit=%s|retry=%d|note=%v|interrupted=%s|with=%s", tr.Kind, tr.Target, tr.Reason, tr.Limit, tr.Retry, tr.NoteOptional, tr.Interrupted, bindingText(tr.With))
	if tr.Detail != nil {
		s += "|detail=" + tr.Detail.Raw
	}
	if tr.Exhausted != nil {
		s += "|exhausted=" + transitionText(tr.Exhausted)
	}
	return s
}

// stepText はステップの種類・遷移・with など（model・effort を除く）を 1 つの文字列にする。
func stepText(s *workflow.Step) string {
	var on []string
	for _, e := range s.On {
		on = append(on, e.Outcome+"=>"+transitionText(e.T))
	}
	exit := ""
	for _, e := range s.Exit {
		exit += fmt.Sprintf("%v;", *e)
	}
	return strings.Join([]string{
		"kind=" + s.Kind, "run=" + s.Run, "output=" + s.Output, "outcome_field=" + s.OutcomeField, "timeout=" + s.Timeout.String(),
		"prompt=" + s.Prompt, "agent=" + s.Agent, "addDirs=" + bindingText(s.AddDirs), "session=" + s.Session + ":" + s.SessionFrom,
		fmt.Sprintf("budget=%v", s.BudgetUSD), "gate=" + s.GateType + "/" + s.Decider + "/" + s.RequestedAction + "/" + strings.Join(s.GateInputs, ",") + "/" + s.Observe,
		"contract=" + s.ContractKind, "refresh=" + s.Refresh, "exit=" + exit,
		"with=" + bindingText(s.With), "on=" + strings.Join(on, "\n"),
	}, "\n")
}

// AC-2: ticket-light の識別子の集合は、ticket の集合から analyze・e2e-route・e2e を除き、branch を足したもの。
func TestTicketLightStepIDsAreTicketMinusAnalysisAndE2EPlusBranch(t *testing.T) {
	var want []string
	for _, id := range stepIDs(parseEmbedded(t, "ticket")) {
		if id != "analyze" && id != "e2e-route" && id != "e2e" {
			want = append(want, id)
		}
	}
	want = append(want, "branch")
	slices.Sort(want)
	if got := stepIDs(parseEmbedded(t, "ticket-light")); !slices.Equal(got, want) {
		t.Errorf("ticket-light step ids = %v, want %v", got, want)
	}
}

// AC-3・AC-10: 簡略なフローの定義の表に無いステップは、種類・遷移・with（model・effort を除く）が ticket と一致する。
// 表にあるのは次の 8 つだけで、ほかは 1 つも違わない（respond の指示文は ticket と同じ ticket-respond.md。self-review の指示文も ticket と同じで、with だけが違う）。
func TestTicketLightStepsOutsideTheTableMatchTicket(t *testing.T) {
	ticket, light := parseEmbedded(t, "ticket"), parseEmbedded(t, "ticket-light")
	inTable := map[string]bool{"integration-route": true, "integration": true, "workspace": true, "implement": true, "fix": true, "sync": true, "self-review": true, "self-review-fix": true}
	for _, ls := range light.Steps {
		ts := ticket.Step(ls.ID)
		if ts == nil {
			continue // branch
		}
		same := stepText(ts) == stepText(ls)
		if inTable[ls.ID] && same {
			t.Errorf("step %s is in the table of differences but equals ticket's", ls.ID)
		}
		if !inTable[ls.ID] && !same {
			t.Errorf("step %s is not in the table of differences but differs from ticket:\n--- ticket\n%s\n--- ticket-light\n%s", ls.ID, stepText(ts), stepText(ls))
		}
	}
	if got := light.Step("respond").Prompt; got != "prompts/ticket-respond.md" {
		t.Errorf("respond prompt = %q", got)
	}
	if got := light.Step("self-review").Prompt; got != ticket.Step("self-review").Prompt {
		t.Errorf("self-review prompt = %q, want ticket's %q", got, ticket.Step("self-review").Prompt)
	}
}

// 表の差分（遷移・with）そのもの。
func TestTicketLightTableDifferences(t *testing.T) {
	light := parseEmbedded(t, "ticket-light")
	target := func(step, outcome string) string {
		for _, e := range light.Step(step).On {
			if e.Outcome == outcome {
				return e.T.Target
			}
		}
		t.Errorf("%s has no on.%s", step, outcome)
		return ""
	}
	for _, c := range [][3]string{
		{"integration-route", "none", "branch"}, {"integration-route", "create", "integration"},
		{"integration", "created", "branch"}, {"integration", "existing", "branch"},
		{"branch", "ok", "workspace"}, {"sync", "current", "quality"}, {"sync", "synced", "quality"},
	} {
		if got := target(c[0], c[1]); got != c[2] {
			t.Errorf("%s.%s -> %q, want %q", c[0], c[1], got, c[2])
		}
	}
	var got string
	for _, b := range light.Step("workspace").With {
		if b.Name == "branch" && b.Value.Ref != nil {
			got = b.Value.Ref.Raw
		}
	}
	if got != "$steps.branch.branch" {
		t.Errorf("workspace with branch = %q, want $steps.branch.branch", got)
	}
}

// AC-4: 上限（limits）は、レビューから直すステップへ戻る回数（self_review。標準 3・簡略 1）を除いて ticket と一致する。facts と overridable の無さもここで確かめる。
func TestTicketLightLimitsAndFactsMatchTicketAndHaveNoOverridable(t *testing.T) {
	ticket, light := parseEmbedded(t, "ticket"), parseEmbedded(t, "ticket-light")
	text := func(w *workflow.Workflow) string {
		var parts []string
		for _, l := range w.Limits {
			if l.Name == "self_review" {
				continue // TestEmbeddedSelfReviewWithDefaults が値を確かめる
			}
			parts = append(parts, fmt.Sprintf("%s=%v", l.Name, l.Value))
		}
		for _, f := range w.Facts {
			var refs []string
			for _, r := range f.Refs {
				refs = append(refs, r.Raw)
			}
			parts = append(parts, f.Name+":"+strings.Join(refs, "|"))
		}
		return strings.Join(parts, "\n")
	}
	if text(ticket) != text(light) {
		t.Errorf("limits/facts differ:\n--- ticket\n%s\n--- ticket-light\n%s", text(ticket), text(light))
	}
	if len(light.Routes) != 0 || len(light.Skips) != 0 {
		t.Errorf("ticket-light has overridable: routes %v skips %v", light.Routes, light.Skips)
	}
}

// AC-5〜AC-9・AC-68: branch は command。implement・fix はエージェントを指定せず、指示文は ticket-light-*.md。implement の with に codex_review が無い。
func TestTicketLightStepsDefinition(t *testing.T) {
	light := parseEmbedded(t, "ticket-light")
	if k := light.Step("branch").Kind; k != "command" {
		t.Errorf("branch kind = %q", k)
	}
	if s := light.Step("implement"); s.Agent != "" || s.Prompt != "prompts/ticket-light-implement.md" {
		t.Errorf("implement agent %q prompt %q", s.Agent, s.Prompt)
	}
	if s := light.Step("fix"); s.Agent != "" || s.Prompt != "prompts/ticket-light-fix.md" {
		t.Errorf("fix agent %q prompt %q", s.Agent, s.Prompt)
	}
	if s := light.Step("self-review-fix"); s.Agent != "" || s.Prompt != "prompts/ticket-light-self-review-fix.md" {
		t.Errorf("self-review-fix agent %q prompt %q", s.Agent, s.Prompt)
	}
	for _, b := range light.Step("implement").With {
		if b.Name == "codex_review" || b.Name == "critical_design" {
			t.Errorf("implement with has %s", b.Name)
		}
	}
	for _, s := range light.Steps {
		if s.Agent != "" {
			t.Errorf("step %s specifies agent %q", s.ID, s.Agent)
		}
	}
}

// AC-109・AC-110: models は main・quick とも sonnet、efforts は main: medium・quick: low。
func TestTicketLightModelsAndEfforts(t *testing.T) {
	models, efforts := mainVars(t, parseEmbedded(t, "ticket-light"))
	if len(models) != 2 || models["main"] != "sonnet" || models["quick"] != "sonnet" {
		t.Errorf("models = %v", models)
	}
	if len(efforts) != 2 || efforts["main"] != "medium" || efforts["quick"] != "low" {
		t.Errorf("efforts = %v", efforts)
	}
}

// AC-111〜AC-115: llm のステップの model・effort の変数。commit・sync-resolve・land-sync-resolve は quick、ほかは main。
func TestTicketLightLLMStepsReferenceVariables(t *testing.T) {
	quick := map[string]bool{"commit": true, "sync-resolve": true, "land-sync-resolve": true}
	seen := 0
	for _, st := range parseEmbedded(t, "ticket-light").Steps {
		if st.Kind != "llm" {
			if st.Model != "" || st.Effort != "" {
				t.Errorf("%s is not an llm step but has model/effort", st.ID)
			}
			continue
		}
		seen++
		want := "main"
		if quick[st.ID] {
			want = "quick"
		}
		if st.Model != want || st.Effort != want {
			t.Errorf("%s model %q effort %q, want %q for both", st.ID, st.Model, st.Effort, want)
		}
	}
	// implement・self-review・self-review-fix・fix・commit・sync-resolve・land-sync-resolve・respond・merge
	if seen != 9 {
		t.Errorf("llm steps = %d, want 9", seen)
	}
}

// 偽の禁止語。簡略なフローの実装・直しの指示文は、否定の文にも書かない（レビューはレビューのステップが行う）。
var lightForbidden = []string{"/self-review", "finding-verifier", "design-deviation-verifier", "codex-review-runner", "feature-implementer",
	"claude-harness:code-reviewer", "claude-harness:design-reviewer", "self_review", "residual_findings"}

// 簡略なフローの実装・直し・直すステップの指示文は、レビュアーを呼ばず、セルフレビューの欄を返さない。
func TestTicketLightPromptsLeaveReviewToTheReviewStep(t *testing.T) {
	for _, name := range []string{"ticket-light-implement.md", "ticket-light-fix.md", "ticket-light-self-review-fix.md"} {
		p := readPrompt(t, name)
		for _, w := range lightForbidden {
			if strings.Contains(p, w) {
				t.Errorf("%s contains %q", name, w)
			}
		}
	}
	p := readPrompt(t, "ticket-light-implement.md")
	for _, terms := range [][]string{
		{"`/quality-check`", "通"},
		{"範囲を超える", "`deviation`", "`deviation_report`"},
		{"セルフレビューはこのステップでは行わない"},
	} {
		if !hasSentenceWith(p, terms...) {
			t.Errorf("no single sentence of ticket-light-implement.md has all of %q", terms)
		}
	}
}

// AC-37: ハッシュの一覧に、S2 で足した資材の行がある。
func TestSumsListTicketLightMaterials(t *testing.T) {
	sums := string(assets.Sums())
	for _, f := range []string{"workflows/ticket-light.yaml", "workflows/prompts/ticket-light-implement.md", "workflows/prompts/ticket-light-fix.md", "workflows/prompts/ticket-light-self-review-fix.md",
		"workflows/prompts/ticket-self-review.md", "workflows/prompts/ticket-self-review-fix.md", "workflows/schemas/self-review-result.json", "workflows/scripts/ticket-light-branch.sh", "workflows/schemas/ticket-light-branch.json"} {
		if !strings.Contains(sums, "  "+f+"\n") {
			t.Errorf("SHA256SUMS has no line for %s", f)
		}
		if _, err := fs.Stat(assets.FS, f); err != nil {
			t.Errorf("%s is not embedded: %v", f, err)
		}
	}
}

// ticket-light-branch.sh: 鍵から feature/<鍵>-light を返し、鍵の形が誤りなら非 0 で終わる。
func TestTicketLightBranchScript(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runtime", "test")
	l, err := assets.Ensure(assets.FS, root)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(l.WorkflowDir, "scripts", "ticket-light-branch.sh")
	for key, want := range map[string]string{"task-12": `{"outcome":"ok","branch":"feature/task-12-light"}`, "issue-7": `{"outcome":"ok","branch":"feature/issue-7-light"}`} {
		out, err := exec.Command(script, "--key", key).Output()
		if err != nil || strings.TrimSpace(string(out)) != want {
			t.Errorf("key %s: out %q err %v, want %s", key, out, err, want)
		}
	}
	for _, key := range []string{"", "task-", "feature/x", "task-1; rm -rf /", "task-0"} {
		if err := exec.Command(script, "--key", key).Run(); err == nil {
			t.Errorf("key %q was accepted", key)
		}
	}
}
