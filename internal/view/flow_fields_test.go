package view_test

import (
	"encoding/json"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// planned_flow・flow_reason・effort の写し（AC-135〜AC-141）。view は core の値を写すだけで、空は null になる。

func jsonKeys(t *testing.T, v any) map[string]json.RawMessage {
	t.Helper()
	var out map[string]json.RawMessage
	if err := json.Unmarshal([]byte(marshal(t, v)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTask_PlannedFlowAndFlowReason_AC135_AC138(t *testing.T) {
	got := view.FromTasks([]core.Task{
		{ID: "T-1", PlannedFlow: "ticket-light", FlowReason: "文書だけ"},
		{ID: "T-2"},
	})
	a, b := jsonKeys(t, got[0]), jsonKeys(t, got[1])
	if string(a["planned_flow"]) != `"ticket-light"` || string(a["flow_reason"]) != `"文書だけ"` {
		t.Errorf("T-1: %s %s", a["planned_flow"], a["flow_reason"])
	}
	if string(b["planned_flow"]) != "null" || string(b["flow_reason"]) != "null" {
		t.Errorf("T-2: %s %s, want null", b["planned_flow"], b["flow_reason"])
	}
}

func TestApprovalPlanTask_PlannedFlowAndFlowReason_AC139_AC140(t *testing.T) {
	p := &core.ApprovalPreview{Kind: core.ApprovalKindPlan, PlanTasks: []core.PlanTaskView{
		{Key: "a", Title: "A", PlannedFlow: "ticket-light", FlowReason: "理由"},
		{Key: "b", Title: "B"},
	}}
	s := view.FromApprovalPreview(p, nil)
	if s.Approval.Plan == nil || len(s.Approval.Plan.Tasks) != 2 {
		t.Fatalf("plan = %+v", s.Approval.Plan)
	}
	a, b := jsonKeys(t, s.Approval.Plan.Tasks[0]), jsonKeys(t, s.Approval.Plan.Tasks[1])
	if string(a["planned_flow"]) != `"ticket-light"` || string(a["flow_reason"]) != `"理由"` {
		t.Errorf("a: %s %s", a["planned_flow"], a["flow_reason"])
	}
	if string(b["planned_flow"]) != "null" || string(b["flow_reason"]) != "null" {
		t.Errorf("b: %s %s, want null", b["planned_flow"], b["flow_reason"])
	}
}

func TestTaskDelegateStep_Effort_AC141(t *testing.T) {
	medium := "medium"
	got := view.FromTasks([]core.Task{{ID: "T-1", Detail: core.TaskDetail{Delegate: &core.TaskDelegate{RunID: "R-1", Steps: []core.TaskDelegateStep{
		{Step: "implement", Effort: &medium}, {Step: "fix"},
	}}}}})
	steps := got[0].Delegate.Steps
	if len(steps) != 2 {
		t.Fatalf("steps = %+v", steps)
	}
	if string(jsonKeys(t, steps[0])["effort"]) != `"medium"` || string(jsonKeys(t, steps[1])["effort"]) != "null" {
		t.Errorf("effort = %s / %s", jsonKeys(t, steps[0])["effort"], jsonKeys(t, steps[1])["effort"])
	}
}
