package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// タスクが対応する取り込み元の子を表示に出す試験（#778。S2b。AC-293・AC-298・AC-300）。
// 承認の要約の API の source（AC-299）は internal/server/show_source_test.go が確かめる。

// showSourceSpec は、タスク ka が子 o/r#8 に対応し、kb は子に対応しない計画。
const showSourceSpec = `{"verdict":"plan","size":"S","summary":"S","uses_integration":true,"topics":[],` +
	`"tasks":[` +
	`{"key":"ka","title":"TITLE-A","body":"BODY-A","done_criteria":"DC-A","non_goals":[],"depends_on":[],"repo":"repo-a","operation":"brief","source":"o/r#8"},` +
	`{"key":"kb","title":"TITLE-B","body":"BODY-B","done_criteria":"DC-B","non_goals":[],"depends_on":["ka"],"repo":"repo-b","operation":"brief"}]}`

func TestShowJSON_AC293_TasksCarrySourceKey(t *testing.T) { // #778
	t.Parallel()
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	coretest.InsertApprovedPlan(t, ws, challengeIDToInternalID(t, id), "BODY", showSourceSpec)
	tasks := objectList(t, runJSON(t, ws, "show", id)["tasks"], "tasks")
	if len(tasks) != 2 {
		t.Fatalf("tasks = %v", tasks)
	}
	for i, tk := range tasks {
		if _, ok := tk["source_key"]; !ok {
			t.Errorf("AC-293 #778: tasks[%d] has no key source_key: %v", i, tk)
		}
	}
	if tasks[0]["source_key"] != "o/r#8" || tasks[1]["source_key"] != nil {
		t.Errorf("source_key = %v / %v, want o/r#8 and null", tasks[0]["source_key"], tasks[1]["source_key"])
	}
}

func TestShowPlan_AC298_TaskLineShowsTheChildsExternalKey(t *testing.T) { // #778
	t.Parallel()
	ws := initializedWorkspace(t)
	id := createPlannedForCase(t, ws)
	coretest.SetPlanSpec(t, ws, challengeIDToInternalID(t, id), 1, showSourceSpec)
	text := runText(t, ws, "show", id, "--plan")
	if l := lineContaining(text, "TITLE-A"); !strings.Contains(l, "o/r#8") {
		t.Errorf("AC-298 #778: the task line = %q, want the child's external_key o/r#8", l)
	}
	if l := lineContaining(text, "TITLE-B"); strings.Contains(l, "取り込み元の子") {
		t.Errorf("a task without a child must not show one: %q", l)
	}
}

func TestApprove_AC300_SummaryShowsTheChildsExternalKey(t *testing.T) { // #778
	t.Parallel()
	ws := initializedWorkspace(t)
	id := createPlannedForCase(t, ws)
	coretest.SetPlanSpec(t, ws, challengeIDToInternalID(t, id), 1, showSourceSpec)
	code, ptyOutput, stdout, stderr := runConfirmedChild(t, []string{"approve", "--workspace", ws, "--json", id}, id)
	if code != 0 {
		t.Fatalf("approve exit = %d, stdout=%s stderr=%s pty=%s", code, stdout, stderr, ptyOutput)
	}
	if l := lineContaining(ptyOutput, "TITLE-A"); !strings.Contains(l, "o/r#8") {
		t.Errorf("AC-300 #778: the summary line = %q, want the child's external_key o/r#8:\n%s", l, ptyOutput)
	}
}
