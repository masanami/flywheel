package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// 終わった理由・止める指示・終わりの段の通し方（区切り S4a の AC-41〜72・AC-158・AC-159・AC-166〜169）。

func (f *roundsFixture) requestStop(t *testing.T) {
	t.Helper()
	mexec(t, f.s, `UPDATE cycle SET stop_requested_at = '2026-11-22T00:00:00.000Z' WHERE ended_at IS NULL`)
}

func (f *roundsFixture) cancels() int {
	f.client.mu.Lock()
	defer f.client.mu.Unlock()
	return len(f.client.cancels)
}

func (f *roundsFixture) backupRan() bool { return countCalls(f.wg.calls, "IsWorkTree") == 1 }

// AC-41〜43・AC-58・AC-63・AC-66: 落ち着いて終わったサイクルは settled・completed で、学び・改修の提案・書き出しの段を通る。
func TestRunCycle_End_Settled_RunsEndPhases(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(contractOut(ContractStateRunning, nil, nil), reviewWaitOut())
	f.seedLearnAndImprove(t)
	res := f.runCycle(t, f.input(f.invoker(t)))
	if res.Cycle.EndReason != CycleEndSettled || res.Cycle.Result != CycleResultCompleted || res.Cycle.Rounds < 1 {
		t.Fatalf("got %q/%q rounds=%d", res.Cycle.EndReason, res.Cycle.Result, res.Cycle.Rounds)
	}
	if f.judgedCount(JudgmentJ6) != 1 || !f.backupRan() {
		t.Errorf("learn/backup not run: J6=%d backup=%v", f.judgedCount(JudgmentJ6), f.backupRan())
	}
	if len(res.Phases) != 10 {
		t.Errorf("phases = %d, want 10", len(res.Phases))
	}
}

// AC-159・AC-158・AC-166〜169: 間隔 60 秒の待ちの 5 秒の所で止める指示を入れる → 待ちは打ち切られ、stopped で終わる。
// 学びと改修の提案は通さず、書き出しは通る。委譲先の実行は取り消さない。
func TestRunCycle_End_StopDuringTheWaitEndsStopped(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.agent.CyclePollIntervalSec = 60
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(contractOut(ContractStateRunning, nil, nil))
	f.onWait = func(_ context.Context, _ int) error { f.requestStop(t); return nil }

	res := f.runCycle(t, f.input(f.invoker(t)))

	if res.Cycle.EndReason != CycleEndStopped || res.Cycle.Result != CycleResultCompleted {
		t.Fatalf("got %q/%q, want stopped/completed", res.Cycle.EndReason, res.Cycle.Result)
	}
	if w := f.waited(); len(w) != 1 || w[0] != 5*time.Second {
		t.Errorf("waits = %v, want one wait of 5s", w)
	}
	if f.judgedCount(JudgmentJ6) != 0 || f.judgedCount(JudgmentJ8) != 0 {
		t.Errorf("end phases ran: %v", f.judged)
	}
	if !f.backupRan() {
		t.Error("backup did not run")
	}
	if f.cancels() != 0 {
		t.Errorf("cancel called %d times", f.cancels())
	}
}

// 長い間隔は 5 秒ずつに区切って待つ（止める指示が無ければ、合計は間隔の長さ）。
func TestRunCycle_End_LongIntervalIsWaitedInFiveSecondSteps(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.agent.CyclePollIntervalSec = 12
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(contractOut(ContractStateRunning, nil, nil), reviewWaitOut())
	f.runCycle(t, f.input(f.invoker(t)))
	w := f.waited()
	if len(w) < 3 || w[0] != 5*time.Second || w[1] != 5*time.Second || w[2] != 2*time.Second {
		t.Errorf("waits = %v, want 5s,5s,2s...", w)
	}
}

// AC-158: 止める指示が始まる前から入っているサイクルは、どの段へも進まず stopped で終わる。
func TestRunCycle_End_StopBeforeFirstPhaseRunsNoPhase(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	in := f.input(f.invoker(t))
	in.OnBegin = func(string) { f.requestStop(t) }
	res := f.runCycle(t, in)
	if res.Cycle.EndReason != CycleEndStopped {
		t.Fatalf("end_reason = %q", res.Cycle.EndReason)
	}
	if len(f.judged) != 0 || len(f.client.starts) != 0 {
		t.Errorf("judged=%v starts=%d, want none", f.judged, len(f.client.starts))
	}
	if !f.backupRan() || f.cancels() != 0 {
		t.Errorf("backup=%v cancels=%d", f.backupRan(), f.cancels())
	}
	if p := phaseOfResult(t, res, CyclePhaseVerify); p.Phase != CyclePhaseVerify {
		t.Error("verify phase missing")
	}
}

// AC-48・AC-67〜69: 段のエラーと止める指示の両方 → error・aborted。書き出しは通る。
func TestRunCycle_End_ErrorWinsOverStop(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(contractOut(ContractStateRunning, nil, nil))
	boom := errors.New("boom")
	f.onWait = func(_ context.Context, _ int) error { f.requestStop(t); return boom }
	_, err := f.s.RunCycle(context.Background(), f.input(f.invoker(t)))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	c := f.lastCycle(t)
	if c.EndReason != CycleEndError || c.Result != CycleResultAborted {
		t.Errorf("got %q/%q", c.EndReason, c.Result)
	}
	if !f.backupRan() {
		t.Error("backup did not run")
	}
}

// AC-70〜72: 実行の取り消し（ctx）→ interrupted。学びの段は通らない。
func TestRunCycle_End_ContextCancelEndsInterrupted(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(contractOut(ContractStateRunning, nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.onWait = func(c context.Context, _ int) error { cancel(); return c.Err() }
	_, err := f.s.RunCycle(ctx, f.input(f.invoker(t)))
	if err == nil {
		t.Fatal("want an error")
	}
	c := f.lastCycle(t)
	if c.EndReason != CycleEndInterrupted || c.Result != CycleResultInterrupted {
		t.Errorf("got %q/%q", c.EndReason, c.Result)
	}
	if f.judgedCount(JudgmentJ6) != 0 || !f.backupRan() {
		t.Errorf("J6=%d backup=%v", f.judgedCount(JudgmentJ6), f.backupRan())
	}
}

// AC-54・AC-60・AC-64・AC-65: 判断点が利用上限に当たった回りの後 → rate_limited。学びと改修の提案は通さず、書き出しは通る。
func TestRunCycle_End_RateLimitedSkipsLearnAndImprove(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(reviewWaitOut())
	f.seedLearnAndImprove(t)
	base := f.invoker(t)
	inv := &cycleFakeInvoker{handle: func(c cycleCall) JudgmentLaunchOutput {
		if c.Judgment == JudgmentJ5 {
			f.mu.Lock()
			f.judged = append(f.judged, c.Judgment)
			f.mu.Unlock()
			return JudgmentLaunchOutput{Result: RunResultErrored, RateLimited: true}
		}
		return base.handle(c)
	}}
	res := f.runCycle(t, f.input(inv))
	if res.Cycle.EndReason != CycleEndRateLimited || res.Cycle.Result != CycleResultCompleted {
		t.Fatalf("got %q/%q", res.Cycle.EndReason, res.Cycle.Result)
	}
	if f.judgedCount(JudgmentJ6) != 0 || f.judgedCount(JudgmentJ8) != 0 || !f.backupRan() {
		t.Errorf("judged=%v backup=%v", f.judged, f.backupRan())
	}
	if len(res.Phases) != 8 {
		t.Errorf("phases = %d, want 8 (7 advance + backup)", len(res.Phases))
	}
}

// AC-45・AC-50〜52・AC-59・AC-61・AC-62: サイクルの費用の上限で見送った回りの後 → budget。終わりの段を通り、取り消しは呼ばない。
func TestRunCycle_End_BudgetRunsEndPhasesAndDoesNotCancel(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(contractOut(ContractStateRunning, nil, nil), reviewWaitOut(), reviewWaitOut())
	f.verdicts = []string{"not_met", "met"}
	f.agent.CycleBudgetUSD = 300
	f.agent.JudgmentBudgetUSD.J5 = 299.8
	f.seedLearnAndImprove(t)
	res := f.runCycle(t, f.input(f.invoker(t)))
	if res.Cycle.EndReason != CycleEndBudget || res.Cycle.Result != CycleResultCompleted {
		t.Fatalf("got %q/%q", res.Cycle.EndReason, res.Cycle.Result)
	}
	if f.judgedCount(JudgmentJ6) != 1 || !f.backupRan() || f.cancels() != 0 {
		t.Errorf("J6=%d backup=%v cancels=%d", f.judgedCount(JudgmentJ6), f.backupRan(), f.cancels())
	}
}

// 優先順: 止める指示と費用の上限が同じ回りで成り立つ → stopped（AC-57）。
func TestRunCycle_End_StopWinsOverBudget(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(contractOut(ContractStateRunning, nil, nil), reviewWaitOut(), reviewWaitOut())
	f.verdicts = []string{"not_met", "met"}
	f.agent.CycleBudgetUSD = 300
	f.agent.JudgmentBudgetUSD.J5 = 299.8
	base := f.invoker(t)
	inv := &cycleFakeInvoker{handle: func(c cycleCall) JudgmentLaunchOutput {
		out := base.handle(c)
		if c.Judgment == JudgmentJ5 {
			f.requestStop(t) // 1 回目の J5 の最中（段の途中）に入る。この回りの終わりで読む。
		}
		return out
	}}
	res := f.runCycle(t, f.input(inv))
	if res.Cycle.EndReason != CycleEndStopped {
		t.Errorf("end_reason = %q, want stopped", res.Cycle.EndReason)
	}
}

// AC-49: 回りが複数のサイクルの phases は段ごとに 1 要素。
func TestRunCycle_End_PhasesAreOnePerPhaseAcrossRounds(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(contractOut(ContractStateRunning, nil, nil), reviewWaitOut())
	res := f.runCycle(t, f.input(f.invoker(t)))
	seen := map[CyclePhase]int{}
	for _, p := range res.Phases {
		seen[p.Phase]++
	}
	if len(res.Phases) != 10 {
		t.Errorf("phases = %d, want 10", len(res.Phases))
	}
	for p, n := range seen {
		if n != 1 {
			t.Errorf("phase %s appears %d times", p, n)
		}
	}
	if res.Cycle.Rounds < 2 {
		t.Errorf("rounds = %d", res.Cycle.Rounds)
	}
}

// lastCycle は、いちばん新しい周の記録を読む（RunCycle がエラーで戻った周も読める）。
func (f *roundsFixture) lastCycle(t *testing.T) *Cycle {
	t.Helper()
	var c *Cycle
	err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		var id int64
		if err := tx.QueryRow(`SELECT MAX(id) FROM cycle`).Scan(&id); err != nil {
			return err
		}
		row, err := loadCycleByID(context.Background(), tx, id)
		if err != nil {
			return err
		}
		c = toPublicCycle(row)
		return nil
	})
	if err != nil {
		t.Fatalf("read cycle: %v", err)
	}
	return c
}

// AC-158: 回りの途中（委譲の段の後・検証の段の前）で止める指示を読んだ → 検証の段へ進まず stopped で終わる。
func TestRunCycle_End_StopAtAMidRoundBoundaryDoesNotStartTheNextPhase(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(reviewWaitOut())
	in := f.input(f.invoker(t))
	in.Contract = stopOnStartClient{ContractClient: f.client, stop: func() { f.requestStop(t) }} // 委譲の段の途中で入れる
	res := f.runCycle(t, in)
	if res.Cycle.EndReason != CycleEndStopped {
		t.Fatalf("end_reason = %q, want stopped", res.Cycle.EndReason)
	}
	if len(f.client.starts) != 1 {
		t.Errorf("starts = %d, want the run phase to finish its start", len(f.client.starts))
	}
	if f.judgedCount(JudgmentJ5) != 0 || f.judgedCount(JudgmentJ6) != 0 {
		t.Errorf("judged = %v, want no verify/learn", f.judged)
	}
	if len(res.Phases) != 8 || !f.backupRan() || f.cancels() != 0 {
		t.Errorf("phases=%d backup=%v cancels=%d", len(res.Phases), f.backupRan(), f.cancels())
	}
}

// stopOnStartClient は、起動の呼び出しの後に止める指示を入れる契約のコマンドの起動の包み。
type stopOnStartClient struct {
	ContractClient
	stop func()
}

func (c stopOnStartClient) Start(ctx context.Context, in ContractLaunchInput) (ContractLaunchOutput, error) {
	out, err := c.ContractClient.Start(ctx, in)
	c.stop()
	return out, err
}

// 優先順の判定（止める指示 → 利用上限 → サイクルの費用の上限）。成り立つ理由を重ねて（同じ回りの終わりで 2 つ以上が成り立つ形）、先のものが採られることを確かめる。
// 段の切れ目の止める指示の読みが先にあるため、段の途中で止める指示を入れながら費用の上限の見送りを同じ回りに起こす形は、試験から作れない。
func TestRoundEndReason_Order(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                      string
		stop, rateLimited, budget bool
		want                      CycleEndReason
		ended                     bool
	}{
		{"止める指示と利用上限と費用の上限 → stopped", true, true, true, CycleEndStopped, true},
		{"止める指示と費用の上限 → stopped", true, false, true, CycleEndStopped, true},
		{"利用上限と費用の上限 → rate_limited", false, true, true, CycleEndRateLimited, true},
		{"費用の上限だけ → budget", false, false, true, CycleEndBudget, true},
		{"どれも無し → 終えない", false, false, false, "", false},
	} {
		got, ended := roundEndReason(tc.stop, tc.rateLimited, tc.budget)
		if got != tc.want || ended != tc.ended {
			t.Errorf("%s: got %q/%v", tc.name, got, ended)
		}
	}
}

// 段のエラーと止める指示の両方が同じ回りで成り立つ → error・aborted。スーパーバイザーの段の途中（判断点の呼び出しの中）で止める指示を入れ、
// 同じ段の手の実行の書き込みがストアのエラーになる（読む表が無い）ようにする。回りの終わりの止める指示の読みより先にエラーが出るので、
// 判定の順を入れ替える・エラーを止める指示の後ろへ置くと通らない。
func TestRunCycle_End_ErrorAndStopInTheSameRound(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(reviewWaitOut())
	if _, err := holdViaEntrance(t, f.s, StatusVerifying, HoldKindHuman, HoldOptionsInput{StopKind: StopKindVerifyUncertain, SupervisorEnabled: true}); err != nil {
		t.Fatal(err)
	}
	base := f.invoker(t)
	in := f.input(&cycleFakeInvoker{handle: func(c cycleCall) JudgmentLaunchOutput {
		if c.Judgment != JudgmentSV {
			return base.handle(c)
		}
		f.requestStop(t)
		mexec(t, f.s, `ALTER TABLE hold RENAME TO hold_gone`)
		return svResult(nil)
	}})
	_, err := f.s.RunCycle(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "supervise hold") {
		t.Fatalf("err = %v, want the supervise stage error", err)
	}
	if !f.stopRequested(t) {
		t.Fatal("the stop request was not recorded in the same round")
	}
	c := f.lastCycle(t)
	if c.EndReason != CycleEndError || c.Result != CycleResultAborted {
		t.Errorf("got %q/%q, want error/aborted", c.EndReason, c.Result)
	}
}

func (f *roundsFixture) stopRequested(t *testing.T) bool {
	t.Helper()
	var n int
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM cycle WHERE stop_requested_at IS NOT NULL`).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func roundsNotStarted(res *CycleRunResult, reason NotStartedReason) bool {
	for _, p := range res.Phases {
		for _, ns := range p.NotStarted {
			if ns.Reason == reason {
				return true
			}
		}
	}
	return false
}

// 課題の枠（run_budget）で起動が見送られただけの回りの後 → budget にならない（サイクルの費用の上限だけが budget にする）。
func TestRunCycle_End_RunBudgetOnlyIsNotBudget(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	mexec(t, f.s, `UPDATE task SET budget_impl_micros = 500000`)
	res := f.runCycle(t, f.input(f.invoker(t)))
	if !roundsNotStarted(res, NotStartedRunBudget) {
		t.Fatalf("not run_budget: phases %+v", res.Phases)
	}
	if res.Cycle.EndReason == CycleEndBudget {
		t.Errorf("end_reason = budget, want not budget when only run_budget held the start back")
	}
}

// 委譲の起動が利用上限に当たった回りの後 → rate_limited。
func TestRunCycle_End_DelegationRateLimited(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	f.newInProgress(t, "rl", "P1", planSpec(nil))
	f.deleg.result = JudgmentLaunchOutput{Result: RunResultErrored, RateLimited: true}
	res, err := f.s.RunCycle(context.Background(), delegateCycleInput(f))
	if err != nil {
		t.Fatal(err)
	}
	if res.Cycle.EndReason != CycleEndRateLimited {
		t.Errorf("end_reason = %q, want rate_limited", res.Cycle.EndReason)
	}
}

func delegateCycleInput(f *delegateFixture) CycleRunInput {
	return CycleRunInput{
		Trigger: "manual", AgentDecl: f.agent, ConnDecl: f.conn, Invoker: &cycleFakeInvoker{handle: func(cycleCall) JudgmentLaunchOutput {
			return JudgmentLaunchOutput{Result: RunResultErrored}
		}}, Upstream: f.upstream, Delegate: f.deleg, Git: f.git, Reconcile: f.branches, Checks: newFakeChecks(), Diffs: &fakeDiffs{},
		HeadGit: &noHeadGit{}, WorkspaceGit: &fakeWorkspaceGit{}, Wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	}
}

// EndCycleWithReason は、終わった理由の 7 値以外を断る（空は「理由を持たない周」として受ける）。
func TestEndCycleWithReason_RejectsUnknownReason(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	cyc, err := s.BeginCycle(context.Background(), BeginCycleInput{Trigger: "t", BudgetUSD: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EndCycleWithReason(context.Background(), cyc.ID, CycleResultCompleted, "bogus"); !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	if _, err := s.EndCycleWithReason(context.Background(), cyc.ID, CycleResultCompleted, ""); err != nil {
		t.Fatalf("empty reason: %v", err)
	}
}
