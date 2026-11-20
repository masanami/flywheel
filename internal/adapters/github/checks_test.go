package github

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

func TestGetPullRequestChecks_NormalizesCheckRunsAndLegacyStatuses(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "checks_pending")
	t.Setenv(envLogFile, logPath)
	c := newTestClient(t, dir)

	got, err := c.GetPullRequestChecks(context.Background(), "o/r", 7)
	if err != nil {
		t.Fatalf("GetPullRequestChecks: %v", err)
	}
	if got.URL != "https://github.com/o/r/pull/7" || got.State != "open" || got.Base != "main" || got.BaseSHA != "base456" || got.HeadSHA != "abc123" {
		t.Fatalf("pull request = %+v", got)
	}
	want := []core.UpstreamCheck{
		{Name: "build", Status: "completed", Conclusion: "success"},
		{Name: "test", Status: "in_progress"},
		{Name: "legacy-a", Status: core.UpstreamCheckStatusLegacy, Conclusion: "success"},
		{Name: "legacy-b", Status: core.UpstreamCheckStatusLegacy, Conclusion: "pending"},
	}
	if len(got.Checks) != len(want) {
		t.Fatalf("checks = %+v, want %+v", got.Checks, want)
	}
	for i := range want {
		if got.Checks[i] != want[i] {
			t.Errorf("checks[%d] = %+v, want %+v", i, got.Checks[i], want[i])
		}
	}
	calls := readInvocationLog(t, logPath)
	if len(calls) != 3 {
		t.Fatalf("gh calls = %v, want 3 (pull, check-runs, status)", calls)
	}
	assertAllGET(t, calls)
}

func TestGetPullRequestChecks_MergedPullRequestIsNormalized(t *testing.T) {
	dir := newFakeGHDir(t)
	t.Setenv(envScenario, "checks_merged")
	c := newTestClient(t, dir)
	got, err := c.GetPullRequestChecks(context.Background(), "o/r", 7)
	if err != nil {
		t.Fatalf("GetPullRequestChecks: %v", err)
	}
	if got.State != "merged" {
		t.Errorf("state = %q, want merged", got.State)
	}
}

func TestGetPullRequestChecks_FailureIsReturnedAsError(t *testing.T) {
	dir := newFakeGHDir(t)
	t.Setenv(envScenario, "checks_runs_fail")
	c := newTestClient(t, dir)
	_, err := c.GetPullRequestChecks(context.Background(), "o/r", 7)
	if err == nil || !strings.Contains(err.Error(), "check runs") {
		t.Fatalf("err = %v, want a check runs failure", err)
	}
}

func TestGetPullRequestChecks_MergeableAndMergeCommitSHA(t *testing.T) {
	cases := []struct {
		env  string
		want *bool
	}{
		{"true", boolPtr(true)},
		{"false", boolPtr(false)},
		{"null", nil},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			dir := newFakeGHDir(t)
			t.Setenv(envScenario, "checks_complete")
			t.Setenv(envMergeable, tc.env)
			c := newTestClient(t, dir)
			got, err := c.GetPullRequestChecks(context.Background(), "o/r", 7)
			if err != nil {
				t.Fatalf("GetPullRequestChecks: %v", err)
			}
			if (got.Mergeable == nil) != (tc.want == nil) || (tc.want != nil && *got.Mergeable != *tc.want) {
				t.Errorf("Mergeable = %v, want %v", got.Mergeable, tc.want)
			}
			if got.MergeCommitSHA != "mc456" {
				t.Errorf("MergeCommitSHA = %q, want mc456", got.MergeCommitSHA)
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }

func TestGetPullRequestChecks_MergeableState(t *testing.T) {
	dir := newFakeGHDir(t)
	t.Setenv(envScenario, "checks_complete")
	t.Setenv(envMergeableState, "behind")
	c := newTestClient(t, dir)
	got, err := c.GetPullRequestChecks(context.Background(), "o/r", 7)
	if err != nil {
		t.Fatalf("GetPullRequestChecks: %v", err)
	}
	if got.MergeableState != "behind" {
		t.Errorf("MergeableState = %q, want %q", got.MergeableState, "behind")
	}
}

// fixedSHA は GetCommitChecks の試験で渡す、承認した head のコミットの SHA。
// 偽の gh はこの SHA のパスにだけ応答する（別の SHA が渡れば取得が失敗する）。
const fixedSHA = "f1xed0123456789abcdef0123456789abcdef012"

func TestGetCommitChecks_ZeroChecksIsEmptyWithNilError(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "commit_checks_none")
	t.Setenv(envLogFile, logPath)
	c := newTestClient(t, dir)

	got, err := c.GetCommitChecks(context.Background(), "o/r", fixedSHA)
	if err != nil {
		t.Fatalf("GetCommitChecks: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("checks = %+v, want empty", got)
	}
	assertCommitChecksCalls(t, logPath)
}

func TestGetCommitChecks_ReturnsChecksAndLegacyStatuses(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "commit_checks_some")
	t.Setenv(envLogFile, logPath)
	c := newTestClient(t, dir)

	got, err := c.GetCommitChecks(context.Background(), "o/r", fixedSHA)
	if err != nil {
		t.Fatalf("GetCommitChecks: %v", err)
	}
	want := []core.UpstreamCheck{
		{Name: "build", Status: "completed", Conclusion: "success"},
		{Name: "legacy-a", Status: core.UpstreamCheckStatusLegacy, Conclusion: "pending"},
	}
	if len(got) != len(want) {
		t.Fatalf("checks = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("checks[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	assertCommitChecksCalls(t, logPath)
}

func TestGetCommitChecks_FailureIsErrorNotEmptyList(t *testing.T) {
	for _, tc := range []struct{ scenario, want string }{
		{"commit_checks_runs_fail", "check runs"},
		{"commit_checks_status_fail", "commit statuses"},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			dir := newFakeGHDir(t)
			t.Setenv(envScenario, tc.scenario)
			c := newTestClient(t, dir)
			got, err := c.GetCommitChecks(context.Background(), "o/r", fixedSHA)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want a %s failure", err, tc.want)
			}
			if got != nil {
				t.Errorf("checks = %+v, want nil on failure", got)
			}
		})
	}
}

func TestGetCommitChecks_UnknownSHAIsError(t *testing.T) {
	dir := newFakeGHDir(t)
	t.Setenv(envScenario, "commit_checks_none")
	c := newTestClient(t, dir)
	// 偽の gh は fixedSHA 以外のパスを失敗させる（存在しない SHA の場合に相当）。
	got, err := c.GetCommitChecks(context.Background(), "o/r", "0000000000000000000000000000000000000000")
	if err == nil || got != nil {
		t.Fatalf("got = %+v, err = %v, want nil and an error", got, err)
	}
	if !strings.Contains(err.Error(), "check runs") || !strings.Contains(err.Error(), "0000000000000000000000000000000000000000") {
		t.Errorf("err = %v, want a check runs failure naming the unknown SHA", err)
	}
}

// assertCommitChecksCalls は、渡した SHA のパスで check-runs と status を GET しただけであることを検査する。
func assertCommitChecksCalls(t *testing.T, logPath string) {
	t.Helper()
	requireOnlyGETCalls(t, logPath)
	calls := readInvocationLog(t, logPath)
	if len(calls) != 2 {
		t.Fatalf("gh calls = %v, want 2 (check-runs, status)", calls)
	}
	wantPaths := []string{
		"repos/o/r/commits/" + fixedSHA + "/check-runs?",
		"repos/o/r/commits/" + fixedSHA + "/status?",
	}
	for i, argv := range calls {
		if got := argv[len(argv)-1]; !strings.HasPrefix(got, wantPaths[i]) {
			t.Errorf("call[%d] endpoint = %q, want prefix %q", i, got, wantPaths[i])
		}
	}
}

// ページングの試験。GetCommitChecks と GetPullRequestChecks は listCommitChecks を共有するので、同じ期待を両方の経路で固定する。
func checksViaBothPaths(t *testing.T, c *Client) map[string]func() ([]core.UpstreamCheck, error) {
	t.Helper()
	return map[string]func() ([]core.UpstreamCheck, error){
		"GetCommitChecks": func() ([]core.UpstreamCheck, error) {
			return c.GetCommitChecks(context.Background(), "o/r", fixedSHA)
		},
		"GetPullRequestChecks": func() ([]core.UpstreamCheck, error) {
			got, err := c.GetPullRequestChecks(context.Background(), "o/r", 7)
			// エラー時も got.Checks をそのまま返し、途中までの一覧が漏れないことを試験で確かめられるようにする。
			return got.Checks, err
		},
	}
}

// pagedEndpoints は、ログから check-runs と status の呼び出しのパスの末尾（クエリ）を順に返す（PR の取得は除く）。
func pagedEndpoints(t *testing.T, logPath string) []string {
	t.Helper()
	requireOnlyGETCalls(t, logPath)
	var out []string
	for _, argv := range readInvocationLog(t, logPath) {
		ep := argv[len(argv)-1]
		if strings.HasPrefix(ep, "repos/o/r/pulls/") {
			continue
		}
		out = append(out, strings.TrimPrefix(ep, "repos/o/r/commits/"+fixedSHA+"/"))
	}
	return out
}

func TestListCommitChecks_ReadsSecondPageWhenFirstPageIsFull(t *testing.T) {
	base := "check-runs?page=%d&per_page=100"
	stBase := "status?page=%d&per_page=100"
	wantCalls := []string{fmt.Sprintf(base, 1), fmt.Sprintf(base, 2), fmt.Sprintf(stBase, 1), fmt.Sprintf(stBase, 2)}
	for _, name := range []string{"GetCommitChecks", "GetPullRequestChecks"} {
		t.Run(name, func(t *testing.T) {
			dir := newFakeGHDir(t)
			logPath := newFakeGHLogPath(t)
			t.Setenv(envScenario, "paged_two")
			t.Setenv(envLogFile, logPath)
			c := newTestClient(t, dir)
			got, err := checksViaBothPaths(t, c)[name]()
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if len(got) != 205 {
				t.Fatalf("checks = %d, want 205 (103 check runs + 102 statuses)", len(got))
			}
			if got[0].Name != "run-p1-0" || got[102].Name != "run-p2-2" || got[103].Name != "st-p1-0" || got[204].Name != "st-p2-1" {
				t.Errorf("order/boundaries wrong: %+v %+v %+v %+v", got[0], got[102], got[103], got[204])
			}
			if got[102].Status != "completed" || got[103].Status != core.UpstreamCheckStatusLegacy {
				t.Errorf("status normalization wrong: %+v %+v", got[102], got[103])
			}
			if calls := pagedEndpoints(t, logPath); !reflect.DeepEqual(calls, wantCalls) {
				t.Errorf("calls = %v, want %v", calls, wantCalls)
			}
		})
	}
}

func TestListCommitChecks_EmptySecondPageEndsAtExactly100(t *testing.T) {
	base := "check-runs?page=%d&per_page=100"
	stBase := "status?page=%d&per_page=100"
	wantCalls := []string{fmt.Sprintf(base, 1), fmt.Sprintf(base, 2), fmt.Sprintf(stBase, 1), fmt.Sprintf(stBase, 2)}
	for _, name := range []string{"GetCommitChecks", "GetPullRequestChecks"} {
		t.Run(name, func(t *testing.T) {
			dir := newFakeGHDir(t)
			logPath := newFakeGHLogPath(t)
			t.Setenv(envScenario, "paged_exact")
			t.Setenv(envLogFile, logPath)
			c := newTestClient(t, dir)
			got, err := checksViaBothPaths(t, c)[name]()
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if len(got) != 200 {
				t.Fatalf("checks = %d, want 200 (100 check runs + 100 statuses)", len(got))
			}
			if calls := pagedEndpoints(t, logPath); !reflect.DeepEqual(calls, wantCalls) {
				t.Errorf("calls = %v (a third page must not be read), want %v", calls, wantCalls)
			}
		})
	}
}

func TestListCommitChecks_SecondPageFailureIsErrorWithoutPartialList(t *testing.T) {
	for _, tc := range []struct{ scenario, want string }{
		{"paged_runs_page2_fail", "check runs"},
		{"paged_status_page2_fail", "commit statuses"},
	} {
		for _, name := range []string{"GetCommitChecks", "GetPullRequestChecks"} {
			t.Run(tc.scenario+"/"+name, func(t *testing.T) {
				dir := newFakeGHDir(t)
				t.Setenv(envScenario, tc.scenario)
				c := newTestClient(t, dir)
				got, err := checksViaBothPaths(t, c)[name]()
				if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "page 2") {
					t.Fatalf("err = %v, want a %s failure on page 2", err, tc.want)
				}
				if got != nil {
					t.Errorf("checks = %d items, want nil on failure", len(got))
				}
			})
		}
	}
}
