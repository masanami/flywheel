// Package core は flywheel の状態機械（状態語彙・遷移表 T1〜T15）と
// 閉集合の定義を持つ正本パッケージである（Issue #8）。
//
// 遷移表はデータとして持ち、コード中の分岐に散らさない
// （親要件チケット #4「機能全体の設計」の決定）。承認の成立条件・作業ログへの
// 記録・ストアの操作は後続チケット（#9・#10）が担い、本パッケージには持たない。
package core

import "strings"

// Status は状態語彙のコード（ストア・JSON 出力で使う安定した英字の識別子）。
// 生成は必ず StatusVocabulary の定数、または ParseStatus を経由する。
type Status string

// 状態語彙8値のコード定数。順序は docs/features/m1-core.md §状態機械 の表と一致する。
const (
	StatusUnclassified               Status = "unclassified"
	StatusClassified                 Status = "classified"
	StatusAwaitingPlanApproval       Status = "awaiting_plan_approval"
	StatusInProgress                 Status = "in_progress"
	StatusVerifying                  Status = "verifying"
	StatusAwaitingCompletionApproval Status = "awaiting_completion_approval"
	StatusDone                       Status = "done"
	StatusAwaitingHuman              Status = "awaiting_human"
)

// StatusVocabEntry は状態語彙の1行（コードと表示名の対）。
type StatusVocabEntry struct {
	Code  Status
	Label string
}

// StatusVocabulary は状態語彙8値の正本。docs/features/m1-core.md §状態機械 の表と
// 1対1に対応する（internal/core/status_test.go の
// TestStatusVocabulary_MatchesSpecCodesAndLabels が照合する）。
var StatusVocabulary = []StatusVocabEntry{
	{Code: StatusUnclassified, Label: "未分類"},
	{Code: StatusClassified, Label: "分類済"},
	{Code: StatusAwaitingPlanApproval, Label: "計画承認待ち"},
	{Code: StatusInProgress, Label: "着手中"},
	{Code: StatusVerifying, Label: "検証中"},
	{Code: StatusAwaitingCompletionApproval, Label: "完了確認待ち"},
	{Code: StatusDone, Label: "完了"},
	{Code: StatusAwaitingHuman, Label: "人間対応待ち"},
}

// ParseStatus は s を状態として解釈する。前後の空白を除いた後、語彙のコードか
// 表示名のいずれかと完全一致したときだけ ok=true を返す。部分一致・大文字小文字の
// 違い・内部の空白違いは解釈しない（現行 validate-artifact のテストが固定する
// 振る舞いの引き継ぎ。docs/features/m1-core.md 「現行から引き継ぐ振る舞い」）。
//
// #9 の `list --status` はこの関数を経由してユーザー入力を解釈する。
func ParseStatus(s string) (Status, bool) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return "", false
	}
	for _, e := range StatusVocabulary {
		if string(e.Code) == trimmed || e.Label == trimmed {
			return e.Code, true
		}
	}
	return "", false
}

// Label は状態コードに対応する表示名を返す。語彙に無いコードでは ok=false。
func (s Status) Label() (string, bool) {
	for _, e := range StatusVocabulary {
		if e.Code == s {
			return e.Label, true
		}
	}
	return "", false
}
