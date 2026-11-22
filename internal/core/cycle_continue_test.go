package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// 区切り S4b: settled で終わったサイクルは、終わりの段を通して排他を放した後に、いま始められるものを数え直し、
// 1 件以上で auto_cycle が真なら、次のサイクルを始める処理（CycleRunInput.Continue）を 1 回呼ぶ。

// continueProbe は Continue の呼び出しを数え、呼ばれた時点で排他が放されていたかを見る。
type continueProbe struct {
	calls        int
	lockHeld     bool
	statusErr    error
	returnsError error
}

func (p *continueProbe) hook(f *roundsFixture) func(context.Context) error {
	return func(ctx context.Context) error {
		p.calls++
		st, err := f.s.ReadCycleStatus(ctx)
		if err != nil {
			p.statusErr = err
		} else {
			p.lockHeld = st.LockHeld
		}
		return p.returnsError
	}
}

// settledFixtureWithLateWork は、最後の回りの読み直しより後（書き出しの段。排他を放す前）に、新しい課題が記録される settled のサイクルを作る。
func settledFixtureWithLateWork(t *testing.T) *roundsFixture {
	t.Helper()
	f := newRoundsFixture(t)
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(contractOut(ContractStateRunning, nil, nil), reviewWaitOut())
	f.seedLearnAndImprove(t)
	f.lateWork(t)
	return f
}

// AC-106・AC-119・AC-120・AC-121: 終わりの段の途中で記録された操作を、排他を放した後の数えで拾い、Continue を 1 回呼ぶ。
func TestRunCycle_Continue_PicksUpWorkRecordedBeforeTheLockIsReleased(t *testing.T) {
	t.Parallel()
	f := settledFixtureWithLateWork(t)
	p := &continueProbe{}
	in := f.input(f.invoker(t))
	in.Continue = p.hook(f)

	res := f.runCycle(t, in)

	if res.Cycle.EndReason != CycleEndSettled {
		t.Fatalf("end_reason = %q, want settled", res.Cycle.EndReason)
	}
	if p.calls != 1 || !res.Continued {
		t.Fatalf("Continue calls = %d, Continued = %v, want 1/true", p.calls, res.Continued)
	}
	if p.statusErr != nil || p.lockHeld {
		t.Errorf("at the time of the call the lock must be released (held=%v err=%v)", p.lockHeld, p.statusErr)
	}
}

// Continue のエラーは、サイクルの成否を変えない（結果に残す）。
func TestRunCycle_Continue_ErrorIsReportedAndDoesNotFailTheCycle(t *testing.T) {
	t.Parallel()
	f := settledFixtureWithLateWork(t)
	p := &continueProbe{returnsError: errors.New("cannot start")}
	in := f.input(f.invoker(t))
	in.Continue = p.hook(f)

	res := f.runCycle(t, in)

	if res.Cycle.Result != CycleResultCompleted || res.ContinueErr == nil {
		t.Fatalf("result = %q, ContinueErr = %v", res.Cycle.Result, res.ContinueErr)
	}
}

// AC-122: いま始められるものが 0 件で、学びの段だけに対象が残っているときは呼ばない。
func TestRunCycle_Continue_NotCalledWhenOnlyTheLearnPhaseHasWork(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(contractOut(ContractStateRunning, nil, nil), reviewWaitOut())
	f.seedLearnAndImprove(t)
	p := &continueProbe{}
	in := f.input(f.invoker(t))
	in.Continue = p.hook(f)

	res := f.runCycle(t, in)

	if res.Cycle.EndReason != CycleEndSettled || p.calls != 0 || res.Continued {
		t.Fatalf("end_reason = %q, Continue calls = %d", res.Cycle.EndReason, p.calls)
	}
}

// AC-123: 宣言の auto_cycle が偽なら、いま始められるものが 1 件以上でも呼ばない。
func TestRunCycle_Continue_NotCalledWhenAutoCycleIsFalse(t *testing.T) {
	t.Parallel()
	f := settledFixtureWithLateWork(t)
	f.agent.AutoCycle = false
	p := &continueProbe{}
	in := f.input(f.invoker(t))
	in.Continue = p.hook(f)

	res := f.runCycle(t, in)

	if res.Cycle.EndReason != CycleEndSettled || p.calls != 0 {
		t.Fatalf("end_reason = %q, Continue calls = %d, want 0", res.Cycle.EndReason, p.calls)
	}
}

// AC-124・AC-125・AC-128: error・interrupted・stopped で終わったサイクルは、次を始めない（始められるものがあっても）。
func TestRunCycle_Continue_NotCalledForEndReasonsOtherThanSettled(t *testing.T) {
	t.Parallel()
	t.Run("stopped", func(t *testing.T) {
		t.Parallel()
		f := newRoundsFixture(t)
		f.startOut(contractOut(ContractStateRunning, nil, nil))
		f.reads(contractOut(ContractStateRunning, nil, nil))
		f.onWait = func(_ context.Context, _ int) error {
			mustCreateChallenge(t, f.s, "pending") // いま始められるものがある状態で終わる
			f.requestStop(t)
			return nil
		}
		p := &continueProbe{}
		in := f.input(f.invoker(t))
		in.Continue = p.hook(f)
		res := f.runCycle(t, in)
		if res.Cycle.EndReason != CycleEndStopped || p.calls != 0 {
			t.Fatalf("end_reason = %q, Continue calls = %d", res.Cycle.EndReason, p.calls)
		}
	})
	t.Run("error", func(t *testing.T) {
		t.Parallel()
		f := newRoundsFixture(t)
		f.startOut(contractOut(ContractStateRunning, nil, nil))
		f.reads(contractOut(ContractStateRunning, nil, nil))
		boom := errors.New("boom")
		f.onWait = func(_ context.Context, _ int) error {
			mustCreateChallenge(t, f.s, "pending")
			return boom
		}
		p := &continueProbe{}
		in := f.input(f.invoker(t))
		in.Continue = p.hook(f)
		if _, err := f.s.RunCycle(context.Background(), in); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
		if p.calls != 0 {
			t.Fatalf("Continue calls = %d, want 0", p.calls)
		}
	})
	t.Run("interrupted", func(t *testing.T) {
		t.Parallel()
		f := newRoundsFixture(t)
		f.startOut(contractOut(ContractStateRunning, nil, nil))
		f.reads(contractOut(ContractStateRunning, nil, nil))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f.onWait = func(c context.Context, _ int) error {
			mustCreateChallenge(t, f.s, "pending")
			cancel()
			return c.Err()
		}
		p := &continueProbe{}
		in := f.input(f.invoker(t))
		in.Continue = p.hook(f)
		_, _ = f.s.RunCycle(ctx, in)
		if p.calls != 0 {
			t.Fatalf("Continue calls = %d, want 0", p.calls)
		}
	})
}

// deferInRunningCycle は、走っているサイクルで見送られた（deferred_cycle_id がそのサイクル）スーパーバイザー待ちの保留を作る。
// lateWork と同じ位置（最後の回りの後・排他を放す前）で呼ぶ。otherWork が真なら、ほかに始められる課題も作る。
func deferInRunningCycle(t *testing.T, f *roundsFixture, otherWork bool) {
	t.Helper()
	f.wg.changed = true
	f.wg.onCommit = func() {
		id, err := holdViaEntrance(t, f.s, StatusVerifying, HoldKindHuman, HoldOptionsInput{StopKind: StopKindVerifyUncertain, SupervisorEnabled: true})
		if err != nil {
			t.Error(err)
			return
		}
		cid, _ := parseChallengeID(id)
		if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE hold SET deferred_cycle_id = (SELECT MAX(id) FROM cycle), deferred_count = 1 WHERE challenge_id = ?`, cid)
			return err
		}); err != nil {
			t.Error(err)
		}
		if otherWork {
			mustCreateChallenge(t, f.s, "other-work")
		}
	}
}

// #1125: そのサイクルで見送った保留だけが残るとき、終わった後に次のサイクルを自動で始めない。
// サイクルの外からの数え（CountRoundWork）には、見送った保留が入る。
func TestRunCycle_Continue_NotCalledWhenOnlyAHoldDeferredInThisCycleRemains(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(contractOut(ContractStateRunning, nil, nil), reviewWaitOut())
	f.seedLearnAndImprove(t)
	deferInRunningCycle(t, f, false)
	p := &continueProbe{}
	in := f.input(f.invoker(t))
	in.Continue = p.hook(f)

	res := f.runCycle(t, in)

	if res.Cycle.EndReason != CycleEndSettled || p.calls != 0 || res.Continued {
		t.Fatalf("end_reason = %q, Continue calls = %d, want settled/0", res.Cycle.EndReason, p.calls)
	}
	if got := startableAfter(t, f); got != 1 {
		t.Errorf("Startable outside a cycle = %d, want 1 (the deferred hold counts)", got)
	}
}

// #1125: 見送った保留のほかに始められるものがあれば、次のサイクルを始める。
func TestRunCycle_Continue_CalledWhenOtherWorkRemainsBesideTheDeferredHold(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(contractOut(ContractStateRunning, nil, nil), reviewWaitOut())
	f.seedLearnAndImprove(t)
	deferInRunningCycle(t, f, true)
	p := &continueProbe{}
	in := f.input(f.invoker(t))
	in.Continue = p.hook(f)

	res := f.runCycle(t, in)

	if p.calls != 1 || !res.Continued {
		t.Fatalf("Continue calls = %d, Continued = %v, want 1/true", p.calls, res.Continued)
	}
}
