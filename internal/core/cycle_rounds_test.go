package core

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// このファイルは、サイクルの回りの繰り返し（区切り S4a。Issue #991）を、実ストア・偽の ContractClient・偽の判断の IF・
// 差し替えた待ちで確かめる。委譲先（偽の ContractClient）は、状態の読み取りのたびに、決めた順に状態を進める。

const roundsPollSec = 7

// roundsFixture は、form: cli の委譲先を持つ、着手中で承認済みの計画付きの課題 1 件（C-1。統合先を使わない）を持つフィクスチャ。
type roundsFixture struct {
	*reviewFlowFixture
	mu    sync.Mutex
	waits []time.Duration
	// onWait は待ちの差し替えが呼ばれるたび（waits へ足した後）に呼ぶ。nil でなければ、返したエラーが待ちのエラーになる。
	onWait func(ctx context.Context, n int) error
	// j5 の判定を呼ばれた順に返す（尽きたら最後のものを返す）。
	verdicts []string
	judged   []JudgmentPoint
	wg       *fakeWorkspaceGit
}

func newRoundsFixture(t *testing.T) *roundsFixture {
	t.Helper()
	rf := newReviewFlowFixture(t)
	rf.agent.CyclePollIntervalSec = roundsPollSec
	rf.setPR(1, reviewPRURL, "open", "main")
	rf.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f := &roundsFixture{reviewFlowFixture: rf, wg: &fakeWorkspaceGit{}}
	return f
}

func (f *roundsFixture) wait(ctx context.Context, d time.Duration) error {
	f.mu.Lock()
	f.waits = append(f.waits, d)
	n := len(f.waits)
	f.mu.Unlock()
	if f.onWait != nil {
		return f.onWait(ctx, n)
	}
	return ctx.Err()
}

func (f *roundsFixture) invoker(t *testing.T) *cycleFakeInvoker {
	t.Helper()
	return &cycleFakeInvoker{handle: func(c cycleCall) JudgmentLaunchOutput {
		f.mu.Lock()
		f.judged = append(f.judged, c.Judgment)
		f.mu.Unlock()
		switch c.Judgment {
		case JudgmentJ3:
			return JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: briefJSON("BRIEF"), ReportedTotalCostUSD: float64Ptr(0.1)}
		case JudgmentJ5:
			f.mu.Lock()
			defer f.mu.Unlock()
			v := "met"
			if len(f.verdicts) > 0 {
				v = f.verdicts[0]
				if len(f.verdicts) > 1 {
					f.verdicts = f.verdicts[1:]
				}
			}
			return j5Out(v, map[bool]any{true: "FEEDBACK-TEXT", false: nil}[v == "not_met"], nil)
		}
		return JudgmentLaunchOutput{Result: RunResultErrored}
	}}
}

func (f *roundsFixture) input(inv JudgmentInvoker) CycleRunInput {
	f.j5.checks.prs["o/r#1"] = prChecks("open", checkDone)
	return CycleRunInput{
		Trigger: "manual", AgentDecl: f.agent, ConnDecl: f.conn, Invoker: inv, Upstream: f.upstream, Delegate: f.deleg, Git: f.git,
		Reconcile: f.branches, Checks: f.j5.checks, Diffs: &fakeDiffs{}, HeadGit: &noHeadGit{}, Contract: f.client,
		WorkspaceGit: f.wg, Wait: f.wait,
	}
}

func (f *roundsFixture) judgedCount(j JudgmentPoint) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, x := range f.judged {
		if x == j {
			n++
		}
	}
	return n
}

func (f *roundsFixture) waited() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration{}, f.waits...)
}

func (f *roundsFixture) runCycle(t *testing.T, in CycleRunInput) *CycleRunResult {
	t.Helper()
	res, err := f.s.RunCycle(context.Background(), in)
	if err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	return res
}

// 委譲先の状態の読みを、見たい順に並べる（最初の読みは「作業中」。start の戻りは作業中）。
func (f *roundsFixture) reads(outs ...ContractLaunchOutput) { f.client.queue = outs }

// AC-1: 統合先を使わない課題が、委譲先の「作業中 → レビュー待ち」と J5 の「満たした」で、1 回のサイクルで完了の承認待ちになる。
// AC-6: いま始められるものが 0 件で作業中の実行が 1 件以上の回りの終わりは、待ちが 1 回、宣言の間隔の長さで呼ばれてから次の回りが始まる。
// AC-20（の後半）: 落ち着いた回りの後は、次の回りが始まらない（end_reason は settled）。
func TestRunCycle_Rounds_AC1_OneCycleReachesCompletionApproval(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(contractOut(ContractStateRunning, nil, nil), reviewWaitOut())

	res := f.runCycle(t, f.input(f.invoker(t)))

	if got := f.status(t, f.cid); got != StatusAwaitingCompletionApproval {
		t.Fatalf("status = %s, want awaiting_completion_approval", got)
	}
	if res.Cycle.EndReason != CycleEndSettled || res.Cycle.Result != CycleResultCompleted {
		t.Errorf("end_reason = %q result = %q, want settled/completed", res.Cycle.EndReason, res.Cycle.Result)
	}
	if res.Cycle.Rounds < 3 {
		t.Errorf("rounds = %d, want at least 3 (start, running read, review read)", res.Cycle.Rounds)
	}
	for _, d := range f.waited() {
		if d != roundsPollSec*time.Second {
			t.Errorf("wait length = %v, want the declared interval %ds", d, roundsPollSec)
		}
	}
	if got := len(f.waited()); got != 2 {
		t.Errorf("waits = %d, want 2 (after the start round and after the still-running read)", got)
	}
	if n := f.judgedCount(JudgmentJ5); n != 1 {
		t.Errorf("J5 calls = %d, want 1", n)
	}
	// AC-24: 作業中の実行を読み直すだけの回り（いま始められるものが 0 件）では、判断点を 1 回も呼ばない（呼んだのは、レビュー待ちを読んだ回りの J5 の 1 回だけ）。
	if len(f.judged) != 1 {
		t.Errorf("judgments = %v, want only the one J5 of the round that read the review wait", f.judged)
	}
}

func (f *roundsFixture) startOut(o ContractLaunchOutput) { f.client.startOut = o }

// AC-3・AC-4・AC-5: J5 が 1 度「満たしていない」を返し、次に「満たした」を返す → 1 回のサイクルで完了の承認待ちになり、
// 差し戻し（respond）は、その実行にちょうど 1 回返る。差し戻しを返す番（いま始められるもの 1 件）で進みがあった回りの後は、待たずに次の回りへ進む。
func TestRunCycle_Rounds_AC3_AC4_AC5_NotMetThenMetRespondsOnce(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(contractOut(ContractStateRunning, nil, nil), reviewWaitOut(), reviewWaitOut())
	f.verdicts = []string{"not_met", "met"}
	f.seedLearnAndImprove(t)

	res := f.runCycle(t, f.input(f.invoker(t)))

	if got := f.status(t, f.cid); got != StatusAwaitingCompletionApproval {
		t.Fatalf("status = %s, want awaiting_completion_approval (judged %v)", got, f.judged)
	}
	if len(f.client.resumes) != 1 || !strings.Contains(strings.Join(f.client.resumes[0], " "), "--choice respond") {
		t.Fatalf("resumes = %v, want exactly one respond (AC-4)", f.client.resumes)
	}
	if n := f.judgedCount(JudgmentJ5); n != 2 {
		t.Errorf("J5 calls = %d, want 2", n)
	}
	// 待ちは、start の回り・作業中を読み直しただけの回り・差し戻しを返した回りの後の 3 回。J5 の差し戻しの回りの後（差し戻しを返す番が 1 件・進みあり）は待たない（AC-5）。
	if got := len(f.waited()); got != 3 {
		t.Errorf("waits = %d, want 3 (the round after the not_met verdict must not wait; AC-5)", got)
	}
	if res.Cycle.EndReason != CycleEndSettled {
		t.Errorf("end_reason = %q, want settled", res.Cycle.EndReason)
	}
	// AC-26〜AC-28: 回りが何回あっても、終わりの段（学び・改修の提案・書き出し）は最後に 1 回だけ通る。
	if res.Cycle.Rounds < 3 {
		t.Fatalf("rounds = %d, want at least 3 for this check", res.Cycle.Rounds)
	}
	if n := f.judgedCount(JudgmentJ6); n != 1 {
		t.Errorf("J6 calls = %d, want exactly 1 (the learn phase runs once at the end; judged %v)", n, f.judged)
	}
	if n := f.judgedCount(JudgmentJ8); n != 1 {
		t.Errorf("J8 calls = %d, want exactly 1 (the improve phase runs once at the end; judged %v)", n, f.judged)
	}
	if n := countCalls(f.wg.calls, "IsWorkTree"); n != 1 {
		t.Errorf("backup phase ran %d times (calls %v), want once", n, f.wg.calls)
	}
}

// seedLearnAndImprove は、学びの段（原因が未記入の発生を持つ完了の課題）と改修の提案の段（しきい値 1 に達する発生）に、対象を 1 つずつ置く。
// どちらも、数えの「いま始められるもの」には数えない。
func (f *delegateFixture) seedLearnAndImprove(t *testing.T) {
	t.Helper()
	cid := mChallenge(t, f.s, "done one")
	f.agent.ImproveThreshold = 1
	mexec(t, f.s, `INSERT INTO activity (at, actor, channel, verification, entity, entity_id, action) VALUES ('2026-03-01T00:00:00.000Z', 'a', 'cli', 'none', 'challenge', ?, 'x')`, cid)
	act := j8MaxID(t, f.s, "activity")
	mexec(t, f.s, `INSERT INTO occurrence (challenge_id, kind, activity_id, outcome, source, created_at)
		VALUES (?, 'plan_rejected', ?, 'bad', 'core', '2026-03-01T00:00:00.000Z')`, cid, act)
	j8Occ(t, f.s, cid, j8Row{})
}

func countCalls(calls []string, name string) int {
	n := 0
	for _, c := range calls {
		if c == name {
			n++
		}
	}
	return n
}

// verifyingWithPendingChecks は、C-1 を、レビュー待ちを読んで検証中にし、PR のチェックが終わらない状態にする設定。
func (f *roundsFixture) pendingChecks(t *testing.T) CycleRunInput {
	t.Helper()
	in := f.input(f.invoker(t))
	f.j5.checks.prs["o/r#1"] = prChecks("open", checkDone, checkRunning)
	return in
}

// AC-7・AC-8・AC-12・AC-13・AC-14: いま始められるものがあり、作業中の実行が 0 件のまま進みの無い回りが 3 回続くと、次の回りは始まらず、
// end_reason は stalled になる。進みの無い回りの後は、待ちが 1 回置かれる（3 回目の後は置かず、終える）。学びの段と書き出しの段を通り、result は completed。
func TestRunCycle_Rounds_AC7_AC8_AC12_AC13_AC14_StalledAfterThreeRoundsWithoutProgress(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(reviewWaitOut())
	f.seedLearnAndImprove(t)
	in := f.pendingChecks(t)

	res := f.runCycle(t, in)

	if res.Cycle.EndReason != CycleEndStalled || res.Cycle.Result != CycleResultCompleted { // AC-8・AC-14
		t.Fatalf("end_reason = %q result = %q, want stalled/completed", res.Cycle.EndReason, res.Cycle.Result)
	}
	// 回り: ① 起動（進みあり・作業中 1 → 待つ）② レビュー待ちを読む（進みあり → 待たずに次へ）③ ④（進み無し → 待つ）⑤（進み無し 3 回目 → 終える）。
	if res.Cycle.Rounds != 5 {
		t.Errorf("rounds = %d, want 5", res.Cycle.Rounds)
	}
	if got := len(f.waited()); got != 3 { // AC-7（進み無しの回りの後に待つ）
		t.Errorf("waits = %d, want 3", got)
	}
	if n := f.judgedCount(JudgmentJ5); n != 0 {
		t.Errorf("J5 calls = %d, want 0 (the checks never finish)", n)
	}
	if n := f.judgedCount(JudgmentJ6); n != 1 { // AC-12
		t.Errorf("J6 calls = %d, want 1 (the learn phase ran)", n)
	}
	if n := countCalls(f.wg.calls, "IsWorkTree"); n != 1 { // AC-13
		t.Errorf("backup phase ran %d times, want once (calls %v)", n, f.wg.calls)
	}
}

// 回りの繰り返しの終わり方の決まり（AC-5〜AC-10・AC-20）。回りの終わりの数えと進みの有無から、次の動きを決める。
func TestRoundGovernor_Decide(t *testing.T) {
	t.Parallel()
	startable := &RoundWork{Startable: 1}
	startableActive := &RoundWork{Startable: 1, Active: 1}
	onlyActive := &RoundWork{Active: 1}
	nothing := &RoundWork{}
	type step struct {
		progressed bool
		work       *RoundWork
		want       roundAction
	}
	cases := []struct {
		name  string
		steps []step
	}{
		{"AC-5 始められるものがあり進みがあれば、待たずに次の回りへ", []step{{true, startable, roundNext}}},
		{"AC-6 始められるものが無く作業中の実行があれば、待つ", []step{{false, onlyActive, roundWait}, {true, onlyActive, roundWait}}},
		{"AC-7 始められるものがあり進みが無ければ、待つ", []step{{false, startable, roundWait}}},
		{"AC-8 作業中の実行が無いまま、進みの無い回りが 3 回続けば、終える", []step{{false, startable, roundWait}, {false, startable, roundWait}, {false, startable, roundStalled}}},
		{"AC-9 作業中の実行があるあいだは、進みの無い回りが何回続いても終えない", []step{
			{false, startableActive, roundWait}, {false, startableActive, roundWait}, {false, startableActive, roundWait},
			{false, startableActive, roundWait}, {false, startableActive, roundWait}}},
		{"AC-10 進みの無い 2 回・作業中の実行がある回り・進みの無い 2 回は、終えない", []step{
			{false, startable, roundWait}, {false, startable, roundWait}, {false, startableActive, roundWait},
			{false, startable, roundWait}, {false, startable, roundWait}}},
		{"進みのあった回りも、続いた数を 0 に戻す", []step{
			{false, startable, roundWait}, {false, startable, roundWait}, {true, startable, roundNext},
			{false, startable, roundWait}, {false, startable, roundWait}, {false, startable, roundStalled}}},
		{"AC-20 始められるものも作業中の実行も無ければ、次の回りは始めない", []step{{true, nothing, roundSettled}, {false, nothing, roundSettled}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var g roundGovernor
			for i, st := range tc.steps {
				if got := g.decide(st.progressed, st.work); got != st.want {
					t.Fatalf("step %d: decide(progressed=%v, %+v) = %v, want %v", i, st.progressed, *st.work, got, st.want)
				}
			}
		})
	}
}

// AC-11: 判断点が同じ課題で失敗を返しても、状態は変わらず失敗の記録（run）だけが増えるので、指紋（進みの判定）は変わらない。
// 回りとしては、始められるものが残っているのに進みが無いので、待ちが置かれる。
func TestProgressFingerprint_AC11_FailedJudgmentRecordsAreNotProgress(t *testing.T) {
	t.Parallel()
	f := newCycleFixture(t)
	f.create(t, "t-1")
	ctx := context.Background()
	inv := &cycleFakeInvoker{handle: func(cycleCall) JudgmentLaunchOutput {
		return JudgmentLaunchOutput{Result: RunResultErrored, ErrorSummary: "boom"}
	}}
	cyc, err := f.s.BeginCycle(ctx, BeginCycleInput{Trigger: "t", BudgetUSD: 10})
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.s.progressFingerprint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.s.ClassifyAutoJ1(ctx, J1AutoInput{AgentDecl: f.agent, Invoker: inv, CycleID: cyc.ID})
	if err != nil || len(res.Items) != 1 || res.Items[0].Result != RunResultErrored {
		t.Fatalf("ClassifyAutoJ1 = %+v, %v, want one errored item", res, err)
	}
	after, err := f.s.progressFingerprint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Errorf("fingerprint changed after a failed judgment, want it unchanged (a failure record is not progress)")
	}
	var runs int
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM run WHERE judgment = 'J1'`).Scan(&runs)
	})
	if runs != 1 {
		t.Errorf("J1 runs = %d, want 1 failure record", runs)
	}

	var waits []time.Duration
	in := f.input(inv)
	in.ConnDecl = nil
	in.Wait = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return ctx.Err() }
	f.run(t, in)
	if len(waits) < 1 {
		t.Errorf("waits = %v, want a wait after a round that only recorded a failure", waits)
	}
}

// AC-29: 1 回目の回りで返事（respond）を返した実行に、2 回目以降の回りで、次の返事を返せる（見送りは、回りごとに作り直す）。
// AC-31: 1 つの回りの中で、同じ実行の状態の読み取り（commands.status）は 1 回だけ呼ばれる。
func TestRunCycle_Rounds_AC29_AC31_RespondsAgainInALaterRoundAndReadsStatusOncePerRound(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(contractOut(ContractStateRunning, nil, nil), reviewWaitOut(), reviewWaitOut(), reviewWaitOut())
	f.verdicts = []string{"not_met", "not_met", "met"}
	var perRound []int // 待ちごと（回りごと）の、その回りの状態の読み取りの回数
	prev := 0
	f.onWait = func(ctx context.Context, _ int) error {
		perRound = append(perRound, len(f.client.statuses)-prev)
		prev = len(f.client.statuses)
		return ctx.Err()
	}

	f.runCycle(t, f.input(f.invoker(t)))

	if got := f.status(t, f.cid); got != StatusAwaitingCompletionApproval {
		t.Fatalf("status = %s, want awaiting_completion_approval", got)
	}
	if len(f.client.resumes) != 2 {
		t.Fatalf("resumes = %v, want 2 responds (one per round that had a not_met verdict; AC-29)", f.client.resumes)
	}
	for i, n := range perRound {
		if n > 1 {
			t.Errorf("round before wait %d read the status %d times, want at most 1 (AC-31)", i+1, n)
		}
	}
	if total := len(f.client.statuses); total != 4 {
		t.Errorf("status reads = %d, want 4 (one per round that read the run)", total)
	}
}

// AC-30: 1 回目の回りで使った額は、2 回目以降の回りの、サイクルの費用の上限の評価に入る。
func TestRunCycle_Rounds_AC30_SpendOfAnEarlierRoundCountsAgainstTheCycleBudget(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(contractOut(ContractStateRunning, nil, nil), reviewWaitOut(), reviewWaitOut())
	f.verdicts = []string{"not_met", "met"}
	// 1 回目の J5 は上限の内（評価額 299.8 ≤ 300）。その J5 が使った 0.5 USD を足すと、2 回目の J5 の評価額は上限を超える（0.5 + 299.8 > 300）。
	f.agent.CycleBudgetUSD = 300
	f.agent.JudgmentBudgetUSD.J5 = 299.8

	res := f.runCycle(t, f.input(f.invoker(t)))

	if n := f.judgedCount(JudgmentJ5); n != 1 {
		t.Fatalf("J5 calls = %d, want 1 (the second must be refused by the cycle budget including the first round's spend)", n)
	}
	if res.Cycle.EndReason != CycleEndBudget {
		t.Errorf("end_reason = %q, want budget", res.Cycle.EndReason)
	}
	verify := phaseOfResult(t, res, CyclePhaseVerify)
	found := false
	for _, ns := range verify.NotStarted {
		found = found || ns.Reason == NotStartedCycleBudget
	}
	if !found {
		t.Errorf("verify not_started = %+v, want a cycle_budget entry", verify.NotStarted)
	}
}

// AC-36: 待ちの間（差し替えた待ちの中で、heartbeat の周期より長く時計を進める）、ロックの heartbeat の時刻は進んでいる。
func TestRunCycle_Rounds_AC36_LockHeartbeatAdvancesDuringTheWait(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(contractOut(ContractStateRunning, nil, nil), reviewWaitOut())
	base := time.Now()
	var offset atomic.Int64
	f.s.now = func() time.Time { return base.Add(time.Duration(offset.Load())) }
	f.s.lockHeartbeatInterval = 10 * time.Millisecond
	var first, during, after time.Time
	f.onWait = func(ctx context.Context, n int) error {
		if n == 2 { // 1 回目の待ちが戻った後（周が次の回りの前に更新した後）の時刻
			after = lockHeartbeatNoFatal(t, f.s)
		}
		if n != 1 {
			return ctx.Err()
		}
		first = lockHeartbeatNoFatal(t, f.s)
		offset.Add(int64(10 * time.Minute)) // 周期（既定 60 秒）も、回収の時間（300 秒）も超えて進める
		time.Sleep(100 * time.Millisecond)
		during = lockHeartbeatNoFatal(t, f.s)
		return ctx.Err()
	}

	f.runCycle(t, f.input(f.invoker(t)))

	if !during.After(first.Add(5 * time.Minute)) {
		t.Errorf("heartbeat during the wait = %v, first = %v, want it advanced with the clock", during, first)
	}
	if after.Before(during) {
		t.Errorf("heartbeat after the wait = %v, before = %v, want it not to go back", after, during)
	}
}

// delegateCycleInput は、委譲の段の偽（delegateFixture）から、サイクルの入力を作る。判断点は何も起動されない（起動されたら失敗を返す）。
func (f *delegateFixture) delegateCycleInput(c ContractClient, wait func(context.Context, time.Duration) error, checks UpstreamCheckSource) CycleRunInput {
	inv := &cycleFakeInvoker{handle: func(cycleCall) JudgmentLaunchOutput { return JudgmentLaunchOutput{Result: RunResultErrored} }}
	return CycleRunInput{
		Trigger: "manual", AgentDecl: f.agent, ConnDecl: f.conn, Invoker: inv, Upstream: f.upstream, Delegate: f.deleg, Git: f.git,
		Reconcile: f.branches, Checks: checks, Diffs: &fakeDiffs{}, HeadGit: &noHeadGit{}, Contract: c,
		WorkspaceGit: &fakeWorkspaceGit{}, Wait: wait,
	}
}

// AC-25: オーナーの回答待ちの実行だけが残っている状態で、サイクルは settled で終わる（待ち続けない）。
func TestRunCycle_Rounds_AC25_OwnerWaitingRunOnlyEndsSettled(t *testing.T) {
	t.Parallel()
	f := newOwnerWaitFixture(t, 2)
	var waits []time.Duration
	wait := func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		if len(waits) > 8 {
			return context.Canceled // 取りこぼしても無限に回らない
		}
		f.pc.set(f.extOf(f.first), waitingOut(&ContractRequestedAction{Kind: "answer", Decider: "human", Text: "どうする"}, nil))
		return ctx.Err()
	}
	res, err := f.s.RunCycle(context.Background(), f.delegateCycleInput(f.pc, wait, newFakeChecks()))
	if err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if res.Cycle.EndReason != CycleEndSettled {
		t.Errorf("end_reason = %q, want settled", res.Cycle.EndReason)
	}
	f.assertRun(t, f.first, "waiting", StatusAwaitingHuman)
	if len(waits) != 1 {
		t.Errorf("waits = %d, want 1 (only while the run was working)", len(waits))
	}
}

// AC-38: 走っているサイクルの回りの間に、保留への回答が記録された → 次の回りで、その回答が委譲先への再開（commands.resume）に入る。
// AC-39: 回りの間に、別の課題の計画の承認が記録された（着手中で承認済みの計画付きになった）→ 次の回りで、その課題のタスクが起動される。
func TestRunCycle_Rounds_AC38_AC39_ApprovalsRecordedBetweenRoundsAreReadByTheNextRound(t *testing.T) {
	t.Parallel()
	f := newOwnerWaitFixture(t, 3)
	f.newOther(t) // C-2（other-repo）。作業中のまま残って、サイクルを回し続けさせる
	var third string
	var secondID string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	wait := func(wctx context.Context, _ time.Duration) error {
		calls++
		if calls > 8 { // 取りこぼしても無限に回らない
			cancel()
			return wctx.Err()
		}
		switch {
		case len(f.pc.resumes) == 0 && f.status(t, f.first) == StatusInProgress && f.startedFor(f.first) == 1 && !f.held(t):
			// 1 回目の待ち: C-1 の委譲先がオーナーへの問いを返す。
			f.pc.set(f.extOf(f.first), waitingOut(&ContractRequestedAction{Kind: "answer", Decider: "human", Text: "どうする"}, nil))
		case f.status(t, f.first) == StatusAwaitingHuman:
			// 2 回目の待ち: オーナーの回答が記録される。別の課題の計画の承認も記録される（着手中で承認済みの計画付きの課題が増える）。
			f.answerFirst(t)
			secondID = f.newInProgress(t, "T3", "P2", planSpec(nil))
		default:
			// 3 回目の待ち: 回答が再開に入った・新しい課題のタスクが起動された。
			if len(f.pc.resumes) == 1 {
				third = strings.Join(f.pc.resumes[0], " ")
			}
			cancel()
		}
		return wctx.Err()
	}
	_, err := f.s.RunCycle(ctx, f.delegateCycleInput(f.pc, wait, newFakeChecks()))
	if err == nil {
		t.Fatal("RunCycle err = nil, want the cancellation")
	}
	if !strings.Contains(third, "ANSWER-MARKER") { // AC-38
		t.Errorf("resume of the next round = %q, want it to carry the recorded answer", third)
	}
	if secondID == "" || f.startedFor(secondID) != 1 { // AC-39
		t.Errorf("starts = %v, want the task of %s started by the next round", f.pc.starts, secondID)
	}
}

// extOf は課題 id の契約の実行 ID（start の呼ばれた順に ext-1, ext-2, …。課題の起動は並行なので、順は決まっていない）。
func (f *ownerWaitFixture) extOf(id string) string {
	for i, args := range f.pc.starts {
		if args[len(args)-1] == id {
			return fmt.Sprintf("ext-%d", i+1)
		}
	}
	return ""
}

func (f *ownerWaitFixture) held(t *testing.T) bool {
	t.Helper()
	return f.status(t, f.first) == StatusAwaitingHuman
}

// routedClient は、running に載せた実行 ID の状態の確認には「作業中」を返し、それ以外は元のクライアントへ渡す。
type routedClient struct {
	ContractClient
	running map[string]bool
}

func (c routedClient) Status(ctx context.Context, in ContractLaunchInput) (ContractLaunchOutput, error) {
	if id := in.Command[2]; c.running[id] {
		return contractOut(ContractStateRunning, nil, func(st *ContractStatus) { st.RunID = id }), nil
	}
	return c.ContractClient.Status(ctx, in)
}

// addRunningContractRun は、着手中の別の課題と、その課題の終わっていない契約の実行（ext）を 1 本置く（サイクルを回し続けさせる作業中の実行）。
func addRunningContractRun(t *testing.T, s *Store, ext string) {
	t.Helper()
	c := createAndAdvance(t, s, StatusInProgress)
	cid := mustParseChallengeID(t, c.ID)
	ctx := context.Background()
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		now := time.Now()
		row, err := insertRun(ctx, tx, cid, insertRunInput{Kind: runKindContract, ChallengeVersion: 1, SessionID: "33333333-3333-4333-8333-333333333333",
			PID: 1, Host: "h", HeartbeatAt: now, StartedAt: now, MaxBudgetUSD: 1, BudgetBucket: budgetBucketImpl})
		if err != nil {
			return err
		}
		id, _ := parseRunID(row.ID)
		if _, err := updateRunEnd(ctx, tx, id, updateRunEndInput{EndedAt: now, Result: RunResultSucceeded}); err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, cost_usd, max_budget_usd, started_at, observed_at)
			VALUES (?, 'tool', ?, ?, 'running', 's', 0, 5000000, 'x', 'x')`, cid, ext, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// AC-40: 走っているサイクルの回りの間に、完了と反映の承認が記録された → 次の回りで、委譲先へ「入れてよい」（land）が返る。
func TestRunCycle_Rounds_AC40_ApprovalRecordedBetweenRoundsReturnsLandInTheNextRound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cf := newContractFixtureWith(t, landConnectorsJSON)
	j := &j5Fixture{delegateFixture: cf.delegateFixture, checks: newFakeChecks(), j5: &fakeJudgmentInvoker{result: j5Out("met", nil, nil)}}
	verified := prChecks("open", checkDone)
	verified.HeadSHA = reviewContentArtifact.Ref
	j.checks.prs["o/r#1"] = verified
	f := &reviewFlowFixture{contractFixture: cf, j5: j}
	f.setPR(1, reviewPRURL, "open", "main")
	f.toVerifying(t)
	if _, err := f.s.VerifyAutoJ5(ctx, f.j5.input(f.cycle(t, 300), &f.cid)); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t, f.cid); got != StatusAwaitingCompletionApproval {
		t.Fatalf("status = %s, want awaiting_completion_approval", got)
	}
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	addRunningContractRun(t, f.s, "ext-9") // 別の課題の作業中の実行。サイクルを、承認が記録される回りまで回し続ける
	pr := openPR()
	pr.URL, pr.HeadSHA = reviewPRURL, reviewContentArtifact.Ref
	var landAtFirst, landAtSecond int
	landCalls := func() int {
		n := 0
		for _, r := range f.client.resumes {
			if strings.Contains(strings.Join(r, " "), "--choice land") {
				n++
			}
		}
		return n
	}
	calls := 0
	wait := func(wctx context.Context, _ time.Duration) error {
		calls++
		switch calls {
		case 1:
			landAtFirst = landCalls()
			prev, err := f.s.PrepareApproval(ctx, f.cid)
			if err != nil || len(prev.PendingReleases) != 1 {
				t.Errorf("PrepareApproval = %+v err %v", prev, err)
				return context.Canceled
			}
			head := reviewContentArtifact.Ref
			if _, _, err := f.s.ExecuteApproval(ctx, ApprovalRequest{
				ChallengeID: f.cid, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
				ReleaseHeads: []ReleaseHead{{Operation: prev.PendingReleases[0], HeadSHA: &head}},
			}, verifiedAttestationForTest(t, f.cid)); err != nil {
				t.Errorf("ExecuteApproval: %v", err)
				return context.Canceled
			}
			return wctx.Err()
		default:
			landAtSecond = landCalls()
			return context.Canceled // 確かめたいのは、承認の次の回りまで
		}
	}
	in := f.delegateCycleInput(routedClient{ContractClient: f.client, running: map[string]bool{"ext-9": true}}, wait, &fakeRelease{pr: pr})
	_, _ = f.s.RunCycle(ctx, in)

	if landAtFirst != 0 {
		t.Fatalf("land was returned before the approval was recorded (%d)", landAtFirst)
	}
	if landAtSecond != 1 {
		t.Fatalf("land returned %d times by the round after the approval, want 1 (resumes %v)", landAtSecond, f.client.resumes)
	}
}

// 統合先を使う課題のタスクが、前提のタスクが済んだ次の回りで起動される（1 回のサイクルの中で、前提の後のタスクへ進む）。
// （2 つのタスクが「レビュー待ち → 検証 → 統合先へ入れる」を経て課題の単位の確かめまで進む通しは、この試験の範囲に入れていない。）
func TestRunCycle_Rounds_IntegrationTaskStartsInTheRoundAfterItsPrerequisiteIsDone(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "chain", "P1", true, multiTask{key: "a"}, multiTask{key: "b", deps: []string{"a"}})
	cid, _ := parseChallengeID(id)
	calls := 0
	wait := func(wctx context.Context, _ time.Duration) error {
		calls++
		switch calls {
		case 1:
			if got := f.startedTasks(t); !equalStrings(got, []string{f.taskID(t, id, "a")}) {
				t.Errorf("started before the prerequisite is done = %v, want only a", got)
			}
			setTaskStatusRaw(t, f.s, cid, "a", TaskStatusDone) // 前提のタスクが済んだことが記録される
			return wctx.Err()
		default:
			return context.Canceled
		}
	}
	_, _ = f.s.RunCycle(context.Background(), f.delegateCycleInput(f.client, wait, newFakeChecks()))

	want := []string{f.taskID(t, id, "a"), f.taskID(t, id, "b")}
	if got := f.startedTasks(t); !equalStrings(got, want) {
		t.Fatalf("started = %v, want %v (b starts in the round after a is done)", got, want)
	}
}
