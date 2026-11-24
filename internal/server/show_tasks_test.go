package server

import (
	"encoding/json"
	"reflect"
	"sort"
	"strconv"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
	"github.com/masanami/flywheel/internal/view"
)

// このファイルは docs/features/core-redesign.md の受入基準のうち、課題の詳細の API の `tasks`（AC-153）と、承認の要約の API の
// `approval.plan` の `topics`・`tasks`・`uses_integration`（AC-167〜AC-170。masanami/flywheel#755）を確かめる。

const challengePath = "/api/v1/workspaces/w/challenges/C-1"

func jsonObject(t *testing.T, raw json.RawMessage, what string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s = %s, want an object: %v", what, raw, err)
	}
	return m
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// AC-153: 課題の詳細の API の応答の根に tasks がある。タスクが無い課題では空の配列、計画の承認の後はタスクの要素が並ぶ。
func TestChallengeDetailAPI_CarriesTasks(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	port := approvalServer(t, ws)

	code, body := apiGet(t, port, challengePath)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	doc := decodeMap(t, body)
	tasks, has := doc["tasks"]
	if !has {
		t.Fatalf("response root has no tasks (AC-153): %s", body)
	}
	if arr, ok := tasks.([]any); !ok || len(arr) != 0 {
		t.Errorf("tasks = %#v, want an empty array when the challenge has no tasks", tasks)
	}
	ch := doc["challenge"].(map[string]any)
	if v, has := ch["uses_integration"]; !has || v != nil {
		t.Errorf("challenge.uses_integration = %v (present=%v), want present and null", v, has)
	}

	// 計画の承認の後は、タスクの要素が出る（行は承認が作る）。
	coretest.SetPlanSpec(t, ws, 1, 1, approvalPlanSpec)
	v := currentVersion(t, port)
	if code, body := apiPost(t, port, approvePath, `{"version":`+strconv.Itoa(v)+`}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	_, body = apiGet(t, port, challengePath)
	var after struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(body), &after); err != nil {
		t.Fatal(err)
	}
	if len(after.Tasks) != 3 || after.Tasks[0]["id"] != "T-1" || after.Tasks[0]["title"] != "TA" || after.Tasks[0]["status"] != "pending" {
		t.Fatalf("tasks = %v, want T-1..T-3", after.Tasks)
	}
	if deps, _ := after.Tasks[1]["depends_on"].([]any); len(deps) != 1 || deps[0] != "T-1" {
		t.Errorf("tasks[1].depends_on = %v, want [T-1]", after.Tasks[1]["depends_on"])
	}
}

// planApproval は計画承認待ちの課題（approvalPlanSpec の計画）の承認の要約の plan を返す。
func planApproval(t *testing.T, port int) map[string]json.RawMessage {
	t.Helper()
	code, body := apiGet(t, port, approvalPath)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var doc struct {
		Approval map[string]json.RawMessage `json:"approval"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	return jsonObject(t, doc.Approval["plan"], "approval.plan")
}

// AC-167〜AC-169: 計画の承認待ちの課題の承認の要約の approval.plan に、topics・tasks・uses_integration がある。
func TestApprovalSummary_PlanCarriesTopicsTasksAndUsesIntegration(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	coretest.SetPlanSpec(t, ws, 1, 1, approvalPlanSpec)
	port := approvalServer(t, ws)
	plan := planApproval(t, port)

	for _, k := range []string{"topics", "tasks", "uses_integration"} {
		if _, ok := plan[k]; !ok {
			t.Errorf("approval.plan has no key %q (AC-167〜169)", k)
		}
	}
	if string(plan["uses_integration"]) != "true" {
		t.Errorf("uses_integration = %s, want true", plan["uses_integration"])
	}
	var topics []struct {
		Title     string
		Decision  string
		Questions []map[string]any
	}
	if err := json.Unmarshal(plan["topics"], &topics); err != nil {
		t.Fatal(err)
	}
	if len(topics) != 1 || topics[0].Title != "T" || topics[0].Decision != "D" || len(topics[0].Questions) != 2 {
		t.Fatalf("topics = %+v", topics)
	}
	var tasks []map[string]any
	if err := json.Unmarshal(plan["tasks"], &tasks); err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("tasks = %v", tasks)
	}
	// 承認の前はタスクの ID が無いので、key・題名・前提の key・source（子に対応しなければ null）で出す。要素のキーはこの 4 つに、表示の区分（phase）・タスクの枠（budget_impl_usd・budget_review_usd。計画が書かなければ null。#906）・フローと理由（planned_flow・flow_reason。計画が選ばなければ null）を足した 9 つだけ（#802）。
	for i, want := range []struct {
		key, title string
		deps       []any
	}{{"a", "TA", []any{}}, {"b", "TB", []any{"a"}}, {"c", "TC", []any{"b"}}} {
		got := tasks[i]
		if len(got) != 9 || got["planned_flow"] != nil || got["flow_reason"] != nil || got["phase"] == nil || got["budget_impl_usd"] != nil || got["budget_review_usd"] != nil || got["source"] != nil || got["key"] != want.key || got["title"] != want.title || !reflect.DeepEqual(got["depends_on"], want.deps) {
			t.Errorf("tasks[%d] = %v, want key %s title %s depends_on %v", i, got, want.key, want.title, want.deps)
		}
	}
}

// AC-170: approval.plan.topics の問いの要素は、number・question・options・recommended・answer のキーを持つ。
// 答えの記録は選ばれた選択肢のラベルを持ち、答えが無い問いは answer が null。
func TestApprovalSummary_PlanQuestionsCarryTheDocumentedKeys(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	coretest.SetPlanSpec(t, ws, 1, 1, approvalPlanSpec)
	coretest.Exec(t, ws,
		`INSERT INTO plan_answer (challenge_id, plan_version, question_number, choice, text, recommended, actor, channel, verification, answered_at)
		 VALUES (1, 1, 1, 3, NULL, 0, 'tester', 'cli', 'tty_confirm', '2026-11-01T00:00:00.000Z')`)
	port := approvalServer(t, ws)
	plan := planApproval(t, port)

	var topics []struct {
		Questions []map[string]json.RawMessage
	}
	if err := json.Unmarshal(plan["topics"], &topics); err != nil {
		t.Fatal(err)
	}
	qs := topics[0].Questions
	if len(qs) != 2 {
		t.Fatalf("questions = %v", qs)
	}
	for i, q := range qs {
		got := keysOf(q)
		want := []string{"number", "question", "options", "recommended", "answer"}
		if len(got) != len(want) {
			t.Errorf("questions[%d] keys = %v, want exactly %v", i, got, want)
		}
		for _, k := range want {
			if _, ok := q[k]; !ok {
				t.Errorf("questions[%d] has no key %q", i, k)
			}
		}
	}
	if string(qs[0]["number"]) != "1" || string(qs[0]["question"]) != `"Q-A"` || string(qs[0]["recommended"]) != "2" ||
		string(qs[1]["number"]) != "2" || string(qs[1]["recommended"]) != "1" {
		t.Errorf("questions = %s / %s", qs[0], qs[1])
	}
	var opts []map[string]any
	if err := json.Unmarshal(qs[0]["options"], &opts); err != nil || len(opts) != 3 || opts[1]["label"] != "a2" {
		t.Errorf("options = %s (%v)", qs[0]["options"], err)
	}
	var ans map[string]any
	if err := json.Unmarshal(qs[0]["answer"], &ans); err != nil {
		t.Fatalf("answer = %s: %v", qs[0]["answer"], err)
	}
	if ans["choice"] != float64(3) || ans["label"] != "a3" || ans["recommended"] != false || ans["text"] != nil {
		t.Errorf("answer = %v, want choice 3 labelled a3, not recommended", ans)
	}
	if string(qs[1]["answer"]) != "null" {
		t.Errorf("answer of the unanswered question = %s, want null", qs[1]["answer"])
	}
}

// AC-233: 課題の詳細の API の応答の challenge と tasks[] の要素は、`flywheel show --json` と同じ型（internal/view）のキーを持つ。
// タスクの枠・使った額と統合先の識別は、記録のとおりの値で出る。
func TestChallengeDetailAPI_ChallengeAndTasksCarryTheSameKeysAsShowJSON(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_plan_approval")
	port := approvalServer(t, ws)
	coretest.SetPlanSpec(t, ws, 1, 1, approvalPlanSpec)
	v := currentVersion(t, port)
	if code, body := apiPost(t, port, approvePath, `{"version":`+strconv.Itoa(v)+`}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	coretest.Exec(t, ws, `UPDATE task SET budget_impl_micros = 30000000, budget_review_micros = 12500000 WHERE id = 1`)
	coretest.Exec(t, ws, `INSERT INTO run (challenge_id, kind, challenge_version, plan_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket, cost_usd, task_id, reflect)
		VALUES (1, 'delegate', 1, 1, 's', 1, 'h', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:01.000Z', 'succeeded', 1000000, 'impl', 2000000, 1, 0)`)
	coretest.Exec(t, ws, `UPDATE challenge SET integration_ref = 'flywheel/integration/c-1' WHERE id = 1`)

	code, body := apiGet(t, port, challengePath)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var doc struct {
		Challenge map[string]any   `json:"challenge"`
		Tasks     []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	keysOfType := func(v any) []string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	sortedKeys := func(m map[string]any) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	if got, want := sortedKeys(doc.Challenge), keysOfType(view.ShowChallenge{}); !reflect.DeepEqual(got, want) {
		t.Errorf("challenge keys = %v, want the show --json keys %v", got, want)
	}
	if len(doc.Tasks) == 0 {
		t.Fatal("no tasks")
	}
	if got, want := sortedKeys(doc.Tasks[0]), keysOfType(view.Task{}); !reflect.DeepEqual(got, want) {
		t.Errorf("tasks[0] keys = %v, want the show --json keys %v", got, want)
	}
	if doc.Tasks[0]["budget_impl_usd"] != float64(30) || doc.Tasks[0]["budget_review_usd"] != 12.5 || doc.Tasks[0]["spent_usd"] != float64(2) {
		t.Errorf("tasks[0] = %v, want frames 30 / 12.5 and spent 2", doc.Tasks[0])
	}
	if doc.Challenge["integration_ref"] != "flywheel/integration/c-1" {
		t.Errorf("challenge.integration_ref = %v", doc.Challenge["integration_ref"])
	}
}
