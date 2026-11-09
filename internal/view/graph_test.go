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
		Edges: []core.GraphEdge{{From: "C-1", To: "C-2", Kind: core.GraphEdgePrerequisite, Directed: true, Satisfied: true, RegisteredVia: core.GraphRegisteredViaPlan, RegisteredAt: "2026-11-01T00:00:00.000Z"}},
	}
	got := marshal(t, view.FromChallengeGraph(g))
	want := `{"edges":[{"directed":true,"from":"C-1","kind":"prerequisite","registered_at":"2026-11-01T00:00:00.000Z","registered_via":"plan","satisfied":true,"to":"C-2"}],` +
		`"nodes":[{"category":"human","context":null,"headline":null,"id":"C-1","next_human_actions":["approve_plan","answer_question"],"parent_id":null,"priority":"P0","running":false,"stale":false,"state":"awaiting_plan_approval","title":"親"},` +
		`{"category":"system","context":null,"headline":"いま","id":"C-2","next_human_actions":[],"parent_id":"C-1","priority":null,"running":true,"stale":true,"state":"in_progress","title":"子"}]}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// 衝突の辺は kind=conflict・registered_via=prediction で、向きの無い組は directed=false。
func TestGraphResponseConflictEdge(t *testing.T) {
	g := &core.ChallengeGraph{Edges: []core.GraphEdge{
		{From: "C-409", To: "C-408", Kind: core.GraphEdgeConflict, Directed: true, RegisteredVia: core.GraphRegisteredViaPrediction, RegisteredAt: "2026-11-05T01:02:03.000Z"},
		{From: "C-1", To: "C-2", Kind: core.GraphEdgeConflict, Directed: false, RegisteredVia: core.GraphRegisteredViaPrediction, RegisteredAt: "2026-11-05T01:02:03.000Z"},
	}}
	got := marshal(t, view.FromChallengeGraph(g))
	want := `{"edges":[` +
		`{"directed":true,"from":"C-409","kind":"conflict","registered_at":"2026-11-05T01:02:03.000Z","registered_via":"prediction","satisfied":false,"to":"C-408"},` +
		`{"directed":false,"from":"C-1","kind":"conflict","registered_at":"2026-11-05T01:02:03.000Z","registered_via":"prediction","satisfied":false,"to":"C-2"}],"nodes":[]}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
