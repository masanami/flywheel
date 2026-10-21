package cli

import (
	"encoding/json"
	"testing"
)

// 仕様 §S4「ストアと JSON の形」の冒頭の注記: J2・J5 の段階の欄の書き込みを、偽の claude の
// 子プロセスの経路（CLI → core → ストア → show）で 1 本ずつ通す（J1 は classify_auto_test.go）。

func claudeStructured(t *testing.T, out map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"is_error": false, "structured_output": out})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// J2 の issues: 論点が課題の stages.issues に書かれ、人間対応待ちになる。
func TestPlanAuto_J2IssuesWritesStageIssuesViaChildProcess(t *testing.T) {
	ws := setupJ2Workspace(t)
	id := newClassifiedForCase(t, ws)
	issue := func(q string) map[string]any {
		return map[string]any{"question": q, "recommended": 2, "options": []any{
			map[string]any{"label": "甲-" + q, "example": nil}, map[string]any{"label": "乙-" + q, "example": "例"}}}
	}
	putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: claudeStructured(t, map[string]any{
		"verdict": "issues", "issues": []any{issue("QA"), issue("QB"), issue("QC")},
	})})

	item := phaseOf(t, runJSON(t, ws, "plan", "--auto", id))["items"].([]any)[0].(map[string]any)
	if item["outcome"] != "issues" || item["status"] != "awaiting_human" {
		t.Fatalf("item = %v", item)
	}
	show := runJSON(t, ws, "show", id)
	issues, _ := show["stages"].(map[string]any)["issues"].([]any)
	if len(issues) != 3 || issues[0].(map[string]any)["question"] != "QA" || issues[2].(map[string]any)["question"] != "QC" {
		t.Errorf("stages.issues = %v", issues)
	}
	if len(show["plans"].([]any)) != 0 || len(show["holds"].([]any)) != 1 {
		t.Errorf("plans/holds = %v / %v", show["plans"], show["holds"])
	}
}

// J2 の plan: 予想・やらないことが課題の stages に書かれ、計画が登録される。
func TestPlanAuto_J2PlanWritesExpectedAndNonGoalsViaChildProcess(t *testing.T) {
	ws := setupJ2Workspace(t)
	id := newClassifiedForCase(t, ws)
	stdout, _ := j2PlanFixture(t, func(m map[string]any) {
		m["expected"] = []any{map[string]any{"kind": "cli", "scene": "SCENE-CLI", "before": "前", "after": "後"}}
		m["non_goals"] = []any{"NONGOAL-CLI"}
	})
	putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: stdout})

	item := phaseOf(t, runJSON(t, ws, "plan", "--auto", id))["items"].([]any)[0].(map[string]any)
	if item["outcome"] != "plan" || item["status"] != "awaiting_plan_approval" {
		t.Fatalf("item = %v", item)
	}
	show := runJSON(t, ws, "show", id)
	st := show["stages"].(map[string]any)
	exp, _ := st["expected"].([]any)
	if len(exp) != 1 || exp[0].(map[string]any)["scene"] != "SCENE-CLI" || exp[0].(map[string]any)["after"] != "後" {
		t.Errorf("stages.expected = %v", st["expected"])
	}
	ng, _ := st["non_goals"].([]any)
	if len(ng) != 1 || ng[0] != "NONGOAL-CLI" {
		t.Errorf("stages.non_goals = %v", st["non_goals"])
	}
	if len(show["plans"].([]any)) != 1 {
		t.Errorf("plans = %v", show["plans"])
	}
}

// J5 の actual: 実物が課題の stages.actual に書かれる。
func TestVerifyAuto_J5WritesActualViaChildProcess(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	id := newVerifyingForCase(t, ws, false)
	route := j5Route("met", nil, nil)
	route.Stdout = `{"session_id":"s","is_error":false,"total_cost_usd":0.4,"structured_output":` +
		`{"verdict":"met","reason":"R","feedback":null,"question":null,"actual":[{"scene":"ACT-SCENE-CLI","after":"ACT-AFTER-CLI"}]}}`
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{route}, "")

	runJSON(t, ws, "verify", "--auto", id)

	st := runJSON(t, ws, "show", id)["stages"].(map[string]any)
	act, _ := st["actual"].([]any)
	if len(act) != 1 || act[0].(map[string]any)["scene"] != "ACT-SCENE-CLI" || act[0].(map[string]any)["after"] != "ACT-AFTER-CLI" {
		t.Errorf("stages.actual = %v", st["actual"])
	}
}
