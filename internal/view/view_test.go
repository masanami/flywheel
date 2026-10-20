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
		view.CycleResponse{}, view.CycleStarted{}, view.CycleStatus{}, view.SlotCleared{}, view.SlotResponse{}, view.InitResponse{},
		view.MarkReadResponse{}, view.BudgetResponse{},
		view.ApprovalPlan{}, view.ApprovalRelease{}, view.ApprovalOtherOperation{}, view.ApprovalSummary{}, view.ApprovalSummaryResponse{},
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

const emptyStages = `{"actual":null,"expected":null,"figure":null,"issues":null,"issues_answer":null,"non_goals":null}`

func TestFromApprovalPreview(t *testing.T) {
	ref := "https://github.com/o/r/pull/3"
	sha := "abc"
	p := &core.ApprovalPreview{
		ChallengeID: "C-1", Title: "t", Kind: core.ApprovalKindCompletion, Version: 4, DoneCriteria: "d",
		OtherPendingOperations: []core.IrreversibleOperation{{ID: "OP-2", Kind: core.OperationKindDelete, Summary: "del"}},
	}
	heads := []core.ReleaseHead{{Operation: core.IrreversibleOperation{ID: "OP-1", Kind: core.OperationKindRelease, Summary: "rel", Ref: &ref}, HeadSHA: &sha}}
	b, err := json.Marshal(view.FromApprovalPreview(p, heads))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"approval":{"challenge_id":"C-1","done_criteria":"d","kind":"completion","other_pending_operations":[{"id":"OP-2","kind":"delete","summary":"del"}],"pairs":[],"plan":null,"release_held":true,"releases":[{"head_sha":"abc","id":"OP-1","ref":"https://github.com/o/r/pull/3","summary":"rel"}],"stages":` + emptyStages + `,"title":"t","version":4}}`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}

	plan := &core.ApprovalPreview{ChallengeID: "C-1", Title: "t", Kind: core.ApprovalKindPlan, Version: 2, PlanVersion: 3, PlanBody: "B"}
	b, _ = json.Marshal(view.FromApprovalPreview(plan, nil))
	want = `{"approval":{"challenge_id":"C-1","done_criteria":null,"kind":"plan","other_pending_operations":[],"pairs":[],"plan":{"body":"B","version":3},"release_held":true,"releases":[],"stages":` + emptyStages + `,"title":"t","version":2}}`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
}

// AC-252: FromApproval・FromHold は会話の手がかりと回答の経路・方式を写す（nil は null）。
func TestFromApprovalAndHoldCopyRelayEvidence(t *testing.T) {
	sess, quote, ch, ver := "sess", "「承認して」", "cli", "relayed"
	at := time.Date(2026, 10, 19, 1, 2, 3, 0, time.UTC)
	a := view.FromApproval(core.Approval{Kind: core.ApprovalKindBudget, Decision: core.ApprovalDecisionApproved, Channel: "cli", Verification: "relayed", DecidedAt: at, RelaySession: &sess, RelayQuote: &quote})
	if a.RelaySession == nil || *a.RelaySession != sess || a.RelayQuote == nil || *a.RelayQuote != quote {
		t.Errorf("FromApproval relay = %v/%v, want %q/%q", a.RelaySession, a.RelayQuote, sess, quote)
	}
	if got := marshal(t, view.FromApproval(core.Approval{Kind: core.ApprovalKindPlan, Verification: "tty_confirm", DecidedAt: at})); !strings.Contains(got, `"relay_quote":null,"relay_session":null`) {
		t.Errorf("FromApproval(tty_confirm) = %s, want relay_* null", got)
	}

	h := view.FromHold(core.Hold{Question: "q", FromStatus: core.StatusUnclassified, RaisedAt: at, AnsweredChannel: &ch, AnsweredVerification: &ver, RelaySession: &sess, RelayQuote: &quote})
	if h.AnsweredChannel == nil || *h.AnsweredChannel != ch || h.AnsweredVerification == nil || *h.AnsweredVerification != ver ||
		h.RelaySession == nil || *h.RelaySession != sess || h.RelayQuote == nil || *h.RelayQuote != quote {
		t.Errorf("FromHold = %+v, want cli/relayed/%q/%q", h, sess, quote)
	}
	got := marshal(t, view.FromHold(core.Hold{Question: "q", FromStatus: core.StatusUnclassified, RaisedAt: at}))
	for _, k := range []string{`"answered_channel":null`, `"answered_verification":null`, `"relay_quote":null`, `"relay_session":null`} {
		if !strings.Contains(got, k) {
			t.Errorf("FromHold(unanswered) = %s, want %s", got, k)
		}
	}
}

func TestFromStagesNullsAndShapes(t *testing.T) {
	if got := marshal(t, view.FromStages(core.StageFields{NonGoals: []string{}, Issues: []core.StageIssue{}}, nil)); got != emptyStages {
		t.Errorf("empty stages = %s", got)
	}
	ans, fig, ex := "答え", "graph TD", "例"
	f := core.StageFields{
		Figure:   &fig,
		Issues:   []core.StageIssue{{Question: "q", Options: []core.StageOption{{Label: "a", Example: &ex}, {Label: "b"}}, Recommended: 2}},
		Expected: []core.StageExpected{{Kind: core.ExpectedKindCLI, Scene: "s", Before: "b", After: "a"}},
		Actual:   []core.StageActual{{Scene: "s", After: "a"}},
		NonGoals: []string{"x"},
	}
	want := `{"actual":[{"after":"a","scene":"s"}],"expected":[{"after":"a","before":"b","kind":"cli","scene":"s"}],"figure":"graph TD","issues":[{"options":[{"example":"例","label":"a"},{"example":null,"label":"b"}],"question":"q","recommended":2}],"issues_answer":"答え","non_goals":["x"]}`
	if got := marshal(t, view.FromStages(f, &ans)); got != want {
		t.Errorf("stages:\n got %s\nwant %s", got, want)
	}
}

func TestFromPairs(t *testing.T) {
	if got := marshal(t, view.FromPairs(core.Pairs(nil, nil))); got != `[]` {
		t.Errorf("empty pairs = %s", got)
	}
	e := []core.StageExpected{{Kind: core.ExpectedKindUI, Scene: "s1", Before: "b", After: "a"}, {Kind: core.ExpectedKindUI, Scene: "s2", Before: "b", After: "a"}}
	a := []core.StageActual{{Scene: "s1", After: "x"}}
	want := `[{"actual":{"after":"x","scene":"s1"},"expected":{"after":"a","before":"b","kind":"ui","scene":"s1"}},{"actual":null,"expected":{"after":"a","before":"b","kind":"ui","scene":"s2"}}]`
	if got := marshal(t, view.FromPairs(core.Pairs(e, a))); got != want {
		t.Errorf("pairs:\n got %s\nwant %s", got, want)
	}
	want = `[{"actual":{"after":"x","scene":"s1"},"expected":null}]`
	if got := marshal(t, view.FromPairs(core.Pairs(nil, a))); got != want {
		t.Errorf("pairs:\n got %s\nwant %s", got, want)
	}
}

func TestChallengeAndApprovalCarryS4Keys(t *testing.T) {
	h, c := "一言", "位置づけ"
	got := marshal(t, view.FromChallenge(core.Challenge{StageFields: core.StageFields{Headline: &h, Context: &c}}))
	if !strings.Contains(got, `"context":"位置づけ"`) || !strings.Contains(got, `"headline":"一言"`) {
		t.Errorf("challenge = %s", got)
	}
	if got := marshal(t, view.FromApproval(core.Approval{})); !strings.Contains(got, `"rejected_fields":null`) {
		t.Errorf("approval = %s", got)
	}
	if got := marshal(t, view.FromApproval(core.Approval{RejectedFields: []string{"expected"}})); !strings.Contains(got, `"rejected_fields":["expected"]`) {
		t.Errorf("approval = %s", got)
	}
}
