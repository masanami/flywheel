package github

import (
	"context"
	"errors"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// 要求と別の repo・番号の Issue が返った場合（gh api が改名・移管の 301 を
// 辿った後の応答）は、別の外部キーの Issue として返さず、「見つからない」
// でもない失敗にする（同じ Issue の課題の二重作成を防ぐ。repoMismatchError）。
// #69: 別リポジトリの応答は core.ErrUpstreamIssueTransferred（404・410 とは
// 別の sentinel）で core 側に伝える。

func TestGetIssue_ResponseFromOtherRepo_IsTransferred(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "get_redirected_other_repo")
	t.Setenv(envLogFile, logPath)

	c := newTestClient(t, dir)
	_, err := c.GetIssue(context.Background(), "masanami/flywheel", 7)
	if err == nil {
		t.Fatal("want an error when the response belongs to another repository")
	}
	if !errors.Is(err, core.ErrUpstreamIssueTransferred) {
		t.Fatalf("a response from another repository must be classified as transferred (#69): %v", err)
	}
	if errors.Is(err, core.ErrUpstreamIssueNotFound) {
		t.Fatalf("a response from another repository must not be classified as not found: %v", err)
	}
	requireOnlyGETCalls(t, logPath)
}

func TestGetIssue_RequestedRepoCaseDiffers_IsAcceptedWithResponseCasing(t *testing.T) {
	dir := newFakeGHDir(t)
	t.Setenv(envScenario, "get_closed_issue")

	c := newTestClient(t, dir)
	issue, err := c.GetIssue(context.Background(), "MasaNami/Flywheel", 7)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.ExternalKey != "masanami/flywheel#7" {
		t.Fatalf("ExternalKey = %q, want the casing of the API response", issue.ExternalKey)
	}
}

func TestGetIssue_ResponseWithOtherNumber_IsFailureNotNotFound(t *testing.T) {
	dir := newFakeGHDir(t)
	t.Setenv(envScenario, "get_wrong_number")

	c := newTestClient(t, dir)
	_, err := c.GetIssue(context.Background(), "masanami/flywheel", 8) // 応答は #7
	if err == nil {
		t.Fatal("want an error when the response is another issue number")
	}
	if errors.Is(err, core.ErrUpstreamIssueNotFound) {
		t.Fatalf("must not be classified as not found: %v", err)
	}
}

func TestListOpenIssues_ElementFromOtherRepo_FailsWholeList(t *testing.T) {
	dir := newFakeGHDir(t)
	t.Setenv(envScenario, "list_other_repo")

	c := newTestClient(t, dir)
	issues, err := c.ListOpenIssues(context.Background(), "masanami/flywheel")
	if err == nil {
		t.Fatalf("want an error when list elements belong to another repository, got %d issues", len(issues))
	}
	if issues != nil {
		t.Fatalf("want no partial results, got %d issues", len(issues))
	}
}

// 呼び出し側が ctx を取り消したときは、gh の時間切れ（DeadlineExceeded）
// ではなく context.Canceled として返す（core が errors.Is で区別できる）。
func TestClientMethods_CallerCancel_IsNotReportedAsTimeout(t *testing.T) {
	dir := newFakeGHDir(t)
	t.Setenv(envScenario, "sleep")
	t.Setenv(envSleepSeconds, "30")

	c := newTestClient(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.ListOpenIssues(ctx, "masanami/flywheel")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ListOpenIssues: want errors.Is(err, context.Canceled), got %v", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ListOpenIssues: caller cancel must not be reported as a timeout: %v", err)
	}
	if _, err := c.GetIssue(ctx, "masanami/flywheel", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetIssue: want context.Canceled, got %v", err)
	}
	if _, err := c.CurrentLogin(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("CurrentLogin: want context.Canceled, got %v", err)
	}
}
