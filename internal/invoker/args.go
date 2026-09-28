package invoker

import (
	"strconv"

	"github.com/masanami/flywheel/internal/core"
)

// buildArgs は判断の呼び出しの引数を組み立てる（§invoker の共通の規則・
// §判断点の共通の規則）:
//
//   - "-p"・"--session-id <UUID>"・"--output-format json"・
//     "--json-schema <出力スキーマ>"・"--max-budget-usd <額>"
//   - 読み取り専用の道具だけを許す指定（§クリティカル設計決定 2・M3P24）:
//     "--allowedTools Read Grep Glob"（この3つだけ）・
//     "--disallowedTools Bash Edit Write NotebookEdit WebFetch WebSearch Task"・
//     "--permission-mode default"
//
// 外部由来の文字列（課題の本文等）は一切引数に現れない（標準入力で渡す。
// stdin.go）。core が決めた唯一の in.SessionID を使う（新規セッションでも
// --resume の再開でも、core が「どのセッションIDを使うか」を一意に決め、
// invoker は問い合わせない。self-review 指摘round1: 以前はSessionIDと
// ResumeSessionIDの2値を受け取り、invoker側が「どちらを使うか」を判断して
// いたため、coreが記録するsession_idと実際にclaudeへ渡すIDが食い違い
// うる構造だった）。
//
// in.IsResume が false（新規セッション）なら "--session-id <UUID>"、
// true（再開）なら "--resume <UUID>" を渡す（【仮定】: 仕様書は一貫して
// 再開の手段を「--resume」と呼んでいる〈§invoker の共通の規則「以後の
// --resumeは返り値の値を使う」・§受入基準「同じsession_idを--resumeした
// runの費用は…」〉ため、このフラグ名を採用する。本チケットの受入基準
// AC-28〜40は新規セッションの6引数だけを固定しており、この--resumeの
// 起動そのものは本物のclaudeで検証していない。返却の「未検証事項」に
// 明記する）。
func buildArgs(in core.JudgmentLaunchInput) []string {
	sessionFlag := "--session-id"
	if in.IsResume {
		sessionFlag = "--resume"
	}
	return []string{
		"-p",
		sessionFlag, in.SessionID,
		"--output-format", "json",
		"--json-schema", string(in.OutputSchema),
		"--max-budget-usd", formatUSDArg(in.MaxBudgetUSD),
		"--allowedTools", "Read", "Grep", "Glob",
		"--disallowedTools", "Bash", "Edit", "Write", "NotebookEdit", "WebFetch", "WebSearch", "Task",
		"--permission-mode", "default",
	}
}

// formatUSDArg は金額を --max-budget-usd に渡す最小表記の10進文字列にする
// （末尾のゼロを持たない。例: 1 → "1"、0.5 → "0.5"）。
func formatUSDArg(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
