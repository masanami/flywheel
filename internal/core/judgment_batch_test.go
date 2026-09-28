package core

import (
	"context"
	"testing"
)

// countingJudgmentInvoker は起動回数を数え、n 回目（1 始まり）の結果を
// results[n-1] で返す偽の判断の IF。
type countingJudgmentInvoker struct {
	calls   int
	results []JudgmentLaunchOutput
}

func (f *countingJudgmentInvoker) Available(_ context.Context) error { return nil }

func (f *countingJudgmentInvoker) InvokeJudgment(_ context.Context, _ JudgmentLaunchInput) (JudgmentLaunchOutput, error) {
	f.calls++
	return f.results[f.calls-1], nil
}

// AC「枠超過を記録した周では、その後の判断の呼び出しが起動されず、対象の課題が
// not_started に理由 rate_limited で出る（未分類の課題を 3 件置き、1 件目で
// 枠超過を返して検証する）」・「枠超過で起動しなかった課題の状態と版は変わらない」。
func TestJudgmentCycle_StopsLaunchingAfterRateLimited(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	ids := []string{
		createChallengeForJudgmentTest(t, s),
		createChallengeForJudgmentTest(t, s),
		createChallengeForJudgmentTest(t, s),
	}
	before := make(map[string]*ChallengeDetail, len(ids))
	for _, id := range ids {
		ch, err := s.GetChallenge(ctx, id)
		if err != nil {
			t.Fatalf("GetChallenge: %v", err)
		}
		before[id] = ch
	}

	inv := &countingJudgmentInvoker{results: []JudgmentLaunchOutput{
		{Result: RunResultErrored, RateLimited: true},
		{Result: RunResultSucceeded, StructuredOutput: []byte(`{}`)},
		{Result: RunResultSucceeded, StructuredOutput: []byte(`{}`)},
	}}
	var c JudgmentCycle
	var notStarted []NotStarted
	for i, id := range ids {
		res, ns, err := c.RunJudgment(ctx, s, RunJudgmentInput{
			ChallengeID: id, Judgment: JudgmentJ1, MaxBudgetUSD: 1, Stdin: []byte("x"), Invoker: inv,
		})
		if err != nil {
			t.Fatalf("RunJudgment[%d]: %v", i, err)
		}
		if i == 0 {
			if res == nil || !res.RateLimited || ns != nil {
				t.Fatalf("first call: res=%+v ns=%+v, want a rate-limited run and no not_started", res, ns)
			}
			continue
		}
		if res != nil || ns == nil {
			t.Fatalf("call %d: res=%+v ns=%+v, want not_started", i, res, ns)
		}
		notStarted = append(notStarted, *ns)
	}

	if inv.calls != 1 {
		t.Errorf("invoker calls = %d, want 1 (no launch after rate_limited)", inv.calls)
	}
	if !c.RateLimited() {
		t.Error("RateLimited() = false, want true")
	}
	if len(notStarted) != 2 || notStarted[0].ChallengeID != ids[1] || notStarted[1].ChallengeID != ids[2] {
		t.Fatalf("not_started = %+v, want %s and %s", notStarted, ids[1], ids[2])
	}
	for _, ns := range notStarted {
		if ns.Reason != NotStartedRateLimited || string(ns.Reason) != "rate_limited" {
			t.Errorf("not_started reason = %q, want rate_limited", ns.Reason)
		}
	}

	for _, id := range ids {
		after, err := s.GetChallenge(ctx, id)
		if err != nil {
			t.Fatalf("GetChallenge: %v", err)
		}
		if after.Status != before[id].Status || after.Version != before[id].Version {
			t.Errorf("%s changed: status %s→%s version %d→%d", id, before[id].Status, after.Status, before[id].Version, after.Version)
		}
	}
	for _, id := range ids[1:] {
		runs, err := s.ListRuns(ctx, RunListOptions{ChallengeID: &id})
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		if len(runs) != 0 {
			t.Errorf("%s has %d runs, want 0 (not launched)", id, len(runs))
		}
	}
}

// 枠超過でない run が続く周は、すべて起動する。
func TestJudgmentCycle_LaunchesAllWhenNotRateLimited(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	inv := &countingJudgmentInvoker{results: []JudgmentLaunchOutput{
		{Result: RunResultErrored},
		{Result: RunResultSucceeded, StructuredOutput: []byte(`{}`)},
	}}
	var c JudgmentCycle
	for i := 0; i < 2; i++ {
		id := createChallengeForJudgmentTest(t, s)
		res, ns, err := c.RunJudgment(ctx, s, RunJudgmentInput{
			ChallengeID: id, Judgment: JudgmentJ1, MaxBudgetUSD: 1, Stdin: []byte("x"), Invoker: inv,
		})
		if err != nil || res == nil || ns != nil {
			t.Fatalf("call %d: res=%+v ns=%+v err=%v", i, res, ns, err)
		}
	}
	if inv.calls != 2 || c.RateLimited() {
		t.Errorf("calls=%d rateLimited=%t, want 2 and false", inv.calls, c.RateLimited())
	}
}
