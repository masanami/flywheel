package github

// 偽の gh の実装。テストが PATH の先頭に置く実行ファイル「gh」は、
// このテストバイナリ自身を FLYWHEEL_FAKE_GH=1 付きで exec するだけの薄い
// シェルスクリプトであり（newFakeGHDir 参照）、TestMain がその環境変数を
// 見て「偽の gh として振る舞うモード」へ分岐する。シナリオの選択・ログ先・
// 補助パラメータはすべて環境変数で渡す（t.Setenv はプロセス全体に効くため、
// このパッケージのテストは t.Parallel を使わない）。

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	envFakeGHMode    = "FLYWHEEL_FAKE_GH"
	envScenario      = "FAKE_GH_SCENARIO"
	envLogFile       = "FAKE_GH_LOG"
	envPidFile       = "FAKE_GH_PID_FILE"
	envSleepSeconds  = "FAKE_GH_SLEEP_SECONDS"
	envGrandchildPid = "FAKE_GH_GRANDCHILD_PID_FILE"
)

func TestMain(m *testing.M) {
	if os.Getenv(envFakeGHMode) == "1" {
		os.Exit(runFakeGH(os.Args[1:]))
	}
	// AC-107: 既定の PATH を空のディレクトリにしてから走らせる。PATH を
	// 差し替え忘れたテストが New を呼んでも、本物の gh には到達しない
	// （見つからず ErrGHNotFound になる）。
	sandbox, err := os.MkdirTemp("", "flywheel-empty-path-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: mkdir temp: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("PATH", sandbox); err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: setenv PATH: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(sandbox)
	if sharedFakeGHDir != "" {
		_ = os.RemoveAll(sharedFakeGHDir)
	}
	os.Exit(code)
}

// runFakeGH は偽の gh 本体。os.Args[1:]（= 本物の gh に渡ったのと同じ argv）
// を FAKE_GH_LOG へ 1 行（JSON 配列）追記した上で、FAKE_GH_SCENARIO に応じた
// 応答を標準出力・標準エラー・終了コードとして返す。
func runFakeGH(argv []string) int {
	if logPath := os.Getenv(envLogFile); logPath != "" {
		appendInvocationLog(logPath, argv)
	}

	switch os.Getenv(envScenario) {
	case "branch_exists":
		return fakeGHStatusLine("HTTP/2.0 200 OK", `{"name":"feat/x"}`)
	case "branch_missing":
		return fakeGHStatusLine("HTTP/2.0 404 Not Found", notFoundBody())
	case "branch_http500":
		return fakeGHStatusLine("HTTP/2.0 500 Internal Server Error", `{"message":"boom"}`)
	case "pulls_three_states":
		fmt.Print(`[
{"html_url":"https://github.com/o/r/pull/1","title":"open one","state":"open","merged_at":null,"base":{"ref":"main"}},
{"html_url":"https://github.com/o/r/pull/2","title":"closed one","state":"closed","merged_at":null,"base":{"ref":"develop"}},
{"html_url":"https://github.com/o/r/pull/3","title":"merged one","state":"closed","merged_at":"2026-10-01T00:00:00Z","base":{"ref":"main"}}
]`)
		return 0
	case "pulls_two_pages":
		return fakeGHPullsTwoPages(argv)
	case "pulls_fail":
		fmt.Fprintln(os.Stderr, "gh: connection refused")
		return 1
	case "sleep":
		return fakeGHSleep()
	case "sleep_with_grandchild":
		return fakeGHSleepWithGrandchild()
	case "list_3_pages":
		return fakeGHList3Pages(argv)
	case "list_page_fails":
		return fakeGHListPageFails(argv)
	case "get_404_http11":
		return fakeGHStatusLine("HTTP/1.1 404 Not Found", notFoundBody())
	case "get_410_http2":
		return fakeGHStatusLine("HTTP/2 410 Gone", notFoundBody())
	case "get_404_http2_0":
		return fakeGHStatusLine("HTTP/2.0 404 Not Found", notFoundBody())
	case "get_500_decoy_text":
		return fakeGHGetDecoy500()
	case "get_exit_nonzero_no_status_line":
		fmt.Fprintln(os.Stderr, "gh: connection refused (this text intentionally omits any HTTP status line)")
		return 1
	case "get_closed_issue":
		return fakeGHStatusLine("HTTP/2.0 200 OK", closedIssueBody())
	case "get_open_issue_full_fields":
		return fakeGHStatusLine("HTTP/2.0 200 OK", fullFieldsIssueBody())
	case "get_redirected_other_repo":
		// 改名・移管のリダイレクトを gh が辿った後の応答（要求と別の repo）。
		return fakeGHStatusLine("HTTP/2.0 200 OK", strings.Replace(closedIssueBody(), "repos/masanami/flywheel", "repos/someone/elsewhere", 1))
	case "get_wrong_number":
		return fakeGHStatusLine("HTTP/2.0 200 OK", closedIssueBody())
	case "list_other_repo":
		fmt.Print("[" + strings.Replace(issueElement(1), "repos/masanami/flywheel", "repos/masanami/renamed", 1) + "]")
		return 0
	case "current_login_env_check":
		// 着色・端末の強制を打ち消した環境で起動されていることを確かめる。
		if os.Getenv("GH_FORCE_TTY") != "" || (os.Getenv("CLICOLOR_FORCE") != "" && os.Getenv("CLICOLOR_FORCE") != "0") || os.Getenv("NO_COLOR") == "" {
			fmt.Fprintf(os.Stderr, "fake gh: color/tty forcing leaked: GH_FORCE_TTY=%q CLICOLOR_FORCE=%q NO_COLOR=%q\n", os.Getenv("GH_FORCE_TTY"), os.Getenv("CLICOLOR_FORCE"), os.Getenv("NO_COLOR"))
			return 1
		}
		fmt.Print(`{"login":"masanami"}`)
		return 0
	case "current_login_ok":
		fmt.Print(`{"login":"masanami"}`)
		return 0
	case "current_login_fail_empty":
		fmt.Print(`{"login":""}`)
		return 0
	case "current_login_fail_exit":
		fmt.Fprintln(os.Stderr, "gh: not authenticated")
		return 1
	case "thread_comments_paginated":
		return fakeGHThreadCommentsPaginated(argv)
	case "context_ac88_bare":
		return fakeGHContextScenario(argv, contextFixture{bodies: map[string]string{
			"masanami/flywheel#100": "See #50 for details.",
			"masanami/flywheel#50":  "target body bare",
		}})
	case "context_ac88_ownername":
		return fakeGHContextScenario(argv, contextFixture{bodies: map[string]string{
			"masanami/flywheel#100": "See other/repo#7 for details.",
			"other/repo#7":          "target body ownername",
		}})
	case "context_ac88_url":
		return fakeGHContextScenario(argv, contextFixture{bodies: map[string]string{
			"masanami/flywheel#100": "See https://github.com/third/repo/issues/9 for details.",
			"third/repo#9":          "target body url",
		}})
	case "context_ac89_six_refs":
		return fakeGHContextScenario(argv, contextFixture{bodies: map[string]string{
			"masanami/flywheel#100": "#201 #202 #203 #204 #205 #206",
			"masanami/flywheel#201": "b201",
			"masanami/flywheel#202": "b202",
			"masanami/flywheel#203": "b203",
			"masanami/flywheel#204": "b204",
			"masanami/flywheel#205": "b205",
			"masanami/flywheel#206": "b206",
		}})
	case "context_ac90_depth_one":
		return fakeGHContextScenario(argv, contextFixture{bodies: map[string]string{
			"masanami/flywheel#100": "#401",
			"masanami/flywheel#401": "see also #402",
			"masanami/flywheel#402": "grandchild body",
		}})
	case "context_reference_is_pull_request":
		// self-review 指摘の修正確認（round2 で判明: #601 の本文を bodies に
		// 登録し忘れていたため、fakeGHContextScenario が PR 判定へ届く前に
		// 404 を返し、GetReferencedIssue の PR 除外処理を無効化しても
		// テストが通ってしまっていた＝恒真のテスト）。#601 に 200 の応答
		// （pull_request キー付き）を用意し、PR 判定の分岐を実際に通す。
		return fakeGHContextScenario(argv, contextFixture{
			bodies: map[string]string{
				"masanami/flywheel#100": "#601",
				"masanami/flywheel#601": "this is a pull request body",
			},
			pullRequestNumbers: map[int]bool{601: true},
		})
	default:
		fmt.Fprintf(os.Stderr, "fake gh: unknown scenario %q\n", os.Getenv(envScenario))
		return 1
	}
}

func appendInvocationLog(logPath string, argv []string) {
	b, err := json.Marshal(argv)
	if err != nil {
		return
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "%s\n", b)
}

func fakeGHSleep() int {
	if pidFile := os.Getenv(envPidFile); pidFile != "" {
		_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o644)
	}
	seconds := 30
	if s := os.Getenv(envSleepSeconds); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			seconds = n
		}
	}
	sleepSeconds(seconds)
	return 0
}

// fakeGHSleepWithGrandchild は、標準出力・標準エラーを引き継いだ孫プロセス
// （このテストバイナリ自身を scenario=sleep で再 exec したもの。pid を
// FAKE_GH_GRANDCHILD_PID_FILE に書く）を起動してから自分も眠る。gh 本体
// だけを殺して孫を残す実装だと、孫が残るうえ、孫が出力のパイプを握ったまま
// なので呼び出しが WaitDelay まで返らない。それをテストで検出するための
// シナリオ。
func fakeGHSleepWithGrandchild() int {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake gh: os.Executable: %v\n", err)
		return 1
	}
	child := exec.Command(self)
	child.Env = append(os.Environ(), envScenario+"=sleep", envPidFile+"="+os.Getenv(envGrandchildPid))
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "fake gh: start grandchild: %v\n", err)
		return 1
	}
	return fakeGHSleep()
}

func fakeGHStatusLine(statusLine, body string) int {
	fmt.Printf("%s\r\nContent-Type: application/json; charset=utf-8\r\n\r\n%s", statusLine, body)
	return 1
}

func fakeGHGetDecoy500() int {
	fmt.Printf("HTTP/2.0 500 Internal Server Error\r\nContent-Type: application/json; charset=utf-8\r\n\r\n{\"message\":\"Not Found\"}")
	fmt.Fprintln(os.Stderr, "gh: Not Found (decoy stderr text; must not be matched by string search)")
	return 1
}

// fakeGHList3Pages は argv の末尾（api の引数）から page=N を読み取り、
// page1=100 件（PR無し）・page2=100 件（issue 60 + PR 40 の raw 100 件。
// ページ境界判定が「除外前の件数」であることを確かめるため意図的に PR を
// 混在させる）・page3=50 件（PR無し）を返す。page4 以降が呼ばれたら
// テストがそれを検出できるよう失敗で終わる。
func fakeGHList3Pages(argv []string) int {
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "fake gh: list_3_pages: no endpoint given")
		return 1
	}
	endpoint := argv[len(argv)-1]
	page := parsePageParam(endpoint)

	var elements []string
	switch page {
	case 1:
		elements = issueElements(1, 100)
	case 2:
		elements = append(issueElements(201, 60), pullRequestElements(301, 40)...)
	case 3:
		elements = issueElements(401, 50)
	default:
		fmt.Fprintf(os.Stderr, "fake gh: list_3_pages: unexpected page %d (endpoint %q)\n", page, endpoint)
		return 1
	}

	fmt.Print("[" + strings.Join(elements, ",") + "]")
	return 0
}

// fakeGHListPageFails は 1 ページ目に 100 件を返し、2 ページ目以降で失敗する
// （途中まで取得できた分を部分結果として返さないことを検証するため）。
func fakeGHListPageFails(argv []string) int {
	if len(argv) > 0 && parsePageParam(argv[len(argv)-1]) == 1 {
		fmt.Print("[" + strings.Join(issueElements(1, 100), ",") + "]")
		return 0
	}
	fmt.Fprintln(os.Stderr, "gh: simulated failure fetching the issue list")
	return 1
}

// parsePageParam は endpoint（例:
// "repos/masanami/flywheel/issues?state=open&per_page=100&page=2"）から
// クエリパラメータ page の値を取り出す。素朴な文字列探索だと
// "per_page=100" の中の "page=100" を誤って拾うため、net/url でクエリを
// 解析する。
func parsePageParam(endpoint string) int {
	u, err := url.Parse(endpoint)
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(u.Query().Get("page"))
	if err != nil {
		return -1
	}
	return n
}

func issueElement(number int) string {
	return fmt.Sprintf(`{"number":%d,"title":"issue %d","body":"body %d","user":{"login":"reporter"},"assignees":[],"labels":[],"state":"open","html_url":"https://github.com/masanami/flywheel/issues/%d","repository_url":"https://api.github.com/repos/masanami/flywheel"}`, number, number, number, number)
}

func issueElements(start, count int) []string {
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, issueElement(start+i))
	}
	return out
}

func pullRequestElement(number int) string {
	return fmt.Sprintf(`{"number":%d,"title":"pr %d","body":"body %d","user":{"login":"reporter"},"assignees":[],"labels":[],"state":"open","html_url":"https://github.com/masanami/flywheel/pull/%d","repository_url":"https://api.github.com/repos/masanami/flywheel","pull_request":{"url":"https://api.github.com/repos/masanami/flywheel/pulls/%d"}}`, number, number, number, number, number)
}

func pullRequestElements(start, count int) []string {
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, pullRequestElement(start+i))
	}
	return out
}

func notFoundBody() string {
	return `{"message":"Not Found","documentation_url":"https://docs.github.com/rest"}`
}

func closedIssueBody() string {
	return `{"number":7,"title":"closed one","body":"b","user":{"login":"reporter"},"assignees":[],"labels":[],"state":"closed","html_url":"https://github.com/masanami/flywheel/issues/7","repository_url":"https://api.github.com/repos/masanami/flywheel"}`
}

func fullFieldsIssueBody() string {
	return `{"number":49,"title":"full fields","body":"body text","user":{"login":"Masanami"},"assignees":[{"login":"masanami"},{"login":"someone-else"}],"labels":[{"name":"priority:high"},"needs-triage"],"state":"open","html_url":"https://github.com/masanami/flywheel/issues/49","repository_url":"https://api.github.com/repos/masanami/flywheel"}`
}

// --- #80: 上流のスレッド・参照先 Issue のシナリオ ---
//
// gh の 1 回の呼び出しの argv は、Issue 1 件の取得（-i 付き）か、一覧・
// コメントのページ取得（-i 無し）のいずれか（Client.GetIssue・
// Client.listAllComments・ListOpenIssues が渡す形。thread.go・issues.go 参照）。
// issueEndpointRe・commentsEndpointRe でその 2 種類を判別し、repo・number・
// page を取り出す。

var (
	issueEndpointRe    = regexp.MustCompile(`^repos/(.+)/issues/(\d+)$`)
	commentsEndpointRe = regexp.MustCompile(`^repos/(.+)/issues/(\d+)/comments\?`)
)

// lastEndpointAndDashI は argv から、gh api の endpoint 引数と、-i が付いて
// いたか（Issue 1 件の取得）を取り出す。
func lastEndpointAndDashI(argv []string) (endpoint string, hasDashI bool) {
	if len(argv) >= 3 && argv[1] == "-i" {
		return argv[2], true
	}
	if len(argv) >= 2 {
		return argv[1], false
	}
	return "", false
}

func commentElement(n int) string {
	return fmt.Sprintf(`{"body":"comment %d","user":{"login":"commenter"},"created_at":"2026-09-25T08:00:00Z","html_url":"https://github.com/masanami/flywheel/issues/100#issuecomment-%d"}`, n, n)
}

func commentElements(start, count int) []string {
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, commentElement(start+i))
	}
	return out
}

// threadSelfIssueBody は "thread_comments_paginated" シナリオの Issue 本体の
// 応答（参照は含まない。コメントのページ送りだけを検証する）。
func threadSelfIssueBody() string {
	return `{"number":100,"title":"thread test","body":"no references here","user":{"login":"reporter"},"assignees":[],"labels":[],"state":"open","html_url":"https://github.com/masanami/flywheel/issues/100","repository_url":"https://api.github.com/repos/masanami/flywheel"}`
}

// fakeGHThreadCommentsPaginated は Client.GetIssueThread を直接検証するための
// シナリオ: Issue 本体は 1 回、コメントは 2 ページ（100 件・30 件、計 130 件）
// に分けて返す。ページ境界判定は ListOpenIssues と同じ（perPage 未満で終了）。
func fakeGHThreadCommentsPaginated(argv []string) int {
	endpoint, hasDashI := lastEndpointAndDashI(argv)
	if hasDashI {
		return fakeGHStatusLine("HTTP/2.0 200 OK", threadSelfIssueBody())
	}
	switch parsePageParam(endpoint) {
	case 1:
		fmt.Print("[" + strings.Join(commentElements(1, 100), ",") + "]")
	case 2:
		fmt.Print("[" + strings.Join(commentElements(101, 30), ",") + "]")
	default:
		fmt.Fprintf(os.Stderr, "fake gh: thread_comments_paginated: unexpected comments endpoint %q\n", endpoint)
		return 1
	}
	return 0
}

// contextFixture は core.FetchUpstreamContext を実物の Client 経由で検証する
// シナリオが使う、"<repo>#<number>" -> 本文 の固定表。表に無いキーへの要求は
// 404 として応答する（打ち間違い・想定外の要求を検出しやすくする）。
type contextFixture struct {
	bodies map[string]string
	// pullRequestNumbers は、応答に pull_request キーを持たせる Issue 番号
	// （self-review 指摘の修正確認用: 参照先が Pull Request の場合に
	// GetReferencedIssue がスキップとして扱うことを検証するシナリオが使う）。
	pullRequestNumbers map[int]bool
}

func (f contextFixture) issueResponse(repo string, number int) int {
	key := fmt.Sprintf("%s#%d", repo, number)
	body, ok := f.bodies[key]
	if !ok {
		return fakeGHStatusLine("HTTP/2.0 404 Not Found", notFoundBody())
	}
	payload := map[string]any{
		"number":         number,
		"title":          "t",
		"body":           body,
		"user":           map[string]string{"login": "reporter"},
		"assignees":      []any{},
		"labels":         []any{},
		"state":          "open",
		"html_url":       fmt.Sprintf("https://github.com/%s/issues/%d", repo, number),
		"repository_url": fmt.Sprintf("https://api.github.com/repos/%s", repo),
	}
	if f.pullRequestNumbers[number] {
		payload["pull_request"] = map[string]string{"url": fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d", repo, number)}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake gh: context scenario: marshal issue %s#%d: %v\n", repo, number, err)
		return 1
	}
	return fakeGHStatusLine("HTTP/2.0 200 OK", string(raw))
}

// fakeGHContextScenario は fx.bodies から Issue 1 件の取得に応答し、コメントの
// 取得は常に 0 件（page1 で空配列）を返す（AC-88・AC-89・AC-90 のシナリオは
// コメントの内容を必要としない。コメントのページ送り自体は
// "thread_comments_paginated" が別に検証する）。
func fakeGHContextScenario(argv []string, fx contextFixture) int {
	endpoint, hasDashI := lastEndpointAndDashI(argv)
	if hasDashI {
		m := issueEndpointRe.FindStringSubmatch(endpoint)
		if m == nil {
			fmt.Fprintf(os.Stderr, "fake gh: context scenario: cannot parse issue endpoint %q\n", endpoint)
			return 1
		}
		number, err := strconv.Atoi(m[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake gh: context scenario: bad issue number in %q\n", endpoint)
			return 1
		}
		return fx.issueResponse(m[1], number)
	}

	m := commentsEndpointRe.FindStringSubmatch(endpoint)
	if m == nil {
		fmt.Fprintf(os.Stderr, "fake gh: context scenario: cannot parse comments endpoint %q\n", endpoint)
		return 1
	}
	if page := parsePageParam(endpoint); page != 1 {
		fmt.Fprintf(os.Stderr, "fake gh: context scenario: unexpected comments page %d for %q\n", page, endpoint)
		return 1
	}
	fmt.Print("[]")
	return 0
}
