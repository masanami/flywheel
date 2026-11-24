package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/flow/runstate"
	"github.com/masanami/flywheel/internal/flow/workflow"
)

// startModels は llm-models を、flow start と同じに定義の値の解決値で始め、最初のゲートまで進める。
func startModels(t *testing.T) (*runstate.State, *Engine) {
	t.Helper()
	scripts := testdata(t, "scripts")
	wf, err := workflow.LoadAndValidate(testdata(t, "workflows", "llm-models.yaml"), workflow.Options{ScriptsDir: scripts})
	if err != nil {
		t.Fatal(err)
	}
	issue, _ := json.Marshal(1)
	e, err := Start(StartParams{RunsDir: t.TempDir(), WF: wf, Inputs: map[string]json.RawMessage{"issue": issue}, ScriptsDir: scripts,
		WorkflowDir: testdata(t, "workflows"), Cwd: t.TempDir(), Origin: "test", Models: workflow.ResolveModels(wf), Efforts: workflow.ResolveEfforts(wf)})
	if err != nil {
		t.Fatal(err)
	}
	e.ClaudeBin = testdata(t, "scripts", "fake-claude.sh")
	e.KillGrace, e.PollInterval = time.Second, 20*time.Millisecond
	st, err := e.Loop(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st, e
}

// claudeResultWithUsage は claudeResult に modelUsage（モデルの名前 → 使用量）を足した結果。names が空なら modelUsage を入れない。
func claudeResultWithUsage(names ...string) string {
	r := claudeResult("pass", 0.1)
	if len(names) == 0 {
		return r
	}
	var parts []string
	for _, n := range names {
		parts = append(parts, fmt.Sprintf(`%q:{"inputTokens":1,"outputTokens":1,"costUSD":0.01}`, n))
	}
	return strings.Replace(r, `"result":"{}"`, `"result":"{}","modelUsage":{`+strings.Join(parts, ",")+`}`, 1)
}

// AC-126・AC-128: 変数を指したステップは既定の値を --model に付け、指さない llm ステップは --model を付けない。
func TestModelVariableDefaultIsPassedAndUnreferencedStepGetsNone(t *testing.T) {
	f := newFake(t)
	f.respond(0, 0, claudeResult("pass", 0.1))
	st, _ := startModels(t)
	if st.Status != runstate.StatusWaiting {
		t.Fatalf("status = %s (%s)", st.Status, st.Reason)
	}
	if _, ok := flag(f.argv(1), "--model"); ok { // plan は変数を指さない
		t.Errorf("plan (no model) got --model: %v", f.argv(1))
	}
	if got, ok := flag(f.argv(2), "--model"); !ok || got != "model-light-default" { // implement は $models.light
		t.Errorf("implement --model = %q (%v), want the default model-light-default; argv %v", got, ok, f.argv(2))
	}
}

// AC-131・AC-94: 続けるコマンド（別プロセスの resume を模して開き直す）の後に起動する claude にも、始めたときの解決値を付ける。
func TestModelVariableGivenValueIsUsedAfterResume(t *testing.T) {
	f := newFake(t)
	f.respond(0, 0, claudeResult("pass", 0.1))
	st, e := startModels(t)
	if st.Status != runstate.StatusWaiting {
		t.Fatalf("status = %s (%s)", st.Status, st.Reason)
	}
	if st.Models["main"] != "model-main-default" || st.Models["light"] != "model-light-default" || st.Efforts["main"] != "high" || st.Efforts["light"] != "low" {
		t.Fatalf("recorded models = %v, efforts = %v", st.Models, st.Efforts)
	}
	mustResolve(t, e, input("respond")) // fix は ゲートの後に起動する
	if f.calls() != 3 {
		t.Fatalf("claude was launched %d times, want 3", f.calls())
	}
	if got, ok := flag(f.argv(3), "--model"); !ok || got != "model-main-default" {
		t.Errorf("fix --model after resume = %q (%v), want model-main-default; argv %v", got, ok, f.argv(3))
	}
	if got, ok := flag(f.argv(3), "--effort"); !ok || got != "high" {
		t.Errorf("fix --effort after resume = %q (%v), want high; argv %v", got, ok, f.argv(3))
	}
}

// AC-136・AC-137: 変数を指したステップの記録は解決した値を持ち、指さないステップは持たない（契約では null）。
func TestModelIsRecordedOnlyForAStepThatReferencesAVariable(t *testing.T) {
	f := newFake(t)
	f.respond(0, 0, claudeResult("pass", 0.1))
	st, _ := startModels(t)
	steps := st.Unit(MainUnit).Rounds[0].Steps
	if len(steps) != 2 || steps[0].Step != "plan" || steps[1].Step != "implement" {
		t.Fatalf("steps = %+v", steps)
	}
	if steps[0].Model != "" || steps[1].Model != "model-light-default" {
		t.Errorf("recorded model: plan %q, implement %q", steps[0].Model, steps[1].Model)
	}
}

// AC-133〜AC-135: claude の結果の modelUsage の名前を、使ったモデルとして記録する（無ければ空）。
func TestModelsUsedComeFromTheModelUsageNames(t *testing.T) {
	f := newFake(t)
	f.respond(1, 0, claudeResultWithUsage("claude-b", "claude-a")) // plan: 2 つ（辞書順に記録）
	f.respond(2, 0, claudeResultWithUsage("claude-x"))             // implement: 1 つ
	st, _ := startModels(t)
	steps := st.Unit(MainUnit).Rounds[0].Steps
	if got := strings.Join(steps[0].ModelsUsed, ","); got != "claude-a,claude-b" {
		t.Errorf("plan models_used = %q, want claude-a,claude-b", got)
	}
	if got := strings.Join(steps[1].ModelsUsed, ","); got != "claude-x" {
		t.Errorf("implement models_used = %q, want claude-x", got)
	}

	f2 := newFake(t)
	f2.respond(0, 0, claudeResult("pass", 0.1)) // modelUsage が無い
	st, _ = startModels(t)
	for _, x := range st.Unit(MainUnit).Rounds[0].Steps {
		if len(x.ModelsUsed) != 0 {
			t.Errorf("%s models_used = %v, want none", x.Step, x.ModelsUsed)
		}
	}
}

// 解決値は run_started に記録する。変数の無い定義の run には記録しない。
func TestResolvedModelsAreRecordedInRunStarted(t *testing.T) {
	f := newFake(t)
	f.respond(0, 0, claudeResult("pass", 0.1))
	_, e := startModels(t)
	var got map[string]string
	for _, ev := range events(t, e) {
		if ev.RunStarted != nil {
			got = ev.RunStarted.Models
		}
	}
	if len(got) != 2 || got["main"] != "model-main-default" || got["light"] != "model-light-default" {
		t.Errorf("run_started.models = %v", got)
	}
	st, _ := start(t, "llm", map[string]any{"issue": 1})
	if st.Models != nil {
		t.Errorf("a definition without models recorded %v", st.Models)
	}
}

// AC-92・AC-93: effort の変数を指したステップは --effort <値> を付け、指さない llm ステップは --effort を付けない。
func TestEffortVariableIsPassedAndUnreferencedStepGetsNone(t *testing.T) {
	f := newFake(t)
	f.respond(0, 0, claudeResult("pass", 0.1))
	st, _ := startModels(t)
	if st.Status != runstate.StatusWaiting {
		t.Fatalf("status = %s (%s)", st.Status, st.Reason)
	}
	if _, ok := flag(f.argv(1), "--effort"); ok { // plan は変数を指さない
		t.Errorf("plan (no effort) got --effort: %v", f.argv(1))
	}
	if got, ok := flag(f.argv(2), "--effort"); !ok || got != "low" { // implement は $efforts.light
		t.Errorf("implement --effort = %q (%v), want low; argv %v", got, ok, f.argv(2))
	}
}

// AC-97・AC-98: 変数を指したステップの記録は解決した effort を持ち、指さないステップは持たない。
func TestEffortIsRecordedOnlyForAStepThatReferencesAVariable(t *testing.T) {
	f := newFake(t)
	f.respond(0, 0, claudeResult("pass", 0.1))
	st, _ := startModels(t)
	steps := st.Unit(MainUnit).Rounds[0].Steps
	if len(steps) != 2 || steps[0].Effort != "" || steps[1].Effort != "low" {
		t.Errorf("recorded effort: %+v", steps)
	}
}

// 解決した effort の変数は run_started に記録する。変数の無い定義の run には記録しない。
func TestResolvedEffortsAreRecordedInRunStarted(t *testing.T) {
	f := newFake(t)
	f.respond(0, 0, claudeResult("pass", 0.1))
	_, e := startModels(t)
	var got map[string]string
	for _, ev := range events(t, e) {
		if ev.RunStarted != nil {
			got = ev.RunStarted.Efforts
		}
	}
	if len(got) != 2 || got["main"] != "high" || got["light"] != "low" {
		t.Errorf("run_started.efforts = %v", got)
	}
	st, _ := start(t, "llm", map[string]any{"issue": 1})
	if st.Efforts != nil {
		t.Errorf("a definition without efforts recorded %v", st.Efforts)
	}
}
