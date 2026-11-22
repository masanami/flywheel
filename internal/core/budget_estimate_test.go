package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// 区切り S4c-1 の土台: 見込み（取りやめでないタスクの枠の合計）を、承認の前の計画と承認の後の課題で同じ関数が数えること
// （AC-8〜13・21・22）と、課題の上限の読み書き。

// 計画の額 def は実装 30・レビュー対応 20 の合計 50（USD の 100 万分の 1 で書く）。
var estimateDef = &planBudgets{Impl: 30_000_000, Review: 20_000_000}

func estimateOf(t *testing.T, bs []taskBudget) (float64, bool) {
	t.Helper()
	sum, ok := sumTaskBudgets(bs)
	return microsToUSD(sum.Impl + sum.Review), ok
}

// AC-8: タスクが 1 個で枠が無い計画の見込みは、サイズから解決した実装の額とレビュー対応の額の合計。
func TestEstimate_SoleTaskWithoutFramesUsesThePlanAmounts_AC8(t *testing.T) {
	t.Parallel()
	got, ok := estimateOf(t, planVersionBudgets(1, []planTaskFrame{{}}, estimateDef, nil))
	if !ok || got != 50 {
		t.Errorf("estimate = %v (known %v), want 50 (30 + 20)", got, ok)
	}
}

// AC-9: 枠を持つタスクが 3 個の計画の見込みは、各タスクの実装の枠とレビュー対応の枠を足した合計。
func TestEstimate_ThreeTasksWithFramesAreSummed_AC9(t *testing.T) {
	t.Parallel()
	frames := []planTaskFrame{{i64(1_000_000), i64(2_000_000)}, {i64(3_000_000), i64(4_000_000)}, {i64(5_000_000), i64(6_000_000)}}
	got, ok := estimateOf(t, planVersionBudgets(1, frames, estimateDef, nil))
	if !ok || got != 21 {
		t.Errorf("estimate = %v (known %v), want 21; the plan amounts (50) must not be read when every task has frames", got, ok)
	}
	// 計画の額が決められなくても、全部のタスクが枠を持てば決められる。
	if got, ok := estimateOf(t, planVersionBudgets(1, frames, nil, nil)); !ok || got != 21 {
		t.Errorf("estimate without the plan amounts = %v (known %v), want 21", got, ok)
	}
}

// AC-10: タスクが 2 個で、片方が枠を省いた計画の見込みは、省いたタスクを計画の額で埋めた合計。
func TestEstimate_AnOmittedFrameIsFilledWithThePlanAmounts_AC10(t *testing.T) {
	t.Parallel()
	frames := []planTaskFrame{{i64(1_000_000), i64(2_000_000)}, {}}
	got, ok := estimateOf(t, planVersionBudgets(1, frames, estimateDef, nil))
	if !ok || got != 3+50 {
		t.Errorf("estimate = %v (known %v), want 3 + 50", got, ok)
	}
	// 片方の側だけ省いたタスクは、省いた側だけを埋める（承認でタスクへ記録する値と同じ決まり）。
	half := []planTaskFrame{{i64(1_000_000), nil}}
	if got, ok := estimateOf(t, planVersionBudgets(1, half, estimateDef, nil)); !ok || got != 1+20 {
		t.Errorf("estimate = %v (known %v), want 1 + 20", got, ok)
	}
}

// AC-21 の根拠: 枠を決められないタスクがある計画（計画の額が決められない）の見込みは、不明。
func TestEstimate_UnknownWhenAFrameCannotBeDecided_AC21(t *testing.T) {
	t.Parallel()
	if _, ok := estimateOf(t, planVersionBudgets(1, []planTaskFrame{{i64(1_000_000), i64(1_000_000)}, {}}, nil, nil)); ok {
		t.Error("estimate is known, want unknown (a task has no frame and no plan amount exists)")
	}
	if _, ok := estimateOf(t, nil); ok {
		t.Error("estimate of no task is known, want unknown")
	}
}

// AC-11: 取りやめのタスクの枠は見込みに入らない。AC-12: 済みのタスクの枠は入る。
func TestEstimate_CancelledIsLeftOutAndDoneIsCounted_AC11_AC12(t *testing.T) {
	t.Parallel()
	bs := []taskBudget{
		{TaskID: 1, PlanVersion: 1, Status: TaskStatusCancelled, Impl: 100_000_000, Review: 100_000_000, Known: true},
		{TaskID: 2, PlanVersion: 1, Status: TaskStatusDone, Impl: 2_000_000, Review: 1_000_000, Known: true},
		{TaskID: 3, PlanVersion: 1, Status: TaskStatusPending, Impl: 4_000_000, Review: 3_000_000, Known: true},
	}
	if got, ok := estimateOf(t, bs); !ok || got != 10 {
		t.Errorf("estimate = %v (known %v), want 10 (the cancelled task's 200 is left out, the done task's 3 is counted)", got, ok)
	}
}

// AC-13: 済みのタスクが残る課題で作り直した計画の見込みは、残る済みのタスクの枠と、新しい版のタスクの枠の合計。
// 前の版の未完のタスクは、新しい版の承認で取りやめになるので入れない。
func TestEstimate_ReplanKeepsDoneTasksOfOtherVersions_AC13(t *testing.T) {
	t.Parallel()
	bs := []taskBudget{
		{TaskID: 1, PlanVersion: 1, Status: TaskStatusDone, Impl: 2_000_000, Review: 1_000_000, Known: true},
		{TaskID: 2, PlanVersion: 1, Status: TaskStatusInProgress, Impl: 40_000_000, Review: 40_000_000, Known: true},
	}
	frames := []planTaskFrame{{i64(10_000_000), i64(5_000_000)}, {i64(20_000_000), i64(6_000_000)}}
	if got, ok := estimateOf(t, planVersionBudgets(2, frames, estimateDef, bs)); !ok || got != 3+41 {
		t.Errorf("estimate = %v (known %v), want 3 (done) + 41 (new version), without the unfinished task's 80", got, ok)
	}
	// 新しい版の行が揃った後（承認の後）は、その行の実効値で数え、同じ見込みになる。
	approved := append(append([]taskBudget{}, bs...),
		taskBudget{TaskID: 3, PlanVersion: 2, Status: TaskStatusPending, Impl: 10_000_000, Review: 5_000_000, Known: true},
		taskBudget{TaskID: 4, PlanVersion: 2, Status: TaskStatusPending, Impl: 20_000_000, Review: 6_000_000, Known: true})
	approved[1].Status = TaskStatusCancelled
	after, _ := estimateOf(t, approved)
	before, _ := estimateOf(t, planVersionBudgets(2, frames, estimateDef, bs))
	if after != before {
		t.Errorf("estimate after approval = %v, before = %v; want the same value", after, before)
	}
	// 承認の後に `flywheel budget --task` で増額した行は、増額した額で数える。
	approved[2].Impl += 7_000_000
	if got, _ := estimateOf(t, planVersionBudgets(2, frames, estimateDef, approved)); got != 44+7 {
		t.Errorf("estimate with a raised task = %v, want 51", got)
	}
}

// 承認の前の計画の見込み（GetPlanView）と、承認した直後の課題の見込み（GetChallengeBudget）は同じ値。
func TestEstimate_PlanViewBeforeApprovalEqualsChallengeBudgetAfter(t *testing.T) {
	agent := &AgentDeclaration{SizeBudgetsUSD: SizeBudgetsUSD{S: SizeBudgetPair{30, 25}, M: SizeBudgetPair{50, 30}, L: SizeBudgetPair{100, 40}}}
	for _, tc := range []struct {
		name   string
		tasks  int
		integ  bool
		want   float64
		useDef bool
	}{
		{"three tasks with frames", 3, true, 15, false},
		{"sole task without frames", 1, false, 41.5 + 30, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPAFixture(t, tc.tasks, tc.integ)
			a := agent
			pv, err := f.s.GetPlanView(context.Background(), f.id, a)
			if err != nil || pv.BudgetUSD == nil {
				t.Fatalf("GetPlanView: %v, budget %v", err, pv)
			}
			if *pv.BudgetUSD != tc.want {
				t.Errorf("estimate before approval = %v, want %v", *pv.BudgetUSD, tc.want)
			}
			if err := f.approve(t, ttyAtt(t, f.id)); err != nil {
				t.Fatal(err)
			}
			b, err := f.s.GetChallengeBudget(context.Background(), f.id, a)
			if err != nil {
				t.Fatal(err)
			}
			if !b.EstimateKnown || b.ImplEstimateUSD+b.ReviewEstimateUSD != *pv.BudgetUSD {
				t.Errorf("estimate after approval = %v (known %v), want the same %v", b.ImplEstimateUSD+b.ReviewEstimateUSD, b.EstimateKnown, *pv.BudgetUSD)
			}
		})
	}
}

// 課題の詳細の budget。上限の記録が無い課題の上限は見込みと同じ額（AC-220 の読み方）。
func TestChallengeDetailBudget_LimitDefaultsToTheEstimate(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 4, 5))
	d, err := f.s.GetChallenge(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Budget.EstimateUSD == nil || *d.Budget.EstimateUSD != 14 || d.Budget.LimitUSD == nil || *d.Budget.LimitUSD != 14 || d.Budget.LimitReason != nil {
		t.Errorf("budget = %+v, want estimate 14, limit 14 (no record), no reason", d.Budget)
	}
	b, err := f.s.GetChallengeBudget(context.Background(), id, f.agent)
	if err != nil || !b.LimitKnown || b.LimitUSD != 14 || b.LimitReason != nil {
		t.Errorf("GetChallengeBudget = %+v, err %v; want limit 14 and no reason", b, err)
	}
}

// AC-21・AC-22: 枠を決められないタスクがある課題の詳細の見込みと上限は、どちらも null（記録した上限があっても）。
func TestChallengeDetailBudget_UnknownEstimateHasNullEstimateAndLimit_AC21_AC22(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "legacy", "P2", "")
	cid, _ := parseChallengeID(id)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO task (challenge_id, plan_version, key, title, body, done_criteria, repo, operation, status, created_at, updated_at)
			VALUES (?, 1, 'a', 't', 'b', 'd', 'o/r', 'pull_request', 'pending', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z')`, cid); err != nil {
			return err
		}
		return setChallengeBudgetLimit(context.Background(), tx, cid, i64(9_000_000), nil)
	})
	d, err := f.s.GetChallenge(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Budget.EstimateUSD != nil {
		t.Errorf("AC-21: estimate = %v, want nil", *d.Budget.EstimateUSD)
	}
	if d.Budget.LimitUSD != nil {
		t.Errorf("AC-22: limit = %v, want nil", *d.Budget.LimitUSD)
	}
}

// 計画の承認待ちの課題の budget は、承認を待っている版の計画の値。
func TestChallengeDetailBudget_AwaitingPlanApprovalShowsThePlanValue(t *testing.T) {
	f := newPAFixture(t, 3, true)
	d := f.detail(t, f.id)
	if d.Budget.EstimateUSD == nil || *d.Budget.EstimateUSD != 15 || d.Budget.LimitUSD == nil || *d.Budget.LimitUSD != 15 {
		t.Errorf("budget = %+v, want the plan's estimate 15 and limit 15", d.Budget)
	}
}

func TestSetChallengeBudgetLimitChecked(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 4, 5)) // 見込み 14
	ctx := context.Background()
	if err := f.s.setChallengeBudgetLimitChecked(ctx, ChannelCLI, id, 13.9, "", f.agent); !errors.Is(err, ErrValidation) {
		t.Errorf("smaller than the estimate: err = %v, want ErrValidation", err)
	}
	if err := f.s.setChallengeBudgetLimitChecked(ctx, ChannelCLI, id, 0, "", f.agent); !errors.Is(err, ErrValidation) {
		t.Errorf("zero: err = %v, want ErrValidation", err)
	}
	if err := f.s.setChallengeBudgetLimitChecked(ctx, ChannelCLI, "C-9999", 20, "", f.agent); !errors.Is(err, ErrNotFound) {
		t.Errorf("no challenge: err = %v, want ErrNotFound", err)
	}
	before, _ := f.s.GetChallenge(ctx, id)
	if err := f.s.setChallengeBudgetLimitChecked(ctx, ChannelCLI, id, 20, "続きを足す", f.agent); err != nil {
		t.Fatal(err)
	}
	b, err := f.s.GetChallengeBudget(ctx, id, f.agent)
	if err != nil || b.LimitUSD != 20 || b.LimitReason == nil || *b.LimitReason != "続きを足す" || b.ImplEstimateUSD+b.ReviewEstimateUSD != 14 {
		t.Errorf("budget = %+v, err %v; want limit 20 with the reason, estimate still 14", b, err)
	}
	after, _ := f.s.GetChallenge(ctx, id)
	if after.Version != before.Version || after.Status != before.Status {
		t.Errorf("version/status = %d/%s, want unchanged %d/%s", after.Version, after.Status, before.Version, before.Status)
	}
	var logged []Activity
	acts, _ := f.s.ListActivities(ctx, &id)
	for _, a := range acts {
		if a.Action == ActivityActionBudgetLimit {
			logged = append(logged, a)
		}
	}
	if len(logged) != 1 || string(logged[0].Before) != `{"limit_usd":14}` {
		t.Errorf("activities = %+v, want one budget_limit entry whose before is the previous limit 14", logged)
	}
}

// 足したタスク（同じ版の行が計画のタスクより多い）も、承認の前後で同じ関数が見込みに入れる。
func TestEstimate_AddedTasksOfTheApprovedVersionStayInTheEstimate(t *testing.T) {
	t.Parallel()
	frames := []planTaskFrame{{i64(1_000_000), i64(2_000_000)}}
	bs := []taskBudget{
		{TaskID: 1, PlanVersion: 1, Status: TaskStatusDone, Impl: 1_000_000, Review: 2_000_000, Known: true},
		{TaskID: 2, PlanVersion: 1, Status: TaskStatusPending, Impl: 8_000_000, Review: 4_000_000, Known: true}, // 足したタスク
	}
	if got, ok := estimateOf(t, planVersionBudgets(1, frames, estimateDef, bs)); !ok || got != 15 {
		t.Errorf("estimate = %v (known %v), want 15 including the added task", got, ok)
	}
}
