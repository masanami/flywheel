package server

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// 区切り S4c-1: 課題の詳細の API の足されたタスクの印（tasks[].added）・足す依頼（additions[]）と、承認の要約の API の
// 足したタスク（added_tasks）・計画の上限と理由。足す依頼は、土台の道具で直接置いて確かめる。

func approvePlanThroughTheUI(t *testing.T, ws string) int {
	t.Helper()
	approvalFixture(t, ws, "awaiting_plan_approval")
	coretest.SetPlanSpec(t, ws, 1, 1, approvalPlanSpec)
	port := approvalServer(t, ws)
	if code, body := apiPost(t, port, approvePath, `{"version":`+strconv.Itoa(currentVersion(t, port))+`}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	return port
}

// AC-193〜AC-202: 課題の詳細の API の、足されたタスクの印と足す依頼の一覧。
func TestChallengeDetailAPI_AddedMarkAndAdditions(t *testing.T) {
	ws := initWorkspace(t)
	port := approvePlanThroughTheUI(t, ws)

	// 足す依頼が無いとき、additions は空の配列で、どのタスクの added も null。
	_, body := apiGet(t, port, challengePath)
	doc := decodeMap(t, body)
	if adds, ok := doc["additions"].([]any); !ok || len(adds) != 0 {
		t.Errorf("additions = %#v, want an empty array (AC-202)", doc["additions"])
	}
	for _, e := range doc["tasks"].([]any) {
		if v, has := e.(map[string]any)["added"]; !has || v != nil {
			t.Errorf("task.added = %v (present=%v), want present and null (AC-196)", v, has)
		}
	}

	ta := coretest.InsertTaskAddition(t, ws, coretest.TaskAdditionRow{ChallengeID: 1, OriginTaskID: 1, State: "awaiting_owner", TotalAfterMicros: 80_250_000, LimitMicros: 75_000_000})
	coretest.InsertTaskAddition(t, ws, coretest.TaskAdditionRow{ChallengeID: 1, Origin: "expand", OriginTaskID: 2, State: "superseded", DecidedAt: "2026-11-22T03:00:00.000Z"})
	coretest.Exec(t, ws, `UPDATE task SET addition_id = ?, created_at = '2026-11-22T00:30:00.000Z' WHERE id = 3`, ta)

	_, body = apiGet(t, port, challengePath)
	doc = decodeMap(t, body)
	tasks := doc["tasks"].([]any)
	added, _ := tasks[2].(map[string]any)["added"].(map[string]any)
	if added["origin"] != "expand" || added["origin_task_id"] != "T-1" || added["added_at"] != "2026-11-22T00:30:00.000Z" {
		t.Errorf("tasks[2].added = %v (AC-193〜AC-195)", added)
	}
	if tasks[0].(map[string]any)["added"] != nil {
		t.Errorf("tasks[0].added = %v, want null", tasks[0].(map[string]any)["added"])
	}
	adds := doc["additions"].([]any)
	if len(adds) != 2 {
		t.Fatalf("additions = %v, want 2 including the closed one (AC-201)", adds)
	}
	a0 := adds[0].(map[string]any)
	ids, _ := a0["task_ids"].([]any)
	if a0["id"] != "TA-1" || a0["origin"] != "expand" || a0["state"] != "awaiting_owner" || len(ids) != 1 || ids[0] != "T-3" ||
		a0["total_after_usd"] != 80.25 || a0["limit_usd"] != 75.0 || a0["origin_task_id"] != "T-1" || a0["decided_at"] != nil {
		t.Errorf("additions[0] = %v (AC-197〜AC-200)", a0)
	}
	if a1 := adds[1].(map[string]any); a1["state"] != "superseded" || a1["decided_at"] != "2026-11-22T03:00:00.000Z" {
		t.Errorf("additions[1] = %v", a1)
	}
}

// AC-185: 着手中の課題の詳細の budget.limit_usd は、課題に記録した上限。
func TestChallengeDetailAPI_BudgetLimitIsTheRecordedOne_AC185(t *testing.T) {
	ws := initWorkspace(t)
	port := approvePlanThroughTheUI(t, ws)
	coretest.SetChallengeBudgetLimit(t, ws, 1, 321_500_000, "続きを足す分")
	_, body := apiGet(t, port, challengePath)
	b := decodeMap(t, body)["budget"].(map[string]any)
	if b["limit_usd"] != 321.5 || b["limit_reason"] != "続きを足す分" {
		t.Errorf("budget = %v, want limit_usd 321.5 (the recorded one) with its reason", b)
	}
	if e, ok := b["estimate_usd"].(float64); !ok || e == 321.5 {
		t.Errorf("budget.estimate_usd = %v, want the sum of the task frames, not the limit", b["estimate_usd"])
	}
}

// AC-209: 完了の承認の要約の API に、計画の後に足したタスクが入る。
func TestApprovalSummaryAPI_CompletionListsAddedTasks_AC209(t *testing.T) {
	ws := initWorkspace(t)
	port := approvePlanThroughTheUI(t, ws)
	ta := coretest.InsertTaskAddition(t, ws, coretest.TaskAdditionRow{ChallengeID: 1, OriginTaskID: 1, State: "applied"})
	coretest.Exec(t, ws, `UPDATE task SET addition_id = ? WHERE id = 3`, ta)
	coretest.SetChallengeStatus(t, ws, 1, "awaiting_completion_approval")

	_, body := apiGet(t, port, approvalPath)
	var doc struct {
		Approval struct {
			AddedTasks []struct {
				ChallengeID string `json:"challenge_id"`
				Origin      string `json:"origin"`
				TaskID      string `json:"task_id"`
				Title       string `json:"title"`
			} `json:"added_tasks"`
		} `json:"approval"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if got := doc.Approval.AddedTasks; len(got) != 1 || got[0].ChallengeID != "C-1" || got[0].TaskID != "T-3" || got[0].Title != "TC" || got[0].Origin != "expand" {
		t.Errorf("added_tasks = %+v, want only T-3 (TC, expand)", got)
	}
}

// AC-189〜AC-192: 計画の承認の要約の API の plan は、見込み（budget_usd）と上限（budget_limit_usd）を持ち、
// 上限の理由（budget_limit_reason）は上限が見込みより大きいときだけ値を持つ。
func TestApprovalSummaryAPI_PlanCarriesEstimateLimitAndReason(t *testing.T) {
	type plan struct {
		Plan struct {
			BudgetUSD         *float64 `json:"budget_usd"`
			BudgetLimitUSD    *float64 `json:"budget_limit_usd"`
			BudgetLimitReason *string  `json:"budget_limit_reason"`
		} `json:"plan"`
	}
	read := func(t *testing.T, spec string) plan {
		t.Helper()
		ws := initWorkspace(t)
		approvalFixture(t, ws, "awaiting_plan_approval")
		coretest.SetPlanSpec(t, ws, 1, 1, spec)
		_, body := apiGet(t, approvalServer(t, ws), approvalPath)
		var doc struct {
			Approval plan `json:"approval"`
		}
		if err := json.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatal(err)
		}
		return doc.Approval
	}
	withBudget := func(extra string) string {
		return strings.Replace(approvalPlanSpec, `"uses_integration":true,`, `"uses_integration":true,"budget_impl_usd":10,"budget_review_usd":5,`+extra, 1)
	}

	same := read(t, withBudget(""))
	if same.Plan.BudgetUSD == nil || same.Plan.BudgetLimitUSD == nil || *same.Plan.BudgetLimitUSD != *same.Plan.BudgetUSD || same.Plan.BudgetLimitReason != nil {
		t.Fatalf("plan without a limit = %+v, want the limit equal to the estimate and no reason (AC-192)", same.Plan)
	}
	est := *same.Plan.BudgetUSD
	bigger := read(t, withBudget(`"budget_limit_usd":`+strconv.FormatFloat(est+20, 'f', -1, 64)+`,"budget_limit_reason":"調査の結果で足す分",`))
	if bigger.Plan.BudgetUSD == nil || *bigger.Plan.BudgetUSD != est || bigger.Plan.BudgetLimitUSD == nil || *bigger.Plan.BudgetLimitUSD != est+20 ||
		bigger.Plan.BudgetLimitReason == nil || *bigger.Plan.BudgetLimitReason != "調査の結果で足す分" {
		t.Errorf("plan with a larger limit = %+v, want estimate %v, limit %v and the reason (AC-189〜AC-191)", bigger.Plan, est, est+20)
	}
}
