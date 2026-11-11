package invoker

// このファイルは `form: cli` の接続ツールの契約のコマンド（start・status。docs/features/
// m3-invoker-delegation.md §委譲の流れ（`form: cli`））の起動を持つ。core.ContractClient の実装であり、
// 状態の振り分け・費用の数え方といった規則は持たない。宣言の引数の配列（差し込みは core が埋め済み）を
// シェルを介さずに起動し、標準出力の JSON（契約の版 1）を core.ContractStatus へ正規化するだけで、
// 閉集合・必須の検査は core が行う。接続ツール固有の名前は持たない。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

// rawContractStatus は契約の標準出力のうち flywheel が読むフィールド。型が合わなければ JSON として
// 読めなかったもの（malformed）として扱う。
type rawContractStatus struct {
	ContractVersion int             `json:"contract_version"`
	RunID           string          `json:"run_id"`
	State           string          `json:"state"`
	Summary         string          `json:"summary"`
	RequestedAction json.RawMessage `json:"requested_action"`
	Artifacts       []struct {
		Kind string `json:"kind"`
		Ref  string `json:"ref"`
	} `json:"artifacts"`
	CostUSD json.RawMessage `json:"cost_usd"`
}

// decodeContractStatus は標準出力を core.ContractStatus へ正規化する。単一の JSON オブジェクトとして
// 読めない・読むフィールドの型が合わないときは ok=false。
func decodeContractStatus(stdout []byte) (*core.ContractStatus, bool) {
	dec := json.NewDecoder(bytes.NewReader(stdout))
	var raw rawContractStatus
	if err := dec.Decode(&raw); err != nil {
		return nil, false
	}
	if dec.More() {
		return nil, false
	}
	st := &core.ContractStatus{
		ContractVersion: raw.ContractVersion, RunID: raw.RunID, State: core.ContractState(raw.State), Summary: raw.Summary,
	}
	if !isJSONNull(raw.RequestedAction) {
		var ra struct {
			Kind    string `json:"kind"`
			Decider string `json:"decider"`
			Text    string `json:"text"`
		}
		if err := json.Unmarshal(raw.RequestedAction, &ra); err != nil {
			return nil, false
		}
		st.RequestedAction = &core.ContractRequestedAction{Kind: ra.Kind, Decider: ra.Decider, Text: ra.Text}
	}
	for _, a := range raw.Artifacts {
		st.Artifacts = append(st.Artifacts, core.ContractArtifact{Kind: a.Kind, Ref: a.Ref})
	}
	if !isJSONNull(raw.CostUSD) {
		v, ok := decodeNumber(raw.CostUSD)
		if !ok {
			return nil, false
		}
		st.CostUSD = &v
	}
	return st, true
}

// Start は core.ContractClient の実装（契約の start）。
func (l *Launcher) Start(ctx context.Context, in core.ContractLaunchInput) (core.ContractLaunchOutput, error) {
	return l.runContractCommand(ctx, in)
}

// Status は core.ContractClient の実装（契約の status）。
func (l *Launcher) Status(ctx context.Context, in core.ContractLaunchInput) (core.ContractLaunchOutput, error) {
	return l.runContractCommand(ctx, in)
}

// runContractCommand は in.Command を WorkDir でシェルを介さずに起動して待ち、標準出力を正規化する。
// command[0] は PATH で解決する。終了コードは「JSON を出力できたか」だけを表すので、0 でなければ
// 出力があっても errored とする（状態は標準出力の state を正とする）。戻り値の error は invoker 自身の
// 予期しない失敗だけに使う。
func (l *Launcher) runContractCommand(ctx context.Context, in core.ContractLaunchInput) (core.ContractLaunchOutput, error) {
	if len(in.Command) == 0 {
		return core.ContractLaunchOutput{Result: core.RunResultLaunchFailed, ErrorSummary: "invoker: empty contract command"}, nil
	}
	path, lookErr := exec.LookPath(in.Command[0])
	if lookErr != nil {
		return core.ContractLaunchOutput{Result: core.RunResultLaunchFailed, ErrorSummary: lookErr.Error()}, nil
	}
	timeout := time.Duration(in.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = defaultInvokeTimeout * time.Second
	}
	if err := os.MkdirAll(in.RunDir, 0o755); err != nil {
		return core.ContractLaunchOutput{Result: core.RunResultLaunchFailed, ErrorSummary: fmt.Sprintf("invoker: create run dir: %v", err)}, nil
	}

	res := runClaude(ctx, path, in.WorkDir, in.Command[1:], nil, timeout, withoutDelegatedMarker(os.Environ()))

	out := core.ContractLaunchOutput{RawOutput: res.stdout}
	switch {
	case res.launchFailed:
		out.Result, out.ErrorSummary = core.RunResultLaunchFailed, res.err.Error()
	case res.timedOut:
		out.Result, out.ErrorSummary = core.RunResultTimedOut, fmt.Sprintf("timed out after %s", timeout)
	case res.err != nil:
		out.Result, out.ErrorSummary = core.RunResultErrored, res.err.Error()
	default:
		st, ok := decodeContractStatus(res.stdout)
		if !ok {
			out.Result, out.ErrorSummary = core.RunResultMalformed, "stdout is not a single JSON object of the expected shape"
		} else {
			out.Result, out.Status = core.RunResultSucceeded, st
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
