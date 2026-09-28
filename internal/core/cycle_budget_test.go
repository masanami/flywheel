package core

// このファイルは #83（親要件チケット #77・docs/features/m3-invoker-delegation.md
// §予算ガード）の受入基準（AC-124〜131: J1のmax-budget-usd・周の上限超過で
// 起動しない/状態不変/既消費額/unknownの扱い/予約額/二重計上しない/
// budget_exceeded）を core のテストで検証する。CLI（`cycle`・`classify --auto`
// の終了コード確認）は #84・#86 の範囲。

import (
	"context"
	"errors"
	"testing"
)

// --- AC-124: J1 の呼び出しの --max-budget-usd は judgment_budget_usd.J1 の値 ---

func TestAgentDeclaration_JudgmentBudgetFor_ReturnsPerPointConfiguredValue(t *testing.T) {
	dir := writeAgentJSON(t, `{"judgment_budget_usd": {"J1": 1.5, "J2": 6, "J3": 3, "J4": 2, "J5": 5}}`)

	decl, err := LoadAgentDeclaration(dir)
	if err != nil {
		t.Fatalf("LoadAgentDeclaration: %v", err)
	}
	if got := decl.JudgmentBudgetFor(JudgmentJ1); got != 1.5 {
		t.Errorf("JudgmentBudgetFor(J1) = %v, want 1.5", got)
	}
	if got := decl.JudgmentBudgetFor(JudgmentJ2); got != 6 {
		t.Errorf("JudgmentBudgetFor(J2) = %v, want 6", got)
	}
}

// JudgmentBudgetFor は J1〜J5 の外の値に 0 を返す（【仮定】。閉集合の外を
// 渡すことは想定しないが、fail-closed で判別不能な額を返さない）。
func TestAgentDeclaration_JudgmentBudgetFor_ReturnsZeroForUnknownPoint(t *testing.T) {
	decl := defaultAgentDeclaration()
	if got := decl.JudgmentBudgetFor(JudgmentPoint("J9")); got != 0 {
		t.Errorf("JudgmentBudgetFor(J9) = %v, want 0", got)
	}
}

// --- AC-125・126: 周の上限超過で起動しない。状態と版は変わらない ---

func TestRunJudgment_CycleBudgetGuard_ThirdLaunchExceedsBudget(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	cyc, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "test", BudgetUSD: 2, Exclusive: false})
	if err != nil {
		t.Fatalf("BeginCycle: %v", err)
	}

	ids := []string{
		createChallengeForJudgmentTest(t, s),
		createChallengeForJudgmentTest(t, s),
		createChallengeForJudgmentTest(t, s),
	}
	beforeThird, err := s.GetChallenge(ctx, ids[2])
	if err != nil {
		t.Fatalf("GetChallenge: %v", err)
	}

	for i, id := range ids {
		inv := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{
			Result: RunResultSucceeded, StructuredOutput: []byte(`{}`), ReportedTotalCostUSD: float64Ptr(0.8),
		}}
		res, err := s.RunJudgment(ctx, RunJudgmentInput{
			ChallengeID: id, Judgment: JudgmentJ1, MaxBudgetUSD: 1, Stdin: []byte("x"), Invoker: inv, CycleID: &cyc.ID,
		})
		if i < 2 {
			if err != nil {
				t.Fatalf("RunJudgment[%d]: %v", i, err)
			}
			if res.CostUSD != 0.8 {
				t.Errorf("RunJudgment[%d].CostUSD = %v, want 0.8", i, res.CostUSD)
			}
			continue
		}
		// 3件目: 0.8(既消費 run1)+0.8(既消費 run2)+1(評価額＝J1 の上限額) = 2.6 > 2 (周の上限額)
		if !errors.Is(err, ErrBudgetExceeded) {
			t.Fatalf("RunJudgment[%d] error = %v, want ErrBudgetExceeded", i, err)
		}
	}

	afterThird, err := s.GetChallenge(ctx, ids[2])
	if err != nil {
		t.Fatalf("GetChallenge: %v", err)
	}
	if afterThird.Status != beforeThird.Status || afterThird.Version != beforeThird.Version {
		t.Errorf("challenge changed by rejected launch: before=%+v after=%+v", beforeThird, afterThird)
	}
	runs, err := s.ListRuns(ctx, RunListOptions{ChallengeID: &ids[2]})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("ListRuns(3rd challenge) = %d runs, want 0 (rejected launches record nothing)", len(runs))
	}
}

// 境界: 既消費額＋予約額＋評価額が周の上限額とちょうど等しい場合は起動できる
// （§予算ガード「＞ 周の上限額」を評価し…＝等号は起動してよい）。
func TestRunJudgment_CycleBudgetGuard_EqualToBudgetIsAllowed(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	cyc, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "test", BudgetUSD: 1, Exclusive: false})
	if err != nil {
		t.Fatalf("BeginCycle: %v", err)
	}
	id := createChallengeForJudgmentTest(t, s)
	inv := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{
		Result: RunResultSucceeded, StructuredOutput: []byte(`{}`), ReportedTotalCostUSD: float64Ptr(1),
	}}
	res, err := s.RunJudgment(ctx, RunJudgmentInput{
		ChallengeID: id, Judgment: JudgmentJ1, MaxBudgetUSD: 1, Stdin: []byte("x"), Invoker: inv, CycleID: &cyc.ID,
	})
	if err != nil {
		t.Fatalf("RunJudgment: %v, want success (evaluation == budget is allowed)", err)
	}
	if res.CostUSD != 1 {
		t.Errorf("CostUSD = %v, want 1", res.CostUSD)
	}
}

// --- AC-127: 周の既消費額は、終了した run の費用の合計（cycle --json の
// spent_usd で検証する＝ここでは EndCycle の戻り値の SpentUSD） ---

func TestEndCycle_SpentUSDIsSumOfEndedRunCosts(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	cyc, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "test", BudgetUSD: 10, Exclusive: false})
	if err != nil {
		t.Fatalf("BeginCycle: %v", err)
	}

	id1 := createChallengeForJudgmentTest(t, s)
	id2 := createChallengeForJudgmentTest(t, s)
	for _, tc := range []struct {
		id   string
		cost float64
	}{{id1, 1.25}, {id2, 2.5}} {
		inv := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{
			Result: RunResultSucceeded, StructuredOutput: []byte(`{}`), ReportedTotalCostUSD: float64Ptr(tc.cost),
		}}
		if _, err := s.RunJudgment(ctx, RunJudgmentInput{
			ChallengeID: tc.id, Judgment: JudgmentJ1, MaxBudgetUSD: 5, Stdin: []byte("x"), Invoker: inv, CycleID: &cyc.ID,
		}); err != nil {
			t.Fatalf("RunJudgment(%s): %v", tc.id, err)
		}
	}

	ended, err := s.EndCycle(ctx, cyc.ID, CycleResultCompleted)
	if err != nil {
		t.Fatalf("EndCycle: %v", err)
	}
	if ended.SpentUSD != 3.75 {
		t.Errorf("EndCycle SpentUSD = %v, want 3.75 (1.25+2.5)", ended.SpentUSD)
	}
}

// --- AC-128: 費用の出所が unknown の run は、渡した上限額が周の既消費額に
// 足される ---

func TestEndCycle_UnknownCostSourceRunCountsFullMaxBudgetAsSpent(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	cyc, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "test", BudgetUSD: 10, Exclusive: false})
	if err != nil {
		t.Fatalf("BeginCycle: %v", err)
	}
	id := createChallengeForJudgmentTest(t, s)

	// ReportedTotalCostUSD が無い成功の結果は、渡した上限額・出所 unknown になる
	// （computeRunCost。§費用の記録）。
	inv := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{}`)}}
	res, err := s.RunJudgment(ctx, RunJudgmentInput{
		ChallengeID: id, Judgment: JudgmentJ1, MaxBudgetUSD: 4, Stdin: []byte("x"), Invoker: inv, CycleID: &cyc.ID,
	})
	if err != nil {
		t.Fatalf("RunJudgment: %v", err)
	}
	if res.CostSource != CostSourceUnknown || res.CostUSD != 4 {
		t.Fatalf("RunJudgment result = %+v, want CostSource=unknown CostUSD=4", res)
	}

	ended, err := s.EndCycle(ctx, cyc.ID, CycleResultCompleted)
	if err != nil {
		t.Fatalf("EndCycle: %v", err)
	}
	if ended.SpentUSD != 4 {
		t.Errorf("EndCycle SpentUSD = %v, want 4 (unknown cost source counts the max budget)", ended.SpentUSD)
	}
}

// --- AC-129: 終了していない run の上限額は予約額として評価に含まれる ---

func TestRunJudgment_CycleBudgetGuard_OpenRunReservesItsMaxBudget(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	cyc, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "test", BudgetUSD: 1.5, Exclusive: false})
	if err != nil {
		t.Fatalf("BeginCycle: %v", err)
	}
	id1 := createChallengeForJudgmentTest(t, s)
	id2 := createChallengeForJudgmentTest(t, s)

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
			ChallengeID: id1, Judgment: JudgmentJ1, MaxBudgetUSD: 1.0, Stdin: []byte("x"), Invoker: inv1, CycleID: &cyc.ID,
		})
	}()
	<-invoked // run1 は記録済みだが終了していない: 予約額 = 1.0

	inv2 := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{}`)}}
	_, err = s.RunJudgment(ctx, RunJudgmentInput{
		ChallengeID: id2, Judgment: JudgmentJ1, MaxBudgetUSD: 0.6, Stdin: []byte("x"), Invoker: inv2, CycleID: &cyc.ID,
	})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("RunJudgment(id2) error = %v, want ErrBudgetExceeded (reserved 1.0 + eval 0.6 > budget 1.5)", err)
	}

	close(block)
	<-done

	// --- AC-130: 終了した run は予約額から外れ、費用だけが既消費額に数えられる
	// （同じ run を二重に数えない）: run1 が終了した(spent=0.5・reserved=0)後は
	// 0.5+0+0.9=1.4<=1.5 で起動できる。
	inv3 := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{}`), ReportedTotalCostUSD: float64Ptr(0.9)}}
	res3, err := s.RunJudgment(ctx, RunJudgmentInput{
		ChallengeID: id2, Judgment: JudgmentJ1, MaxBudgetUSD: 0.9, Stdin: []byte("x"), Invoker: inv3, CycleID: &cyc.ID,
	})
	if err != nil {
		t.Fatalf("RunJudgment(id2, after run1 ended): %v, want success (run1's reservation released on end)", err)
	}
	if res3.CostUSD != 0.9 {
		t.Errorf("res3.CostUSD = %v, want 0.9", res3.CostUSD)
	}

	ended, err := s.EndCycle(ctx, cyc.ID, CycleResultCompleted)
	if err != nil {
		t.Fatalf("EndCycle: %v", err)
	}
	if ended.SpentUSD != 1.4 {
		t.Errorf("EndCycle SpentUSD = %v, want 1.4 (0.5+0.9, run1 not double-counted)", ended.SpentUSD)
	}
}

// --- AC-131: ID を指定した個別の操作が周の上限で起動できないときは
// core.ErrBudgetExceeded で終わる（CLI の budget_exceeded・終了コード1への
// 写像は #86 の範囲。ここでは core API の結果で検証する） ---

func TestRunJudgment_CycleBudgetGuard_ReturnsErrBudgetExceededDirectly(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	cyc, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "test", BudgetUSD: 1, Exclusive: false})
	if err != nil {
		t.Fatalf("BeginCycle: %v", err)
	}
	id := createChallengeForJudgmentTest(t, s)
	inv := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{}`)}}
	_, err = s.RunJudgment(ctx, RunJudgmentInput{
		ChallengeID: id, Judgment: JudgmentJ1, MaxBudgetUSD: 1.01, Stdin: []byte("x"), Invoker: inv, CycleID: &cyc.ID,
	})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("RunJudgment error = %v, want ErrBudgetExceeded", err)
	}
}

// --- RunJudgmentInput.CycleID が nil の呼び出しは、#83 より前と同じく
// 予算の評価を一切行わない（後方互換性） ---

func TestRunJudgment_NoCycleID_SkipsBudgetGuard(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	id := createChallengeForJudgmentTest(t, s)
	inv := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{}`), ReportedTotalCostUSD: float64Ptr(1_000_000)}}
	res, err := s.RunJudgment(ctx, RunJudgmentInput{
		ChallengeID: id, Judgment: JudgmentJ1, MaxBudgetUSD: 1, Stdin: []byte("x"), Invoker: inv,
	})
	if err != nil {
		t.Fatalf("RunJudgment: %v, want success (no CycleID means no budget guard)", err)
	}
	if res.CostUSD != 1_000_000 {
		t.Errorf("CostUSD = %v, want 1000000", res.CostUSD)
	}
	runs, err := s.ListRuns(ctx, RunListOptions{ChallengeID: &id})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].CycleID != nil {
		t.Errorf("ListRuns = %+v, want a single run with CycleID == nil", runs)
	}
}

// --- 周が存在しない・既に終了していれば ErrValidation ---

func TestRunJudgment_CycleBudgetGuard_UnknownOrEndedCycleIsValidationError(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	id := createChallengeForJudgmentTest(t, s)
	inv := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{}`)}}

	missing := "Y-9999"
	if _, err := s.RunJudgment(ctx, RunJudgmentInput{
		ChallengeID: id, Judgment: JudgmentJ1, MaxBudgetUSD: 1, Stdin: []byte("x"), Invoker: inv, CycleID: &missing,
	}); !errors.Is(err, ErrValidation) {
		t.Errorf("RunJudgment(missing cycle) error = %v, want ErrValidation", err)
	}

	cyc, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "test", BudgetUSD: 10, Exclusive: false})
	if err != nil {
		t.Fatalf("BeginCycle: %v", err)
	}
	if _, err := s.EndCycle(ctx, cyc.ID, CycleResultCompleted); err != nil {
		t.Fatalf("EndCycle: %v", err)
	}
	if _, err := s.RunJudgment(ctx, RunJudgmentInput{
		ChallengeID: id, Judgment: JudgmentJ1, MaxBudgetUSD: 1, Stdin: []byte("x"), Invoker: inv, CycleID: &cyc.ID,
	}); !errors.Is(err, ErrValidation) {
		t.Errorf("RunJudgment(ended cycle) error = %v, want ErrValidation", err)
	}
}

// --- JudgmentCycle（--auto の一括処理）は ErrBudgetExceeded を
// NotStarted{Reason: cycle_budget} へ翻訳し、周を止めない（他の課題の評価は
// 独立に行う） ---

func TestJudgmentCycle_TranslatesBudgetExceededToNotStartedAndContinues(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	cyc, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "classify --auto", BudgetUSD: 1, Exclusive: false})
	if err != nil {
		t.Fatalf("BeginCycle: %v", err)
	}
	c := NewJudgmentCycle(cyc.ID)

	tooExpensive := createChallengeForJudgmentTest(t, s)
	inv1 := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{}`)}}
	res, ns, err := c.RunJudgment(ctx, s, RunJudgmentInput{
		ChallengeID: tooExpensive, Judgment: JudgmentJ1, MaxBudgetUSD: 1.5, Stdin: []byte("x"), Invoker: inv1,
	})
	if err != nil {
		t.Fatalf("RunJudgment(too expensive): unexpected error %v", err)
	}
	if res != nil || ns == nil || ns.Reason != NotStartedCycleBudget || ns.ChallengeID != tooExpensive {
		t.Fatalf("RunJudgment(too expensive) = res=%+v ns=%+v, want NotStarted{Reason: cycle_budget}", res, ns)
	}

	// 周は続けられる: 上限内の別の課題は起動できる。
	fitsBudget := createChallengeForJudgmentTest(t, s)
	inv2 := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{}`), ReportedTotalCostUSD: float64Ptr(0.5)}}
	res2, ns2, err := c.RunJudgment(ctx, s, RunJudgmentInput{
		ChallengeID: fitsBudget, Judgment: JudgmentJ1, MaxBudgetUSD: 0.5, Stdin: []byte("x"), Invoker: inv2,
	})
	if err != nil || res2 == nil || ns2 != nil {
		t.Fatalf("RunJudgment(fits budget) = res=%+v ns=%+v err=%v, want a successful run", res2, ns2, err)
	}

	// run が cyc.ID に紐づいて記録されていることも確かめる。
	runs, err := s.ListRuns(ctx, RunListOptions{ChallengeID: &fitsBudget})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].CycleID == nil || *runs[0].CycleID != cyc.ID {
		t.Fatalf("ListRuns = %+v, want a single run with CycleID == %s", runs, cyc.ID)
	}
}

// JudgmentCycle のゼロ値（周の ID 未設定）は、#83 より前と同じく run.cycle_id
// を NULL のまま記録し、予算の評価を行わない（後方互換性）。
func TestJudgmentCycle_ZeroValue_RecordsNilCycleID(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	id := createChallengeForJudgmentTest(t, s)
	inv := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{}`)}}
	var c JudgmentCycle
	res, ns, err := c.RunJudgment(ctx, s, RunJudgmentInput{
		ChallengeID: id, Judgment: JudgmentJ1, MaxBudgetUSD: 1, Stdin: []byte("x"), Invoker: inv,
	})
	if err != nil || res == nil || ns != nil {
		t.Fatalf("RunJudgment = res=%+v ns=%+v err=%v, want a successful run", res, ns, err)
	}
	runs, err := s.ListRuns(ctx, RunListOptions{ChallengeID: &id})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].CycleID != nil {
		t.Errorf("ListRuns = %+v, want a single run with CycleID == nil", runs)
	}
}
