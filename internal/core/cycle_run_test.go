package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// このファイルは #86（親要件チケット #77・docs/features/m3-invoker-delegation.md
// §一括の操作（サイクル））の Store.RunCycle を、実ストア（t.TempDir() の
// SQLite）・偽の判断の IF・偽の上流の取得で検証する。CLI からの結線の検証は
// internal/cli の cycle_test.go が偽の `claude`・偽の `gh` で行う。
// AC-nn は m3-invoker-delegation.md ## 受入基準 配下の項目の通し序数。

// --- 偽の判断の IF（判断点・課題ごとに応答を変える） ---

type cycleCall struct {
	Judgment JudgmentPoint
	Text     string // 入力の区画の全文（課題のタイトルで、どの課題への呼び出しかを見分ける）
}

type cycleFakeInvoker struct {
	mu     sync.Mutex
	calls  []cycleCall
	handle func(c cycleCall) JudgmentLaunchOutput
}

func (f *cycleFakeInvoker) Available(context.Context) error { return nil }

func (f *cycleFakeInvoker) InvokeJudgment(_ context.Context, in JudgmentLaunchInput) (JudgmentLaunchOutput, error) {
	c := cycleCall{Judgment: in.Judgment, Text: sectionsText(in.Sections)}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	return f.handle(c), nil
}

// judgments は呼ばれた判断点の列（呼ばれた順）。
func (f *cycleFakeInvoker) judgments() []JudgmentPoint {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]JudgmentPoint, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.Judgment)
	}
	return out
}

// titlesFor は judgment の呼び出しの対象の課題のタイトル（"t-<n>"）を呼ばれた順に返す。
func (f *cycleFakeInvoker) titlesFor(j JudgmentPoint) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if c.Judgment != j {
			continue
		}
		i := strings.Index(c.Text, "タイトル: ")
		if i < 0 {
			out = append(out, "?")
			continue
		}
		rest := c.Text[i+len("タイトル: "):]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[:nl]
		}
		out = append(out, rest)
	}
	return out
}

func succeeded(output []byte) JudgmentLaunchOutput {
	return JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: output}
}

// j1MineP は優先度 p の mine の出力。
func j1MineP(t *testing.T, p string) []byte {
	t.Helper()
	return j1Output(t, j1RawOutput{Verdict: "mine", Priority: strPtr(p), Reason: "r"})
}

// mineThenPlanInvoker は J1 なら mine（優先度 P1）、J2 なら plan（宛先は brief）を返す。
func mineThenPlanInvoker(t *testing.T) *cycleFakeInvoker {
	t.Helper()
	return &cycleFakeInvoker{handle: func(c cycleCall) JudgmentLaunchOutput {
		if c.Judgment == JudgmentJ1 {
			return succeeded(j1MineP(t, "P1"))
		}
		return succeeded(j2OutputFor(false, nil))
	}}
}

type cycleFixture struct {
	s        *Store
	agent    *AgentDeclaration
	conn     *ConnectorsDeclaration
	upstream *fakeUpstreamThreads
}

func newCycleFixture(t *testing.T) *cycleFixture {
	t.Helper()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	return &cycleFixture{
		s:        s,
		agent:    newAgentDeclForJ1Test(t, s, "UNIQUE-POSITION-MARKER 担当範囲"),
		conn:     j2TestConnectors(t),
		upstream: newFakeUpstreamThreads(),
	}
}

func (f *cycleFixture) create(t *testing.T, title string) *Challenge {
	t.Helper()
	ch, err := f.s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: title, Description: "DESC-" + title})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	return ch
}

func (f *cycleFixture) classified(t *testing.T, title, priority string) *Challenge {
	t.Helper()
	ch := f.create(t, title)
	if _, err := f.s.ClassifyChallenge(context.Background(), ChannelCLI, ch.ID, ClassifyInput{Priority: priority}); err != nil {
		t.Fatalf("ClassifyChallenge: %v", err)
	}
	return ch
}

func (f *cycleFixture) input(inv JudgmentInvoker) CycleRunInput {
	return CycleRunInput{Trigger: "manual", AgentDecl: f.agent, ConnDecl: f.conn, Invoker: inv, Upstream: f.upstream}
}

func (f *cycleFixture) run(t *testing.T, in CycleRunInput) *CycleRunResult {
	t.Helper()
	res, err := f.s.RunCycle(context.Background(), in)
	if err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	return res
}

func (f *cycleFixture) status(t *testing.T, id string) Status {
	t.Helper()
	d, err := f.s.GetChallenge(context.Background(), id)
	if err != nil {
		t.Fatalf("GetChallenge(%s): %v", id, err)
	}
	return d.Status
}

func phaseNames(res *CycleRunResult) []CyclePhase {
	var out []CyclePhase
	for _, p := range res.Phases {
		out = append(out, p.Phase)
	}
	return out
}

func phaseOfResult(t *testing.T, res *CycleRunResult, name CyclePhase) CyclePhaseResult {
	t.Helper()
	for _, p := range res.Phases {
		if p.Phase == name {
			return p
		}
	}
	t.Fatalf("no phase %q in %v", name, phaseNames(res))
	return CyclePhaseResult{}
}

// --- 段の順・skipped（AC-132〜136） ---

// AC-132: 取り込み・分類・計画の順に段を実行する。取り込みで作られた課題が同じ周の分類・
// 計画へ進む（段の順は結果の phases の並びと、判断の呼び出しの順で確かめる）。
func TestRunCycle_RunsIngestClassifyPlanInOrder(t *testing.T) {
	f := newCycleFixture(t)
	inv := mineThenPlanInvoker(t)
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t-ingested", "body", "carol", []string{"masanami"}, nil)},
	}}
	// 上流の取り込み元の対応のある課題の J2 は上流の取得を行うので、偽の取得にも置く。
	f.upstream.threads["o/r#1"] = simpleThread("body")

	in := f.input(inv)
	in.Ingest = &CycleIngestInput{Sources: []SourceEntry{selfOnlySource("src", []string{"o/r"}, []string{"masanami"})}, Upstream: up}
	// 取り込み元の対応のある課題は operation の invocation が取り込み元の差し込みを持てるので、
	// J2 の出力は impl（invocation に {issue_number}）を使ってよい。
	inv.handle = func(c cycleCall) JudgmentLaunchOutput {
		if c.Judgment == JudgmentJ1 {
			return succeeded(j1MineP(t, "P1"))
		}
		return succeeded(j2OutputFor(true, nil))
	}
	res := f.run(t, in)

	want := []CyclePhase{CyclePhaseIngest, CyclePhaseClassify, CyclePhasePlan}
	if got := phaseNames(res); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("phases = %v, want %v", got, want)
	}
	if got := inv.judgments(); fmt.Sprint(got) != fmt.Sprint([]JudgmentPoint{JudgmentJ1, JudgmentJ2}) {
		t.Errorf("judgment calls = %v, want [J1 J2] (the challenge created by ingest is classified then planned in this cycle)", got)
	}
	ing := phaseOfResult(t, res, CyclePhaseIngest)
	if ing.Skipped || ing.Ingest == nil || len(ing.Ingest.Sources) != 1 {
		t.Errorf("ingest phase = %+v, want an executed ingest with 1 source", ing)
	}
	if got := f.status(t, "C-1"); got != StatusAwaitingPlanApproval {
		t.Errorf("C-1 status = %q, want %q", got, StatusAwaitingPlanApproval)
	}
}

// AC-133: 取り込み元の宣言が無い（Ingest が nil）とき、ingest の段は skipped で、結果を持たない。
func TestRunCycle_NoIngestInput_IngestPhaseIsSkipped(t *testing.T) {
	f := newCycleFixture(t)
	res := f.run(t, f.input(mineThenPlanInvoker(t)))

	ing := phaseOfResult(t, res, CyclePhaseIngest)
	if !ing.Skipped || ing.Ingest != nil {
		t.Errorf("ingest phase = %+v, want skipped with no result", ing)
	}
	if got := phaseNames(res); fmt.Sprint(got) != fmt.Sprint([]CyclePhase{CyclePhaseIngest, CyclePhaseClassify, CyclePhasePlan}) {
		t.Errorf("phases = %v, want all three phases listed even when ingest is skipped", got)
	}
}

// AC-134・AC-135・AC-136: 接続ツールの宣言（ConnDecl）が無い周は、エラーにならず、計画の段は
// skipped で J2 を起動せず、分類の段は J1 を起動する。
func TestRunCycle_NoConnectors_PlanSkippedJ2NotInvokedClassifyRuns(t *testing.T) {
	f := newCycleFixture(t)
	f.create(t, "t-1")
	inv := mineThenPlanInvoker(t)
	in := f.input(inv)
	in.ConnDecl = nil
	in.Upstream = nil

	res := f.run(t, in) // AC-134: エラーにならない

	plan := phaseOfResult(t, res, CyclePhasePlan)
	if !plan.Skipped || len(plan.Items) != 0 || len(plan.NotStarted) != 0 {
		t.Errorf("plan phase = %+v, want skipped with no items and no not_started (AC-135)", plan)
	}
	if got := inv.judgments(); fmt.Sprint(got) != fmt.Sprint([]JudgmentPoint{JudgmentJ1}) {
		t.Errorf("judgment calls = %v, want only [J1] (AC-135 J2 not invoked, AC-136 J1 invoked)", got)
	}
	cls := phaseOfResult(t, res, CyclePhaseClassify)
	if cls.Skipped || len(cls.Items) != 1 {
		t.Errorf("classify phase = %+v, want an executed phase with 1 item", cls)
	}
	if got := f.status(t, "C-1"); got != StatusClassified {
		t.Errorf("C-1 status = %q, want %q (classified but not planned)", got, StatusClassified)
	}
}

// --- 対象（AC-137〜140） ---

// AC-137: 同じ周の分類の段で分類済になった課題は、同じ周の計画の段で J2 の対象になる。
func TestRunCycle_ClassifiedInThisCycleIsPlannedInThisCycle(t *testing.T) {
	f := newCycleFixture(t)
	ch := f.create(t, "t-1")
	inv := mineThenPlanInvoker(t)

	res := f.run(t, f.input(inv))

	plan := phaseOfResult(t, res, CyclePhasePlan)
	if len(plan.Items) != 1 || plan.Items[0].ChallengeID != ch.ID || plan.Items[0].Outcome != "plan" {
		t.Fatalf("plan items = %+v, want %s planned in the same cycle", plan.Items, ch.ID)
	}
	if got := f.status(t, ch.ID); got != StatusAwaitingPlanApproval {
		t.Errorf("status = %q, want %q", got, StatusAwaitingPlanApproval)
	}
}

// AC-138: 各段の対象は優先度 P0→P1→P2→未設定、同じ優先度では ID の昇順で処理される
// （計画の段で確かめる。分類の段の対象は全員が優先度未設定なので ID の昇順になる）。
func TestRunCycle_TargetsAreProcessedByPriorityThenID(t *testing.T) {
	f := newCycleFixture(t)
	f.classified(t, "t-1", "P2")
	f.classified(t, "t-2", "P0")
	f.classified(t, "t-3", "P1")
	f.classified(t, "t-4", "P0")
	// 分類の段の対象（優先度未設定）は ID の昇順。
	f.create(t, "t-5")
	f.create(t, "t-6")
	inv := &cycleFakeInvoker{handle: func(c cycleCall) JudgmentLaunchOutput {
		if c.Judgment == JudgmentJ1 {
			return succeeded(j1Output(t, j1RawOutput{Verdict: "not_mine", Reason: "r"}))
		}
		return succeeded(j2OutputFor(false, nil))
	}}

	f.run(t, f.input(inv))

	if got := inv.titlesFor(JudgmentJ1); fmt.Sprint(got) != fmt.Sprint([]string{"t-5", "t-6"}) {
		t.Errorf("J1 order = %v, want [t-5 t-6] (ID ascending)", got)
	}
	if got := inv.titlesFor(JudgmentJ2); fmt.Sprint(got) != fmt.Sprint([]string{"t-2", "t-4", "t-3", "t-1"}) {
		t.Errorf("J2 order = %v, want [t-2 t-4 t-3 t-1] (P0 by ID, then P1, then P2)", got)
	}
}

// AC-139・AC-140: 計画承認待ち・完了確認待ち・人間対応待ちの課題は、どの段の対象にもならず、
// cycle は承認・差し戻し・保留への回答の作業ログを残さない（残るのは経路 invoker の遷移だけ）。
func TestRunCycle_SkipsWaitingChallengesAndWritesNoApprovalActivity(t *testing.T) {
	f := newCycleFixture(t)
	awaitingPlan := f.classified(t, "t-1", "P0")
	if _, _, err := f.s.PlanChallenge(context.Background(), ChannelCLI, awaitingPlan.ID, PlanInput{Body: "manual plan"}); err != nil {
		t.Fatalf("PlanChallenge: %v", err)
	}
	awaitingCompletion := f.create(t, "t-2")
	setChallengeStatus(t, f.s, mustChallengeInt(t, awaitingCompletion.ID), StatusAwaitingCompletionApproval)
	awaitingHuman := f.create(t, "t-3")
	if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, awaitingHuman.ID, HoldInput{Question: "q"}); err != nil {
		t.Fatalf("HoldChallenge: %v", err)
	}
	target := f.create(t, "t-4") // 対象になる課題が 1 件だけある
	inv := mineThenPlanInvoker(t)

	f.run(t, f.input(inv))

	for _, title := range append(inv.titlesFor(JudgmentJ1), inv.titlesFor(JudgmentJ2)...) {
		if title != "t-4" {
			t.Errorf("a judgment was invoked for %q, which is waiting for a human (only t-4 is a target)", title)
		}
	}
	if got := len(inv.titlesFor(JudgmentJ1)) + len(inv.titlesFor(JudgmentJ2)); got != 2 {
		t.Errorf("judgment calls for the target = %d, want 2 (J1 and J2 for %s)", got, target.ID)
	}
	acts, err := f.s.ListActivities(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListActivities: %v", err)
	}
	for _, a := range acts {
		switch a.Action {
		case "approve", "reject", "answer":
			t.Errorf("activity %s/%s %s recorded by cycle (approve/reject/answer must never be recorded)", a.Entity, a.EntityID, a.Action)
		}
		if a.Channel == string(ChannelInvoker) && a.Verification != "none" {
			t.Errorf("invoker activity has verification %q, want none", a.Verification)
		}
	}
}

func mustChallengeInt(t *testing.T, id string) int64 {
	t.Helper()
	n, ok := parseChallengeID(id)
	if !ok {
		t.Fatalf("bad challenge id %q", id)
	}
	return n
}

// --- 契機・結果・失敗（AC-141〜145・AC-2） ---

// AC-141・AC-142: 契機（trigger）を周の記録に残す。
func TestRunCycle_RecordsTrigger(t *testing.T) {
	for _, trigger := range []string{"cron", "manual"} {
		t.Run(trigger, func(t *testing.T) {
			f := newCycleFixture(t)
			in := f.input(mineThenPlanInvoker(t))
			in.Trigger = trigger
			res := f.run(t, in)
			if res.Cycle.Trigger != trigger {
				t.Errorf("Cycle.Trigger = %q, want %q", res.Cycle.Trigger, trigger)
			}
			if res.Cycle.Result != CycleResultCompleted || res.Cycle.EndedAt == nil {
				t.Errorf("Cycle = %+v, want completed with ended_at", res.Cycle)
			}
		})
	}
}

// AC-144・AC-145: 周の中の判断の呼び出しが失敗（errored）しても RunCycle はエラーを返さず、
// その課題の要素の result が errored で、課題の状態は変わらない。
func TestRunCycle_FailedRunIsAnErroredItemNotAnError(t *testing.T) {
	f := newCycleFixture(t)
	ch := f.create(t, "t-1")
	inv := &cycleFakeInvoker{handle: func(cycleCall) JudgmentLaunchOutput {
		return JudgmentLaunchOutput{Result: RunResultErrored, ErrorSummary: "boom"}
	}}

	res, err := f.s.RunCycle(context.Background(), f.input(inv))
	if err != nil {
		t.Fatalf("RunCycle returned an error %v, want the failure shown in the result only (AC-144)", err)
	}
	cls := phaseOfResult(t, res, CyclePhaseClassify)
	if len(cls.Items) != 1 || cls.Items[0].Result != RunResultErrored {
		t.Fatalf("classify items = %+v, want 1 item with result errored (AC-145)", cls.Items)
	}
	if got := f.status(t, ch.ID); got != StatusUnclassified {
		t.Errorf("status = %q, want it unchanged (%q)", got, StatusUnclassified)
	}
	if res.Cycle.Result != CycleResultCompleted {
		t.Errorf("Cycle.Result = %q, want completed", res.Cycle.Result)
	}
}

// AC-2（core 側）: agent.json の既定値を使ったことを結果の ConfigDefaultsUsed に示す。
func TestRunCycle_ConfigDefaultsUsed(t *testing.T) {
	f := newCycleFixture(t)
	f.agent.DefaultsUsed = true
	if got := f.run(t, f.input(mineThenPlanInvoker(t))).ConfigDefaultsUsed; fmt.Sprint(got) != "[agent.json]" {
		t.Errorf("ConfigDefaultsUsed = %v, want [agent.json]", got)
	}
	f2 := newCycleFixture(t)
	f2.agent.DefaultsUsed = false
	got := f2.run(t, f2.input(mineThenPlanInvoker(t))).ConfigDefaultsUsed
	if got == nil || len(got) != 0 {
		t.Errorf("ConfigDefaultsUsed = %#v, want an empty non-nil slice when nothing defaulted", got)
	}
}

// AC-127（core 側）: 周の既消費額（SpentUSD）は、終了した run の費用の合計。
func TestRunCycle_SpentUSDIsTheSumOfRunCosts(t *testing.T) {
	f := newCycleFixture(t)
	f.create(t, "t-1")
	f.create(t, "t-2")
	cost := 0.4
	inv := &cycleFakeInvoker{handle: func(c cycleCall) JudgmentLaunchOutput {
		out := succeeded(j1Output(t, j1RawOutput{Verdict: "not_mine", Reason: "r"}))
		out.ReportedTotalCostUSD = &cost
		return out
	}}
	in := f.input(inv)
	in.ConnDecl = nil

	res := f.run(t, in)

	if got := res.Cycle.SpentUSD; got < 0.7999 || got > 0.8001 {
		t.Errorf("SpentUSD = %v, want 0.8 (2 runs x 0.4)", got)
	}
}

// --- 枠超過（周をまたいで伝播） ---

// 分類の段で枠超過を記録した周は、計画の段の判断の呼び出しも起動しない（理由 rate_limited。
// 上流の取得もしない）。結果の RateLimited が立つ。
func TestRunCycle_RateLimitInClassifyPropagatesToPlanPhase(t *testing.T) {
	f := newCycleFixture(t)
	f.classified(t, "t-1", "P0") // 計画の段の対象
	f.create(t, "t-2")           // 分類の段の対象（この J1 が枠超過になる）
	inv := &cycleFakeInvoker{handle: func(c cycleCall) JudgmentLaunchOutput {
		if c.Judgment == JudgmentJ1 {
			return JudgmentLaunchOutput{Result: RunResultErrored, RateLimited: true}
		}
		return succeeded(j2OutputFor(false, nil))
	}}

	res := f.run(t, f.input(inv))

	if !res.RateLimited {
		t.Error("RateLimited = false, want true")
	}
	if got := inv.judgments(); fmt.Sprint(got) != fmt.Sprint([]JudgmentPoint{JudgmentJ1}) {
		t.Errorf("judgment calls = %v, want only the J1 that hit the limit (J2 must not start in the same cycle)", got)
	}
	plan := phaseOfResult(t, res, CyclePhasePlan)
	if len(plan.NotStarted) != 1 || plan.NotStarted[0].ChallengeID != "C-1" || plan.NotStarted[0].Reason != NotStartedRateLimited {
		t.Errorf("plan not_started = %+v, want C-1 rate_limited", plan.NotStarted)
	}
	if got := f.status(t, "C-1"); got != StatusClassified {
		t.Errorf("C-1 status = %q, want it unchanged", got)
	}
}

// --- サイクルの排他（AC-146〜148） ---

// AC-146: 生きている保持者がいれば RunCycle は ErrLocked で終わり、判断の呼び出しをしない。
func TestRunCycle_LiveHolder_ReturnsErrLockedWithoutInvoking(t *testing.T) {
	f := newCycleFixture(t)
	f.create(t, "t-1")
	if _, err := f.s.BeginCycle(context.Background(), BeginCycleInput{Trigger: "other", BudgetUSD: 10, Exclusive: true}); err != nil {
		t.Fatalf("BeginCycle (holder): %v", err)
	}
	inv := mineThenPlanInvoker(t)

	_, err := f.s.RunCycle(context.Background(), f.input(inv))

	if !errors.Is(err, ErrLocked) {
		t.Fatalf("RunCycle err = %v, want ErrLocked", err)
	}
	if got := inv.judgments(); len(got) != 0 {
		t.Errorf("judgment calls = %v, want none", got)
	}
}

// AC-148: RunCycle の終了後、排他は解放されている（続けて実行した RunCycle が ErrLocked にならない）。
// 失敗した周（段の途中のエラー）でも解放される。
func TestRunCycle_ReleasesTheLockAfterTheCycle(t *testing.T) {
	f := newCycleFixture(t)
	f.create(t, "t-1")
	f.run(t, f.input(mineThenPlanInvoker(t)))
	if _, err := f.s.RunCycle(context.Background(), f.input(mineThenPlanInvoker(t))); err != nil {
		t.Fatalf("second RunCycle err = %v, want nil (the lock must have been released)", err)
	}

	// 段の途中でエラーになる周（position_file を消す）でも、排他を残さない。
	f.create(t, "t-2")
	if err := removePositionFile(f.s, f.agent); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.RunCycle(context.Background(), f.input(mineThenPlanInvoker(t))); err == nil {
		t.Fatal("RunCycle err = nil, want an error for the missing position file")
	}
	cycles := countLocksForTest(t, f.s)
	if cycles != 0 {
		t.Errorf("lock rows after an aborted cycle = %d, want 0", cycles)
	}
}

// 排他ロックの heartbeat（段の実行中）を更新する巡回が動き、終了後に止まる。
func TestRunCycle_HeartbeatsTheLockWhileRunning(t *testing.T) {
	f := newCycleFixture(t)
	f.s.lockHeartbeatInterval = 20 * time.Millisecond
	f.create(t, "t-1")
	var first, last time.Time
	inv := &cycleFakeInvoker{handle: func(c cycleCall) JudgmentLaunchOutput {
		first = lockHeartbeatForTest(t, f.s)
		time.Sleep(150 * time.Millisecond)
		last = lockHeartbeatForTest(t, f.s)
		return succeeded(j1Output(t, j1RawOutput{Verdict: "not_mine", Reason: "r"}))
	}}
	in := f.input(inv)
	in.ConnDecl = nil

	f.run(t, in)

	if !last.After(first) {
		t.Errorf("lock heartbeat did not advance while the cycle was running: first=%v last=%v", first, last)
	}
}

// 入力の検証: Trigger が空・宣言や判断の IF が無いときは ErrValidation で、何も記録しない。
func TestRunCycle_InvalidInput_IsValidationErrorAndRecordsNothing(t *testing.T) {
	f := newCycleFixture(t)
	inv := mineThenPlanInvoker(t)
	cases := map[string]func(in *CycleRunInput){
		"empty trigger":               func(in *CycleRunInput) { in.Trigger = "" },
		"nil agent declaration":       func(in *CycleRunInput) { in.AgentDecl = nil },
		"nil invoker":                 func(in *CycleRunInput) { in.Invoker = nil },
		"connectors without upstream": func(in *CycleRunInput) { in.Upstream = nil },
		"ingest without upstream": func(in *CycleRunInput) {
			in.Ingest = &CycleIngestInput{Sources: []SourceEntry{selfOnlySource("s", []string{"o/r"}, nil)}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := f.input(inv)
			mutate(&in)
			if _, err := f.s.RunCycle(context.Background(), in); !errors.Is(err, ErrValidation) {
				t.Fatalf("err = %v, want ErrValidation", err)
			}
		})
	}
	if got := countCyclesForTest(t, f.s); got != 0 {
		t.Errorf("cycles recorded = %d, want 0", got)
	}
}

// --- テスト用のヘルパー ---

// removePositionFile は agent の position_file をワークスペースから消す（段の途中で
// ErrConfigInvalid になる周を作る）。
func removePositionFile(s *Store, agent *AgentDeclaration) error {
	return os.Remove(filepath.Join(s.Workspace(), agent.PositionFile))
}

func countLocksForTest(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM lock`).Scan(&n)
	}); err != nil {
		t.Fatalf("countLocksForTest: %v", err)
	}
	return n
}

// lockHeartbeatForTest はサイクルの排他ロックの heartbeat_at を返す。
func lockHeartbeatForTest(t *testing.T, s *Store) time.Time {
	t.Helper()
	var at time.Time
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		l, err := loadLockByName(context.Background(), tx, cycleLockName)
		if err != nil {
			return err
		}
		if l == nil {
			return fmt.Errorf("no cycle lock row")
		}
		at = l.HeartbeatAt
		return nil
	}); err != nil {
		t.Fatalf("lockHeartbeatForTest: %v", err)
	}
	return at
}
