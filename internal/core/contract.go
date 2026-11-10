package core

// このファイルは `form: cli` の接続ツールの汎用の契約（docs/architecture.md §10。契約の版 1）の
// うち、core が持つ部分を置く: 宣言の差し込みの閉集合・コマンドの引数の組み立て・接続ツールが
// 出力する JSON の検査・汎用の状態から既存の委譲の報告への写像。コマンドの起動は
// internal/invoker が行い、core は起動しない。接続ツール固有の名前（工程・判断値）は知らない。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// cliCommandPlaceholders は `form: cli` の commands の各コマンドの引数に書ける差し込みの閉集合。
// start は課題の差し込み（operations[].invocation と同じ 4 つ）、status・cancel は start が返した
// 実行 ID、resume は実行 ID と、再開で渡す文（人間の回答・差し戻しの指摘。無ければ空）。
var cliCommandPlaceholders = map[string]map[string]bool{
	"start":  {"{issue_number}": true, "{issue_url}": true, "{external_key}": true, "{challenge_id}": true},
	"status": {"{run_id}": true},
	"cancel": {"{run_id}": true},
	"resume": {"{run_id}": true, "{input}": true},
}

// cliPendingSessionPrefix は、形態 cli の新しい起動が接続ツールの実行 ID を受け取るまで run の
// session_id に入れる目印の接頭辞。
const cliPendingSessionPrefix = "pending-"

const (
	cliPlaceholderRunID = "{run_id}"
	cliPlaceholderInput = "{input}"
)

// startNeedsSource は form: cli の start の引数が取り込み元の Issue の差し込みを持つか。
func (c *CLICommands) startNeedsSource() bool {
	if c == nil {
		return false
	}
	return invocationNeedsSource(strings.Join(c.Start, "\x00"))
}

// fillCLIStart は start の引数の差し込みを埋める。取り込み元の差し込みがあるのに課題に取り込み元の
// 対応が無ければ ErrValidation。
func fillCLIStart(args []string, challengeID string, sb *sourceBinding) ([]string, error) {
	out := make([]string, 0, len(args))
	for _, a := range args {
		f, err := fillInvocation(a, challengeID, sb)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// fillCLIRunArgs は status・resume・cancel の引数の `{run_id}` と `{input}` を埋める。input が空の
// ときは、`{input}` を含む引数を取り除き、その引数が `{input}` だけなら直前の `--` で始まる引数（オプション名）も取り除く
// （入力を渡さない再開が、空のオプションの値を接続ツールへ渡さないようにする）。
func fillCLIRunArgs(args []string, runID, input string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		// 宣言の文字列に実行 ID を先に入れる（入力の中の `{run_id}` という文字列は置換しない）。
		a = strings.ReplaceAll(a, cliPlaceholderRunID, runID)
		if strings.Contains(a, cliPlaceholderInput) {
			if input == "" {
				if a == cliPlaceholderInput {
					if n := len(out); n > 0 && strings.HasPrefix(out[n-1], "--") {
						out = out[:n-1]
					}
				}
				continue
			}
			a = strings.ReplaceAll(a, cliPlaceholderInput, input)
		}
		out = append(out, a)
	}
	return out
}

// contractState は契約の汎用の状態の閉集合。
type contractState string

// contractState の 5 値。
const (
	contractStateRunning   contractState = "running"
	contractStateWaiting   contractState = "waiting"
	contractStateSucceeded contractState = "succeeded"
	contractStateFailed    contractState = "failed"
	contractStateCancelled contractState = "cancelled"
)

// contractRequestedAction は接続ツールが人に求める操作（待っているときだけ非 nil）。
type contractRequestedAction struct {
	Kind    string `json:"kind"`
	Decider string `json:"decider"`
	Text    string `json:"text"`
}

// contractArtifact は成果物の参照。
type contractArtifact struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// contractDocument は契約の版 1 の出力（start・status・resume が標準出力へ 1 つ出す JSON）。
type contractDocument struct {
	ContractVersion int                      `json:"contract_version"`
	RunID           string                   `json:"run_id"`
	State           contractState            `json:"state"`
	Summary         string                   `json:"summary"`
	RequestedAction *contractRequestedAction `json:"requested_action"`
	Artifacts       []contractArtifact       `json:"artifacts"`
	CostUSD         *float64                 `json:"cost_usd"`
}

var contractDocumentKeys = []string{"contract_version", "run_id", "state", "summary", "requested_action", "artifacts", "cost_usd"}

// parseContractDocument は data を契約の版 1 の出力として検査して返す。JSON のオブジェクト 1 つでない・
// 必須のキーの欠落・未知のキー・版が 1 でない・実行 ID が空・状態が閉集合の外・型の違いはエラー。
func parseContractDocument(data []byte) (*contractDocument, error) {
	_, ok := parseStrictObject(bytes.TrimSpace(data), contractDocumentKeys)
	if !ok {
		return nil, fmt.Errorf("the output is not one JSON object with exactly the contract keys")
	}
	var d contractDocument
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("the output does not match the contract shape: %w", err)
	}
	if d.ContractVersion != 1 {
		return nil, fmt.Errorf("contract_version is %d, want 1", d.ContractVersion)
	}
	if d.CostUSD != nil && (*d.CostUSD < 0 || *d.CostUSD != *d.CostUSD) {
		return nil, fmt.Errorf("cost_usd is negative or not a number")
	}
	if strings.TrimSpace(d.RunID) == "" {
		return nil, fmt.Errorf("run_id is empty")
	}
	switch d.State {
	case contractStateRunning, contractStateWaiting, contractStateSucceeded, contractStateFailed, contractStateCancelled:
	default:
		return nil, fmt.Errorf("state %q is not one of the contract states", d.State)
	}
	if a := d.RequestedAction; a != nil {
		switch a.Decider {
		case "human", "parent", "any":
		default:
			return nil, fmt.Errorf("requested_action.decider %q is not one of human|parent|any", a.Decider)
		}
		switch a.Kind {
		case "answer", "approve", "observe":
		default:
			return nil, fmt.Errorf("requested_action.kind %q is not one of answer|approve|observe", a.Kind)
		}
	}
	return &d, nil
}

// contractMapping は契約の出力を既存の委譲の結果へ写した値。
type contractMapping struct {
	Result  RunResult
	Error   string
	Report  []byte // 委譲の報告（delegationReportKeys の形）の JSON。run の出力になる。
	CostUSD *float64
	RunID   string
}

// mapContractOutput は契約の出力 data を、委譲の run の結果と報告へ写す。
//
//   - 出力が契約を満たさない: 新しい起動（start）なら errored（接続ツールは動いた。実行 ID が無く再開できない。
//     費用は上限額で数え、照合も行う）、再開なら invalid_output。
//   - succeeded: succeeded・結末 completed。成果物（pr・branch・commit）を報告へ写す。
//   - waiting: succeeded・結末 blocked。人に求める操作を要約に書き、汎用の保留になる。
//   - failed・cancelled・running: errored（要約を error に残す）。
//
// 費用は cost_usd（孫の Claude を含む接続ツールの申告）をそのまま使う。
func mapContractOutput(data []byte, isResume bool) contractMapping {
	doc, err := parseContractDocument(data)
	if err != nil {
		if isResume {
			return contractMapping{Result: RunResultInvalidOutput, Error: invalidReportErrorPrefix + "contract output: " + err.Error()}
		}
		return contractMapping{Result: RunResultErrored, Error: "contract output: " + err.Error()}
	}
	m := contractMapping{RunID: doc.RunID, CostUSD: doc.CostUSD}
	summary := strings.TrimSpace(doc.Summary)
	switch doc.State {
	case contractStateSucceeded:
		m.Result = RunResultSucceeded
		m.Report = contractReport(DelegationOutcomeCompleted, summary, doc.Artifacts)
	case contractStateWaiting:
		m.Result = RunResultSucceeded
		m.Report = contractReport(DelegationOutcomeBlocked, contractWaitingSummary(doc), doc.Artifacts)
	default:
		m.Result = RunResultErrored
		m.Error = fmt.Sprintf("contract state %s: %s", doc.State, summary)
	}
	return m
}

// contractWaitingSummary は waiting の出力を、保留の問い（人が読む）に入れる要約にする。
func contractWaitingSummary(d *contractDocument) string {
	var b strings.Builder
	b.WriteString("接続ツールが止まって待っている")
	if a := d.RequestedAction; a != nil {
		fmt.Fprintf(&b, "（求める操作: %s、決める人: %s）。\n%s", a.Kind, a.Decider, strings.TrimSpace(a.Text))
	} else {
		b.WriteString("（求める操作は示されていない）。")
	}
	if s := strings.TrimSpace(d.Summary); s != "" {
		b.WriteString("\n" + s)
	}
	fmt.Fprintf(&b, "\n実行 ID: %s。接続ツール側の操作を済ませた後に回答すると、その実行を再開する。", d.RunID)
	return b.String()
}

// contractReport は契約の成果物を、委譲の報告の JSON（必須のキーが揃った形）にする。
// pr は URL の形の参照だけを pr_urls に入れる（照合は取得で行う）。
func contractReport(outcome DelegationOutcome, summary string, artifacts []contractArtifact) []byte {
	prs, commits := []string{}, []string{}
	var branch *string
	for _, a := range artifacts {
		ref := strings.TrimSpace(a.Ref)
		switch a.Kind {
		case "pr":
			if strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "http://") {
				prs = append(prs, ref)
			}
		case "branch":
			if branch == nil && plausibleBranchName(ref) {
				b := ref
				branch = &b
			}
		case "commit":
			if ref != "" {
				commits = append(commits, ref)
			}
		}
	}
	b, _ := json.Marshal(map[string]any{
		"outcome": string(outcome), "summary": summary, "branch": branch, "pr_urls": prs, "commits": commits,
		"quality_gate": nil, "assumptions": []string{}, "unverified": []string{}, "questions": []any{},
	})
	return b
}
