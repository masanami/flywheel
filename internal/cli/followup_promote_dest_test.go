package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// 区切り S4c-2 の 3: followup promote の上げる先（AC-1〜AC-19・AC-48〜AC-51）の CLI の面。

type promoteDoc struct {
	Challenge          *struct{ ID string } `json:"challenge"`
	Destination        string               `json:"destination"`
	NewChallengeReason *string              `json:"new_challenge_reason"`
	TaskAdditionID     *string              `json:"task_addition_id"`
	Followup           struct {
		Status string `json:"status"`
	} `json:"followup"`
}

func promoteRun(t *testing.T, ws, id string, extra ...string) (int, string, string) {
	t.Helper()
	args := append(fuArgs("followup promote "+id+" "+fuRelayed), extra...)
	return fuRun(t, ws, args...)
}

// 足せる着手中の課題 C-1（見込み 10 USD・上限 14 USD）に属する未のフォローアップ F-1 を置く。
func addableFollowupWorkspace(t *testing.T) string {
	t.Helper()
	ws, _ := overLimitAdditionWorkspaceBase(t)
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{ChallengeID: 1, Title: "続き", Detail: "説明"})
	return ws
}

func overLimitAdditionWorkspaceBase(t *testing.T) (ws, id string) {
	t.Helper()
	ws = initializedWorkspace(t)
	writeConnectorsJSONForTest(t, ws, taskAdditionConnectorsJSON)
	id = createForCase(t, ws)
	n := challengeIDToInternalID(t, id)
	coretest.InsertApprovedPlan(t, ws, n, "PLAN-BODY", taskAdditionPlanSpec)
	coretest.SetChallengeBudgetLimit(t, ws, n, 14_000_000, "続きを足す分")
	return ws, id
}

// AC-1・AC-2・AC-17〜AC-19・AC-48〜AC-50: 足せる課題のフォローアップは、元の課題のタスクとして上がり、出力に種類と足す依頼の ID が入る。
func TestFollowupPromote_TaskDestination_JSONCarriesKindAndAdditionID(t *testing.T) {
	ws := addableFollowupWorkspace(t)
	code, stdout, stderr := promoteRun(t, ws, "F-1", "--budget-impl-usd", "1", "--budget-review-usd", "0.5")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	var doc promoteDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Destination != "task" || doc.TaskAdditionID == nil || *doc.TaskAdditionID != "TA-1" || doc.Challenge != nil || doc.NewChallengeReason != nil {
		t.Errorf("doc = %+v (AC-49・AC-50)", doc)
	}
	if doc.Followup.Status != "open" || fuStatus(t, ws, 1) != "open" {
		t.Errorf("followup status = %s (AC-48)", doc.Followup.Status)
	}
	if got := fuCount(t, ws, `SELECT COUNT(*) FROM challenge`); got != "1" {
		t.Errorf("challenges = %s, want 1 (AC-1)", got)
	}
	rows := coretest.QueryRows(t, ws, `SELECT origin, state, followup_id, budget_impl_micros, budget_review_micros FROM task_addition`)
	if len(rows) != 1 || rows[0][0] != "followup" || rows[0][1] != "pending" || rows[0][3].(int64) != 1_000_000 || rows[0][4].(int64) != 500_000 {
		t.Errorf("task_addition rows = %v", rows)
	}
}

// AC-4・AC-5・AC-51: --new-challenge は新しい課題になり、足す依頼は作らない。出力に種類（課題）が入る。
func TestFollowupPromote_NewChallengeFlag_JSONCarriesKindChallenge(t *testing.T) {
	ws := addableFollowupWorkspace(t)
	code, stdout, stderr := promoteRun(t, ws, "F-1", "--new-challenge")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	var doc promoteDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Destination != "challenge" || doc.Challenge == nil || doc.TaskAdditionID != nil {
		t.Errorf("doc = %+v (AC-51)", doc)
	}
	if got := fuCount(t, ws, `SELECT COUNT(*) FROM task_addition`); got != "0" {
		t.Errorf("task_addition rows = %s, want 0 (AC-5)", got)
	}
}

// AC-8・AC-15・AC-16: タスクにしなかった理由が出力に出る（課題に属さないフォローアップ）。
func TestFollowupPromote_NewChallengeReasonIsShown(t *testing.T) {
	ws := fuWorkspace(t)
	code, stdout, stderr := promoteRun(t, ws, "F-3", "--json")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	var doc promoteDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Destination != "challenge" || doc.NewChallengeReason == nil || *doc.NewChallengeReason == "" {
		t.Errorf("doc = %+v", doc)
	}
	// テキスト出力（--json なし）にも理由が出る。
	ws2 := fuWorkspace(t)
	var out, errb bytes.Buffer
	args := append(fuArgs("followup promote F-3 "+fuRelayed), "--workspace", ws2)
	if code := runWithLookPath(args, strings.NewReader(""), &out, &errb, defaultCommands(), lookPathFor(t)); code != 0 || !strings.Contains(out.String(), "タスクにしなかった理由") {
		t.Errorf("text output = %q (exit %d, %s)", out.String(), code, errb.String())
	}
}

// 枠の指定が数でなければ usage_error（終了コード 2）。
func TestFollowupPromote_BadBudgetFlagIsUsageError(t *testing.T) {
	ws := addableFollowupWorkspace(t)
	if code, _, _ := promoteRun(t, ws, "F-1", "--budget-impl-usd", "abc"); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if got := fuCount(t, ws, `SELECT COUNT(*) FROM task_addition`); got != "0" {
		t.Errorf("task_addition rows = %s", got)
	}
}

// 足した後の見込みが上限を超えるタスクは、本人確認の前に検証の誤り（終了コード 1）で断り、何も書かない。
func TestFollowupPromote_OverLimitIsRefusedBeforeConfirmation(t *testing.T) {
	ws := addableFollowupWorkspace(t)
	code, _, stderr := promoteRun(t, ws, "F-1", "--budget-impl-usd", "30", "--budget-review-usd", "25")
	if code != 1 || !strings.Contains(stderr, "validation_failed") || !strings.Contains(stderr, "を超えます") {
		t.Errorf("exit=%d stderr=%s", code, stderr)
	}
	if got := fuCount(t, ws, `SELECT COUNT(*) FROM task_addition`); got != "0" {
		t.Errorf("task_addition rows = %s", got)
	}
}
