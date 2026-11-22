package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// S4c-1 の AC-115: 足す依頼つきの保留（task_add_over_limit）の間に、タスクの枠の増額で見込みが増えた後に、選択肢 add で答えると、
// CLI は検証の誤りと同じ終了コードで終わる。足す依頼つきの保留を CLI から作る口は無いので、土台の道具（coretest）で直接置く。

const taskAdditionConnectorsJSON = `{
  "version": 1,
  "connectors": [
    {"id": "direct", "form": "brief", "permission_mode": "auto", "operations": [
      {"id": "brief", "interactive": false, "child_may_decide": true, "artifacts": "pr"}
    ]}
  ],
  "repos": [
    {"name": "harness-repo", "remote": "o/harness", "default_branch": "main", "connector": "direct"}
  ]
}`

// 承認済みの計画のタスク 2 個（枠は 3+2 USD ずつ。見込み 10 USD）。
const taskAdditionPlanSpec = `{"verdict":"plan","size":"M","uses_integration":true,"tasks":[
  {"key":"a","title":"a","body":"b","done_criteria":"d","non_goals":[],"depends_on":[],"repo":"harness-repo","operation":"brief","budget_impl_usd":3,"budget_review_usd":2},
  {"key":"b","title":"b","body":"b","done_criteria":"d","non_goals":[],"depends_on":[],"repo":"harness-repo","operation":"brief","budget_impl_usd":3,"budget_review_usd":2}]}`

// 足す依頼に保存した出力のタスク（枠は 3+2 USD。足した後の見込み 15 USD）。
const taskAdditionSavedTasks = `[{"key":"c","title":"続き c","body":"本文","done_criteria":"完了","non_goals":[],"depends_on":[],"repo":"harness-repo","operation":"brief","budget_impl_usd":3,"budget_review_usd":2}]`

const taskAdditionHoldOptions = `[{"id":"add","label":"足す","description":"上限を $15（足した後の見込み）に上げて、タスクを足す"},` +
	`{"id":"skip","label":"足さずに進める","description":"タスクを足さず、いまの計画のまま進める"},` +
	`{"id":"replan","label":"計画を作り直す","description":"計画を作り直す（動いている作業が終わるまで選べない）"}]`

// overLimitAdditionWorkspace は、上限 14 USD・見込み 10 USD の着手中の課題に、上限を超える足す依頼（足した後の見込み 15 USD）と、
// その保留（task_add_over_limit。選択肢 add の説明は $15）を置いた、保留中の課題を作る。
func overLimitAdditionWorkspace(t *testing.T) (ws, id string) {
	t.Helper()
	ws = initializedWorkspace(t)
	writeConnectorsJSONForTest(t, ws, taskAdditionConnectorsJSON)
	id = createForCase(t, ws)
	n := challengeIDToInternalID(t, id)
	coretest.InsertApprovedPlan(t, ws, n, "PLAN-BODY", taskAdditionPlanSpec)
	coretest.SetChallengeBudgetLimit(t, ws, n, 14_000_000, "続きを足す分")
	aid := coretest.InsertTaskAddition(t, ws, coretest.TaskAdditionRow{
		ChallengeID: n, OriginTaskID: 1, State: "awaiting_owner", Spec: taskAdditionSavedTasks, TotalAfterMicros: 15_000_000, LimitMicros: 14_000_000,
	})
	coretest.InsertTaskAdditionHold(t, ws, aid, "task_add_over_limit",
		"足すと上限を超えるので、足してよいかを決めてください。\n足した後の見込み: $15\n", "in_progress", taskAdditionHoldOptions)
	coretest.SetChallengeStatus(t, ws, n, "awaiting_human")
	return ws, id
}

func answerAddArgs(ws, id string) []string {
	return []string{"answer", id, "--option", "add", "--workspace", ws, "--relayed", "--session", "s", "--quote", "足す"}
}

// AC-115: タスクの枠の増額で見込みが増えた後に add で答えると、CLI は検証の誤りと同じ終了コード（1・validation_failed）で終わり、
// 新しい額で問いを出し直した旨を出す。保留は開いたまま・上限とタスクは変わらない。
func TestAC115_AddAfterTheEstimateGrewExitsLikeAValidationError(t *testing.T) {
	t.Parallel()
	ws, id := overLimitAdditionWorkspace(t)
	n := challengeIDToInternalID(t, id)
	// 保留の間のタスクの枠の増額（足した後の見込みが 15 → 16 USD）。
	coretest.Exec(t, ws, `UPDATE task SET budget_impl_micros = budget_impl_micros + 1000000 WHERE challenge_id = ? AND key = 'b'`, n)

	args := append(answerAddArgs(ws, id), "--json")
	var stdout, stderr bytes.Buffer
	code := runWithLookPath(args, strings.NewReader(""), &stdout, &stderr, defaultCommands(), lookPathFor(t))
	// 検証の誤り（answer --option を断る道）と同じ終了コード・エラーコード。
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (the same as a validation error; stderr=%s)", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty on failure", stdout.String())
	}
	var doc struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &doc); err != nil {
		t.Fatalf("stderr is not JSON: %v (%q)", err, stderr.String())
	}
	if doc.Error.Code != string(CodeValidationFailed) {
		t.Errorf("error code = %q, want %q", doc.Error.Code, CodeValidationFailed)
	}
	for _, want := range []string{"出し直しました", "$16"} {
		if !strings.Contains(doc.Error.Message, want) {
			t.Errorf("message = %q, want it to contain %q", doc.Error.Message, want)
		}
	}

	// 保留は開いたまま。タスクは足されていない。
	if got := workStatus(t, ws, id); got != "awaiting_human" {
		t.Errorf("status = %q, want awaiting_human", got)
	}
	h := anySlice(t, runJSON(t, ws, "show", id)["holds"], "holds")[0].(map[string]any)
	if h["answer"] != nil || h["answer_option"] != nil {
		t.Errorf("hold = %v, want it still open", h)
	}
	if n := coretestCount(t, ws, `SELECT COUNT(*) FROM task WHERE challenge_id = ? AND key = 'c'`, n); n != 0 {
		t.Errorf("tasks with key c = %d, want 0", n)
	}
}

// 対: 額が変わっていなければ、同じ add の回答は成立する（終了コード 0）。
func TestAC115_AddWithoutAChangeInTheEstimateSucceeds(t *testing.T) {
	t.Parallel()
	ws, id := overLimitAdditionWorkspace(t)
	runJSON(t, ws, answerAddArgs(ws, id)...)
	if got := workStatus(t, ws, id); got != "in_progress" {
		t.Errorf("status = %q, want in_progress", got)
	}
	if n := coretestCount(t, ws, `SELECT COUNT(*) FROM task WHERE challenge_id = ? AND key = 'c'`, challengeIDToInternalID(t, id)); n != 1 {
		t.Errorf("tasks with key c = %d, want 1", n)
	}
}

func coretestCount(t *testing.T, ws, query string, args ...any) int {
	t.Helper()
	rows := coretest.QueryRows(t, ws, query, args...)
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("rows = %v, want one count", rows)
	}
	switch v := rows[0][0].(type) {
	case int64:
		return int(v)
	case int:
		return v
	}
	t.Fatalf("count = %#v", rows[0][0])
	return 0
}
