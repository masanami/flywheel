package core

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

// 区切り S4c-2 の 3: 上げる先の決め方と、タスクとして上げる書き込み（AC-1〜AC-19・AC-48〜AC-51）。

func destFloat(v float64) *float64 { return &v }

// destFixtureFollowup は newExpandFixture の課題（見込み 10 USD・上限 14 USD）に属する未のフォローアップを 1 件足す。
func destFixtureFollowup(t *testing.T, f *paFixture) string {
	t.Helper()
	return s4c2OpenFollowup(t, f.s, f.id)
}

// 足しても上限以内に収まる枠（1+1 USD）。
var destSmall = FollowupPromoteOptions{BudgetImplUSD: destFloat(1), BudgetReviewUSD: destFloat(1)}

func destCountChallenges(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	writeForTest(t, s, func(tx *sql.Tx) error { return tx.QueryRow(`SELECT COUNT(*) FROM challenge`).Scan(&n) })
	return n
}

func destPromote(t *testing.T, s *Store, id string, opts FollowupPromoteOptions) (*FollowupPromotePlan, *FollowupPromoteResult, error) {
	t.Helper()
	ctx := context.Background()
	plan, err := s.PrepareFollowupPromote(ctx, id, opts, nil)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	res, err := s.PromoteFollowup(ctx, plan, fuAtt(t, id), nil)
	return plan, res, err
}

// AC-1・AC-2・AC-48・AC-49・AC-50
func TestPromoteDest_InProgressAddableChallengeGetsAPendingFollowupAddition(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	fid := destFixtureFollowup(t, f)
	before := destCountChallenges(t, f.s)
	plan, res, err := destPromote(t, f.s, fid, destSmall)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if plan.Dest != FollowupPromoteToTask || plan.NewChallengeReason != "" || plan.TargetChallengeID != f.id {
		t.Errorf("plan = %+v, want task for %s", plan, f.id)
	}
	if res.Dest != FollowupPromoteToTask || res.Challenge != nil || res.Addition == nil {
		t.Fatalf("result = %+v", res)
	}
	if after := destCountChallenges(t, f.s); after != before {
		t.Errorf("challenges %d -> %d, want no new challenge (AC-1)", before, after)
	}
	a := res.Addition
	if a.ChallengeID != f.id || a.Origin != TaskAdditionOriginFollowup || a.State != TaskAdditionPending || a.FollowupID == nil || *a.FollowupID != fid {
		t.Errorf("addition = %+v (AC-2)", a)
	}
	if res.Followup.Status != FollowupStatusOpen || res.Followup.PromotedTaskID != nil {
		t.Errorf("followup = %+v, want still open (AC-48)", res.Followup)
	}
	adds := f.additions(t)
	if len(adds) != 1 || adds[0].ID != a.ID {
		t.Errorf("additions = %+v", adds)
	}
}

// AC-3
func TestPromoteDest_HeldChallengeFromInProgressGetsAnAddition(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	f.raiseHold(t, StatusInProgress)
	fid := destFixtureFollowup(t, f)
	plan, res, err := destPromote(t, f.s, fid, destSmall)
	if err != nil || plan.Dest != FollowupPromoteToTask || res.Addition == nil || res.Addition.State != TaskAdditionPending {
		t.Fatalf("plan=%+v res=%+v err=%v", plan, res, err)
	}
}

// AC-4・AC-5
func TestPromoteDest_NewChallengeFlagMakesAChallengeAndNoAddition(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	fid := destFixtureFollowup(t, f)
	before := destCountChallenges(t, f.s)
	plan, res, err := destPromote(t, f.s, fid, FollowupPromoteOptions{NewChallenge: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Dest != FollowupPromoteToChallenge || res.Challenge == nil || res.Addition != nil {
		t.Errorf("plan=%+v res=%+v", plan, res)
	}
	if after := destCountChallenges(t, f.s); after != before+1 {
		t.Errorf("challenges %d -> %d, want +1", before, after)
	}
	if len(f.additions(t)) != 0 {
		t.Errorf("additions = %+v, want none (AC-5)", f.additions(t))
	}
}

// AC-6・AC-10〜AC-13・AC-16: 足せない状態の課題は新しい課題になり、理由が出る。
func TestPromoteDest_UnaddableStatusesGoToNewChallengeWithReason(t *testing.T) {
	t.Parallel()
	for _, st := range []Status{StatusAwaitingPlanApproval, StatusVerifying, StatusAwaitingCompletionApproval, StatusDone, StatusWithdrawn} {
		st := st
		t.Run(string(st), func(t *testing.T) {
			t.Parallel()
			f := newExpandFixture(t)
			fid := destFixtureFollowup(t, f)
			f.setChallengeStatus(t, st)
			plan, err := f.s.PrepareFollowupPromote(context.Background(), fid, destSmall, nil)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Dest != FollowupPromoteToChallenge || plan.NewChallengeReason == "" {
				t.Errorf("plan = %+v, want new challenge with a reason", plan)
			}
			before := destCountChallenges(t, f.s)
			_, res, err := destPromote(t, f.s, fid, destSmall)
			if err != nil || res.Challenge == nil || res.Addition != nil || destCountChallenges(t, f.s) != before+1 {
				t.Errorf("res=%+v err=%v", res, err)
			}
		})
	}
}

// AC-7・AC-8: 見込みが不明の課題は新しい課題になり、理由に「枠を決められないタスクがある」が出る。
func TestPromoteDest_UnknownEstimateGoesToNewChallengeWithReason(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	f.makeEstimateUnknown(t)
	fid := destFixtureFollowup(t, f)
	plan, err := f.s.PrepareFollowupPromote(context.Background(), fid, destSmall, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Dest != FollowupPromoteToChallenge || !strings.Contains(plan.NewChallengeReason, "枠を決められないタスクがある") {
		t.Errorf("plan = %+v", plan)
	}
}

// AC-14・AC-15: 統合先を使わない課題は新しい課題になり、理由が出る。
func TestPromoteDest_NoIntegrationGoesToNewChallengeWithReason(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET uses_integration = 0 WHERE id = ?`, f.cid())
		return err
	})
	fid := destFixtureFollowup(t, f)
	plan, err := f.s.PrepareFollowupPromote(context.Background(), fid, destSmall, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Dest != FollowupPromoteToChallenge || !strings.Contains(plan.NewChallengeReason, "統合先を使わない") {
		t.Errorf("plan = %+v", plan)
	}
}

// AC-9: 課題に属さないフォローアップは新しい課題になる。
func TestPromoteDest_FollowupWithoutChallengeGoesToNewChallenge(t *testing.T) {
	t.Parallel()
	s := newFollowupStore(t)
	f := fuCreate(t, s, "", "ひとり", "")
	plan, res, err := destPromote(t, s, f.ID, FollowupPromoteOptions{})
	if err != nil || plan.Dest != FollowupPromoteToChallenge || res.Challenge == nil {
		t.Fatalf("plan=%+v res=%+v err=%v", plan, res, err)
	}
}

// AC-17: 枠を指定しなければ宣言のサイズ S の額。
func TestPromoteDest_DefaultFramesAreTheDeclaredSizeS(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	fid := destFixtureFollowup(t, f)
	decl := &AgentDeclaration{SizeBudgetsUSD: SizeBudgetsUSD{S: SizeBudgetPair{Impl: 0.5, Review: 0.25}}}
	plan, err := f.s.PrepareFollowupPromote(context.Background(), fid, FollowupPromoteOptions{}, decl)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Dest != FollowupPromoteToTask || plan.BudgetImplUSD != 0.5 || plan.BudgetReviewUSD != 0.25 {
		t.Fatalf("plan = %+v", plan)
	}
	res, err := f.s.PromoteFollowup(context.Background(), plan, fuAtt(t, fid), decl)
	if err != nil {
		t.Fatal(err)
	}
	if got := additionFrames(t, f.s, res.Addition.ID); got != [2]int64{500000, 250000} {
		t.Errorf("frames = %v", got)
	}
}

func additionFrames(t *testing.T, s *Store, id string) [2]int64 {
	t.Helper()
	n, _ := parseTaskAdditionID(id)
	var out [2]int64
	writeForTest(t, s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT budget_impl_micros, budget_review_micros FROM task_addition WHERE id = ?`, n).Scan(&out[0], &out[1])
	})
	return out
}

// AC-18・AC-19: 各枠は個別に指定できる（指定しない側は既定のまま）。
func TestPromoteDest_BudgetFlagsOverrideEachFrame(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	decl := &AgentDeclaration{SizeBudgetsUSD: SizeBudgetsUSD{S: SizeBudgetPair{Impl: 0.5, Review: 0.25}}}
	for name, tc := range map[string]struct {
		opts FollowupPromoteOptions
		want [2]int64
	}{
		"impl":   {FollowupPromoteOptions{BudgetImplUSD: destFloat(2)}, [2]int64{2000000, 250000}},
		"review": {FollowupPromoteOptions{BudgetReviewUSD: destFloat(3)}, [2]int64{500000, 3000000}},
	} {
		fid := destFixtureFollowup(t, f)
		plan, err := f.s.PrepareFollowupPromote(context.Background(), fid, tc.opts, decl)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		res, err := f.s.PromoteFollowup(context.Background(), plan, fuAtt(t, fid), decl)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := additionFrames(t, f.s, res.Addition.ID); got != tc.want {
			t.Errorf("%s: frames = %v, want %v", name, got, tc.want)
		}
	}
}

func TestPromoteDest_NegativeFrameIsRejected(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	fid := destFixtureFollowup(t, f)
	if _, err := f.s.PrepareFollowupPromote(context.Background(), fid, FollowupPromoteOptions{NewChallenge: true, BudgetImplUSD: destFloat(1e9), BudgetReviewUSD: destFloat(0)}, nil); err != nil {
		t.Errorf("boundary 1e9 must be accepted: %v", err)
	}
	if _, err := f.s.PrepareFollowupPromote(context.Background(), fid, FollowupPromoteOptions{NewChallenge: true, BudgetReviewUSD: destFloat(1e9 + 1)}, nil); !errors.Is(err, ErrValidation) {
		t.Errorf("review frame over 1e9: err = %v, want ErrValidation", err)
	}
	for _, v := range []float64{-1, math.NaN(), math.Inf(1), 1e300} {
		for _, newChallenge := range []bool{false, true} {
			_, err := f.s.PrepareFollowupPromote(context.Background(), fid, FollowupPromoteOptions{NewChallenge: newChallenge, BudgetImplUSD: destFloat(v)}, nil)
			if !errors.Is(err, ErrValidation) {
				t.Errorf("frame %v (new-challenge=%v): err = %v, want ErrValidation", v, newChallenge, err)
			}
		}
	}
}

// 足す依頼の origin_task_id には、フォローアップの出どころのタスクを写す。
func TestPromoteDest_AdditionCopiesTheFollowupsOriginTask(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	fid := destFixtureFollowup(t, f)
	fn, _ := parseFollowupID(fid)
	var tid int64
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT id FROM task WHERE challenge_id = ? ORDER BY id LIMIT 1`, f.cid()).Scan(&tid); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE followup SET task_id = ? WHERE id = ?`, tid, fn)
		return err
	})
	_, res, err := destPromote(t, f.s, fid, destSmall)
	if err != nil {
		t.Fatal(err)
	}
	if res.Addition.OriginTaskID == nil || *res.Addition.OriginTaskID != formatTaskID(tid) {
		t.Errorf("origin task = %v, want %s", res.Addition.OriginTaskID, formatTaskID(tid))
	}
}

// 足した後の見込みが上限を超えるときは、何も書かずに断る（上限の引き上げは別の操作）。
func TestPromoteDest_OverLimitIsRefusedAndWritesNothing(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	fid := destFixtureFollowup(t, f)
	plan, err := f.s.PrepareFollowupPromote(context.Background(), fid, FollowupPromoteOptions{BudgetImplUSD: destFloat(30), BudgetReviewUSD: destFloat(25)}, nil)
	if err != nil || !plan.OverLimit() || plan.EstimateAfterUSD != 65 || plan.LimitUSD != 14 {
		t.Fatalf("plan = %+v err=%v", plan, err)
	}
	if _, err := f.s.PromoteFollowup(context.Background(), plan, fuAtt(t, fid), nil); !errors.Is(err, ErrValidation) {
		t.Errorf("err = %v, want ErrValidation", err)
	}
	if len(f.additions(t)) != 0 {
		t.Errorf("additions = %+v, want none", f.additions(t))
	}
}

// 見せたときと書き込みのときで、課題の版・足した後の見込みが違えば、何も書かずにコンフリクトで断る。
func TestPromoteDest_ChangedAfterPrepareIsAConflict(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(t *testing.T, f *paFixture){
		"version": func(t *testing.T, f *paFixture) {
			writeForTest(t, f.s, func(tx *sql.Tx) error {
				_, err := tx.Exec(`UPDATE challenge SET version = version + 1, updated_at = ? WHERE id = ?`, formatTimestamp(time.Now()), f.cid())
				return err
			})
		},
		"estimate": func(t *testing.T, f *paFixture) {
			writeForTest(t, f.s, func(tx *sql.Tx) error {
				_, err := tx.Exec(`UPDATE task SET budget_impl_micros = budget_impl_micros + 1000000 WHERE challenge_id = ? AND id = (SELECT MIN(id) FROM task WHERE challenge_id = ?)`, f.cid(), f.cid())
				return err
			})
		},
	} {
		mutate := mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newExpandFixture(t)
			fid := destFixtureFollowup(t, f)
			plan, err := f.s.PrepareFollowupPromote(context.Background(), fid, destSmall, nil)
			if err != nil || plan.Dest != FollowupPromoteToTask {
				t.Fatalf("plan=%+v err=%v", plan, err)
			}
			mutate(t, f)
			if _, err := f.s.PromoteFollowup(context.Background(), plan, fuAtt(t, fid), nil); !errors.Is(err, ErrConflict) {
				t.Errorf("err = %v, want ErrConflict", err)
			}
			if len(f.additions(t)) != 0 {
				t.Errorf("additions = %+v, want none", f.additions(t))
			}
		})
	}
}
