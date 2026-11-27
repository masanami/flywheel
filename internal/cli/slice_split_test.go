package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	j1NotOneSliceFixture = `{"is_error":false,"structured_output":{"verdict":"mine","fits_one_slice":false,"priority":"P1","reason":"SLICE-REASON","headline":"CLI の課題を分類する","context":"全体計画の一部で、分類のために要る。"}}`
	j10SingleFixture     = `{"is_error":false,"structured_output":{"verdict":"single","reason":"CLI test fixture","slices":null,"evidence":[{"path":"docs/a.md","note":"n"}]}}`
	// j10Match は区切りを分ける判断点の出力スキーマだけが持つ値。
	j10Match = `"single"`
)

// setupPendingSliceSplit は、取り込んだ課題に classify --auto を当てて、区切りを分ける記録が pending の課題を持つ
// ワークスペースと、その課題の ID を返す。同じ classify --auto が区切りを分ける判断点も呼ぶことを確かめる。
func setupPendingSliceSplit(t *testing.T) (ws, id string) {
	t.Helper()
	ws = setupJ2Workspace(t)
	writeSourcesDeclaration(t, ws, cycleSourcesJSON)
	issue := marshalFakeGHIssue(t, fakeGHIssue{Number: 1, Title: "ingested-issue", Body: "body", Repo: "o/r", UpdatedAt: "2026-09-25T09:00:00Z"})
	order := filepath.Join(t.TempDir(), "order.log")
	withFakeGHRoutesOrderedOnPATH(t, []fakeGHRoute{
		fakeGHListRoute("o/r", 1, "["+issue+"]", 0),
		fakeGHGetIssueStatusLineRoute("o/r", 1, "HTTP/2.0 200 OK", issue, 0),
		{match: "repos/o/r/issues/1/comments?per_page=100&page=1", stdout: "[]"},
	}, order)
	runJSON(t, ws, "ingest", "o/r#1")
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{
		{Match: fakeClaudeJ1Match, Tag: "J1", Stdout: j1NotOneSliceFixture},
		{Match: j10Match, Tag: "J10", Stdout: j10SingleFixture},
	}, order)

	phase := runJSON(t, ws, "classify", "--auto")["phase"].(map[string]any)
	items := phase["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("classify --auto items = %v, want the classification and the slice split", items)
	}
	first, second := items[0].(map[string]any), items[1].(map[string]any)
	if first["outcome"] != "mine" || second["outcome"] != "single" || second["result"] != "succeeded" {
		t.Fatalf("classify --auto items = %v", items)
	}
	log, err := os.ReadFile(order)
	if err != nil || !strings.Contains(string(log), "J10") {
		t.Fatalf("the fake claude was not called for J10: log=%q err=%v", log, err)
	}
	return ws, first["challenge_id"].(string)
}

// flywheel classify --auto は pending の課題に区切りを分ける判断点を呼び、その課題を指定した plan --auto は
// 計画を作る判断点を呼ばず、「着手しなかった」に slice_split_pending を出す。
func TestPlanAuto_PendingSliceSplit_ReportsNotStarted(t *testing.T) {
	parallelTools(t, false)
	ws, id := setupPendingSliceSplit(t)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j2RoutePlan(t)}, "")

	pm := runJSON(t, ws, "plan", "--auto", id)["phase"].(map[string]any)

	if items := pm["items"].([]any); len(items) != 0 {
		t.Errorf("plan items = %v, want none", items)
	}
	ns := phaseNotStarted(t, pm)
	if len(ns) != 1 || ns[0]["challenge_id"] != id || ns[0]["reason"] != "slice_split_pending" {
		t.Errorf("not_started = %v, want slice_split_pending for %s", ns, id)
	}
}
