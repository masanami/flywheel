package invoker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// このファイルは契約のコマンドの起動（Launcher.Start・Status）を、偽の実行ファイルで検証する。

// Launcher は core.ContractClient を実装する。
var _ core.ContractClient = (*Launcher)(nil)

const sampleContractStatus = `{
  "contract_version": 1, "run_id": "r-42", "state": "waiting", "summary": "承認待ち",
  "requested_action": {"kind": "approve", "decider": "human", "text": "承認してください"},
  "artifacts": [{"kind": "pr", "ref": "https://github.com/o/r/pull/3"}],
  "cost_usd": 12.5
}`

func contractInput(t *testing.T, fx fakeClaudeFixture) (core.ContractLaunchInput, string) {
	t.Helper()
	t.Setenv(envFixture, writeFixture(t, fx))
	ws, work := t.TempDir(), t.TempDir()
	cmd := filepath.Join(newFakeClaudeDir(t), "claude")
	return core.ContractLaunchInput{
		Workspace: ws, WorkDir: work, RunDir: filepath.Join(ws, ".flywheel", "runs", "R-9"),
		Command: []string{cmd, "status", "a;b", "r-42"}, TimeoutSec: 5,
	}, work
}

func TestContractCommand_ArgvHasNoShellAndStdoutIsNormalized(t *testing.T) {
	argvLog := filepath.Join(t.TempDir(), "argv.log")
	cwdLog := filepath.Join(t.TempDir(), "cwd.txt")
	in, work := contractInput(t, fakeClaudeFixture{Stdout: sampleContractStatus, ArgvLogPath: argvLog, CwdLogPath: cwdLog})
	for name, call := range map[string]func(context.Context, core.ContractLaunchInput) (core.ContractLaunchOutput, error){
		"start": NewLauncher().Start, "status": NewLauncher().Status,
	} {
		t.Run(name, func(t *testing.T) {
			out, err := call(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if out.Result != core.RunResultSucceeded || out.Status == nil {
				t.Fatalf("out = %+v", out)
			}
			cost := 12.5
			want := &core.ContractStatus{
				ContractVersion: 1, RunID: "r-42", State: core.ContractStateWaiting, Summary: "承認待ち",
				RequestedAction: &core.ContractRequestedAction{Kind: "approve", Decider: "human", Text: "承認してください"},
				Artifacts:       []core.ContractArtifact{{Kind: "pr", Ref: "https://github.com/o/r/pull/3"}}, CostUSD: &cost,
			}
			if !reflect.DeepEqual(out.Status, want) {
				t.Errorf("status = %+v, want %+v", out.Status, want)
			}
		})
	}
	raw, _ := os.ReadFile(argvLog)
	var argv []string
	if err := json.Unmarshal([]byte(strings.Split(strings.TrimSpace(string(raw)), "\n")[0]), &argv); err != nil {
		t.Fatal(err)
	}
	if want := []string{"status", "a;b", "r-42"}; !reflect.DeepEqual(argv, want) {
		t.Errorf("argv = %v, want %v", argv, want)
	}
	if got, _ := os.ReadFile(cwdLog); !sameDir(t, string(got), work) {
		t.Errorf("cwd = %q, want %q", got, work)
	}
	if _, err := os.Stat(filepath.Join(in.RunDir, "stdout.json")); err != nil {
		t.Errorf("stdout is not saved under the run dir: %v", err)
	}
}

func TestContractCommand_NullActionAndCostAreNil(t *testing.T) {
	in, _ := contractInput(t, fakeClaudeFixture{Stdout: `{"contract_version":1,"run_id":"r","state":"running","summary":"","requested_action":null,"artifacts":[],"cost_usd":null}`})
	out, _ := NewLauncher().Start(context.Background(), in)
	if out.Result != core.RunResultSucceeded || out.Status.RequestedAction != nil || out.Status.CostUSD != nil || len(out.Status.Artifacts) != 0 {
		t.Fatalf("out = %+v status=%+v", out, out.Status)
	}
}

func TestContractCommand_ResultsAreDistinguished(t *testing.T) {
	cases := map[string]struct {
		fx   fakeClaudeFixture
		want core.RunResult
	}{
		"non-zero exit even with JSON": {fakeClaudeFixture{Stdout: sampleContractStatus, ExitCode: 3}, core.RunResultErrored},
		"not json":                     {fakeClaudeFixture{Stdout: "oops"}, core.RunResultMalformed},
		"two objects":                  {fakeClaudeFixture{Stdout: sampleContractStatus + sampleContractStatus}, core.RunResultMalformed},
		"wrong field type":             {fakeClaudeFixture{Stdout: `{"contract_version":1,"run_id":"r","state":"running","cost_usd":"x"}`}, core.RunResultMalformed},
		"timeout":                      {fakeClaudeFixture{Stdout: sampleContractStatus, SleepSeconds: 3}, core.RunResultTimedOut},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			in, _ := contractInput(t, c.fx)
			if name == "timeout" {
				in.TimeoutSec = 1
			}
			out, err := NewLauncher().Status(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if out.Result != c.want || out.Status != nil {
				t.Fatalf("result = %s status=%v, want %s without status", out.Result, out.Status, c.want)
			}
		})
	}
}

func TestContractCommand_MissingExecutableIsLaunchFailed(t *testing.T) {
	in, _ := contractInput(t, fakeClaudeFixture{})
	in.Command = []string{"flywheel-no-such-command-xyz", "start"}
	out, _ := NewLauncher().Start(context.Background(), in)
	if out.Result != core.RunResultLaunchFailed {
		t.Fatalf("result = %s, want launch_failed", out.Result)
	}
	in.Command = nil
	if out, _ := NewLauncher().Start(context.Background(), in); out.Result != core.RunResultLaunchFailed {
		t.Fatalf("empty command result = %s, want launch_failed", out.Result)
	}
}

func TestContractCommand_DoesNotPassTheDelegatedMarker(t *testing.T) {
	t.Setenv(core.DelegatedRunEnvVar, "R-1")
	envLog := filepath.Join(t.TempDir(), "env.txt")
	in, _ := contractInput(t, fakeClaudeFixture{Stdout: sampleContractStatus, EnvLogPath: envLog})
	if _, err := NewLauncher().Start(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(envLog); !strings.HasPrefix(string(got), "present=false") {
		t.Fatalf("env = %q, want the marker absent", got)
	}
}
