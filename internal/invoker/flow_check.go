package invoker

// このファイルは、flow の形の委譲先でタスクの委譲を始める前のフローの定義の確かめ（`flow check --json <名前>`）の起動を持つ。
// core.ContractClient の CheckFlow の実装で、確かめの JSON を core.FlowCheckOutput へ写すだけ（通ったかの扱いは core）。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

type rawFlowCheck struct {
	Definitions []struct {
		Name     string `json:"name"`
		Source   string `json:"source"`
		Kind     string `json:"kind"`
		Path     string `json:"path"`
		SHA256   string `json:"sha256"`
		OK       *bool  `json:"ok"`
		Problems []struct {
			File    string `json:"file"`
			Line    int    `json:"line"`
			Message string `json:"message"`
		} `json:"problems"`
	} `json:"definitions"`
}

// decodeFlowCheck は確かめの標準出力を読む。単一の JSON オブジェクトとして読めない・definitions が無い・ok が無いときは ok=false。
func decodeFlowCheck(stdout []byte) ([]core.FlowCheckDefinition, bool) {
	dec := json.NewDecoder(bytes.NewReader(stdout))
	var raw rawFlowCheck
	if err := dec.Decode(&raw); err != nil || dec.More() || raw.Definitions == nil {
		return nil, false
	}
	out := make([]core.FlowCheckDefinition, 0, len(raw.Definitions))
	for _, d := range raw.Definitions {
		if d.OK == nil || d.Name == "" {
			return nil, false
		}
		def := core.FlowCheckDefinition{Name: d.Name, Source: d.Source, Kind: d.Kind, Path: d.Path, SHA256: d.SHA256, OK: *d.OK}
		for _, p := range d.Problems {
			def.Problems = append(def.Problems, core.FlowCheckProblem{File: p.File, Line: p.Line, Message: p.Message})
		}
		out = append(out, def)
	}
	return out, true
}

// CheckFlow は core.ContractClient の実装（委譲を始める前のフローの定義の確かめ）。終了コードは 0（すべて通った）か 1（通らないものがある）
// のときだけ標準出力を読み、それ以外の終了は errored にする。実行の記録の置き場には何も書かない。
func (l *Launcher) CheckFlow(ctx context.Context, in core.ContractLaunchInput) (core.FlowCheckOutput, error) {
	if len(in.Command) == 0 {
		return core.FlowCheckOutput{Result: core.RunResultLaunchFailed, ErrorSummary: "invoker: empty flow check command"}, nil
	}
	path, lookErr := l.look(in.Command[0])
	if lookErr != nil {
		return core.FlowCheckOutput{Result: core.RunResultLaunchFailed, ErrorSummary: lookErr.Error()}, nil
	}
	timeout := time.Duration(in.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = defaultInvokeTimeout * time.Second
	}
	res := runClaude(ctx, path, in.WorkDir, in.Command[1:], nil, timeout, withoutDelegatedMarker(os.Environ()))
	switch {
	case res.launchFailed:
		return core.FlowCheckOutput{Result: core.RunResultLaunchFailed, ErrorSummary: res.err.Error()}, nil
	case res.timedOut:
		return core.FlowCheckOutput{Result: core.RunResultTimedOut, ErrorSummary: fmt.Sprintf("timed out after %s", timeout)}, nil
	case res.err != nil:
		var exitErr *exec.ExitError
		if !errors.As(res.err, &exitErr) || exitErr.ExitCode() != 1 {
			return core.FlowCheckOutput{Result: core.RunResultErrored, ErrorSummary: res.err.Error() + stderrTail(res.stderr)}, nil
		}
	}
	defs, ok := decodeFlowCheck(res.stdout)
	if !ok {
		return core.FlowCheckOutput{Result: core.RunResultMalformed, ErrorSummary: "stdout is not the JSON of the flow check" + stderrTail(res.stderr)}, nil
	}
	return core.FlowCheckOutput{Result: core.RunResultSucceeded, Definitions: defs}, nil
}

// stderrTail は標準エラーの末尾（最大 500 バイト）を「（stderr: …）」として返す（無ければ空）。
func stderrTail(stderr []byte) string {
	t := bytes.TrimSpace(stderr)
	if len(t) == 0 {
		return ""
	}
	if len(t) > 500 {
		t = t[len(t)-500:]
	}
	return "（stderr: " + string(t) + "）"
}
