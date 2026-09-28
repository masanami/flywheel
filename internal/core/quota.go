package core

import "strings"

// このファイルは #81（親要件チケット #77・docs/features/m3-invoker-delegation.md
// §枠（レート制限）超過・決定M3P12）が持つ、枠超過の判定規則を持つ。
//
// self-review 指摘（design-reviewer, round1）: M3P12 の決定文言「現行
// quota-check.sh の規則を core へ移し」と、§機能全体の設計「invoker は…
// 結果のJSONをcoreの型（結果・費用・session_id・構造化出力・枠超過の
// "判定材料"）へ正規化するだけで、規則を持たない」を踏まえ、判定規則
// （IsRateLimited）の定義と移植テストは internal/invoker ではなく
// internal/core に置く（規則は core、入出力は invoker、の原則どおり）。
// internal/invoker（Launcher.InvokeJudgment）は core をimportできるため、
// 抽出した result（自由記述）を core.IsRateLimited へそのまま渡して呼ぶ。

// rateLimitPrefix は枠超過の先頭一致に使う判定文字列（正本。末尾の半角
// スペースまでが判定対象で、これが枠名との境界を作る）。移植元:
// claude-flywheel a5dfd43 の scripts/quota-check.sh の PREFIX（§枠超過・
// M3P12「現行quota-check.shの規則をcoreへ移し、テストを移植する」）。
const rateLimitPrefix = "You've hit your "

// IsRateLimited は result（子の返り値の自由記述）が枠超過を示すかを判定する
// （§枠超過の判定規則。移植元 quota-check.sh の3規則をそのまま実装する）:
//
//  1. 先頭一致であって部分一致ではない。前後の空白（改行・タブを含む）を
//     除いた文字列全体の先頭が rateLimitPrefix に一致することだけを見る
//     （2行目以降の行頭・文中の言及には一致しない）
//  2. 枠の名前を限定しない（前置き部分までで判定する）
//  3. 判定不能（空・空白のみ）なら「枠超過ではない」側へ倒す
func IsRateLimited(result string) bool {
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return false
	}
	return strings.HasPrefix(trimmed, rateLimitPrefix)
}
