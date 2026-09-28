package invoker

// このファイルは #82（親要件 #77・docs/features/m3-invoker-delegation.md
// §指示文の歯止め）が定める、指示文とブリーフの固定の節の雛形に対する
// 禁止語の生成・照合を持つ。
//
// 生成（ForbiddenTerms）は入力を引数で受け取る純粋関数にし、コードの定義
// （状態の語彙・コマンドの一覧・設定のキー・出力スキーマの閉集合）から
// 禁止の一覧を組み立てる責務は呼び出し元（internal/cli。4つの生成元すべてを
// import できるのは cli だけ）に置く。internal/invoker はストア・core の
// 具体的な定義を知らない（import の向きは core と標準ライブラリだけ）。
//
// 照合（FindForbiddenTerms）は、実物の埋め込み内容の検査（禁止語を含まない
// ことの確認）と、フィクスチャに禁止語を差し込んで失敗することの確認の
// どちらにも同じ関数を使う（検査関数を2つ持たない）。

import (
	"regexp"
	"strings"
	"unicode"
)

// ForbiddenSources は禁止語の生成元（§指示文の歯止め「禁止の一覧は…コードの
// 定義から生成される」）。呼び出し元（internal/cli）が core・cli の定義から
// 集めて渡す。
type ForbiddenSources struct {
	// StateCodesAndLabels は状態語彙のコードと表示名
	// （core.StatusVocabulary の Code・Label の全値）。
	StateCodesAndLabels []string
	// Subcommands は flywheel のサブコマンド名
	// （internal/cli の defaultCommands() の各 Path のトークン）。
	Subcommands []string
	// ConfigKeys は .flywheel/agent.json のキー名
	// （core.AgentDeclarationKeys()）。
	ConfigKeys []string
	// OutputClosedValues は判断点の出力スキーマの閉集合の値
	// （委譲の報告の outcome・kind を含む。core.AllJudgmentOutputClosedValues()）。
	OutputClosedValues []string
}

// ForbiddenTerms は src の中身を1つの禁止語の一覧へ合成する（純粋関数）。
// 重複は除く。空文字列は無視する。
func ForbiddenTerms(src ForbiddenSources) []string {
	var all []string
	all = append(all, src.StateCodesAndLabels...)
	all = append(all, src.Subcommands...)
	all = append(all, src.ConfigKeys...)
	all = append(all, src.OutputClosedValues...)

	seen := make(map[string]bool, len(all))
	out := make([]string, 0, len(all))
	for _, term := range all {
		if term == "" || seen[term] {
			continue
		}
		seen[term] = true
		out = append(out, term)
	}
	return out
}

// moneyPattern は「$ か USD（大文字小文字無視）を伴う数」を検出する
// （§決定済みの設計。例: $5・$ 5・5 USD・5USD・USD 5・1.5 usd）。
var moneyPattern = regexp.MustCompile(`(?i)(\$\s*\d+(\.\d+)?)|(\d+(\.\d+)?\s*usd)|(usd\s*\d+(\.\d+)?)`)

// FindForbiddenTerms は content の中に terms のいずれか、または金額の表記が
// 現れるかを検査し、見つかった禁止語（金額はプレースホルダ文字列で表す）を
// 返す。何も見つからなければ空スライスを返す。
//
//   - ASCII の語（大文字小文字を区別）は、前後が [A-Za-z0-9_] でない位置での
//     一致だけを検出する（awaiting_plan_approval の中の plan は当たらない。
//     S単独・Sサイズは当たる）。大文字小文字の揺れ（例: "Plan"）は検出しない。
//     これは呼び出し元の決定済みの設計「ASCIIの語は大文字小文字を区別し」に
//     従った意図的な挙動である（self-review 指摘 round1, code-reviewer
//     PLAUSIBLE: 仕様の文言だけでは大文字小文字の扱いが読み取れないが、
//     呼び出し元のIssue #82ブリーフが明示している）。
//   - 非ASCII（表示名）は単純な部分文字列一致で検出する。この規則は状態の
//     表示名のような短い語（例: 2文字の「完了」）を、それを含む複合語
//     （「完了条件」「完了報告」等）ごと検出する副作用を持つ。これも呼び出し元の
//     決定済みの設計であり、本チケットでは緩めていない（self-review 指摘
//     round1, code-reviewer/design-reviewer 両方が PLAUSIBLE で指摘: J2 の
//     「完了条件」・J3 の「完了報告の様式」等、後続チケット〔#85・S2〕が
//     仕様書の用語をそのまま指示文へ書こうとすると必ずこの禁止語に当たる。
//     後続チケットは言い換え〔例: 「達成条件」「作業結果の様式」〕で対応する
//     ことを想定する。この副作用は本チケットの返却で「仕様への指摘」として
//     明記した）。
//   - 金額（$ か USD を伴う数）は terms に依らず常に検査する。全角の数字・
//     通貨記号（例: ＄５）は検出しない（§指示文の歯止めの例示が半角の書き方
//     だけであるため。self-review 指摘 round1, code-reviewer PLAUSIBLE）。
//     逆に、シェルの位置引数や正規表現の後方参照（`$1` 等。金額ではない）は
//     誤検出する（self-review 指摘 round2, code-reviewer CONFIRMED:
//     round1のコメントは全角非対応にしか触れておらず、この誤検出に
//     触れていなかった）。低頻度・低リスクと判断し、本チケットでは正規表現を
//     変えていない（後続チケットが指示文にシェルの例を書く場合は、このコメントを
//     手掛かりに言い換えるか、正規表現を見直す判断をする）。
func FindForbiddenTerms(content string, terms []string) []string {
	var hits []string
	for _, term := range terms {
		if term == "" {
			continue
		}
		if isASCIIOnly(term) {
			if asciiBoundaryMatch(content, term) {
				hits = append(hits, term)
			}
		} else if strings.Contains(content, term) {
			hits = append(hits, term)
		}
	}
	if moneyPattern.MatchString(content) {
		hits = append(hits, "<money amount>")
	}
	return hits
}

func isASCIIOnly(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII {
			return false
		}
	}
	return true
}

// asciiBoundaryMatch は term が content の中に、前後が単語構成文字
// （[A-Za-z0-9_]）でない位置に現れるかを判定する。
func asciiBoundaryMatch(content, term string) bool {
	pattern := `(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(term) + `($|[^A-Za-z0-9_])`
	re := regexp.MustCompile(pattern)
	return re.MatchString(content)
}
