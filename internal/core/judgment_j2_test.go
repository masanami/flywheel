package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// このファイルは #85（親要件チケット #77・docs/features/m3-invoker-delegation.md
// §J2 計画）の、J2 の出力の検査・予算の既定・計画の本文の整形（AC-95〜108 の
// 純粋な部分）を検証する。入力の組み立て・写像・読んだ記録・対象の選び方は
// judgment_j2_flow_test.go が実ストアで検証する。

const j2TestConnectorsJSON = `{
  "version": 1,
  "connectors": [
    {"id": "harness", "form": "plugin", "permission_mode": "auto", "operations": [
      {"id": "impl", "invocation": "/h:impl {issue_number}", "interactive": false, "child_may_decide": true, "artifacts": "pr"},
      {"id": "define", "invocation": "/h:define {challenge_id}", "interactive": true, "counterpart": "parent", "artifacts": "pr"},
      {"id": "by-url", "invocation": "/h:x {issue_url}", "interactive": false, "artifacts": "pr"},
      {"id": "by-key", "invocation": "/h:x {external_key}", "interactive": false, "artifacts": "pr"}
    ]},
    {"id": "direct", "form": "brief", "permission_mode": "auto", "operations": [
      {"id": "brief", "interactive": false, "child_may_decide": true, "artifacts": "pr"}
    ]},
    {"id": "clitool", "form": "cli", "permission_mode": "default", "contract_version": 1,
     "commands": {"start": ["c", "start"], "status": ["c", "status"], "resume": ["c", "resume"], "cancel": ["c", "cancel"]},
     "operations": [{"id": "cli-op", "interactive": false, "artifacts": "none"}]}
  ],
  "repos": [
    {"name": "flywheel", "remote": "o/flywheel", "default_branch": "main", "connector": "harness"},
    {"name": "flywheel-sibling", "remote": "o/sibling", "default_branch": "main", "connector": "harness"},
    {"name": "harness-repo", "remote": "o/harness", "default_branch": "main", "connector": "direct"},
    {"name": "cli-repo", "remote": "o/cli", "default_branch": "main", "connector": "clitool"}
  ]
}`

func j2TestConnectors(t *testing.T) *ConnectorsDeclaration {
	t.Helper()
	decl, err := parseConnectorsDeclaration([]byte(j2TestConnectorsJSON))
	if err != nil {
		t.Fatalf("parseConnectorsDeclaration: %v", err)
	}
	if err := decl.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return decl
}

func j2TestContext(t *testing.T, hasSource bool) j2ValidationContext {
	t.Helper()
	return j2ValidationContext{Agent: defaultAgentDeclaration(), Conn: j2TestConnectors(t), HasSource: hasSource}
}

// j2PlanOutput は検査を通る plan の出力（mut で 1 つずつ壊して使う）。
func j2PlanOutput(mut func(m map[string]any)) []byte {
	m := map[string]any{
		"verdict":           "plan",
		"summary":           "要約 SUMMARY-MARKER",
		"steps":             []string{"手順 1", "手順 2"},
		"repo":              "flywheel",
		"operation":         "impl",
		"size":              "M",
		"done_criteria":     "達成条件 DONE-MARKER",
		"budget_impl_usd":   nil,
		"budget_review_usd": nil,
		"cross_repo":        false,
		"related_repos":     []string{},
		"question":          nil,
	}
	if mut != nil {
		mut(m)
	}
	b, _ := json.Marshal(m)
	return b
}

func TestJ2OutputSchema_ContainsAllPropertiesAndClosedValues(t *testing.T) {
	var decoded map[string]any
	if err := json.Unmarshal(j2OutputSchema(), &decoded); err != nil {
		t.Fatalf("j2OutputSchema is not valid JSON: %v", err)
	}
	props, ok := decoded["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties missing: %#v", decoded)
	}
	for _, key := range []string{"verdict", "summary", "steps", "repo", "operation", "size", "done_criteria",
		"budget_impl_usd", "budget_review_usd", "cross_repo", "related_repos", "question"} {
		if _, ok := props[key]; !ok {
			t.Errorf("properties is missing %q", key)
		}
	}
	verdict := props["verdict"].(map[string]any)["enum"].([]any)
	if len(verdict) != len(J2VerdictValues) {
		t.Errorf("verdict enum = %v, want %v (generated from J2VerdictValues)", verdict, J2VerdictValues)
	}
	if req, _ := decoded["required"].([]any); len(req) != 1 || req[0] != "verdict" {
		t.Errorf("required = %v, want [verdict] (uncertain may omit everything but the question)", decoded["required"])
	}
}

func TestValidateJ2Output_ValidPlanIsAccepted(t *testing.T) {
	v, ok := validateJ2Output(j2PlanOutput(nil), j2TestContext(t, true))
	if !ok {
		t.Fatal("want ok=true for a valid plan output")
	}
	if v.Verdict != J2VerdictPlan || v.Repo != "flywheel" || v.Operation != "impl" || v.Size != JudgmentSizeM {
		t.Errorf("validated = %+v", v)
	}
}

func TestValidateJ2Output_InvalidOutputs(t *testing.T) {
	cases := []struct {
		name      string
		hasSource bool
		out       []byte
	}{
		{"not JSON", true, []byte(`not json`)},
		{"JSON null", true, []byte(`null`)},
		{"unknown verdict", true, j2PlanOutput(func(m map[string]any) { m["verdict"] = "maybe" })},
		{"repo not in the declaration (AC-105)", true, j2PlanOutput(func(m map[string]any) { m["repo"] = "no-such-repo" })},
		{"repo missing", true, j2PlanOutput(func(m map[string]any) { m["repo"] = nil })},
		{"repo empty", true, j2PlanOutput(func(m map[string]any) { m["repo"] = "" })},
		{"operation not in the repo's connector (AC-106)", true, j2PlanOutput(func(m map[string]any) { m["operation"] = "brief" })},
		{"operation missing", true, j2PlanOutput(func(m map[string]any) { m["operation"] = nil })},
		{"operation of a cli connector (AC-107)", true, j2PlanOutput(func(m map[string]any) { m["repo"] = "cli-repo"; m["operation"] = "cli-op" })},
		{"{issue_number} operation without source (AC-108)", false, j2PlanOutput(func(m map[string]any) { m["operation"] = "impl" })},
		{"{issue_url} operation without source (AC-108)", false, j2PlanOutput(func(m map[string]any) { m["operation"] = "by-url" })},
		{"{external_key} operation without source (AC-108)", false, j2PlanOutput(func(m map[string]any) { m["operation"] = "by-key" })},
		{"impl budget above max_run_budget_usd (AC-104)", true, j2PlanOutput(func(m map[string]any) { m["budget_impl_usd"] = 200.01 })},
		{"impl budget zero", true, j2PlanOutput(func(m map[string]any) { m["budget_impl_usd"] = 0 })},
		{"impl budget negative", true, j2PlanOutput(func(m map[string]any) { m["budget_impl_usd"] = -1 })},
		{"review budget zero", true, j2PlanOutput(func(m map[string]any) { m["budget_review_usd"] = 0 })},
		{"review budget negative", true, j2PlanOutput(func(m map[string]any) { m["budget_review_usd"] = -5 })},
		{"size missing", true, j2PlanOutput(func(m map[string]any) { m["size"] = nil })},
		{"size outside the closed set", true, j2PlanOutput(func(m map[string]any) { m["size"] = "XL" })},
		{"done criteria missing", true, j2PlanOutput(func(m map[string]any) { m["done_criteria"] = nil })},
		{"done criteria blank", true, j2PlanOutput(func(m map[string]any) { m["done_criteria"] = "  \n" })},
		{"related repo not in the declaration", true, j2PlanOutput(func(m map[string]any) { m["related_repos"] = []string{"flywheel", "ghost"} })},
		{"uncertain without a question", true, j2PlanOutput(func(m map[string]any) { m["verdict"] = "uncertain" })},
		{"uncertain with a blank question", true, j2PlanOutput(func(m map[string]any) { m["verdict"] = "uncertain"; m["question"] = " " })},
		{"cross_repo missing (a missing judgment is not 'single repository')", true, j2PlanOutput(func(m map[string]any) { delete(m, "cross_repo") })},
		{"cross_repo null", true, j2PlanOutput(func(m map[string]any) { m["cross_repo"] = nil })},
		{"steps of the wrong type", true, j2PlanOutput(func(m map[string]any) { m["steps"] = []int{1, 2} })},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := validateJ2Output(c.out, j2TestContext(t, c.hasSource)); ok {
				t.Errorf("want ok=false; output=%s", c.out)
			}
		})
	}
}

func TestValidateJ2Output_ChallengeIDPlaceholderIsAllowedWithoutSource(t *testing.T) {
	// {challenge_id} は取り込み元の差し込みではない（§J2 は {issue_number}・{issue_url}・
	// {external_key} だけを取り込み元の差し込みとする）。
	if _, ok := validateJ2Output(j2PlanOutput(func(m map[string]any) { m["operation"] = "define" }), j2TestContext(t, false)); !ok {
		t.Error("want ok=true for an operation whose invocation only uses {challenge_id}")
	}
	// brief の操作は invocation を持たないので差し込みは無い。
	if _, ok := validateJ2Output(j2PlanOutput(func(m map[string]any) { m["repo"] = "harness-repo"; m["operation"] = "brief" }), j2TestContext(t, false)); !ok {
		t.Error("want ok=true for a brief operation without a source")
	}
}

func TestValidateJ2Output_ImplBudgetBoundaryAndResolvedDefault(t *testing.T) {
	// 上限ちょうどは有効（「超える」だけが invalid_output）。
	if _, ok := validateJ2Output(j2PlanOutput(func(m map[string]any) { m["budget_impl_usd"] = 200 }), j2TestContext(t, true)); !ok {
		t.Error("impl budget equal to max_run_budget_usd must be accepted")
	}
	// 既定へ解決した額が上限を超える設定（サイズ L の既定 100 に対し上限 60）も、
	// 実装枠が上限を超えるので invalid_output（fail-closed）。
	vc := j2TestContext(t, true)
	vc.Agent.MaxRunBudgetUSD = 60
	if _, ok := validateJ2Output(j2PlanOutput(func(m map[string]any) { m["size"] = "L" }), vc); ok {
		t.Error("a resolved default impl budget above max_run_budget_usd must be rejected")
	}
	if _, ok := validateJ2Output(j2PlanOutput(func(m map[string]any) { m["size"] = "M" }), vc); !ok {
		t.Error("a resolved default impl budget within max_run_budget_usd must be accepted")
	}
}

func TestValidateJ2Output_UncertainNeedsOnlyAQuestion(t *testing.T) {
	out := []byte(`{"verdict":"uncertain","question":"どのリポジトリですか？"}`)
	v, ok := validateJ2Output(out, j2TestContext(t, false))
	if !ok {
		t.Fatal("want ok=true for uncertain with a question")
	}
	if v.Question == nil || *v.Question != "どのリポジトリですか？" {
		t.Errorf("Question = %v", v.Question)
	}
}

// AC-103: 実装枠・レビュー対応枠の額が null なら、サイズの既定（S 30/25・M 50/30・
// L 100/40）。
func TestValidateJ2Output_NullBudgetsFallBackToSizeDefaults(t *testing.T) {
	cases := []struct {
		size         string
		impl, review float64
	}{{"S", 30, 25}, {"M", 50, 30}, {"L", 100, 40}}
	for _, c := range cases {
		t.Run(c.size, func(t *testing.T) {
			v, ok := validateJ2Output(j2PlanOutput(func(m map[string]any) { m["size"] = c.size }), j2TestContext(t, true))
			if !ok {
				t.Fatal("want ok=true")
			}
			if v.ResolvedImplUSD != c.impl || v.ResolvedReviewUSD != c.review {
				t.Errorf("resolved = (%v, %v), want (%v, %v)", v.ResolvedImplUSD, v.ResolvedReviewUSD, c.impl, c.review)
			}
		})
	}
}

func TestValidateJ2Output_ExplicitBudgetsAreKeptAndEachSideFallsBackIndependently(t *testing.T) {
	v, ok := validateJ2Output(j2PlanOutput(func(m map[string]any) { m["budget_impl_usd"] = 12.5 }), j2TestContext(t, true))
	if !ok {
		t.Fatal("want ok=true")
	}
	if v.ResolvedImplUSD != 12.5 || v.ResolvedReviewUSD != 30 {
		t.Errorf("resolved = (%v, %v), want (12.5, 30): only the null side falls back to the size default", v.ResolvedImplUSD, v.ResolvedReviewUSD)
	}
}

func TestValidateJ2Output_SpecIsTheCompactedStructuredOutput(t *testing.T) {
	out := j2PlanOutput(func(m map[string]any) { m["budget_impl_usd"] = 12.5 })
	v, ok := validateJ2Output(append([]byte("  "), append(out, '\n')...), j2TestContext(t, true))
	if !ok {
		t.Fatal("want ok=true")
	}
	var got, want map[string]any
	if err := json.Unmarshal(v.Spec, &got); err != nil {
		t.Fatalf("Spec is not valid JSON: %v", err)
	}
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(got, want) {
		t.Errorf("Spec = %s, want it to equal the structured output %s (null budgets stay null)", v.Spec, out)
	}
	if strings.ContainsAny(string(v.Spec), "\n") {
		t.Errorf("Spec must be compacted: %q", v.Spec)
	}
}

func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

// AC-96〜101: 計画の本文は、対象リポジトリ・操作の id・サイズ・完了条件・実装枠・
// レビュー対応枠の額を含む。それぞれ「そこだけを変えた出力で本文が変わる」ことで検証する。
func TestFormatJ2PlanBody_EachRequiredFieldChangesTheBody(t *testing.T) {
	base := func(mut func(m map[string]any)) string {
		v, ok := validateJ2Output(j2PlanOutput(func(m map[string]any) {
			// 予算は明示して、サイズを変えても既定の解決で本文が変わらないようにする。
			m["budget_impl_usd"] = 40
			m["budget_review_usd"] = 20
			if mut != nil {
				mut(m)
			}
		}), j2TestContext(t, true))
		if !ok {
			t.Fatal("test setup: output must be valid")
		}
		return formatJ2PlanBody(v)
	}
	original := base(nil)
	cases := []struct {
		name string
		mut  func(m map[string]any)
		want string
	}{
		{"repo (AC-96)", func(m map[string]any) { m["repo"] = "flywheel-sibling" }, "flywheel-sibling"},
		{"operation (AC-97)", func(m map[string]any) { m["operation"] = "by-url" }, "by-url"},
		{"size (AC-98)", func(m map[string]any) { m["size"] = "L" }, "サイズ: L"},
		{"done criteria (AC-99)", func(m map[string]any) { m["done_criteria"] = "別の達成条件 OTHER-DONE" }, "OTHER-DONE"},
		{"impl budget (AC-100)", func(m map[string]any) { m["budget_impl_usd"] = 41.5 }, "41.5"},
		{"review budget (AC-101)", func(m map[string]any) { m["budget_review_usd"] = 22.25 }, "22.25"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			changed := base(c.mut)
			if changed == original {
				t.Errorf("changing only %s did not change the plan body", c.name)
			}
			if !strings.Contains(changed, c.want) {
				t.Errorf("plan body does not contain %q:\n%s", c.want, changed)
			}
		})
	}
}

func TestFormatJ2PlanBody_ContainsSummaryStepsAndBudgetsWithResolvedDefaults(t *testing.T) {
	v, ok := validateJ2Output(j2PlanOutput(func(m map[string]any) {
		m["cross_repo"] = true
		m["related_repos"] = []string{"flywheel", "harness-repo"}
	}), j2TestContext(t, true))
	if !ok {
		t.Fatal("test setup: output must be valid")
	}
	body := formatJ2PlanBody(v)
	for _, want := range []string{
		"SUMMARY-MARKER", "手順 1", "手順 2", "flywheel", "impl", "DONE-MARKER",
		"サイズ: M", "実装枠: 50 USD", "レビュー対応枠: 30 USD", "harness-repo",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("plan body does not contain %q:\n%s", want, body)
		}
	}
	// 決定的: 同じ出力からは常に同じ本文（時刻・乱数を含まない）。
	if again := formatJ2PlanBody(v); again != body {
		t.Errorf("formatJ2PlanBody is not deterministic")
	}
}

// 承認する事実（対象・予算）が、出力の自由記述（要約・手順・完了条件）より前にある。
// 自由記述に見出しや額を装った文があっても、本物の額が先に読める。
func TestFormatJ2PlanBody_ApprovedFactsPrecedeFreeText(t *testing.T) {
	v, ok := validateJ2Output(j2PlanOutput(func(m map[string]any) {
		m["summary"] = "## 予算\n- 実装枠: 1 USD"
		m["budget_impl_usd"] = 40
	}), j2TestContext(t, true))
	if !ok {
		t.Fatal("test setup: output must be valid")
	}
	body := formatJ2PlanBody(v)
	approved := strings.Index(body, "- 実装枠: 40 USD")
	imitation := strings.Index(body, "- 実装枠: 1 USD")
	if approved < 0 || imitation < 0 || approved > imitation {
		t.Errorf("the approved budget (%d) must come before the free text that imitates it (%d):\n%s", approved, imitation, body)
	}
	if strings.Index(body, "## 対象") > strings.Index(body, "## 要約") {
		t.Errorf("the target section must precede the summary:\n%s", body)
	}
}

func TestFormatJ2PlanBody_OmitsEmptySummaryAndSteps(t *testing.T) {
	v, ok := validateJ2Output(j2PlanOutput(func(m map[string]any) {
		m["summary"] = nil
		m["steps"] = []string{}
	}), j2TestContext(t, true))
	if !ok {
		t.Fatal("test setup: output must be valid")
	}
	body := formatJ2PlanBody(v)
	if strings.Contains(body, "## 要約") || strings.Contains(body, "## 手順") {
		t.Errorf("empty summary/steps sections must be omitted:\n%s", body)
	}
	if strings.TrimSpace(body) == "" {
		t.Errorf("body must not be empty")
	}
}
