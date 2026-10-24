package cli

import (
	"testing"
)

// M4.5 S2: 依存先が満たされていない課題は、run <C-ID> が終了コード 0 で phase.not_started に
// reason: dependency と detail で示し、何も起動しない。
func TestRun_WithChallengeID_UnmetDependencyIsNotStartedWithExitCodeZero(t *testing.T) {
	ws := setupRunWorkspace(t)
	id := newInProgressForRun(t, ws, nil)
	dep := createForCase(t, ws)
	runJSON(t, ws, "dep", "add", id, dep)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j3Route("b"), delegateRoute("completed")}, "")

	phase := phaseOf(t, runJSON(t, ws, "run", id))
	ns, _ := phase["not_started"].([]any)
	if len(ns) != 1 {
		t.Fatalf("not_started = %v", phase["not_started"])
	}
	n := ns[0].(map[string]any)
	if n["challenge_id"] != id || n["reason"] != "dependency" || n["detail"] != dep {
		t.Errorf("not_started[0] = %v, want %s dependency %s", n, id, dep)
	}
	if items, _ := phase["items"].([]any); len(items) != 0 {
		t.Errorf("items = %v, want none", items)
	}
	if got := delegateRunsOf(t, ws); len(got) != 0 {
		t.Errorf("delegate runs = %v, want none", got)
	}
}
