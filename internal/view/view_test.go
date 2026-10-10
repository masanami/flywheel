package view_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEmptyListsAreArraysAndNullsStayNull(t *testing.T) {
	got := marshal(t, view.FromOverview(&core.Overview{}))
	want := `{"actionable":{"challenges":[]},"approved":{"operations":[]},"needs_human":{"budget_exhausted":[],"challenges":[],"discrepancies":[],"operations":[],"slots":[],"triage":[]},"waiting_external":{"challenges":[]}}`
	if got != want {
		t.Errorf("status:\n got %s\nwant %s", got, want)
	}
	if got := marshal(t, view.LogResponse{Activities: view.FromActivities(nil)}); got != `{"activities":[]}` {
		t.Errorf("log: %s", got)
	}
	if got := marshal(t, view.RunsResponse{Runs: view.FromRuns(nil)}); got != `{"runs":[]}` {
		t.Errorf("runs: %s", got)
	}
}

func TestRunKeysAreSortedAndNullable(t *testing.T) {
	at := time.Date(2026, 10, 10, 1, 2, 3, 4_000_000, time.UTC)
	got := marshal(t, view.FromRun(core.Run{ID: "R-1", Kind: "judgment", StartedAt: at, MaxBudgetUSD: 2}))
	want := `{"challenge_id":null,"cost_source":null,"cost_usd":null,"cycle_budget_usd":null,"cycle_id":null,"ended_at":null,"id":"R-1","judgment":null,"kind":"judgment","max_budget_usd":2,"rate_limited":false,"result":null,"session_id":null,"started_at":"2026-10-10T01:02:03.004Z"}`
	if got != want {
		t.Errorf("run:\n got %s\nwant %s", got, want)
	}
}

func TestShowSourceBindingNullAndPlanSpec(t *testing.T) {
	spec := `{"a":1}`
	d := &core.ChallengeDetail{Plans: []core.Plan{{Version: 1, Body: "b"}, {Version: 2, Body: "c", Spec: &spec}}}
	got := marshal(t, view.FromChallengeDetail(d))
	for _, sub := range []string{`"source_binding":null`, `"spec":null`, `"spec":{"a":1}`} {
		if !strings.Contains(got, sub) {
			t.Errorf("%s not in %s", sub, got)
		}
	}
}

// TestStructFieldsAreInKeyOrder は、view の型のフィールドが json キーの辞書順に並んでいることを検査する
// （CLI の --json の出力をバイト単位で保つ前提。encoding/json は map のキーを辞書順で出していた）。
func TestStructFieldsAreInKeyOrder(t *testing.T) {
	types := []any{
		view.Challenge{}, view.Plan{}, view.PlanDetail{}, view.Approval{}, view.Hold{}, view.Operation{},
		view.SourceBinding{}, view.Run{}, view.Activity{}, view.ChallengeResponse{}, view.ChallengeWithPlanResponse{},
		view.ChallengeApprovalResponse{}, view.ChallengeHoldResponse{}, view.OperationResponse{},
		view.OperationApprovalResponse{}, view.ListResponse{}, view.ShowResponse{}, view.LogResponse{},
		view.RunsResponse{}, view.Discrepancy{}, view.Triage{}, view.BudgetExhausted{}, view.WaitingExternal{},
		view.Slot{}, view.StatusNeedsHuman{}, view.StatusActionable{}, view.StatusApproved{},
		view.StatusWaitingExternal{}, view.StatusResponse{}, view.IngestItem{}, view.IngestRepo{},
		view.IngestSource{}, view.IngestResponse{}, view.PhaseItem{}, view.PhaseNotStarted{}, view.SerialGroup{},
		view.JudgmentPhase{}, view.DelegationPhase{}, view.IngestPhase{}, view.PhaseResponse{}, view.Cycle{},
		view.CycleResponse{}, view.SlotCleared{}, view.SlotResponse{}, view.InitResponse{},
		view.MarkReadResponse{}, view.BudgetResponse{},
	}
	for _, v := range types {
		rt := reflect.TypeOf(v)
		prev := ""
		for i := 0; i < rt.NumField(); i++ {
			key := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
			if key <= prev {
				t.Errorf("%s: field %s (%q) is not after %q in key order", rt.Name(), rt.Field(i).Name, key, prev)
			}
			prev = key
		}
	}
}
