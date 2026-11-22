package cli

import (
	"testing"
)

// `flywheel ingest <指定>...` は選んだ Issue だけを課題にする（判定・作成は core）。ここでは
// CLI の写像（引数・JSON の形・エラーコード）を確かめる。

const selectRepo = "owner/select-repo"

func selectSetup(t *testing.T) string {
	t.Helper()
	ws := initializedWorkspace(t)
	writeSourcesDeclaration(t, ws, `{
  "version": 1,
  "sources": [
    {"id": "select-source", "type": "github-issue", "repos": ["`+selectRepo+`"], "self_assignees": ["someone"]}
  ]
}`)
	withFakeGHRoutesOnPATH(t, []fakeGHRoute{
		fakeGHListRoute(selectRepo, 1, fakeGHIssueListBody(t, []fakeGHIssue{
			{Number: 1, Title: "one", Body: "b1", Repo: selectRepo},
			{Number: 2, Title: "two", Body: "b2", Repo: selectRepo},
			{Number: 3, Title: "other's", Body: "b3", Repo: selectRepo, Assignees: []string{"eve"}},
		}), 0),
	})
	return ws
}

func TestIngestSelect_JSONRootIsIngestedAndOnlyChosenIssuesBecomeChallenges(t *testing.T) {
	parallelTools(t, false)
	ws := selectSetup(t)

	got := runJSON(t, ws, "ingest", "OWNER/select-repo#2")
	// cycle は、操作の後にサイクルを始めたかの結果（取り込み元を指定した ingest だけが持つ）。
	if _, ok := got["cycle"]; !ok {
		t.Fatalf("ingest <指定> --json = %v, want the key cycle", got)
	}
	delete(got, "cycle")
	items, ok := got["ingested"].([]any)
	if !ok || len(items) != 1 || len(got) != 1 {
		t.Fatalf("ingest --json = %v, want the single root key ingested with 1 element", got)
	}
	it := items[0].(map[string]any)
	if it["challenge_id"] != "C-1" || it["external_key"] != selectRepo+"#2" || it["children"] != 0.0 || len(it) != 3 {
		t.Fatalf("ingested[0] = %v, want challenge_id C-1 / canonical external_key / children 0", it)
	}
	if n := len(runJSON(t, ws, "list")["challenges"].([]any)); n != 1 {
		t.Fatalf("challenges = %d, want 1 (選ばなかった Issue は課題にしない)", n)
	}

	// 追従（引数なし）は新しい課題を作らず、対応の無い Issue をどの件数にも数えない。
	follow := runJSON(t, ws, "ingest")
	repo := repoOf(t, findSourceByID(t, follow["sources"].([]any), "select-source"), selectRepo)
	if len(itemsOf(t, repo)) != 1 || repo["excluded"] != 0.0 {
		t.Fatalf("follow-up repo = %v, want only the ingested issue and excluded=0", repo)
	}
	if n := len(runJSON(t, ws, "list")["challenges"].([]any)); n != 1 {
		t.Fatalf("challenges after follow-up = %d, want 1", n)
	}
}

func TestIngestSelect_NumberOnlyIsAcceptedWhenExactlyOneMatches(t *testing.T) {
	parallelTools(t, false)
	ws := selectSetup(t)
	got := runJSON(t, ws, "ingest", "1", "--source", "select-source")
	items := got["ingested"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["external_key"] != selectRepo+"#1" {
		t.Fatalf("ingested = %v, want %s#1", items, selectRepo)
	}
}

func TestIngestSelect_ErrorCodes(t *testing.T) {
	parallelTools(t, false)
	ws := selectSetup(t)
	for _, tc := range []struct {
		name string
		args []string
		code ErrorCode
		exit int
	}{
		{"同じ Issue を 2 度", []string{"ingest", selectRepo + "#1", selectRepo + "#1"}, CodeUsageError, 2},
		{"番号と指定の重複", []string{"ingest", "1", "1"}, CodeUsageError, 2},
		{"番号と指定が同じ Issue", []string{"ingest", selectRepo + "#1", "1"}, CodeUsageError, 2},
		{"形が違う", []string{"ingest", "abc"}, CodeUsageError, 2},
		{"方針に合わない", []string{"ingest", selectRepo + "#3"}, CodeValidationFailed, 1},
		{"存在しない番号", []string{"ingest", "99"}, CodeValidationFailed, 1},
		{"1 つでも取り込めないと全部取りやめ", []string{"ingest", selectRepo + "#1", selectRepo + "#3"}, CodeValidationFailed, 1},
		{"宣言に無い --source", []string{"ingest", "1", "--source", "nope"}, CodeValidationFailed, 1},
	} {
		requireJSONErrorEnvelope(t, append(append([]string{}, tc.args...), "--workspace", ws), tc.exit, tc.code)
	}
	if n := len(runJSON(t, ws, "list")["challenges"].([]any)); n != 0 {
		t.Fatalf("challenges = %d, want 0 (どれも取り込まない)", n)
	}
}

func TestIngestSelect_NoSourcesDeclarationIsConfigNotFound(t *testing.T) {
	parallelTools(t, false)
	ws := initializedWorkspace(t)
	requireJSONErrorEnvelope(t, []string{"ingest", "1", "--workspace", ws}, 2, CodeConfigNotFound)
}

func TestIngestSelect_UnresolvedSelfAssigneesIsUpstreamUnavailable(t *testing.T) {
	parallelTools(t, false)
	ws := initializedWorkspace(t)
	// self_assignees を省略し、gh api user が失敗する（偽の gh に user のルートが無い）。
	writeSourcesDeclaration(t, ws, `{"version": 1, "sources": [{"id": "s", "type": "github-issue", "repos": ["`+selectRepo+`"]}]}`)
	withFakeGHRoutesOnPATH(t, []fakeGHRoute{
		fakeGHListRoute(selectRepo, 1, fakeGHIssueListBody(t, []fakeGHIssue{{Number: 1, Title: "one", Body: "b", Repo: selectRepo}}), 0),
	})
	requireJSONErrorEnvelope(t, []string{"ingest", selectRepo + "#1", "--workspace", ws}, 2, CodeUpstreamUnavailable)
}

// 対応の無い、開いている Issue が上流にあるとき、サイクルを回しても、取り込みの段で課題は増えない
// （core の見直し S2b・masanami/flywheel#775 の AC-110）。サイクルが始まるよう、進められる課題を 1 件置く。
func TestIngestSelect_CycleIngestStageCreatesNoChallengeForUnboundIssues(t *testing.T) {
	parallelTools(t, false)
	ws := setupWorkspaceWithPosition(t)
	writeSourcesDeclaration(t, ws, cycleSourcesJSON)
	issue := marshalFakeGHIssue(t, fakeGHIssue{Number: 1, Title: "unbound", Body: "body", Repo: "o/r", UpdatedAt: "2026-09-25T09:00:00Z"})
	withFakeGHRoutesOnPATH(t, []fakeGHRoute{fakeGHListRoute("o/r", 1, "["+issue+"]", 0)})
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1")}, "")
	createTitled(t, ws, "pre-existing")

	doc := cycleDoc(t, ws)

	if ingest := cyclePhase(t, doc, "ingest"); ingest["skipped"] != false {
		t.Fatalf("ingest phase = %v, want it executed", ingest)
	}
	if n := len(runJSON(t, ws, "list")["challenges"].([]any)); n != 1 {
		t.Fatalf("challenges = %d, want 1 (取り込みの段は新しい課題を作らない)", n)
	}
}
