package github

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// 固定した SHA を compare の API で指し、差分の形式を求める（#465）。
func TestGetCompareDiff_AsksForTheFixedCommit(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "compare_ok")
	t.Setenv(envLogFile, logPath)

	got, err := newTestClient(t, dir).GetCompareDiff(context.Background(), "o/r", "base456", "abc123")
	if err != nil {
		t.Fatalf("GetCompareDiff: %v", err)
	}
	if !strings.HasPrefix(got, "diff --git") {
		t.Errorf("diff = %q", got)
	}
	calls := readInvocationLog(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("calls = %v", calls)
	}
	joined := strings.Join(calls[0], " ")
	for _, want := range []string{"Accept: application/vnd.github.diff", "repos/o/r/compare/base456...abc123"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv %q lacks %q", joined, want)
		}
	}
}

func TestGetCompareDiff_Failures(t *testing.T) {
	dir := newFakeGHDir(t)
	t.Setenv(envLogFile, newFakeGHLogPath(t))

	for _, scenario := range []string{"compare_too_large", "compare_too_slow", "compare_not_found"} {
		t.Setenv(envScenario, scenario)
		if _, err := newTestClient(t, dir).GetCompareDiff(context.Background(), "o/r", "b", "abc"); !errors.Is(err, core.ErrUpstreamDiffUnavailable) {
			t.Errorf("%s: err = %v", scenario, err)
		}
	}
	t.Setenv(envScenario, "compare_fail")
	_, err := newTestClient(t, dir).GetCompareDiff(context.Background(), "o/r", "b", "abc")
	if err == nil || errors.Is(err, core.ErrUpstreamDiffUnavailable) {
		t.Errorf("server error: err = %v", err)
	}
}
