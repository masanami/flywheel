package github

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

func fakeGHPullsTwoPages(argv []string) int {
	endpoint, _ := lastEndpointAndDashI(argv)
	var items []string
	switch parsePageParam(endpoint) {
	case 1:
		for i := 0; i < perPage; i++ {
			items = append(items, fmt.Sprintf(`{"html_url":"https://github.com/o/r/pull/%d","title":"t","state":"open","merged_at":null,"base":{"ref":"main"}}`, i+1))
		}
	case 2:
		items = append(items, `{"html_url":"https://github.com/o/r/pull/101","title":"t","state":"open","merged_at":null,"base":{"ref":"main"}}`)
	default:
		return 1
	}
	fmt.Print("[" + strings.Join(items, ",") + "]")
	return 0
}

// assertAllGET は、記録した gh の呼び出しがすべて GET（api サブコマンドで、書き込みの
// フラグ・-X/--method を持たない）であることを確かめる（AC-226）。
func assertAllGET(t *testing.T, calls [][]string) {
	t.Helper()
	for _, call := range calls {
		if len(call) == 0 || call[0] != "api" {
			t.Fatalf("gh call is not an api call: %v", call)
		}
		for _, a := range call[1:] {
			switch a {
			case "-X", "--method", "-f", "-F", "--field", "--raw-field", "--input":
				t.Fatalf("gh call has a non-GET flag %q: %v", a, call)
			}
			if strings.HasPrefix(a, "--method=") || strings.HasPrefix(a, "-X") {
				t.Fatalf("gh call has a non-GET flag %q: %v", a, call)
			}
		}
	}
}

func TestBranchExists(t *testing.T) {
	cases := []struct {
		scenario string
		want     bool
		wantErr  bool
	}{
		{"branch_exists", true, false},
		{"branch_missing", false, false},
		{"branch_http500", false, true},
		{"get_exit_nonzero_no_status_line", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.scenario, func(t *testing.T) {
			dir := newFakeGHDir(t)
			logPath := newFakeGHLogPath(t)
			t.Setenv(envScenario, tc.scenario)
			t.Setenv(envLogFile, logPath)
			c := newTestClient(t, dir)
			got, err := c.BranchExists(context.Background(), "o/r", "feat/x")
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("BranchExists = %v, %v; want %v, err=%v", got, err, tc.want, tc.wantErr)
			}
			calls := readInvocationLog(t, logPath)
			assertAllGET(t, calls)
			if ep, _ := lastEndpointAndDashI(calls[0]); ep != "repos/o/r/branches/feat/x" {
				t.Fatalf("endpoint = %q", ep)
			}
		})
	}
}

func TestListPullRequestsByHead_ThreeStatesAndBase(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "pulls_three_states")
	t.Setenv(envLogFile, logPath)
	c := newTestClient(t, dir)
	got, err := c.ListPullRequestsByHead(context.Background(), "o/r", "feat/x")
	if err != nil {
		t.Fatalf("ListPullRequestsByHead: %v", err)
	}
	want := []core.UpstreamPullRequest{
		{URL: "https://github.com/o/r/pull/1", Title: "open one", State: "open", Base: "main"},
		{URL: "https://github.com/o/r/pull/2", Title: "closed one", State: "closed", Base: "develop"},
		{URL: "https://github.com/o/r/pull/3", Title: "merged one", State: "merged", Base: "main"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	calls := readInvocationLog(t, logPath)
	assertAllGET(t, calls)
	ep, _ := lastEndpointAndDashI(calls[0])
	u, err := url.Parse(ep)
	if err != nil || u.Path != "repos/o/r/pulls" {
		t.Fatalf("endpoint = %q (%v)", ep, err)
	}
	q := u.Query()
	if q.Get("head") != "o:feat/x" || q.Get("state") != "all" {
		t.Fatalf("query = %v", q)
	}
}

func TestListPullRequestsByHead_PaginatesAndFails(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "pulls_two_pages")
	t.Setenv(envLogFile, logPath)
	c := newTestClient(t, dir)
	got, err := c.ListPullRequestsByHead(context.Background(), "o/r", "b")
	if err != nil || len(got) != perPage+1 {
		t.Fatalf("got %d, err=%v", len(got), err)
	}
	assertAllGET(t, readInvocationLog(t, logPath))

	t.Setenv(envScenario, "pulls_fail")
	if _, err := c.ListPullRequestsByHead(context.Background(), "o/r", "b"); err == nil {
		t.Fatal("want an error when gh fails")
	}
}

func TestGetPullRequest_NormalizesTheThreeStates(t *testing.T) {
	cases := []struct {
		scenario string
		want     core.UpstreamPullRequest
	}{
		{"pull_one_open", core.UpstreamPullRequest{URL: "https://github.com/o/r/pull/7", Title: "open one", State: "open", Base: "main", HeadRef: "feat/x", HeadSHA: "0123abcd"}},
		{"pull_one_closed", core.UpstreamPullRequest{URL: "https://github.com/o/r/pull/7", Title: "closed one", State: "closed", Base: "develop"}},
		{"pull_one_merged", core.UpstreamPullRequest{URL: "https://github.com/o/r/pull/7", Title: "merged one", State: "merged", Base: "main"}},
	}
	for _, tc := range cases {
		t.Run(tc.scenario, func(t *testing.T) {
			dir := newFakeGHDir(t)
			logPath := newFakeGHLogPath(t)
			t.Setenv(envScenario, tc.scenario)
			t.Setenv(envLogFile, logPath)
			c := newTestClient(t, dir)
			got, err := c.GetPullRequest(context.Background(), "o/r", 7)
			if err != nil {
				t.Fatalf("GetPullRequest: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			calls := readInvocationLog(t, logPath)
			assertAllGET(t, calls)
			if len(calls) != 1 {
				t.Fatalf("gh calls = %v, want exactly one", calls)
			}
			if ep, _ := lastEndpointAndDashI(calls[0]); ep != "repos/o/r/pulls/7" {
				t.Fatalf("endpoint = %q", ep)
			}
		})
	}
}

func TestGetPullRequest_Failures(t *testing.T) {
	for _, scenario := range []string{"pulls_fail", "pull_one_unknown_state", "pull_one_no_base"} {
		t.Run(scenario, func(t *testing.T) {
			dir := newFakeGHDir(t)
			t.Setenv(envScenario, scenario)
			c := newTestClient(t, dir)
			if _, err := c.GetPullRequest(context.Background(), "o/r", 7); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}
