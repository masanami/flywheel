package view

import "github.com/masanami/flywheel/internal/core"

// ClosedSet は UI が文言を持つ閉集合 1 つ（TypeScript 側の定数名と、コードの一覧）。
type ClosedSet struct {
	Name  string
	Codes []string
}

// ClosedSets は core の閉集合（課題カードの操作・修飾・状態・食い違いの種類、保留の種類、タスクの表示の区分）を
// 生成物（web/src/api/types.gen.ts）へ書き出すための一覧。順序は core の定義の順。
// 例外として CONDITIONS_AFTER_RELEASE_HEADINGS は view が持つ承認の見出しで、添字は ConditionsOutlook の値。
// EXECUTION_RESULT_WORDS も view が持つ語で、添字は EXECUTION_RESULT_CODES と同じ（i 番目の値の語）。
func ClosedSets() []ClosedSet {
	var actions, modifiers, states, kinds, holdKinds, phases, execResults, execLabels []string
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
	for _, v := range core.HoldKindValues() {
		holdKinds = append(holdKinds, string(v))
	}
	for _, v := range core.TaskPhaseValues() {
		phases = append(phases, string(v))
	}
	for _, v := range core.ExecutionResultValues() {
		execResults = append(execResults, string(v))
		execLabels = append(execLabels, executionResultLabels[v])
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
		{Name: "HOLD_KIND_CODES", Codes: holdKinds},
		{Name: "TASK_PHASE_CODES", Codes: phases},
		{Name: "EXECUTION_RESULT_CODES", Codes: execResults},
		{Name: "EXECUTION_RESULT_WORDS", Codes: execLabels},
		{Name: "CONDITIONS_AFTER_RELEASE_HEADINGS", Codes: headings},
	}
}

// executionResultLabels は反映の試行の結果の語の正本（端末の文の語）。生成物の EXECUTION_RESULT_WORDS へ書き出し、
// 画面の表（web/src/labels.ts の EXECUTION_RESULT_LABELS）はそれを読む。
var executionResultLabels = map[core.ExecutionResult]string{
	core.ExecutionResultMerged:           "反映先へ入れた",
	core.ExecutionResultAlreadyMerged:    "すでに反映先へ入っていた",
	core.ExecutionResultWaitingChecks:    "チェックの完了を待っている",
	core.ExecutionResultUpstreamFailed:   "上流を読めなかった",
	core.ExecutionResultChecksFailed:     "チェックが失敗した",
	core.ExecutionResultNotMergeable:     "反映先へ入れられない",
	core.ExecutionResultMergeFailed:      "反映先へ入れるのに失敗した",
	core.ExecutionResultMergeUnconfirmed: "反映先へ入ったか分からない",
	core.ExecutionResultHeadChanged:      "承認した後に中身が変わった",
	core.ExecutionResultClosed:           "成果物が、入らずに閉じられた",
	core.ExecutionResultBaseChanged:      "成果物の宛先が、宣言の反映先でない",
	core.ExecutionResultNoApprovedHead:   "承認した中身の記録が無い",
	core.ExecutionResultUnsupported:      "flywheel が扱えない形だった",
	core.ExecutionResultMergedUnapproved: "承認なしで反映された",
	core.ExecutionResultLimitReached:     "失敗が連続の回数の上限に達した",
	core.ExecutionResultInterrupted:      "試行が途中で止まった",
	core.ExecutionResultLanded:           "委譲先が反映先へ入れた",
	core.ExecutionResultLandSent:         "委譲先へ「入れてよい」を返した",
	core.ExecutionResultLandUnconfirmed:  "「入れてよい」が届いたか分からない",
	core.ExecutionResultLandRefused:      "委譲先が入れられなかった",
	core.ExecutionResultContentMismatch:  "委譲先の中身と、上流の中身が食い違っている",
	core.ExecutionResultReflectStarted:   "反映の依頼を始めた",
	core.ExecutionResultReflectFailed:    "反映の依頼を始められなかった",
	core.ExecutionResultWaitingDelegate:  "委譲先が進めている",
	core.ExecutionResultWaitingOutside:   "flywheel の外での反映を待っている",
	core.ExecutionResultDelegateEnded:    "委譲先の実行が、入れずに終わった",
	core.ExecutionResultWaitingFixCheck:  "衝突の解消の確かめを待っている",
}

// ExecutionResultLabel は反映の試行の結果の語を返す。閉集合の外には空文字。
func ExecutionResultLabel(r core.ExecutionResult) string {
	return executionResultLabels[r]
}
