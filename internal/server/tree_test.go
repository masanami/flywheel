package server

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルのテスト名・サブテストの名前の AC-<n> は、core の見直しの区切り S2c の課題（masanami/flywheel#802）の
// 受入基準の番号。課題のツリーの API と、課題の詳細・承認の要約・問いの一覧の拡張を、実ストア・実際の HTTP の要求で確かめる。
// ツリーの中身の規則（AC-2〜53）は internal/core/tree_test.go が確かめる。

const (
	treePath     = "/api/v1/workspaces/w/tree"
	treeElemKeys = "budget_usd children cost_usd dependencies followups_open id next_human_actions parent progress status status_label tasks tasks_done tasks_total title updated_at uses_integration"
)

type treeDoc struct {
	Challenges []map[string]any `json:"challenges"`
	RecentDone []map[string]any `json:"recent_done"`
}

func getTree(t *testing.T, port int) treeDoc {
	t.Helper()
	code, body := apiGet(t, port, treePath)
	if code != 200 {
		t.Fatalf("GET %s = %d %s", treePath, code, body)
	}
	var d treeDoc
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func addChallenges(t *testing.T, ws string, n int) {
	t.Helper()
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	for i := 0; i < n; i++ {
		if _, err := st.CreateChallenge(t.Context(), core.ChannelCLI, core.CreateInput{Title: "extra", DoneCriteria: "d"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTreeAPI_ShapeAndKeys_AC1_802(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	port := approvalServer(t, ws)
	code, body := apiGet(t, port, treePath)
	if code != 200 {
		t.Fatalf("AC-1: status = %d %s, want 200", code, body)
	}
	doc := decodeMap(t, body)
	assertKeys(t, "root", doc, "challenges", "recent_done")
	if arr, ok := doc["recent_done"].([]any); !ok || len(arr) != 0 {
		t.Errorf("recent_done = %#v, want an empty array", doc["recent_done"])
	}
	el := doc["challenges"].([]any)[0].(map[string]any)
	assertKeys(t, "challenges[0]", el, splitWords(treeElemKeys)...)
}

func splitWords(s string) []string {
	var out []string
	cur := ""
	for _, r := range s + " " {
		if r == ' ' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	return out
}

func TestTreeAPI_StoreNotFound_AC54_802(t *testing.T) {
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "none", Path: t.TempDir()})
	code, body := apiGet(t, port, "/api/v1/workspaces/none/tree")
	if code != 404 || errCode(t, body) != "store_not_found" {
		t.Errorf("AC-54: GET tree for a workspace without a store = %d %s, want 404 store_not_found", code, body)
	}
}

func TestTreeAPI_DoesNotAddActivities_AC55_802(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	port := approvalServer(t, ws)
	count := func() any { return coretest.QueryRows(t, ws, `SELECT COUNT(*) FROM activity`)[0][0] }
	before := count()
	getTree(t, port)
	getTree(t, port)
	if after := count(); after != before {
		t.Errorf("AC-55: activity rows %v -> %v, want unchanged", before, after)
	}
}

func TestTreeAPI_PostIs404_AC73_802(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	port := approvalServer(t, ws)
	if code, body := apiPost(t, port, treePath, `{}`); code != 404 {
		t.Errorf("AC-73: POST %s = %d %s, want 404", treePath, code, body)
	}
}

func TestChallengeDetailAPI_Tree_AC56to60_AC61_802(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	coretest.SetPlanSpec(t, ws, 1, 1, approvalPlanSpec)
	addChallenges(t, ws, 2)
	coretest.SetChallengeStatus(t, ws, 2, "done")
	coretest.SetChallengeStatus(t, ws, 3, "withdrawn")
	port := approvalServer(t, ws)

	detail := mustJSON(t, port, "/api/v1/workspaces/w/challenges/C-1")
	tree, ok := detail["tree"].(map[string]any)
	t.Run("AC-56 the root has tree whose id is the challenge's", func(t *testing.T) {
		if !ok || tree["id"] != "C-1" {
			t.Fatalf("tree = %v", detail["tree"])
		}
	})
	t.Run("AC-57 tree.tasks equals the tree API's element tasks", func(t *testing.T) {
		list := getTree(t, port)
		if len(list.Challenges) == 0 || !reflect.DeepEqual(tree["tasks"], list.Challenges[0]["tasks"]) || len(tree["tasks"].([]any)) != 3 {
			t.Errorf("detail tree.tasks = %v, tree API tasks = %v", tree["tasks"], list.Challenges[0]["tasks"])
		}
	})
	t.Run("AC-58 a done challenge has tree", func(t *testing.T) {
		if m := mustJSON(t, port, "/api/v1/workspaces/w/challenges/C-2"); m["tree"] == nil {
			t.Errorf("done challenge has no tree: %v", m)
		}
	})
	t.Run("AC-59 a withdrawn challenge has tree", func(t *testing.T) {
		if m := mustJSON(t, port, "/api/v1/workspaces/w/challenges/C-3"); m["tree"] == nil {
			t.Errorf("withdrawn challenge has no tree: %v", m)
		}
	})
	t.Run("AC-60 the root keeps waiting", func(t *testing.T) {
		if w, has := detail["waiting"].(map[string]any); !has || w["prerequisites"] == nil {
			t.Errorf("waiting = %v", detail["waiting"])
		}
	})
	t.Run("AC-61 flywheel show --json has no tree", func(t *testing.T) {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(cliJSON(t, ws, "show", "C-1")), &m); err != nil {
			t.Fatal(err)
		}
		if _, has := m["tree"]; has {
			t.Errorf("flywheel show --json has tree")
		}
	})
	t.Run("AC-62 without card, waiting and tree the detail equals flywheel show --json", func(t *testing.T) {
		delete(detail, "card")
		delete(detail, "waiting")
		delete(detail, "tree")
		b, _ := json.Marshal(detail)
		sameJSON(t, string(b), cliJSON(t, ws, "show", "C-1"))
	})
}

// AC-20（#802）: 取り下げた課題の tree.tasks の、取りやめのタスクの phase は cancelled。
func TestChallengeDetailAPI_WithdrawnTaskPhaseIsCancelled_AC20_802(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "in_progress")
	coretest.InsertApprovedPlan(t, ws, 1, "PLAN", approvalPlanSpec)
	coretest.Exec(t, ws, `UPDATE task SET status = 'cancelled'`)
	coretest.SetChallengeStatus(t, ws, 1, "withdrawn")
	port := approvalServer(t, ws)
	tree := mustJSON(t, port, "/api/v1/workspaces/w/challenges/C-1")["tree"].(map[string]any)
	tasks := tree["tasks"].([]any)
	if len(tasks) != 3 {
		t.Fatalf("tasks = %v", tasks)
	}
	for _, e := range tasks {
		if e.(map[string]any)["phase"] != "cancelled" {
			t.Errorf("phase = %v, want cancelled", e)
		}
	}
}

// AC-35（#802）: 終わっていないタスクを残したまま完了を承認すると、そのタスクの phase は cancelled になる
// （本物の承認の経路 POST …/approve で確かめる）。
func TestTreeAPI_CompletionApprovalCancelsUnfinishedTasks_AC35_802(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "in_progress")
	coretest.InsertApprovedPlan(t, ws, 1, "PLAN", approvalPlanSpec)
	coretest.Exec(t, ws, `UPDATE task SET status = 'done' WHERE key = 'a'`)
	coretest.SetChallengeStatus(t, ws, 1, "awaiting_completion_approval")
	port := approvalServer(t, ws)
	v := currentVersion(t, port)
	if code, body := apiPost(t, port, approvePath, `{"version":`+strconv.Itoa(v)+`}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	tree := mustJSON(t, port, "/api/v1/workspaces/w/challenges/C-1")["tree"].(map[string]any)
	phases := map[string]any{}
	for _, e := range tree["tasks"].([]any) {
		m := e.(map[string]any)
		phases[m["title"].(string)] = m["phase"]
	}
	if phases["TA"] != "done" || phases["TB"] != "cancelled" || phases["TC"] != "cancelled" {
		t.Errorf("phases = %v, want TA done, TB・TC cancelled", phases)
	}
}

// AC-40・AC-42（#802）: budget_usd は `flywheel show <ID> --plan --json` の plan.budget_usd と同じ（既定のサイズごとの額を含む）。
// AC-41: 計画が無い課題は null。
func TestTreeAPI_BudgetEqualsShowPlan_AC40_AC41_AC42_802(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	coretest.SetPlanSpec(t, ws, 1, 1, approvalPlanSpec) // size S で額を書かない（宣言のサイズごとの既定）
	addChallenges(t, ws, 1)
	port := approvalServer(t, ws)
	var show struct {
		Plan struct {
			BudgetUSD *float64 `json:"budget_usd"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(cliJSON(t, ws, "show", "C-1", "--plan")), &show); err != nil || show.Plan.BudgetUSD == nil {
		t.Fatalf("show --plan = %+v, %v", show, err)
	}
	list := getTree(t, port)
	if got := list.Challenges[0]["budget_usd"]; got == nil || got.(float64) != *show.Plan.BudgetUSD || got.(float64) != 165 {
		t.Errorf("AC-40・AC-42: budget_usd = %v, want %v (165: three tasks without frames, each filled with the plan amount 55)", got, *show.Plan.BudgetUSD)
	}
	if got := list.Challenges[1]["budget_usd"]; got != nil {
		t.Errorf("AC-41: budget_usd = %v for a challenge without a plan, want null", got)
	}
}

// AC-63〜AC-66（#802）: 承認の要約の approval.plan の summary・budget_usd は `show --plan --json` の plan.summary・plan.budget_usd と同じ。
func TestApprovalSummary_PlanSummaryAndBudget_AC63to66_802(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	coretest.SetPlanSpec(t, ws, 1, 1, approvalPlanSpec)
	port := approvalServer(t, ws)
	var show struct {
		Plan struct {
			Summary   *string  `json:"summary"`
			BudgetUSD *float64 `json:"budget_usd"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(cliJSON(t, ws, "show", "C-1", "--plan")), &show); err != nil {
		t.Fatal(err)
	}
	// approvalPlanSpec は summary を持たない。書いてある計画で確かめるため、summary を足した版へ差し替える。
	withSummary := `{"summary":"一言の要約",` + approvalPlanSpec[1:]
	coretest.SetPlanSpec(t, ws, 1, 1, withSummary)
	if err := json.Unmarshal([]byte(cliJSON(t, ws, "show", "C-1", "--plan")), &show); err != nil || show.Plan.Summary == nil || show.Plan.BudgetUSD == nil {
		t.Fatalf("show --plan = %+v, %v", show, err)
	}
	plan := planApproval(t, port)
	var summary *string
	var budget *float64
	if err := json.Unmarshal(plan["summary"], &summary); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(plan["budget_usd"], &budget); err != nil {
		t.Fatal(err)
	}
	t.Run("AC-63 summary equals show --plan", func(t *testing.T) {
		if summary == nil || *summary != *show.Plan.Summary || *summary != "一言の要約" {
			t.Errorf("summary = %v, want %q", summary, *show.Plan.Summary)
		}
	})
	t.Run("AC-64 budget_usd equals show --plan", func(t *testing.T) {
		if budget == nil || *budget != *show.Plan.BudgetUSD {
			t.Errorf("budget_usd = %v, want %v", budget, *show.Plan.BudgetUSD)
		}
	})
	t.Run("AC-66 a plan without amounts gets the size default per task, not null", func(t *testing.T) {
		if budget == nil || *budget != 165 {
			t.Errorf("budget_usd = %v, want 165 (size S default 55 for each of the three tasks without frames)", budget)
		}
	})
	t.Run("AC-65 an old-shape plan has summary null", func(t *testing.T) {
		coretest.Exec(t, ws, `UPDATE task_plan SET spec = NULL WHERE challenge_id = 1`)
		plan := planApproval(t, port)
		if string(plan["summary"]) != "null" {
			t.Errorf("summary = %s, want null", plan["summary"])
		}
	})
}

// AC-68・AC-69（#802）: 承認の要約の approval.plan.tasks の phase は、前提の無いタスクが next、前提のあるタスクが blocked。
func TestApprovalSummary_PlanTaskPhases_AC68_AC69_802(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	coretest.SetPlanSpec(t, ws, 1, 1, approvalPlanSpec)
	port := approvalServer(t, ws)
	var tasks []map[string]any
	if err := json.Unmarshal(planApproval(t, port)["tasks"], &tasks); err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 || tasks[0]["phase"] != "next" || tasks[1]["phase"] != "blocked" || tasks[2]["phase"] != "blocked" {
		t.Errorf("AC-68・AC-69: phases = %v, want next, blocked, blocked", tasks)
	}
}

// AC-70・AC-71（#802）: 問いの一覧の要素の task_id は、保留が付いているタスクの ID（付いていなければ null）。
func TestQuestionsAPI_TaskID_AC70_AC71_802(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "in_progress")
	coretest.InsertApprovedPlan(t, ws, 1, "PLAN", approvalPlanSpec)
	tid := coretest.QueryRows(t, ws, `SELECT id FROM task WHERE key = 'a'`)[0][0].(int64)
	coretest.Exec(t, ws, `INSERT INTO hold (challenge_id, question, from_status, raised_at, kind, task_id) VALUES (1, 'タスクの問い', 'in_progress', '2026-11-02T00:00:00.000Z', 'human', ?)`, tid)
	coretest.Exec(t, ws, `INSERT INTO hold (challenge_id, question, from_status, raised_at, kind) VALUES (1, '課題の問い', 'in_progress', '2026-11-03T00:00:00.000Z', 'human')`)
	port := approvalServer(t, ws)
	qs := mustJSON(t, port, "/api/v1/questions")["questions"].([]any)
	if len(qs) != 2 {
		t.Fatalf("questions = %v", qs)
	}
	if got := qs[0].(map[string]any)["task_id"]; got != "T-"+strconv.FormatInt(tid, 10) {
		t.Errorf("AC-70: task_id = %v, want T-%d", got, tid)
	}
	if got := qs[1].(map[string]any)["task_id"]; got != nil {
		t.Errorf("AC-71: task_id = %v, want null", got)
	}
}

// #929: ツリーの API は、core が決めた progress（タスクと課題）をそのまま返す。phase の値は変えない。
func TestTreeAPI_ProgressIsCoreValue_929(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "in_progress")
	coretest.InsertApprovedPlan(t, ws, 1, "PLAN", approvalPlanSpec)
	coretest.Exec(t, ws, `UPDATE task SET status = 'in_progress'`)
	port := approvalServer(t, ws)
	d := getTree(t, port)
	if len(d.Challenges) != 1 {
		t.Fatalf("challenges = %v", d.Challenges)
	}
	tasks := d.Challenges[0]["tasks"].([]any)
	if len(tasks) != 3 {
		t.Fatalf("tasks = %v, want 3", tasks)
	}
	if d.Challenges[0]["progress"] != nil {
		t.Errorf("challenge progress = %v, want null", d.Challenges[0]["progress"])
	}
	for _, e := range tasks {
		m := e.(map[string]any)
		if m["progress"] != "working" || m["phase"] != "running" {
			t.Errorf("task = %v, want progress working / phase running", m)
		}
	}
}

// #929: 統合先を使う課題が検証中のとき、課題の progress は checking で返る。
func TestTreeAPI_ChallengeProgressChecking_929(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "verifying")
	coretest.InsertApprovedPlan(t, ws, 1, "PLAN", approvalPlanSpec)
	coretest.Exec(t, ws, `UPDATE challenge SET uses_integration = 1 WHERE id = 1`)
	coretest.SetChallengeStatus(t, ws, 1, "verifying")
	port := approvalServer(t, ws)
	d := getTree(t, port)
	if len(d.Challenges) != 1 || d.Challenges[0]["progress"] != "checking" {
		t.Errorf("challenges = %v, want progress checking", d.Challenges)
	}
}
