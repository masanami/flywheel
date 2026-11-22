package core

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// 受入基準の穴埋め（Issue #1037。番号は #988 本文の「## 受入基準」の並びの順）。AC-12・43・44・49・62・121・126・127・129 の core 側を、
// 実ストア・偽の ContractClient・偽の判断の IF で固定する。

// statusProbeClient は、状態の読み取り（Status）の呼び出しのたびに、走っているサイクルの running.rounds を記録する契約のコマンドの起動の包み。
type statusProbeClient struct {
	ContractClient
	f      *roundsFixture
	rounds []int
}

func (c *statusProbeClient) Status(ctx context.Context, in ContractLaunchInput) (ContractLaunchOutput, error) {
	st, err := c.f.s.ReadCycleStatus(ctx)
	if err == nil && st.Running != nil {
		c.rounds = append(c.rounds, st.Running.Rounds)
	} else {
		c.rounds = append(c.rounds, -1)
	}
	return c.ContractClient.Status(ctx, in)
}

// #988 AC-43: サイクルの記録の rounds は、通した回りの数とちょうど等しい（下限ではなく厳密な一致）。
// 回りの数が決まる設定で比べる: 落ち着いて終わるサイクルは、最後の回りを除く各回りの後に待ちが 1 回あるので、回りの数は待ちの数 + 1。
// 何も無いサイクルは 1 回。stalled で終わるサイクル（進みのあった回り + 進みの無い回り 3 回）の数は、
// TestRunCycle_Rounds_UnreadableApprovedChecksStall が固定する。
func TestRunCycle_AC43_RoundsEqualsTheNumberOfRoundsRun(t *testing.T) {
	t.Parallel()
	t.Run("settled: rounds = waits + 1, in the result and in the stored record", func(t *testing.T) {
		t.Parallel()
		f := newRoundsFixture(t)
		f.startOut(contractOut(ContractStateRunning, nil, nil))
		f.reads(contractOut(ContractStateRunning, nil, nil), reviewWaitOut())
		res := f.runCycle(t, f.input(f.invoker(t)))
		want := len(f.waited()) + 1
		if want < 3 {
			t.Fatalf("waits = %d: the setup must run several rounds", want-1)
		}
		if res.Cycle.EndReason != CycleEndSettled || res.Cycle.Rounds != want {
			t.Errorf("end_reason = %q rounds = %d, want settled with %d", res.Cycle.EndReason, res.Cycle.Rounds, want)
		}
		if got := f.lastCycle(t).Rounds; got != want {
			t.Errorf("stored rounds = %d, want %d", got, want)
		}
	})
	t.Run("nothing to do: one round", func(t *testing.T) {
		t.Parallel()
		f := newJ6Fixture(t) // 課題が 1 件も無い
		res, err := f.s.RunCycle(context.Background(), f.cycleInput())
		if err != nil {
			t.Fatal(err)
		}
		if res.Cycle.EndReason != CycleEndSettled || res.Cycle.Rounds != 1 {
			t.Errorf("end_reason = %q rounds = %d, want settled with 1", res.Cycle.EndReason, res.Cycle.Rounds)
		}
	})
}

// #988 AC-44: 2 回目の回りの途中（段の中）で読んだ running.rounds は 1（通し終えた回りの数）。3 回目の回りの途中では 2。
// 1 回目の回りは委譲の起動だけで状態を読まないので、状態の読み取りの 1 回目が 2 回目の回りにある。
func TestRunCycle_AC44_RunningRoundsDuringTheSecondRoundIsOne(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(contractOut(ContractStateRunning, nil, nil), reviewWaitOut())
	probe := &statusProbeClient{ContractClient: f.client, f: f}
	in := f.input(f.invoker(t))
	in.Contract = probe

	res := f.runCycle(t, in)

	if res.Cycle.Rounds < 3 {
		t.Fatalf("rounds = %d, want at least 3", res.Cycle.Rounds)
	}
	if len(probe.rounds) < 2 {
		t.Fatalf("status reads = %v, want one in each of the second and third rounds", probe.rounds)
	}
	for i, got := range probe.rounds[:2] {
		if want := i + 1; got != want {
			t.Errorf("running.rounds during round %d = %d, want %d (all reads: %v)", i+2, got, want, probe.rounds)
		}
	}
}

// lateWork は、最後の回りの後（書き出しの段。排他を放す前）に、新しい課題（未分類。いま始められるもの）が記録されるようにする。
// 回りの中で作ると分類の段（J1）の対象になるため、回りの後で作る。
func (f *roundsFixture) lateWork(t *testing.T) {
	t.Helper()
	f.wg.changed = true
	f.wg.onCommit = func() { mustCreateChallenge(t, f.s, "late-arrival") }
}

// startableAfter は、サイクルが終わった後の「いま始められるもの」の数（回りの終わりに数えるものと同じ数え）。
func startableAfter(t *testing.T, f *roundsFixture) int {
	t.Helper()
	w, err := f.s.CountRoundWork(context.Background(), f.agent, f.conn)
	if err != nil {
		t.Fatal(err)
	}
	return w.Startable
}

// #988 AC-62・AC-127: end_reason が budget のサイクルは、改修の提案の段を通っていて（J8 の呼び出しが 1 回・段は実行されている）、
// いま始められるもの（未分類の課題）があっても、終わった後に始める処理（Continue）を呼ばない。
func TestRunCycle_AC62_AC127_BudgetRunsImproveAndDoesNotContinue(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(contractOut(ContractStateRunning, nil, nil), reviewWaitOut(), reviewWaitOut())
	f.verdicts = []string{"not_met", "met"}
	f.agent.CycleBudgetUSD = 300
	f.agent.JudgmentBudgetUSD.J5 = 299.8
	f.seedLearnAndImprove(t)
	f.lateWork(t) // いま始められるもの
	probe := &continueProbe{}
	in := f.input(f.invoker(t))
	in.Continue = probe.hook(f)

	res := f.runCycle(t, in)

	if res.Cycle.EndReason != CycleEndBudget {
		t.Fatalf("end_reason = %q, want budget", res.Cycle.EndReason)
	}
	if improve := phaseOfResult(t, res, CyclePhaseImprove); improve.Skipped || f.judgedCount(JudgmentJ8) != 1 || len(improve.ImproveItems) != 1 {
		t.Errorf("AC-62: improve phase = %+v, J8 calls = %d, want an executed phase with one J8 call", improve, f.judgedCount(JudgmentJ8))
	}
	if n := startableAfter(t, f); n < 1 {
		t.Fatalf("AC-127 premise: startable = %d, want at least 1", n)
	}
	if probe.calls != 0 || res.Continued {
		t.Errorf("AC-127: Continue calls = %d, Continued = %v, want 0/false", probe.calls, res.Continued)
	}
}

// #988 AC-129: end_reason が rate_limited のサイクルは、いま始められるもの（未分類の課題）があっても、終わった後に始める処理（Continue）を呼ばない。
func TestRunCycle_AC129_RateLimitedDoesNotContinue(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.startOut(contractOut(ContractStateRunning, nil, nil))
	f.reads(reviewWaitOut())
	f.lateWork(t)
	base := f.invoker(t)
	inv := &cycleFakeInvoker{handle: func(c cycleCall) JudgmentLaunchOutput {
		if c.Judgment == JudgmentJ5 {
			return JudgmentLaunchOutput{Result: RunResultErrored, RateLimited: true}
		}
		return base.handle(c)
	}}
	probe := &continueProbe{}
	in := f.input(inv)
	in.Continue = probe.hook(f)

	res := f.runCycle(t, in)

	if res.Cycle.EndReason != CycleEndRateLimited {
		t.Fatalf("end_reason = %q, want rate_limited", res.Cycle.EndReason)
	}
	if n := startableAfter(t, f); n < 1 {
		t.Fatalf("AC-129 premise: startable = %d, want at least 1", n)
	}
	if probe.calls != 0 || res.Continued {
		t.Errorf("AC-129: Continue calls = %d, Continued = %v, want 0/false", probe.calls, res.Continued)
	}
}

// #988 AC-49: 回りが 2 回のサイクルの phases は、段ごとに 1 要素で、対象の一覧（Items）は 2 回分を順に連結したもの。
// 1 回目の回りで a、2 回目の回りで（a が済んだ後に）b が起動するので、委譲の段の対象は [a の実行, b の実行] の順に並ぶ。
func TestRunCycle_AC49_TwoRoundsGiveOneElementPerPhaseWithConcatenatedItems(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "chain", "P1", true, multiTask{key: "a"}, multiTask{key: "b", deps: []string{"a"}})
	cid, _ := parseChallengeID(id)
	calls := 0
	wait := func(ctx context.Context, _ time.Duration) error {
		calls++
		if calls == 1 {
			setTaskStatusRaw(t, f.s, cid, "a", TaskStatusDone) // 前提のタスクが済んだことが記録される
		} else {
			mexec(t, f.s, `UPDATE cycle SET stop_requested_at = '2026-11-22T00:00:00.000Z' WHERE ended_at IS NULL`) // 2 回目の回りの後で終える
		}
		return ctx.Err()
	}

	res, err := f.s.RunCycle(context.Background(), f.delegateCycleInput(f.client, wait, newFakeChecks()))
	if err != nil {
		t.Fatal(err)
	}

	if res.Cycle.Rounds != 2 {
		t.Fatalf("rounds = %d, want 2", res.Cycle.Rounds)
	}
	seen := map[CyclePhase]int{}
	for _, p := range res.Phases {
		seen[p.Phase]++
	}
	for p, n := range seen {
		if n != 1 {
			t.Errorf("phase %s appears %d times, want once", p, n)
		}
	}
	items := phaseOfResult(t, res, CyclePhaseRun).Items
	if len(items) != 2 {
		t.Fatalf("run items = %+v, want one run from each round", items)
	}
	// 順序: 実行の ID は起動の順に増えるので、1 回目の回りの実行が先に並ぶ。
	var first, second int
	if _, err := fmt.Sscanf(items[0].RunID, "R-%d", &first); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Sscanf(items[1].RunID, "R-%d", &second); err != nil {
		t.Fatal(err)
	}
	if first >= second {
		t.Errorf("run items = %s then %s, want the first round's run before the second round's", items[0].RunID, items[1].RunID)
	}
	started := f.startedTasks(t)
	if want := []string{f.taskID(t, id, "a"), f.taskID(t, id, "b")}; !equalStrings(started, want) {
		t.Errorf("started = %v, want %v (a in the first round, b in the second)", started, want)
	}
}

// #988 AC-49（連結の規則そのもの）: 回りの段の結果を足し合わせるとき、段ごとに 1 要素のまま、対象の一覧（Items）・起動しなかったものの一覧（NotStarted）・
// 反映の対象（ReleaseItems）は 2 回分を順に連結し、1 つだけの値（取り込みの結果）は最後の回りのものにする。
func TestMergeRoundPhases_AC49_ConcatenatesInOrderAndKeepsOneElementPerPhase(t *testing.T) {
	t.Parallel()
	round := func(tag string) []CyclePhaseResult {
		return []CyclePhaseResult{
			{Phase: CyclePhaseIngest, Ingest: &IngestResult{Sources: []SourceIngestResult{{ID: "src-" + tag}}}},
			{Phase: CyclePhaseRelease, ReleaseItems: []ReleaseExecItem{{OperationID: "OP-" + tag}}},
			{Phase: CyclePhaseRun, Items: []JudgmentAutoItem{{ChallengeID: "C-" + tag}}, NotStarted: []NotStarted{{ChallengeID: "C-" + tag}}},
		}
	}
	got := mergeRoundPhases(round("1"), round("2"))

	if len(got) != 3 {
		t.Fatalf("phases = %d, want 3 (one per phase)", len(got))
	}
	if ing := got[0].Ingest; ing == nil || len(ing.Sources) != 1 || ing.Sources[0].ID != "src-2" {
		t.Errorf("ingest = %+v, want the last round's value (src-2)", ing)
	}
	got = got[1:]
	if rel := got[0].ReleaseItems; len(rel) != 2 || rel[0].OperationID != "OP-1" || rel[1].OperationID != "OP-2" {
		t.Errorf("release items = %+v, want [OP-1 OP-2]", rel)
	}
	if run := got[1]; len(run.Items) != 2 || run.Items[0].ChallengeID != "C-1" || run.Items[1].ChallengeID != "C-2" ||
		len(run.NotStarted) != 2 || run.NotStarted[0].ChallengeID != "C-1" || run.NotStarted[1].ChallengeID != "C-2" {
		t.Errorf("run phase = %+v, want items and not_started [C-1 C-2]", run)
	}
}

// #988 AC-141: watching の要素の title は、そのタスクの題名（課題の題名ではない）。課題の題名と異なる題名のタスクを置いて見る。
// タスクに結び付かない実行は課題の題名（タスクの題名が無いときだけの代わり）。
func TestReadCycleStatus_AC141_WatchingTitleIsTheTaskTitle(t *testing.T) {
	t.Parallel()
	f := newTwoTaskFixture(t)
	f.runningAt(t, statusBase)
	if _, err := f.exec(`UPDATE challenge SET title = 'CHALLENGE-TITLE' WHERE id = ?`, f.cidInt); err != nil {
		t.Fatal(err)
	}
	if _, err := f.exec(`UPDATE task SET title = 'TASK-ONE-TITLE' WHERE id = ?`, f.task1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.exec(`UPDATE task SET title = 'TASK-TWO-TITLE' WHERE id = ?`, f.task2); err != nil {
		t.Fatal(err)
	}

	w := f.watchingByTask(t)

	if got := w[formatTaskID(f.task1)].Title; got != "TASK-ONE-TITLE" {
		t.Errorf("task 1 title = %q, want the task's title (not the challenge's)", got)
	}
	if got := w[formatTaskID(f.task2)].Title; got != "TASK-TWO-TITLE" {
		t.Errorf("task 2 title = %q, want the task's title (not the challenge's)", got)
	}
}

// #988 AC-142: watching の要素の run_started_at は、その実行の始まった時刻（既知の時刻を置いて、等しいことを見る。実行ごとに別の時刻）。
func TestReadCycleStatus_AC142_WatchingRunStartedAtIsTheRunsStartTime(t *testing.T) {
	t.Parallel()
	f := newTwoTaskFixture(t)
	f.runningAt(t, statusBase)
	if _, err := f.exec(`UPDATE contract_run SET started_at = '2026-09-30T08:09:10.123Z' WHERE id = ?`, f.run1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.exec(`UPDATE contract_run SET started_at = '2026-09-30T09:10:11.456Z' WHERE id = ?`, f.run2); err != nil {
		t.Fatal(err)
	}

	w := f.watchingByTask(t)

	if got, want := w[formatTaskID(f.task1)].RunStartedAt, time.Date(2026, 9, 30, 8, 9, 10, 123_000_000, time.UTC); !got.Equal(want) {
		t.Errorf("run 1 run_started_at = %v, want %v", got, want)
	}
	if got, want := w[formatTaskID(f.task2)].RunStartedAt, time.Date(2026, 9, 30, 9, 10, 11, 456_000_000, time.UTC); !got.Equal(want) {
		t.Errorf("run 2 run_started_at = %v, want %v", got, want)
	}
}
