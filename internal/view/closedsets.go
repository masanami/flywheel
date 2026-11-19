package view

import "github.com/masanami/flywheel/internal/core"

// ClosedSet は UI が文言を持つ閉集合 1 つ（TypeScript 側の定数名と、コードの一覧）。
type ClosedSet struct {
	Name  string
	Codes []string
}

// ClosedSets は core の閉集合（課題カードの操作・修飾・状態・食い違いの種類、関係グラフの区分・依存の種類・登録の経路、保留の種類、タスクの表示の区分）を
// 生成物（web/src/api/types.gen.ts）へ書き出すための一覧。順序は core の定義の順。
// 例外として CONDITIONS_AFTER_RELEASE_HEADINGS は view が持つ承認の見出しで、添字は ConditionsOutlook の値。
func ClosedSets() []ClosedSet {
	var actions, modifiers, states, kinds, categories, edgeKinds, registeredVia, holdKinds, phases []string
	for _, v := range core.CardActionValues() {
		actions = append(actions, string(v))
	}
	for _, v := range core.CardModifierValues() {
		modifiers = append(modifiers, string(v))
	}
	for _, e := range core.StatusVocabulary {
		states = append(states, string(e.Code))
	}
	for _, v := range core.DiscrepancyKindValues() {
		kinds = append(kinds, string(v))
	}
	for _, v := range core.GraphCategoryValues() {
		categories = append(categories, string(v))
	}
	for _, v := range core.GraphEdgeKindValues() {
		edgeKinds = append(edgeKinds, string(v))
	}
	for _, v := range core.GraphRegisteredViaValues() {
		registeredVia = append(registeredVia, string(v))
	}
	for _, v := range core.HoldKindValues() {
		holdKinds = append(holdKinds, string(v))
	}
	for _, v := range core.TaskPhaseValues() {
		phases = append(phases, string(v))
	}
	// 完了の承認の要約の見出し。添字は ConditionsOutlook の値（OutlookNotReleasing, OutlookReleasing, OutlookApprovedRelease の順）。
	headings := make([]string, 0, 3)
	for _, o := range []ConditionsOutlook{OutlookNotReleasing, OutlookReleasing, OutlookApprovedRelease} {
		headings = append(headings, ConditionsAfterReleaseHeading(o))
	}
	return []ClosedSet{
		{Name: "CARD_ACTION_CODES", Codes: actions},
		{Name: "CARD_MODIFIER_CODES", Codes: modifiers},
		{Name: "STATUS_CODES", Codes: states},
		{Name: "DISCREPANCY_KIND_CODES", Codes: kinds},
		{Name: "GRAPH_CATEGORY_CODES", Codes: categories},
		{Name: "GRAPH_EDGE_KIND_CODES", Codes: edgeKinds},
		{Name: "GRAPH_REGISTERED_VIA_CODES", Codes: registeredVia},
		{Name: "HOLD_KIND_CODES", Codes: holdKinds},
		{Name: "TASK_PHASE_CODES", Codes: phases},
		{Name: "CONDITIONS_AFTER_RELEASE_HEADINGS", Codes: headings},
	}
}
