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
	"math"
	"os"
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
		// role は任意（文字列）。在って文字列でない（null を含む）と型が合わない（malformed）。
		Role json.RawMessage `json:"role"`
	} `json:"artifacts"`
	CostUSD json.RawMessage `json:"cost_usd"`
	// facts は任意（名前から文字列への対応）。形が合わなければ無いものとして扱い、出力の全体は malformed にしない。
	Facts json.RawMessage `json:"facts"`
	// flow・steps は任意。形が合わなければ無いものとして扱い、出力の全体は malformed にしない。
	Flow  json.RawMessage `json:"flow"`
	Steps json.RawMessage `json:"steps"`
	// definition は任意（source・sha256 の 2 つの文字列）。形が合わなければ無いものとして扱い、出力の全体は malformed にしない。
	Definition json.RawMessage `json:"definition"`
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
			// options は求められた操作が示す選択肢（任意。文字列の配列）。文字列でない要素が 1 つでもあれば型が合わない
			// （malformed）として、契約の状態を更新しない。
			Options []string `json:"options"`
			// resolvable_by は任意（answer | terminal）。文字列以外・空文字は閉集合の外として core が安全側（terminal）に扱う。
			ResolvableBy json.RawMessage `json:"resolvable_by"`
		}
		if err := json.Unmarshal(raw.RequestedAction, &ra); err != nil {
			return nil, false
		}
		st.RequestedAction = &core.ContractRequestedAction{Kind: ra.Kind, Decider: ra.Decider, Text: ra.Text, Options: ra.Options,
			ResolvableBy: decodeResolvableBy(ra.ResolvableBy)}
	}
	for _, a := range raw.Artifacts {
		art := core.ContractArtifact{Kind: a.Kind, Ref: a.Ref}
		if a.Role != nil {
			// キーが在る。null は Go の復号では「無い」と区別できないので、生の値を文字列として読み直す。
			if isJSONNull(a.Role) || json.Unmarshal(a.Role, &art.Role) != nil {
				return nil, false
			}
		}
		st.Artifacts = append(st.Artifacts, art)
	}
	if !isJSONNull(raw.Facts) {
		st.Facts = decodeFacts(raw.Facts)
	}
	if !isJSONNull(raw.Flow) {
		var f string
		if json.Unmarshal(raw.Flow, &f) == nil {
			st.Flow = &f
		}
	}
	st.Steps = decodeSteps(raw.Steps)
	st.Definition = decodeDefinition(raw.Definition)
	if !isJSONNull(raw.CostUSD) {
		v, ok := decodeNumber(raw.CostUSD)
		if !ok {
			return nil, false
		}
		st.CostUSD = &v
	}
	return st, true
}

// decodeDefinition は definition を読む。無い・null・オブジェクトでない・source か sha256 が文字列でないときは nil
// （値の閉集合は core が確かめる）。
func decodeDefinition(raw json.RawMessage) *core.ContractDefinition {
	if isJSONNull(raw) {
		return nil
	}
	var d struct {
		Source *string `json:"source"`
		SHA256 *string `json:"sha256"`
	}
	if json.Unmarshal(raw, &d) != nil || d.Source == nil || d.SHA256 == nil {
		return nil
	}
	return &core.ContractDefinition{Source: *d.Source, SHA256: *d.SHA256}
}

// Start は core.ContractClient の実装（契約の start）。
func (l *Launcher) Start(ctx context.Context, in core.ContractLaunchInput) (core.ContractLaunchOutput, error) {
	return l.runContractCommand(ctx, in)
}

// Status は core.ContractClient の実装（契約の status）。
func (l *Launcher) Status(ctx context.Context, in core.ContractLaunchInput) (core.ContractLaunchOutput, error) {
	return l.runContractCommand(ctx, in)
}

// Resume は core.ContractClient の実装（契約の resume。判断待ちへの答えを返す。start と同じくすぐ戻る）。
func (l *Launcher) Resume(ctx context.Context, in core.ContractLaunchInput) (core.ContractLaunchOutput, error) {
	return l.runContractCommand(ctx, in)
}

// Cancel は core.ContractClient の実装（契約の cancel）。
func (l *Launcher) Cancel(ctx context.Context, in core.ContractLaunchInput) (core.ContractLaunchOutput, error) {
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
	path, lookErr := l.look(in.Command[0])
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

// decodeResolvableBy は requested_action.resolvable_by を値の文字列へ写す。項目が無い・null は空（今までどおり）。
// 文字列でない値・空文字は閉集合の外の値として core.ContractResolvableByInvalid を返す（core が安全側に terminal として扱う）。
func decodeResolvableBy(raw json.RawMessage) string {
	if len(raw) == 0 || isJSONNull(raw) {
		return ""
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil || v == "" {
		return core.ContractResolvableByInvalid
	}
	return v
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

// decodeFacts は facts を名前から文字列への対応として読む。対応でない・値に文字列でないもの（null を含む）が
// 1 つでもあれば nil（無いものとして扱う）。
func decodeFacts(raw json.RawMessage) map[string]string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		var s string
		if isJSONNull(v) || json.Unmarshal(v, &s) != nil {
			return nil
		}
		out[k] = s
	}
	return out
}

// stepKeys は steps の要素の必須の欄（effort・findings_count は任意で含めない）（値が null でよい欄も、キーは必須）。
var stepKeys = []string{"seq", "step", "kind", "attempt", "state", "outcome", "started_at", "ended_at", "cost_usd", "cost_unknown", "model", "models_used"}

// decodeSteps は steps を読む。配列でない・要素に必須の欄が無い・欄の型が違う・state が 4 つの値の外・seq が 1 未満・
// seq が重なる・attempt が 1 未満のどれか 1 つでもあれば nil（steps の全体を無いものとして扱う）。
// 無い・null も nil。在って 0 件の配列は空の非 nil。
func decodeSteps(raw json.RawMessage) []core.ContractStep {
	if isJSONNull(raw) {
		return nil
	}
	var elems []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil || elems == nil {
		return nil
	}
	out := make([]core.ContractStep, 0, len(elems))
	seen := map[int]bool{}
	for _, e := range elems {
		if e == nil {
			return nil
		}
		for _, k := range stepKeys {
			if _, ok := e[k]; !ok {
				return nil
			}
		}
		var sp core.ContractStep
		var ok bool
		if sp.Seq, ok = stepInt(e["seq"]); !ok || sp.Seq < 1 || seen[sp.Seq] {
			return nil
		}
		seen[sp.Seq] = true
		if sp.Attempt, ok = stepInt(e["attempt"]); !ok || sp.Attempt < 1 {
			return nil
		}
		if json.Unmarshal(e["step"], &sp.Step) != nil || json.Unmarshal(e["kind"], &sp.Kind) != nil ||
			json.Unmarshal(e["state"], &sp.State) != nil || json.Unmarshal(e["started_at"], &sp.StartedAt) != nil ||
			json.Unmarshal(e["cost_unknown"], &sp.CostUnknown) != nil || isJSONNull(e["step"]) || isJSONNull(e["kind"]) ||
			isJSONNull(e["state"]) || isJSONNull(e["started_at"]) || isJSONNull(e["cost_unknown"]) {
			return nil
		}
		switch sp.State {
		case "running", "finished", "cancelled", "interrupted":
		default:
			return nil
		}
		if sp.Outcome, ok = stepNullableString(e["outcome"]); !ok {
			return nil
		}
		if sp.EndedAt, ok = stepNullableString(e["ended_at"]); !ok {
			return nil
		}
		if sp.Model, ok = stepNullableString(e["model"]); !ok {
			return nil
		}
		if !isJSONNull(e["cost_usd"]) {
			v, ok := decodeNumber(e["cost_usd"])
			if !ok {
				return nil
			}
			sp.CostUSD = &v
		}
		if raw, present := e["effort"]; present {
			// 任意の欄。無い・null は nil。文字列でも null でもなければ steps の全体を無いものとして扱う。
			if sp.Effort, ok = stepNullableString(raw); !ok {
				return nil
			}
		}
		if raw, present := e["findings_count"]; present && !isJSONNull(raw) {
			// 任意の欄。無い・null は nil。0 以上の整数でなければ steps の全体を無いものとして扱う。
			n, ok := stepInt(raw)
			if !ok || n < 0 {
				return nil
			}
			sp.FindingsCount = &n
		}
		var models []string
		if isJSONNull(e["models_used"]) || json.Unmarshal(e["models_used"], &models) != nil {
			return nil
		}
		if models == nil {
			models = []string{}
		}
		sp.ModelsUsed = models
		out = append(out, sp)
	}
	return out
}

// stepInt は raw が整数の JSON の数なら (値, true)。
func stepInt(raw json.RawMessage) (int, bool) {
	v, ok := decodeNumber(raw)
	if !ok || v != math.Trunc(v) || math.Abs(v) > 1<<31 {
		return 0, false
	}
	return int(v), true
}

// stepNullableString は raw が文字列か null のとき (値か nil, true)。
func stepNullableString(raw json.RawMessage) (*string, bool) {
	if isJSONNull(raw) {
		return nil, true
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return nil, false
	}
	return &s, true
}
