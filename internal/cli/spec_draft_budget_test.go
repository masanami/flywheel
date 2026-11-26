package cli

import (
	"path/filepath"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/flow"
	"github.com/masanami/flywheel/internal/flow/assets"
	"github.com/masanami/flywheel/internal/flow/workflow"
)

// AC-120: core はフローの定義を読めないので、1 回の上限額の定数と、埋め込んだ定義の limits.budget_usd の一致をここで確かめる。
func TestSpecDraftDefaultBudgetMatchesTheEmbeddedDefinition(t *testing.T) {
	layout, err := assets.Ensure(assets.FS, filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	wf, err := workflow.Load(filepath.Join(layout.WorkflowDir, flow.SpecDraftFlow+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := wf.Limit("budget_usd").Value; got != core.SpecDraftDefaultBudgetUSD {
		t.Fatalf("limits.budget_usd = %v, core.SpecDraftDefaultBudgetUSD = %v", got, core.SpecDraftDefaultBudgetUSD)
	}
}
