package core

import "sort"

// AgentDeclarationKeys は .flywheel/agent.json の全キー名
// （トップレベル＋ネストした葉のキー）をソート済みで返す。
// agent_declaration.go の非公開マップ（agentTopLevelKeys・
// sizeBudgetsSizeKeys・sizeBudgetPairKeys・judgmentBudgetKeys・
// agentTimeoutSecKeys）から生成し、手で書き写さない
// （#82・docs/features/m3-invoker-delegation.md §指示文の歯止め
// 「上の禁止の一覧は、コードの定義（…設定のキー…）から生成する
// （一覧を手で写さない。語彙を足せば検査も追従する）」）。
//
// internal/cli が本関数の戻り値を internal/invoker.ForbiddenSources へ渡し、
// 指示文の禁止語の生成元にする。
func AgentDeclarationKeys() []string {
	var keys []string
	for k := range agentTopLevelKeys {
		keys = append(keys, k)
	}
	for k := range sizeBudgetsSizeKeys {
		keys = append(keys, k)
	}
	for k := range sizeBudgetPairKeys {
		keys = append(keys, k)
	}
	for k := range judgmentBudgetKeys {
		keys = append(keys, k)
	}
	for k := range agentTimeoutSecKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
