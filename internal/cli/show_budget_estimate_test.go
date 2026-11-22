package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// 区切り S4c-1: `flywheel show` の予算（見込み・上限・上限の理由）。

// 上限の記録が無い課題の上限は、見込みと同じ額。記録した上限と理由は、そのまま出る。
func TestShowJSON_BudgetCarriesEstimateAndLimit(t *testing.T) {
	t.Parallel()
	ws, id := budgetShownWorkspace(t) // 見込み = (30 + 12.5) + (5 + 6) + (30 + 12.5) = 96.0
	b := runJSON(t, ws, "show", id, "--json")["budget"].(map[string]any)
	if b["estimate_usd"] != 96.0 || b["limit_usd"] != 96.0 {
		t.Errorf("budget = %v, want estimate 96 and limit 96 (no limit record reads as the estimate)", b)
	}
	if v, has := b["limit_reason"]; !has || v != nil {
		t.Errorf("limit_reason = %v (present=%v), want present and null", v, has)
	}
	coretest.SetChallengeBudgetLimit(t, ws, 1, 100_000_000, "続きを足す分")
	b = runJSON(t, ws, "show", id, "--json")["budget"].(map[string]any)
	if b["estimate_usd"] != 96.0 || b["limit_usd"] != 100.0 || b["limit_reason"] != "続きを足す分" {
		t.Errorf("budget = %v, want estimate 96.0, limit 100, the reason", b)
	}
}

// AC-21・AC-22: 枠を決められないタスクがある課題の budget.estimate_usd・limit_usd は null。
func TestShowJSON_BudgetIsNullWhenAFrameCannotBeDecided(t *testing.T) {
	t.Parallel()
	ws, id := budgetShownWorkspace(t)
	coretest.Exec(t, ws, `UPDATE task SET budget_impl_micros = NULL, budget_review_micros = NULL WHERE id = 2`)
	coretest.Exec(t, ws, `UPDATE task_plan SET spec = json_remove(spec, '$.budget_impl_usd', '$.budget_review_usd', '$.size')`)
	coretest.SetChallengeBudgetLimit(t, ws, 1, 90_000_000, "")
	b := runJSON(t, ws, "show", id, "--json")["budget"].(map[string]any)
	for _, k := range []string{"estimate_usd", "limit_usd"} {
		if v, has := b[k]; !has || v != nil {
			t.Errorf("budget.%s = %v (present=%v), want present and null", k, v, has)
		}
	}
	if out := runText(t, ws, "show", id); !strings.Contains(out, "予算:          不明") {
		t.Errorf("show text = %q, want the budget line to say 不明", out)
	}
}

func TestShowText_BudgetLineHasEstimateLimitAndReason(t *testing.T) {
	t.Parallel()
	ws, id := budgetShownWorkspace(t)
	if out := runText(t, ws, "show", id); !strings.Contains(out, "予算:          見込み 96 USD・上限 96 USD\n") {
		t.Errorf("show text = %q, want the estimate and the limit", out)
	}
	coretest.SetChallengeBudgetLimit(t, ws, 1, 100_000_000, "続きを足す分")
	if out := runText(t, ws, "show", id); !strings.Contains(out, "予算:          見込み 96 USD・上限 100 USD（上限の理由: 続きを足す分）\n") {
		t.Errorf("show text = %q, want the limit with its reason", out)
	}
}
