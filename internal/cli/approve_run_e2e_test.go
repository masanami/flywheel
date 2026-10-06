package cli

import (
	"encoding/json"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは #143 の E2E: 本物の `flywheel approve`（疑似端末での本人確認）で計画を承認した課題が、
// `flywheel run` の委譲の候補に入ること、計画の版を持たない既存の承認は理由つきで not_started に出る
// ことを、子プロセスの CLI を通して検証する（承認を SQL で直接挿入しない）。

// approvedByCLI は create→classify→plan（2 版）→ 版 2 に構造化した出力 → 本物の approve の順で、
// 着手中の課題を作る。課題の版（承認の target_version）と計画の版（2）は一致しない。
func approvedByCLI(t *testing.T, ws string) string {
	t.Helper()
	id := createForCase(t, ws)
	runJSON(t, ws, "classify", id, "--priority", "P1")
	runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, "PLAN-V1"))
	runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, "PLAN-V2"))
	_, output := j2PlanFixture(t, nil)
	spec, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	coretest.SetPlanSpec(t, ws, challengeIDToInternalID(t, id), 2, string(spec))
	approveViaPTY(t, ws, id)
	return id
}

// 完了条件 1: CLI の approve で計画を承認した課題が、run の委譲の候補に入る。
func TestE2E_ApproveThenRun_DelegatesTheApprovedPlanVersion(t *testing.T) {
	ws := setupRunWorkspace(t)
	id := approvedByCLI(t, ws)

	show := runJSON(t, ws, "show", id)
	ap := show["approvals"].([]any)[0].(map[string]any)
	if ap["target_version"] != float64(4) {
		t.Fatalf("target_version = %v, want 4 (the challenge version)", ap["target_version"])
	}

	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j3Route("BRIEF-E2E"), delegateRoute("completed")}, "")
	withFakeGHRoutesOnPATH(t, nil)
	phase := phaseOf(t, runJSON(t, ws, "run"))
	items := phase["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["challenge_id"] != id {
		t.Fatalf("run phase = %v, want %s delegated", phase, id)
	}
	if ns, _ := phase["not_started"].([]any); len(ns) != 0 {
		t.Errorf("not_started = %v, want none", ns)
	}
	if got := len(delegateRunsOf(t, ws)); got != 1 {
		t.Errorf("delegate runs = %d, want 1", got)
	}
}

// 完了条件 2: 承認に計画の版が無い既存の承認は委譲せず、not_started に理由つきで出る。
func TestE2E_ApprovalWithoutPlanVersion_RunReportsNotStarted(t *testing.T) {
	ws := setupRunWorkspace(t)
	id := approvedByCLI(t, ws)
	coretest.ClearApprovalPlanVersion(t, ws, challengeIDToInternalID(t, id))

	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j3Route("BRIEF-E2E"), delegateRoute("completed")}, "")
	withFakeGHRoutesOnPATH(t, nil)
	phase := phaseOf(t, runJSON(t, ws, "run"))
	if items, _ := phase["items"].([]any); len(items) != 0 {
		t.Fatalf("items = %v, want none", items)
	}
	ns := phase["not_started"].([]any)
	if len(ns) != 1 || ns[0].(map[string]any)["challenge_id"] != id || ns[0].(map[string]any)["reason"] != "plan_unavailable" {
		t.Fatalf("not_started = %v, want %s plan_unavailable", ns, id)
	}
	if got := len(delegateRunsOf(t, ws)); got != 0 {
		t.Errorf("delegate runs = %d, want 0", got)
	}
}
