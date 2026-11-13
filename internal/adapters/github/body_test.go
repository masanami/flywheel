package github

import (
	"context"
	"strings"
	"testing"
)

// 本文と head の SHA を、PR の取得 1 回（GET だけ）で読む。
func TestGetPullRequestBody_ReadsBodyAndHeadInOneGet(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "pull_body_ok")
	t.Setenv(envLogFile, logPath)

	got, err := newTestClient(t, dir).GetPullRequestBody(context.Background(), "o/r", 7)
	if err != nil {
		t.Fatalf("GetPullRequestBody: %v", err)
	}
	if got.Body != "本文です" || got.HeadSHA != "abc123" {
		t.Errorf("got = %+v", got)
	}
	calls := readInvocationLog(t, logPath)
	if len(calls) != 1 || strings.Join(calls[0], " ") != "api repos/o/r/pulls/7" {
		t.Errorf("calls = %v, want exactly one GET of the pull request", calls)
	}
}

func TestGetPullRequestBody_NullBodyIsEmpty(t *testing.T) {
	dir := newFakeGHDir(t)
	t.Setenv(envScenario, "pull_body_null")
	t.Setenv(envLogFile, newFakeGHLogPath(t))
	got, err := newTestClient(t, dir).GetPullRequestBody(context.Background(), "o/r", 7)
	if err != nil || got.Body != "" || got.HeadSHA != "abc123" {
		t.Errorf("got = %+v, err = %v", got, err)
	}
}

func TestGetPullRequestBody_Failure(t *testing.T) {
	dir := newFakeGHDir(t)
	t.Setenv(envScenario, "pull_one_missing")
	t.Setenv(envLogFile, newFakeGHLogPath(t))
	if _, err := newTestClient(t, dir).GetPullRequestBody(context.Background(), "o/r", 7); err == nil {
		t.Error("err = nil, want a failure")
	}
}
