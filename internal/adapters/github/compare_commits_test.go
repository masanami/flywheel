package github

import (
	"context"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// release の head の変化の判定に使う、2 つのコミットの比較の取得（core.UpstreamCompareSource）。
func TestCompareCommits_NormalizesCommitsAndFiles(t *testing.T) {
	dir := newFakeGHDir(t)
	logPath := newFakeGHLogPath(t)
	t.Setenv(envScenario, "compare_commits_ok")
	t.Setenv(envLogFile, logPath)

	got, err := newTestClient(t, dir).CompareCommits(context.Background(), "o/r", "v", "h1")
	if err != nil {
		t.Fatalf("CompareCommits: %v", err)
	}
	if got.Status != core.UpstreamCompareAhead || got.TotalCommits != 2 || len(got.Commits) != 2 {
		t.Fatalf("comparison = %+v", got)
	}
	if got.Commits[1].SHA != "h1" || len(got.Commits[1].Parents) != 2 || got.Commits[1].Parents[1] != "m1" || got.Commits[1].Message != "Merge branch 'main'" {
		t.Errorf("merge commit = %+v", got.Commits[1])
	}
	if got.Commits[0].Message != "main change\n\nbody" {
		t.Errorf("message = %q, want the full message", got.Commits[0].Message)
	}
	if len(got.Files) != 2 || got.Files[0].Filename != "a.go" || got.Files[0].Patch != "@@ -1 +1 @@\n-a\n+b" || got.Files[1].Patch != "" {
		t.Errorf("files = %+v, want the patch kept and empty for the file without one", got.Files)
	}
	if got.FilesTruncated {
		t.Errorf("FilesTruncated = true for 2 files")
	}
	calls := readInvocationLog(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("calls = %v, want 1", calls)
	}
	assertAllGET(t, calls)
	if joined := strings.Join(calls[0], " "); !strings.Contains(joined, "repos/o/r/compare/v...h1") || !strings.Contains(joined, "per_page=100") {
		t.Errorf("argv = %q, want the compare endpoint with per_page=100", joined)
	}
}

func TestCompareCommits_ReportsTruncation(t *testing.T) {
	dir := newFakeGHDir(t)
	t.Setenv(envScenario, "compare_commits_truncated")
	got, err := newTestClient(t, dir).CompareCommits(context.Background(), "o/r", "v", "h1")
	if err != nil {
		t.Fatalf("CompareCommits: %v", err)
	}
	if got.TotalCommits != 300 || len(got.Commits) != 1 {
		t.Errorf("comparison = %+v, want the total (300) kept next to the one returned commit", got)
	}

}

func TestCompareCommits_FilesTruncatedOnlyAtApiLimit(t *testing.T) {
	dir := newFakeGHDir(t)
	cases := []struct {
		scenario  string
		files     int
		truncated bool
		calls     int
	}{
		{"compare_commits_files_188", 188, false, 1},
		{"compare_commits_files_100", 100, false, 1},
		{"compare_commits_files_300", 300, true, 1},
	}
	for _, tc := range cases {
		logPath := newFakeGHLogPath(t)
		t.Setenv(envScenario, tc.scenario)
		t.Setenv(envLogFile, logPath)
		got, err := newTestClient(t, dir).CompareCommits(context.Background(), "o/r", "v", "h1")
		if err != nil {
			t.Fatalf("%s: CompareCommits: %v", tc.scenario, err)
		}
		if len(got.Files) != tc.files || got.FilesTruncated != tc.truncated || got.Status != core.UpstreamCompareDiverged || len(got.Commits) != 1 {
			t.Errorf("%s: files %d truncated %v status %q commits %d, want files %d truncated %v", tc.scenario, len(got.Files), got.FilesTruncated, got.Status, len(got.Commits), tc.files, tc.truncated)
		}
		calls := readInvocationLog(t, logPath)
		if len(calls) != tc.calls {
			t.Errorf("%s: calls = %d, want %d", tc.scenario, len(calls), tc.calls)
		}
		assertAllGET(t, calls)
	}
}

func TestCompareCommits_Failures(t *testing.T) {
	dir := newFakeGHDir(t)
	for _, scenario := range []string{"compare_fail", "compare_not_found", "compare_commits_bad_json"} {
		t.Setenv(envScenario, scenario)
		if _, err := newTestClient(t, dir).CompareCommits(context.Background(), "o/r", "v", "h1"); err == nil {
			t.Errorf("%s: err = nil, want an error", scenario)
		}
	}
}
