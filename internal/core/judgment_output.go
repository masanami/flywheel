package core

// このファイルは docs/features/m3-invoker-delegation.md §IF / API
// 「判断点の出力（--json-schema に渡す形の要点）」・§J3「報告の問いの種類」が
// 定める、判断点 J1〜J5 の出力と委譲の報告の閉集合の値を Go の定義として置く
// （#82・親要件 #77）。
//
// このファイルはあくまで閉集合の値の正本である。JSON スキーマ
// （`--json-schema` に渡す実際のスキーマ文字列）の組み立ては本チケットの
// 範囲外で、後続チケット（J1: #84、J2: #85、J3〜J5: S2・S3）がこの定義から
// 機械的に生成する単一の正本として本ファイルを使う（手で書き写さない）。
//
// 既存の閉集合（enums.go の VerifyResult 等）と文字列値が重複していても、
// 判断点の出力の閉集合としてここに独立して列挙する（判断点の出力は
// core.judgment.go の RunJudgmentResult とは別の関心事である）。この方針は
// docs/features/m3-invoker-delegation.md 本体にも Issue #82 の本文・
// コメントにも明文化されていない、本チケットの実装時の判断である（self-review
// 指摘round2, design-reviewer CONFIRMED: round1の修正でIssue #82の決定済みの
// 設計と引用したが、Issue #82の本文・コメントを確認してもその文言は無く、
// 後から読む人が出典を確かめられなかった。出典を「Issue」と偽らず、実装時の
// 判断であると明記する形へ改めた）。優先度（Priority）はこの方針に沿って
// 既存の型を再利用する側を選んだ（重複を許容してまで別型を起こす理由が
// 無いため）。この判断は返却の「仕様への指摘」にも記載する。

// J1Verdict は J1（分類）の判定の閉集合。
type J1Verdict string

// J1Verdict の3値。
const (
	J1VerdictMine      J1Verdict = "mine"
	J1VerdictNotMine   J1Verdict = "not_mine"
	J1VerdictUncertain J1Verdict = "uncertain"
)

// J1VerdictValues は J1Verdict の全値（§IF / API の記載順）。
var J1VerdictValues = []J1Verdict{J1VerdictMine, J1VerdictNotMine, J1VerdictUncertain}

// J2Verdict は J2（計画）の判定の閉集合。
type J2Verdict string

// J2Verdict の2値。
const (
	J2VerdictPlan      J2Verdict = "plan"
	J2VerdictUncertain J2Verdict = "uncertain"
)

// J2VerdictValues は J2Verdict の全値。
var J2VerdictValues = []J2Verdict{J2VerdictPlan, J2VerdictUncertain}

// JudgmentSize は J1・J2 が出すサイズ案の閉集合（S|M|L。
// .flywheel/agent.json の size_budgets_usd のキーと同じ3文字だが、後者は
// 設定のキー、前者は判断点の出力値であり関心事が異なる）。
type JudgmentSize string

// JudgmentSize の3値。
const (
	JudgmentSizeS JudgmentSize = "S"
	JudgmentSizeM JudgmentSize = "M"
	JudgmentSizeL JudgmentSize = "L"
)

// JudgmentSizeValues は JudgmentSize の全値。
var JudgmentSizeValues = []JudgmentSize{JudgmentSizeS, JudgmentSizeM, JudgmentSizeL}

// J4Decision は J4（子の問いへの応答）の、問いごとの判定の閉集合。
type J4Decision string

// J4Decision の2値。
const (
	J4DecisionAnswer   J4Decision = "answer"
	J4DecisionEscalate J4Decision = "escalate"
)

// J4DecisionValues は J4Decision の全値。
var J4DecisionValues = []J4Decision{J4DecisionAnswer, J4DecisionEscalate}

// J5Verdict は J5（検証）の判定の閉集合。
type J5Verdict string

// J5Verdict の3値。
const (
	J5VerdictMet       J5Verdict = "met"
	J5VerdictNotMet    J5Verdict = "not_met"
	J5VerdictUncertain J5Verdict = "uncertain"
)

// J5VerdictValues は J5Verdict の全値。
var J5VerdictValues = []J5Verdict{J5VerdictMet, J5VerdictNotMet, J5VerdictUncertain}

// DelegationOutcome は委譲の報告の結末（outcome）の閉集合。
type DelegationOutcome string

// DelegationOutcome の3値。
const (
	DelegationOutcomeCompleted DelegationOutcome = "completed"
	DelegationOutcomeQuestions DelegationOutcome = "questions"
	DelegationOutcomeBlocked   DelegationOutcome = "blocked"
)

// DelegationOutcomeValues は DelegationOutcome の全値。
var DelegationOutcomeValues = []DelegationOutcome{
	DelegationOutcomeCompleted, DelegationOutcomeQuestions, DelegationOutcomeBlocked,
}

// QuestionKind は委譲の報告の問いの種類（kind）のうち、宣言
// （.flywheel/connectors.json の human_question_kinds）に依らない組み込みの
// 閉集合8値（§J3「問いの種類…の閉集合は§J3のとおり」・§IF / API
// 「宣言の人間へ上げる問いの種類のidとの和集合」）。宣言側の動的な id は
// この型に含めない（宣言の検証は internal/core/connectors_declaration.go の
// 範囲）。
type QuestionKind string

// QuestionKind の組み込み8値。
const (
	QuestionKindRequirements       QuestionKind = "requirements"
	QuestionKindCriticalDesign     QuestionKind = "critical_design"
	QuestionKindAcceptanceCriteria QuestionKind = "acceptance_criteria"
	QuestionKindSafetyTradeoff     QuestionKind = "safety_tradeoff"
	QuestionKindCrossRepo          QuestionKind = "cross_repo"
	QuestionKindScope              QuestionKind = "scope"
	QuestionKindProductPolicy      QuestionKind = "product_policy"
	QuestionKindMinor              QuestionKind = "minor"
)

// QuestionKindValues は QuestionKind の組み込み8値（§J3 の記載順）。
var QuestionKindValues = []QuestionKind{
	QuestionKindRequirements,
	QuestionKindCriticalDesign,
	QuestionKindAcceptanceCriteria,
	QuestionKindSafetyTradeoff,
	QuestionKindCrossRepo,
	QuestionKindScope,
	QuestionKindProductPolicy,
	QuestionKindMinor,
}

// AllJudgmentOutputClosedValues は、判断点 J1〜J5 の出力と委譲の報告が持つ
// 閉集合の値をすべて1つの一覧に合成して返す（重複を含みうる。例:
// JudgmentSize の "S" と .flywheel/agent.json のキー "S" は別の関心事だが
// 文字としては同じ）。
//
// J1 の出力が持つ優先度（`priority`。§IF / API「J1 | {"verdict": …,
// "priority": "P0|P1|P2"|null, …}」）も判断点の出力スキーマの閉集合の値の
// 1つであり、ここに含める（既存の Priority 型〈enums.go〉を再利用する。
// self-review 指摘: 当初 J1 の優先度がここから漏れており、指示文に
// P0/P1/P2 が現れても禁止語のテストが検出できなかった＝AC「指示文に判断点の
// 出力スキーマの閉集合の値が現れると、禁止語のテストが失敗する」の一部が
// 未達だった）。
//
// internal/cli が本関数の戻り値を internal/invoker.ForbiddenSources へ渡し、
// 指示文の禁止語の生成元にする（§指示文の歯止め「指示文に判断点の出力
// スキーマの閉集合の値が現れると、禁止語のテストが失敗する」「禁止の一覧は
// …出力スキーマのコードの定義から生成される」）。この関数は禁止語の生成のためだけの
// ものではなく、判断点の出力の閉集合の値の単一の正本として、後続チケット
// （J1〜J5・JSONスキーマの組み立て）からも参照されうる。
func AllJudgmentOutputClosedValues() []string {
	var vals []string
	for _, v := range priorityValues {
		vals = append(vals, string(v))
	}
	for _, v := range J1VerdictValues {
		vals = append(vals, string(v))
	}
	for _, v := range J2VerdictValues {
		vals = append(vals, string(v))
	}
	for _, v := range JudgmentSizeValues {
		vals = append(vals, string(v))
	}
	for _, v := range J4DecisionValues {
		vals = append(vals, string(v))
	}
	for _, v := range J5VerdictValues {
		vals = append(vals, string(v))
	}
	for _, v := range DelegationOutcomeValues {
		vals = append(vals, string(v))
	}
	for _, v := range QuestionKindValues {
		vals = append(vals, string(v))
	}
	return vals
}
