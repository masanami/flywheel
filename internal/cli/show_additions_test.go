package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// 区切り S4c-1: `flywheel show` の足されたタスクの印（tasks[].added）・足す依頼の一覧（additions[]）・完了の承認の要約。
// 足す依頼は、土台の道具（coretest.InsertTaskAddition）で直接置いて確かめる。

// addedTaskWorkspace は T-1〜T-3 を持つ着手中の課題に、T-1 を出どころとする足す依頼 TA-1 を置き、T-3 をその依頼で足したタスクにする。
func addedTaskWorkspace(t *testing.T) (ws, id string) {
	t.Helper()
	ws, id = approvedTasksWorkspace(t)
	ta := coretest.InsertTaskAddition(t, ws, coretest.TaskAdditionRow{ChallengeID: 1, OriginTaskID: 1, State: "applied", TotalAfterMicros: 120_000_000, LimitMicros: 100_000_000, DecidedAt: "2026-11-22T01:00:00.000Z"})
	coretest.Exec(t, ws, `UPDATE task SET addition_id = ?, created_at = '2026-11-22T00:30:00.000Z' WHERE id = 3`, ta)
	return ws, id
}

// AC-193〜AC-195: 足されたタスクの added は、足す依頼の出どころ・出どころのタスクの ID・足した日時。
// AC-196: 計画の承認で作ったタスクの added は null。
func TestShowJSON_AddedMarkOfAnAddedTask_AC193_AC196(t *testing.T) {
	t.Parallel()
	ws, id := addedTaskWorkspace(t)
	tasks := objectList(t, runJSON(t, ws, "show", id, "--json")["tasks"], "tasks")
	added, ok := tasks[2]["added"].(map[string]any)
	if !ok {
		t.Fatalf("tasks[2].added = %#v, want an object", tasks[2]["added"])
	}
	if added["origin"] != "expand" || added["origin_task_id"] != "T-1" || added["added_at"] != "2026-11-22T00:30:00.000Z" {
		t.Errorf("added = %v, want origin expand, origin_task_id T-1, added_at the task's creation time", added)
	}
	for _, i := range []int{0, 1} {
		if v, has := tasks[i]["added"]; !has || v != nil {
			t.Errorf("tasks[%d].added = %v (present=%v), want present and null (made by the plan approval)", i, v, has)
		}
	}
}

// AC-197・AC-198・AC-201: additions[] の要素は id・origin・state・task_ids を持ち、閉じた足す依頼も入る。
func TestShowJSON_AdditionsIncludeClosedOnes_AC197_AC198_AC201(t *testing.T) {
	t.Parallel()
	ws, id := addedTaskWorkspace(t)
	coretest.InsertTaskAddition(t, ws, coretest.TaskAdditionRow{ChallengeID: 1, OriginTaskID: 2, State: "declined", DecidedAt: "2026-11-22T02:00:00.000Z"})
	adds := objectList(t, runJSON(t, ws, "show", id, "--json")["additions"], "additions")
	if len(adds) != 2 {
		t.Fatalf("additions = %v, want 2 (the closed one is included)", adds)
	}
	if adds[0]["id"] != "TA-1" || adds[0]["origin"] != "expand" || adds[0]["state"] != "applied" || adds[0]["origin_task_id"] != "T-1" {
		t.Errorf("additions[0] = %v", adds[0])
	}
	if ids, _ := adds[0]["task_ids"].([]any); len(ids) != 1 || ids[0] != "T-3" {
		t.Errorf("additions[0].task_ids = %v, want [T-3]", adds[0]["task_ids"])
	}
	if adds[1]["state"] != "declined" || adds[1]["decided_at"] != "2026-11-22T02:00:00.000Z" {
		t.Errorf("additions[1] = %v, want the declined one with decided_at", adds[1])
	}
	if ids, ok := adds[1]["task_ids"].([]any); !ok || len(ids) != 0 {
		t.Errorf("additions[1].task_ids = %#v, want an empty array", adds[1]["task_ids"])
	}
}

// AC-199・AC-200: 上限を超えて待っている足す依頼の total_after_usd・limit_usd は、判定のときに記録した値。
func TestShowJSON_AdditionRecordsTheLastJudgement_AC199_AC200(t *testing.T) {
	t.Parallel()
	ws, id := approvedTasksWorkspace(t)
	coretest.InsertTaskAddition(t, ws, coretest.TaskAdditionRow{ChallengeID: 1, OriginTaskID: 1, State: "awaiting_owner", TotalAfterMicros: 120_500_000, LimitMicros: 100_000_000})
	adds := objectList(t, runJSON(t, ws, "show", id, "--json")["additions"], "additions")
	if len(adds) != 1 || adds[0]["state"] != "awaiting_owner" || adds[0]["total_after_usd"] != 120.5 || adds[0]["limit_usd"] != 100.0 {
		t.Errorf("additions = %v, want the awaiting_owner one with total_after_usd 120.5 and limit_usd 100", adds)
	}
}

// AC-202: 足す依頼が無い課題の additions は空の配列。
func TestShowJSON_AdditionsIsAnEmptyArrayWithoutAdditions_AC202(t *testing.T) {
	t.Parallel()
	ws, id := approvedTasksWorkspace(t)
	adds, ok := runJSON(t, ws, "show", id, "--json")["additions"].([]any)
	if !ok || len(adds) != 0 {
		t.Errorf("additions = %#v, want an empty array (not null)", adds)
	}
}

// AC-208: 完了の承認待ちの課題の show に、計画の後に足したタスクの題名が出る。それ以外の状態では出さない。
func TestShowText_AddedTasksAreListedWhileAwaitingCompletion_AC208(t *testing.T) {
	t.Parallel()
	ws, id := addedTaskWorkspace(t)
	if out := runText(t, ws, "show", id); strings.Contains(out, "計画の後に足したタスク") {
		t.Errorf("show text in progress = %q, want no added-task list", out)
	}
	coretest.SetChallengeStatus(t, ws, 1, "awaiting_completion_approval")
	out := runText(t, ws, "show", id)
	if !strings.Contains(out, "計画の後に足したタスク: 1 件\n  T-3 TITLE-C（出どころ: expand）\n") {
		t.Errorf("show text = %q, want the added task's title listed", out)
	}
	if strings.Contains(out, "TITLE-B（出どころ") {
		t.Errorf("show text = %q, want only the added task listed", out)
	}
}
