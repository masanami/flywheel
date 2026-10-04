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

// このファイルは #106（親要件チケット #98 §クリティカル設計決定 7・M3P36）の衝突の予測の口の
// 起動（Launcher.Predict）を、偽の実行ファイルで検証する（AC-297〜300・347〜351 の invoker 側）。
// 偽の実行ファイルは偽の claude と同じもの（宣言の command に絶対パスで置く）。

const samplePrediction = `{
  "schema": "harness.conflict-prediction/v1", "complete": true, "error": null, "head_sha": "abc123",
  "cost_usd": 0.7, "unknown_cost_count": 0,
  "issues": [{"issue": 11, "status": "predicted"}, {"issue": 12, "status": "failed"}],
  "pairs": [
    {"issues": [11, 12], "status": "predicted", "shared_files": [{"path": "a.go", "merge_friendly": true, "ignored": false}], "dependency": {"first": 12}},
    {"issues": [11, 13], "status": "unknown", "shared_files": [], "dependency": null}
  ]
}`

func predictInput(t *testing.T, fx fakeClaudeFixture) (core.PredictLaunchInput, string, string) {
	t.Helper()
	t.Setenv(envFixture, writeFixture(t, fx))
	ws := t.TempDir()
	work := t.TempDir()
	cmd := filepath.Join(newFakeClaudeDir(t), "claude")
	return core.PredictLaunchInput{
		Workspace: ws, WorkDir: work, RunDir: filepath.Join(ws, ".flywheel", "runs", "R-9"),
		Command: []string{cmd, "predict-conflicts", "a;b"}, Issues: []int{11, 12}, MaxBudgetUSD: 4.5, TimeoutSec: 5,
	}, ws, work
}

func TestPredict_ArgvIsCommandPlusBudgetAndIssuesWithoutAShell(t *testing.T) {
	argvLog := filepath.Join(t.TempDir(), "argv.log")
	cwdLog := filepath.Join(t.TempDir(), "cwd.txt")
	in, _, work := predictInput(t, fakeClaudeFixture{Stdout: samplePrediction, ArgvLogPath: argvLog, CwdLogPath: cwdLog})
	out, err := NewLauncher().Predict(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != core.RunResultSucceeded {
		t.Fatalf("result = %s (%s)", out.Result, out.ErrorSummary)
	}
	raw, _ := os.ReadFile(argvLog)
	var argv []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &argv); err != nil {
		t.Fatal(err)
	}
	if want := []string{"predict-conflicts", "a;b", "--max-budget-usd", "4.5", "11", "12"}; !reflect.DeepEqual(argv, want) {
		t.Errorf("argv = %v, want %v", argv, want)
	}
	if got, _ := os.ReadFile(cwdLog); !sameDir(t, string(got), work) {
		t.Errorf("cwd = %q, want %q", got, work)
	}
}

func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

func TestPredict_NormalizesTheReadFields(t *testing.T) {
	in, _, _ := predictInput(t, fakeClaudeFixture{Stdout: samplePrediction})
	out, _ := NewLauncher().Predict(context.Background(), in)
	p := out.Prediction
	if p == nil {
		t.Fatalf("no prediction (%s)", out.ErrorSummary)
	}
	if p.Schema != "harness.conflict-prediction/v1" || !p.Complete || p.ErrorSet || p.HeadSHA != "abc123" ||
		p.CostUSD == nil || *p.CostUSD != 0.7 || p.UnknownCostCount == nil || *p.UnknownCostCount != 0 || p.UnknownCostCountInvalid {
		t.Errorf("prediction = %+v", p)
	}
	wantIssues := []core.PredictedIssue{{Issue: 11, Status: "predicted"}, {Issue: 12, Status: "failed"}}
	if !reflect.DeepEqual(p.Issues, wantIssues) {
		t.Errorf("issues = %+v", p.Issues)
	}
	if len(p.Pairs) != 2 {
		t.Fatalf("pairs = %+v", p.Pairs)
	}
	first := 12
	want0 := core.PredictedPair{Issues: [2]int{11, 12}, Status: "predicted", DependencyFirst: &first,
		SharedFiles: []core.PredictedSharedFile{{Path: "a.go", MergeFriendly: true}}}
	if !reflect.DeepEqual(p.Pairs[0], want0) {
		t.Errorf("pair 0 = %+v, want %+v", p.Pairs[0], want0)
	}
	if p.Pairs[1].Status != "unknown" || p.Pairs[1].DependencyFirst != nil {
		t.Errorf("pair 1 = %+v", p.Pairs[1])
	}
}

func TestPredict_CostFieldsThatAreNotNumbersAreNotTrusted(t *testing.T) {
	cases := map[string]struct {
		body    string
		costNil bool
		invalid bool
		errSet  bool
	}{
		"cost missing":        {`{"schema":"s","unknown_cost_count":0}`, true, false, false},
		"cost string":         {`{"schema":"s","cost_usd":"0.7"}`, true, false, false},
		"count string":        {`{"schema":"s","cost_usd":1,"unknown_cost_count":"0"}`, false, true, false},
		"error is an object":  {`{"schema":"s","cost_usd":1,"error":{"message":"x"}}`, false, false, true},
		"error is a string":   {`{"schema":"s","cost_usd":1,"error":"boom"}`, false, false, true},
		"error explicit null": {`{"schema":"s","cost_usd":1,"error":null}`, false, false, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			in, _, _ := predictInput(t, fakeClaudeFixture{Stdout: tc.body})
			out, _ := NewLauncher().Predict(context.Background(), in)
			p := out.Prediction
			if p == nil {
				t.Fatalf("no prediction (%s)", out.ErrorSummary)
			}
			if (p.CostUSD == nil) != tc.costNil || p.UnknownCostCountInvalid != tc.invalid || p.ErrorSet != tc.errSet {
				t.Errorf("prediction = %+v", p)
			}
		})
	}
}

func TestPredict_ResultClassification(t *testing.T) {
	t.Run("non-zero exit is errored but the readable output is kept", func(t *testing.T) {
		in, _, _ := predictInput(t, fakeClaudeFixture{Stdout: samplePrediction, ExitCode: 3})
		out, _ := NewLauncher().Predict(context.Background(), in)
		if out.Result != core.RunResultErrored || out.Prediction == nil {
			t.Errorf("out = %+v", out)
		}
	})
	t.Run("stdout that is not json is malformed", func(t *testing.T) {
		in, _, _ := predictInput(t, fakeClaudeFixture{Stdout: "not json"})
		out, _ := NewLauncher().Predict(context.Background(), in)
		if out.Result != core.RunResultMalformed || out.Prediction != nil {
			t.Errorf("out = %+v", out)
		}
	})
	t.Run("a pair with the wrong number of issues is malformed", func(t *testing.T) {
		in, _, _ := predictInput(t, fakeClaudeFixture{Stdout: `{"schema":"s","pairs":[{"issues":[1],"status":"predicted"}]}`})
		out, _ := NewLauncher().Predict(context.Background(), in)
		if out.Result != core.RunResultMalformed {
			t.Errorf("out = %+v", out)
		}
	})
	t.Run("time limit", func(t *testing.T) {
		in, _, _ := predictInput(t, fakeClaudeFixture{Stdout: samplePrediction, SleepSeconds: 10})
		in.TimeoutSec = 1
		out, _ := NewLauncher().Predict(context.Background(), in)
		if out.Result != core.RunResultTimedOut {
			t.Errorf("out = %+v", out)
		}
	})
	t.Run("command that cannot start", func(t *testing.T) {
		in, _, _ := predictInput(t, fakeClaudeFixture{Stdout: samplePrediction})
		in.Command = []string{filepath.Join(t.TempDir(), "missing")}
		out, _ := NewLauncher().Predict(context.Background(), in)
		if out.Result != core.RunResultLaunchFailed {
			t.Errorf("out = %+v", out)
		}
	})
	t.Run("empty command", func(t *testing.T) {
		in, _, _ := predictInput(t, fakeClaudeFixture{})
		in.Command = nil
		out, _ := NewLauncher().Predict(context.Background(), in)
		if out.Result != core.RunResultLaunchFailed {
			t.Errorf("out = %+v", out)
		}
	})
}

func TestPredict_SavesTheOutputUnderTheRunDir(t *testing.T) {
	in, _, _ := predictInput(t, fakeClaudeFixture{Stdout: samplePrediction, Stderr: "warn"})
	out, _ := NewLauncher().Predict(context.Background(), in)
	got, err := os.ReadFile(filepath.Join(in.RunDir, "stdout.json"))
	if err != nil || string(got) != samplePrediction {
		t.Errorf("stdout.json = %q, %v", got, err)
	}
	if string(out.RawOutput) != samplePrediction {
		t.Errorf("raw output = %q", out.RawOutput)
	}
	if e, _ := os.ReadFile(filepath.Join(in.RunDir, "stderr.txt")); string(e) != "warn" {
		t.Errorf("stderr.txt = %q", e)
	}
}

func TestBuildPredictArgs_SeparateElementsAndMinimalBudgetFormat(t *testing.T) {
	got := buildPredictArgs([]string{"harness", "predict-conflicts"}, 3, []int{5, 30})
	want := []string{"predict-conflicts", "--max-budget-usd", "3", "5", "30"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}
