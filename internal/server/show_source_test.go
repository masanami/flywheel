package server

import (
	"encoding/json"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// 承認の要約の API の approval.plan.tasks の要素の source の試験（#778。S2b。AC-299）。

func TestApprovalSummary_AC299_PlanTasksCarrySource(t *testing.T) { // #778
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	coretest.SetPlanSpec(t, ws, 1, 1, `{"verdict":"plan","size":"S","uses_integration":true,"topics":[],"tasks":[`+
		`{"key":"a","title":"TA","body":"b","done_criteria":"d","non_goals":[],"depends_on":[],"repo":"r","operation":"brief","source":"o/r#8"},`+
		`{"key":"b","title":"TB","body":"b","done_criteria":"d","non_goals":[],"depends_on":["a"],"repo":"r","operation":"brief"}]}`)
	port := approvalServer(t, ws)
	var tasks []map[string]any
	if err := json.Unmarshal(planApproval(t, port)["tasks"], &tasks); err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("tasks = %v", tasks)
	}
	for i, tk := range tasks {
		if _, ok := tk["source"]; !ok {
			t.Errorf("AC-299 #778: tasks[%d] has no key source: %v", i, tk)
		}
	}
	if tasks[0]["source"] != "o/r#8" || tasks[1]["source"] != nil {
		t.Errorf("source = %v / %v, want o/r#8 and null", tasks[0]["source"], tasks[1]["source"])
	}
}
