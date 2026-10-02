package invoker

// このファイルは委譲の起動（親要件チケット #98 §J3 ブリーフと委譲の起動・決定 M3H4・
// M3P5・M3P46）を持つ。core.DelegationInvoker の実装であり、規則（意思決定者の判定・
// スロットの割り当て・予算の評価）は持たない。固定の節は embed した雛形をそのまま差し込み、
// ワークスペースからは読まない。

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

// disallowedFlywheelTool は委譲の子へ必ず付ける、flywheel コマンドの Bash での実行を拒否する
// 指定（M3P46。前方一致のため、絶対パス・`cd <dir> && flywheel`・`env`・`sh -c` などで
// すり抜けうる残余リスクは M3H12 で受け入れ済み）。
const disallowedFlywheelTool = "Bash(flywheel:*)"

// briefRegionBegin・briefRegionEnd は、J3 の出力（課題に固有の依頼）を囲む区切りの行。
// 外部由来のデータの区画ではなく、委譲先が従う依頼の本体を示す。
const (
	briefRegionBegin = "----- BEGIN BRIEF -----"
	briefRegionEnd   = "----- END BRIEF -----"
)

// invocationHeading は形態 plugin の操作の invocation を示す見出し。
const invocationHeading = "# 実行するスキル\n\n次の呼び出しを実行する。\n\n"

// DelegationBrief は BuildDelegationStdin の入力。
type DelegationBrief struct {
	// Decider・DeciderRow は意思決定者と該当した行（最初の節に埋め込みの雛形の文面で示す）。
	Decider    string
	DeciderRow int
	// Brief は J3 の出力（固定の節の間の区画にだけ現れる）。
	Brief string
	// SourceIssueNumber・SourceIssueURL は取り込み元の Issue（無ければ 0・空）。
	SourceIssueNumber int
	SourceIssueURL    string
	// Invocation は差し込みを埋めた invocation（形態 brief・無しは空）。
	Invocation string
}

// BuildDelegationStdin は委譲の標準入力を組み立てる。順序は、意思決定者（雛形＋値）・
// 意思決定者ごとの規律・（形態 plugin のとき）実行するスキル・J3 の出力の区画・
// 最終報告の様式・禁止する操作と代替手段・報告の形。固定の節は埋め込みの雛形のまま
// 差し込み、LLM に書かせない。
func BuildDelegationStdin(in DelegationBrief) ([]byte, error) {
	fixed, err := BriefFixedSections()
	if err != nil {
		return nil, err
	}
	body := map[string]string{}
	for _, f := range fixed {
		body[f.ID] = f.Body
	}
	for _, s := range briefSectionOrder {
		if _, ok := body[s.ID]; !ok {
			return nil, fmt.Errorf("invoker: missing brief fixed section %s", s.ID)
		}
	}
	if strings.Contains(in.Brief, briefRegionBegin) || strings.Contains(in.Brief, briefRegionEnd) {
		return nil, fmt.Errorf("invoker: the brief contains a region delimiter line")
	}
	var b strings.Builder
	writeSection := func(text string) {
		b.WriteString(text)
		if !strings.HasSuffix(text, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	writeSection(body["decider"] + fmt.Sprintf("意思決定者: %s\n該当した行: %d\n", in.Decider, in.DeciderRow))
	writeSection(body["decider_discipline"])
	if in.Invocation != "" {
		writeSection(invocationHeading + in.Invocation)
	}
	if in.SourceIssueNumber > 0 {
		writeSection(fmt.Sprintf("# 取り込み元の Issue\n\n番号: %d\nURL: %s", in.SourceIssueNumber, in.SourceIssueURL))
	}
	b.WriteString(briefRegionBegin + "\n")
	b.WriteString(in.Brief)
	if !strings.HasSuffix(in.Brief, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(briefRegionEnd + "\n\n")
	writeSection(body["completion_report_style"])
	writeSection(body["forbidden_operations"])
	writeSection(body["delegation_output_shape"])
	return []byte(b.String()), nil
}

// buildDelegateArgs は委譲の引数を組み立てる: `-p`・`--session-id <UUID>`・
// `--output-format json`・`--json-schema <報告のスキーマ>`・`--max-budget-usd <額>`・
// `--permission-mode <宣言の値>`・`--disallowedTools Bash(flywheel:*)`。外部由来の文字列は
// 引数に現れない（標準入力で渡す）。
func buildDelegateArgs(in core.DelegateLaunchInput) []string {
	return []string{
		"-p",
		"--session-id", in.SessionID,
		"--output-format", "json",
		"--json-schema", string(in.OutputSchema),
		"--max-budget-usd", formatUSDArg(in.MaxBudgetUSD),
		"--permission-mode", string(in.PermissionMode),
		"--disallowedTools", disallowedFlywheelTool,
	}
}

// InvokeDelegation は core.DelegationInvoker の実装本体。スロットの作業ツリーを作業
// ディレクトリとして claude を起動して待ち、応答を core.JudgmentLaunchOutput へ正規化する。
// 戻り値の error は invoker 自身の予期しない失敗だけに使う（claude の起動失敗・時間切れ・
// 異常終了は Result で表す）。
func (l *Launcher) InvokeDelegation(ctx context.Context, in core.DelegateLaunchInput) (core.JudgmentLaunchOutput, error) {
	claudePath, lookErr := exec.LookPath("claude")
	if lookErr != nil {
		return core.JudgmentLaunchOutput{Result: core.RunResultLaunchFailed, ErrorSummary: lookErr.Error()}, nil
	}
	timeout := time.Duration(in.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = defaultInvokeTimeout * time.Second
	}
	if err := os.MkdirAll(in.RunDir, 0o755); err != nil {
		return core.JudgmentLaunchOutput{
			Result:       core.RunResultLaunchFailed,
			ErrorSummary: fmt.Sprintf("invoker: create run dir: %v", err),
		}, nil
	}
	stdin, err := BuildDelegationStdin(DelegationBrief{
		Decider: string(in.Decider), DeciderRow: in.DeciderRow, Brief: in.Brief, Invocation: in.Invocation,
		SourceIssueNumber: in.SourceIssueNumber, SourceIssueURL: in.SourceIssueURL,
	})
	if err != nil {
		return core.JudgmentLaunchOutput{
			Result:       core.RunResultLaunchFailed,
			ErrorSummary: fmt.Sprintf("invoker: build stdin: %v", err),
		}, nil
	}
	return finishClaudeRun(ctx, claudePath, in.WorkDir, in.Workspace, in.RunDir, buildDelegateArgs(in), stdin, timeout), nil
}
