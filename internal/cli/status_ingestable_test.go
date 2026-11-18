package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// `flywheel status` の ingestable・followups と、文の出力「いま進められること」
// （masanami/flywheel#779。受入基準の番号は AC-<番号>）。

const ingRepo = "owner/ing-repo"

func ingestableSetup(t *testing.T, selfAssignees string) string {
	t.Helper()
	ws := initializedWorkspace(t)
	writeSourcesDeclaration(t, ws, `{"version":1,"sources":[{"id":"ing-source","type":"github-issue","repos":["`+ingRepo+`"],`+selfAssignees+`}]}`)
	withFakeGHRoutesOnPATH(t, []fakeGHRoute{
		fakeGHListRoute(ingRepo, 1, fakeGHIssueListBody(t, []fakeGHIssue{
			{Number: 1, Title: "parent one", Repo: ingRepo},
			{Number: 2, Title: "child two", Repo: ingRepo, ParentNumber: 1},
			{Number: 3, Title: "other's", Repo: ingRepo, Assignees: []string{"eve"}},
		}), 0),
	})
	return ws
}

func ingestableOf(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	ing, ok := doc["ingestable"].(map[string]any)
	if !ok {
		t.Fatalf("status has no ingestable object: %v", doc)
	}
	return ing
}

// AC-230・AC-231・AC-233・AC-247（#779）: 根のキー（足したものと見直し前のもの）。宣言が無ければ items は空の配列・error は null。
func TestStatus_AC230_AC231_AC233_AC247_RootKeysAndEmptyIngestable(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	doc := runJSON(t, ws, "status")
	for _, k := range []string{"ingestable", "followups", "actionable", "approved", "binary_commit", "needs_human", "parent_agent", "waiting_external", "cycle_work"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("status root has no %q key: %v", k, doc)
		}
	}
	ing := ingestableOf(t, doc)
	if items, ok := ing["items"].([]any); !ok || len(items) != 0 {
		t.Errorf("ingestable.items = %#v, want an empty array (AC-247)", ing["items"])
	}
	if ing["error"] != nil {
		t.Errorf("ingestable.error = %#v, want null", ing["error"])
	}
}

// AC-234・AC-235・AC-236・AC-237・AC-238・AC-239・AC-240・AC-246（#779）: 取り込めるものの形と、親子のまとまり。
func TestStatus_AC234_AC235_AC239_AC240_AC246_IngestableItemsWithChildren(t *testing.T) {
	parallelTools(t, false)
	ws := ingestableSetup(t, `"self_assignees":["someone"]`)
	ing := ingestableOf(t, runJSON(t, ws, "status"))
	if ing["error"] != nil {
		t.Fatalf("ingestable.error = %v, want null (AC-246)", ing["error"])
	}
	items := ing["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v, want only the parent (子は根に無い AC-239・方針に合わない #3 は無い AC-238)", items)
	}
	it := items[0].(map[string]any)
	if it["external_key"] != ingRepo+"#1" {
		t.Fatalf("item = %v, want %s#1 (AC-234)", it, ingRepo)
	}
	if got := keysOf2(it); strings.Join(got, ",") != "children,external_key,number,parent_challenge_id,parent_external_key,repo,title,url" { // AC-235
		t.Errorf("item keys = %v", got)
	}
	kids := it["children"].([]any)
	if len(kids) != 1 || kids[0].(map[string]any)["external_key"] != ingRepo+"#2" { // AC-240
		t.Fatalf("children = %v, want %s#2", kids, ingRepo)
	}
	if got := keysOf2(kids[0].(map[string]any)); strings.Join(got, ",") != "external_key,number,parent_external_key,title,url" {
		t.Errorf("child keys = %v", got)
	}

	// 課題にすると（AC-236）、親も子も取り込めるものに無い（子は課題の子として記録される。AC-237）。
	runJSON(t, ws, "ingest", ingRepo+"#1")
	if items := ingestableOf(t, runJSON(t, ws, "status"))["items"].([]any); len(items) != 0 {
		t.Fatalf("items after ingesting #1 = %v, want none (AC-236・AC-237)", items)
	}
}

// AC-243（#779）: 親がすでに課題になっているとき、parent_challenge_id は親の課題の ID。
func TestStatus_AC243_ParentChallengeID(t *testing.T) {
	parallelTools(t, false)
	ws := initializedWorkspace(t)
	writeSourcesDeclaration(t, ws, `{"version":1,"sources":[{"id":"ing-source","type":"github-issue","repos":["`+ingRepo+`"],"self_assignees":["someone"]}]}`)
	list := func(issues []fakeGHIssue) {
		withFakeGHRoutesOnPATH(t, []fakeGHRoute{fakeGHListRoute(ingRepo, 1, fakeGHIssueListBody(t, issues), 0)})
	}
	list([]fakeGHIssue{{Number: 1, Title: "parent", Repo: ingRepo}})
	runJSON(t, ws, "ingest", ingRepo+"#1")
	list([]fakeGHIssue{{Number: 1, Title: "parent", Repo: ingRepo}, {Number: 2, Title: "later child", Repo: ingRepo, ParentNumber: 1}})
	items := ingestableOf(t, runJSON(t, ws, "status"))["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["external_key"] != ingRepo+"#2" || items[0].(map[string]any)["parent_challenge_id"] != "C-1" {
		t.Fatalf("items = %v, want %s#2 with parent_challenge_id C-1", items, ingRepo)
	}
}

// AC-244・AC-245（#779）: 上流を読めない（gh が無い）とき、status は終了コード 0 で、ingestable.error は null でない。
func TestStatus_AC244_AC245_UpstreamUnavailableKeepsExitZeroAndSetsError(t *testing.T) {
	parallelTools(t, false)
	ws := initializedWorkspace(t)
	writeSourcesDeclaration(t, ws, validSourcesDeclaration)
	restrictToolDirs(t, 0)
	ing := ingestableOf(t, runJSON(t, ws, "status")) // runJSON は終了コード 0 を要求する（AC-244）
	if ing["error"] == nil {
		t.Errorf("ingestable.error = null, want the reason (AC-245)")
	}
	if items := ing["items"].([]any); len(items) != 0 {
		t.Errorf("items = %v, want none", items)
	}
}

// AC-248（#779）: self_assignees を解決できない取り込み元の Issue は items に無く、error は null でない。
func TestStatus_AC248_UnresolvedSelfAssigneesSetsError(t *testing.T) {
	parallelTools(t, false)
	ws := ingestableSetup(t, `"assignee_policy":"exclude-others"`) // self_assignees 省略＝gh api user で解決（偽の gh に経路が無い）
	ing := ingestableOf(t, runJSON(t, ws, "status"))
	if ing["error"] == nil || len(ing["items"].([]any)) != 0 {
		t.Errorf("ingestable = %v, want no items and an error", ing)
	}
}

// AC-249（#779）: 未のフォローアップが 2 件のとき followups.open は 2。AC-250・AC-251 は core のテストが直接確かめる。
func TestStatus_AC249_FollowupsOpen(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{Title: "a"})
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{Title: "b"})
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{Title: "done", Status: "done", Note: "n"})
	f, ok := runJSON(t, ws, "status")["followups"].(map[string]any)
	if !ok || f["open"] != 2.0 || len(f) != 1 {
		t.Fatalf("followups = %v, want {open: 2}", f)
	}
}

// --- 文の出力 ---

var statusTextHeadings = []string{
	"あなたの承認を待っているもの:", "あなたの回答を待っているもの:", "そのほかの、あなたの操作を待っているもの:",
	"取り込めるもの:", "サイクルを回すと進むもの", "オーケストレーターの番:", "外を待っているもの:", "承認済みの不可逆操作:", "未のフォローアップ",
}

// textSection は heading から次の見出しの直前までを返す（見出しが無ければ空）。
func textSection(text, heading string) string {
	i := strings.Index(text, heading)
	if i < 0 {
		return ""
	}
	rest := text[i+len(heading):]
	end := len(rest)
	for _, h := range statusTextHeadings {
		if j := strings.Index(rest, h); j >= 0 && j < end {
			end = j
		}
	}
	return rest[:end]
}

// AC-265・AC-266・AC-267・AC-268・AC-271・AC-272・AC-273・AC-274・AC-275・AC-277・AC-278・AC-279（#779）。
func TestStatusText_SectionsOfWhatCanBeAdvanced(t *testing.T) {
	parallelTools(t, true)
	ws := initializedWorkspace(t)
	plan := createWithStatusForCase(t, ws, "awaiting_plan_approval")
	completion := createWithStatusForCase(t, ws, "awaiting_completion_approval")
	holdID := classifiedChallenge(t, ws, "")
	runJSON(t, ws, "hold", holdID, "--question", "聞く")
	orchestrator := parentDirectHeld(t, ws, "")
	rel := prepareReleaseWorkspace(t, ws)
	opChallenge := createForCase(t, ws)
	pendingOp := rel.addPRRelease(t, opChallenge, "pending summary")
	approvedChallenge := createForCase(t, ws)
	approvedOp := rel.addPRRelease(t, approvedChallenge, "approved summary")
	code, pty, stdout, stderr := runConfirmedChild(t, []string{"approve", "--workspace", ws, "--json", approvedOp}, approvedOp)
	if code != 0 {
		t.Fatalf("approve exit=%d (stdout=%q stderr=%q pty=%q)", code, stdout, stderr, pty)
	}
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{Title: "f1"})
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{Title: "f2"})

	got := runText(t, ws, "status")
	if !strings.Contains(got, "いま進められること") { // AC-265
		t.Errorf("no heading いま進められること: %q", got)
	}
	if !strings.HasPrefix(got, "バイナリのコミット:") { // AC-274
		t.Errorf("no binary commit line: %q", got)
	}
	approval := textSection(got, "あなたの承認を待っているもの:")
	if !strings.Contains(approval, plan) || !strings.Contains(approval, completion) { // AC-266・AC-267
		t.Errorf("approval section = %q, want %s and %s", approval, plan, completion)
	}
	if answer := textSection(got, "あなたの回答を待っているもの:"); !strings.Contains(answer, holdID) || strings.Contains(answer, orchestrator) { // AC-268
		t.Errorf("answer section = %q, want %s only", answer, holdID)
	}
	if other := textSection(got, "そのほかの、あなたの操作を待っているもの:"); !strings.Contains(other, pendingOp) { // AC-272
		t.Errorf("other section = %q, want %s", other, pendingOp)
	}
	if o := textSection(got, "オーケストレーターの番:"); !strings.Contains(o, orchestrator) { // AC-275
		t.Errorf("orchestrator section = %q, want %s", o, orchestrator)
	}
	if a := textSection(got, "承認済みの不可逆操作:"); !strings.Contains(a, approvedOp) { // AC-277
		t.Errorf("approved section = %q, want %s", a, approvedOp)
	}
	if f := textSection(got, "未のフォローアップ"); !strings.Contains(f, "2 件") { // AC-271
		t.Errorf("followups line = %q, want 2 件", f)
	}
	if c := textSection(got, "サイクルを回すと進むもの"); !strings.Contains(c, "件") || strings.Contains(c, "なし") { // AC-278
		t.Errorf("cycle work line = %q, want a count", c)
	}
	for _, banned := range []string{"人間の操作", "親のエージェント"} { // AC-279
		if strings.Contains(got, banned) {
			t.Errorf("text has %q: %q", banned, got)
		}
	}
}

// AC-273（#779）: そのほかの操作を待っているものが無いとき、その見出しは出ない。
func TestStatusText_AC273_NoOtherHeadingWhenNothingWaitsForOtherOperations(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	createWithStatusForCase(t, ws, "awaiting_plan_approval")
	if got := runText(t, ws, "status"); strings.Contains(got, "そのほかの、あなたの操作を待っているもの") {
		t.Errorf("unexpected heading: %q", got)
	}
}

// AC-269・AC-270（#779）: 取り込めるものの番号・題名と、子の件数が同じ行に出る。
func TestStatusText_AC269_AC270_IngestableNumberTitleAndChildCount(t *testing.T) {
	parallelTools(t, false)
	ws := ingestableSetup(t, `"self_assignees":["someone"]`)
	section := textSection(runText(t, ws, "status"), "取り込めるもの:")
	var line string
	for _, l := range strings.Split(section, "\n") {
		if strings.Contains(l, "parent one") {
			line = l
		}
	}
	if !strings.Contains(line, "#1") || !strings.Contains(line, "子 1 件") {
		t.Errorf("section = %q, want a line with #1, the title and 子 1 件", section)
	}
}

// AC-276（#779）: チェックの完了を待っている課題の ID が「外を待っているもの」に出る。
func TestStatusText_AC276_WaitingExternalSection(t *testing.T) {
	parallelTools(t, false)
	ws := setupWorkspaceWithPosition(t)
	id := newVerifyingForCase(t, ws, true)
	withFakeGHRoutesOnPATH(t, ghChecksRoutes("open", checkRunsPending))
	if s := textSection(runText(t, ws, "status"), "外を待っているもの:"); !strings.Contains(s, id) {
		t.Errorf("waiting section = %q, want %s", s, id)
	}
}

// AC-280・AC-281・AC-282（#779）: あなたを待つもの・取り込めるもの・サイクルを回すと進むものが無ければ、その旨が出る。
// 未のフォローアップがあっても同じで、終了コードは 0（runText が 0 を要求する）。
func TestStatusText_AC280_AC281_AC282_NothingToAdvanceIsStated(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	const nothing = "いま進められるものはありません"
	if got := runText(t, ws, "status"); !strings.Contains(got, nothing) {
		t.Errorf("empty workspace text = %q, want %q", got, nothing)
	}
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{Title: "f"})
	got := runText(t, ws, "status")
	if !strings.Contains(got, nothing) || !strings.Contains(got, "1 件") {
		t.Errorf("text with only a followup = %q, want %q and the followup count", got, nothing)
	}
}

// AC-247（#779）: sources.json に取り込み元が 1 つも無い（`"sources": []`）とき、gh が無くても items は空・error は null
// （状況の API と同じ）。
func TestStatus_AC247_EmptySourcesDoesNotNeedUpstream(t *testing.T) {
	parallelTools(t, false)
	ws := initializedWorkspace(t)
	writeSourcesDeclaration(t, ws, `{"version":1,"sources":[]}`)
	restrictToolDirs(t, 0)
	ing := ingestableOf(t, runJSON(t, ws, "status"))
	if ing["error"] != nil || len(ing["items"].([]any)) != 0 {
		t.Errorf("ingestable = %v, want empty items and null error", ing)
	}
}

// 文書の形（status.ingestable.items[].children を含む）と実出力の照合。実際に子のある items で確かめる。
func TestStatus_AC230_DocumentedShapeWithIngestableChildren(t *testing.T) {
	parallelTools(t, false)
	ws := ingestableSetup(t, `"self_assignees":["me"]`)
	out := runJSON(t, ws, "status")
	items := ingestableOf(t, out)["items"].([]any)
	if len(items) == 0 || len(items[0].(map[string]any)["children"].([]any)) == 0 {
		t.Fatalf("fixture must have an item with children: %v", items)
	}
	assertDocumentedJSON(t, loadDocumentedJSON(t), "status", out)
}

// ingestableKeys は ingestable.items の根と、その children の external_key を全部返す。
func ingestableKeys(t *testing.T, doc map[string]any) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, e := range ingestableOf(t, doc)["items"].([]any) {
		it := e.(map[string]any)
		out[it["external_key"].(string)] = true
		for _, c := range it["children"].([]any) {
			out[c.(map[string]any)["external_key"].(string)] = true
		}
	}
	return out
}

// AC-162（#776 から引き継いだ確かめ。#779）: 子の記録を持つ課題の対応を外す（unbind）と、消した子の Issue が
// `flywheel status --json` の ingestable.items に出る（親も取り込めるものへ戻るので、子は親の children に入る）。
func TestStatus_AC162_ChildRemovedByUnbindIsIngestableAgain(t *testing.T) {
	parallelTools(t, false)
	ws := ingestableSetup(t, `"self_assignees":["someone"]`)
	child := ingRepo + "#2"
	runJSON(t, ws, "ingest", ingRepo+"#1")
	if kids := runJSON(t, ws, "show", "C-1")["source_children"].([]any); len(kids) != 1 {
		t.Fatalf("setup: source_children = %v, want the child recorded", kids)
	}
	if got := ingestableKeys(t, runJSON(t, ws, "status")); got[child] {
		t.Fatalf("before unbind: %s is in ingestable.items though it is recorded as a child: %v", child, got)
	}
	runJSON(t, ws, "unbind", "C-1")
	if got := ingestableKeys(t, runJSON(t, ws, "status")); !got[child] {
		t.Errorf("AC-162: after unbind, %s is not in ingestable.items: %v", child, got)
	}
}

func readDocForStatusTest(t *testing.T, rel ...string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(append([]string{repoRoot(t)}, rel...)...))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// AC-308（#779）: m1-core.md の status の形の説明に、ingestable・followups・cycle_work がある。
func TestSpec_AC308_M1CoreStatusShapeHasTheS2bKeys(t *testing.T) {
	t.Parallel()
	found := false
	for _, line := range strings.Split(readDocForStatusTest(t, "docs", "features", "m1-core.md"), "\n") {
		if !strings.HasPrefix(line, "`{\"needs_human\":") {
			continue
		}
		found = true
		for _, k := range []string{"\"ingestable\":", "\"followups\":", "\"cycle_work\":"} {
			if !strings.Contains(line, k) {
				t.Errorf("AC-308: the status shape in m1-core.md lacks %s", k)
			}
		}
	}
	if !found {
		t.Fatal("AC-308: m1-core.md has no line documenting the status shape")
	}
}

// AC-313（#779）: m4-ui-server.md の状況の API の説明に、ingestable=true の問い合わせがある。
func TestSpec_AC313_M4StatusAPIRowMentionsTheIngestableQuery(t *testing.T) {
	t.Parallel()
	found := false
	for _, line := range strings.Split(readDocForStatusTest(t, "docs", "features", "m4-ui-server.md"), "\n") {
		if !strings.HasPrefix(line, "| `GET /api/v1/workspaces/{name}/status") {
			continue
		}
		found = true
		if !strings.Contains(line, "ingestable=true") {
			t.Errorf("AC-313: the status API row lacks ingestable=true: %s", line)
		}
	}
	if !found {
		t.Fatal("AC-313: m4-ui-server.md has no row for the status API")
	}
}

// AC-320（#779）: CLAUDE.md のモジュール構成の規約に、internal/server が、状況の API の取り込めるもののために
// 上流の Issue を読む口を組み立ててよい旨がある。
func TestSpec_AC320_ClaudeMDAllowsServerToBuildTheUpstreamIssueSource(t *testing.T) {
	t.Parallel()
	found := false
	for _, line := range strings.Split(readDocForStatusTest(t, "CLAUDE.md"), "\n") {
		if !strings.Contains(line, "`internal/server` が `internal/adapters/github` を import してよいのは") {
			continue
		}
		found = true
		if !strings.Contains(line, "取り込めるもの") || !strings.Contains(line, "UpstreamIssueSource") {
			t.Errorf("AC-320: the import rule of internal/server does not allow building the upstream issue source: %s", line)
		}
	}
	if !found {
		t.Fatal("AC-320: CLAUDE.md has no import rule for internal/server")
	}
}
