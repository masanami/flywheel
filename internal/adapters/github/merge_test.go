package github

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

const testPRURL = "https://github.com/o/r/pull/7"

// AC-39・AC-40: 引数は URL・方式のフラグ・--match-head-commit <head> だけ。3 方式を列挙して検証する。
func TestMergePullRequest_ArgsAreOnlyURLAutoMethodFlagAndMatchHead(t *testing.T) {
	cases := map[string]string{
		core.MergeMethodSquash: "--squash",
		core.MergeMethodMerge:  "--merge",
		core.MergeMethodRebase: "--rebase",
	}
	for method, flag := range cases {
		t.Run(method, func(t *testing.T) {
			dir := newFakeGHDir(t)
			logPath := newFakeGHLogPath(t)
			t.Setenv(envScenario, "merge_ok")
			t.Setenv(envLogFile, logPath)
			c := newTestClient(t, dir)
			if err := c.MergePullRequest(context.Background(), testPRURL, method, "abc123"); err != nil {
				t.Fatalf("MergePullRequest: %v", err)
			}
			calls := readInvocationLog(t, logPath)
			want := []string{"pr", "merge", testPRURL, "--auto", flag, "--match-head-commit", "abc123"}
			if len(calls) != 1 || !reflect.DeepEqual(calls[0], want) {
				t.Fatalf("gh calls = %v, want [%v]", calls, want)
			}
			for _, a := range calls[0] {
				if a == "--admin" || a == "--delete-branch" {
					t.Errorf("forbidden flag %q", a)
				}
			}
		})
	}
}

func TestMergePullRequest_NonZeroExitIsFailure(t *testing.T) {
	dir := newFakeGHDir(t)
	t.Setenv(envScenario, "merge_fail")
	c := newTestClient(t, dir)
	err := c.MergePullRequest(context.Background(), testPRURL, core.MergeMethodSquash, "abc123")
	if err == nil || !strings.Contains(err.Error(), "Head branch was modified") {
		t.Fatalf("err = %v, want a failure carrying stderr", err)
	}
}

func TestMergePullRequest_InvalidInputDoesNotCallGH(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "merge_ok")
	t.Setenv(envLogFile, logPath)
	c := newTestClient(t, dir)
	for _, tc := range []struct{ url, method, head string }{
		{testPRURL, "bogus", "abc"},
		{testPRURL, "", "abc"},
		{"", core.MergeMethodSquash, "abc"},
		{testPRURL, core.MergeMethodSquash, ""},
	} {
		if err := c.MergePullRequest(context.Background(), tc.url, tc.method, tc.head); err == nil {
			t.Errorf("%+v: want error", tc)
		}
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Errorf("gh was called for invalid input")
	}
}

func TestUpdatePullRequestBranch_ArgsArePutUpdateBranchWithExpectedHead(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "update_branch_ok")
	t.Setenv(envLogFile, logPath)
	c := newTestClient(t, dir)
	if err := c.UpdatePullRequestBranch(context.Background(), "o/r", 7, "abc123"); err != nil {
		t.Fatalf("UpdatePullRequestBranch: %v", err)
	}
	calls := readInvocationLog(t, logPath)
	want := []string{"api", "-X", "PUT", "repos/o/r/pulls/7/update-branch", "-f", "expected_head_sha=abc123"}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("gh calls = %v, want [%v]", calls, want)
	}
}

func TestUpdatePullRequestBranch_FailureAndInvalidInput(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "update_branch_fail")
	t.Setenv(envLogFile, logPath)
	c := newTestClient(t, dir)
	if err := c.UpdatePullRequestBranch(context.Background(), "o/r", 7, "abc123"); err == nil || !strings.Contains(err.Error(), "HTTP 422") {
		t.Fatalf("err = %v, want a failure carrying stderr", err)
	}
	for _, tc := range []struct {
		repo string
		n    int
		head string
	}{{"", 7, "a"}, {"o/r", 0, "a"}, {"o/r", 7, ""}} {
		if err := c.UpdatePullRequestBranch(context.Background(), tc.repo, tc.n, tc.head); err == nil {
			t.Errorf("%+v: want error", tc)
		}
	}
	if n := len(readInvocationLog(t, logPath)); n != 1 {
		t.Errorf("gh calls = %d, want 1 (invalid input must not call gh)", n)
	}
}
