package github

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

func newTestClient(t *testing.T, ghDir string) *Client {
	t.Helper()
	t.Setenv("PATH", ghDir)
	c, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// TestListOpenIssues_PaginatesAllThreePagesAndExcludesPullRequests は
// 完了条件「一覧は全ページを取得する（件数の上限で打ち切らない）」と
// 「pull_request キーを持つ要素を除く」、および「ページ境界判定は除外前の
// 件数で行う」を検証する。page2 は raw 100 件（issue 60 + PR 40）にして
// あり、フィルタ後の件数（60）で終了判定していれば page3 が呼ばれない
// バグを検出できる。
func TestListOpenIssues_PaginatesAllThreePagesAndExcludesPullRequests(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "list_3_pages")
	t.Setenv(envLogFile, logPath)

	c := newTestClient(t, dir)
	issues, err := c.ListOpenIssues(context.Background(), "masanami/flywheel")
	if err != nil {
		t.Fatalf("ListOpenIssues: %v", err)
	}

	wantTotal := 100 + 60 + 50
	if len(issues) != wantTotal {
		t.Fatalf("len(issues) = %d, want %d", len(issues), wantTotal)
	}

	calls := readInvocationLog(t, logPath)
	if len(calls) != 3 {
		t.Fatalf("gh was invoked %d times, want exactly 3 (one per page)", len(calls))
	}

	for _, issue := range issues {
		if issue.Repo != "masanami/flywheel" {
			t.Fatalf("issue.Repo = %q", issue.Repo)
		}
	}

	requireOnlyGETCalls(t, logPath)
}

func TestListOpenIssues_PageFetchFailure_ReturnsNoPartialResults(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "list_page_fails")
	t.Setenv(envLogFile, logPath)

	c := newTestClient(t, dir)
	issues, err := c.ListOpenIssues(context.Background(), "masanami/flywheel")
	if err == nil {
		t.Fatal("want error when a page fetch fails")
	}
	if issues != nil {
		t.Fatalf("want nil issues on failure (page 1 had 100 issues; none may be returned), got %d", len(issues))
	}
	if calls := readInvocationLog(t, logPath); len(calls) != 2 {
		t.Fatalf("want 2 calls (page 1 ok, page 2 fails), got %d", len(calls))
	}
}

func TestGetIssue_404_HTTP1_1_ReturnsErrUpstreamIssueNotFound(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "get_404_http11")
	t.Setenv(envLogFile, logPath)

	c := newTestClient(t, dir)
	_, err := c.GetIssue(context.Background(), "masanami/flywheel", 999999)
	if !errors.Is(err, core.ErrUpstreamIssueNotFound) {
		t.Fatalf("want errors.Is(err, core.ErrUpstreamIssueNotFound), got %v", err)
	}
	requireOnlyGETCalls(t, logPath)
}

func TestGetIssue_410_HTTP2_ReturnsErrUpstreamIssueNotFound(t *testing.T) {
	dir := newFakeGHDir(t)
	t.Setenv(envScenario, "get_410_http2")

	c := newTestClient(t, dir)
	_, err := c.GetIssue(context.Background(), "masanami/flywheel", 1)
	if !errors.Is(err, core.ErrUpstreamIssueNotFound) {
		t.Fatalf("want errors.Is(err, core.ErrUpstreamIssueNotFound), got %v", err)
	}
}

func TestGetIssue_404_HTTP2_0_ReturnsErrUpstreamIssueNotFound(t *testing.T) {
	dir := newFakeGHDir(t)
	t.Setenv(envScenario, "get_404_http2_0")

	c := newTestClient(t, dir)
	_, err := c.GetIssue(context.Background(), "masanami/flywheel", 1)
	if !errors.Is(err, core.ErrUpstreamIssueNotFound) {
		t.Fatalf("want errors.Is(err, core.ErrUpstreamIssueNotFound), got %v", err)
	}
}

// TestGetIssue_500WithDecoyNotFoundText_IsNotClassifiedAsNotFound は
// 「エラー文言・stderr の部分一致で判定しない」ことを固定する: 500 応答の
// 本文・stderr に "Not Found" という文言が含まれていても、ステータス行が
// 500 である限り core.ErrUpstreamIssueNotFound には一致しない。
func TestGetIssue_500WithDecoyNotFoundText_IsNotClassifiedAsNotFound(t *testing.T) {
	dir := newFakeGHDir(t)
	t.Setenv(envScenario, "get_500_decoy_text")

	c := newTestClient(t, dir)
	_, err := c.GetIssue(context.Background(), "masanami/flywheel", 1)
	if err == nil {
		t.Fatal("want error for http 500")
	}
	if errors.Is(err, core.ErrUpstreamIssueNotFound) {
		t.Fatalf("want NOT errors.Is(err, core.ErrUpstreamIssueNotFound) for a decoy 500, got %v", err)
	}
}

// TestGetIssue_NonZeroExitWithoutStatusLine_IsNotClassifiedAsNotFound は
// 有効なステータス行が全く読めない失敗（ネットワークエラー等）も
// NotFound に分類されないことを確かめる。
func TestGetIssue_NonZeroExitWithoutStatusLine_IsNotClassifiedAsNotFound(t *testing.T) {
	dir := newFakeGHDir(t)
	t.Setenv(envScenario, "get_exit_nonzero_no_status_line")

	c := newTestClient(t, dir)
	_, err := c.GetIssue(context.Background(), "masanami/flywheel", 1)
	if err == nil {
		t.Fatal("want error")
	}
	if errors.Is(err, core.ErrUpstreamIssueNotFound) {
		t.Fatalf("want NOT errors.Is(err, core.ErrUpstreamIssueNotFound), got %v", err)
	}
}

func TestGetIssue_Closed_ReturnsStateClosed(t *testing.T) {
	dir := newFakeGHDir(t)
	t.Setenv(envScenario, "get_closed_issue")

	c := newTestClient(t, dir)
	issue, err := c.GetIssue(context.Background(), "masanami/flywheel", 7)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.State != "closed" {
		t.Errorf("State = %q, want closed", issue.State)
	}
}

func TestGetIssue_OpenIssue_MapsAllFieldsThroughRealGHInvocation(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "get_open_issue_full_fields")
	t.Setenv(envLogFile, logPath)

	c := newTestClient(t, dir)
	issue, err := c.GetIssue(context.Background(), "masanami/flywheel", 49)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.ExternalKey != "masanami/flywheel#49" {
		t.Errorf("ExternalKey = %q", issue.ExternalKey)
	}
	if issue.Reporter != "Masanami" {
		t.Errorf("Reporter = %q", issue.Reporter)
	}
	if len(issue.Assignees) != 2 {
		t.Errorf("Assignees = %v", issue.Assignees)
	}
	if len(issue.Labels) != 2 {
		t.Errorf("Labels = %v", issue.Labels)
	}
	requireOnlyGETCalls(t, logPath)
}

func TestCurrentLogin_Success(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "current_login_ok")
	t.Setenv(envLogFile, logPath)

	c := newTestClient(t, dir)
	login, err := c.CurrentLogin(context.Background())
	if err != nil {
		t.Fatalf("CurrentLogin: %v", err)
	}
	if login != "masanami" {
		t.Errorf("login = %q, want masanami", login)
	}
	requireOnlyGETCalls(t, logPath)
}

func TestCurrentLogin_EmptyLogin_Fails(t *testing.T) {
	dir := newFakeGHDir(t)
	t.Setenv(envScenario, "current_login_fail_empty")

	c := newTestClient(t, dir)
	_, err := c.CurrentLogin(context.Background())
	if err == nil {
		t.Fatal("want error for empty login")
	}
}

func TestCurrentLogin_NonZeroExit_Fails(t *testing.T) {
	dir := newFakeGHDir(t)
	t.Setenv(envScenario, "current_login_fail_exit")

	c := newTestClient(t, dir)
	_, err := c.CurrentLogin(context.Background())
	if err == nil {
		t.Fatal("want error for gh exit failure")
	}
}

// TestClientMethods_TimeOutAndLeaveNoProcessBehind は、New 経由で組み立てた
// Client でも（run() だけでなく ListOpenIssues 経由でも）タイムアウトで
// 速やかに失敗し、偽 gh のプロセスが残らないことを確認する。
func TestClientMethods_TimeOutAndLeaveNoProcessBehind(t *testing.T) {
	dir := newFakeGHDir(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Setenv(envScenario, "sleep")
	t.Setenv(envPidFile, pidFile)
	t.Setenv(envSleepSeconds, "30")
	t.Setenv("PATH", dir)

	c, err := New(Options{Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	start := time.Now()
	_, err = c.ListOpenIssues(context.Background(), "masanami/flywheel")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("want timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want errors.Is(err, context.DeadlineExceeded), got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("took too long to time out: %s", elapsed)
	}

	pidBytes, rerr := waitForFile(t, pidFile, 3*time.Second)
	if rerr != nil {
		t.Fatalf("read pid file: %v", rerr)
	}
	pid, perr := strconv.Atoi(string(pidBytes))
	if perr != nil {
		t.Fatalf("parse pid: %v", perr)
	}
	if !waitForProcessGone(pid, 3*time.Second) {
		t.Fatal("fake gh process still alive after timeout")
	}
}

// TestNew_PrefersFirstGHOnPATH_AC107 は、PATH の先頭に置いた偽の gh が
// 使われ、後方に置いた「本物を装った」別の gh は一切呼ばれないことを
// 確かめる（AC-107）。
func TestNew_PrefersFirstGHOnPATH_AC107(t *testing.T) {
	fakeDir := newFakeGHDir(t)

	realishDir := t.TempDir()
	markerPath := filepath.Join(t.TempDir(), "real-gh-was-called")
	writeExecutableScript(t, filepath.Join(realishDir, "gh"), fmt.Sprintf("#!/bin/sh\necho called > %s\nexit 0\n", shellQuote(markerPath)))

	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+realishDir)
	t.Setenv(envScenario, "current_login_ok")

	c, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if filepath.Dir(c.ghPath) != fakeDir {
		t.Fatalf("resolved gh path %q is not under the fake gh dir %q", c.ghPath, fakeDir)
	}

	login, err := c.CurrentLogin(context.Background())
	if err != nil {
		t.Fatalf("CurrentLogin: %v", err)
	}
	if login != "masanami" {
		t.Fatalf("login = %q", login)
	}

	if _, err := os.Stat(markerPath); err == nil {
		t.Fatal("the real-looking gh (later in PATH) was invoked; it must never be reached")
	}
}
