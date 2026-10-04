package invoker

// このファイルは衝突の予測の口（接続ツールの宣言の conflict_prediction。例: `harness
// predict-conflicts`・出力の形 `harness.conflict-prediction/v1`。親要件チケット #98 §クリティカル
// 設計決定 7・M3P36）の起動を持つ。core.ConflictPredictor の実装であり、並列にするか・どの順で
// 入れるかといった規則は持たない。出力から読むのは flywheel が使う汎用のフィールドだけで、
// 規則（schema の照合・status の閉集合・費用の数え方）の判定は core が行う。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

// rawPrediction は予測の口の出力のうち flywheel が読むフィールド。型が合わなければ JSON として
// 読めなかったもの（malformed）として扱う。cost_usd・unknown_cost_count・error は型が何であっても
// 読めるよう生の値で受ける（型の判定は decodePrediction が行う）。
type rawPrediction struct {
	Schema           string          `json:"schema"`
	Complete         bool            `json:"complete"`
	Error            json.RawMessage `json:"error"`
	HeadSHA          string          `json:"head_sha"`
	CostUSD          json.RawMessage `json:"cost_usd"`
	UnknownCostCount json.RawMessage `json:"unknown_cost_count"`
	Issues           []struct {
		Issue  int    `json:"issue"`
		Status string `json:"status"`
	} `json:"issues"`
	Pairs []struct {
		Issues      []int  `json:"issues"`
		Status      string `json:"status"`
		SharedFiles []struct {
			Path          string `json:"path"`
			MergeFriendly bool   `json:"merge_friendly"`
			Ignored       bool   `json:"ignored"`
		} `json:"shared_files"`
		Dependency *struct {
			First *int `json:"first"`
		} `json:"dependency"`
	} `json:"pairs"`
}

func isJSONNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

// decodeNumber は raw が JSON の数なら (値, true)。無い・null・数でないは (0, false)。
func decodeNumber(raw json.RawMessage) (float64, bool) {
	if isJSONNull(raw) {
		return 0, false
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, false
	}
	return v, true
}

// decodePrediction は標準出力を core.ConflictPredictionOutput へ正規化する。単一の JSON
// オブジェクトとして読めない・読むフィールドの型が合わない・pairs[].issues が 2 件でないときは
// ok=false。
func decodePrediction(stdout []byte) (*core.ConflictPredictionOutput, bool) {
	var raw rawPrediction
	if err := json.Unmarshal(stdout, &raw); err != nil {
		return nil, false
	}
	out := &core.ConflictPredictionOutput{
		Schema: raw.Schema, Complete: raw.Complete, ErrorSet: !isJSONNull(raw.Error), HeadSHA: raw.HeadSHA,
	}
	if v, ok := decodeNumber(raw.CostUSD); ok {
		out.CostUSD = &v
	}
	if !isJSONNull(raw.UnknownCostCount) {
		if v, ok := decodeNumber(raw.UnknownCostCount); ok {
			out.UnknownCostCount = &v
		} else {
			out.UnknownCostCountInvalid = true
		}
	}
	for _, i := range raw.Issues {
		out.Issues = append(out.Issues, core.PredictedIssue{Issue: i.Issue, Status: i.Status})
	}
	for _, p := range raw.Pairs {
		if len(p.Issues) != 2 {
			return nil, false
		}
		pair := core.PredictedPair{Issues: [2]int{p.Issues[0], p.Issues[1]}, Status: p.Status}
		for _, f := range p.SharedFiles {
			pair.SharedFiles = append(pair.SharedFiles, core.PredictedSharedFile{Path: f.Path, MergeFriendly: f.MergeFriendly, Ignored: f.Ignored})
		}
		if p.Dependency != nil {
			pair.DependencyFirst = p.Dependency.First
		}
		out.Pairs = append(out.Pairs, pair)
	}
	return out, true
}

// buildPredictArgs は宣言の command の後ろへ `--max-budget-usd <額>` と Issue 番号を別々の
// 要素として足した引数（command[0] を除く）を返す。シェルを介さない。
func buildPredictArgs(command []string, maxBudgetUSD float64, issues []int) []string {
	args := append([]string(nil), command[1:]...)
	args = append(args, "--max-budget-usd", formatUSDArg(maxBudgetUSD))
	for _, n := range issues {
		args = append(args, strconv.Itoa(n))
	}
	return args
}

// Predict は core.ConflictPredictor の実装本体。宣言の command を、作業ディレクトリ WorkDir で
// シェルを介さずに起動して待ち、標準出力を正規化する。command[0] は PATH で解決する。
// 戻り値の error は invoker 自身の予期しない失敗だけに使う（起動失敗・時間切れ・異常終了・
// 読めない出力は Result で表す）。
func (l *Launcher) Predict(ctx context.Context, in core.PredictLaunchInput) (core.PredictLaunchOutput, error) {
	if len(in.Command) == 0 {
		return core.PredictLaunchOutput{Result: core.RunResultLaunchFailed, ErrorSummary: "invoker: empty conflict prediction command"}, nil
	}
	path, lookErr := exec.LookPath(in.Command[0])
	if lookErr != nil {
		return core.PredictLaunchOutput{Result: core.RunResultLaunchFailed, ErrorSummary: lookErr.Error()}, nil
	}
	timeout := time.Duration(in.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = defaultInvokeTimeout * time.Second
	}
	if err := os.MkdirAll(in.RunDir, 0o755); err != nil {
		return core.PredictLaunchOutput{Result: core.RunResultLaunchFailed, ErrorSummary: fmt.Sprintf("invoker: create run dir: %v", err)}, nil
	}

	res := runClaude(ctx, path, in.WorkDir, buildPredictArgs(in.Command, in.MaxBudgetUSD, in.Issues), nil, timeout)

	out := core.PredictLaunchOutput{RawOutput: res.stdout}
	switch {
	case res.launchFailed:
		out.Result, out.ErrorSummary = core.RunResultLaunchFailed, res.err.Error()
	case res.timedOut:
		out.Result, out.ErrorSummary = core.RunResultTimedOut, fmt.Sprintf("timed out after %s", timeout)
	}
	if out.Result == "" {
		// 終了コードに関わらず、読めるなら費用の材料として正規化する。
		p, ok := decodePrediction(res.stdout)
		out.Prediction = p
		switch {
		case res.err != nil:
			out.Result, out.ErrorSummary = core.RunResultErrored, res.err.Error()
		case !ok:
			out.Result, out.ErrorSummary = core.RunResultMalformed, "stdout is not a single JSON object of the expected shape"
		default:
			out.Result = core.RunResultSucceeded
		}
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
	return out, nil
}
