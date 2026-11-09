package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// #485: 完了の承認まで済んだ PR が本番反映でマージできなくなったときの立て直し。
// D＝検証の後に head が動いても、既定ブランチの取り込みだけで CI が通っていれば承認し直せる。
// A＝それ以外の変更が入った・マージできない場合は、課題を完了から着手中へ戻して検証し直す。
// 偽の gh（PATH の先頭）の応答だけで動かし、本物の GitHub は読まない。

const (
	miMain = "1111111111111111111111111111111111111111" // 既定ブランチ側のコミット（取り込まれる側）
	miBase = "2222222222222222222222222222222222222222" // PR の枝の分かれ目（既定ブランチにもある）
	miPR   = "3333333333333333333333333333333333333333" // PR 側の通常のコミット
	miLast = "4444444444444444444444444444444444444444" // 取り込み以外のコミット
)

type miCommit struct {
	sha     string
	parents []string
	msg     string
}

func miCommitJSON(cs ...miCommit) string {
	var parts []string
	for _, c := range cs {
		var ps []string
		for _, p := range c.parents {
			ps = append(ps, fmt.Sprintf(`{"sha":%q}`, p))
		}
		parts = append(parts, fmt.Sprintf(`{"sha":%q,"parents":[%s],"commit":{"message":%q}}`, c.sha, strings.Join(ps, ","), c.msg))
	}
	return strings.Join(parts, ",")
}

// miCompare は compare の応答（status は ahead）。files は {名前: patch}（patch が空ならキー無し）の並び。
func miCompare(total int, commits []miCommit, files ...[2]string) string {
	var fs []string
	for _, f := range files {
		if f[1] == "" {
			fs = append(fs, fmt.Sprintf(`{"filename":%q}`, f[0]))
		} else {
			fs = append(fs, fmt.Sprintf(`{"filename":%q,"patch":%q}`, f[0], f[1]))
		}
	}
	return fmt.Sprintf(`{"status":"ahead","total_commits":%d,"commits":[%s],"files":[%s]}`, total, miCommitJSON(commits...), strings.Join(fs, ","))
}

func miCompareRoute(base, head, body string) fakeGHRoute {
	return fakeGHRoute{match: fmt.Sprintf("repos/o/r/compare/%s...%s?per_page=100&page=1", base, head), stdout: body}
}

// miRoutes は PR o/r#n（今の head が head）の偽の gh の応答。ci が空なら CI は成功 1 件、"fail" なら失敗 1 件、"none" なら 0 件。
func miRoutes(n int, head, ci string, mergeable string) []fakeGHRoute {
	checkRuns := `{"total_count":1,"check_runs":[{"name":"build","status":"completed","conclusion":"success"}]}`
	switch ci {
	case "fail":
		checkRuns = `{"total_count":1,"check_runs":[{"name":"build","status":"completed","conclusion":"failure"}]}`
	case "none":
		checkRuns = `{"total_count":0,"check_runs":[]}`
	}
	return []fakeGHRoute{
		{match: fmt.Sprintf("repos/o/r/commits/%s/check-runs?page=1&per_page=100", head), stdout: checkRuns},
		{match: fmt.Sprintf("repos/o/r/commits/%s/status?page=1&per_page=100", head), stdout: `{"state":"success","total_count":0,"statuses":[]}`},
		{match: fmt.Sprintf("repos/o/r/pulls/%d", n), stdout: fmt.Sprintf(
			`{"html_url":%q,"title":"PR","state":"open","merged_at":null,"base":{"ref":"main","sha":"b"},"head":{"sha":%q},"mergeable":%s}`, releasePRURL(n), head, mergeable)},
	}
}

// miMergeInRoutes は「検証の head V に既定ブランチを取り込んだマージ H が入った」履歴の compare の応答。
// a.go は衝突を直したファイル（両方の親と異なる）、b.go は取り込みで増えただけ（PR 側の親とだけ異なる）。
func miMergeInRoutes(v, h string) []fakeGHRoute {
	merge := miCommit{h, []string{v, miMain}, "Merge branch 'main' into feat"}
	mainSide := miCommit{miMain, []string{"0000000000000000000000000000000000000000"}, "main: change a.go"}
	return []fakeGHRoute{
		miCompareRoute(v, h, miCompare(2, []miCommit{mainSide, merge}, [2]string{"a.go", "@@ -1 +1 @@\n-old\n+resolved"}, [2]string{"b.go", "@@ -1 +1 @@\n-x\n+y"})),
		miCompareRoute("main", h, miCompare(3, []miCommit{{miBase, nil, "base"}, {v, []string{miBase}, "feat"}, merge})),
		miCompareRoute(miMain, h, miCompare(2, []miCommit{{v, []string{miBase}, "feat"}}, [2]string{"a.go", "@@ -2 +2 @@\n-m\n+resolved"}, [2]string{"c.go", "@@ c"})),
	}
}

func runCLIRaw(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(args, strings.NewReader(""), &out, &errOut, defaultCommands())
	return code, out.String(), errOut.String()
}

func relayedArgs(ws string, args ...string) []string {
	return append(append([]string{}, args...), "--workspace", ws, "--json", "--relayed", "--session", "s", "--quote", "q")
}

func decodeJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(s), &doc); err != nil {
		t.Fatalf("not JSON: %v (%q)", err, s)
	}
	return doc
}

// newMergeInFixture は完了確認待ちの課題に PR #1 の release を 1 件持たせ、J5 の検証の head を v に、今の head を h にする。
func newMergeInFixture(t *testing.T, routes []fakeGHRoute) (*completionFixture, string, string) {
	t.Helper()
	v, h := releaseHead(1, 0), releaseHead(1, 1)
	f := newCompletionFixture(t, map[int]string{1: h}, releasePRURL(1))
	f.verified(t, releasePRURL(1), v)
	withFakeGHRoutesOnPATH(t, routes)
	return f, v, h
}

func concatRoutes(rs ...[]fakeGHRoute) []fakeGHRoute {
	var out []fakeGHRoute
	for _, r := range rs {
		out = append(out, r...)
	}
	return out
}

// --- D: 完了の承認 ---

func TestMergeIn_RelayedCompletionApprovesTheMovedHeadAndPrintsWhatCameIn(t *testing.T) {
	v, h := releaseHead(1, 0), releaseHead(1, 1)
	f, _, _ := newMergeInFixture(t, concatRoutes(miRoutes(1, h, "", "true"), miMergeInRoutes(v, h)))

	code, stdout, stderr := runCLIRaw(t, relayedArgs(f.ws, "approve", f.id)...)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	doc := decodeJSON(t, stdout)
	rels := jsonList(t, doc, "releases")
	if len(rels) != 1 || rels[0]["head_sha"] != h {
		t.Fatalf("releases = %v, want the moved head %s approved", rels, h)
	}
	mi, _ := rels[0]["merge_in"].(map[string]any)
	if mi == nil || mi["kind"] != "merge_in_only" || mi["verified_head"] != v || mi["head"] != h || mi["checks_passed"] != true {
		t.Fatalf("merge_in = %v", mi)
	}
	commits, _ := mi["commits"].([]any)
	if len(commits) != 2 {
		t.Fatalf("merge_in.commits = %v, want the merge and the default-branch commit", commits)
	}
	roles := map[string]string{}
	for _, c := range commits {
		cm := c.(map[string]any)
		roles[cm["sha"].(string)] = cm["role"].(string)
	}
	if roles[h] != "merge_in" || roles[miMain] != "default_branch" {
		t.Errorf("roles = %v", roles)
	}
	fixes, _ := mi["conflict_fixes"].([]any)
	if len(fixes) != 1 {
		t.Fatalf("conflict_fixes = %v", fixes)
	}
	files := fixes[0].(map[string]any)["files"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["filename"] != "a.go" || !strings.Contains(files[0].(map[string]any)["patch"].(string), "+resolved") {
		t.Errorf("fixed files = %v, want only a.go (the file that differs from both parents)", files)
	}
	if got := f.state(t, f.ops[0]); got != "approved" {
		t.Errorf("release state = %s, want approved", got)
	}
	if head, ok := f.approvedHead(t, f.ops[0]); !ok || head != h {
		t.Errorf("approved head = %q (%v), want %s", head, ok, h)
	}
	// 承認の記録に、取り込みだけで承認したことと検証の head が残り、show で分かる。
	show := runJSON(t, f.ws, "show", f.id)
	var found bool
	for _, a := range jsonList(t, show, "approvals") {
		if a["kind"] == "release" && a["merge_in_verified_head"] == v {
			found = true
		}
	}
	if !found {
		t.Errorf("show.approvals lacks a release approval with merge_in_verified_head=%s: %v", v, show["approvals"])
	}
	if got := f.challengeStatus(t); got != "done" {
		t.Errorf("challenge status = %s, want done", got)
	}
}

func TestMergeIn_TerminalSummaryShowsTheVerifiedHeadTheCommitsAndTheFixedDiffBeforeTheConfirmation(t *testing.T) {
	v, h := releaseHead(1, 0), releaseHead(1, 1)
	f, _, _ := newMergeInFixture(t, concatRoutes(miRoutes(1, h, "", "true"), miMergeInRoutes(v, h)))
	code, ptyOutput, stdout, stderr := runConfirmedChild(t, []string{"approve", "--workspace", f.ws, "--json", f.id}, f.id)
	if code != 0 {
		t.Fatalf("exit=%d (stdout=%q stderr=%q pty=%q)", code, stdout, stderr, ptyOutput)
	}
	prompt := strings.Index(ptyOutput, confirmPrompt(f.id))
	for _, want := range []string{v, h[:12], miMain[:12], "Merge branch 'main' into feat", "取り込みのマージ", "既定ブランチ側", "a.go", "+resolved"} {
		if i := strings.Index(ptyOutput, want); i < 0 || i > prompt {
			t.Errorf("%q must be shown before the confirmation prompt (pos %d, prompt %d)", want, i, prompt)
		}
	}
	if strings.Contains(ptyOutput, "c.go") || strings.Contains(ptyOutput, "b.go") {
		t.Errorf("files that differ from only one parent must not be shown as the conflict fix: %q", ptyOutput)
	}
	assertFullHeadOnOwnLine(t, ptyOutput, h)
	assertFullHeadOnOwnLine(t, ptyOutput, v)
	if got := f.state(t, f.ops[0]); got != "approved" {
		t.Errorf("release state = %s, want approved", got)
	}
}

func TestMergeIn_NotApprovedUnlessMergeInOnlyAndCIPasses(t *testing.T) {
	v, h := releaseHead(1, 0), releaseHead(1, 1)
	cases := []struct {
		name   string
		routes []fakeGHRoute
		detail string
	}{
		{"CI failing", concatRoutes(miRoutes(1, h, "fail", "true"), miMergeInRoutes(v, h)), "CI が通っていません"},
		{"no checks at all", concatRoutes(miRoutes(1, h, "none", "true"), miMergeInRoutes(v, h)), "CI が通っていません"},
		{"commit that is not a merge", concatRoutes(miRoutes(1, h, "", "true"), []fakeGHRoute{
			miCompareRoute(v, h, miCompare(2, []miCommit{{miLast, []string{v}, "add feature"}, {h, []string{miLast, miMain}, "Merge"}})),
			miCompareRoute("main", h, miCompare(3, []miCommit{{v, []string{miBase}, "feat"}, {miLast, []string{v}, "add feature"}, {h, []string{miLast, miMain}, "Merge"}})),
		}), "取り込み以外のコミット"},
		{"comparison unavailable", miRoutes(1, h, "", "true"), "判定できませんでした"},
		{"commits truncated", concatRoutes(miRoutes(1, h, "", "true"), []fakeGHRoute{
			miCompareRoute(v, h, miCompare(400, []miCommit{{h, []string{v, miMain}, "Merge"}})),
		}), "判定できませんでした"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, _, _ := newMergeInFixture(t, tc.routes)
			code, stdout, stderr := runCLIRaw(t, relayedArgs(f.ws, "approve", f.id)...)
			if code != 0 {
				t.Fatalf("exit=%d stderr=%s (the completion approval itself still succeeds)", code, stderr)
			}
			doc := decodeJSON(t, stdout)
			ex := jsonList(t, doc, "excluded_releases")
			if len(ex) != 1 || ex[0]["reason"] != "verified_head_mismatch" || !strings.Contains(fmt.Sprint(ex[0]["detail"]), tc.detail) {
				t.Fatalf("excluded_releases = %v, want verified_head_mismatch with %q", ex, tc.detail)
			}
			if got := f.state(t, f.ops[0]); got != "pending" {
				t.Errorf("release state = %s, want pending", got)
			}
			if got := f.challengeStatus(t); got != "done" {
				t.Errorf("challenge status = %s, want done (a completion approval never reopens the challenge)", got)
			}
		})
	}
}

// 検証の head と一致していれば、取り込みの判定は要らない（GitHub の比較を読まない）。
func TestMergeIn_MatchingHeadNeedsNoComparison(t *testing.T) {
	h := releaseHead(1, 0)
	f := newCompletionFixture(t, map[int]string{1: h}, releasePRURL(1))
	f.verified(t, releasePRURL(1), h)
	calls := fakeGHReleaseHeads(t, map[int]string{1: h})
	doc := f.relayed(t, f.id)
	rels := jsonList(t, doc, "releases")
	if len(rels) != 1 || rels[0]["merge_in"] != nil {
		t.Fatalf("releases = %v, want an ordinary approval without merge_in", rels)
	}
	for _, c := range calls() {
		if strings.Contains(c, "/compare/") {
			t.Errorf("unexpected comparison call: %s", c)
		}
	}
}

// (d) 別の課題に付け直した release（op add で着手中でない課題に付けたもの）でも、同じ PR の検証の head と比べて同じ判定になる。
func TestMergeIn_ReleaseAttachedToAnotherChallengeIsAssessedAgainstThePRsVerifiedHead(t *testing.T) {
	v, h := releaseHead(1, 0), releaseHead(1, 1)
	f, _, _ := newMergeInFixture(t, concatRoutes(miRoutes(1, h, "", "true"), miMergeInRoutes(v, h)))
	// 検証の記録は元の課題 f.id の run にある。release は別の課題 C2 に付け直す。
	other := runJSON(t, f.ws, "create", "--title", "other", "--done-criteria", "x")["challenge"].(map[string]any)["id"].(string)
	rel := addRelease(t, f.ws, other, "ship again", releasePRURL(1))
	coretest.SetChallengeStatus(t, f.ws, challengeIDToInternalID(t, other), "awaiting_completion_approval")
	_ = f.ops

	code, stdout, stderr := runCLIRaw(t, relayedArgs(f.ws, "approve", other)...)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	rels := jsonList(t, decodeJSON(t, stdout), "releases")
	var got map[string]any
	for _, r := range rels {
		if r["operation_id"] == rel {
			got = r
		}
	}
	if got == nil || got["head_sha"] != h || got["merge_in"] == nil {
		t.Fatalf("releases = %v, want %s approved at the moved head as a merge-in", rels, rel)
	}
}

// --- D: 単独の承認と (ii) ---

func TestMergeIn_SingleOperationApprovalOfAMergeInHead(t *testing.T) {
	v, h := releaseHead(1, 0), releaseHead(1, 1)
	f, _, _ := newMergeInFixture(t, concatRoutes(miRoutes(1, h, "", "true"), miMergeInRoutes(v, h)))
	code, stdout, stderr := runCLIRaw(t, relayedArgs(f.ws, "approve", f.ops[0])...)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	doc := decodeJSON(t, stdout)
	mi, _ := doc["merge_in"].(map[string]any)
	if mi == nil || mi["verified_head"] != v || mi["head"] != h {
		t.Fatalf("merge_in = %v", mi)
	}
	if ap, _ := doc["approval"].(map[string]any); ap["merge_in_verified_head"] != v || !strings.HasPrefix(fmt.Sprint(ap["id"]), "AP-") {
		t.Errorf("approval = %v, want merge_in_verified_head=%s and an AP- id", ap, v)
	}
}

// 課題が完了で、検証の後に取り込み以外の変更が入った release を承認しようとすると、承認せず、課題を着手中へ戻す。
func TestMergeIn_SingleApprovalOfAContentChangeOnADoneChallengeReopensTheChallenge(t *testing.T) {
	v, h := releaseHead(1, 0), releaseHead(1, 1)
	routes := concatRoutes(miRoutes(1, h, "", "true"), []fakeGHRoute{
		miCompareRoute(v, h, miCompare(2, []miCommit{{miLast, []string{v}, "add feature"}, {h, []string{miLast, miMain}, "Merge"}})),
		miCompareRoute("main", h, miCompare(3, []miCommit{{v, []string{miBase}, "feat"}, {miLast, []string{v}, "add feature"}, {h, []string{miLast, miMain}, "Merge"}})),
	})
	f, _, _ := newMergeInFixture(t, routes)
	coretest.SetChallengeStatus(t, f.ws, challengeIDToInternalID(t, f.id), "done")

	code, _, stderr := runCLIRaw(t, relayedArgs(f.ws, "approve", f.ops[0])...)
	if code != 1 || !strings.Contains(stderr, `"upstream_unavailable"`) {
		t.Fatalf("exit=%d stderr=%s, want exit 1 with upstream_unavailable", code, stderr)
	}
	if !strings.Contains(stderr, "着手中へ戻し") {
		t.Errorf("stderr = %s, want it to say the challenge was moved back to in_progress", stderr)
	}
	if got := f.challengeStatus(t); got != "in_progress" {
		t.Errorf("challenge status = %s, want in_progress", got)
	}
	show := runJSON(t, f.ws, "show", f.id)
	old := findOperation(t, show, f.ops[0])
	if old["state"] != "closed" || old["closed_reason"] != "superseded" || old["superseded_by"] == nil {
		t.Errorf("old release = %v, want closed/superseded with a successor", old)
	}
	newOp := findOperation(t, show, old["superseded_by"].(string))
	if newOp["state"] != "pending" || newOp["supersedes"] != f.ops[0] || newOp["ref"] != releasePRURL(1) {
		t.Errorf("new release = %v", newOp)
	}
}

// 取得の失敗など、履歴が書き換わったと確かめられないときは、完了の課題でも戻さない。
func TestMergeIn_SingleApprovalWhenTheComparisonIsUnavailableDoesNotReopen(t *testing.T) {
	h := releaseHead(1, 1)
	f, _, _ := newMergeInFixture(t, miRoutes(1, h, "", "true"))
	coretest.SetChallengeStatus(t, f.ws, challengeIDToInternalID(t, f.id), "done")
	code, _, stderr := runCLIRaw(t, relayedArgs(f.ws, "approve", f.ops[0])...)
	if code != 1 || !strings.Contains(stderr, "verified_head_mismatch") {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	if got := f.challengeStatus(t); got != "done" {
		t.Errorf("challenge status = %s, want done", got)
	}
}

// --- show ---

func TestShow_PendingReleaseWhoseHeadMovedShowsWhatCameIn(t *testing.T) {
	v, h := releaseHead(1, 0), releaseHead(1, 1)
	f, _, _ := newMergeInFixture(t, concatRoutes(miRoutes(1, h, "", "true"), miMergeInRoutes(v, h)))
	doc := runJSON(t, f.ws, "show", f.id)
	changes := jsonList(t, doc, "release_head_changes")
	if len(changes) != 1 || changes[0]["operation_id"] != f.ops[0] {
		t.Fatalf("release_head_changes = %v", changes)
	}
	hc := changes[0]["head_change"].(map[string]any)
	if hc["kind"] != "merge_in_only" || hc["verified_head"] != v || hc["head"] != h {
		t.Errorf("head_change = %v", hc)
	}
	code, stdout, _ := runCLIRaw(t, "show", f.id, "--workspace", f.ws)
	if code != 0 || !strings.Contains(stdout, "検証の後に head が動いた承認待ちの release") || !strings.Contains(stdout, "+resolved") {
		t.Errorf("text show (exit %d) lacks the head change block:\n%s", code, stdout)
	}
}

func TestShow_NoHeadChangeKeyWhenTheHeadDidNotMove(t *testing.T) {
	h := releaseHead(1, 0)
	f := newCompletionFixture(t, map[int]string{1: h}, releasePRURL(1))
	f.verified(t, releasePRURL(1), h)
	doc := runJSON(t, f.ws, "show", f.id)
	if _, has := doc["release_head_changes"]; has {
		t.Errorf("release_head_changes = %v, want the key omitted", doc["release_head_changes"])
	}
}

// --- 本番反映の段（op exec） ---

func execOpsJSON(t *testing.T, ws string, args ...string) map[string]any {
	t.Helper()
	return runJSON(t, ws, append([]string{"op", "exec"}, args...)...)
}

func TestOpExec_NotMergeableOnADoneChallengeMovesItBackToInProgress(t *testing.T) {
	ws, cid, op := execWorkspace(t)
	routes := []fakeGHRoute{
		{match: "repos/o/r/commits/" + execHead + "/check-runs?page=1&per_page=100", stdout: `{"total_count":0,"check_runs":[]}`},
		{match: "repos/o/r/commits/" + execHead + "/status?page=1&per_page=100", stdout: `{"state":"success","total_count":0,"statuses":[]}`},
		{match: "repos/o/r/pulls/1", stdout: fmt.Sprintf(`{"html_url":%q,"title":"PR","state":"open","merged_at":null,"base":{"ref":"main"},"head":{"sha":%q},"mergeable":false}`, releasePRURL(1), execHead)},
	}
	withFakeGHRoutesOnPATH(t, routes)

	doc := execOpsJSON(t, ws)
	_, items, _ := execItems(t, doc)
	if len(items) != 1 {
		t.Fatalf("items = %v", items)
	}
	it := items[0].(map[string]any)
	if it["result"] != "not_mergeable" || it["reopen_reason"] != "not_mergeable" || it["new_operation_id"] == nil {
		t.Fatalf("item = %v, want not_mergeable with the challenge reopened", it)
	}
	if got := statusOfChallenge(t, ws, cid); got != "in_progress" {
		t.Errorf("challenge status = %s, want in_progress", got)
	}
	show := runJSON(t, ws, "show", cid)
	if o := findOperation(t, show, op); o["state"] != "closed" || o["closed_reason"] != "superseded" || o["superseded_by"] != it["new_operation_id"] {
		t.Errorf("old release = %v", o)
	}
	if o := findOperation(t, show, it["new_operation_id"].(string)); o["state"] != "pending" || o["supersedes"] != op {
		t.Errorf("new release = %v", o)
	}
}

func prMergedRoute(n int) []fakeGHRoute {
	return []fakeGHRoute{{match: fmt.Sprintf("repos/o/r/pulls/%d", n), stdout: fmt.Sprintf(
		`{"html_url":%q,"title":"PR","state":"closed","merged_at":"2026-10-23T00:00:00Z","base":{"ref":"main"},"head":{"sha":%q},"mergeable":null,"merge_commit_sha":"m"}`, releasePRURL(n), execHead)},
		{match: "repos/o/r/commits/" + execHead + "/check-runs?page=1&per_page=100", stdout: `{"total_count":0,"check_runs":[]}`},
		{match: "repos/o/r/commits/" + execHead + "/status?page=1&per_page=100", stdout: `{"state":"success","total_count":0,"statuses":[]}`}}
}

func TestOpExec_ClosesAPendingReleaseWhosePRWasMergedOutside(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	writePositionFileForTest(t, ws, "pos")
	writeAgentJSONForTest(t, ws, `{"position_file": "position.md"}`)
	declareReleaseRepo(t, ws)
	cid := createWithStatusForCase(t, ws, "done")
	opN := coretest.InsertReleaseOperation(t, ws, challengeIDToInternalID(t, cid), releasePRURL(1), "pending")
	op := fmt.Sprintf("OP-%d", opN)
	withFakeGHRoutesOnPATH(t, prMergedRoute(1))

	// ID を指定した op exec は承認待ちの release を読まない。
	if doc := execOpsJSON(t, ws, op); len(doc["phase"].(map[string]any)["tidied"].([]any)) != 0 {
		t.Errorf("explicit ID tidied = %v, want none", doc["phase"])
	}
	if o := findOperation(t, runJSON(t, ws, "show", cid), op); o["state"] != "pending" {
		t.Fatalf("state = %v, want pending after the explicit-ID run", o["state"])
	}

	doc := execOpsJSON(t, ws)
	tidied := doc["phase"].(map[string]any)["tidied"].([]any)
	if len(tidied) != 1 {
		t.Fatalf("tidied = %v", tidied)
	}
	td := tidied[0].(map[string]any)
	if td["action"] != "closed" || td["closed_reason"] != "merged_outside" || td["operation_id"] != op {
		t.Errorf("tidied[0] = %v", td)
	}
	o := findOperation(t, runJSON(t, ws, "show", cid), op)
	if o["state"] != "closed" || o["closed_reason"] != "merged_outside" || o["closed_at"] == nil {
		t.Errorf("operation = %v", o)
	}
	// 閉じた操作は承認も差し戻しもできない。
	requireErrorCode(t, relayedArgs(ws, "approve", op), 1, CodeInvalidTransition)
	requireErrorCode(t, append(relayedArgs(ws, "reject", op), "--reason", "x"), 1, CodeInvalidTransition)
}
