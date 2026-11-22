package invoker

import (
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// AC-65: スーパーバイザーの判断点に許す道具は Read・Grep・Glob だけ（ほかの判断点と同じ引数の組み立てで、コマンドを実行する道具を持たない）。
func TestBuildArgs_SupervisorJudgmentAllowsOnlyReadGrepGlob(t *testing.T) {
	t.Parallel()
	args := buildArgs(core.JudgmentLaunchInput{SessionID: "11111111-1111-4111-8111-111111111111", Judgment: core.JudgmentSV, OutputSchema: core.SVOutputSchema(), MaxBudgetUSD: 5})
	idx := -1
	for i, a := range args {
		if a == "--allowedTools" {
			idx = i
		}
	}
	if idx < 0 || idx+4 >= len(args) {
		t.Fatalf("args = %v, want --allowedTools followed by three tools and the next flag", args)
	}
	if got := args[idx+1 : idx+4]; got[0] != "Read" || got[1] != "Grep" || got[2] != "Glob" {
		t.Errorf("allowed tools = %v, want [Read Grep Glob]", got)
	}
	if args[idx+4] != "--disallowedTools" {
		t.Errorf("args after the three allowed tools = %q, want --disallowedTools", args[idx+4])
	}
	if !hasConsecutive(args, "--max-budget-usd", "5") {
		t.Errorf("args = %v, want --max-budget-usd 5", args)
	}
}

// 判断点 SV の指示文は、埋め込みの指示文の一覧から引け、分量の上限の表に載っている。
func TestInstructions_SupervisorJudgmentHasAnEmbeddedPromptWithALimit(t *testing.T) {
	t.Parallel()
	body, err := Instructions(core.JudgmentSV)
	if err != nil || body == "" {
		t.Fatalf("Instructions(SV) = %d bytes, %v", len(body), err)
	}
	if judgmentPromptLimit["sv.md"] <= 0 || len(body) > judgmentPromptLimit["sv.md"] {
		t.Errorf("sv.md is %d bytes; limit %d", len(body), judgmentPromptLimit["sv.md"])
	}
	fsys, err := promptsSubFS()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckPromptSizes(fsys); err != nil {
		t.Errorf("CheckPromptSizes(embedded) = %v", err)
	}
}
