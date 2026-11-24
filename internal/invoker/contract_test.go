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
		"resume": NewLauncher().Resume, "cancel": NewLauncher().Cancel,
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

// resume・cancel の標準出力も同じ形に正規化され、requested_action の options は文字列の配列として読む。
func TestContractCommand_RequestedActionOptionsAreNormalized(t *testing.T) {
	in, _ := contractInput(t, fakeClaudeFixture{Stdout: `{"contract_version": 1, "run_id": "r-1", "state": "waiting", "summary": "s",
	  "requested_action": {"kind": "answer", "decider": "parent", "text": "t", "options": ["A", "B"]}, "artifacts": [], "cost_usd": null}`})
	out, err := NewLauncher().Resume(context.Background(), in)
	if err != nil || out.Status == nil || out.Status.RequestedAction == nil {
		t.Fatalf("out = %+v err = %v", out, err)
	}
	if want := []string{"A", "B"}; !reflect.DeepEqual(out.Status.RequestedAction.Options, want) {
		t.Fatalf("options = %q, want %q", out.Status.RequestedAction.Options, want)
	}
}

// requested_action.options に文字列でない要素（数値・オブジェクト）が 1 つでもあれば、契約の出力の誤り（malformed）にし、状態を返さない。
func TestContractCommand_NonStringOptionsAreMalformed(t *testing.T) {
	for name, opts := range map[string]string{"number": `["A", 1]`, "object": `["A", {"id": "b"}]`} {
		t.Run(name, func(t *testing.T) {
			in, _ := contractInput(t, fakeClaudeFixture{Stdout: `{"contract_version": 1, "run_id": "r-1", "state": "waiting", "summary": "s",
			  "requested_action": {"kind": "answer", "decider": "human", "text": "t", "options": ` + opts + `}, "artifacts": [], "cost_usd": null}`})
			out, err := NewLauncher().Status(context.Background(), in)
			if err != nil || out.Result != core.RunResultMalformed || out.Status != nil {
				t.Fatalf("out = %+v err = %v, want malformed without a status", out, err)
			}
		})
	}
}

// requested_action.resolvable_by は任意。無ければ空、文字列はそのまま、文字列でない値・空文字は閉集合の外として "invalid"。
func TestContractCommand_ResolvableByIsNormalized(t *testing.T) {
	cases := map[string]string{
		``:                            "",
		`,"resolvable_by":null`:       "",
		`,"resolvable_by":"answer"`:   "answer",
		`,"resolvable_by":"terminal"`: "terminal",
		`,"resolvable_by":"other"`:    "other",
		`,"resolvable_by":7`:          "invalid",
		`,"resolvable_by":""`:         "invalid",
	}
	for field, want := range cases {
		in, _ := contractInput(t, fakeClaudeFixture{Stdout: `{"contract_version":1,"run_id":"r","state":"waiting","summary":"","requested_action":{"kind":"approve","decider":"human","text":"t"` + field + `},"artifacts":[],"cost_usd":null}`})
		out, _ := NewLauncher().Status(context.Background(), in)
		if out.Result != core.RunResultSucceeded || out.Status == nil || out.Status.RequestedAction == nil {
			t.Fatalf("field %q: out = %+v", field, out)
		}
		if got := out.Status.RequestedAction.ResolvableBy; got != want {
			t.Errorf("field %q: ResolvableBy = %q, want %q", field, got, want)
		}
	}
}

func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// artifacts の role は任意の文字列。無ければ空、あれば値を写す。
func TestContractCommand_ArtifactRoleIsNormalized(t *testing.T) {
	in, _ := contractInput(t, fakeClaudeFixture{Stdout: `{"contract_version": 1, "run_id": "r-1", "state": "succeeded", "summary": "s",
	  "requested_action": null, "artifacts": [{"kind": "commit", "ref": "a1"}, {"kind": "commit", "ref": "b2", "role": "integration"}], "cost_usd": null}`})
	out, err := NewLauncher().Status(context.Background(), in)
	if err != nil || out.Status == nil {
		t.Fatalf("out = %+v err = %v", out, err)
	}
	want := []core.ContractArtifact{{Kind: "commit", Ref: "a1"}, {Kind: "commit", Ref: "b2", Role: "integration"}}
	if !reflect.DeepEqual(out.Status.Artifacts, want) {
		t.Fatalf("artifacts = %+v, want %+v", out.Status.Artifacts, want)
	}
}

// 成果物の role が文字列でなければ、契約の出力の誤り（malformed）にし、状態を返さない。
func TestContractCommand_NonStringArtifactRoleIsMalformed(t *testing.T) {
	for _, role := range []string{`3`, `null`, `["integration"]`} {
		in, _ := contractInput(t, fakeClaudeFixture{Stdout: `{"contract_version": 1, "run_id": "r-1", "state": "succeeded", "summary": "s",
	  "requested_action": null, "artifacts": [{"kind": "commit", "ref": "a1", "role": ` + role + `}], "cost_usd": null}`})
		out, err := NewLauncher().Status(context.Background(), in)
		if err != nil || out.Result != core.RunResultMalformed || out.Status != nil {
			t.Fatalf("role %s: out = %+v err = %v, want malformed without a status", role, out, err)
		}
	}
}

// facts は名前から文字列への対応のときだけ読む。形が合わない facts は無いものとして扱い、出力の全体は malformed にしない。
func TestContractCommand_FactsAreReadOnlyWhenTheShapeFits(t *testing.T) {
	cases := map[string]struct {
		facts string
		want  map[string]string
	}{
		"map":        {`"facts": {"tests": "pass"},`, map[string]string{"tests": "pass"}},
		"absent":     {``, nil},
		"null":       {`"facts": null,`, nil},
		"not_a_map":  {`"facts": ["a"],`, nil},
		"non_string": {`"facts": {"a": "x", "b": 1},`, nil},
		"null_value": {`"facts": {"a": "x", "b": null},`, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			facts := c.facts
			in, _ := contractInput(t, fakeClaudeFixture{Stdout: `{"contract_version": 1, "run_id": "r-1", "state": "running", "summary": "s",` + facts + `
			  "requested_action": null, "artifacts": [], "cost_usd": null}`})
			out, err := NewLauncher().Status(context.Background(), in)
			if err != nil || out.Result == core.RunResultMalformed || out.Status == nil {
				t.Fatalf("out = %+v err = %v, want a status", out, err)
			}
			if !reflect.DeepEqual(out.Status.Facts, c.want) {
				t.Fatalf("facts = %v, want %v", out.Status.Facts, c.want)
			}
		})
	}
}

const stepJSON = `{"seq": 1, "step": "analyze", "kind": "agent", "attempt": 1, "state": "finished", "outcome": "ok", "started_at": "2026-01-01T00:00:00Z",
  "ended_at": "2026-01-01T00:01:00Z", "cost_usd": 0.5, "cost_unknown": false, "model": null, "models_used": ["m1"]}`

func statusWithSteps(t *testing.T, extra string) contractReadForTest {
	t.Helper()
	in, _ := contractInput(t, fakeClaudeFixture{Stdout: `{"contract_version": 1, "run_id": "r-1", "state": "running", "summary": "s",
	  "requested_action": null, "artifacts": [], "cost_usd": null, "facts": {"a": "b"}, ` + extra + `}`})
	out, err := NewLauncher().Status(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return contractReadForTest{Result: out.Result, Status: out.Status}
}

// flow・steps は任意。在れば写し、欄の無い出力は従来どおり読む。
func TestContractCommand_FlowAndStepsAreNormalized(t *testing.T) {
	got := statusWithSteps(t, `"flow": "ticket", "steps": [`+stepJSON+`]`)
	if got.Result != core.RunResultSucceeded || got.Status == nil || got.Status.Flow == nil || *got.Status.Flow != "ticket" || len(got.Status.Steps) != 1 {
		t.Fatalf("got = %+v", got)
	}
	sp := got.Status.Steps[0]
	if sp.Seq != 1 || sp.Step != "analyze" || sp.State != "finished" || *sp.Outcome != "ok" || sp.Model != nil || *sp.CostUSD != 0.5 || len(sp.ModelsUsed) != 1 {
		t.Errorf("step = %+v", sp)
	}
	none := statusWithSteps(t, `"x": 1`)
	if none.Status == nil || none.Status.Steps != nil || none.Status.Flow != nil {
		t.Errorf("without the fields: %+v", none.Status)
	}
	empty := statusWithSteps(t, `"steps": []`)
	if empty.Status == nil || empty.Status.Steps == nil || len(empty.Status.Steps) != 0 {
		t.Errorf("empty array must stay present: %+v", empty.Status)
	}
}

// steps の要素の effort は任意。文字列ならそのまま、無い・null なら nil、それ以外なら steps の全体を無いものとして扱う。
func TestContractCommand_StepEffort(t *testing.T) {
	withEffort := func(v string) string {
		return strings.Replace(stepJSON, `"models_used"`, `"effort": `+v+`, "models_used"`, 1)
	}
	if got := statusWithSteps(t, `"steps": [`+withEffort(`"medium"`)+`]`); got.Status == nil || len(got.Status.Steps) != 1 || got.Status.Steps[0].Effort == nil || *got.Status.Steps[0].Effort != "medium" {
		t.Errorf("effort string: %+v", got.Status)
	}
	for name, extra := range map[string]string{"absent": `"steps": [` + stepJSON + `]`, "null": `"steps": [` + withEffort(`null`) + `]`} {
		got := statusWithSteps(t, extra)
		if got.Status == nil || len(got.Status.Steps) != 1 || got.Status.Steps[0].Effort != nil {
			t.Errorf("%s: %+v", name, got.Status)
		}
	}
	for name, v := range map[string]string{"number": `3`, "bool": `true`, "array": `["a"]`, "object": `{}`} {
		got := statusWithSteps(t, `"flow": "ticket", "steps": [`+withEffort(v)+`]`)
		if got.Result != core.RunResultSucceeded || got.Status == nil || got.Status.Steps != nil || got.Status.RunID != "r-1" {
			t.Errorf("%s: want steps dropped but a normal read, got %v %+v", name, got.Result, got.Status)
		}
	}
}

// steps の形が合わないとき（AC-111）、steps の全体を無いものとして扱い、出力は malformed にせず、ほかの欄は従来どおり読む。
func TestContractCommand_MalformedStepsAreDroppedWithoutMalformedOutput(t *testing.T) {
	bad := map[string]string{
		"not an array":     `"steps": {"seq": 1}`,
		"missing field":    `"steps": [` + strings.Replace(stepJSON, `"kind": "agent", `, "", 1) + `]`,
		"wrong type":       `"steps": [` + strings.Replace(stepJSON, `"step": "analyze"`, `"step": 3`, 1) + `]`,
		"state outside":    `"steps": [` + strings.Replace(stepJSON, `"finished"`, `"done"`, 1) + `]`,
		"seq below 1":      `"steps": [` + strings.Replace(stepJSON, `"seq": 1`, `"seq": 0`, 1) + `]`,
		"seq not integer":  `"steps": [` + strings.Replace(stepJSON, `"seq": 1`, `"seq": 1.5`, 1) + `]`,
		"seq duplicated":   `"steps": [` + stepJSON + `,` + stepJSON + `]`,
		"attempt below 1":  `"steps": [` + strings.Replace(stepJSON, `"attempt": 1`, `"attempt": 0`, 1) + `]`,
		"one bad of two":   `"steps": [` + stepJSON + `,` + strings.Replace(strings.Replace(stepJSON, `"seq": 1`, `"seq": 2`, 1), `"finished"`, `"done"`, 1) + `]`,
		"models not array": `"steps": [` + strings.Replace(stepJSON, `["m1"]`, `"m1"`, 1) + `]`,
	}
	for name, extra := range bad {
		t.Run(name, func(t *testing.T) {
			got := statusWithSteps(t, `"flow": "ticket", `+extra)
			if got.Result != core.RunResultSucceeded || got.Status == nil {
				t.Fatalf("result = %v status = %+v, want a normal read", got.Result, got.Status)
			}
			if got.Status.Steps != nil {
				t.Errorf("steps = %+v, want none", got.Status.Steps)
			}
			if got.Status.Facts["a"] != "b" || got.Status.RunID != "r-1" || got.Status.Flow == nil {
				t.Errorf("other fields were not read as before: %+v", got.Status)
			}
		})
	}
}

type contractReadForTest struct {
	Result core.RunResult
	Status *core.ContractStatus
}
