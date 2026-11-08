package core

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// 完了報告の branch 欄はブランチ名だけを書く欄。注記つきの値（C-404 の形）は、
// 出力スキーマの pattern と core の再検査の両方で拒む。

const c404Branch = "fix/issue-437-j6-evidence-id-pattern (flywheel; merged and cleaned up by the run)"

func TestDelegationReport_BranchFieldMustBeABranchNameOrNull(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := []struct {
		name   string
		branch *string
		wantOK bool
	}{
		{"null", nil, true},
		{"empty string", str(""), true},
		{"plain branch", str("feat/issue-475-x"), true},
		{"C-404 annotated branch", str(c404Branch), false},
		{"contains a space", str("a b"), false},
		{"double dots", str("x/../.."), false},
		{"leading dash", str("-rf"), false},
		{"trailing .lock", str("x.lock"), false},
		{"tilde", str("a~b"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := reportJSON(func(m map[string]any) {
				if c.branch == nil {
					m["branch"] = nil
				} else {
					m["branch"] = *c.branch
				}
			})
			_, ok := validateDelegationReport(out, nil)
			if ok != c.wantOK {
				t.Errorf("validateDelegationReport ok = %v, want %v", ok, c.wantOK)
			}
		})
	}
}

func TestDelegationReportSchema_BranchHasAPatternThatRejectsNotes(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(delegationReportSchema(nil), &schema); err != nil {
		t.Fatal(err)
	}
	branch := schema["properties"].(map[string]any)["branch"].(map[string]any)
	types, _ := branch["type"].([]any)
	if len(types) != 2 || types[0] != "string" || types[1] != "null" {
		t.Errorf("branch type = %v, want [string null]", branch["type"])
	}
	pattern, _ := branch["pattern"].(string)
	if pattern == "" {
		t.Fatal("branch has no pattern")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("pattern %q does not compile: %v", pattern, err)
	}
	for _, ok := range []string{"feat/issue-475-x", "fix/a.b_c-d", "main"} {
		if !re.MatchString(ok) {
			t.Errorf("pattern rejects the valid branch %q", ok)
		}
	}
	for _, bad := range []string{c404Branch, "a b", "a\tb", "a~b", "a^b", "a:b", "a?b", "a*b", "a[b", `a\b`} {
		if re.MatchString(bad) {
			t.Errorf("pattern accepts %q", bad)
		}
	}
}

// 注記つきの branch の報告は invalid_output になり、課題の状態は変わらず、不備の理由が
// run に残る。
func TestRunDelegation_AnnotatedBranchIsInvalidOutputWithAReason(t *testing.T) {
	f := newReconFixture(t, "impl")
	f.deleg.result.StructuredOutput = reportJSON(func(m map[string]any) { m["branch"] = c404Branch })
	res, err := f.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].Result != RunResultInvalidOutput {
		t.Fatalf("result = %s, want invalid_output", res.Items[0].Result)
	}
	if got := f.status(t); got != StatusInProgress {
		t.Errorf("status = %s, want in_progress", got)
	}
	runs := f.delegateRuns(t)
	if !strings.Contains(runs[0].Error, "branch") {
		t.Errorf("run error = %q, want a reason naming the branch field", runs[0].Error)
	}
}

// invalid_output の後の再開で、子は不備の理由を受け取り、報告を直せる。
func TestResume_AfterInvalidOutputCarriesTheReasonToTheChild(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.queue = []JudgmentLaunchOutput{{
		Result: RunResultSucceeded, ReportedTotalCostUSD: float64Ptr(2),
		StructuredOutput: reportJSON(func(m map[string]any) { m["branch"] = c404Branch }),
	}}
	f.runDelegation(t, id)
	first := f.lastLaunch(t)
	f.runDelegation(t, id)
	got := f.lastLaunch(t)
	if !got.IsResume || got.SessionID != first.SessionID || got.ResumeKind != ResumeKindInterrupted {
		t.Fatalf("launch = %+v, want an interrupted resume of %s", got, first.SessionID)
	}
	if !strings.Contains(got.ResumeProblem, "branch") {
		t.Errorf("ResumeProblem = %q, want the reason the previous report was rejected", got.ResumeProblem)
	}
}

// 失敗が invalid_output 以外の再開には、不備の理由を渡さない。
func TestResume_AfterOtherFailuresCarriesNoProblem(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.queue = []JudgmentLaunchOutput{failOut(RunResultErrored)}
	f.runDelegation(t, id)
	f.runDelegation(t, id)
	if got := f.lastLaunch(t); got.ResumeProblem != "" {
		t.Errorf("ResumeProblem = %q, want empty", got.ResumeProblem)
	}
}
