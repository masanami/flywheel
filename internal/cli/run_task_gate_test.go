package cli

import (
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// assertRunWithIDIsNotStarted は `flywheel run <課題の ID>` が終了コード 0 で phase.not_started に
// その課題と理由を 1 件だけ示し、何も起動しないことを確かめる（runJSON は終了コードが 0 でなければ失敗する）。
func assertRunWithIDIsNotStarted(t *testing.T, ws, id, reason string) {
	t.Helper()
	phase := phaseOf(t, runJSON(t, ws, "run", id))
	ns := phaseNotStarted(t, phase)
	if len(ns) != 1 {
		t.Fatalf("not_started = %v, want exactly one entry", phase["not_started"])
	}
	if ns[0]["challenge_id"] != id || ns[0]["reason"] != reason {
		t.Errorf("not_started[0] = %v, want %s %s", ns[0], id, reason)
	}
	if items, _ := phase["items"].([]any); len(items) != 0 {
		t.Errorf("items = %v, want none", items)
	}
	// 委譲だけでなく、委譲の指示文を書く判断点（J3）も起動していない（run の記録が 1 件も無い）。
	if runs, _ := runJSON(t, ws, "runs")["runs"].([]any); len(runs) != 0 {
		t.Errorf("runs = %v, want none (neither the delegation nor the J3 judgment may start)", runs)
	}
}

// newTwoTaskIntegrationForRun は、起動できるタスクが 2 個ある（統合先を使う）承認済みの計画を持つ着手中の課題を作る。
func newTwoTaskIntegrationForRun(t *testing.T, ws string) string {
	t.Helper()
	return newInProgressForRun(t, ws, func(m map[string]any) {
		task := func(key string) map[string]any {
			return map[string]any{"key": key, "title": "T-" + key, "body": "B", "done_criteria": "D",
				"non_goals": []string{}, "depends_on": []string{}, "repo": "direct-repo", "operation": "brief-op"}
		}
		m["tasks"] = []any{task("a"), task("b")}
		m["uses_integration"] = true
	})
}

// newOldShapePlanForRun は、タスクを持たない過去の形の計画（tasks のキーが無い）が承認済みで、task の行が無い着手中の課題を作る。
func newOldShapePlanForRun(t *testing.T, ws string) string {
	t.Helper()
	id := createForCase(t, ws)
	coretest.InsertApprovedPlan(t, ws, challengeIDToInternalID(t, id), "PLAN-BODY-OLD",
		`{"verdict":"plan","summary":"s","steps":["a"],"repo":"direct-repo","operation":"brief-op","size":"S","done_criteria":"d","cross_repo":false,"related_repos":[]}`)
	return id
}

// S2a（AC-119）: タスクが 2 個の着手中の課題（統合先を使う）に `flywheel run <課題の ID>` を実行すると、
// エラーではなく、起動しなかった理由 integration_unavailable が返る。
func TestRun_WithChallengeID_TwoTasksIsIntegrationUnavailable(t *testing.T) {
	parallelTools(t, false)
	ws := setupRunWorkspace(t)
	id := newTwoTaskIntegrationForRun(t, ws)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j3Route("b"), delegateRoute("completed")}, "")

	assertRunWithIDIsNotStarted(t, ws, id, "integration_unavailable")
}

// S2a（AC-122）: タスクを持たない過去の形の計画（tasks のキーが無い）で task の行が無い着手中の課題に
// `flywheel run <課題の ID>` を実行すると、エラーではなく、起動しなかった理由 no_tasks が返る。
func TestRun_WithChallengeID_OldShapePlanIsNoTasks(t *testing.T) {
	parallelTools(t, false)
	ws := setupRunWorkspace(t)
	id := newOldShapePlanForRun(t, ws)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j3Route("b"), delegateRoute("completed")}, "")

	assertRunWithIDIsNotStarted(t, ws, id, "no_tasks")
}
