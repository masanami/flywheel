package core

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// M4.5 S4 AC-390〜AC-397・AC-399: MetricsCompare は実ストアの行から適用の時点の前後の組を数える。

func mAt(i int) string { return fmt.Sprintf("2026-03-%02dT12:00:00.000Z", i) }

func mDone(t *testing.T, s *Store, title string, day int) int64 {
	t.Helper()
	id := mChallenge(t, s, title)
	mApproval(t, s, id, "completion", "approved", "cli", "tty_confirm", mAt(day), nil)
	return id
}

func mRelease(t *testing.T, s *Store, cid int64, state string, executedAt any) {
	t.Helper()
	mexec(t, s, `INSERT INTO operation (challenge_id, kind, summary, ref, state, version, created_at, executed_at)
		VALUES (?, 'release', 's', 'r', ?, 1, '2026-01-01T00:00:00.000Z', ?)`, cid, state, executedAt)
}

func compare(t *testing.T, s *Store, cid int64) *MetricsCompareResult {
	t.Helper()
	r, err := s.MetricsCompare(context.Background(), fmt.Sprintf("C-%d", cid))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestMetricsCompare_AppliedAtIsLatestReleaseExecutionElseCompletionApproval(t *testing.T) {
	s := newStoreForTest(t)
	a := mDone(t, s, "a", 10)
	if got := compare(t, s, a).AppliedAt; got != mAt(10) {
		t.Errorf("no release: applied_at = %s, want completion approval %s", got, mAt(10))
	}
	mRelease(t, s, a, "executed", mAt(5))
	mRelease(t, s, a, "executed", mAt(7))
	mRelease(t, s, a, "rejected", nil)
	r := compare(t, s, a)
	if r.AppliedAt != mAt(7) || r.ChallengeID != fmt.Sprintf("C-%d", a) {
		t.Errorf("result = %+v, want the latest executed_at %s", r, mAt(7))
	}
}

func TestMetricsCompare_GroupsAreNearestTenExcludingTheTargetAndTheBoundary(t *testing.T) {
	s := newStoreForTest(t)
	for d := 1; d <= 12; d++ { // 12 件が前
		mRun(t, s, mDone(t, s, "b", d), "delegate", nil, int64(d)*1_000_000)
	}
	target := mDone(t, s, "t", 15)
	mDone(t, s, "same", 15)     // 同時刻は前にも後にも入らない
	for d := 16; d <= 20; d++ { // 5 件が後
		mRun(t, s, mDone(t, s, "a", d), "delegate", nil, int64(d)*1_000_000)
	}
	mChallenge(t, s, "never approved") // 完了の承認なしは入らない
	// 差し戻しだけの課題は入らない。
	rej := mChallenge(t, s, "rej")
	mApproval(t, s, rej, "completion", "rejected", "cli", "tty_confirm", mAt(14), `["issues"]`)
	mRun(t, s, rej, "delegate", nil, 200_000_000)

	r := compare(t, s, target)
	if r.Before.TargetChallenges != 10 || r.After.TargetChallenges != 5 {
		t.Fatalf("targets before/after = %d/%d, want 10/5", r.Before.TargetChallenges, r.After.TargetChallenges)
	}
	// 組の中身は費用の平均で固定する: 前は day 3〜12（古い day 1・2、同時刻の課題、差し戻しだけの課題は入らない）、後は day 16〜20。
	if m := r.Before.CostPerChallenge.Mean; m == nil || *m != 7.5 {
		t.Errorf("before cost mean = %v, want 7.5 (days 3..12)", m)
	}
	if m := r.After.CostPerChallenge.Mean; m == nil || *m != 18 {
		t.Errorf("after cost mean = %v, want 18 (days 16..20)", m)
	}
	// 前の組は新しい順に 10 件: 古い 2 件（day 1・2）は入らない。承認の数 = 課題数。
	if r.Before.ApprovalCount.Total != 10 || r.After.ApprovalCount.Total != 5 {
		t.Errorf("approval totals = %d/%d, want 10/5 (only the group's challenges)", r.Before.ApprovalCount.Total, r.After.ApprovalCount.Total)
	}
}

func TestMetricsCompare_GroupMetricsUseTheGroupsChallengesOnly(t *testing.T) {
	s := newStoreForTest(t)
	b1 := mDone(t, s, "b1", 1)
	mApproval(t, s, b1, "completion", "rejected", "cli", "tty_confirm", mAt(1), `["issues"]`)
	mRun(t, s, b1, "delegate", nil, 2_000_000)
	b2 := mDone(t, s, "b2", 2)
	mRun(t, s, b2, "delegate", nil, 4_000_000)
	target := mDone(t, s, "t", 5)
	mRun(t, s, target, "delegate", nil, 99_000_000) // 比べる課題自身は数えない
	a1 := mDone(t, s, "a1", 8)
	first := mRun(t, s, a1, "delegate", nil, 1_000_000)
	mRun(t, s, a1, "delegate", first, nil)
	other := mChallenge(t, s, "outside")
	mApproval(t, s, other, "completion", "rejected", "cli", "tty_confirm", mAt(3), `["figure"]`)

	r := compare(t, s, target)
	bg, ag := r.Before, r.After
	if bg.TargetChallenges != 2 || bg.RejectionRate.Numerator != 1 || bg.RejectionRate.Denominator != 2 {
		t.Errorf("before = %+v", bg)
	}
	if bg.RejectionRate.ByField["figure"] != 0 || bg.RejectionRate.ByField["issues"] != 1 {
		t.Errorf("before by_field = %v (the outside challenge must not count)", bg.RejectionRate.ByField)
	}
	if bg.CostPerChallenge.Mean == nil || *bg.CostPerChallenge.Mean != 3 {
		t.Errorf("before cost mean = %v, want 3", bg.CostPerChallenge.Mean)
	}
	if bg.ApprovalCount.Total != 3 { // b1: approved + rejected, b2: approved
		t.Errorf("before approvals = %+v, want 3", bg.ApprovalCount)
	}
	if ag.TargetChallenges != 1 || ag.ResumeCount.Total != 1 || ag.CostPerChallenge.UnknownCostRuns != 1 {
		t.Errorf("after = %+v", ag)
	}
	if ag.ApprovalCount.By[0].Kind != "completion" || ag.ApprovalCount.By[0].Decision != "approved" || ag.ApprovalCount.By[0].Count != 1 {
		t.Errorf("after approval rows = %+v", ag.ApprovalCount.By)
	}
}

func TestMetricsCompare_ShortGroupsAndEmpty(t *testing.T) {
	s := newStoreForTest(t)
	only := mDone(t, s, "only", 5)
	r := compare(t, s, only)
	if r.Before.TargetChallenges != 0 || r.After.TargetChallenges != 0 || r.Before.RejectionRate.Value != nil {
		t.Errorf("empty groups = %+v", r)
	}
	if r.Before.ApprovalCount.By == nil || len(r.Before.ApprovalCount.By) != 0 {
		t.Errorf("by must be an empty non-nil slice: %#v", r.Before.ApprovalCount.By)
	}
}

func TestMetricsCompare_Errors(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	if _, err := s.MetricsCompare(ctx, "C-999"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if _, err := s.MetricsCompare(ctx, "bogus"); !errors.Is(err, ErrNotFound) {
		t.Errorf("malformed: %v", err)
	}
	notDone := mChallenge(t, s, "x")
	mexec(t, s, `UPDATE challenge SET status = 'in_progress' WHERE id = ?`, notDone)
	if _, err := s.MetricsCompare(ctx, fmt.Sprintf("C-%d", notDone)); !errors.Is(err, ErrValidation) {
		t.Errorf("not done: %v", err)
	}
	for _, st := range []string{"pending", "approved"} {
		c := mDone(t, s, st, 3)
		mRelease(t, s, c, "executed", mAt(3))
		mRelease(t, s, c, st, nil)
		if _, err := s.MetricsCompare(ctx, fmt.Sprintf("C-%d", c)); !errors.Is(err, ErrValidation) {
			t.Errorf("%s release: %v", st, err)
		}
	}
}

func TestMetricsCompare_DoesNotWriteAndIsDeterministic(t *testing.T) {
	s := newStoreForTest(t)
	mDone(t, s, "b", 1)
	c := mDone(t, s, "t", 2)
	mDone(t, s, "a", 3)
	r1, r2 := compare(t, s, c), compare(t, s, c)
	if !reflect.DeepEqual(r1, r2) {
		t.Errorf("two runs differ: %+v vs %+v", r1, r2)
	}
}
