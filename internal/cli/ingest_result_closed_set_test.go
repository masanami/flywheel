package cli

import (
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは AC-95「result の値の閉集合を双方向に照合する」を検証する:
// - `ingest --json` の各 item.result は core.IngestOutcomeValues() の閉集合に
//   すべて含まれる（表 ⊇ 実装）
// - core.IngestOutcomeValues() の全ての値が、実際の ingest --json の出力に
//   少なくとも1回は現れる（表 ⊆ 実装）
//
// internal/cli/overview_discrepancies_test.go の
// TestRunStatus_Discrepancies_KindsMatchCoreClosedSetBothWays と同じ形の
// 双方向照合を、6つすべての result を1回の ingest で作り出して行う。

func repoOf(t *testing.T, source map[string]any, repo string) map[string]any {
	t.Helper()
	for _, r := range reposOf(t, source) {
		if r["repo"] == repo {
			return r
		}
	}
	t.Fatalf("repo %q not found in source %v", repo, source)
	return nil
}

func findItemByExternalKey(t *testing.T, items []map[string]any, externalKey string) map[string]any {
	t.Helper()
	for _, it := range items {
		if it["external_key"] == externalKey {
			return it
		}
	}
	t.Fatalf("item %q not found in %v", externalKey, items)
	return nil
}

func TestIngest_ResultValuesCoverClosedSetBothWays(t *testing.T) {
	ws := initializedWorkspace(t)
	const repo = "owner/closed-set-repo"
	decl := `{
  "version": 1,
  "sources": [
    {"id": "closed-set-source", "type": "github-issue", "repos": ["` + repo + `"], "self_assignees": ["someone"]}
  ]
}`
	writeSourcesDeclaration(t, ws, decl)

	// --- unchanged: 既存の課題・対応の fingerprint が上流の本文と一致する ---
	unchangedID := runJSON(t, ws, "create", "--title", "unchanged")["challenge"].(map[string]any)["id"].(string)
	coretest.InsertSourceBinding(t, ws, challengeIDToInternalID(t, unchangedID),
		"closed-set-source", repo+"#1", "https://example.invalid/"+repo+"#1",
		core.Fingerprint("same body"), "open", "in_policy", "2026-09-26T00:00:00.000Z")

	// --- updated: fingerprint が上流の本文と食い違う ---
	updatedID := runJSON(t, ws, "create", "--title", "old title")["challenge"].(map[string]any)["id"].(string)
	coretest.InsertSourceBinding(t, ws, challengeIDToInternalID(t, updatedID),
		"closed-set-source", repo+"#2", "https://example.invalid/"+repo+"#2",
		core.Fingerprint("old body"), "open", "in_policy", "2026-09-26T00:00:00.000Z")

	// --- skipped_done: 課題が完了している ---
	doneID := runJSON(t, ws, "create", "--title", "done one")["challenge"].(map[string]any)["id"].(string)
	coretest.InsertSourceBinding(t, ws, challengeIDToInternalID(t, doneID),
		"closed-set-source", repo+"#3", "https://example.invalid/"+repo+"#3",
		core.Fingerprint("whatever"), "open", "in_policy", "2026-09-26T00:00:00.000Z")
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, doneID), "done")

	// --- fingerprint_unknown_version: 記録された版が 2 でない ---
	unknownVersionID := runJSON(t, ws, "create", "--title", "unknown version")["challenge"].(map[string]any)["id"].(string)
	coretest.InsertSourceBinding(t, ws, challengeIDToInternalID(t, unknownVersionID),
		"closed-set-source", repo+"#4", "https://example.invalid/"+repo+"#4",
		"1:aaaaaaaaaaaa", "open", "in_policy", "2026-09-26T00:00:00.000Z")

	// --- failed: 対応はあるが open の一覧に現れず、1件取得が 404/410 以外で失敗する ---
	failedID := runJSON(t, ws, "create", "--title", "will fail on get")["challenge"].(map[string]any)["id"].(string)
	coretest.InsertSourceBinding(t, ws, challengeIDToInternalID(t, failedID),
		"closed-set-source", repo+"#5", "https://example.invalid/"+repo+"#5",
		core.Fingerprint("irrelevant"), "open", "in_policy", "2026-09-26T00:00:00.000Z")

	withFakeGHRoutesOnPATH(t, []fakeGHRoute{
		fakeGHListRoute(repo, 1, fakeGHIssueListBody(t, []fakeGHIssue{
			{Number: 1, Title: "unchanged", Body: "same body", Repo: repo},
			{Number: 2, Title: "new title", Body: "new body", Repo: repo},
			{Number: 3, Title: "done one", Body: "whatever", Repo: repo},
			{Number: 4, Title: "unknown version", Body: "changed body", Repo: repo},
			// #5 は意図的に一覧に含めない（close の確かめの対象にする）。
			{Number: 6, Title: "brand new", Body: "brand new body", Repo: repo},
		}), 0),
		fakeGHGetIssueStatusLineRoute(repo, 5, "HTTP/2.0 500 Internal Server Error", `{"message":"boom"}`, 1),
	})

	got := runJSON(t, ws, "ingest")
	sources := got["sources"].([]any)
	source := findSourceByID(t, sources, "closed-set-source")
	r := repoOf(t, source, repo)
	if r["error"] != nil {
		t.Fatalf("repo.error = %v, want nil", r["error"])
	}
	items := itemsOf(t, r)

	wantResultByKey := map[string]string{
		repo + "#1": "unchanged",
		repo + "#2": "updated",
		repo + "#3": "skipped_done",
		repo + "#4": "fingerprint_unknown_version",
		repo + "#5": "failed",
		repo + "#6": "created",
	}
	for key, want := range wantResultByKey {
		item := findItemByExternalKey(t, items, key)
		if item["result"] != want {
			t.Errorf("item[%s].result = %v, want %q", key, item["result"], want)
		}
	}

	// --- 双方向の照合 ---
	seen := map[string]bool{}
	for _, it := range items {
		result, ok := it["result"].(string)
		if !ok {
			t.Fatalf("item.result = %#v, want a string", it["result"])
		}
		seen[result] = true
	}
	want := map[string]bool{}
	for _, v := range core.IngestOutcomeValues() {
		want[string(v)] = true
		if !seen[string(v)] {
			t.Errorf("core.IngestOutcomeValues() の %q が ingest --json のどの item.result にも現れなかった (seen=%v)", v, seen)
		}
	}
	for r := range seen {
		if !want[r] {
			t.Errorf("ingest --json が result %q を出力したが、core.IngestOutcomeValues() に含まれない", r)
		}
	}
}
