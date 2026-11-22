package core

import (
	"context"
	"errors"
	"testing"
)

// 承認した head のチェックが読めない・読む手段が無い場合の waiting_checks は、待っても解けないので WaitNotResolving を立てる。
// チェックが走っている・取り込み後の中身にチェックがまだ無い場合は立てない。

// commitChecksFake は fakeRelease に、承認した head のチェックを読む口を足したもの。
type commitChecksFake struct {
	*fakeRelease
	checks []UpstreamCheck
	err    error
}

func (c *commitChecksFake) GetCommitChecks(context.Context, string, string) ([]UpstreamCheck, error) {
	return c.checks, c.err
}

func (f *landFixture) movedWithChecks(t *testing.T, src UpstreamCheckSource, checks []UpstreamCheck) ReleaseExecItem {
	t.Helper()
	f.sendLand(t)
	cmp := newFakeCompare()
	f.movedByMergeIn(cmp, "h1")
	f.fake.pr.Checks = checks
	f.client.queue = []ContractLaunchOutput{reviewFor("h1")}
	res, err := f.s.ExecuteReleases(context.Background(), []string{"OP-1"}, ReleaseExecOptions{
		Checks: src, Compare: cmp, Connectors: f.conn, Agent: &AgentDeclaration{FailureLimit: 3}, Contract: f.client, Trigger: ReleaseExecTriggerOpExec,
	})
	if err != nil || len(res.Items) != 1 {
		t.Fatalf("ExecuteReleases = %+v err %v", res, err)
	}
	return res.Items[0]
}

func TestRelease_WaitNotResolving(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		src  func(f *landFixture) UpstreamCheckSource
		// moved は、取り込み後の head に付いているチェック。
		moved []UpstreamCheck
		want  bool
	}{
		{"unreadable", func(f *landFixture) UpstreamCheckSource {
			return &commitChecksFake{fakeRelease: f.fake, err: errors.New("auth expired")}
		}, nil, true},
		{"unsupported", func(f *landFixture) UpstreamCheckSource { return f.fake }, nil, true},
		{"checks running", func(f *landFixture) UpstreamCheckSource {
			return &commitChecksFake{fakeRelease: f.fake, err: errors.New("auth expired")}
		}, []UpstreamCheck{checkRunning}, false},
		{"no checks yet (approved head had checks)", func(f *landFixture) UpstreamCheckSource {
			return &commitChecksFake{fakeRelease: f.fake, checks: []UpstreamCheck{checkDone}}
		}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newLandFixture(t)
			item := f.movedWithChecks(t, tc.src(f), tc.moved)
			if item.Result != ExecutionResultWaitingChecks || item.WaitNotResolving != tc.want {
				t.Fatalf("item = %+v, want waiting_checks with WaitNotResolving=%v", item, tc.want)
			}
		})
	}
}

// RunCycle を通して、承認した head のチェックが読めない待ちだけなら、進みの無い回りが 3 回続いて stalled で終わり、
// 学びの段と書き出しの段を通って completed になる。
func TestRunCycle_Rounds_UnreadableApprovedChecksStall(t *testing.T) {
	t.Parallel()
	lf := newLandFixture(t)
	lf.agent.CyclePollIntervalSec = roundsPollSec
	lf.sendLand(t)
	cmp := newFakeCompare()
	lf.movedByMergeIn(cmp, "h1")
	lf.fake.pr.Checks = nil
	lf.client.queue = []ContractLaunchOutput{reviewFor("h1")}
	f := &roundsFixture{reviewFlowFixture: lf.reviewFlowFixture, wg: &fakeWorkspaceGit{}}
	f.seedLearnAndImprove(t)
	in := f.input(f.invoker(t))
	in.Checks = &commitChecksFake{fakeRelease: lf.fake, err: errors.New("auth expired")}
	in.Compare = cmp
	probe := &continueProbe{}
	in.Continue = probe.hook(f) // #988 AC-126: stalled で終わったサイクルは、続きを始める処理を呼ばない
	// 退行したとき（待ちが解けないまま続く）に、タイムアウトまで回り続けず、読みやすい失敗にする。
	f.onWait = func(ctx context.Context, n int) error {
		if n > 10 {
			t.Errorf("waited %d times: the unreadable wait kept the cycle going", n)
			return context.Canceled
		}
		return ctx.Err()
	}

	res := f.runCycle(t, in)

	if res.Cycle.EndReason != CycleEndStalled || res.Cycle.Result != CycleResultCompleted {
		t.Fatalf("end_reason = %q result = %q, want stalled/completed", res.Cycle.EndReason, res.Cycle.Result)
	}
	rel := phaseOfResult(t, res, CyclePhaseRelease)
	if len(rel.ReleaseItems) == 0 {
		t.Fatalf("release phase has no items: %+v", rel)
	}
	for _, it := range rel.ReleaseItems {
		if it.Result != ExecutionResultWaitingChecks || !it.WaitNotResolving {
			t.Fatalf("release item = %+v, want waiting_checks with WaitNotResolving", it)
		}
	}
	if got := len(f.waited()); got != 2 { // 進みの無い回り 3 回のうち、3 回目の後は待たずに終える
		t.Errorf("waits = %d, want 2", got)
	}
	if n := countCalls(f.wg.calls, "IsWorkTree"); n != 1 {
		t.Errorf("backup phase ran %d times, want once", n)
	}

	// #988 AC-12: end_reason が stalled のサイクルは、学びの段を通っている（学びの段は実行され、対象ごとに J6 を 1 回呼んでいる）。
	learn := phaseOfResult(t, res, CyclePhaseLearn)
	if learn.Skipped || len(learn.Items) == 0 || f.judgedCount(JudgmentJ6) != len(learn.Items) {
		t.Errorf("AC-12: learn phase = %+v, J6 calls = %d, want an executed phase with one J6 call per item", learn, f.judgedCount(JudgmentJ6))
	}
	// #988 AC-43: rounds は、通した回りの数とちょうど等しい。進みのあった 1 回目の回り + 進みの無い回り 3 回 = 4（結果にも、ストアの記録にも）。
	const wantRounds = 1 + cycleStalledRounds
	if res.Cycle.Rounds != wantRounds || f.lastCycle(t).Rounds != wantRounds {
		t.Errorf("AC-43: rounds = %d (stored %d), want exactly %d", res.Cycle.Rounds, f.lastCycle(t).Rounds, wantRounds)
	}
	// #988 AC-49: 回りをまたいだ release の段の対象の一覧は、回りの数だけの分（回りごとに 1 件）を連結したもので、段ごとに 1 要素のまま。
	if len(rel.ReleaseItems) != wantRounds {
		t.Errorf("AC-49: release items = %d, want %d (one per round, concatenated)", len(rel.ReleaseItems), wantRounds)
	}
	if len(res.Phases) != 9 {
		t.Errorf("AC-49: phases = %d, want 9 (one element per phase)", len(res.Phases))
	}
	// #988 AC-126: いま始められるものがあっても（stalled の前提）、終わった後に始める処理を呼ばない。
	work, err := f.s.CountRoundWork(context.Background(), in.AgentDecl, in.ConnDecl)
	if err != nil || work.Startable < 1 {
		t.Fatalf("AC-126 premise: startable = %+v err %v, want at least 1", work, err)
	}
	if probe.calls != 0 || res.Continued {
		t.Errorf("AC-126: Continue calls = %d, Continued = %v, want 0/false", probe.calls, res.Continued)
	}
}
