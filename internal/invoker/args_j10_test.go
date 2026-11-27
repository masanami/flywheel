package invoker

import (
	"slices"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// toolFlags は args のうち道具の指定（--allowedTools から --permission-mode の値まで）を返す。
func toolFlags(t *testing.T, args []string) []string {
	t.Helper()
	from := slices.Index(args, "--allowedTools")
	to := slices.Index(args, "--permission-mode")
	if from < 0 || to < from {
		t.Fatalf("args have no tool flags: %v", args)
	}
	return args[from : to+2]
}

// 区切りを分ける判断点の道具は読み取りだけで、分類の判断点と同じ一覧。
func TestBuildArgs_J10UsesTheSameReadOnlyToolsAsJ1(t *testing.T) {
	t.Parallel()
	j1 := toolFlags(t, buildArgs(core.JudgmentLaunchInput{Judgment: core.JudgmentJ1, MaxBudgetUSD: 1}))
	j10 := toolFlags(t, buildArgs(core.JudgmentLaunchInput{Judgment: core.JudgmentJ10, MaxBudgetUSD: 3}))
	if !slices.Equal(j1, j10) {
		t.Errorf("J10 tool flags = %v, want the same as J1 %v", j10, j1)
	}
	want := []string{"--allowedTools", "Read", "Grep", "Glob",
		"--disallowedTools", "Bash", "Edit", "Write", "NotebookEdit", "WebFetch", "WebSearch", "Task", "--permission-mode", "default"}
	if !slices.Equal(j10, want) {
		t.Errorf("J10 tool flags = %v, want %v", j10, want)
	}
}
