package server

import (
	"strconv"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// 画面の承認の口（POST …/challenges/{id}/approve。本文は版だけ）でも、計画の承認はタスクの行と、答えの無い問いへの
// 推奨の記録を同じように行う（AC-97・AC-98。core の規則は internal/core/plan_answer_test.go が確かめる）。
const approvalPlanSpec = `{"verdict":"plan","size":"S","uses_integration":true,` +
	`"topics":[{"title":"T","decision":"D","questions":[` +
	`{"question":"Q-A","options":[{"label":"a1"},{"label":"a2"},{"label":"a3"}],"recommended":2},` +
	`{"question":"Q-B","options":[{"label":"b1"},{"label":"b2"}],"recommended":1}]}],` +
	`"tasks":[` +
	`{"key":"a","title":"TA","body":"b","done_criteria":"d","non_goals":[],"depends_on":[],"repo":"r","operation":"brief"},` +
	`{"key":"b","title":"TB","body":"b","done_criteria":"d","non_goals":[],"depends_on":["a"],"repo":"r","operation":"brief"},` +
	`{"key":"c","title":"TC","body":"b","done_criteria":"d","non_goals":[],"depends_on":["b"],"repo":"r","operation":"brief"}]}`

func TestApprovePlanThroughTheUIRecordsTasksAndRecommendedAnswers(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	coretest.SetPlanSpec(t, ws, 1, 1, approvalPlanSpec)
	port := approvalServer(t, ws)
	v := currentVersion(t, port)
	if code, body := apiPost(t, port, approvePath, `{"version":`+strconv.Itoa(v)+`}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}

	tasks := coretest.QueryRows(t, ws, `SELECT key, status, plan_version FROM task ORDER BY id`)
	if len(tasks) != 3 {
		t.Fatalf("task rows = %v, want 3 (AC-97)", tasks)
	}
	for _, r := range tasks {
		if r[1] != "pending" || r[2] != int64(1) {
			t.Errorf("task = %v, want pending in plan version 1", r)
		}
	}
	answers := coretest.QueryRows(t, ws, `SELECT question_number, choice, recommended, channel, verification FROM plan_answer ORDER BY question_number`)
	if len(answers) != 2 {
		t.Fatalf("plan_answer rows = %v, want 2 (AC-98)", answers)
	}
	want := []int64{2, 1}
	for i, r := range answers {
		if r[1] != want[i] || r[2] != int64(1) || r[3] != "ui" || r[4] != "none" {
			t.Errorf("answer = %v, want recommended choice %d via (ui, none)", r, want[i])
		}
	}
	if deps := coretest.QueryRows(t, ws, `SELECT COUNT(*) FROM task_dependency`); deps[0][0] != int64(2) {
		t.Errorf("task_dependency rows = %v, want 2", deps)
	}
	if u := coretest.QueryRows(t, ws, `SELECT uses_integration FROM challenge WHERE id = 1`); u[0][0] != int64(1) {
		t.Errorf("uses_integration = %v, want 1", u)
	}
}
