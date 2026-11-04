package view_test

import (
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

func TestGraphResponseEmptyListsAreArrays(t *testing.T) {
	got := marshal(t, view.FromChallengeGraph(&core.ChallengeGraph{}))
	if want := `{"edges":[],"nodes":[]}`; got != want {
		t.Errorf("got %s want %s", got, want)
	}
}

func TestGraphResponseKeysAreSortedAndNullable(t *testing.T) {
	head, parent := "いま", "C-1"
	p0 := core.PriorityP0
	g := &core.ChallengeGraph{
		Nodes: []core.GraphNode{
			{ID: "C-1", Title: "親", Priority: &p0, Category: core.GraphCategoryHuman, State: core.StatusAwaitingPlanApproval, NextHumanActions: []core.CardAction{core.CardActionApprovePlan, core.CardActionAnswerQuestion}},
			{ID: "C-2", Title: "子", Headline: &head, Category: core.GraphCategorySystem, State: core.StatusInProgress, Running: true, Stale: true, ParentID: &parent},
		},
		Edges: []core.GraphEdge{{From: "C-1", To: "C-2", Kind: core.GraphEdgePrerequisite, Satisfied: true, RegisteredVia: core.GraphRegisteredViaPlan, RegisteredAt: "2026-11-01T00:00:00.000Z"}},
	}
	got := marshal(t, view.FromChallengeGraph(g))
	want := `{"edges":[{"from":"C-1","kind":"prerequisite","registered_at":"2026-11-01T00:00:00.000Z","registered_via":"plan","satisfied":true,"to":"C-2"}],` +
		`"nodes":[{"category":"human","context":null,"headline":null,"id":"C-1","next_human_actions":["approve_plan","answer_question"],"parent_id":null,"priority":"P0","running":false,"stale":false,"state":"awaiting_plan_approval","title":"親"},` +
		`{"category":"system","context":null,"headline":"いま","id":"C-2","next_human_actions":[],"parent_id":"C-1","priority":null,"running":true,"stale":true,"state":"in_progress","title":"子"}]}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
