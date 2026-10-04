package github

import (
	"context"
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
	if got.URL != "https://github.com/o/r/pull/7" || got.State != "open" || got.Base != "main" || got.HeadSHA != "abc123" {
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
