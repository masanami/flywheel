package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// このファイルは判断点 J9（2 つの課題を同時に進めてよいかの判断。docs/features/m3-invoker-delegation.md
// §同時に進めてよいかの判断）を、実ストア・偽の予測の口・偽の判断の IF で検証する。

// j9Invoker は J9 の呼び出しだけを respond で偽装し、それ以外（J3）は inner に渡す。
type j9Invoker struct {
	inner   JudgmentInvoker
	respond func(in JudgmentLaunchInput, n int) JudgmentLaunchOutput
	mu      sync.Mutex
	calls   []JudgmentLaunchInput
}

func (j *j9Invoker) Available(ctx context.Context) error { return j.inner.Available(ctx) }

func (j *j9Invoker) InvokeJudgment(ctx context.Context, in JudgmentLaunchInput) (JudgmentLaunchOutput, error) {
	if in.Judgment != JudgmentJ9 {
		return j.inner.InvokeJudgment(ctx, in)
	}
	j.mu.Lock()
	j.calls = append(j.calls, in)
	n := len(j.calls)
	j.mu.Unlock()
	return j.respond(in, n), nil
}

func (j *j9Invoker) called() []JudgmentLaunchInput {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]JudgmentLaunchInput(nil), j.calls...)
}

func j9Out(verdict, reason string, cost float64) JudgmentLaunchOutput {
	b, _ := json.Marshal(map[string]string{"verdict": verdict, "reason": reason})
	return JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: b, ReportedTotalCostUSD: float64Ptr(cost)}
}

// serialJudgmentFixture は planFixture に J9 の偽の呼び出し口を足し、指定のリポジトリの serial_judgment を真にする。
func serialJudgmentFixture(t *testing.T, repos ...string) (*planFixture, *j9Invoker) {
	t.Helper()
	f := newPlanFixture(t)
	for i := range f.conn.Repos {
		for _, r := range repos {
			if f.conn.Repos[i].Name == r {
				f.conn.Repos[i].SerialJudgment = true
			}
		}
	}
	// 3 つの候補の全部の組を判断できる数にする。
	f.agent.MaxParallelRuns = 3
	inv := &j9Invoker{inner: f.j3, respond: func(JudgmentLaunchInput, int) JudgmentLaunchOutput { return j9Out("weak", "独立", 0.1) }}
	return f, inv
}

func (f *planFixture) runPlanWith(t *testing.T, inv JudgmentInvoker) *DelegateResult {
	t.Helper()
	in := f.in(f.cycle(t, 300), nil)
	in.Judgment = inv
	res, err := f.s.RunDelegation(context.Background(), in)
	if err != nil {
		t.Fatalf("RunDelegation: %v", err)
	}
	return res
}

func (f *planFixture) j9Runs(t *testing.T) []runRow {
	t.Helper()
	var out []runRow
	err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id FROM run WHERE kind = 'judgment' AND judgment = 'J9' ORDER BY id`)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		_ = rows.Close()
		for _, id := range ids {
			r, err := loadRunByID(context.Background(), tx, id)
			if err != nil {
				return err
			}
			out = append(out, *r)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sharedPair() PredictedPair { return pairOf(11, 12, shared("a.go")) }

func TestJ9_Declared_Weak_Parallel_RecordsTheJudgment(t *testing.T) {
	t.Parallel()
	f, inv := serialJudgmentFixture(t, "multi")
	a := f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput { return predictionFor(in, nil, sharedPair()) }
	res := f.runPlanWith(t, inv)
	if want := [][]string{{a}, {b}}; !reflect.DeepEqual(groupsOf(res), want) {
		t.Fatalf("groups = %v, want %v", groupsOf(res), want)
	}
	calls := inv.called()
	if len(calls) != 1 {
		t.Fatalf("J9 calls = %d, want 1", len(calls))
	}
	if calls[0].MaxBudgetUSD != 0.5 {
		t.Errorf("per-pair budget = %v, want 0.5", calls[0].MaxBudgetUSD)
	}
	var joined string
	for _, s := range calls[0].Sections {
		joined += s.Label + "\n" + s.Content + "\n"
	}
	for _, want := range []string{a, b, "a.go"} {
		if !strings.Contains(joined, want) {
			t.Errorf("J9 input lacks %q: %s", want, joined)
		}
	}
	for _, g := range res.SerialGroups {
		if len(g.Judgments) != 1 || g.Judgments[0].Outcome != PairOutcomeWeak || g.Judgments[0].Reason != "独立" ||
			g.Judgments[0].Challenges != [2]string{a, b} {
			t.Errorf("judgments = %+v", g.Judgments)
		}
		if len(g.Reasons) != 0 {
			t.Errorf("reasons = %v, want none", g.Reasons)
		}
	}
	runs := f.j9Runs(t)
	if len(runs) != 1 || runs[0].ChallengeID != "" || runs[0].Result != RunResultSucceeded {
		t.Errorf("J9 runs = %+v", runs)
	}
}

func TestJ9_Declared_StrongAndUncertain_Serial(t *testing.T) {
	t.Parallel()
	for verdict, reason := range map[string]SerialGroupReason{"strong": SerialReasonJudgedStrong, "uncertain": SerialReasonJudgedUndetermined} {
		verdict, reason := verdict, reason
		t.Run(verdict, func(t *testing.T) {
			f, inv := serialJudgmentFixture(t, "multi")
			a := f.candidate(t, "multi", "P1", 11)
			b := f.candidate(t, "multi", "P1", 12)
			f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput { return predictionFor(in, nil, sharedPair()) }
			inv.respond = func(JudgmentLaunchInput, int) JudgmentLaunchOutput { return j9Out(verdict, "理由", 0.1) }
			res := f.runPlanWith(t, inv)
			if want := [][]string{{a, b}}; !reflect.DeepEqual(groupsOf(res), want) {
				t.Fatalf("groups = %v, want %v", groupsOf(res), want)
			}
			if want := []SerialGroupReason{SerialReasonSharedFiles, reason}; !reflect.DeepEqual(res.SerialGroups[0].Reasons, want) {
				t.Errorf("reasons = %v, want %v", res.SerialGroups[0].Reasons, want)
			}
			if j := res.SerialGroups[0].Judgments; len(j) != 1 || string(j[0].Outcome) != verdict {
				t.Errorf("judgments = %+v", j)
			}
		})
	}
}

func TestJ9_FailuresAndBudgetAreSerial(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		out    JudgmentLaunchOutput
		reason SerialGroupReason
		kind   PairOutcome
	}{
		"errored":          {JudgmentLaunchOutput{Result: RunResultErrored, ErrorSummary: "x"}, SerialReasonJudgmentFailed, PairOutcomeFailed},
		"invalid output":   {JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{"verdict":"maybe","reason":"r"}`)}, SerialReasonJudgmentFailed, PairOutcomeFailed},
		"empty reason":     {JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{"verdict":"weak","reason":"  "}`)}, SerialReasonJudgmentFailed, PairOutcomeFailed},
		"budget exhausted": {JudgmentLaunchOutput{Result: RunResultBudgetExhausted, ReportedTotalCostUSD: float64Ptr(1)}, SerialReasonJudgmentBudget, PairOutcomeBudget},
	}
	for name, c := range cases {
		c := c
		t.Run(name, func(t *testing.T) {
			f, inv := serialJudgmentFixture(t, "multi")
			a := f.candidate(t, "multi", "P1", 11)
			b := f.candidate(t, "multi", "P1", 12)
			f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput { return predictionFor(in, nil, sharedPair()) }
			inv.respond = func(JudgmentLaunchInput, int) JudgmentLaunchOutput { return c.out }
			res := f.runPlanWith(t, inv)
			if want := [][]string{{a, b}}; !reflect.DeepEqual(groupsOf(res), want) {
				t.Fatalf("groups = %v, want %v", groupsOf(res), want)
			}
			g := res.SerialGroups[0]
			if !hasReason(g.Reasons, c.reason) || len(g.Judgments) != 1 || g.Judgments[0].Outcome != c.kind {
				t.Errorf("group = %+v", g)
			}
		})
	}
}

func TestJ9_DisabledRepo_NeverCallsTheJudgment_AndKeepsTheOldBehaviour(t *testing.T) {
	t.Parallel()
	f, inv := serialJudgmentFixture(t) // どのリポジトリも有効にしない
	a := f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	p := f.candidate(t, "plain", "P1", 21)
	q := f.candidate(t, "plain", "P1", 22)
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		return predictionFor(in, nil, sharedPair(), PredictedPair{Issues: [2]int{11, 12}, Status: predictPairPredicted,
			SharedFiles: []PredictedSharedFile{{Path: "a.go"}}, DependencyStated: []PredictedDependEdge{{11, 12}, {12, 11}}})
	}
	res := f.runPlanWith(t, inv)
	if len(inv.called()) != 0 {
		t.Fatalf("J9 was called %d times for a disabled repo", len(inv.called()))
	}
	if want := [][]string{{a, b}, {p, q}}; !reflect.DeepEqual(groupsOf(res), want) {
		t.Errorf("groups = %v, want %v", groupsOf(res), want)
	}
}

func TestJ9_FirstDependency_IsNotJudged_AndStaysSerialInOrder(t *testing.T) {
	t.Parallel()
	f, inv := serialJudgmentFixture(t, "multi")
	a := f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	first := 12
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		return predictionFor(in, nil, PredictedPair{Issues: [2]int{11, 12}, Status: predictPairPredicted,
			SharedFiles: []PredictedSharedFile{{Path: "a.go"}}, DependencyFirst: &first, DependencyStated: []PredictedDependEdge{{11, 12}}})
	}
	res := f.runPlanWith(t, inv)
	if len(inv.called()) != 0 {
		t.Fatalf("J9 was called for a pair with dependency.first")
	}
	if want := [][]string{{b, a}}; !reflect.DeepEqual(groupsOf(res), want) {
		t.Errorf("groups = %v, want %v", groupsOf(res), want)
	}
	if !hasReason(res.SerialGroups[0].Reasons, SerialReasonDependency) {
		t.Errorf("reasons = %v", res.SerialGroups[0].Reasons)
	}
}

func TestJ9_MutualDependency_AlwaysSerial_MergeCandidate_NoQuestion(t *testing.T) {
	t.Parallel()
	f, inv := serialJudgmentFixture(t, "multi")
	a := f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	// 共有ファイルが無く、互いに前提にし合う組。J9 が weak を返しても直列。
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		return predictionFor(in, nil, PredictedPair{Issues: [2]int{11, 12}, Status: predictPairPredicted,
			DependencyStated: []PredictedDependEdge{{11, 12}, {12, 11}}})
	}
	res := f.runPlanWith(t, inv)
	if want := [][]string{{a, b}}; !reflect.DeepEqual(groupsOf(res), want) {
		t.Fatalf("groups = %v, want %v", groupsOf(res), want)
	}
	g := res.SerialGroups[0]
	if !reflect.DeepEqual(g.Reasons, []SerialGroupReason{SerialReasonMergeCandidate}) {
		t.Errorf("reasons = %v, want [merge_candidate]", g.Reasons)
	}
	if len(g.Judgments) != 1 || g.Judgments[0].Outcome != PairOutcomeWeak {
		t.Errorf("judgments = %+v", g.Judgments)
	}
	if len(res.NotStarted) != 0 {
		t.Errorf("not_started = %v, want none (no hold, the cycle must not stop)", res.NotStarted)
	}
}

func TestJ9_MutualDependency_WithoutJudgmentEnabled_IsNotSerialized(t *testing.T) {
	t.Parallel()
	f, inv := serialJudgmentFixture(t)
	a := f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		return predictionFor(in, nil, PredictedPair{Issues: [2]int{11, 12}, Status: predictPairPredicted,
			DependencyStated: []PredictedDependEdge{{11, 12}, {12, 11}}})
	}
	res := f.runPlanWith(t, inv)
	if want := [][]string{{a}, {b}}; !reflect.DeepEqual(groupsOf(res), want) {
		t.Errorf("groups = %v, want %v (default off keeps today's behaviour)", groupsOf(res), want)
	}
}

func TestJ9_Undeclared_JudgesPairsOfTheCandidates(t *testing.T) {
	t.Parallel()
	f, inv := serialJudgmentFixture(t, "plain")
	a := f.candidate(t, "plain", "P1", 21)
	b := f.candidate(t, "plain", "P1", 22)
	res := f.runPlanWith(t, inv)
	if len(f.pred.called()) != 0 {
		t.Error("the prediction must not be called without a declaration")
	}
	if want := [][]string{{a}, {b}}; !reflect.DeepEqual(groupsOf(res), want) {
		t.Fatalf("weak: groups = %v, want %v", groupsOf(res), want)
	}

	f2, inv2 := serialJudgmentFixture(t, "plain")
	a2 := f2.candidate(t, "plain", "P1", 21)
	b2 := f2.candidate(t, "plain", "P1", 22)
	inv2.respond = func(JudgmentLaunchInput, int) JudgmentLaunchOutput {
		return j9Out("strong", "同じ規則を決め直す", 0.1)
	}
	res2 := f2.runPlanWith(t, inv2)
	if want := [][]string{{a2, b2}}; !reflect.DeepEqual(groupsOf(res2), want) {
		t.Fatalf("strong: groups = %v, want %v", groupsOf(res2), want)
	}
	if want := []SerialGroupReason{SerialReasonNoPredictionDeclare, SerialReasonJudgedStrong}; !reflect.DeepEqual(res2.SerialGroups[0].Reasons, want) {
		t.Errorf("reasons = %v, want %v", res2.SerialGroups[0].Reasons, want)
	}
}

func TestJ9_Undeclared_OnlyCandidatesUpToTheConcurrencyLimitAreJudged(t *testing.T) {
	t.Parallel()
	f, inv := serialJudgmentFixture(t, "plain")
	f.agent.MaxParallelRuns = 2 // plain は 2 スロット。3 件目の候補は判断せず、全員と直列
	a := f.candidate(t, "plain", "P1", 21)
	b := f.candidate(t, "plain", "P1", 22)
	c := f.candidate(t, "plain", "P1", 23)
	res := f.runPlanWith(t, inv)
	if n := len(inv.called()); n != 1 {
		t.Fatalf("J9 calls = %d, want 1 (only the pair within the limit)", n)
	}
	if want := [][]string{{a, b, c}}; !reflect.DeepEqual(groupsOf(res), want) {
		t.Errorf("groups = %v, want %v (candidates over the limit are serial)", groupsOf(res), want)
	}
}

func TestJ9_CycleBudget_RemainingPairsAreSerialWithoutJudging(t *testing.T) {
	t.Parallel()
	f, inv := serialJudgmentFixture(t, "plain")
	f.conn.Repos[3].Slots.Paths = []string{"p1", "p2", "p3"}
	f.git.states[f.s.Workspace()+"/p3"] = SlotTreeState{Exists: true, PointerOK: true, OriginURL: "https://github.com/o/plain.git"}
	f.agent.SerialJudgmentCycleBudgetUSD = 1.0 // 1 組 0.5 USD の上限で、費用 0.6 の判断の後は次の 0.5 を足せない
	f.candidate(t, "plain", "P1", 21)
	f.candidate(t, "plain", "P1", 22)
	f.candidate(t, "plain", "P1", 23)
	inv.respond = func(JudgmentLaunchInput, int) JudgmentLaunchOutput { return j9Out("weak", "独立", 0.6) }
	res := f.runPlanWith(t, inv)
	if n := len(inv.called()); n != 1 {
		t.Fatalf("J9 calls = %d, want 1 (the cycle cap stops the rest)", n)
	}
	var budget int
	for _, g := range res.SerialGroups {
		for _, j := range g.Judgments {
			if j.Outcome == PairOutcomeBudget {
				budget++
			}
		}
	}
	if budget == 0 {
		t.Errorf("no budget outcome recorded: %+v", res.SerialGroups)
	}
	if len(res.SerialGroups) != 1 || !hasReason(res.SerialGroups[0].Reasons, SerialReasonJudgmentBudget) {
		t.Errorf("groups = %+v, want one serial group with serial_judgment_budget", res.SerialGroups)
	}
}

func TestJ9_RunningDelegation_IsNotJudged_Undeclared(t *testing.T) {
	t.Parallel()
	f, inv := serialJudgmentFixture(t, "plain")
	run := f.candidate(t, "plain", "P1", 21)
	release := f.holdRunning(t, run)
	defer release()
	a := f.candidate(t, "plain", "P1", 22)
	b := f.candidate(t, "plain", "P1", 23)
	res := f.runPlanWith(t, inv)
	if len(inv.called()) != 0 {
		t.Errorf("J9 was called while a delegation was running")
	}
	if want := [][]string{{run, a, b}}; !reflect.DeepEqual(groupsOf(res), want) {
		t.Errorf("groups = %v, want %v", groupsOf(res), want)
	}
}

func TestValidateJ9Output(t *testing.T) {
	t.Parallel()
	ok := map[string]J9Verdict{
		`{"verdict":"strong","reason":"r"}`:    J9VerdictStrong,
		`{"verdict":"weak","reason":" r "}`:    J9VerdictWeak,
		`{"verdict":"uncertain","reason":"r"}`: J9VerdictUncertain,
	}
	for in, want := range ok {
		v, r, good := validateJ9Output([]byte(in))
		if !good || v != want || r != strings.TrimSpace(r) || r != "r" {
			t.Errorf("validateJ9Output(%s) = %v %q %v", in, v, r, good)
		}
	}
	for _, bad := range []string{``, `[]`, `{}`, `{"verdict":"weak"}`, `{"reason":"r"}`, `{"verdict":"maybe","reason":"r"}`, `{"verdict":"weak","reason":""}`, `{"verdict":null,"reason":"r"}`} {
		if _, _, good := validateJ9Output([]byte(bad)); good {
			t.Errorf("validateJ9Output(%s) = ok, want invalid", bad)
		}
	}
	var schema map[string]any
	if err := json.Unmarshal(j9OutputSchema(), &schema); err != nil || schema["additionalProperties"] != false {
		t.Errorf("schema = %s, err %v", j9OutputSchema(), err)
	}
}

func TestJ9_ConfigDeclarations(t *testing.T) {
	t.Parallel()
	conn, err := parseConnectorsDeclaration([]byte(strings.Replace(planConnectorsJSON, `"name": "multi",`, `"name": "multi", "serial_judgment": true,`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range conn.Repos {
		if r.SerialJudgment != (r.Name == "multi") {
			t.Errorf("repo %s SerialJudgment = %v", r.Name, r.SerialJudgment)
		}
	}
	for _, bad := range []string{`null`, `"yes"`, `1`} {
		if _, err := parseConnectorsDeclaration([]byte(strings.Replace(planConnectorsJSON, `"name": "multi",`, `"name": "multi", "serial_judgment": `+bad+`,`, 1))); err == nil {
			t.Errorf("serial_judgment %s was accepted", bad)
		}
	}
	d := defaultAgentDeclaration()
	if d.JudgmentBudgetFor(JudgmentJ9) != 0.5 || d.SerialJudgmentCycleBudgetUSD != 3 {
		t.Errorf("defaults = %v / %v, want 0.5 / 3", d.JudgmentBudgetFor(JudgmentJ9), d.SerialJudgmentCycleBudgetUSD)
	}
	for _, bad := range []string{`{"judgment_budget_usd": {"J9": 0}}`, `{"serial_judgment_cycle_budget_usd": 0}`, `{"serial_judgment_cycle_budget_usd": -1}`} {
		if _, err := LoadAgentDeclaration(writeAgentJSON(t, bad)); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}
