package github

import (
	"context"
	"errors"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// このファイルは #69「移管された Issue を missing として扱う」のうち、
// adapter が 3 つの失敗（404・410／移管／それ以外）を取り違えないことを
// 確かめる。移管そのものの検証（別 repo の応答 → core.ErrUpstreamIssueTransferred）
// は integration_mismatch_test.go の TestGetIssue_ResponseFromOtherRepo_IsTransferred
// が担う（既存テストを最小限に直したもの）。ここでは、それと混同されやすい
// 隣接ケース（404・同じ repo の番号違い）が Transferred に一致しないことを
// 確かめる。

func TestGetIssue_404_IsNotFoundButNotTransferred(t *testing.T) {
	dir := newFakeGHDir(t)
	t.Setenv(envScenario, "get_404_http11")

	c := newTestClient(t, dir)
	_, err := c.GetIssue(context.Background(), "masanami/flywheel", 999999)
	if !errors.Is(err, core.ErrUpstreamIssueNotFound) {
		t.Fatalf("want errors.Is(err, core.ErrUpstreamIssueNotFound), got %v", err)
	}
	if errors.Is(err, core.ErrUpstreamIssueTransferred) {
		t.Fatalf("a plain 404 must not be classified as transferred: %v", err)
	}
}

func TestGetIssue_ResponseWithOtherNumber_IsNotTransferred(t *testing.T) {
	dir := newFakeGHDir(t)
	t.Setenv(envScenario, "get_wrong_number")

	c := newTestClient(t, dir)
	_, err := c.GetIssue(context.Background(), "masanami/flywheel", 8) // 応答は同じ repo の #7
	if err == nil {
		t.Fatal("want an error when the response is another issue number")
	}
	if errors.Is(err, core.ErrUpstreamIssueTransferred) {
		t.Fatalf("same-repo response with a different number must not be classified as transferred (#69): %v", err)
	}
	if errors.Is(err, core.ErrUpstreamIssueNotFound) {
		t.Fatalf("must not be classified as not found: %v", err)
	}
}
