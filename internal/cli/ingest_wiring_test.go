package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// このファイルは Issue #59「結線: ingest に取得と反映をつなぐ」のうち、
// 実際に `gh` を呼んで取得・反映する結線そのもの（AC-14・AC-16・AC-17・
// AC-18・AC-19・AC-75）を検証する。宣言の検証・--source の絞り込み自体
// （#54）・取り込みの規則そのもの（#56〜#58・core 側のテスト）は対象外。

// twoSourceDeclaration は 2 つの取り込み元を宣言する（AC-14・AC-75 が
// 「指定した取り込み元のリポジトリだけを取得する」「宣言から外したリポジトリへ
// gh の呼び出しが無い」を検証するために使う）。self_assignees を明示して
// `gh api user` を呼ばせない（#59 の範囲外である self_assignees 省略時の
// 挙動は core 側のテストが担う）。
const twoSourceDeclaration = `{
  "version": 1,
  "sources": [
    {"id": "source-a", "type": "github-issue", "repos": ["owner/repo-a"], "self_assignees": ["someone"]},
    {"id": "source-b", "type": "github-issue", "repos": ["owner/repo-b"], "self_assignees": ["someone"]}
  ]
}`

// findSourceByID は ingest --json の sources 配列から id の一致する要素を返す。
func findSourceByID(t *testing.T, sources []any, id string) map[string]any {
	t.Helper()
	for _, s := range sources {
		m := s.(map[string]any)
		if m["id"] == id {
			return m
		}
	}
	t.Fatalf("source %q not found in %v", id, sources)
	return nil
}

func reposOf(t *testing.T, source map[string]any) []map[string]any {
	t.Helper()
	raw, ok := source["repos"].([]any)
	if !ok {
		t.Fatalf("source.repos = %#v, want an array", source["repos"])
	}
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.(map[string]any))
	}
	return out
}

func itemsOf(t *testing.T, repo map[string]any) []map[string]any {
	t.Helper()
	raw, ok := repo["items"].([]any)
	if !ok {
		t.Fatalf("repo.items = %#v, want an array", repo["items"])
	}
	out := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		out = append(out, it.(map[string]any))
	}
	return out
}

// callsContain は logged calls（"$*" の値の一覧）のいずれかが substr を含むかを
// 返す。
func callsContain(calls []string, substr string) bool {
	for _, c := range calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

// --- AC-14・AC-75: --source は指定した取り込み元のリポジトリだけを取得する。
// 宣言から外したリポジトリへの gh の呼び出しが無く、その課題の upstream_state
// も変わらない。 ---

func TestIngest_SourceFlag_OnlyFetchesSelectedSourceRepos(t *testing.T) {
	ws := initializedWorkspace(t)
	writeSourcesDeclaration(t, ws, twoSourceDeclaration)

	// repo-b に対応する既存の課題を 1 件置く（AC-75 の「その課題の
	// upstream_state が変わらない」を検証するため）。
	created := runJSON(t, ws, "create", "--title", "excluded-repo-challenge")
	challengeID := created["challenge"].(map[string]any)["id"].(string)
	bindSourceForDiscrepancyCase(t, ws, challengeID, "owner/repo-b#9", "open", "in_policy")

	// repo-b にも応答するルートを用意する（code-reviewer 指摘の再発防止:
	// 偽の gh に repo-b のルートが無いと、--source の絞り込みが誤って
	// repo-b も処理してしまうバグがあっても、一覧の取得が単に失敗するだけで
	// upstream_state のアサーションには検出力が無かった）。repo-b#9 を
	// 一覧に含めず（open の一覧に無い）、1 件取得が 404 を返すようにすることで、
	// 誤って処理された場合は upstream_state が "missing" に変わり、
	// このテストが検出できるようにする。
	calls := withFakeGHRoutesOnPATH(t, []fakeGHRoute{
		fakeGHListRoute("owner/repo-a", 1, fakeGHIssueListBody(t, []fakeGHIssue{
			{Number: 1, Title: "a1", Body: "body a1", Repo: "owner/repo-a"},
		}), 0),
		fakeGHListRoute("owner/repo-b", 1, "[]", 0),
		fakeGHGetIssueStatusLineRoute("owner/repo-b", 9, "HTTP/2.0 404 Not Found", `{"message":"Not Found"}`, 1),
	})

	got := runJSON(t, ws, "ingest", "--source", "source-a")

	sources := got["sources"].([]any)
	if len(sources) != 1 {
		t.Fatalf("sources = %v, want exactly the selected source (source-a)", sources)
	}
	source := findSourceByID(t, sources, "source-a")
	repos := reposOf(t, source)
	if len(repos) != 1 || repos[0]["repo"] != "owner/repo-a" {
		t.Fatalf("source-a.repos = %v, want exactly owner/repo-a", repos)
	}

	allCalls := calls()
	if callsContain(allCalls, "repo-b") {
		t.Errorf("gh must not be called for the excluded repo (repo-b), got calls: %v", allCalls)
	}

	show := runJSON(t, ws, "show", challengeID)
	sb := show["source_binding"].(map[string]any)
	if sb["upstream_state"] != "open" {
		t.Errorf("excluded repo's challenge upstream_state = %v, want unchanged (open)", sb["upstream_state"])
	}
}

// --- AC-14（前半）: `--source` を省略すると、宣言のすべての取り込み元を
// 宣言の順に処理する（code-reviewer 指摘の再発防止: 上のテストは --source に
// よる絞り込みしか検証しておらず、順序そのものを検証するテストが無かった）。
func TestIngest_NoSourceFlag_ProcessesAllSourcesInDeclaredOrder(t *testing.T) {
	ws := initializedWorkspace(t)
	writeSourcesDeclaration(t, ws, twoSourceDeclaration)

	calls := withFakeGHRoutesOnPATH(t, []fakeGHRoute{
		fakeGHListRoute("owner/repo-a", 1, fakeGHIssueListBody(t, []fakeGHIssue{
			{Number: 1, Title: "a1", Body: "body a1", Repo: "owner/repo-a"},
		}), 0),
		fakeGHListRoute("owner/repo-b", 1, fakeGHIssueListBody(t, []fakeGHIssue{
			{Number: 9, Title: "b9", Body: "body b9", Repo: "owner/repo-b"},
		}), 0),
	})

	got := runJSON(t, ws, "ingest")

	sources := got["sources"].([]any)
	if len(sources) != 2 {
		t.Fatalf("sources = %v, want both declared sources", sources)
	}
	// JSON の並び自体が宣言の順（source-a → source-b）であること。
	if sources[0].(map[string]any)["id"] != "source-a" || sources[1].(map[string]any)["id"] != "source-b" {
		t.Errorf("sources order = [%v, %v], want [source-a, source-b] (宣言の順)", sources[0].(map[string]any)["id"], sources[1].(map[string]any)["id"])
	}

	// gh の呼び出しそのものも宣言の順（repo-a の一覧取得 → repo-b の一覧取得）で
	// 行われること（JSON の並びだけでなく、実際の呼び出し順まで確かめる）。
	allCalls := calls()
	idxA, idxB := -1, -1
	for i, c := range allCalls {
		switch {
		case strings.Contains(c, "repo-a"):
			if idxA == -1 {
				idxA = i
			}
		case strings.Contains(c, "repo-b"):
			if idxB == -1 {
				idxB = i
			}
		}
	}
	if idxA == -1 || idxB == -1 {
		t.Fatalf("expected gh calls for both repo-a and repo-b, got calls: %v", allCalls)
	}
	if idxA >= idxB {
		t.Errorf("gh call order = %v, want repo-a's list call before repo-b's (宣言の順)", allCalls)
	}
}

// --- AC-16・AC-17: 3 ページ 250 件のうち、ポリシーに合う Issue がすべて
// 課題になり、Pull Request は課題にならない。 ---

func TestIngest_ThreePagesOfIssues_AllBecomeChallengesAndPullRequestsAreExcluded(t *testing.T) {
	ws := initializedWorkspace(t)
	decl := `{
  "version": 1,
  "sources": [
    {"id": "big-repo-source", "type": "github-issue", "repos": ["owner/big-repo"], "self_assignees": ["someone"]}
  ]
}`
	writeSourcesDeclaration(t, ws, decl)

	page1 := make([]fakeGHIssue, 0, 100)
	for i := 1; i <= 100; i++ {
		page1 = append(page1, fakeGHIssue{Number: i, Title: fmt.Sprintf("issue %d", i), Body: fmt.Sprintf("body %d", i), Repo: "owner/big-repo"})
	}
	page2 := make([]fakeGHIssue, 0, 100)
	for i := 101; i <= 160; i++ {
		page2 = append(page2, fakeGHIssue{Number: i, Title: fmt.Sprintf("issue %d", i), Body: fmt.Sprintf("body %d", i), Repo: "owner/big-repo"})
	}
	for i := 900; i < 940; i++ { // 混在させた Pull Request（除外の対象）
		page2 = append(page2, fakeGHIssue{Number: i, Title: fmt.Sprintf("pr %d", i), Body: "pr body", Repo: "owner/big-repo", PullRequest: true})
	}
	page3 := make([]fakeGHIssue, 0, 50)
	for i := 161; i <= 210; i++ {
		page3 = append(page3, fakeGHIssue{Number: i, Title: fmt.Sprintf("issue %d", i), Body: fmt.Sprintf("body %d", i), Repo: "owner/big-repo"})
	}

	withFakeGHRoutesOnPATH(t, []fakeGHRoute{
		fakeGHListRoute("owner/big-repo", 1, fakeGHIssueListBody(t, page1), 0),
		fakeGHListRoute("owner/big-repo", 2, fakeGHIssueListBody(t, page2), 0),
		fakeGHListRoute("owner/big-repo", 3, fakeGHIssueListBody(t, page3), 0),
	})

	got := runJSON(t, ws, "ingest")

	sources := got["sources"].([]any)
	source := findSourceByID(t, sources, "big-repo-source")
	repos := reposOf(t, source)
	if len(repos) != 1 {
		t.Fatalf("repos = %v, want 1", repos)
	}
	repo := repos[0]
	if repo["error"] != nil {
		t.Fatalf("repo.error = %v, want nil", repo["error"])
	}
	items := itemsOf(t, repo)
	if len(items) != 210 {
		t.Fatalf("len(items) = %d, want 210 (すべての Issue が課題になり、Pull Request は含まれない)", len(items))
	}
	for _, it := range items {
		if it["result"] != "created" {
			t.Errorf("item.result = %v, want created: %+v", it["result"], it)
		}
	}

	list := runJSON(t, ws, "list")
	if got := len(list["challenges"].([]any)); got != 210 {
		t.Errorf("list.challenges = %d, want 210 (Pull Request は課題にならない)", got)
	}
}

// --- AC-18・AC-19: 2 つのリポジトリのうち 1 つの一覧の取得が失敗しても、
// もう 1 つは取り込まれ、終了コード 0 で終わる。失敗したリポジトリの
// error は null でなく items は []・excluded は 0。失敗したリポジトリに
// 対応する既存の課題は何も変わらない。 ---

func TestIngest_OneRepoListFailure_OtherRepoStillIngestedAndFailedRepoUnchanged(t *testing.T) {
	ws := initializedWorkspace(t)
	decl := `{
  "version": 1,
  "sources": [
    {"id": "two-repo-source", "type": "github-issue", "repos": ["owner/ok-repo", "owner/broken-repo"], "self_assignees": ["someone"]}
  ]
}`
	writeSourcesDeclaration(t, ws, decl)

	created := runJSON(t, ws, "create", "--title", "broken-repo-challenge")
	challengeID := created["challenge"].(map[string]any)["id"].(string)
	bindSourceForDiscrepancyCase(t, ws, challengeID, "owner/broken-repo#3", "open", "in_policy")
	beforeShow := runJSON(t, ws, "show", challengeID)

	withFakeGHRoutesOnPATH(t, []fakeGHRoute{
		fakeGHListRoute("owner/ok-repo", 1, fakeGHIssueListBody(t, []fakeGHIssue{
			{Number: 1, Title: "ok issue", Body: "ok body", Repo: "owner/ok-repo"},
		}), 0),
		{match: "repos/owner/broken-repo/issues?state=open&per_page=100&page=1", stdout: "gh: simulated list failure", exit: 1},
	})

	var stdout, stderr strings.Builder
	code := run([]string{"ingest", "--workspace", ws, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (partial success. stdout=%q stderr=%q)", code, stdout.String(), stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout.String()), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (%q)", err, stdout.String())
	}

	sources := got["sources"].([]any)
	source := findSourceByID(t, sources, "two-repo-source")
	repos := reposOf(t, source)
	var okRepo, brokenRepo map[string]any
	for _, r := range repos {
		switch r["repo"] {
		case "owner/ok-repo":
			okRepo = r
		case "owner/broken-repo":
			brokenRepo = r
		}
	}
	if okRepo == nil || okRepo["error"] != nil || len(itemsOf(t, okRepo)) != 1 {
		t.Errorf("ok-repo = %+v, want error=nil and 1 item", okRepo)
	}
	if brokenRepo == nil {
		t.Fatalf("broken-repo missing from output: %v", repos)
	}
	if brokenRepo["error"] == nil {
		t.Errorf("broken-repo.error = nil, want non-null (list failure)")
	}
	if items := itemsOf(t, brokenRepo); len(items) != 0 {
		t.Errorf("broken-repo.items = %v, want [] (AC-18/19)", items)
	}
	if excluded, ok := brokenRepo["excluded"].(float64); !ok || excluded != 0 {
		t.Errorf("broken-repo.excluded = %v, want 0", brokenRepo["excluded"])
	}

	afterShow := runJSON(t, ws, "show", challengeID)
	beforeSB := beforeShow["source_binding"].(map[string]any)
	afterSB := afterShow["source_binding"].(map[string]any)
	if beforeSB["upstream_state"] != afterSB["upstream_state"] || beforeSB["policy_state"] != afterSB["policy_state"] || beforeSB["fingerprint"] != afterSB["fingerprint"] {
		t.Errorf("broken-repo's challenge source_binding changed: before=%+v after=%+v", beforeSB, afterSB)
	}
	beforeChallenge := beforeShow["challenge"].(map[string]any)
	afterChallenge := afterShow["challenge"].(map[string]any)
	if beforeChallenge["version"] != afterChallenge["version"] {
		t.Errorf("broken-repo's challenge version changed: before=%v after=%v", beforeChallenge["version"], afterChallenge["version"])
	}
}
