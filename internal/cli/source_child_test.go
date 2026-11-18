package cli

import (
	"strings"
	"testing"
)

// 取り込み元の子の CLI の出力（#776。S2b）: ingest の children・show の source_children と子の件数。

const childRepo = "owner/child-repo"

func childSetup(t *testing.T) string {
	t.Helper()
	ws := initializedWorkspace(t)
	writeSourcesDeclaration(t, ws, `{
  "version": 1,
  "sources": [
    {"id": "child-source", "type": "github-issue", "repos": ["`+childRepo+`"], "self_assignees": ["someone"]}
  ]
}`)
	withFakeGHRoutesOnPATH(t, []fakeGHRoute{
		fakeGHListRoute(childRepo, 1, fakeGHIssueListBody(t, []fakeGHIssue{
			{Number: 1, Title: "parent", Body: "bp", Repo: childRepo},
			{Number: 2, Title: "child two", Body: "b2", Repo: childRepo, ParentNumber: 1},
			{Number: 3, Title: "child three", Body: "b3", Repo: childRepo, ParentNumber: 1},
		}), 0),
	})
	return ws
}

func TestSourceChildCLI_AC147_IngestedChildrenIsTheNumberOfRecordedChildren(t *testing.T) { // #776
	parallelTools(t, false)
	ws := childSetup(t)
	got := runJSON(t, ws, "ingest", childRepo+"#1")
	items := got["ingested"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["children"] != 2.0 {
		t.Fatalf("AC-147 #776: ingested = %v, want children 2", items)
	}
	if n := len(runJSON(t, ws, "list")["challenges"].([]any)); n != 1 {
		t.Fatalf("challenges = %d, want 1", n)
	}
}

func TestSourceChildCLI_AC291_292_ShowJSONCarriesSourceChildren(t *testing.T) { // #776
	parallelTools(t, false)
	ws := childSetup(t)
	runJSON(t, ws, "ingest", childRepo+"#1")
	got := runJSON(t, ws, "show", "C-1")
	// 文書（m1-core.md の show の形）との照合を、子の要素がある出力でかける（空の配列では、要素の形が照合されない）。
	assertDocumentedJSON(t, loadDocumentedJSON(t), "show", got)
	kids, ok := got["source_children"].([]any) // AC-291
	if !ok || len(kids) != 2 {
		t.Fatalf("AC-291 #776: source_children = %v, want 2 elements at the root", got["source_children"])
	}
	first := kids[0].(map[string]any) // AC-292
	if len(first) != 5 {
		t.Fatalf("AC-292 #776: keys = %v, want exactly 5", first)
	}
	want := map[string]any{
		"external_key":        childRepo + "#2",
		"title":               "child two",
		"url":                 "https://github.com/" + childRepo + "/issues/2",
		"upstream_state":      "open",
		"parent_external_key": childRepo + "#1",
	}
	for k, v := range want {
		if first[k] != v {
			t.Errorf("AC-292 #776: %s = %v, want %v", k, first[k], v)
		}
	}
}

func TestSourceChildCLI_ShowJSONHasAnEmptyListWithoutChildren(t *testing.T) { // #776
	parallelTools(t, false)
	ws := childSetup(t)
	runJSON(t, ws, "ingest", childRepo+"#2")
	kids, ok := runJSON(t, ws, "show", "C-1")["source_children"].([]any)
	if !ok || len(kids) != 0 {
		t.Fatalf("source_children = %v, want []", kids)
	}
}

func TestSourceChildCLI_AC297_ShowTextShowsTheNumberOfChildren(t *testing.T) { // #776
	parallelTools(t, false)
	ws := childSetup(t)
	runJSON(t, ws, "ingest", childRepo+"#1")
	out := runText(t, ws, "show", "C-1")
	if !strings.Contains(out, "取り込み元の子: 2 件") {
		t.Fatalf("AC-297 #776: show text = %q, want the number of children", out)
	}
	runJSON(t, ws, "unbind", "C-1")
	if strings.Contains(runText(t, ws, "show", "C-1"), "取り込み元の子") {
		t.Errorf("show text still mentions children after unbind")
	}
}
