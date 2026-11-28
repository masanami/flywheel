package invoker

import (
	"context"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

const sampleFlowCheck = `{"definitions":[{"name":"ticket","source":"workspace","kind":"replace","path":"/ws/.flywheel/flows/ticket.yaml","sha256":"",
  "ok":false,"problems":[{"file":"/ws/.flywheel/flows/ticket.yaml","line":3,"message":"bad"}]}]}`

// 確かめの出力は、終了コードが 0 か 1 のときだけ読み、定義ごとの値を写す。ほかの終了・読めない出力・起動できないは失敗の種類で返す。
func TestCheckFlow_ResultsAndShape(t *testing.T) {
	for name, tc := range map[string]struct {
		fx   fakeClaudeFixture
		want core.RunResult
	}{
		"exit 1 with json": {fakeClaudeFixture{Stdout: sampleFlowCheck, ExitCode: 1}, core.RunResultSucceeded},
		"exit 0 with json": {fakeClaudeFixture{Stdout: `{"definitions":[{"name":"ticket","source":"builtin","kind":"builtin","path":"","sha256":"abc","ok":true,"problems":[]}]}`}, core.RunResultSucceeded},
		"exit 2":           {fakeClaudeFixture{Stdout: sampleFlowCheck, ExitCode: 2}, core.RunResultErrored},
		"not json":         {fakeClaudeFixture{Stdout: "oops"}, core.RunResultMalformed},
		"no ok":            {fakeClaudeFixture{Stdout: `{"definitions":[{"name":"ticket"}]}`}, core.RunResultMalformed},
		"no definitions":   {fakeClaudeFixture{Stdout: `{}`, ExitCode: 1}, core.RunResultMalformed},
	} {
		t.Run(name, func(t *testing.T) {
			in, _ := contractInput(t, tc.fx)
			out, err := NewLauncher().CheckFlow(context.Background(), in)
			if err != nil || out.Result != tc.want {
				t.Fatalf("result = %s err %v (%s), want %s", out.Result, err, out.ErrorSummary, tc.want)
			}
		})
	}
	in, _ := contractInput(t, fakeClaudeFixture{Stdout: sampleFlowCheck, ExitCode: 1})
	out, _ := NewLauncher().CheckFlow(context.Background(), in)
	if len(out.Definitions) != 1 {
		t.Fatalf("definitions = %+v", out.Definitions)
	}
	d := out.Definitions[0]
	if d.Name != "ticket" || d.Source != "workspace" || d.Kind != "replace" || d.OK || len(d.Problems) != 1 ||
		d.Problems[0] != (core.FlowCheckProblem{File: "/ws/.flywheel/flows/ticket.yaml", Line: 3, Message: "bad"}) {
		t.Errorf("definition = %+v", d)
	}
	described, _ := contractInput(t, fakeClaudeFixture{Stdout: `{"definitions":[{"name":"ui-only","source":"workspace","kind":"new","path":"/ws/x.yaml","sha256":"abc","ok":true,"problems":[],"description":"画面だけの直し"}]}`})
	if out, _ := NewLauncher().CheckFlow(context.Background(), described); len(out.Definitions) != 1 || out.Definitions[0].Description != "画面だけの直し" {
		t.Errorf("description = %+v", out.Definitions)
	}
	missing := core.ContractLaunchInput{Command: []string{"/no/such/flywheel", "flow", "check"}, TimeoutSec: 5}
	if out, _ := NewLauncher().CheckFlow(context.Background(), missing); out.Result != core.RunResultLaunchFailed {
		t.Errorf("missing executable = %s, want launch_failed", out.Result)
	}
}
