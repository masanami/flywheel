package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

func f64(v float64) *float64 { return &v }

// タスクが枠を持つ計画は、承認の要約と show --plan の文に、タスクごとの額と計画の見込み（タスクの枠の合計）を出す。
func TestPlanText_ShowsTaskFramesAndTheirSum(t *testing.T) {
	tasks := []core.PlanTaskView{
		{Key: "a", Title: "A", BudgetImplUSD: f64(10), BudgetReviewUSD: f64(2)},
		{Key: "b", Title: "B", BudgetImplUSD: f64(10), BudgetReviewUSD: f64(3)},
	}
	var b strings.Builder
	writePlanStageSummary(&b, &core.ApprovalPreview{PlanTasks: tasks, PlanBudgetUSD: f64(25)})
	for _, want := range []string{"（枠: 実装 10 USD・レビュー対応 2 USD）", "（枠: 実装 10 USD・レビュー対応 3 USD）", "計画の見込み（タスクの枠の合計）: 25 USD"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("approval summary lacks %q:\n%s", want, b.String())
		}
	}
	text := planViewText(&core.PlanView{Structured: true, Tasks: tasks, BudgetUSD: f64(25)})
	for _, want := range []string{"見込み 25 USD", "（枠: 実装 10 USD・レビュー対応 2 USD）", "（枠: 実装 10 USD・レビュー対応 3 USD）"} {
		if !strings.Contains(text, want) {
			t.Errorf("show --plan lacks %q:\n%s", want, text)
		}
	}
	// 枠を持たない計画には、枠の行を足さない。
	var c strings.Builder
	writePlanStageSummary(&c, &core.ApprovalPreview{PlanTasks: []core.PlanTaskView{{Key: "a", Title: "A"}}, PlanBudgetUSD: f64(55)})
	if strings.Contains(c.String(), "枠") {
		t.Errorf("a plan without task frames must not show frames:\n%s", c.String())
	}
}
