package view_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

func closedSet(t *testing.T, name string) []string {
	t.Helper()
	for _, cs := range view.ClosedSets() {
		if cs.Name == name {
			return cs.Codes
		}
	}
	t.Fatalf("%s が ClosedSets に無い", name)
	return nil
}

// AC-251: internal/view の閉じた一覧に、止まり方・手・対処の結果・supervisor_state の値がある（断りの理由・フォローアップの出どころも）。
func TestClosedSets_Supervisor_AC251(t *testing.T) {
	for name, want := range map[string][]string{
		"STOP_KIND_CODES":                {"verify_uncertain", "artifact_missing", "delegate_stopped", "facts_unmet", "rework_limit", "checks_failed", "judgment_failed", "intake_unverified", "upstream_unreadable", "child_question"},
		"SUPERVISOR_HAND_CODES":          {"reverify", "send_back", "retry", "split_followup", "replan", "defer", "escalate", "answer"},
		"SUPERVISOR_RESULT_CODES":        {"executed", "failed", "rejected"},
		"SUPERVISOR_REJECT_REASON_CODES": {"state_changed", "owner_answered"},
		"SUPERVISOR_STATE_CODES":         {"pending", "handled", "escalated"},
		"FOLLOWUP_SOURCE_CODES":          {"learn", "supervisor"},
	} {
		if got := closedSet(t, name); !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
}

func TestFromHold_SupervisorFields(t *testing.T) {
	h := view.FromHold(core.Hold{Question: "q", FromStatus: core.StatusVerifying, RaisedAt: time.Unix(0, 0).UTC(), Kind: core.HoldKindHuman})
	if h.StopKind != nil || h.SupervisorState != nil || h.SupervisorFindings != nil || h.SupervisorRecommendation != nil {
		t.Errorf("a hold without a stop kind: %+v; want all four null", h)
	}
	h = view.FromHold(core.Hold{Question: "q", FromStatus: core.StatusVerifying, RaisedAt: time.Unix(0, 0).UTC(), Kind: core.HoldKindHuman,
		StopKind: core.StopKindFactsUnmet, SupervisorState: core.SupervisorStateEscalated, SupervisorFindings: "調べた", SupervisorRecommendation: "推奨"})
	if h.StopKind == nil || *h.StopKind != "facts_unmet" || *h.SupervisorState != "escalated" || *h.SupervisorFindings != "調べた" || *h.SupervisorRecommendation != "推奨" {
		t.Errorf("hold = %+v", h)
	}
}

func TestFromSupervisorAction_ShapeAndNulls(t *testing.T) {
	hand := core.HandSplitFollowup
	task := "T-2"
	hold := int64(7)
	a := view.FromSupervisorAction(core.SupervisorAction{
		ID: 3, CycleID: "Y-1", ChallengeID: "C-1", TaskID: &task, HoldID: &hold, StopKind: core.StopKindVerifyUncertain, Hand: &hand,
		Findings: "f", Note: "n", Basis: []core.SupervisorBasis{{Path: "docs/a.md", Locator: "節"}},
		Conditions: []core.SupervisorCondition{{Number: 1, Text: "条件"}}, Result: core.ActionExecuted, CreatedAt: time.Unix(0, 0).UTC(),
	})
	want := `{"basis":[{"locator":"節","path":"docs/a.md"}],"challenge_id":"C-1","conditions":[{"number":1,"text":"条件"}],"created_at":"1970-01-01T00:00:00.000Z","cycle_id":"Y-1","findings":"f","hand":"split_followup","hold_id":"H-7","id":3,"note":"n","questions":[],"reject_reason":null,"result":"executed","run_id":null,"stop_kind":"verify_uncertain","task_id":"T-2"}`
	if got := marshal(t, a); got != want {
		t.Errorf("action:\n got %s\nwant %s", got, want)
	}
	empty := marshal(t, view.FromSupervisorActions(nil))
	if empty != "[]" || strings.Contains(marshal(t, view.FromSupervisorAction(core.SupervisorAction{})), "null,\"challenge_id\"") {
		t.Errorf("empty = %s", empty)
	}
}

func TestFromSupervisorQuestions_Shape(t *testing.T) {
	got := marshal(t, view.FromSupervisorQuestions([]core.SupervisorQuestion{
		{Question: "q", Answer: "a", Basis: []core.SupervisorBasis{{Path: "docs/a.md", Locator: "節"}}},
		{Question: "q2", Answer: "a2"},
	}))
	want := `[{"answer":"a","basis":[{"locator":"節","path":"docs/a.md"}],"question":"q"},{"answer":"a2","basis":[],"question":"q2"}]`
	if got != want {
		t.Errorf("questions:\n got %s\nwant %s", got, want)
	}
	if e := marshal(t, view.FromSupervisorQuestions(nil)); e != "[]" {
		t.Errorf("empty = %s", e)
	}
}
