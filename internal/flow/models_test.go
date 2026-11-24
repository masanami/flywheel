package flow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/flow/runstate"
)

// AC-130: --model の形が <変数>=<値> でない（= が無い・値が空）とき誤りになる。
func TestParseModelFlags(t *testing.T) {
	got, err := ParseModelFlags([]string{"main=a", "light=b=c"})
	if err != nil || got["main"] != "a" || got["light"] != "b=c" || len(got) != 2 {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := ParseModelFlags(nil); err != nil || got != nil {
		t.Errorf("no flags: %v, %v", got, err)
	}
	for _, raw := range [][]string{{"main"}, {"main="}, {"=x"}, {""}, {"main=a", "main=b"}} {
		if _, err := ParseModelFlags(raw); err == nil {
			t.Errorf("%q was accepted", raw)
		}
	}
}

// AC-129: 埋め込んだフローが定義していない変数は、始める前の確かめで誤りになる（ticket・reflect は models を持たない）。
func TestCheckModelFlags(t *testing.T) {
	for _, name := range []string{"ticket", "reflect"} {
		if err := CheckModelFlags(name, []string{"main=x"}); err == nil || !strings.Contains(err.Error(), `"main" is not defined`) {
			t.Errorf("%s: err = %v", name, err)
		}
		if err := CheckModelFlags(name, []string{"main"}); err == nil {
			t.Errorf("%s: a malformed --model was accepted", name)
		}
		if err := CheckModelFlags(name, nil); err != nil {
			t.Errorf("%s: no --model: %v", name, err)
		}
	}
	if err := CheckModelFlags("no-such-flow", []string{"main=x"}); err != nil {
		t.Errorf("a flow that is not embedded is reported by Start, not here: %v", err)
	}
}

// AC-136・AC-137: 変数を指したステップの steps の要素は model に解決値を持ち、指さないステップは null。
// AC-133〜AC-135: models_used は modelUsage の名前（1 つ・2 つ・無ければ空の配列）。
func TestContractStepsCarryModelAndModelsUsed(t *testing.T) {
	l := newEvlog(t, "analyze")
	l.add(runstate.Event{Type: runstate.EvStepStarted, StepStarted: &runstate.StepStarted{Unit: mainUnit, Step: "analyze", Attempt: 1, Model: "model-light"}})
	l.finish(runstate.StepFinished{Step: "analyze", Attempt: 1, Outcome: "ok", CostUSD: fp(0.1), ModelsUsed: []string{"claude-a"}})
	l.goTo("analyze", "workspace")
	l.start("workspace", 1)
	l.finish(runstate.StepFinished{Step: "workspace", Attempt: 1, Outcome: "ok"})
	l.goTo("workspace", "implement")
	l.add(runstate.Event{Type: runstate.EvStepStarted, StepStarted: &runstate.StepStarted{Unit: mainUnit, Step: "implement", Attempt: 1, Model: "model-main"}})
	l.finish(runstate.StepFinished{Step: "implement", Attempt: 1, Outcome: "pass", CostUSD: fp(0.2), ModelsUsed: []string{"claude-a", "claude-b"}})
	l.goTo("implement", "ci")
	l.start("ci", 1)
	steps := *l.doc().Steps
	if len(steps) != 4 {
		t.Fatalf("steps = %+v", steps)
	}
	raw, err := json.Marshal(steps)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	for i, c := range []struct {
		model any
		used  []any
	}{
		{"model-light", []any{"claude-a"}},
		{nil, []any{}},
		{"model-main", []any{"claude-a", "claude-b"}},
		{nil, []any{}},
	} {
		m, used := out[i]["model"], out[i]["models_used"]
		if v, ok := out[i]["model"]; !ok || v != c.model {
			t.Errorf("step %d model = %v (present %v), want %v", i, m, ok, c.model)
		}
		u, isList := used.([]any)
		if !isList || len(u) != len(c.used) {
			t.Fatalf("step %d models_used = %#v, want %v", i, used, c.used)
		}
		for j := range u {
			if u[j] != c.used[j] {
				t.Errorf("step %d models_used = %v, want %v", i, u, c.used)
			}
		}
	}
}
