package core

import (
	"sort"
	"testing"
)

// TestAgentDeclarationKeys_ContainsAllTopLevelAndNestedKeys は
// AgentDeclarationKeys が .flywheel/agent.json のトップレベルのキーと、
// ネストした葉のキー（size_budgets_usd の S/M/L・そのimpl/review、
// judgment_budget_usd の J1〜J5、timeout_sec の judgment/delegate）を
// すべて含むソート済みの一覧を返すことを検証する
// （§指示文の歯止め「一覧を手で写さない。語彙を足せば検査も追従する」の
// 検証対象そのもの）。
func TestAgentDeclarationKeys_ContainsAllTopLevelAndNestedKeys(t *testing.T) {
	got := AgentDeclarationKeys()

	want := []string{
		// トップレベル（agentTopLevelKeys）
		"version", "position_file", "cycle_budget_usd",
		"size_budgets_usd", "max_run_budget_usd", "judgment_budget_usd",
		"timeout_sec", "max_parallel_runs", "rework_limit", "failure_limit", "conflict_prediction_budget_usd",
		// ネスト: size_budgets_usd の段（S/M/L）
		"S", "M", "L",
		// ネスト: size pair（impl/review）
		"impl", "review",
		// ネスト: judgment_budget_usd（J1〜J5）
		"J1", "J2", "J3", "J4", "J5",
		// ネスト: timeout_sec（judgment/delegate）
		"judgment", "delegate",
	}

	set := map[string]bool{}
	for _, k := range got {
		set[k] = true
	}
	for _, w := range want {
		if !set[w] {
			t.Errorf("AgentDeclarationKeys() is missing %q", w)
		}
	}

	if !sort.StringsAreSorted(got) {
		t.Errorf("AgentDeclarationKeys() is not sorted: %v", got)
	}
}

// TestAgentDeclarationKeys_NoDuplicates は返り値に重複が無いことを検証する
// （手で書き写していれば重複や漏れが起きやすいという設計上の懸念に対する
// 回帰。マップから生成する実装なら自然に満たされる）。
func TestAgentDeclarationKeys_NoDuplicates(t *testing.T) {
	got := AgentDeclarationKeys()
	seen := map[string]bool{}
	for _, k := range got {
		if seen[k] {
			t.Errorf("AgentDeclarationKeys() contains duplicate %q", k)
		}
		seen[k] = true
	}
}
