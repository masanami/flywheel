package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// 予算の 2 枠・評価額と予約額・`flywheel budget`（M3 S2 #105。AC-218・263・264・336〜346・352・364）。

func budgetSpec(impl, review any) string {
	return planSpec(func(m map[string]any) {
		m["budget_impl_usd"] = impl
		m["budget_review_usd"] = review
	})
}

func (f *delegateFixture) budgetAttestation(t *testing.T, id string) Attestation {
	t.Helper()
	att, err := Verify(ChannelCLI, &fakeVerifier{method: VerificationTTYConfirm, actor: "tester"}, "s", id)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	return att
}

func (f *delegateFixture) overview(t *testing.T) *Overview {
	t.Helper()
	ov, err := f.s.GetOverviewFor(context.Background(), f.agent)
	if err != nil {
		t.Fatalf("GetOverviewFor: %v", err)
	}
	return ov
}

func TestComputeBucketRemaining_NeverShiftsMoneyBetweenBuckets(t *testing.T) {
	for _, tc := range []struct {
		name         string
		b            planBudgets
		sp           bucketSpend
		wantImpl     int64
		wantReview   int64
		wantAboveMin bool
	}{
		{"amount minus spend", planBudgets{50_000_000, 30_000_000}, bucketSpend{Impl: 2_500_000}, 47_500_000, 30_000_000, true},
		{"impl overspent is zero, review untouched", planBudgets{50_000_000, 30_000_000}, bucketSpend{Impl: 60_000_000}, 0, 30_000_000, false},
		{"review overspent is zero, impl untouched", planBudgets{50_000_000, 30_000_000}, bucketSpend{Review: 45_000_000}, 50_000_000, 0, true},
		{"stopped run is treated as zero or less", planBudgets{50_000_000, 30_000_000}, bucketSpend{Impl: 10_000_000, ImplStopped: true}, 0, 30_000_000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := computeBucketRemaining(tc.b, tc.sp)
			if got.Impl != tc.wantImpl || got.Review != tc.wantReview {
				t.Errorf("remaining = %+v, want impl %d review %d", got, tc.wantImpl, tc.wantReview)
			}
			if (got.Impl >= minImplLaunchMicros) != tc.wantAboveMin {
				t.Errorf("launchable = %v, want %v", got.Impl >= minImplLaunchMicros, tc.wantAboveMin)
			}
		})
	}
}

// AC-218・336: --max-budget-usd と予約額（run の max_budget_usd）は実装枠の残りだけ。
func TestBudget_MaxBudgetAndReservedAreTheImplRemainingOnly(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", budgetSpec(50, 30))
	f.mustRun(t, nil)
	runs := f.delegateRuns(t)
	if got := f.deleg.launched()[0].MaxBudgetUSD; got != 50 || runs[0].MaxBudgetUSD != 50_000_000 {
		t.Fatalf("first launch cap = %v (run %d), want 50 (impl only, not 80)", got, runs[0].MaxBudgetUSD)
	}
	// 1 回目の費用 2.5 USD は実装枠だけから引かれる。
	f.refreshInProgress(t, id)
	f.mustRun(t, &id)
	if got := f.deleg.launched()[1].MaxBudgetUSD; got != 47.5 {
		t.Errorf("second launch cap = %v, want 47.5", got)
	}
}

// refreshInProgress は succeeded の run の後に、課題を着手中へ戻す（検証中へ進んだ課題を再び委譲の対象にする）。
func (f *delegateFixture) refreshInProgress(t *testing.T, id string) {
	t.Helper()
	cid, _ := parseChallengeID(id)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET status = 'in_progress', version = version + 1 WHERE id = ?`, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// AC-337・338: 評価額は実装枠の残り＋レビュー対応枠の残り。周の上限に（J3 の上限を含めて）ちょうど
// 入れば起動し、1 µUSD でも超えれば cycle_budget で起動しない。
func TestBudget_EstimateIsImplPlusReviewRemainingAgainstTheCycleCap(t *testing.T) {
	f := newDelegateFixture(t)
	f.newInProgress(t, "t", "P1", budgetSpec(50, 30))
	// J3 の上限 3 ＋ 評価額 80（実装 50 ＋ レビュー対応 30）＝ 83。
	res, err := f.s.RunDelegation(context.Background(), f.input(f.cycle(t, 82.99), nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedCycleBudget || len(f.deleg.launched()) != 0 {
		t.Fatalf("cap 82.99: notStarted=%+v launches=%d, want cycle_budget and no launch", res.NotStarted, len(f.deleg.launched()))
	}
	res, err = f.s.RunDelegation(context.Background(), f.input(f.cycle(t, 83), nil))
	if err != nil || len(res.NotStarted) != 0 || len(f.deleg.launched()) != 1 {
		t.Fatalf("cap 83: res=%+v err=%v launches=%d, want one launch", res, err, len(f.deleg.launched()))
	}
}

// AC-352: 終了していない委譲の上限額の和が予約額に入り、評価式は並列の数で変わらない。
func TestBudget_ReservedIsTheSumOfOpenRunsCaps(t *testing.T) {
	f := newDelegateFixture(t)
	f.agent.MaxParallelRuns = 10 // 予算の評価式を検査する（同時の起動の上限は delegate_plan_test.go が検査する）
	f.newInProgress(t, "a", "P1", budgetSpec(50, 30))
	cyc := f.cycle(t, 183) // J3 3 ＋ 評価額 80 ＋ 予約額 100（終了していない 2 つの委譲の上限額）
	cyid, _ := parseCycleID(cyc)
	f.insertOpenDelegateRuns(t, cyid, 2, 50_000_000)
	res, err := f.s.RunDelegation(context.Background(), f.input(cyc, nil))
	if err != nil || len(res.NotStarted) != 0 || len(f.deleg.launched()) != 1 {
		t.Fatalf("res=%+v err=%v launches=%d, want a launch at exactly the cap", res, err, len(f.deleg.launched()))
	}
	// 終了していない run が 1 つ多ければ 50 USD 超える。
	f2 := newDelegateFixture(t)
	f2.agent.MaxParallelRuns = 10
	f2.newInProgress(t, "a", "P1", budgetSpec(50, 30))
	cyc2 := f2.cycle(t, 183)
	cyid2, _ := parseCycleID(cyc2)
	f2.insertOpenDelegateRuns(t, cyid2, 3, 50_000_000)
	res, err = f2.s.RunDelegation(context.Background(), f2.input(cyc2, nil))
	if err != nil || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedCycleBudget {
		t.Fatalf("three open runs: res=%+v err=%v, want cycle_budget", res, err)
	}
}

func (f *delegateFixture) insertOpenDelegateRuns(t *testing.T, cycleID int64, n int, capMicros int64) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		ch, err := f.s.CreateChallenge(ctx, ChannelCLI, CreateInput{Title: "other", Description: "d", DoneCriteria: "d"})
		if err != nil {
			t.Fatal(err)
		}
		cid, _ := parseChallengeID(ch.ID)
		now := f.s.currentTime()
		if err := f.s.db.Write(ctx, func(tx *sql.Tx) error {
			_, err := insertRun(ctx, tx, cid, insertRunInput{CycleID: &cycleID, Kind: runKindDelegate, ChallengeVersion: 1,
				SessionID: "44444444-4444-4444-8444-44444444444" + string(rune('0'+i)), PID: 1, Host: "h", HeartbeatAt: now, StartedAt: now,
				MaxBudgetUSD: capMicros, BudgetBucket: budgetBucketImpl})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// AC-339〜341: 実装枠の残りが 0.99 USD なら起動せず run_budget・needs_human.budget_exhausted に出る。
// 1.00 USD なら起動する。ID を指定した run は budget_exceeded。
func TestBudget_BelowOneUSDStopsAndExactlyOneLaunches(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", budgetSpec(0.99, 30))
	res, err := f.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.deleg.launched()) != 0 || len(f.j3Inputs) != 0 {
		t.Errorf("launches = %d, J3 calls = %d, want none (J3 must not be paid either)", len(f.deleg.launched()), len(f.j3Inputs))
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].ChallengeID != id || res.NotStarted[0].Reason != NotStartedRunBudget {
		t.Fatalf("notStarted = %+v, want run_budget for %s", res.NotStarted, id)
	}
	if _, err := f.run(t, &id); !errors.Is(err, ErrBudgetExceeded) {
		t.Errorf("run <C-ID> err = %v, want ErrBudgetExceeded", err)
	}
	ex := f.overview(t).NeedsHumanBudgetExhausted
	if len(ex) != 1 || ex[0].ChallengeID != id || ex[0].PlanVersion != 1 || ex[0].ImplRemainingUSD != 0.99 || ex[0].ReviewRemainingUSD != 30 {
		t.Errorf("budget_exhausted = %+v", ex)
	}

	g := newDelegateFixture(t)
	g.newInProgress(t, "t", "P1", budgetSpec(1.0, 30))
	if _, err := g.run(t, nil); err != nil {
		t.Fatal(err)
	}
	if len(g.deleg.launched()) != 1 || g.deleg.launched()[0].MaxBudgetUSD != 1 {
		t.Errorf("launches = %+v, want one with cap 1", g.deleg.launched())
	}
	if ex := g.overview(t).NeedsHumanBudgetExhausted; len(ex) != 0 {
		t.Errorf("budget_exhausted = %+v, want []", ex)
	}
}

// AC-263・264・342〜346: budget_exhausted の run は課題の状態と版を変えず、枠を使い切ったとして
// 止まり、flywheel budget で置き換えると同じセッションへ固定の文面で --resume する。
func TestBudget_ExhaustedRunStopsUntilRaisedThenResumesTheSameSession(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", budgetSpec(50, 30))
	v := f.version(t, id)
	// 費用 10 USD（上限 50 未満）でも、budget_exhausted は残りを 0 以下として扱う。
	f.deleg.result = JudgmentLaunchOutput{Result: RunResultBudgetExhausted, ReportedTotalCostUSD: float64Ptr(10), ErrorSummary: "cap"}
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	first := f.lastLaunch(t)
	if d := f.detail(t, id); d.Status != StatusInProgress || d.Version != v {
		t.Fatalf("status/version = %s v%d, want in_progress v%d", d.Status, d.Version, v)
	}
	res, err := f.run(t, nil)
	if err != nil || len(f.deleg.launched()) != 1 || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedRunBudget {
		t.Fatalf("next cycle: res=%+v err=%v launches=%d, want run_budget and no launch", res, err, len(f.deleg.launched()))
	}
	ex := f.overview(t).NeedsHumanBudgetExhausted
	if len(ex) != 1 || ex[0].ImplRemainingUSD != 0 || ex[0].ReviewRemainingUSD != 30 {
		t.Fatalf("budget_exhausted = %+v, want impl 0 and review 30", ex)
	}

	// 本人確認つきの増額（実装枠だけ。レビュー対応枠は変わらない）。
	preview, err := f.s.PrepareBudget(context.Background(), id, f.agent, 80, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.CurrentKnown || preview.CurrentImplUSD != 50 || preview.ImplRemainingUSD != 0 || !preview.ReviewUnchanged || preview.NewReviewUSD != 30 {
		t.Fatalf("preview = %+v", preview)
	}
	approval, err := f.s.ExecuteBudget(context.Background(), BudgetRequest{ChallengeID: id, ExpectedVersion: preview.Version, PlanVersion: preview.PlanVersion, ImplUSD: 80}, f.budgetAttestation(t, id))
	if err != nil {
		t.Fatal(err)
	}
	if approval.Kind != ApprovalKindBudget || approval.Decision != ApprovalDecisionApproved || approval.TargetVersion != 1 {
		t.Errorf("approval = %+v", approval)
	}
	if ex := f.overview(t).NeedsHumanBudgetExhausted; len(ex) != 0 {
		t.Errorf("after raise budget_exhausted = %+v, want []", ex)
	}
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	got := f.lastLaunch(t)
	if len(f.deleg.launched()) != 2 || !got.IsResume || got.SessionID != first.SessionID || got.ResumeKind != ResumeKindBudget {
		t.Fatalf("launch = %+v, want a budget resume of session %s", got, first.SessionID)
	}
	// 実装枠 80 − 費用 10 ＝ 70 USD（レビュー対応枠 30 は融通されない）。
	if got.MaxBudgetUSD != 70 {
		t.Errorf("resume cap = %v, want 70", got.MaxBudgetUSD)
	}
	if f.j3Calls() != 1 {
		t.Errorf("J3 calls = %d, want 1 (a resume must not call J3)", f.j3Calls())
	}

	// 増額の後の再開がまた上限に達したら、その後の増額が無い限り再び止まる。
	// （偽の委譲は常に budget_exhausted を返すので、上の再開の run もそう終わっている。）
	res, err = f.run(t, nil)
	if err != nil || len(f.deleg.launched()) != 2 || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedRunBudget {
		t.Fatalf("after a second exhaustion: res=%+v err=%v launches=%d, want run_budget and no launch", res, err, len(f.deleg.launched()))
	}
}

// 失敗の run（費用が取れない結果は渡した上限額が費用）は実装枠の残りを使い切るので、人間が
// flywheel budget で増やすまで次の委譲は run_budget で止まる（再開・連続失敗の上限の検査は
// refillImplBudget で枠を置き直して行う。この相互作用は PR の説明の「仕様への指摘」）。
func TestBudget_AFailedRunWithUnknownCostExhaustsTheImplBucket(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", budgetSpec(50, 30))
	f.deleg.result = failOut(RunResultTimedOut)
	f.mustRun(t, &id)
	if _, err := f.run(t, &id); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("second run err = %v, want ErrBudgetExceeded", err)
	}
	if ex := f.overview(t).NeedsHumanBudgetExhausted; len(ex) != 1 {
		t.Errorf("budget_exhausted = %+v, want the challenge", ex)
	}
}

func TestExecuteBudget_RejectsAnAmountThatWouldOverflow(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", budgetSpec(50, 30))
	if _, err := f.s.PrepareBudget(context.Background(), id, f.agent, 1e20, nil); !errors.Is(err, ErrValidation) {
		t.Errorf("err = %v, want ErrValidation", err)
	}
}

func (f *delegateFixture) j3Calls() int { return len(f.j3Inputs) }

// AC-342・343: 承認の種類 budget の承認の記録と、approval_kind が budget の approve の作業ログ。
// 課題の状態と版は変わらず、レビュー対応枠は --review-usd のときだけ置き換わる。
func TestExecuteBudget_ReviewUnchangedWithoutReviewUSDAndReplacedWithIt(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", budgetSpec(50, 30))
	v := f.version(t, id)
	exec := func(impl float64, review *float64) {
		t.Helper()
		p, err := f.s.PrepareBudget(context.Background(), id, f.agent, impl, review)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.ExecuteBudget(context.Background(), BudgetRequest{ChallengeID: id, ExpectedVersion: p.Version, PlanVersion: p.PlanVersion, ImplUSD: impl, ReviewUSD: review}, f.budgetAttestation(t, id)); err != nil {
			t.Fatal(err)
		}
	}
	bucketsOf := func() planBudgets {
		var b planBudgets
		if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
			o, err := loadPlanBudgetOverride(context.Background(), tx, 1, 1)
			if err != nil {
				return err
			}
			b = applyBudgetOverride(50, 30, o)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return b
	}
	exec(80, nil)
	if b := bucketsOf(); b.Impl != 80_000_000 || b.Review != 30_000_000 {
		t.Errorf("after --impl-usd 80: %+v, want impl 80 review 30 (unchanged)", b)
	}
	r := 20.0
	exec(90, &r)
	if b := bucketsOf(); b.Impl != 90_000_000 || b.Review != 20_000_000 {
		t.Errorf("after --review-usd 20: %+v, want impl 90 review 20", b)
	}
	exec(95, nil) // 先の --review-usd の置き換えは残る
	if b := bucketsOf(); b.Impl != 95_000_000 || b.Review != 20_000_000 {
		t.Errorf("after a second raise without --review-usd: %+v, want review 20 kept", b)
	}

	if d := f.detail(t, id); d.Status != StatusInProgress || d.Version != v {
		t.Errorf("status/version = %s v%d, want in_progress v%d", d.Status, d.Version, v)
	}
	var budgetApprovals int
	for _, a := range f.detail(t, id).Approvals {
		if a.Kind == ApprovalKindBudget {
			budgetApprovals++
			if a.Decision != ApprovalDecisionApproved || a.TargetVersion != 1 || a.Actor != "tester" || a.Channel != "cli" || a.Verification != "tty_confirm" {
				t.Errorf("approval = %+v", a)
			}
		}
	}
	if budgetApprovals != 3 {
		t.Errorf("budget approvals = %d, want 3", budgetApprovals)
	}
	logs, err := f.s.ListActivities(context.Background(), &id)
	if err != nil {
		t.Fatal(err)
	}
	var approves int
	for _, a := range logs {
		if a.Entity == "challenge" && a.Action == "approve" && strings.Contains(string(a.After), `"approval_kind":"budget"`) {
			approves++
		}
	}
	if approves != 3 {
		t.Errorf("budget approve activities = %d, want 3 (%d total)", approves, len(logs))
	}
}

func TestExecuteBudget_RejectsInvalidAmountsWrongAttestationAndStaleVersion(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", budgetSpec(50, 30))
	for _, impl := range []float64{0, -1} {
		if _, err := f.s.PrepareBudget(context.Background(), id, f.agent, impl, nil); !errors.Is(err, ErrValidation) {
			t.Errorf("PrepareBudget(%v) err = %v, want ErrValidation", impl, err)
		}
	}
	zero := 0.0
	if _, err := f.s.PrepareBudget(context.Background(), id, f.agent, 10, &zero); !errors.Is(err, ErrValidation) {
		t.Errorf("review 0: err = %v, want ErrValidation", err)
	}
	p, err := f.s.PrepareBudget(context.Background(), id, f.agent, 80, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := BudgetRequest{ChallengeID: id, ExpectedVersion: p.Version, PlanVersion: p.PlanVersion, ImplUSD: 80}
	if _, err := f.s.ExecuteBudget(context.Background(), req, Attestation{}); !errors.Is(err, ErrVerificationRejected) {
		t.Errorf("zero attestation: err = %v, want ErrVerificationRejected", err)
	}
	other := f.newInProgress(t, "o", "P1", budgetSpec(50, 30))
	if _, err := f.s.ExecuteBudget(context.Background(), req, f.budgetAttestation(t, other)); !errors.Is(err, ErrVerificationRejected) {
		t.Errorf("attestation for another challenge: err = %v, want ErrVerificationRejected", err)
	}
	stale := req
	stale.ExpectedVersion--
	if _, err := f.s.ExecuteBudget(context.Background(), stale, f.budgetAttestation(t, id)); !errors.Is(err, ErrConflict) {
		t.Errorf("stale version: err = %v, want ErrConflict", err)
	}
	if _, err := f.s.PrepareBudget(context.Background(), "C-999", f.agent, 80, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown challenge: err = %v, want ErrNotFound", err)
	}
	unplanned, err := f.s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "u", Description: "d", DoneCriteria: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.PrepareBudget(context.Background(), unplanned.ID, f.agent, 80, nil); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("no approved plan: err = %v, want ErrInvalidTransition", err)
	}
	// どれも枠を変えていない。
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		o, err := loadPlanBudgetOverride(context.Background(), tx, 1, 1)
		if err != nil {
			return err
		}
		if o.ImplBudgetUSD != nil || o.ReviewBudgetUSD != nil {
			t.Errorf("override = %+v, want none", o)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// 経路 invoker の本人確認は登録簿が拒否する（枠は変わらない）。
func TestExecuteBudget_InvokerChannelCannotVerify(t *testing.T) {
	if _, err := Verify(ChannelInvoker, &fakeVerifier{method: VerificationTTYConfirm, actor: "x"}, "s", "C-1"); !errors.Is(err, ErrVerificationRejected) {
		t.Errorf("Verify(invoker) err = %v, want ErrVerificationRejected", err)
	}
}

// 枠はほかの計画の版・ほかの課題と混ざらない。
func TestBudget_RemainingIsPerPlanVersion(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", budgetSpec(5, 30))
	f.mustRun(t, nil) // 費用 2.5（版 1）
	f.refreshInProgress(t, id)
	cid, _ := parseChallengeID(id)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO task_plan (challenge_id, version, body, created_at, spec) SELECT challenge_id, 2, body, created_at, spec FROM task_plan WHERE challenge_id = ? AND version = 1`, cid); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO approval (challenge_id, kind, decision, target_version, actor, channel, verification, decided_at)
			VALUES (?, 'plan', 'approved', 2, 'alice', 'cli', 'tty_confirm', '2026-09-26T00:00:00.000Z')`, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.mustRun(t, &id)
	if got := f.lastLaunch(t).MaxBudgetUSD; got != 5 {
		t.Errorf("new plan version cap = %v, want the full 5 (version 1's spend does not carry over)", got)
	}
}

func (f *delegateFixture) mustRun(t *testing.T, id *string) {
	t.Helper()
	if _, err := f.run(t, id); err != nil {
		t.Fatalf("RunDelegation: %v", err)
	}
}
