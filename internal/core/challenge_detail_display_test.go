package core

import (
	"database/sql"
	"testing"
)

// AC-228・AC-229・AC-230: 課題の詳細のタスクは、タスクの枠（増額を含む）と、そのタスクの実行の費用の合計を持つ。
func TestChallengeDetail_TasksCarryTheirFramesAndSpent(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(30, 10, 40, 12))
	cid, _ := parseChallengeID(id)
	tasks := taskRows(t, f.s, cid)
	a := mustTaskID(t, tasks[0].ID)
	costRun(t, f.s, cid, a, "delegate", "", "impl", 2, 0)
	costRun(t, f.s, cid, a, "delegate", "", "review", 1.5, 0)
	costRun(t, f.s, cid, a, "judgment", "J5", "judgment", 7, 0) // 判断点の費用は数えない
	costRun(t, f.s, cid, a, "contract", "", "impl", 11, 1)      // 反映の依頼の run は数えない
	if _, err := f.raise(t, id, tasks[1].ID, 55, nil); err != nil {
		t.Fatalf("raise: %v", err)
	}
	d, err := f.s.GetChallenge(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Tasks) != 2 {
		t.Fatalf("tasks = %d, want 2", len(d.Tasks))
	}
	type want struct{ impl, review, spent float64 }
	for i, w := range []want{{30, 10, 3.5}, {55, 12, 0}} { // 増額は 2 個目のタスクの枠だけを置き換える
		got := d.Tasks[i]
		if got.BudgetImplUSD != w.impl || got.BudgetReviewUSD != w.review || got.SpentUSD != w.spent {
			t.Errorf("task %s = impl %v / review %v / spent %v, want %+v", got.ID, got.BudgetImplUSD, got.BudgetReviewUSD, got.SpentUSD, w)
		}
	}
}

// AC-232: 課題の詳細の IntegrationRef は、記録があればその値、無ければ nil。
func TestChallengeDetail_IntegrationRefIsTheRecordOrNil(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "one", "P2", twoTaskSpec(3, 2, 4, 5))
	d, err := f.s.GetChallenge(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if d.IntegrationRef != nil {
		t.Errorf("IntegrationRef = %q, want nil", *d.IntegrationRef)
	}
	cid, _ := parseChallengeID(id)
	if err := f.s.db.Write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET integration_ref = ? WHERE id = ?`, "flywheel/integration/c-1", cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	d, err = f.s.GetChallenge(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if d.IntegrationRef == nil || *d.IntegrationRef != "flywheel/integration/c-1" {
		t.Errorf("IntegrationRef = %v, want flywheel/integration/c-1", d.IntegrationRef)
	}
}

// 枠の列が空の過去のタスクは、見直し前の決め方の値（計画が両方の額を書いていればその額）を枠として出す。
// 片方の枠だけを記録したタスクは、記録した枠を 0 にしない。
func TestChallengeDetail_EmptyFrameColumnsFallBackToThePlanAndKeepRecordedHalf(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 4, 5))
	cid, _ := parseChallengeID(id)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task SET budget_impl_micros = NULL, budget_review_micros = NULL WHERE challenge_id = ?`, cid)
		return err
	})
	d, err := f.s.GetChallenge(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range d.Tasks { // 計画が両方の額を書いている（99・88）ので、その額が枠になる
		if tk.BudgetImplUSD != 99 || tk.BudgetReviewUSD != 88 {
			t.Errorf("task %s frame = %v / %v, want the plan's 99 / 88", tk.ID, tk.BudgetImplUSD, tk.BudgetReviewUSD)
		}
	}
	// 計画が額を書かず、サイズだけのときは、宣言のサイズごとの既定（宣言の無いワークスペースでは既定の宣言）から引く。
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task_plan SET spec = json_remove(spec, '$.budget_impl_usd', '$.budget_review_usd') WHERE challenge_id = ?`, cid)
		return err
	})
	d, err = f.s.GetChallenge(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	wantM := defaultAgentDeclaration().SizeBudgetsUSD.M
	for _, tk := range d.Tasks {
		if wantM.Impl == 0 || tk.BudgetImplUSD != wantM.Impl || tk.BudgetReviewUSD != wantM.Review {
			t.Errorf("task %s frame = %v / %v, want the size M default %+v", tk.ID, tk.BudgetImplUSD, tk.BudgetReviewUSD, wantM)
		}
	}
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task SET budget_impl_micros = 9000000 WHERE challenge_id = ? AND id = (SELECT MIN(id) FROM task WHERE challenge_id = ?)`, cid, cid)
		return err
	})
	d, err = f.s.GetChallenge(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Tasks[0].BudgetImplUSD != 9 {
		t.Errorf("recorded impl frame = %v, want 9 (not dropped when the review frame is unresolved)", d.Tasks[0].BudgetImplUSD)
	}
}
