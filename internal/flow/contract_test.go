package flow

import (
	"encoding/json"
	"math"
	"path/filepath"
	"testing"

	"github.com/masanami/flywheel/internal/flow/runstate"
	"github.com/masanami/flywheel/internal/flow/workflow"
)

// evlog は畳み込みへ渡す記録を、通し番号と時刻を付けて組み立てる。
type evlog struct {
	t  *testing.T
	ev []runstate.Event
}

func newEvlog(t *testing.T, entry string) *evlog {
	t.Helper()
	// 定義は埋め込みの ticket（analyze・implement は llm、ci は command）を読む。
	def, err := filepath.Abs("assets/files/workflows/ticket.yaml")
	if err != nil {
		t.Fatal(err)
	}
	l := &evlog{t: t}
	l.add(runstate.Event{Type: runstate.EvRunStarted, RunStarted: &runstate.RunStarted{
		RunID: "r1", Workflow: runstate.WorkflowRecord{WorkflowRef: runstate.WorkflowRef{ID: "ticket", Path: def, Hash: defHash(t, def)}},
		Limits: map[string]float64{runstate.BudgetLimitName: 100}, Units: []string{mainUnit}, EntryStep: entry,
	}})
	l.add(runstate.Event{Type: runstate.EvRoundStarted, RoundStarted: &runstate.RoundStarted{Unit: mainUnit, Round: 1, Trigger: runstate.Trigger{Kind: "start"}}})
	return l
}

func defHash(t *testing.T, path string) string {
	t.Helper()
	wf, err := workflow.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return wf.Hash
}

func (l *evlog) add(e runstate.Event) int {
	e.Seq = len(l.ev) + 1
	e.TS = "2026-11-24T00:00:" + string(rune('0'+e.Seq/10%6)) + string(rune('0'+e.Seq%10)) + "Z"
	l.ev = append(l.ev, e)
	return e.Seq
}

func (l *evlog) start(step string, attempt int) int {
	return l.add(runstate.Event{Type: runstate.EvStepStarted, StepStarted: &runstate.StepStarted{Unit: mainUnit, Step: step, Attempt: attempt}})
}

func (l *evlog) finish(f runstate.StepFinished) {
	f.Unit = mainUnit
	l.add(runstate.Event{Type: runstate.EvStepFinished, StepFinished: &f})
}

func (l *evlog) goTo(from, to string) {
	l.add(runstate.Event{Type: runstate.EvTransition, Transition: &runstate.Transition{Unit: mainUnit, From: from, Outcome: "ok", Action: "step", To: to}})
}

func (l *evlog) doc() contractDoc {
	l.t.Helper()
	st, err := runstate.Fold(l.ev)
	if err != nil {
		l.t.Fatal(err)
	}
	return contractFromView(newView(st), "")
}

func fp(v float64) *float64 { return &v }

func TestContractFlowIsTheStartedFlowName(t *testing.T) { // AC-88
	d := newEvlog(t, "analyze").doc()
	if d.Flow == nil || *d.Flow != "ticket" {
		t.Fatalf("flow = %v", d.Flow)
	}
	if d.ContractVersion != 1 {
		t.Errorf("contract_version = %d, want 1", d.ContractVersion)
	}
}

func TestContractStepsListEachStartedExecutionInOrderWithSeqAndKind(t *testing.T) { // AC-89・AC-90・AC-91
	l := newEvlog(t, "analyze")
	s1 := l.start("analyze", 1)
	l.finish(runstate.StepFinished{Step: "analyze", Attempt: 1, Outcome: "ok", CostUSD: fp(0.3)})
	l.goTo("analyze", "implement")
	s2 := l.start("implement", 1)
	l.finish(runstate.StepFinished{Step: "implement", Attempt: 1, Outcome: "pass", CostUSD: fp(4)})
	l.goTo("implement", "ci")
	s3 := l.start("ci", 1)
	steps := *l.doc().Steps
	if len(steps) != 3 {
		t.Fatalf("steps = %+v", steps)
	}
	want := []struct {
		step, kind string
		seq        int
	}{{"analyze", "llm", s1}, {"implement", "llm", s2}, {"ci", "command", s3}}
	for i, w := range want {
		if steps[i].Step != w.step || steps[i].Kind != w.kind || steps[i].Seq != w.seq {
			t.Errorf("steps[%d] = %s/%s/seq %d, want %s/%s/seq %d", i, steps[i].Step, steps[i].Kind, steps[i].Seq, w.step, w.kind, w.seq)
		}
	}
	if steps[0].Seq >= steps[1].Seq || steps[1].Seq >= steps[2].Seq || steps[0].Seq == 1 {
		t.Errorf("seq is not the record's sequence number: %d %d %d", steps[0].Seq, steps[1].Seq, steps[2].Seq)
	}
}

func TestContractFinishedStepCarriesOutcomeAndEndedAt(t *testing.T) { // AC-92・AC-93・AC-94
	l := newEvlog(t, "analyze")
	l.start("analyze", 1)
	l.finish(runstate.StepFinished{Step: "analyze", Attempt: 1, Outcome: "ok", CostUSD: fp(0.3)})
	s := (*l.doc().Steps)[0]
	if s.State != "finished" || s.Outcome == nil || *s.Outcome != "ok" || s.EndedAt == nil || *s.EndedAt != l.ev[len(l.ev)-1].TS {
		t.Errorf("step = %+v", s)
	}
	if s.StartedAt == "" || s.Attempt != 1 {
		t.Errorf("started_at/attempt = %q/%d", s.StartedAt, s.Attempt)
	}
}

func TestContractRunningStepHasNullOutcomeEndedAtAndCost(t *testing.T) { // AC-95
	l := newEvlog(t, "analyze")
	l.start("analyze", 1)
	d := l.doc()
	b, err := json.Marshal((*d.Steps)[0])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["state"] != "running" {
		t.Errorf("state = %v", m["state"])
	}
	for _, k := range []string{"outcome", "ended_at", "cost_usd"} {
		if v, ok := m[k]; !ok || v != nil {
			t.Errorf("%s = %v (present %v), want null", k, v, ok)
		}
	}
}

func TestContractCancelledAndInterruptedStepStates(t *testing.T) { // AC-96
	for _, c := range []struct {
		name string
		f    runstate.StepFinished
		want string
	}{
		{"cancelled", runstate.StepFinished{Step: "analyze", Attempt: 1, Cancelled: true}, "cancelled"},
		{"interrupted", runstate.StepFinished{Step: "analyze", Attempt: 1, Interrupted: true}, "interrupted"},
	} {
		l := newEvlog(t, "analyze")
		l.start("analyze", 1)
		l.finish(c.f)
		s := (*l.doc().Steps)[0]
		if s.State != c.want || s.Outcome != nil || s.EndedAt == nil {
			t.Errorf("%s: step = %+v", c.name, s)
		}
	}
}

func TestContractCostUSDIsTheAmountAddedAndNonLLMIsNull(t *testing.T) { // AC-97・AC-98
	l := newEvlog(t, "analyze")
	l.start("analyze", 1)
	l.finish(runstate.StepFinished{Step: "analyze", Attempt: 1, Outcome: "ok", CostUSD: fp(0.3)})
	l.goTo("analyze", "ci")
	l.start("ci", 1)
	l.finish(runstate.StepFinished{Step: "ci", Attempt: 1, Outcome: "green"})
	steps := *l.doc().Steps
	if steps[0].CostUSD == nil || *steps[0].CostUSD != 0.3 {
		t.Errorf("llm cost_usd = %v, want 0.3", steps[0].CostUSD)
	}
	if steps[1].Kind != "command" || steps[1].CostUSD != nil {
		t.Errorf("command step = %+v, want kind command with null cost_usd", steps[1])
	}
}

func TestContractCostUnknownIsTrueOnlyForAnLLMStepWithoutAReportedCost(t *testing.T) { // AC-99・AC-100
	l := newEvlog(t, "analyze")
	l.start("analyze", 1)
	l.finish(runstate.StepFinished{Step: "analyze", Attempt: 1, Outcome: "ok", CostUSD: fp(0.3)})
	l.goTo("analyze", "implement")
	l.start("implement", 1)
	// 費用を得られなかった実行は、渡した上限額（5）を使ったものとして加える。
	l.finish(runstate.StepFinished{Step: "implement", Attempt: 1, Outcome: "pass", CostUSD: fp(5), CostUnknown: true})
	d := l.doc()
	steps := *d.Steps
	if steps[0].CostUnknown {
		t.Errorf("analyze cost_unknown = true, want false")
	}
	if !steps[1].CostUnknown || steps[1].CostUSD == nil || *steps[1].CostUSD != 5 {
		t.Errorf("implement = %+v, want cost_unknown with the granted limit as cost_usd", steps[1])
	}
}

func TestContractStepCostsSumToTheContractCost(t *testing.T) { // AC-101
	l := newEvlog(t, "analyze")
	l.start("analyze", 1)
	l.finish(runstate.StepFinished{Step: "analyze", Attempt: 1, Outcome: "ok", CostUSD: fp(0.3)})
	l.goTo("analyze", "implement")
	l.start("implement", 1)
	l.finish(runstate.StepFinished{Step: "implement", Attempt: 1, Outcome: "pass", CostUSD: fp(5), CostUnknown: true})
	l.goTo("implement", "ci")
	l.start("ci", 1)
	l.finish(runstate.StepFinished{Step: "ci", Attempt: 1, Outcome: "green"})
	d := l.doc()
	var sum float64
	for _, s := range *d.Steps {
		if s.CostUSD != nil {
			sum += *s.CostUSD
		}
	}
	if d.CostUSD == nil || math.Abs(sum-*d.CostUSD) > 1e-9 || sum == 0 {
		t.Errorf("sum of steps = %v, contract cost_usd = %v", sum, d.CostUSD)
	}
}

func TestContractRepeatedStepIsTwoElements(t *testing.T) { // AC-102
	l := newEvlog(t, "analyze")
	l.start("analyze", 1)
	l.finish(runstate.StepFinished{Step: "analyze", Attempt: 1, Outcome: "ok", CostUSD: fp(0.3)})
	l.goTo("analyze", "analyze")
	l.start("analyze", 2)
	steps := *l.doc().Steps
	if len(steps) != 2 || steps[0].Step != "analyze" || steps[1].Step != "analyze" || steps[0].Seq == steps[1].Seq || steps[1].Attempt != 2 || steps[1].State != "running" {
		t.Errorf("steps = %+v", steps)
	}
}

func TestContractStepsAreEmptyArrayBeforeAnyStep(t *testing.T) {
	l := newEvlog(t, "analyze")
	b, err := json.Marshal(l.doc())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if string(m["steps"]) != "[]" {
		t.Errorf("steps = %s, want []", m["steps"])
	}
	// 失敗の契約（run を読めない）には flow・steps を出さない。
	b, _ = json.Marshal(failedDoc(nil, "x"))
	m = map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"flow", "steps"} {
		if _, ok := m[k]; ok {
			t.Errorf("failed contract has %s: %s", k, b)
		}
	}
}

func TestContractStepsCoverEveryRoundInSeqOrder(t *testing.T) {
	l := newEvlog(t, "analyze")
	s1 := l.start("analyze", 1)
	l.finish(runstate.StepFinished{Step: "analyze", Attempt: 1, Outcome: "ok", CostUSD: fp(0.3)})
	l.goTo("analyze", "implement")
	l.add(runstate.Event{Type: runstate.EvRoundStarted, RoundStarted: &runstate.RoundStarted{Unit: mainUnit, Round: 2, Trigger: runstate.Trigger{Kind: "start"}}})
	s2 := l.start("implement", 1)
	steps := *l.doc().Steps
	if len(steps) != 2 || steps[0].Seq != s1 || steps[1].Seq != s2 {
		t.Errorf("steps = %+v", steps)
	}
}

func TestContractStepKindIsEmptyWhenTheDefinitionHashDiffers(t *testing.T) {
	l := newEvlog(t, "analyze")
	l.ev[0].RunStarted.Workflow.Hash = "other"
	l.start("analyze", 1)
	if k := (*l.doc().Steps)[0].Kind; k != "" {
		t.Errorf("kind = %q", k)
	}
}

func TestContractStepKindIsEmptyWhenTheDefinitionCannotBeRead(t *testing.T) {
	l := newEvlog(t, "analyze")
	l.ev[0].RunStarted.Workflow.Path = filepath.Join(t.TempDir(), "missing.yaml")
	l.start("analyze", 1)
	steps := *l.doc().Steps
	if len(steps) != 1 || steps[0].Kind != "" {
		t.Errorf("steps = %+v", steps)
	}
}

// 実物の流れ（偽の claude で ticket をレビュー待ちまで進めた run）の status に、flow と steps が出る。
func TestStatusOfARealRunListsTheStepsWithTheirKinds(t *testing.T) {
	f := newFixture(t, "5.3.0")
	id := f.runToReview()
	d := f.status(id)
	if d.Flow == nil || *d.Flow != "ticket" || d.Steps == nil || len(*d.Steps) == 0 {
		t.Fatalf("flow/steps = %v / %v", d.Flow, d.Steps)
	}
	byStep := map[string]contractStep{}
	var sum float64
	prev := 0
	for _, s := range *d.Steps {
		byStep[s.Step] = s
		if s.Seq <= prev || s.Kind == "" || s.State != "finished" {
			t.Errorf("step %+v (prev seq %d)", s, prev)
		}
		prev = s.Seq
		if s.CostUSD != nil {
			sum += *s.CostUSD
		}
	}
	if byStep["analyze"].Kind != "llm" || byStep["analyze"].CostUSD == nil || byStep["ci"].Kind != "command" || byStep["ci"].CostUSD != nil {
		t.Errorf("analyze = %+v, ci = %+v", byStep["analyze"], byStep["ci"])
	}
	if d.CostUSD == nil || math.Abs(sum-*d.CostUSD) > 1e-9 {
		t.Errorf("sum %v != cost_usd %v", sum, d.CostUSD)
	}
}
