package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// このファイルは #106（親要件チケット #98 §実行スロット「同じリポジトリの並列と直列化グループ」・
// §予算ガード）の委譲の段の実行計画を、実ストア・偽の予測の口・偽の判断／委譲の IF・偽の SlotGit で
// 検証する（AC-279・297〜335・347〜351・366・369 の core 側）。

const planConnectorsJSON = `{
  "version": 1,
  "human_question_kinds": [],
  "connectors": [
    {"id": "pc", "form": "plugin", "permission_mode": "acceptEdits",
     "conflict_prediction": {"command": ["predict"], "schema": "harness.conflict-prediction/v1"},
     "operations": [{"id": "impl", "invocation": "/h:impl {challenge_id}", "interactive": false, "child_may_decide": true, "artifacts": "pr"}]},
    {"id": "plain", "form": "plugin", "permission_mode": "acceptEdits",
     "operations": [{"id": "impl", "invocation": "/h:impl {challenge_id}", "interactive": false, "child_may_decide": true, "artifacts": "pr"}]}
  ],
  "repos": [
    {"name": "multi", "remote": "o/multi", "default_branch": "main", "connector": "pc", "slots": {"provider": "clone", "paths": ["m1", "m2", "m3"]}},
    {"name": "other", "remote": "o/other", "default_branch": "main", "connector": "pc", "slots": {"provider": "clone", "paths": ["o1", "o2"]}},
    {"name": "solo", "remote": "o/solo", "default_branch": "main", "connector": "pc", "slots": {"provider": "clone", "paths": ["s1"]}},
    {"name": "plain", "remote": "o/plain", "default_branch": "main", "connector": "plain", "slots": {"provider": "clone", "paths": ["p1", "p2"]}},
    {"name": "wt", "remote": "o/wt", "default_branch": "main", "connector": "pc", "slots": {"provider": "worktree", "base": "wt-base", "count": 2}}
  ]
}`

const predictionSchema = "harness.conflict-prediction/v1"

// fakePredictor は予測の口の偽の実装（呼び出しの引数を記録し、固定の結果を返す）。
type fakePredictor struct {
	mu     sync.Mutex
	calls  []PredictLaunchInput
	out    func(in PredictLaunchInput) PredictLaunchOutput
	events *eventLog
}

func (p *fakePredictor) Predict(_ context.Context, in PredictLaunchInput) (PredictLaunchOutput, error) {
	p.mu.Lock()
	p.calls = append(p.calls, in)
	p.mu.Unlock()
	if p.events != nil {
		p.events.add("predict")
	}
	return p.out(in), nil
}

func (p *fakePredictor) called() []PredictLaunchInput {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]PredictLaunchInput(nil), p.calls...)
}

type eventLog struct {
	mu sync.Mutex
	ev []string
}

func (l *eventLog) add(e string) {
	l.mu.Lock()
	l.ev = append(l.ev, e)
	l.mu.Unlock()
}

func (l *eventLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.ev...)
}

// predictionFor は渡された Issue をすべて predicted とし、pairs を持つ成功の出力を返す。
func predictionFor(in PredictLaunchInput, mut func(o *ConflictPredictionOutput), pairs ...PredictedPair) PredictLaunchOutput {
	out := &ConflictPredictionOutput{
		Schema: predictionSchema, Complete: true, HeadSHA: "deadbeef", CostUSD: float64Ptr(0.7), UnknownCostCount: float64Ptr(0), Pairs: pairs,
	}
	for _, n := range in.Issues {
		out.Issues = append(out.Issues, PredictedIssue{Issue: n, Status: predictIssuePredicted})
	}
	if mut != nil {
		mut(out)
	}
	return PredictLaunchOutput{Result: RunResultSucceeded, Prediction: out, RawOutput: []byte("{}")}
}

type planFixture struct {
	*delegateFixture
	pred   *fakePredictor
	events *eventLog
	// maxUnfinished は偽の委譲の起動の時点で見えた、終了していない委譲の run の数の最大値。
	maxUnfinished atomic.Int64
	nextIssue     int
	// holdBlock・holdStarted は、次の 1 回の委譲の起動を止める（holdRunning が置く）。
	holdMu      sync.Mutex
	holdBlock   chan struct{}
	holdStarted chan struct{}
}

func newPlanFixture(t *testing.T) *planFixture {
	t.Helper()
	old := delegationWaitInterval
	delegationWaitInterval = 5 * time.Millisecond
	t.Cleanup(func() { delegationWaitInterval = old })

	f := &planFixture{delegateFixture: newDelegateFixture(t), events: &eventLog{}, nextIssue: 100}
	conn, err := parseConnectorsDeclaration([]byte(planConnectorsJSON))
	if err != nil {
		t.Fatalf("parseConnectorsDeclaration: %v", err)
	}
	if err := conn.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	f.conn = conn
	for _, r := range conn.Repos {
		paths := r.Slots.Paths
		for _, rel := range paths {
			f.git.states[filepath.Join(f.s.Workspace(), rel)] = SlotTreeState{Exists: true, PointerOK: true, OriginURL: "https://github.com/" + r.Remote + ".git"}
		}
	}
	f.git.provisioned = SlotTreeState{Exists: true, PointerOK: true, OriginURL: "https://github.com/o/wt.git"}
	f.j3.onInvoke = nil
	f.pred = &fakePredictor{events: f.events, out: func(in PredictLaunchInput) PredictLaunchOutput { return predictionFor(in, nil) }}
	f.deleg.onInvoke = func(in DelegateLaunchInput) {
		f.holdMu.Lock()
		block, started := f.holdBlock, f.holdStarted
		f.holdBlock, f.holdStarted = nil, nil
		f.holdMu.Unlock()
		if started != nil {
			started <- struct{}{}
		}
		if block != nil {
			<-block
		}
		f.events.add(fmt.Sprintf("delegate %d", in.SourceIssueNumber))
		var n int64
		_ = f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT COUNT(*) FROM run WHERE kind = 'delegate' AND result IS NULL`).Scan(&n)
		})
		for {
			cur := f.maxUnfinished.Load()
			if n <= cur || f.maxUnfinished.CompareAndSwap(cur, n) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
	}
	return f
}

// candidate は repo の着手中の課題（取り込み元の Issue 番号 issue を持つ。0 なら対応無し）を作る。
func (f *planFixture) candidate(t *testing.T, repo, priority string, issue int) string {
	t.Helper()
	f.nextIssue++
	id := f.newInProgress(t, fmt.Sprintf("c%d", f.nextIssue), priority, planSpec(func(m map[string]any) { m["repo"] = repo }))
	if issue == 0 {
		return id
	}
	key := fmt.Sprintf("o/%s#%d", repo, issue)
	in := validBindingInput(key)
	th := simpleThread("body")
	in.UpstreamUpdatedAt = th.Issue.UpdatedAt
	if _, err := createSourceBindingForTest(f.s, id, time.Now(), in); err != nil {
		t.Fatal(err)
	}
	th.Issue.ExternalKey, th.Issue.Repo, th.Issue.Number, th.Issue.State = key, "o/"+repo, issue, "open"
	f.upstream.threads[key] = th
	return id
}

func (f *planFixture) in(cycleID string, id *string) DelegateInput {
	in := f.input(cycleID, id)
	in.Predictor = f.pred
	return in
}

func (f *planFixture) runPlan(t *testing.T) (*DelegateResult, error) {
	t.Helper()
	return f.s.RunDelegation(context.Background(), f.in(f.cycle(t, 300), nil))
}

func (f *planFixture) mustRunPlan(t *testing.T) *DelegateResult {
	t.Helper()
	res, err := f.runPlan(t)
	if err != nil {
		t.Fatalf("RunDelegation: %v", err)
	}
	return res
}

func (f *planFixture) predictRuns(t *testing.T) []runRow {
	t.Helper()
	var out []runRow
	err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id FROM run WHERE kind = 'predict' ORDER BY id`)
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

func (f *planFixture) setSlotState(t *testing.T, base string, st slotState) {
	t.Helper()
	slots, err := f.s.ListSlots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, sl := range slots {
		if filepath.Base(sl.Path) == base {
			id, _ := parseSlotID(sl.ID)
			if err := f.s.mutateSlots(context.Background(), func(tx *sql.Tx) error {
				_, err := updateSlotState(context.Background(), tx, id, st, nil, "x")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("slot %s not found", base)
}

func (f *planFixture) ensure(t *testing.T, repo string) {
	t.Helper()
	r, _ := f.conn.findConnectorRepo(repo)
	if err := f.s.EnsureSlots(context.Background(), *r); err != nil {
		t.Fatal(err)
	}
}

func groupsOf(res *DelegateResult) [][]string {
	var out [][]string
	for _, g := range res.SerialGroups {
		out = append(out, g.Challenges)
	}
	return out
}

func launchedIssues(f *planFixture) []int {
	var out []int
	for _, l := range f.deleg.launched() {
		out = append(out, l.SourceIssueNumber)
	}
	return out
}

// holdRunning は課題 id の委譲を別の周で起動したまま止める（終了していない委譲の run を作る）。
// 返る release を呼ぶと止めた委譲が終わる。
func (f *planFixture) holdRunning(t *testing.T, id string) (release func()) {
	t.Helper()
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	f.holdMu.Lock()
	f.holdBlock, f.holdStarted = block, started
	f.holdMu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := f.s.RunDelegation(context.Background(), f.in(f.cycle(t, 300), &id))
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the held delegation did not start")
	}
	var once sync.Once
	rel := func() {
		once.Do(func() {
			close(block)
			if err := <-done; err != nil {
				t.Errorf("held delegation: %v", err)
			}
		})
	}
	t.Cleanup(rel)
	return rel
}

// --- 予測の口に渡す Issue の順と呼ぶ条件（AC-297〜300） ---

func TestPlan_IssueOrder_RunningFirstThenCandidatesByPriorityThenID(t *testing.T) {
	f := newPlanFixture(t)
	running := f.candidate(t, "multi", "P1", 5)
	f.candidate(t, "multi", "P2", 20)
	f.candidate(t, "multi", "P0", 30)
	f.candidate(t, "multi", "P0", 25)
	release := f.holdRunning(t, running)
	defer release()

	res := f.mustRunPlan(t)
	calls := f.pred.called()
	if len(calls) != 1 || !reflect.DeepEqual(calls[0].Issues, []int{5, 30, 25, 20}) {
		t.Fatalf("prediction calls = %+v, want one call with [5 30 25 20]", calls)
	}
	if len(res.Items) != 3 {
		t.Errorf("items = %+v, want the 3 candidates (no shared files, separate groups)", res.Items)
	}
}

func TestPlan_NotCalledForOneIssueOrZeroPredictableCandidates(t *testing.T) {
	assertNotCalled := func(t *testing.T, f *planFixture) {
		t.Helper()
		if n := len(f.pred.called()); n != 0 {
			t.Fatalf("calls = %d, want 0", n)
		}
		if got := len(f.predictRuns(t)); got != 0 {
			t.Errorf("predict runs = %d, want 0", got)
		}
	}
	t.Run("one Issue", func(t *testing.T) {
		f := newPlanFixture(t)
		f.candidate(t, "multi", "P1", 7)
		f.mustRunPlan(t)
		assertNotCalled(t, f)
	})
	t.Run("running and candidates that cannot be predicted", func(t *testing.T) {
		f := newPlanFixture(t)
		running := f.candidate(t, "multi", "P1", 5)
		release := f.holdRunning(t, running)
		defer release()
		f.candidate(t, "multi", "P1", 0) // 取り込み元の対応が無く、Issue 番号を渡せない
		before, beforeRuns := len(f.pred.called()), len(f.predictRuns(t))
		f.mustRunPlan(t)
		if n := len(f.pred.called()) - before; n != 0 {
			t.Fatalf("calls = %d with no predictable candidate, want 0", n)
		}
		if n := len(f.predictRuns(t)) - beforeRuns; n != 0 {
			t.Errorf("predict runs = %d, want 0", n)
		}
	})
	t.Run("two running and zero candidates", func(t *testing.T) {
		f := newPlanFixture(t)
		a := f.candidate(t, "multi", "P1", 5)
		b := f.candidate(t, "multi", "P1", 6)
		releaseA := f.holdRunning(t, a)
		defer releaseA()
		releaseB := f.holdRunning(t, b)
		defer releaseB()
		// 2 件目を止めるための起動（1 件目が実行中・2 件目が候補）が予測の口を呼んでいるので、
		// その分を除き、実行中 2 件・候補 0 件の周で増えないことを見る。
		before, beforeRuns := len(f.pred.called()), len(f.predictRuns(t))
		f.mustRunPlan(t)
		if n := len(f.pred.called()) - before; n != 0 {
			t.Fatalf("calls = %d with two running and no candidates, want 0", n)
		}
		if n := len(f.predictRuns(t)) - beforeRuns; n != 0 {
			t.Errorf("predict runs = %d, want 0", n)
		}
	})
}

func TestPlan_CalledOnceForTwoAndForTwentyIssues(t *testing.T) {
	for _, n := range []int{2, 20} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			f := newPlanFixture(t)
			for i := 0; i < n; i++ {
				f.candidate(t, "multi", "P1", 1000+i)
			}
			f.mustRunPlan(t)
			calls := f.pred.called()
			if len(calls) != 1 || len(calls[0].Issues) != n {
				t.Fatalf("calls = %+v, want one call with %d Issues", calls, n)
			}
		})
	}
}

func TestPlan_TwentyOneIssues_NotCalled_OneGroup_NotPredictable(t *testing.T) {
	f := newPlanFixture(t)
	for i := 0; i < 21; i++ {
		f.candidate(t, "multi", "P1", 1000+i)
	}
	cyc := f.cycle(t, 3000)
	res, err := f.s.RunDelegation(context.Background(), f.in(cyc, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.pred.called()) != 0 {
		t.Error("the prediction must not be called for more than 20 Issues")
	}
	if len(res.SerialGroups) != 1 || len(res.SerialGroups[0].Challenges) != 21 ||
		!reflect.DeepEqual(res.SerialGroups[0].Reasons, []SerialGroupReason{SerialReasonNotPredictable}) || res.SerialGroups[0].PredictionHeadSHA != nil {
		t.Errorf("serial groups = %+v", res.SerialGroups)
	}
}

func TestPlan_CandidateWithoutSource_OneGroup_NotPredictable(t *testing.T) {
	f := newPlanFixture(t)
	f.candidate(t, "multi", "P1", 7)
	f.candidate(t, "multi", "P1", 8)
	f.candidate(t, "multi", "P1", 0)
	res := f.mustRunPlan(t)
	if len(f.pred.called()) != 0 {
		t.Error("no prediction is needed when the repo is already fail-closed")
	}
	if len(res.SerialGroups) != 1 || len(res.SerialGroups[0].Challenges) != 3 ||
		!reflect.DeepEqual(res.SerialGroups[0].Reasons, []SerialGroupReason{SerialReasonNotPredictable}) {
		t.Errorf("serial groups = %+v", res.SerialGroups)
	}
}

// --- グループの作り方と fail-closed（AC-301〜311） ---

func TestPlan_SharedFilesChain_OneGroup_OthersApart(t *testing.T) {
	f := newPlanFixture(t)
	a := f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	c := f.candidate(t, "multi", "P1", 13)
	d := f.candidate(t, "multi", "P1", 14)
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		return predictionFor(in, nil, pairOf(11, 12, shared("a.go")), pairOf(12, 13, shared("b.go")))
	}
	res := f.mustRunPlan(t)
	if want := [][]string{{a, b, c}, {d}}; !reflect.DeepEqual(groupsOf(res), want) {
		t.Fatalf("groups = %v, want %v", groupsOf(res), want)
	}
	g := res.SerialGroups[0]
	if !reflect.DeepEqual(g.Reasons, []SerialGroupReason{SerialReasonSharedFiles}) || g.PredictionHeadSHA == nil || *g.PredictionHeadSHA != "deadbeef" {
		t.Errorf("group = %+v", g)
	}
	if len(res.SerialGroups[1].Reasons) != 0 {
		t.Errorf("second group reasons = %v, want none", res.SerialGroups[1].Reasons)
	}
}

func TestPlan_FailClosedReasons(t *testing.T) {
	cases := []struct {
		name   string
		mut    func(o *ConflictPredictionOutput)
		reason SerialGroupReason
	}{
		{"issue failed", func(o *ConflictPredictionOutput) { o.Issues[0].Status = predictIssueFailed }, SerialReasonPredictionFailed},
		{"issue missing", func(o *ConflictPredictionOutput) { o.Issues = o.Issues[1:] }, SerialReasonPredictionFailed},
		{"issue budget exhausted", func(o *ConflictPredictionOutput) { o.Issues[1].Status = predictIssueBudgetExhausted }, SerialReasonPredictionBudget},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newPlanFixture(t)
			f.candidate(t, "multi", "P1", 11)
			f.candidate(t, "multi", "P1", 12)
			f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput { return predictionFor(in, tc.mut) }
			res := f.mustRunPlan(t)
			if len(res.SerialGroups) != 1 || len(res.SerialGroups[0].Challenges) != 2 ||
				!reflect.DeepEqual(res.SerialGroups[0].Reasons, []SerialGroupReason{tc.reason}) {
				t.Errorf("serial groups = %+v", res.SerialGroups)
			}
		})
	}
}

func TestPlan_WholeCallFailures_OneGroup_PredictionFailed(t *testing.T) {
	good := func(in PredictLaunchInput) *ConflictPredictionOutput { return predictionFor(in, nil).Prediction }
	cases := map[string]func(in PredictLaunchInput) PredictLaunchOutput{
		"launch failed": func(PredictLaunchInput) PredictLaunchOutput {
			return PredictLaunchOutput{Result: RunResultLaunchFailed, ErrorSummary: "no such file"}
		},
		"timed out": func(PredictLaunchInput) PredictLaunchOutput { return PredictLaunchOutput{Result: RunResultTimedOut} },
		"non-zero exit": func(in PredictLaunchInput) PredictLaunchOutput {
			return PredictLaunchOutput{Result: RunResultErrored, Prediction: good(in)}
		},
		"not json": func(PredictLaunchInput) PredictLaunchOutput { return PredictLaunchOutput{Result: RunResultMalformed} },
		"schema differs": func(in PredictLaunchInput) PredictLaunchOutput {
			p := good(in)
			p.Schema = "harness.conflict-prediction/v2"
			return PredictLaunchOutput{Result: RunResultSucceeded, Prediction: p}
		},
		"error is set": func(in PredictLaunchInput) PredictLaunchOutput {
			p := good(in)
			p.ErrorSet = true
			return PredictLaunchOutput{Result: RunResultSucceeded, Prediction: p}
		},
		"issue status outside the closed set": func(in PredictLaunchInput) PredictLaunchOutput {
			p := good(in)
			p.Issues[0].Status = "weird"
			return PredictLaunchOutput{Result: RunResultSucceeded, Prediction: p}
		},
		"pair status outside the closed set": func(in PredictLaunchInput) PredictLaunchOutput {
			p := good(in)
			p.Pairs = []PredictedPair{{Issues: [2]int{11, 12}, Status: "weird"}}
			return PredictLaunchOutput{Result: RunResultSucceeded, Prediction: p}
		},
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			f := newPlanFixture(t)
			f.candidate(t, "multi", "P1", 11)
			f.candidate(t, "multi", "P1", 12)
			f.pred.out = out
			res := f.mustRunPlan(t)
			if len(res.SerialGroups) != 1 || len(res.SerialGroups[0].Challenges) != 2 ||
				!reflect.DeepEqual(res.SerialGroups[0].Reasons, []SerialGroupReason{SerialReasonPredictionFailed}) || res.SerialGroups[0].PredictionHeadSHA != nil {
				t.Errorf("serial groups = %+v", res.SerialGroups)
			}
		})
	}
}

func TestPlan_CompleteFalseWithoutError_IsNotAWholeFailure(t *testing.T) {
	f := newPlanFixture(t)
	f.candidate(t, "multi", "P1", 11)
	f.candidate(t, "multi", "P1", 12)
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		return predictionFor(in, func(o *ConflictPredictionOutput) { o.Complete = false })
	}
	res := f.mustRunPlan(t)
	if len(res.SerialGroups) != 2 {
		t.Errorf("serial groups = %+v, want 2 separate groups", res.SerialGroups)
	}
}

func TestPlan_NoPredictionDeclared_OneGroup(t *testing.T) {
	f := newPlanFixture(t)
	f.candidate(t, "plain", "P1", 11)
	f.candidate(t, "plain", "P1", 12)
	res := f.mustRunPlan(t)
	if len(f.pred.called()) != 0 {
		t.Error("the prediction must not be called without a declaration")
	}
	if len(res.SerialGroups) != 1 || !reflect.DeepEqual(res.SerialGroups[0].Reasons, []SerialGroupReason{SerialReasonNoPredictionDeclare}) {
		t.Errorf("serial groups = %+v", res.SerialGroups)
	}
}

func TestPlan_DependencyOnly_OneGroup_AndOrder(t *testing.T) {
	f := newPlanFixture(t)
	hi := f.candidate(t, "multi", "P0", 11) // 優先度は高いが、依存で後
	lo := f.candidate(t, "multi", "P2", 12)
	first := 12
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		p := pairOf(11, 12)
		p.DependencyFirst = &first
		return predictionFor(in, nil, p)
	}
	res := f.mustRunPlan(t)
	if want := [][]string{{lo, hi}}; !reflect.DeepEqual(groupsOf(res), want) {
		t.Fatalf("groups = %v, want %v", groupsOf(res), want)
	}
	if !reflect.DeepEqual(res.SerialGroups[0].Reasons, []SerialGroupReason{SerialReasonDependency}) {
		t.Errorf("reasons = %v", res.SerialGroups[0].Reasons)
	}
	if got, want := launchedIssues(f), []int{12, 11}; !reflect.DeepEqual(got, want) {
		t.Errorf("launch order = %v, want %v", got, want)
	}
}

func TestPlan_UnknownPair_OneGroup_PriorityOrder(t *testing.T) {
	f := newPlanFixture(t)
	f.candidate(t, "multi", "P2", 11)
	f.candidate(t, "multi", "P0", 12)
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		return predictionFor(in, nil, PredictedPair{Issues: [2]int{11, 12}, Status: predictPairUnknown})
	}
	res := f.mustRunPlan(t)
	if len(res.SerialGroups) != 1 || !reflect.DeepEqual(res.SerialGroups[0].Reasons, []SerialGroupReason{SerialReasonUnknownPair}) {
		t.Fatalf("serial groups = %+v", res.SerialGroups)
	}
	if got, want := launchedIssues(f), []int{12, 11}; !reflect.DeepEqual(got, want) {
		t.Errorf("launch order = %v, want %v", got, want)
	}
}

func TestPlan_RunningWithoutIssue_FailClosed_AllCandidatesSerialized(t *testing.T) {
	f := newPlanFixture(t)
	running := f.candidate(t, "multi", "P1", 0)
	a := f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	release := f.holdRunning(t, running)
	defer release()

	res := f.mustRunPlan(t)
	if len(f.pred.called()) != 0 {
		t.Error("no prediction is needed when a running challenge has no Issue number")
	}
	if len(res.Items) != 0 || len(res.NotStarted) != 2 {
		t.Fatalf("result = %+v, want both candidates serialized", res)
	}
	for _, ns := range res.NotStarted {
		if ns.Reason != NotStartedSerialized {
			t.Errorf("reason = %s, want serialized", ns.Reason)
		}
	}
	g := res.SerialGroups[0]
	if !reflect.DeepEqual(g.Challenges, []string{running, a, b}) ||
		!reflect.DeepEqual(g.Reasons, []SerialGroupReason{SerialReasonNotPredictable, SerialReasonRunningRun}) {
		t.Errorf("group = %+v", g)
	}
}

func TestPlan_RunningInSameGroup_Serialized_OtherGroupStarts(t *testing.T) {
	f := newPlanFixture(t)
	running := f.candidate(t, "multi", "P1", 5)
	same := f.candidate(t, "multi", "P1", 11)
	apart := f.candidate(t, "multi", "P1", 12)
	release := f.holdRunning(t, running)
	defer release()
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		return predictionFor(in, nil, pairOf(5, 11, shared("x.go")))
	}
	res := f.mustRunPlan(t)
	if len(res.NotStarted) != 1 || res.NotStarted[0].ChallengeID != same || res.NotStarted[0].Reason != NotStartedSerialized {
		t.Errorf("not started = %+v, want %s serialized", res.NotStarted, same)
	}
	if len(res.Items) != 1 || res.Items[0].ChallengeID != apart {
		t.Errorf("items = %+v, want only %s", res.Items, apart)
	}
	g := res.SerialGroups[0]
	if !reflect.DeepEqual(g.Challenges, []string{running, same}) ||
		!reflect.DeepEqual(g.Reasons, []SerialGroupReason{SerialReasonSharedFiles, SerialReasonRunningRun}) {
		t.Errorf("group = %+v", g)
	}
}

func TestPlan_ExplicitID_SerializedWhenGroupedWithRunning(t *testing.T) {
	f := newPlanFixture(t)
	running := f.candidate(t, "multi", "P1", 5)
	same := f.candidate(t, "multi", "P1", 11)
	release := f.holdRunning(t, running)
	defer release()
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		return predictionFor(in, nil, pairOf(5, 11, shared("x.go")))
	}
	before := len(f.deleg.launched())
	_, err := f.s.RunDelegation(context.Background(), f.in(f.cycle(t, 300), &same))
	if !errors.Is(err, ErrSerialized) {
		t.Fatalf("err = %v, want ErrSerialized", err)
	}
	if len(f.deleg.launched()) != before {
		t.Error("the delegation must not start")
	}
	if len(f.pred.called()) != 1 {
		t.Errorf("the prediction is called once by an explicit run, got %d", len(f.pred.called()))
	}

	// 共有ファイルが無ければ別のグループなので起動する。
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput { return predictionFor(in, nil) }
	if _, err := f.s.RunDelegation(context.Background(), f.in(f.cycle(t, 300), &same)); err != nil {
		t.Fatalf("err = %v, want success", err)
	}
	if len(f.deleg.launched()) != before+1 {
		t.Error("the delegation must start in a separate group")
	}
}

// --- グループの中の順・並列と枠待ち（AC-312〜328） ---

func TestPlan_GroupRunsSequentially_AndContinuesAfterFailure(t *testing.T) {
	f := newPlanFixture(t)
	a := f.candidate(t, "multi", "P0", 11)
	b := f.candidate(t, "multi", "P1", 12)
	c := f.candidate(t, "multi", "P2", 13)
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		return predictionFor(in, nil, pairOf(11, 12, shared("x")), pairOf(12, 13, shared("x")))
	}
	f.deleg.queue = []JudgmentLaunchOutput{
		{Result: RunResultErrored, ErrorSummary: "boom"},
		{Result: RunResultSucceeded, StructuredOutput: reportJSON(func(m map[string]any) {
			m["outcome"] = "questions"
			m["questions"] = []any{map[string]any{"kind": "release_timing", "text": "q?", "options": []string{}, "recommendation": nil}}
		})},
	}
	res := f.mustRunPlan(t)
	if len(res.Items) != 3 || res.Items[0].ChallengeID != a || res.Items[1].ChallengeID != b || res.Items[2].ChallengeID != c {
		t.Fatalf("items = %+v", res.Items)
	}
	runs := f.delegateRuns(t)
	if len(runs) != 3 {
		t.Fatalf("delegate runs = %d, want 3", len(runs))
	}
	for i := 1; i < len(runs); i++ {
		if runs[i].EndedAt == nil || runs[i-1].EndedAt == nil || runs[i].StartedAt.Before(*runs[i-1].EndedAt) {
			t.Errorf("run %d started at %v before the previous ended at %v", i, runs[i].StartedAt, runs[i-1].EndedAt)
		}
	}
}

func TestPlan_RateLimitedStopsTheNextCandidateOfTheGroup(t *testing.T) {
	f := newPlanFixture(t)
	f.candidate(t, "multi", "P0", 11)
	b := f.candidate(t, "multi", "P1", 12)
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		return predictionFor(in, nil, pairOf(11, 12, shared("x")))
	}
	f.deleg.result = JudgmentLaunchOutput{Result: RunResultErrored, RateLimited: true}
	res := f.mustRunPlan(t)
	if len(res.Items) != 1 || len(res.NotStarted) != 1 || res.NotStarted[0].ChallengeID != b || res.NotStarted[0].Reason != NotStartedRateLimited {
		t.Errorf("result = %+v", res)
	}
}

func TestPlan_TwoGroupsRunConcurrently(t *testing.T) {
	f := newPlanFixture(t)
	f.candidate(t, "multi", "P1", 11)
	f.candidate(t, "multi", "P1", 12)
	f.mustRunPlan(t)
	if got := f.maxUnfinished.Load(); got != 2 {
		t.Errorf("max unfinished delegate runs = %d, want 2", got)
	}
}

func TestPlan_MaxParallelRunsOne_SecondGroupWaitsForTheFirst(t *testing.T) {
	f := newPlanFixture(t)
	f.agent.MaxParallelRuns = 1
	f.candidate(t, "multi", "P1", 11)
	f.candidate(t, "multi", "P1", 12)
	res := f.mustRunPlan(t)
	if len(res.Items) != 2 || len(res.NotStarted) != 0 {
		t.Fatalf("result = %+v, want both launched in the same cycle", res)
	}
	if got := f.maxUnfinished.Load(); got != 1 {
		t.Errorf("max unfinished = %d, want 1", got)
	}
	runs := f.delegateRuns(t)
	if runs[1].StartedAt.Before(*runs[0].EndedAt) {
		t.Error("the second group started before the first run ended")
	}
}

func TestPlan_GlobalLimitAcrossRepos(t *testing.T) {
	f := newPlanFixture(t)
	f.candidate(t, "multi", "P1", 11)
	f.candidate(t, "multi", "P1", 12)
	f.candidate(t, "other", "P1", 21)
	res := f.mustRunPlan(t)
	if len(res.Items) != 3 {
		t.Fatalf("items = %+v", res.Items)
	}
	if got := f.maxUnfinished.Load(); got > 2 || got < 2 {
		t.Errorf("max unfinished = %d, want exactly 2 (max_parallel_runs)", got)
	}
}

func TestPlan_UsableSlotsLimitTheRepo(t *testing.T) {
	f := newPlanFixture(t)
	f.ensure(t, "multi")
	f.setSlotState(t, "m2", slotStateNeedsAttention)
	f.setSlotState(t, "m3", slotStateNeedsAttention)
	f.candidate(t, "multi", "P1", 11)
	f.candidate(t, "multi", "P1", 12)
	res := f.mustRunPlan(t)
	if len(res.Items) != 2 {
		t.Fatalf("items = %+v, want both launched one after the other", res.Items)
	}
	if got := f.maxUnfinished.Load(); got != 1 {
		t.Errorf("max unfinished = %d, want 1", got)
	}
}

func TestPlan_NoUsableSlot_NotStartedSlotUnavailable_WithoutWaiting(t *testing.T) {
	f := newPlanFixture(t)
	f.ensure(t, "solo")
	f.setSlotState(t, "s1", slotStateNeedsAttention)
	id := f.candidate(t, "solo", "P1", 11)
	res := f.mustRunPlan(t)
	if len(res.NotStarted) != 1 || res.NotStarted[0].ChallengeID != id || res.NotStarted[0].Reason != NotStartedSlotUnavailable {
		t.Errorf("not started = %+v", res.NotStarted)
	}
}

func TestPlan_UnprovisionedWorktreeSlotsCount(t *testing.T) {
	f := newPlanFixture(t)
	f.candidate(t, "wt", "P1", 11)
	f.candidate(t, "wt", "P1", 12)
	res := f.mustRunPlan(t)
	if len(res.Items) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if got := f.maxUnfinished.Load(); got != 2 {
		t.Errorf("max unfinished = %d, want 2 (count 2, not provisioned yet)", got)
	}
}

func TestPlan_WaitingGroupsStartInHeadPriorityOrder(t *testing.T) {
	f := newPlanFixture(t)
	f.agent.MaxParallelRuns = 1
	f.candidate(t, "multi", "P2", 11)
	f.candidate(t, "other", "P0", 21)
	f.candidate(t, "multi", "P1", 12)
	f.mustRunPlan(t)
	if got, want := launchedIssues(f), []int{21, 12, 11}; !reflect.DeepEqual(got, want) {
		t.Errorf("launch order = %v, want %v", got, want)
	}
}

func TestPlan_CycleBudgetAndRunBudget_AfterWaiting(t *testing.T) {
	f := newPlanFixture(t)
	f.agent.MaxParallelRuns = 1
	f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	// 最初の起動（J3 の 3 ＋ 実装枠 50 ＋ レビュー対応枠 30）だけが入る周の上限。2 件目は、1 件目の費用
	// （J3 の 3 ＋ 委譲の 2.5）と予測の 0.7 が数えられた後では入らない。
	res, err := f.s.RunDelegation(context.Background(), f.in(f.cycle(t, 88), nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || len(res.NotStarted) != 1 || res.NotStarted[0].ChallengeID != b || res.NotStarted[0].Reason != NotStartedCycleBudget {
		t.Errorf("result = %+v", res)
	}
}

// --- 予測の口の呼び出しの順と予算・記録（AC-334・335・347〜351） ---

func TestPlan_PredictionIsCalledBeforeAnyDelegation(t *testing.T) {
	f := newPlanFixture(t)
	f.candidate(t, "multi", "P1", 11)
	f.candidate(t, "other", "P1", 21)
	f.candidate(t, "multi", "P1", 12)
	f.mustRunPlan(t)
	ev := f.events.list()
	lastPredict, firstDelegate := -1, len(ev)
	for i, e := range ev {
		if e == "predict" {
			lastPredict = i
		} else if i < firstDelegate {
			firstDelegate = i
		}
	}
	if lastPredict < 0 || lastPredict > firstDelegate {
		t.Errorf("events = %v, want every prediction before the first delegation", ev)
	}
}

func TestPlan_PredictionBudgetArgument(t *testing.T) {
	cases := []struct {
		perIssue float64
		issues   int
		want     float64
	}{{1.5, 3, 4.5}, {0, 3, 3}}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.want), func(t *testing.T) {
			f := newPlanFixture(t)
			if tc.perIssue > 0 {
				f.agent.ConflictPredictionBudgetUSD = tc.perIssue
			}
			for i := 0; i < tc.issues; i++ {
				f.candidate(t, "multi", "P1", 11+i)
			}
			f.mustRunPlan(t)
			calls := f.pred.called()
			if len(calls) != 1 || calls[0].MaxBudgetUSD != tc.want {
				t.Fatalf("calls = %+v, want MaxBudgetUSD %v", calls, tc.want)
			}
			if !reflect.DeepEqual(calls[0].Command, []string{"predict"}) || calls[0].TimeoutSec != f.agent.TimeoutSec.Judgment {
				t.Errorf("call = %+v", calls[0])
			}
			if want := filepath.Join(f.s.Workspace(), "m1"); calls[0].WorkDir != want {
				t.Errorf("WorkDir = %q, want the first idle clone %q", calls[0].WorkDir, want)
			}
		})
	}
}

func TestPlan_WorkDir_WorktreeUsesTheBaseClone(t *testing.T) {
	f := newPlanFixture(t)
	f.candidate(t, "wt", "P1", 11)
	f.candidate(t, "wt", "P1", 12)
	f.mustRunPlan(t)
	calls := f.pred.called()
	if len(calls) != 1 || calls[0].WorkDir != filepath.Join(f.s.Workspace(), "wt-base") {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestPlan_Clone_NoIdleWorkingClone_FailsLikeAWholeFailure(t *testing.T) {
	f := newPlanFixture(t)
	f.ensure(t, "multi")
	for _, p := range []string{"m1", "m2", "m3"} {
		f.setSlotState(t, p, slotStateNeedsAttention)
	}
	f.candidate(t, "multi", "P1", 11)
	f.candidate(t, "multi", "P1", 12)
	res := f.mustRunPlan(t)
	if len(f.pred.called()) != 0 {
		t.Error("the prediction must not be called without an idle clone")
	}
	if len(res.SerialGroups) != 1 || !reflect.DeepEqual(res.SerialGroups[0].Reasons, []SerialGroupReason{SerialReasonPredictionFailed}) {
		t.Errorf("serial groups = %+v", res.SerialGroups)
	}
}

func TestPlan_CycleBudgetBlocksThePrediction_DelegationsStillRunOneByOne(t *testing.T) {
	f := newPlanFixture(t)
	f.agent.ConflictPredictionBudgetUSD = 50 // 2 件 × 50 = 100 > 周の上限 90
	a := f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	res, err := f.s.RunDelegation(context.Background(), f.in(f.cycle(t, 90), nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.pred.called()) != 0 || len(f.predictRuns(t)) != 0 {
		t.Error("the prediction must not be called nor recorded when the cycle budget does not allow it")
	}
	if len(res.SerialGroups) != 1 || !reflect.DeepEqual(res.SerialGroups[0].Reasons, []SerialGroupReason{SerialReasonPredictionBudget}) ||
		!reflect.DeepEqual(res.SerialGroups[0].Challenges, []string{a, b}) {
		t.Errorf("serial groups = %+v", res.SerialGroups)
	}
	if len(res.Items) != 2 {
		t.Errorf("items = %+v, want both launched in order", res.Items)
	}
	if got := f.maxUnfinished.Load(); got != 1 {
		t.Errorf("max unfinished = %d, want 1", got)
	}
}

func TestPlan_PredictRunIsRecordedWithCostReported(t *testing.T) {
	f := newPlanFixture(t)
	f.candidate(t, "multi", "P1", 11)
	f.candidate(t, "multi", "P1", 12)
	// error が null でない出力でも、読めて schema が一致し cost_usd が数なら reported。
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		return predictionFor(in, func(o *ConflictPredictionOutput) { o.ErrorSet = true })
	}
	cyc := f.cycle(t, 300)
	if _, err := f.s.RunDelegation(context.Background(), f.in(cyc, nil)); err != nil {
		t.Fatal(err)
	}
	runs := f.predictRuns(t)
	if len(runs) != 1 {
		t.Fatalf("predict runs = %d, want 1", len(runs))
	}
	r := runs[0]
	if r.Kind != runKindPredict || r.Repo != "multi" || r.ChallengeID != "" || r.BudgetBucket != budgetBucketPredict || r.PID <= 0 || r.Host == "" {
		t.Errorf("run = %+v", r)
	}
	if r.Result != RunResultErrored || r.CostSource != costSourceReported || r.CostUSD == nil || *r.CostUSD != usdToMicros(0.7) {
		t.Errorf("result/cost = %v %v %v, want errored/reported/0.7", r.Result, r.CostSource, r.CostUSD)
	}
	if r.MaxBudgetUSD != usdToMicros(2) {
		t.Errorf("max budget = %d, want 2 USD", r.MaxBudgetUSD)
	}
	c, err := f.s.EndCycle(context.Background(), cyc, CycleResultCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if c.SpentUSD < 0.7 {
		t.Errorf("cycle spent = %v, want it to include the prediction cost 0.7", c.SpentUSD)
	}
}

func TestPlan_PredictCostIsTheLimitWhenNotTrustworthy(t *testing.T) {
	cases := map[string]func(o *ConflictPredictionOutput){
		"cost missing":    func(o *ConflictPredictionOutput) { o.CostUSD = nil },
		"cost negative":   func(o *ConflictPredictionOutput) { o.CostUSD = float64Ptr(-1) },
		"unknown count 1": func(o *ConflictPredictionOutput) { o.UnknownCostCount = float64Ptr(1) },
		"count not a num": func(o *ConflictPredictionOutput) { o.UnknownCostCountInvalid = true },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			f := newPlanFixture(t)
			f.candidate(t, "multi", "P1", 11)
			f.candidate(t, "multi", "P1", 12)
			f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput { return predictionFor(in, mut) }
			f.mustRunPlan(t)
			r := f.predictRuns(t)[0]
			if r.CostSource != costSourceUnknown || r.CostUSD == nil || *r.CostUSD != usdToMicros(2) {
				t.Errorf("cost = %v %v, want unknown / 2 USD", r.CostSource, r.CostUSD)
			}
		})
	}
	t.Run("output not json", func(t *testing.T) {
		f := newPlanFixture(t)
		f.candidate(t, "multi", "P1", 11)
		f.candidate(t, "multi", "P1", 12)
		f.pred.out = func(PredictLaunchInput) PredictLaunchOutput { return PredictLaunchOutput{Result: RunResultMalformed} }
		f.mustRunPlan(t)
		r := f.predictRuns(t)[0]
		if r.Result != RunResultMalformed || r.CostSource != costSourceUnknown || *r.CostUSD != usdToMicros(2) {
			t.Errorf("run = %+v", r)
		}
	})
	t.Run("launch failed costs nothing", func(t *testing.T) {
		f := newPlanFixture(t)
		f.candidate(t, "multi", "P1", 11)
		f.candidate(t, "multi", "P1", 12)
		f.pred.out = func(PredictLaunchInput) PredictLaunchOutput {
			return PredictLaunchOutput{Result: RunResultLaunchFailed}
		}
		f.mustRunPlan(t)
		r := f.predictRuns(t)[0]
		if r.Result != RunResultLaunchFailed || r.CostUSD == nil || *r.CostUSD != 0 {
			t.Errorf("run = %+v", r)
		}
	})
}

func TestPlan_NilPredictor_FailsClosed(t *testing.T) {
	f := newPlanFixture(t)
	f.candidate(t, "multi", "P1", 11)
	f.candidate(t, "multi", "P1", 12)
	in := f.in(f.cycle(t, 300), nil)
	in.Predictor = nil
	res, err := f.s.RunDelegation(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.SerialGroups) != 1 || !reflect.DeepEqual(res.SerialGroups[0].Reasons, []SerialGroupReason{SerialReasonPredictionFailed}) {
		t.Errorf("serial groups = %+v", res.SerialGroups)
	}
}

// 枠超過を記録した周には、衝突の予測の口を呼ばない（【決定 A 2026-10-05 オーナー】）。
func TestPlan_RateLimitedCycle_DoesNotCallThePredictor(t *testing.T) {
	f := newPlanFixture(t)
	f.candidate(t, "multi", "P1", 11)
	f.candidate(t, "multi", "P1", 12)
	cycleID := f.cycle(t, 300)
	in := f.in(cycleID, nil)
	jc := NewJudgmentCycle(cycleID)
	jc.rateLimited.Store(true)
	in.Cycle = jc
	if _, err := f.s.RunDelegation(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if got := f.pred.called(); len(got) != 0 {
		t.Errorf("predictor called %d times in a rate-limited cycle, want 0", len(got))
	}
}

// 取り込み元の Issue が対象リポジトリの Issue でなければ、番号は別の Issue を指すので予測できない。
func TestPlan_SourceIssueOfAnotherRepoIsNotPredictable(t *testing.T) {
	f := newPlanFixture(t)
	f.candidate(t, "multi", "P1", 11)
	id := f.candidate(t, "multi", "P1", 12)
	cid, _ := parseChallengeID(id)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE source_binding SET external_key = 'elsewhere/x#12' WHERE challenge_id = ?`, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	res := f.mustRunPlan(t)
	if len(f.pred.called()) != 0 {
		t.Error("the prediction must not be called with an Issue number of another repository")
	}
	if len(res.SerialGroups) != 1 || !reflect.DeepEqual(res.SerialGroups[0].Reasons, []SerialGroupReason{SerialReasonNotPredictable}) {
		t.Errorf("serial groups = %+v", res.SerialGroups)
	}
}

// 実装枠の残りが起動の最小額に満たない候補は run_budget で起動しない（J3 の費用を払う前）。
func TestPlan_RunBudget_NotStarted(t *testing.T) {
	f := newPlanFixture(t)
	f.agent.MaxParallelRuns = 1
	f.candidate(t, "multi", "P1", 11)
	id := f.newInProgress(t, "lowbudget", "P2", planSpec(func(m map[string]any) { m["repo"] = "multi"; m["budget_impl_usd"] = 0.5 }))
	res := f.mustRunPlan(t)
	var got NotStartedReason
	for _, ns := range res.NotStarted {
		if ns.ChallengeID == id {
			got = ns.Reason
		}
	}
	if got != NotStartedRunBudget {
		t.Errorf("not started = %+v, want %s run_budget", res.NotStarted, id)
	}
}

// 使えるスロットが 1 本のリポジトリ（worktree。予測は元のクローンで呼べる）で、衝突しない（別グループの）2 課題を別々の呼び出しで起動すると、後の呼び出しは
// serialized ではなく slot_unavailable で終わる（AC-1120 の前提: 同じグループに入らない）。
func TestPlan_ExplicitID_SingleSlot_SeparateGroups_SlotUnavailable(t *testing.T) {
	f := newPlanFixture(t)
	f.ensure(t, "wt")
	slots, _ := f.s.ListSlots(context.Background())
	f.setSlotState(t, filepath.Base(slots[len(slots)-1].Path), slotStateNeedsAttention) // 使えるスロットは 1 本
	running := f.candidate(t, "wt", "P1", 5)
	other := f.candidate(t, "wt", "P1", 6)
	release := f.holdRunning(t, running)
	defer release()
	_, err := f.s.RunDelegation(context.Background(), f.in(f.cycle(t, 300), &other))
	if !errors.Is(err, ErrSlotUnavailable) {
		t.Fatalf("err = %v, want ErrSlotUnavailable", err)
	}
}
