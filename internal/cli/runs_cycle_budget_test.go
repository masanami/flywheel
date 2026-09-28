package cli

import (
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは #83（親要件チケット #77・docs/features/m3-invoker-delegation.md
// §IF / API「runs」）が `internal/cli/runs.go` に足した cycle_budget_usd を
// 検証する。`cycle` コマンド自体（#86）はまだ結線されていないため、
// coretest.InsertCycle・coretest.InsertRunInput.CycleID でフィクスチャを作る。

// TestRuns_CycleBudgetUSDIsNullWhenRunHasNoCycle は、cycle_id が NULL の run
// （周の外で記録された run）の cycle_budget_usd が null であることを検証する
// （既存の runs_test.go の他のケースと同じく、cycle_id を設定しないフィクスチャ
// では常にこうなる）。
func TestRuns_CycleBudgetUSDIsNullWhenRunHasNoCycle(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	insertRunFixture(t, ws, id, nil)

	doc := runJSON(t, ws, "runs", id)
	runs := doc["runs"].([]any)
	elem := runs[0].(map[string]any)
	if elem["cycle_id"] != nil {
		t.Fatalf("cycle_id = %v, want nil", elem["cycle_id"])
	}
	if elem["cycle_budget_usd"] != nil {
		t.Errorf("cycle_budget_usd = %v, want null when cycle_id is null", elem["cycle_budget_usd"])
	}
}

// TestRuns_CycleBudgetUSDReflectsTheRunsCycle は、cycle_id を持つ run の
// cycle_budget_usd が、その周（cycle 行）の budget_usd（USD）であることを
// 検証する。
func TestRuns_CycleBudgetUSDReflectsTheRunsCycle(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	cycleID := coretest.InsertCycle(t, ws, "classify --auto", 2_500_000, "2026-09-28T00:00:00.000Z")
	insertRunFixture(t, ws, id, func(in *coretest.InsertRunInput) {
		in.CycleID = &cycleID
	})

	doc := runJSON(t, ws, "runs", id)
	runs := doc["runs"].([]any)
	elem := runs[0].(map[string]any)
	if elem["cycle_id"] != "Y-1" {
		t.Fatalf("cycle_id = %v, want Y-1", elem["cycle_id"])
	}
	if elem["cycle_budget_usd"] != float64(2.5) {
		t.Errorf("cycle_budget_usd = %v, want 2.5", elem["cycle_budget_usd"])
	}
}
