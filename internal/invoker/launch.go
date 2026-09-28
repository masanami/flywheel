package invoker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

// InvokeJudgment は core.JudgmentInvoker の実装本体。claude を起動して待ち、
// 応答を core.JudgmentLaunchOutput へ正規化する。規則（対象の選び方・出力の
// 課題への写像・予算の評価等）は一切持たない（§機能全体の設計）。
//
// 戻り値の error は「invoker 自身の予期しない失敗」（出力の保存に使う
// ディレクトリを作れない等）だけに使う。claude 自身の起動失敗・タイムアウト・
// 異常終了・不正な出力は、error ではなく core.JudgmentLaunchOutput.Result で
// 表す（§結果の判別）。
func (l *Launcher) InvokeJudgment(ctx context.Context, in core.JudgmentLaunchInput) (core.JudgmentLaunchOutput, error) {
	claudePath, lookErr := exec.LookPath("claude")
	if lookErr != nil {
		// Available() の事前チェックの後でも、TOCTOU で claude が消えることは
		// ありうる。この場合は起動失敗として run の結果に示す（呼び出し元の
		// RunJudgment は既に①で run を記録済みのため、ここで core.Store の状態を
		// 変えずに終える経路は無い＝launch_failed として③で閉じる）。
		return core.JudgmentLaunchOutput{
			Result:       core.RunResultLaunchFailed,
			ErrorSummary: lookErr.Error(),
		}, nil
	}

	timeout := time.Duration(in.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = defaultInvokeTimeout * time.Second
	}

	// self-review 指摘（round1, code-reviewer/design-reviewer 双方が
	// CONFIRMED）: 以前はここで RunDir をまだ作らずに claude を起動し、
	// 起動後の保存の失敗を launch_failed（費用0・出力破棄）へすり替えて
	// いた。claude が実際に起動・課金された後の保存失敗を launch_failed に
	// すると、§費用の記録の fail-closed 規則（結果のJSONを得られないときは
	// 上限額を消費したとみなす）を回避してしまう。RunDir の作成は「まだ
	// 何も起動していない」時点（＝失敗しても launch_failed が正しい）に
	// 前倒しし、起動後の保存失敗は判別結果を上書きしない。
	if err := os.MkdirAll(in.RunDir, 0o755); err != nil {
		return core.JudgmentLaunchOutput{
			Result:       core.RunResultLaunchFailed,
			ErrorSummary: fmt.Sprintf("invoker: create run dir: %v", err),
		}, nil
	}

	args := buildArgs(in)
	res := runClaude(ctx, claudePath, in.Workspace, args, in.Stdin, timeout)

	var launchErr error
	if res.launchFailed {
		launchErr = res.err
	}

	result, resp := classifyOutcome(launchErr, res.timedOut, res.stdout)

	out := core.JudgmentLaunchOutput{Result: result}
	if resp != nil {
		out.SessionIDReturned = resp.SessionID
		out.ReportedTotalCostUSD = resp.TotalCostUSD
		// self-review 指摘（round1, design-reviewer）: 枠超過の判定規則
		// （M3P12「現行quota-check.shの規則をcoreへ移し」）は core.IsRateLimited
		// に置く。invoker はここで得た自由記述（resp.Result）をそのまま渡す
		// だけで、判定の規則自体は持たない。
		out.RateLimited = core.IsRateLimited(resp.Result)
		if result == core.RunResultSucceeded {
			out.StructuredOutput = resp.StructuredOutput
		}
		if resp.IsError {
			out.ErrorSummary = resp.Result
		}
	}
	switch {
	case result == core.RunResultLaunchFailed && launchErr != nil:
		out.ErrorSummary = launchErr.Error()
	case result == core.RunResultTimedOut:
		out.ErrorSummary = fmt.Sprintf("timed out after %s", timeout)
	case result == core.RunResultMalformed:
		out.ErrorSummary = "stdout is not a single JSON object"
	}

	// 保存: 標準出力・標準エラー・渡した入力（§invoker の共通の規則）。
	// claude は既に起動（この時点で launch_failed 以外なら課金されうる）
	// しているため、保存の失敗は上で決めた result・費用の材料
	// （ReportedTotalCostUSD・StructuredOutput 等）を一切上書きしない
	// （self-review 指摘 round1: 保存失敗を launch_failed へすり替えると
	// 実行済みの run の費用が0に落ちる）。保存できなかった事実だけを
	// ErrorSummary に付記する（既存のエラー要約があれば残しつつ追記する）。
	if err := saveRunArtifacts(in.RunDir, in.Stdin, res.stdout, res.stderr); err != nil {
		note := fmt.Sprintf("invoker: save run artifacts: %v", err)
		if out.ErrorSummary == "" {
			out.ErrorSummary = note
		} else {
			out.ErrorSummary = out.ErrorSummary + "; " + note
		}
	}
	// .gitignore の更新は付随の記録であり、失敗しても run の判別には
	// 影響させない（ベストエフォート。runs/ の既定はワークスペース初期化
	// 〈core.Init〉の時点で core が書く。ここは.gitignoreが後から
	// 削除された場合の保険）。
	_ = ensureRunsGitignoreEntry(in.Workspace)

	return out, nil
}

// saveRunArtifacts は runDir（.flywheel/runs/<run の ID>/）を作り、渡した
// 標準入力・受け取った標準出力・標準エラーを保存する
// （§invoker の共通の規則）。
func saveRunArtifacts(runDir string, stdin, stdout, stderr []byte) error {
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	writes := []struct {
		name string
		data []byte
	}{
		{"stdin.txt", stdin},
		{"stdout.json", stdout},
		{"stderr.txt", stderr},
	}
	for _, w := range writes {
		if err := os.WriteFile(filepath.Join(runDir, w.name), w.data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
