package core

import (
	"context"
	"testing"
)

// このファイルは、回りの終わりの数え（CountRoundWork。Issue #991）を、受入基準の 1 つずつに照らして確かめる。
// 実ストア・偽の ContractClient で、状態を作ってから数える。

func countRound(t *testing.T, f *delegateFixture) *RoundWork {
	t.Helper()
	w, err := f.s.CountRoundWork(context.Background(), f.agent, f.conn)
	if err != nil {
		t.Fatalf("CountRoundWork: %v", err)
	}
	return w
}

func (f *delegateFixture) setStatusRaw(t *testing.T, id string, st Status) {
	t.Helper()
	cid, _ := parseChallengeID(id)
	mexec(t, f.s, `UPDATE challenge SET status = ?, version = version + 1 WHERE id = ?`, string(st), cid)
	if st != StatusAwaitingHuman {
		// 人間対応待ちを離れる書き換えは、開いている保留を閉じる（closeOpenHoldsUnlessHeld の理由を参照）。
		mexec(t, f.s, `UPDATE hold SET closed_at = '2026-11-23T00:00:00.000Z' WHERE challenge_id = ? AND answer IS NULL AND closed_at IS NULL`, cid)
	}
}

// AC-15: 前提が終わっていないタスクは、いま始められるものに数えない（前提が起動できるなら、前提だけを数える）。
func TestCountRoundWork_AC15_TaskWithAnUnfinishedPrerequisiteIsNotStartable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		prereq  TaskStatus
		wantRun int
	}{{TaskStatusVerifying, 0}, {TaskStatusPending, 1}} {
		f := newMultiFixture(t, 2)
		id := f.newMulti(t, "chain", "P1", true, multiTask{key: "a"}, multiTask{key: "b", deps: []string{"a"}})
		cid, _ := parseChallengeID(id)
		setTaskStatusRaw(t, f.s, cid, "a", tc.prereq)
		w := countRound(t, f.delegateFixture)
		if w.Stages.Run != tc.wantRun {
			t.Errorf("prerequisite %s: run = %d, want %d (task b must not be counted)", tc.prereq, w.Stages.Run, tc.wantRun)
		}
	}
}

// AC-16: 課題の枠（run_budget）で起動できないタスクは、いま始められるものに数えない。
func TestCountRoundWork_AC16_RunBudgetTaskIsNotStartable(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "lowbudget", "P1", planSpec(func(m map[string]any) { m["budget_impl_usd"] = 0.5 }))
	if w := countRound(t, f); w.Startable != 0 || w.Active != 0 {
		t.Fatalf("work = %+v, want nothing startable and nothing active", *w)
	}
	// 数えの基準にした状態が、委譲の段で run_budget になる状態であること。
	notStartedReasonAfter(f, id, NotStartedRunBudget)(t)
}

// 起動できるタスクは数える。同時に走らせる数の上限（max_parallel_runs）で待つタスクは数えない。
func TestCountRoundWork_TaskThatFitsIsStartableButOneWaitingForTheParallelLimitIsNot(t *testing.T) {
	t.Parallel()
	f := newOwnerWaitFixture(t, 1)
	if w := countRound(t, f.delegateFixture); w.Stages.Run != 1 || w.Startable != 1 {
		t.Fatalf("work = %+v, want one startable task", *w)
	}
	f.startFirst(t)
	f.newOther(t)
	w := countRound(t, f.delegateFixture)
	if w.Active != 1 || w.Startable != 0 {
		t.Fatalf("work = %+v, want the running run counted as active and the second task (waiting for the limit of 1) not startable", *w)
	}
}

// AC-17: 学びの段だけに対象が残っているとき、いま始められるものは 0 件になる（改修の提案・片付けも同じ）。
func TestCountRoundWork_AC17_OnlyTheLearnPhaseLeftIsZero(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	f.seedLearnAndImprove(t)
	staleRunForCycleWork(t, f.s, mChallenge(t, f.s, "stale"))
	w := countRound(t, f)
	if w.Startable != 0 || w.Stages != (CycleWorkStages{}) {
		t.Fatalf("work = %+v, want 0 startable (learn/improve/cleanup are not counted)", *w)
	}
	// 多めに数える側（サイクルを始めるかの数え）は、同じ状態を数える（S2b のまま。AC-37）。
	old, err := f.s.CountCycleWork(context.Background(), f.agent, f.conn)
	if err != nil {
		t.Fatal(err)
	}
	if old.Stages.Learn != 1 || old.Stages.Cleanup != 1 || old.Stages.Improve != 1 || old.Total != 3 {
		t.Fatalf("CountCycleWork = %+v, want learn, improve and cleanup counted (unchanged S2b rule)", *old)
	}
}

// AC-18・AC-19: 回答が記録された保留を持つ実行は、いま始められるものに数える。委譲先が作業している実行（form: cli）は、作業中の実行に数える。
func TestCountRoundWork_AC18_AC19_AnsweredHoldIsStartableAndRunningRunIsActive(t *testing.T) {
	t.Parallel()
	f := newOwnerWaitFixture(t, 2)
	f.startFirst(t)
	if w := countRound(t, f.delegateFixture); w.Active != 1 || w.Startable != 0 { // AC-19
		t.Fatalf("running: work = %+v, want active 1 and startable 0", *w)
	}
	f.holdFirst(t)
	f.answerFirst(t)
	if w := countRound(t, f.delegateFixture); w.Stages.Run != 1 { // AC-18
		t.Fatalf("answered: work = %+v, want the run with the recorded answer counted in the run stage", *w)
	}
}

// AC-21: オーナーへの問いを返して待っているだけの実行（課題は保留）は、作業中の実行にも、いま始められるものにも数えない。
func TestCountRoundWork_AC21_OwnerWaitingRunIsNeitherActiveNorStartable(t *testing.T) {
	t.Parallel()
	f := newOwnerWaitFixture(t, 2)
	f.startFirst(t)
	f.holdFirst(t)
	if w := countRound(t, f.delegateFixture); w.Active != 0 || w.Startable != 0 {
		t.Fatalf("work = %+v, want nothing active and nothing startable", *w)
	}
	// サイクルを始めるかの数え（多めに数える）は、この実行を数える（S2b のまま）。
	if old, err := f.s.CountCycleWork(context.Background(), f.agent, f.conn); err != nil || old.Total == 0 {
		t.Fatalf("CountCycleWork = %+v, %v, want the open run counted by the generous count", old, err)
	}
}

// AC-22・AC-23: 完了の承認を待っているだけの課題の実行は作業中の実行に数えず、その課題はいま始められるものにも数えない。
func TestCountRoundWork_AC22_AC23_CompletionApprovalWaitIsNeitherActiveNorStartable(t *testing.T) {
	t.Parallel()
	f := newOwnerWaitFixture(t, 2)
	f.startFirst(t)
	f.pc.set("ext-1", reviewWaitOut())
	f.stage(t)
	f.setStatusRaw(t, f.first, StatusAwaitingCompletionApproval)
	w := countRound(t, f.delegateFixture)
	if w.Active != 0 { // AC-22
		t.Errorf("active = %d, want 0", w.Active)
	}
	if w.Startable != 0 { // AC-23
		t.Errorf("startable = %d (%+v), want 0", w.Startable, w.Stages)
	}
}
