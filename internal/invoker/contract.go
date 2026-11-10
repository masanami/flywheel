package invoker

// このファイルは `form: cli` の接続ツールの委譲（docs/architecture.md §10 の契約。開始・状態の確認・
// 再開・中止）の起動を持つ。コマンドは宣言の引数の配列をシェルを介さずに起動し、標準出力の JSON は
// 検査せずに core へ渡す（検査と状態の写像は core の mapContractOutput）。接続ツールの内部の名前は
// 持たない。start・resume は最初の待ちか終わりまで戻らないので、待つのは flywheel の側
// （宣言の timeout_sec.delegate）で、子の道具の待ち時間に左右されない。

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

// cliCancelTimeout は、再開が時間切れになったあとに中止のコマンドを待つ上限。
const cliCancelTimeout = 60 * time.Second

// stderrTail は失敗の要約に付ける標準エラーの末尾の長さ（バイト）。
const stderrTail = 500

// invokeCLIDelegation は形態 cli の委譲 1 回（新しい起動なら start、再開なら resume）を実行する。
//
//   - 起動できない（実行ファイルが無い・exec の失敗）: launch_failed。
//   - 時間切れ: timed_out。再開なら中止のコマンドも呼ぶ（中止の失敗は要約に残す）。新しい起動は実行 ID を
//     受け取れていないので宛先が無く、中止も再開もできない（接続ツールの動いた分の費用は core が数える）。
//   - 非 0 終了・出力なし: errored。再開なら status で 1 回だけ状態を取り直す。
//   - 終了コード 0 で標準出力が得られた: succeeded と ContractOutput（読み方は core）。
func (l *Launcher) invokeCLIDelegation(ctx context.Context, in core.DelegateLaunchInput) core.JudgmentLaunchOutput {
	args := in.CLIStart
	if in.IsResume {
		args = in.CLIResume
	}
	if len(args) == 0 {
		return core.JudgmentLaunchOutput{Result: core.RunResultLaunchFailed, ErrorSummary: "invoker: empty contract command"}
	}
	timeout := time.Duration(in.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = defaultInvokeTimeout * time.Second
	}
	if err := os.MkdirAll(in.RunDir, 0o755); err != nil {
		return core.JudgmentLaunchOutput{Result: core.RunResultLaunchFailed, ErrorSummary: fmt.Sprintf("invoker: create run dir: %v", err)}
	}
	env := delegatedEnv(os.Environ(), in.RunID)

	res, lookErr := runContractCommand(ctx, args, in.WorkDir, timeout, env)
	out := core.JudgmentLaunchOutput{}
	switch {
	case lookErr != nil:
		out.Result, out.ErrorSummary = core.RunResultLaunchFailed, lookErr.Error()
	case res.launchFailed:
		out.Result, out.ErrorSummary = core.RunResultLaunchFailed, res.err.Error()
	case res.timedOut:
		out.Result = core.RunResultTimedOut
		out.ErrorSummary = fmt.Sprintf("timed out after %s", timeout)
		if !in.IsResume {
			out.ErrorSummary += "; the run id was not received, so the connector's run cannot be resumed or cancelled"
		} else if len(in.CLICancel) > 0 {
			cres, cerr := runContractCommand(context.WithoutCancel(ctx), in.CLICancel, in.WorkDir, cliCancelTimeout, env)
			if cerr != nil || cres.err != nil || cres.launchFailed || cres.timedOut {
				out.ErrorSummary += "; cancel failed"
			} else {
				out.ErrorSummary += "; cancelled the connector's run"
			}
		}
	case res.err != nil || len(strings.TrimSpace(string(res.stdout))) == 0:
		out.Result = core.RunResultErrored
		out.ErrorSummary = fmt.Sprintf("%v: %s", res.err, tail(res.stderr))
		if res.err == nil {
			out.ErrorSummary = "no output on stdout: " + tail(res.stderr)
		}
		if in.IsResume && len(in.CLIStatus) > 0 {
			sres, serr := runContractCommand(ctx, in.CLIStatus, in.WorkDir, cliCancelTimeout, env)
			if serr == nil && !sres.launchFailed && !sres.timedOut && sres.err == nil && len(strings.TrimSpace(string(sres.stdout))) > 0 {
				res.stdout = sres.stdout
				out.Result, out.ErrorSummary = core.RunResultSucceeded, ""
			}
		}
	default:
		out.Result = core.RunResultSucceeded
	}
	if out.Result == core.RunResultSucceeded {
		out.ContractOutput = res.stdout
	}
	if err := saveRunArtifacts(in.RunDir, nil, res.stdout, res.stderr); err != nil {
		note := fmt.Sprintf("invoker: save run artifacts: %v", err)
		if out.ErrorSummary == "" {
			out.ErrorSummary = note
		} else {
			out.ErrorSummary += "; " + note
		}
	}
	_ = ensureRunsGitignoreEntry(in.Workspace)
	return out
}

// runContractCommand は引数の配列（先頭が実行ファイル）を、シェルを介さずに workDir で起動して待つ。
func runContractCommand(ctx context.Context, args []string, workDir string, timeout time.Duration, env []string) (runResult, error) {
	path, err := exec.LookPath(args[0])
	if err != nil {
		return runResult{}, err
	}
	return runClaude(ctx, path, workDir, args[1:], nil, timeout, env), nil
}

func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > stderrTail {
		s = s[len(s)-stderrTail:]
	}
	return s
}
