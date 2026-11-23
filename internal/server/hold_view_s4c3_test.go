package server

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// 区切り S4c-3 の見せ方（AC-108〜AC-115）。課題の詳細の API の holds（画面の課題の詳細の待ち・タスクの詳細が読む）と、問いの一覧に、
// 保留の ID（hold_id）と止める範囲（scope）が出る。

const twoTaskPlanSpec = `{"verdict":"plan","size":"M","uses_integration":true,"tasks":[
  {"key":"a","title":"a","body":"b","done_criteria":"d","non_goals":[],"depends_on":[],"repo":"harness-repo","operation":"brief","budget_impl_usd":3,"budget_review_usd":2},
  {"key":"b","title":"b","body":"b","done_criteria":"d","non_goals":[],"depends_on":[],"repo":"harness-repo","operation":"brief","budget_impl_usd":3,"budget_review_usd":2}]}`

// twoTaskHoldsWorkspace は、タスク a・b を止める保留（H-1・H-2）が開いている人間対応待ちの課題 C-1 を持つ。
func twoTaskHoldsWorkspace(t *testing.T) string {
	t.Helper()
	ws := initWorkspace(t)
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateChallenge(context.Background(), core.ChannelCLI, core.CreateInput{Title: "課題", DoneCriteria: "完了"}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	coretest.InsertApprovedPlan(t, ws, 1, "PLAN-BODY", twoTaskPlanSpec)
	coretest.InsertTaskHold(t, ws, 1, "a", "a への問い", "in_progress", "2026-11-22T00:00:00.000Z", "")
	coretest.InsertTaskHold(t, ws, 1, "b", "b への問い", "in_progress", "2026-11-22T00:00:01.000Z", "")
	coretest.SetChallengeStatus(t, ws, 1, "awaiting_human")
	return ws
}

func TestChallengeAPI_HoldsAreAllOpenHoldsWithHoldIDAndScope(t *testing.T) { // AC-108, AC-109, AC-110, AC-114, AC-115
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	ws := twoTaskHoldsWorkspace(t)
	port := startServerAt(t, now, core.FleetWorkspace{Name: "w", Path: ws})
	holds := mustJSON(t, port, "/api/v1/workspaces/w/challenges/C-1")["holds"].([]any)
	if len(holds) != 2 {
		t.Fatalf("holds = %d, want 2", len(holds))
	}
	var ids, scopes []string
	for _, h := range holds {
		m := h.(map[string]any)
		ids = append(ids, m["hold_id"].(string))
		scopes = append(scopes, m["scope"].(string))
	}
	if !reflect.DeepEqual(ids, []string{"H-1", "H-2"}) || !reflect.DeepEqual(scopes, []string{"task", "task"}) {
		t.Errorf("hold_id = %v, scope = %v, want [H-1 H-2] / [task task]", ids, scopes)
	}
}

func TestQuestionsAPI_OneQuestionPerOpenHoldWithHoldIDAndScope(t *testing.T) { // AC-111, AC-112, AC-113
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	ws := twoTaskHoldsWorkspace(t)
	port := startServerAt(t, now, core.FleetWorkspace{Name: "w", Path: ws})
	qs := mustJSON(t, port, "/api/v1/questions")["questions"].([]any)
	if len(qs) != 2 {
		t.Fatalf("questions = %d, want 2", len(qs))
	}
	for i, want := range []string{"H-1", "H-2"} {
		q := qs[i].(map[string]any)
		if q["hold_id"] != want || q["scope"] != "task" || q["challenge_id"] != "C-1" {
			t.Errorf("questions[%d] = %v, want hold_id %s / scope task", i, q, want)
		}
	}
}

func TestCardAPI_TaskHoldOnlyChallengeOffersAnswerQuestion(t *testing.T) { // AC-102
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	ws := twoTaskHoldsWorkspace(t)
	port := startServerAt(t, now, core.FleetWorkspace{Name: "w", Path: ws})
	for _, c := range mustJSON(t, port, "/api/v1/workspaces/w/challenges")["challenges"].([]any) {
		m := c.(map[string]any)
		if m["id"] == "C-1" {
			if got := strs(m["card"].(map[string]any)["next_human_actions"]); !reflect.DeepEqual(got, []string{"answer_question"}) {
				t.Errorf("next_human_actions = %v, want [answer_question]", got)
			}
			return
		}
	}
	t.Fatal("C-1 が課題の一覧に無い")
}
