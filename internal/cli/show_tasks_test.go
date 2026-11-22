package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは docs/features/core-redesign.md の受入基準のうち、タスク・問い・統合先を使うかを表示に出す課題
// （masanami/flywheel#755）の `flywheel show`・`flywheel show --plan`・`flywheel approve` の要約を確かめる
// （AC-144〜AC-166・AC-171・AC-172）。API の形（AC-153・AC-167〜AC-170）は internal/server/show_tasks_test.go、
// 型の生成（AC-173）は internal/view/cmd/gentypes のテストが確かめる。

// showTasksSpec は、方針・完了したら見えるもの・やらないこと・論点 1 つ（問い 2 件。Q1 は推奨 2、Q2 は推奨 1）・
// タスク 3 件（b は a に、c は b に前提を持つ）・統合先を使う・予算（実装 30 と レビュー対応 12.5）を持つ計画。
const showTasksSpec = `{"verdict":"plan","size":"M","summary":"SUMMARY-TEXT","uses_integration":true,` +
	`"budget_impl_usd":30,"budget_review_usd":12.5,` +
	`"expected":[{"kind":"cli","scene":"EXPECTED-SCENE","before":"BEFORE-TEXT","after":"AFTER-TEXT"}],` +
	`"non_goals":["NONGOAL-TEXT"],` +
	`"topics":[{"title":"TOPIC-TITLE","decision":"D","questions":[` +
	`{"question":"QTEXT-1","options":[{"label":"LBL-A1"},{"label":"LBL-A2"},{"label":"LBL-A3"}],"recommended":2},` +
	`{"question":"QTEXT-2","options":[{"label":"LBL-B1"},{"label":"LBL-B2"}],"recommended":1}]}],` +
	`"tasks":[` +
	`{"key":"ka","title":"TITLE-A","body":"BODY-A","done_criteria":"DC-A","non_goals":["NG-A"],"depends_on":[],"repo":"repo-a","operation":"brief"},` +
	`{"key":"kb","title":"TITLE-B","body":"BODY-B","done_criteria":"DC-B","non_goals":[],"depends_on":["ka"],"repo":"repo-b","operation":"brief"},` +
	`{"key":"kc","title":"TITLE-C","body":"BODY-C","done_criteria":"DC-C","non_goals":[],"depends_on":["kb"],"repo":"repo-c","operation":"brief"}]}`

// approvedTasksWorkspace は計画の承認の後（着手中。タスク T-1〜T-3 は pending）の課題を持つワークスペースを作る。
func approvedTasksWorkspace(t *testing.T) (ws, id string) {
	t.Helper()
	ws = initializedWorkspace(t)
	id = createForCase(t, ws)
	coretest.InsertApprovedPlan(t, ws, challengeIDToInternalID(t, id), "BODY", showTasksSpec)
	return ws, id
}

func objectList(t *testing.T, v any, what string) []map[string]any {
	t.Helper()
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("%s = %#v, want an array", what, v)
	}
	out := make([]map[string]any, 0, len(arr))
	for _, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("%s element = %#v, want an object", what, e)
		}
		out = append(out, m)
	}
	return out
}

// AC-144・AC-146〜AC-149: show --json の根に tasks があり、要素の id は T-<番号>、status は 5 値のどれか、depends_on は
// 前提のタスクの ID の配列、そのほかのキーも持つ。
func TestShowJSON_TasksCarryTheDocumentedKeys(t *testing.T) {
	t.Parallel()
	ws, id := approvedTasksWorkspace(t)
	doc := runJSON(t, ws, "show", id)

	raw, has := doc["tasks"]
	if !has {
		t.Fatal("show --json has no root key tasks (AC-144)")
	}
	tasks := objectList(t, raw, "tasks")
	if len(tasks) != 3 {
		t.Fatalf("tasks = %d elements, want 3", len(tasks))
	}
	statuses := map[string]bool{"pending": true, "in_progress": true, "verifying": true, "done": true, "cancelled": true}
	for i, tk := range tasks {
		wantID := []string{"T-1", "T-2", "T-3"}[i]
		if tk["id"] != wantID {
			t.Errorf("tasks[%d].id = %v, want %s (AC-146)", i, tk["id"], wantID)
		}
		if s, _ := tk["status"].(string); !statuses[s] {
			t.Errorf("tasks[%d].status = %v, want one of the 5 values (AC-147)", i, tk["status"])
		}
		for _, k := range []string{"title", "body", "done_criteria", "non_goals", "depends_on", "repo", "operation", "plan_version"} {
			if _, ok := tk[k]; !ok {
				t.Errorf("tasks[%d] has no key %q (AC-149)", i, k)
			}
		}
	}
	// AC-147: 承認の直後のタスクは pending。
	if tasks[0]["status"] != "pending" {
		t.Errorf("tasks[0].status = %v, want pending", tasks[0]["status"])
	}
	// AC-148: depends_on は前提のタスクの ID の配列（前提が無ければ空の配列）。
	wantDeps := [][]any{{}, {"T-1"}, {"T-2"}}
	for i, tk := range tasks {
		deps, ok := tk["depends_on"].([]any)
		if !ok || len(deps) != len(wantDeps[i]) {
			t.Fatalf("tasks[%d].depends_on = %#v, want %v (AC-148)", i, tk["depends_on"], wantDeps[i])
		}
		for j := range deps {
			if deps[j] != wantDeps[i][j] {
				t.Errorf("tasks[%d].depends_on = %v, want %v", i, deps, wantDeps[i])
			}
		}
	}
	// AC-149: 値も計画のとおり。
	if tasks[1]["title"] != "TITLE-B" || tasks[1]["body"] != "BODY-B" || tasks[1]["done_criteria"] != "DC-B" ||
		tasks[1]["repo"] != "repo-b" || tasks[1]["operation"] != "brief" || tasks[1]["plan_version"] != float64(1) {
		t.Errorf("tasks[1] = %v", tasks[1])
	}
	if ng, _ := tasks[0]["non_goals"].([]any); len(ng) != 1 || ng[0] != "NG-A" {
		t.Errorf("tasks[0].non_goals = %v, want [NG-A]", tasks[0]["non_goals"])
	}
}

// AC-147: 状態は課題の進みに合わせて 5 値のどれかで出る（進んだ状態も、そのまま出す）。
func TestShowJSON_TaskStatusFollowsTheStoredStatus(t *testing.T) {
	t.Parallel()
	ws, id := approvedTasksWorkspace(t)
	for tid, st := range map[int]string{1: "in_progress", 2: "verifying", 3: "cancelled"} {
		coretest.Exec(t, ws, `UPDATE task SET status = ? WHERE id = ?`, st, tid)
	}
	coretest.Exec(t, ws, `UPDATE task SET status = 'done' WHERE id = 1`)
	tasks := objectList(t, runJSON(t, ws, "show", id)["tasks"], "tasks")
	want := []string{"done", "verifying", "cancelled"}
	for i, tk := range tasks {
		if tk["status"] != want[i] {
			t.Errorf("tasks[%d].status = %v, want %s", i, tk["status"], want[i])
		}
	}
}

// AC-145: タスクが 0 個の課題では、tasks は空の配列（null でも無いキーでもない）。
func TestShowJSON_TasksIsAnEmptyArrayWhenTheChallengeHasNoTasks(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	doc := runJSON(t, ws, "show", id)
	tasks, ok := doc["tasks"].([]any)
	if !ok || len(tasks) != 0 {
		t.Fatalf("tasks = %#v, want an empty array", doc["tasks"])
	}
}

// AC-150・AC-151: runs[] と holds[] の要素に task_id がある（タスクに結び付くものは T-<番号>、結び付かないものは null）。
func TestShowJSON_RunsAndHoldsCarryTaskID(t *testing.T) {
	t.Parallel()
	ws, id := approvedTasksWorkspace(t)
	insertRunFixture(t, ws, id, func(in *coretest.InsertRunInput) {
		in.SessionID = "11111111-1111-1111-1111-000000000001"
		in.EndedAt = "2026-09-28T00:01:00.000Z"
		in.Result = "succeeded"
	})
	insertRunFixture(t, ws, id, func(in *coretest.InsertRunInput) {
		in.SessionID = "11111111-1111-1111-1111-000000000002"
		in.EndedAt = "2026-09-28T00:02:00.000Z"
		in.Result = "succeeded"
	})
	coretest.Exec(t, ws, `UPDATE run SET task_id = 2 WHERE id = 2`)
	coretest.InsertHold(t, ws, challengeIDToInternalID(t, id), "Q-TASK", "in_progress", "2026-09-28T00:00:00.000Z", nil)
	coretest.InsertHold(t, ws, challengeIDToInternalID(t, id), "Q-CHALLENGE", "in_progress", "2026-09-28T00:00:01.000Z", nil)
	coretest.Exec(t, ws, `UPDATE hold SET task_id = 3 WHERE question = 'Q-TASK'`)

	doc := runJSON(t, ws, "show", id)
	runs := objectList(t, doc["runs"], "runs") // 新しい順
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(runs))
	}
	for i, want := range []any{"T-2", nil} {
		v, has := runs[i]["task_id"]
		if !has || v != want {
			t.Errorf("runs[%d].task_id = %v (present=%v), want %v (AC-150)", i, v, has, want)
		}
	}
	holds := objectList(t, doc["holds"], "holds")
	if len(holds) != 2 {
		t.Fatalf("holds = %d, want 2", len(holds))
	}
	for i, want := range []any{"T-3", nil} {
		v, has := holds[i]["task_id"]
		if !has || v != want {
			t.Errorf("holds[%d].task_id = %v (present=%v), want %v (AC-151)", i, v, has, want)
		}
	}
}

// AC-152: challenge に uses_integration がある。計画の承認で決まった値（真）、偽、未決（null）がそのまま出る。
func TestShowJSON_ChallengeCarriesUsesIntegration(t *testing.T) {
	t.Parallel()
	ws, id := approvedTasksWorkspace(t)
	ch := runJSON(t, ws, "show", id)["challenge"].(map[string]any)
	if v, has := ch["uses_integration"]; !has || v != true {
		t.Errorf("challenge.uses_integration = %v (present=%v), want true", v, has)
	}
	coretest.Exec(t, ws, `UPDATE challenge SET uses_integration = 0`)
	if v := runJSON(t, ws, "show", id)["challenge"].(map[string]any)["uses_integration"]; v != false {
		t.Errorf("challenge.uses_integration = %v, want false", v)
	}
	coretest.Exec(t, ws, `UPDATE challenge SET uses_integration = NULL`)
	ch = runJSON(t, ws, "show", id)["challenge"].(map[string]any)
	if v, has := ch["uses_integration"]; !has || v != nil {
		t.Errorf("challenge.uses_integration = %v (present=%v), want null", v, has)
	}
}

// AC-154・AC-155: show（文の出力）に、タスクの題名と状態が出る。
func TestShowText_ListsTaskTitlesAndStatuses(t *testing.T) {
	t.Parallel()
	ws, id := approvedTasksWorkspace(t)
	coretest.Exec(t, ws, `UPDATE task SET status = 'in_progress' WHERE id = 2`)
	text := runText(t, ws, "show", id)
	for _, want := range []string{"TITLE-A", "TITLE-B", "TITLE-C"} {
		if !strings.Contains(text, want) {
			t.Errorf("show text lacks the task title %q (AC-154):\n%s", want, text)
		}
	}
	lineOf := func(title string) string {
		for _, l := range strings.Split(text, "\n") {
			if strings.Contains(l, title) {
				return l
			}
		}
		return ""
	}
	if l := lineOf("TITLE-A"); !strings.Contains(l, "T-1") || !strings.Contains(l, "pending") || !strings.Contains(l, "未着手") {
		t.Errorf("task line = %q, want the ID and the status of T-1 (AC-155)", l)
	}
	if l := lineOf("TITLE-B"); !strings.Contains(l, "in_progress") || !strings.Contains(l, "作業中") {
		t.Errorf("task line = %q, want the status of T-2 (AC-155)", l)
	}
}

// planViewWorkspace は計画の承認待ち（問い 2 件・タスク 3 件の計画の版 1）の課題を持つワークスペースを作る。
func planViewWorkspace(t *testing.T) (ws, id string) {
	t.Helper()
	ws = initializedWorkspace(t)
	id = createPlannedForCase(t, ws)
	coretest.SetPlanSpec(t, ws, challengeIDToInternalID(t, id), 1, showTasksSpec)
	return ws, id
}

func lineContaining(text, needle string) string {
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, needle) {
			return l
		}
	}
	return ""
}

// AC-156〜AC-159・AC-161〜AC-164: show --plan に、方針・問い・推奨の印・タスクの題名・完了したら見えるもの・
// やらないこと・予算の見込み（実装の枠とレビュー対応の枠の合計）・統合先を使うかが出る。
func TestShowPlan_PrintsTheLatestPlanBeforeApproval(t *testing.T) {
	t.Parallel()
	ws, id := planViewWorkspace(t)
	text := runText(t, ws, "show", id, "--plan")

	for name, want := range map[string]string{
		"AC-156 summary":   "SUMMARY-TEXT",
		"AC-157 question1": "QTEXT-1",
		"AC-157 question2": "QTEXT-2",
		"AC-159 task A":    "TITLE-A",
		"AC-159 task B":    "TITLE-B",
		"AC-159 task C":    "TITLE-C",
		"AC-161 expected":  "EXPECTED-SCENE",
		"AC-162 non_goals": "NONGOAL-TEXT",
		"AC-163 budget":    "127.5",
		"AC-164 uses":      "統合先を使うか: 使う",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("%s: show --plan lacks %q:\n%s", name, want, text)
		}
	}
	// AC-158: 推奨の選択肢（Q1 は 2 番、Q2 は 1 番）にだけ「推奨」の印が付く。
	for needle, wantMark := range map[string]bool{"LBL-A1": false, "LBL-A2": true, "LBL-A3": false, "LBL-B1": true, "LBL-B2": false} {
		l := lineContaining(text, needle)
		if l == "" {
			t.Fatalf("no line for option %s:\n%s", needle, text)
		}
		if got := strings.Contains(l, "推奨"); got != wantMark {
			t.Errorf("option line %q: has the recommended mark = %v, want %v (AC-158)", l, got, wantMark)
		}
	}
}

// AC-160: 計画の中で B の前提が A のとき、承認の前の show --plan の B の行に、A の識別子（key）が前提として出る。
func TestShowPlan_BeforeApprovalShowsPrerequisitesByKey(t *testing.T) {
	t.Parallel()
	ws, id := planViewWorkspace(t)
	text := runText(t, ws, "show", id, "--plan")
	if l := lineContaining(text, "TITLE-B"); !strings.Contains(l, "前提") || !strings.Contains(l, "ka") {
		t.Errorf("B line = %q, want the prerequisite key ka (AC-160)", l)
	}
	if l := lineContaining(text, "TITLE-C"); !strings.Contains(l, "kb") || strings.Contains(l, "ka") {
		t.Errorf("C line = %q, want only the prerequisite key kb", l)
	}
	if l := lineContaining(text, "TITLE-A"); strings.Contains(l, "前提") {
		t.Errorf("A line = %q, must not list a prerequisite", l)
	}
}

// AC-160: 承認の後は、B の行に A のタスクの ID が前提として出る（key ではない）。
func TestShowPlan_AfterApprovalShowsPrerequisitesByTaskID(t *testing.T) {
	t.Parallel()
	ws, id := approvedTasksWorkspace(t)
	text := runText(t, ws, "show", id, "--plan")
	b := lineContaining(text, "TITLE-B")
	if !strings.Contains(b, "T-2") || !strings.Contains(b, "前提") || !strings.Contains(b, "T-1") || strings.Contains(b, "ka") {
		t.Errorf("B line = %q, want T-2 with the prerequisite T-1 (AC-160)", b)
	}
	if c := lineContaining(text, "TITLE-C"); !strings.Contains(c, "T-3") || !strings.Contains(c, "T-2") {
		t.Errorf("C line = %q, want T-3 with the prerequisite T-2", c)
	}
}

// AC-165: 答えが記録されている問いは、選ばれた選択肢のラベルが出る（文の答えは文が出る）。
func TestShowPlan_PrintsTheLabelOfTheChosenOption(t *testing.T) {
	t.Parallel()
	ws, id := planViewWorkspace(t)
	if code, stderr := runAnswerCLI(t, ws, relayedAnswerArgs(id, "--choice", "Q1=3", "--text", "Q2=TEXT-ANSWER")...); code != 0 {
		t.Fatalf("answer exit=%d stderr=%s", code, stderr)
	}
	// 推奨と違う答えで課題は分類済へ戻る。計画の承認待ちに戻して（再計画の代わりに）、同じ版の計画を読む。
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "awaiting_plan_approval")
	text := runText(t, ws, "show", id, "--plan")
	if l := lineContaining(text, "答え: 3) LBL-A3"); l == "" || !strings.Contains(l, "推奨と違う") {
		t.Errorf("show --plan lacks the label of the chosen option for Q1 (AC-165):\n%s", text)
	}
	if !strings.Contains(text, "TEXT-ANSWER") {
		t.Errorf("show --plan lacks the text answer for Q2:\n%s", text)
	}
	// 同じ問いに答えが複数あるときは後の行を使う。
	if code, stderr := runAnswerCLI(t, ws, relayedAnswerArgs(id, "--choice", "Q1=2")...); code != 0 {
		t.Fatalf("answer exit=%d stderr=%s", code, stderr)
	}
	text = runText(t, ws, "show", id, "--plan")
	if l := lineContaining(text, "答え: 2) LBL-A2"); l == "" || !strings.Contains(l, "推奨と同じ") || strings.Contains(text, "答え: 3) LBL-A3") {
		t.Errorf("show --plan must use the later answer for Q1:\n%s", text)
	}
}

// AC-166: 計画が無い課題に show --plan を実行すると、validation_failed のエラーになる。
func TestShowPlan_WithoutAPlanIsValidationFailed(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	requireErrorCode(t, []string{"show", id, "--plan", "--workspace", ws}, 1, CodeValidationFailed)
}

// show --plan --json は、計画の中身を構造化して返す（予算は合計の額）。
func TestShowPlanJSON_CarriesThePlanOverview(t *testing.T) {
	t.Parallel()
	ws, id := planViewWorkspace(t)
	plan := runJSON(t, ws, "show", id, "--plan")["plan"].(map[string]any)
	if plan["summary"] != "SUMMARY-TEXT" || plan["budget_usd"] != 127.5 || plan["uses_integration"] != true || plan["version"] != float64(1) {
		t.Errorf("plan = %v", plan)
	}
	tasks := objectList(t, plan["tasks"], "plan.tasks")
	if len(tasks) != 3 || tasks[1]["key"] != "kb" || tasks[1]["id"] != nil {
		t.Errorf("plan.tasks = %v, want 3 tasks with keys and no IDs before approval", tasks)
	}
}

// AC-171・AC-172: 端末の approve が確認の前に出す要約に、問いの文とタスクの題名が含まれる。
func TestApprove_PlanSummaryShowsQuestionsAndTaskTitles(t *testing.T) {
	t.Parallel()
	ws, id := planViewWorkspace(t)
	code, ptyOutput, stdout, stderr := runConfirmedChild(t, []string{"approve", "--workspace", ws, "--json", id}, id)
	if code != 0 {
		t.Fatalf("approve exit = %d, stdout=%s stderr=%s pty=%s", code, stdout, stderr, ptyOutput)
	}
	for name, want := range map[string]string{
		"AC-171 Q1": "QTEXT-1", "AC-171 Q2": "QTEXT-2",
		"AC-172 A": "TITLE-A", "AC-172 B": "TITLE-B", "AC-172 C": "TITLE-C",
	} {
		if !strings.Contains(ptyOutput, want) {
			t.Errorf("%s: the summary before the confirmation lacks %q:\n%s", name, want, ptyOutput)
		}
	}
	if l := lineContaining(ptyOutput, "TITLE-B"); !strings.Contains(l, "ka") {
		t.Errorf("B line = %q, want the prerequisite key", l)
	}
	if !strings.Contains(ptyOutput, "統合先を使うか: 使う") {
		t.Errorf("the summary lacks whether the integration branch is used:\n%s", ptyOutput)
	}
}

// AC-202: 計画が額を書いていないとき、`show --plan` の予算の見込みは、宣言のサイズごとの既定の合計で出る（「未設定」にならない。
// 委譲が使う額と同じ規則）。初期の宣言のサイズ S の既定は、実装 30・レビュー対応 25。
func TestShowPlan_BudgetFallsBackToTheSizeDefaults(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	id := createPlannedForCase(t, ws)
	coretest.SetPlanSpec(t, ws, challengeIDToInternalID(t, id), 1,
		`{"verdict":"plan","size":"S","summary":"S","topics":[],"uses_integration":false,"tasks":[`+
			`{"key":"t1","title":"TT","body":"B","done_criteria":"D","non_goals":[],"depends_on":[],"repo":"direct-repo","operation":"brief-op"}]}`)
	text := runText(t, ws, "show", id, "--plan")
	line := lineContaining(text, "タスクのツリー")
	if !strings.Contains(line, "見込み 55 USD") {
		t.Errorf("the plan line = %q, want the size default 55 USD (30 + 25)", line)
	}
}
