package cli

import (
	"fmt"
	"testing"
)

// このファイルは approve の ① の head の取得（M4.5 S1）のテストが使う、偽の gh と宣言のフィクスチャ。

const releaseConnectorsFixture = `{
  "version": 1,
  "connectors": [
    {"id": "harness", "form": "plugin", "permission_mode": "acceptEdits", "operations": [
      {"id": "impl-op", "invocation": "/h:impl {issue_number}", "interactive": false, "child_may_decide": true, "artifacts": "pr"}
    ]}
  ],
  "repos": [
    {"name": "r-repo", "remote": "o/r", "default_branch": "main", "connector": "harness"}
  ]
}`

// releasePRURL は o/r の PR #n の URL。
func releasePRURL(n int) string { return fmt.Sprintf("https://github.com/o/r/pull/%d", n) }

// releaseHead は PR #n の偽の gh が返す head（40 文字）。gen を変えると別の head になる。
// 先頭 12 文字（短縮形）が (n, gen) ごとに違い、完全形の残りにも他の head にも現れない形にする。
func releaseHead(n, gen int) string { return fmt.Sprintf("%06x%06x%028x", 0xabc000+n, 0xdef000+gen, 1) }

// declareReleaseRepo は repos[].remote に o/r を持つ connectors.json を置く。
func declareReleaseRepo(t *testing.T, ws string) {
	t.Helper()
	writeConnectorsJSONForTest(t, ws, releaseConnectorsFixture)
}

// fakeGHReleaseHeads は PR #n（n は heads のキー）の head を返す偽の gh を PATH の先頭に置く。
// 値が空の PR は取得に失敗する（gh api が終了コード 1）。
func fakeGHReleaseHeads(t *testing.T, heads map[int]string) func() []string {
	t.Helper()
	var routes []fakeGHRoute
	for n, head := range heads {
		if head == "" {
			routes = append(routes, fakeGHRoute{match: fmt.Sprintf("repos/o/r/pulls/%d", n), exit: 1})
			continue
		}
		routes = append(routes,
			fakeGHRoute{match: fmt.Sprintf("repos/o/r/commits/%s/check-runs?page=1&per_page=100", head), stdout: `{"total_count":0,"check_runs":[]}`},
			fakeGHRoute{match: fmt.Sprintf("repos/o/r/commits/%s/status?page=1&per_page=100", head), stdout: `{"state":"success","total_count":0,"statuses":[]}`})
		routes = append(routes, fakeGHRoute{
			match:  fmt.Sprintf("repos/o/r/pulls/%d", n),
			stdout: fmt.Sprintf(`{"html_url":%q,"title":"PR","state":"open","merged_at":null,"base":{"ref":"main"},"head":{"sha":%q}}`, releasePRURL(n), head),
		})
	}
	return withFakeGHRoutesOnPATH(t, routes)
}

// addRelease は課題に ref つきの release を足し、その ID を返す。
func addRelease(t *testing.T, ws, challengeID, summary, ref string) string {
	t.Helper()
	args := []string{"op", "add", challengeID, "--kind", "release", "--summary", summary}
	if ref != "" {
		args = append(args, "--ref", ref)
	}
	return runJSON(t, ws, args...)["operation"].(map[string]any)["id"].(string)
}

// releaseWorkspace は、宣言と偽の gh（PR #1〜#300 の head を返す）を用意したワークスペースと、
// そのワークスペースで次に参照する PR の番号を持つ。値はテストごと（ワークスペースごと）で、
// パッケージ変数に置かないので t.Parallel のテストの間で共有しない。
type releaseWorkspace struct {
	ws     string
	lastPR int
}

// prepareReleaseWorkspace は ws に宣言と偽の gh を用意し、PR を参照に持つ release を足すための値を返す。
func prepareReleaseWorkspace(t *testing.T, ws string) *releaseWorkspace {
	t.Helper()
	declareReleaseRepo(t, ws)
	heads := map[int]string{}
	for n := 1; n <= 300; n++ {
		heads[n] = releaseHead(n, 0)
	}
	fakeGHReleaseHeads(t, heads)
	return &releaseWorkspace{ws: ws}
}

// addPRRelease は課題に、宣言のリポジトリ（o/r）の PR を参照に持つ release を足し、その ID を返す。
// PR の番号はこのワークスペースの中で 1, 2, 3, …（300 の次は 1）の順に進む。
func (r *releaseWorkspace) addPRRelease(t *testing.T, challengeID, summary string) string {
	t.Helper()
	r.lastPR = r.lastPR%300 + 1
	return addRelease(t, r.ws, challengeID, summary, releasePRURL(r.lastPR))
}
