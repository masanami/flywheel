package core

// このファイルは #83 の予算ガードと「課題ごとに終了していない run は高々 1 つ」
// の順序を検証する: 終了していない run を持つ課題は、周の予算が足りなくても
// ErrRunInProgress で終わる（ErrBudgetExceeded と取り違えない）。

import (
	"context"
	"errors"
	"testing"
)

func TestRunJudgment_CycleBudgetGuard_ActiveRunTakesPrecedenceOverBudget(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	cyc, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "test", BudgetUSD: 1, Exclusive: false})
	if err != nil {
		t.Fatalf("BeginCycle: %v", err)
	}
	id := createChallengeForJudgmentTest(t, s)

	invoked := make(chan struct{})
	block := make(chan struct{})
	inv1 := &fakeJudgmentInvoker{
		invoked: invoked, block: block,
		result: JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{}`), ReportedTotalCostUSD: float64Ptr(0.5)},
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = s.RunJudgment(ctx, RunJudgmentInput{
			ChallengeID: id, Judgment: JudgmentJ1, MaxBudgetUSD: 1, Stdin: []byte("x"), Invoker: inv1, CycleID: &cyc.ID,
		})
	}()
	<-invoked // 予約額 1 で周の上限額 1 を使い切っている

	inv2 := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{}`)}}
	_, err = s.RunJudgment(ctx, RunJudgmentInput{
		ChallengeID: id, Judgment: JudgmentJ1, MaxBudgetUSD: 1, Stdin: []byte("x"), Invoker: inv2, CycleID: &cyc.ID,
	})
	if !errors.Is(err, ErrRunInProgress) {
		t.Errorf("RunJudgment(same challenge) error = %v, want ErrRunInProgress", err)
	}

	close(block)
	<-done
}
