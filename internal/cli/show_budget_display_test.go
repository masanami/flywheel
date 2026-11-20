package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは docs/features/core-redesign.md の「表示」（区切り S3b）のうち、`flywheel show` のタスクの枠・使った額と
// 統合先の識別（AC-228〜AC-230・AC-232・AC-234・AC-235）を確かめる。

// budgetShownWorkspace は、T-1 に枠（実装 30・レビュー対応 12.5）と使った額（実装 2 + レビュー対応 1.5）を持たせた課題を作る。
func budgetShownWorkspace(t *testing.T) (ws, id string) {
	t.Helper()
	ws, id = approvedTasksWorkspace(t)
	coretest.Exec(t, ws, `UPDATE task SET budget_impl_micros = 30000000, budget_review_micros = 12500000 WHERE id = 1`)
	coretest.Exec(t, ws, `UPDATE task SET budget_impl_micros = 5000000, budget_review_micros = 6000000 WHERE id = 2`)
	for _, r := range []struct {
		bucket string
		micros int64
		task   int
		reflct int
	}{{"impl", 2000000, 1, 0}, {"review", 1500000, 1, 0}, {"judgment", 9000000, 1, 0}, {"impl", 4000000, 1, 1}} {
		coretest.Exec(t, ws, `INSERT INTO run (challenge_id, kind, challenge_version, plan_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket, cost_usd, task_id, reflect)
			VALUES (1, 'delegate', 1, 1, 's', 1, 'h', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:01.000Z', 'succeeded', 1000000, ?, ?, ?, ?)`,
			r.bucket, r.micros, r.task, r.reflct)
	}
	return ws, id
}

// AC-228・AC-229・AC-230: tasks[] の要素が budget_impl_usd・budget_review_usd（枠）と spent_usd（そのタスクの実行の費用の合計）を持つ。
func TestShowJSON_TasksCarryTheirFramesAndSpent(t *testing.T) {
	t.Parallel()
	ws, id := budgetShownWorkspace(t)
	tasks := objectList(t, runJSON(t, ws, "show", id, "--json")["tasks"], "tasks")
	want := []struct{ impl, review, spent float64 }{{30, 12.5, 3.5}, {5, 6, 0}}
	for i, w := range want {
		for _, k := range []string{"budget_impl_usd", "budget_review_usd", "spent_usd"} {
			if _, ok := tasks[i][k].(float64); !ok {
				t.Fatalf("tasks[%d].%s = %#v, want a number", i, k, tasks[i][k])
			}
		}
		if tasks[i]["budget_impl_usd"] != w.impl || tasks[i]["budget_review_usd"] != w.review || tasks[i]["spent_usd"] != w.spent {
			t.Errorf("tasks[%d] frames = %v / %v / %v, want %+v", i, tasks[i]["budget_impl_usd"], tasks[i]["budget_review_usd"], tasks[i]["spent_usd"], w)
		}
	}
}

// AC-232: challenge.integration_ref は、記録が無ければ null、あればその値。
func TestShowJSON_ChallengeCarriesIntegrationRef(t *testing.T) {
	t.Parallel()
	ws, id := budgetShownWorkspace(t)
	ch := runJSON(t, ws, "show", id, "--json")["challenge"].(map[string]any)
	if v, has := ch["integration_ref"]; !has || v != nil {
		t.Errorf("integration_ref = %v (present=%v), want present and null", v, has)
	}
	coretest.Exec(t, ws, `UPDATE challenge SET integration_ref = 'flywheel/integration/c-1' WHERE id = 1`)
	ch = runJSON(t, ws, "show", id, "--json")["challenge"].(map[string]any)
	if ch["integration_ref"] != "flywheel/integration/c-1" {
		t.Errorf("integration_ref = %v, want flywheel/integration/c-1", ch["integration_ref"])
	}
}

// AC-234: 文の出力のタスクの行に、枠と使った額が出る。
func TestShowText_TaskLinesCarryFramesAndSpent(t *testing.T) {
	t.Parallel()
	ws, id := budgetShownWorkspace(t)
	out := runText(t, ws, "show", id)
	var line1, line2 string
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "  T-1 "):
			line1 = l
		case strings.HasPrefix(l, "  T-2 "):
			line2 = l
		}
	}
	for _, want := range []string{"実装 30 USD", "レビュー対応 12.5 USD", "使った額: 3.5 USD"} {
		if !strings.Contains(line1, want) {
			t.Errorf("T-1 line %q lacks %q", line1, want)
		}
	}
	for _, want := range []string{"実装 5 USD", "レビュー対応 6 USD", "使った額: 0 USD"} {
		if !strings.Contains(line2, want) {
			t.Errorf("T-2 line %q lacks %q", line2, want)
		}
	}
}

// AC-235: 文の出力の課題の欄に、統合先の識別が出る（記録があるときだけ）。
func TestShowText_IntegrationRefAppearsOnlyWhenRecorded(t *testing.T) {
	t.Parallel()
	ws, id := budgetShownWorkspace(t)
	if out := runText(t, ws, "show", id); strings.Contains(out, "統合先の識別") {
		t.Errorf("output names an integration ref although none is recorded:\n%s", out)
	}
	coretest.Exec(t, ws, `UPDATE challenge SET integration_ref = 'flywheel/integration/c-1' WHERE id = 1`)
	if out := runText(t, ws, "show", id); !strings.Contains(out, "統合先の識別:  flywheel/integration/c-1") {
		t.Errorf("output lacks the integration ref:\n%s", out)
	}
}
