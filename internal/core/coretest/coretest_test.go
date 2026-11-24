package coretest_test

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// FillAllTables は、すべての表に行を持つストアを作る。表の一覧は sqlite_master から読む側（FillAllTables
// 自身）が持ち、行の無い表が残ればフィクスチャ自身が落ちる（マイグレーションが表を足したときの更新漏れの検出）。
// ここでは別の経路（DumpTables）で、17 個の表のすべてに行があることを確かめる。
func TestFillAllTables_EveryTableHasRows(t *testing.T) {
	ws := t.TempDir()
	if _, err := core.Init(ws); err != nil {
		t.Fatalf("core.Init: %v", err)
	}
	coretest.FillAllTables(t, ws)
	tables := []string{"activity", "approval", "challenge", "challenge_dependency", "challenge_parent", "contract_run", "contract_run_step", "cycle", "hold", "lock",
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

// 足す依頼と、そこから立つ保留を直接置く道具は、前段の成果（足す依頼を作る処理）が無くても、置いた行が core の読み口から見える。
func TestInsertTaskAdditionAndHold_AreReadableThroughCore(t *testing.T) {
	ws := t.TempDir()
	if _, err := core.Init(ws); err != nil {
		t.Fatalf("core.Init: %v", err)
	}
	coretest.InsertChallenge(t, ws, "c1")
	const options = `[{"id":"add","label":"足す","description":"上限を 40 USD に上げる"},{"id":"skip","label":"足さない","description":""}]`
	id := coretest.InsertTaskAddition(t, ws, coretest.TaskAdditionRow{ChallengeID: 1, State: "awaiting_owner", TotalAfterMicros: 41_000_000, LimitMicros: 30_000_000})
	holdID := coretest.InsertTaskAdditionHold(t, ws, id, "task_add_over_limit", "上限を超えます", "in_progress", options)
	coretest.SetChallengeBudgetLimit(t, ws, 1, 30_000_000, "続きを足す分")

	s, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	list, err := s.ListTaskAdditions(t.Context(), "C-1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListTaskAdditions = %+v, err %v", list, err)
	}
	ta := list[0]
	if ta.ID != "TA-1" || ta.State != core.TaskAdditionAwaitingOwner || ta.Origin != core.TaskAdditionOriginExpand ||
		ta.TotalAfterUSD == nil || *ta.TotalAfterUSD != 41 || ta.LimitUSD == nil || *ta.LimitUSD != 30 || ta.HoldID == nil || *ta.HoldID != holdID {
		t.Errorf("addition = %+v, want awaiting_owner expand, total 41, limit 30, hold %d", ta, holdID)
	}
	d, err := s.GetChallenge(t.Context(), "C-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Holds) != 1 || len(d.Holds[0].Options) != 2 || !d.Holds[0].OptionRequired || d.Holds[0].Options[0].ID != "add" {
		t.Errorf("holds = %+v, want the one option-bearing hold", d.Holds)
	}
}
