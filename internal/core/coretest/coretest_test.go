package coretest_test

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// FillAllTables は、すべての表に行を持つストアを作る。表の一覧は sqlite_master から読む側（FillAllTables
// 自身）が持ち、行の無い表が残ればフィクスチャ自身が落ちる（マイグレーションが表を足したときの更新漏れの検出）。
// ここでは別の経路（DumpTables）で、15 個の表のすべてに行があることを確かめる。
func TestFillAllTables_EveryTableHasRows(t *testing.T) {
	ws := t.TempDir()
	if _, err := core.Init(ws); err != nil {
		t.Fatalf("core.Init: %v", err)
	}
	coretest.FillAllTables(t, ws)
	tables := []string{"activity", "approval", "challenge", "challenge_dependency", "cycle", "hold", "lock",
		"occurrence", "operation", "operation_execution", "run", "run_artifact", "slot", "source_binding", "task_plan"}
	rowsOf := map[string]int{}
	current := ""
	for _, line := range strings.Split(coretest.DumpTables(t, ws, tables...), "\n") {
		switch {
		case line == "":
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") && !strings.Contains(line, " "):
			current = strings.Trim(line, "[]")
			rowsOf[current] = 0
		default:
			rowsOf[current]++
		}
	}
	for _, tbl := range tables {
		if rowsOf[tbl] == 0 {
			t.Errorf("table %s has no rows after FillAllTables", tbl)
		}
	}
}
