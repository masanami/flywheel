package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// 区切り S4c-1 の 4: 計画の段で、足す依頼を作り、判断点を「足す」の形で呼ぶ（入力の組み立てと呼び出しまで。出力は受け口だけ）。
// 実ストアと偽の判断の IF で確かめる。

// expandRecorder は呼ばれた判断点の入力を記録する偽の判断の IF（出力は out）。
type expandRecorder struct {
	mu    sync.Mutex
	out   []byte
	cost  *float64
	calls []JudgmentLaunchInput
}

func (r *expandRecorder) Available(context.Context) error { return nil }

func (r *expandRecorder) InvokeJudgment(_ context.Context, in JudgmentLaunchInput) (JudgmentLaunchOutput, error) {
	r.mu.Lock()
	r.calls = append(r.calls, in)
	r.mu.Unlock()
	return JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: r.out, ReportedTotalCostUSD: r.cost}, nil
}

func (r *expandRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// expandAskOutput は足す依頼を仕様待ちのまま残す出力（ask は、保留の処理が後続のため、何も書かない）。
const expandAskOutput = `{"verdict":"ask","tasks":null,"question":"続きは何か"}`

// newExpandFixture は、2 個のタスク（a は続きを決める印つき・b。枠は 3+2 USD ずつ）で承認済みの、着手中の課題を作る。
func newExpandFixture(t *testing.T) *paFixture {
	t.Helper()
	f := newLimitFixture(t, func(m map[string]any) { m["budget_limit_usd"] = 14.0; m["budget_limit_reason"] = "続きを足す分" })
	if err := f.approve(t, ttyAtt(t, f.id)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	return f
}

func (f *paFixture) cid() int64 {
	cid, _ := parseChallengeID(f.id)
	return cid
}

func (f *paFixture) setTaskStatus(t *testing.T, key string, st TaskStatus) {
	t.Helper()
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task SET status = ? WHERE challenge_id = ? AND key = ?`, string(st), f.cid(), key)
		return err
	})
}

func (f *paFixture) setChallengeStatus(t *testing.T, st Status) {
	t.Helper()
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET status = ? WHERE id = ?`, string(st), f.cid())
		return err
	})
}

// raiseHold は課題を、from の状態から置かれた開いている保留つきの保留中にする。
func (f *paFixture) raiseHold(t *testing.T, from Status) {
	t.Helper()
	f.setChallengeStatus(t, StatusAwaitingHuman)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at) VALUES (?, 'q', ?, '2026-11-22T00:00:00.000Z')`, f.cid(), string(from))
		return err
	})
}

// expand は計画の段の「足す」の処理を 1 回呼ぶ。
func (f *paFixture) expand(t *testing.T, inv JudgmentInvoker) *J2AutoResult {
	t.Helper()
	res, err := f.s.expandAutoJ2(context.Background(), f.input(inv, f.beginCycle(t, 300), nil))
	if err != nil {
		t.Fatalf("expandAutoJ2: %v", err)
	}
	return res
}

func (f *paFixture) additions(t *testing.T) []TaskAddition {
	t.Helper()
	adds, err := f.s.ListTaskAdditions(context.Background(), f.id)
	if err != nil {
		t.Fatalf("ListTaskAdditions: %v", err)
	}
	return adds
}

// addActiveRun は課題に、終わっていない判断の実行を 1 件足す（taskKey が空ならタスクに結び付かない実行）。
func (f *paFixture) addActiveRun(t *testing.T, taskKey string) {
	t.Helper()
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		var tid *int64
		if taskKey != "" {
			var id int64
			if err := tx.QueryRow(`SELECT id FROM task WHERE challenge_id = ? AND key = ?`, f.cid(), taskKey).Scan(&id); err != nil {
				return err
			}
			tid = &id
		}
		sid, err := newLowercaseUUIDv4()
		if err != nil {
			return err
		}
		now := time.Now()
		_, err = insertRun(context.Background(), tx, f.cid(), insertRunInput{
			Kind: runKindJudgment, Judgment: JudgmentJ3, ChallengeVersion: 1, SessionID: sid, PID: 1, Host: "h",
			HeartbeatAt: now, StartedAt: now, MaxBudgetUSD: 1, BudgetBucket: budgetBucketJudgment, TaskID: tid,
		})
		return err
	})
}

func TestExpand_DoneMarkedTaskCreatesOnePendingAdditionAndCallsJ2OnceInTheExpandShape(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	f.setTaskStatus(t, "a", TaskStatusDone)
	inv := &expandRecorder{out: []byte(expandAskOutput)}
	res := f.expand(t, inv)

	adds := f.additions(t)
	if len(adds) != 1 || adds[0].Origin != TaskAdditionOriginExpand || adds[0].State != TaskAdditionPending || adds[0].OriginTaskID == nil {
		t.Fatalf("additions = %+v, want one pending expand addition", adds)
	}
	if inv.count() != 1 {
		t.Fatalf("J2 calls = %d, want 1", inv.count())
	}
	if len(res.Items) != 1 || res.Items[0].Result != RunResultSucceeded || res.Items[0].Outcome != "ask" {
		t.Errorf("items = %+v", res.Items)
	}
	// ask は課題を保留にする（足す依頼は仕様待ちのまま。保留の中身は judgment_j2_expand_holds_test.go）。
	if st := f.detail(t, f.id).Status; st != StatusAwaitingHuman {
		t.Errorf("status = %s, want awaiting_human", st)
	}
	// 実行の記録は課題単位の J2 で、タスクの指定を持たない。
	runs, err := f.s.ListRuns(context.Background(), RunListOptions{ChallengeID: &f.id})
	if err != nil || len(runs) != 2 || runs[0].Judgment != JudgmentJ2 || runs[1].Judgment != JudgmentJ2 {
		t.Fatalf("runs = %+v err=%v", runs, err)
	}
	var withTask int
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM run WHERE challenge_id = ? AND task_id IS NOT NULL`, f.cid()).Scan(&withTask)
	})
	if withTask != 0 {
		t.Errorf("runs with a task = %d, want 0", withTask)
	}
}

func TestExpand_ExpandInputCarriesTheCallShapeFirstAndTheDesignedSections(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	f.setTaskStatus(t, "a", TaskStatusDone)
	// 済んだタスクの結果: 委譲先の要約・成果物・タスクの検証の結果。
	var tid int64
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT id FROM task WHERE challenge_id = ? AND key = 'a'`, f.cid()).Scan(&tid)
	})
	if err := f.s.mutate(context.Background(), ChannelInvoker, func(_ *sql.Tx, rec *activityRecorder) error {
		return rec.record("task", tid, ActivityActionTaskVerify, nil, map[string]any{"verdict": "met", "reason": "EVIDENCE-OK"})
	}); err != nil {
		t.Fatalf("record verification: %v", err)
	}
	inv := &expandRecorder{out: []byte(expandAskOutput)}
	f.expand(t, inv)
	if inv.count() != 1 {
		t.Fatalf("calls = %d", inv.count())
	}
	secs := inv.calls[0].Sections
	if secs[0].Label != callShapeSectionLabel || secs[0].Content != "足す" {
		t.Fatalf("first section = %+v, want the call shape 足す", secs[0])
	}
	text := sectionsText(secs)
	for _, want := range []string{
		"方針:", "完了条件:", "やらないこと:", // 承認した計画
		"鍵: a", "鍵: b", "題名:", "状態: done", "状態: pending", "前提:", // いまのタスク
		"委譲先の要約:", "成果物:", "タスクの検証: met｜EVIDENCE-OK", // 済んだタスクの結果
		"上限: 14 USD", "対象リポジトリと操作の宣言", "足す依頼の出どころ", "上限までの残り（上限 − 見込み）: 4 USD", // 上限と残り（見込みは 10 USD）
	} {
		if !strings.Contains(text, want) {
			t.Errorf("input lacks %q:\n%s", want, text)
		}
	}
}

func TestExpand_PlanShapeInputHasNoCallShapeSection(t *testing.T) {
	t.Parallel()
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "plan-shape", "P1")
	inv := &expandRecorder{out: j2OutputFor(false, nil)}
	if _, err := f.run(t, inv, idPtr(ch.ID)); err != nil || inv.count() != 1 {
		t.Fatalf("plan: %v calls=%d", err, inv.count())
	}
	for _, s := range inv.calls[0].Sections {
		if s.Label == callShapeSectionLabel {
			t.Errorf("the plan shape must not carry the call shape section: %+v", s)
		}
	}
}

func TestExpand_NoAdditionWithoutADoneMarkedTask(t *testing.T) {
	t.Parallel()
	t.Run("印の付いたタスクが済みでない", func(t *testing.T) {
		f := newExpandFixture(t)
		inv := &expandRecorder{out: []byte(expandAskOutput)}
		f.expand(t, inv)
		if len(f.additions(t)) != 0 || inv.count() != 0 {
			t.Errorf("additions=%d calls=%d, want none", len(f.additions(t)), inv.count())
		}
	})
	t.Run("印の無いタスクだけが済み", func(t *testing.T) {
		f := newExpandFixture(t)
		f.setTaskStatus(t, "b", TaskStatusDone)
		inv := &expandRecorder{out: []byte(expandAskOutput)}
		f.expand(t, inv)
		if len(f.additions(t)) != 0 || inv.count() != 0 {
			t.Errorf("additions=%d calls=%d, want none", len(f.additions(t)), inv.count())
		}
	})
	t.Run("統合先を使わない課題", func(t *testing.T) {
		f := newExpandFixture(t)
		f.setTaskStatus(t, "a", TaskStatusDone)
		writeForTest(t, f.s, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE challenge SET uses_integration = 0 WHERE id = ?`, f.cid())
			return err
		})
		inv := &expandRecorder{out: []byte(expandAskOutput)}
		f.expand(t, inv)
		if len(f.additions(t)) != 0 || inv.count() != 0 {
			t.Errorf("additions=%d calls=%d, want none", len(f.additions(t)), inv.count())
		}
	})
	t.Run("見込みが不明の課題", func(t *testing.T) {
		f := newExpandFixture(t)
		f.setTaskStatus(t, "a", TaskStatusDone)
		f.makeEstimateUnknown(t)
		inv := &expandRecorder{out: []byte(expandAskOutput)}
		f.expand(t, inv)
		if len(f.additions(t)) != 0 || inv.count() != 0 {
			t.Errorf("additions=%d calls=%d, want none", len(f.additions(t)), inv.count())
		}
	})
	t.Run("完了の課題", func(t *testing.T) {
		f := newExpandFixture(t)
		f.setTaskStatus(t, "a", TaskStatusDone)
		f.setChallengeStatus(t, StatusVerifying)
		inv := &expandRecorder{out: []byte(expandAskOutput)}
		f.expand(t, inv)
		if len(f.additions(t)) != 0 || inv.count() != 0 {
			t.Errorf("additions=%d calls=%d, want none", len(f.additions(t)), inv.count())
		}
	})
}

// makeEstimateUnknown は、タスクの枠の記録と計画の額を消して、課題の見込みを不明にする。
func (f *paFixture) makeEstimateUnknown(t *testing.T) {
	t.Helper()
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE task SET budget_impl_micros = NULL, budget_review_micros = NULL WHERE challenge_id = ?`, f.cid()); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE task_plan SET spec = NULL WHERE challenge_id = ?`, f.cid())
		return err
	})
}

func TestExpand_AdditionIsNotDuplicatedAcrossRounds(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	f.setTaskStatus(t, "a", TaskStatusDone)
	inv := &expandRecorder{out: []byte(expandAskOutput)}
	f.expand(t, inv)
	f.expand(t, inv)
	if adds := f.additions(t); len(adds) != 1 {
		t.Errorf("additions = %+v, want 1 after two rounds", adds)
	}
}

func TestExpand_HeldChallengeGetsAnAdditionButNoJudgmentUntilResumed(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	f.setTaskStatus(t, "a", TaskStatusDone)
	f.raiseHold(t, StatusInProgress)
	inv := &expandRecorder{out: []byte(expandAskOutput)}
	f.expand(t, inv)
	if adds := f.additions(t); len(adds) != 1 || adds[0].State != TaskAdditionPending {
		t.Fatalf("additions = %+v, want one pending", adds)
	}
	if inv.count() != 0 {
		t.Fatalf("J2 calls while held = %d, want 0", inv.count())
	}
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE hold SET answer = 'a', closed_at = '2026-11-22T01:00:00.000Z' WHERE challenge_id = ?`, f.cid())
		return err
	})
	f.setChallengeStatus(t, StatusInProgress)
	f.expand(t, inv)
	if inv.count() != 1 {
		t.Errorf("J2 calls after resuming = %d, want 1", inv.count())
	}
}

func TestExpand_UnknownEstimateAfterTheAdditionWaits(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	f.setTaskStatus(t, "a", TaskStatusDone)
	inv := &expandRecorder{out: []byte(expandAskOutput)}
	f.expand(t, inv)
	f.makeEstimateUnknown(t)
	f.expand(t, inv)
	if inv.count() != 1 {
		t.Errorf("J2 calls = %d, want only the first", inv.count())
	}
	if adds := f.additions(t); len(adds) != 1 || adds[0].State != TaskAdditionPending {
		t.Errorf("additions = %+v, want the pending one kept", adds)
	}
}

func TestExpand_AnotherTasksRunDoesNotBlockButATaskLessRunDoes(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	f.setTaskStatus(t, "a", TaskStatusDone)
	f.addActiveRun(t, "b")
	inv := &expandRecorder{out: []byte(expandAskOutput)}
	f.expand(t, inv)
	if inv.count() != 1 {
		t.Fatalf("J2 calls with another task's run = %d, want 1", inv.count())
	}

	g := newExpandFixture(t)
	g.setTaskStatus(t, "a", TaskStatusDone)
	g.addActiveRun(t, "")
	inv2 := &expandRecorder{out: []byte(expandAskOutput)}
	g.expand(t, inv2)
	if inv2.count() != 0 {
		t.Errorf("J2 calls with a task-less run = %d, want 0", inv2.count())
	}
}

func TestExpand_OnlyTheOldestPendingAdditionIsCalledPerRound(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	for _, key := range []string{"a", "b"} {
		var tid string
		for _, tk := range taskRows(t, f.s, f.cid()) {
			if tk.Key == key {
				tid = tk.ID
			}
		}
		if _, err := f.s.CreateTaskAddition(context.Background(), ChannelInvoker, f.id, TaskAdditionInput{Origin: TaskAdditionOriginExpand, OriginTaskID: tid}); err != nil {
			t.Fatalf("CreateTaskAddition: %v", err)
		}
	}
	inv := &expandRecorder{out: []byte(expandAskOutput)}
	f.expand(t, inv)
	if inv.count() != 1 {
		t.Errorf("J2 calls = %d, want 1 for two pending additions", inv.count())
	}
}

func TestExpand_JudgmentCostIsNotPartOfTheEstimate(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	f.setTaskStatus(t, "a", TaskStatusDone)
	before := f.detail(t, f.id).Budget
	cost := 0.5
	f.expand(t, &expandRecorder{out: []byte(expandAskOutput), cost: &cost})
	after := f.detail(t, f.id).Budget
	if before.EstimateUSD == nil || after.EstimateUSD == nil || *before.EstimateUSD != *after.EstimateUSD {
		t.Errorf("estimate before=%v after=%v, want unchanged", before.EstimateUSD, after.EstimateUSD)
	}
}

func TestExpandOutput_SchemaAndReceiver(t *testing.T) {
	t.Parallel()
	if string(j2ExpandOutputSchema()) == string(j2OutputSchema()) {
		t.Error("the expand schema must differ from the plan schema")
	}
	var sc map[string]any
	if err := json.Unmarshal(j2ExpandOutputSchema(), &sc); err != nil {
		t.Fatal(err)
	}
	props := sc["properties"].(map[string]any)
	if enum := props["verdict"].(map[string]any)["enum"].([]any); len(enum) != 3 {
		t.Errorf("verdict enum = %v, want none/add/ask", enum)
	}
	tasks := props["tasks"].(map[string]any)
	if tasks["maxItems"] != float64(MaxPlanTasks) {
		t.Errorf("tasks maxItems = %v, want %d", tasks["maxItems"], MaxPlanTasks)
	}
	if _, ok := tasks["items"].(map[string]any)["properties"].(map[string]any)["key"]; !ok {
		t.Error("task items lack key (same shape as the plan tasks)")
	}
	for in, want := range map[string]string{
		`{"verdict":"add","tasks":[]}`:     "add",
		`{"verdict":"ask","question":"q"}`: "ask",
		`{"verdict":"none"}`:               "none",
		`{"verdict":"plan"}`:               "",
		`not json`:                         "",
	} {
		got := ""
		if out, ok := parseJ2ExpandOutput([]byte(in)); ok {
			got = out.Verdict
		}
		if got != want {
			t.Errorf("parse(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestExpandFormatters_CarryTheValues(t *testing.T) {
	t.Parallel()
	tasks := []Task{
		{ID: "T-1", Key: "a", Title: "題A", Status: TaskStatusDone, Detail: TaskDetail{
			Delegate:     &TaskDelegate{Summary: "SUMMARY-A"},
			Artifacts:    []TaskArtifactView{{Kind: "pull_request", Ref: "o/r#1"}},
			Verification: &TaskVerificationView{Verdict: "met", Reason: "R"},
		}},
		{ID: "T-2", Key: "b", Title: "題B", Status: TaskStatusPending, DependsOn: []string{"T-1"}},
	}
	got := formatExpandTasksSection(tasks)
	if !strings.Contains(got, "鍵: b｜題名: 題B｜状態: pending｜前提: a") {
		t.Errorf("tasks section = %q", got)
	}
	done := formatExpandDoneResultsSection(tasks)
	for _, want := range []string{"委譲先の要約: SUMMARY-A", "pull_request o/r#1", "タスクの検証: met｜R"} {
		if !strings.Contains(done, want) {
			t.Errorf("done results lack %q:\n%s", want, done)
		}
	}
	summary := "POLICY-SUMMARY"
	plan := formatExpandPlanSection(Challenge{DoneCriteria: "DONE-CRIT"}, &PlanView{Summary: &summary, NonGoals: []string{"NG-1"}})
	for _, want := range []string{"POLICY-SUMMARY", "DONE-CRIT", "NG-1"} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan section lacks %q:\n%s", want, plan)
		}
	}
}

func TestRunJudgment_TaskLess_BlockedByATaskLessRunAndRejectsATaskID(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	f.addActiveRun(t, "")
	cyc := f.beginCycle(t, 300)
	inv := &expandRecorder{out: []byte(expandAskOutput)}
	in := RunJudgmentInput{ChallengeID: f.id, Judgment: JudgmentJ2, MaxBudgetUSD: 1, Sections: []JudgmentDataSection{{Label: "x", Content: "y"}}, OutputSchema: j2ExpandOutputSchema(), Invoker: inv, CycleID: &cyc, TaskLess: true}
	if _, err := f.s.RunJudgment(context.Background(), in); !errors.Is(err, ErrRunInProgress) {
		t.Errorf("err = %v, want ErrRunInProgress with a task-less run active", err)
	}
	tid := int64(1)
	in.TaskID = &tid
	if _, err := f.s.RunJudgment(context.Background(), in); !errors.Is(err, ErrValidation) {
		t.Errorf("err = %v, want ErrValidation for TaskLess with a task ID", err)
	}
	if inv.count() != 0 {
		t.Errorf("invoked %d times, want 0", inv.count())
	}
}

func TestExpand_HoldFromAnotherStateGetsNoAddition(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	f.setTaskStatus(t, "a", TaskStatusDone)
	f.raiseHold(t, StatusVerifying)
	f.expand(t, &expandRecorder{out: []byte(expandAskOutput)})
	if adds := f.additions(t); len(adds) != 0 {
		t.Errorf("additions = %+v, want none for a hold raised from another state", adds)
	}
}

func TestExpand_OldestAdditionsOriginTaskIsInTheInput(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	ids := map[string]string{}
	for _, tk := range taskRows(t, f.s, f.cid()) {
		ids[tk.Key] = tk.ID
	}
	for _, key := range []string{"b", "a"} { // 古いほうは b
		if _, err := f.s.CreateTaskAddition(context.Background(), ChannelInvoker, f.id, TaskAdditionInput{Origin: TaskAdditionOriginExpand, OriginTaskID: ids[key]}); err != nil {
			t.Fatal(err)
		}
	}
	inv := &expandRecorder{out: []byte(expandAskOutput)}
	f.expand(t, inv)
	if inv.count() != 1 || !strings.Contains(sectionsText(inv.calls[0].Sections), "続きを決めるタスク）: b（") {
		t.Errorf("calls=%d; the input must name the oldest addition's origin task b", inv.count())
	}
}

func TestExpand_RunCycleCreatesTheAdditionAndCallsJ2InThePlanPhase(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	f.setTaskStatus(t, "a", TaskStatusDone)
	inv := &expandRecorder{out: []byte(expandAskOutput)}
	cin := CycleRunInput{
		Trigger: "manual", AgentDecl: f.agent, ConnDecl: f.conn, Invoker: inv, Upstream: f.upstream,
		Delegate: &fakeDelegator{}, Git: newFakeSlotGit(), Reconcile: newFakeBranchSource(), Checks: newFakeChecks(), Diffs: &fakeDiffs{}, HeadGit: &noHeadGit{},
		WorkspaceGit: &fakeWorkspaceGit{},
	}
	res, err := f.s.RunCycle(context.Background(), cin)
	if err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	var expandCalls int
	for _, c := range inv.calls {
		if len(c.Sections) > 0 && c.Sections[0].Label == callShapeSectionLabel {
			expandCalls++
		}
	}
	if expandCalls < 1 {
		t.Fatalf("the plan phase did not call J2 in the expand shape (calls=%d)", inv.count())
	}
	if adds := f.additions(t); len(adds) != 1 {
		t.Errorf("additions = %+v, want 1", adds)
	}
	var found bool
	for _, p := range res.Phases {
		if p.Phase == CyclePhasePlan {
			for _, it := range p.Items {
				found = found || it.Outcome == "ask"
			}
		}
	}
	if !found {
		t.Error("the plan phase result lacks the expand item")
	}
}

func TestExpand_ClosedUpstreamChallengeIsNotCalledAndShownAsNotStarted(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	f.setTaskStatus(t, "a", TaskStatusDone)
	in := validBindingInput("o/r#77")
	in.UpstreamState = upstreamStateClosed
	if _, err := createSourceBindingForTest(f.s, f.id, time.Now(), in); err != nil {
		t.Fatal(err)
	}
	inv := &expandRecorder{out: []byte(expandAskOutput)}
	res := f.expand(t, inv)
	if inv.count() != 0 || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedUpstreamClosed {
		t.Errorf("calls=%d notStarted=%+v, want none called and upstream_closed", inv.count(), res.NotStarted)
	}
}

func TestExpand_OnlyTasksOfTheApprovedPlanVersionGetAnAddition(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	f.setTaskStatus(t, "a", TaskStatusDone)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE approval SET plan_version = plan_version + 1 WHERE challenge_id = ? AND kind = 'plan'`, f.cid())
		return err
	})
	f.expand(t, &expandRecorder{out: []byte(expandAskOutput)})
	if adds := f.additions(t); len(adds) != 0 {
		t.Errorf("additions = %+v, want none for a task outside the approved version", adds)
	}
}

func TestExpand_MismatchedCycleIDIsRejected(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	in := f.input(&expandRecorder{out: []byte(expandAskOutput)}, f.beginCycle(t, 300), nil)
	in.Cycle = NewJudgmentCycle("Y-9999")
	if _, err := f.s.expandAutoJ2(context.Background(), in); !errors.Is(err, ErrValidation) {
		t.Errorf("err = %v, want ErrValidation", err)
	}
}

func TestPrepareJ2Expand_UnknownEstimateIsSkippedNotAnError(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	f.makeEstimateUnknown(t)
	ch, err := f.s.loadChallengeForAuto(context.Background(), f.id)
	if err != nil {
		t.Fatal(err)
	}
	secs, ok, err := f.s.prepareJ2Expand(context.Background(), *ch, f.input(&expandRecorder{}, f.beginCycle(t, 300), nil), expandTarget{})
	if err != nil || ok || secs != nil {
		t.Errorf("secs=%v ok=%v err=%v, want skipped without an error", secs, ok, err)
	}
}
