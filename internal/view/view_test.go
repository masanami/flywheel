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
	want := `{"actionable":{"challenges":[]},"approved":{"operations":[]},"binary_commit":"unknown","cycle_work":{"stages":{"classify":0,"cleanup":0,"improve":0,"learn":0,"plan":0,"release":0,"run":0,"supervise":0,"verify":0},"total":0},"followups":{"open":0},"ingestable":null,"needs_human":{"budget_exhausted":[],"challenges":[],"discrepancies":[],"operations":[],"slots":[],"triage":[]},"parent_agent":{"challenges":[]},"supervisor_checking":{"challenges":[],"count":0},"waiting_external":{"challenges":[]}}`
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
	want := `{"challenge_id":null,"cost_source":null,"cost_usd":null,"cycle_budget_usd":null,"cycle_id":null,"ended_at":null,"error":null,"flywheel_version":null,"id":"R-1","judgment":null,"kind":"judgment","max_budget_usd":2,"rate_limited":false,"result":null,"session_id":null,"started_at":"2026-10-10T01:02:03.004Z","task_id":null}`
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
		view.Challenge{}, view.Task{}, view.ApprovalPlanTask{}, view.TaskDelegateStep{}, view.Plan{}, view.PlanDetail{}, view.Approval{}, view.Hold{}, view.Operation{},
		view.SourceBinding{}, view.Run{}, view.Activity{}, view.ChallengeResponse{}, view.ChallengeWithPlanResponse{},
		view.ChallengeApprovalResponse{}, view.ChallengeHoldResponse{}, view.OperationResponse{},
		view.OperationApprovalResponse{}, view.ListResponse{}, view.ShowResponse{}, view.LogResponse{},
		view.RunsResponse{}, view.Discrepancy{}, view.Triage{}, view.BudgetExhausted{}, view.WaitingExternal{},
		view.Slot{}, view.StatusNeedsHuman{}, view.StatusActionable{}, view.StatusApproved{},
		view.StatusWaitingExternal{}, view.StatusResponse{}, view.IngestItem{}, view.IngestRepo{},
		view.IngestSource{}, view.IngestResponse{}, view.IngestedItem{}, view.IngestedResponse{}, view.PhaseItem{}, view.PhaseNotStarted{}, view.ReleaseNotStarted{}, view.ReleaseItem{}, view.ReleaseTidied{}, view.ReleasePhase{},
		view.JudgmentPhase{}, view.DelegationPhase{}, view.IngestPhase{}, view.PhaseResponse{}, view.Cycle{},
		view.CycleResponse{}, view.CycleStarted{}, view.CycleStatus{}, view.SlotCleared{}, view.SlotResponse{}, view.InitResponse{},
		view.MarkReadResponse{}, view.BudgetResponse{}, view.ExportBody{}, view.ExportResponse{}, view.ImportBody{}, view.ImportResponse{},
		view.ApprovalPlan{}, view.ApprovalRelease{}, view.ApprovalOtherOperation{}, view.ApprovalSummary{}, view.ApprovalSummaryResponse{}, view.DirectCompletion{}, view.ChallengeDirectCompletionResponse{}, view.StatusParentAgent{}, view.ShowCardResponse{}, view.ApprovedCardResponse{},
		view.VersionResponse{}, view.FollowupPromoteResponse{},
		view.ChatActivity{}, view.ChatMessage{}, view.ChatResponse{}, view.ChatQuestionOption{}, view.ChatQuestion{}, view.ChatPromptAnswer{}, view.ChatPrompt{},
		view.ChatPromptAnswerRequest{}, view.ChatSessionChallenge{}, view.ChatSessionSummary{}, view.ChatSessionsResponse{},
		view.ChallengeChatSession{}, view.ChatTranscriptEntry{}, view.ChatTranscriptResponse{}, view.ShowChallenge{},
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
	want := `{"approval":{"added_tasks":[],"challenge_id":"C-1","conditions_after_release":[],"conditions_after_release_heading":"` + view.ConditionsAfterReleaseHeading(view.OutlookNotReleasing) + `","conflict_fixes":[],"direct_completion":null,"done_criteria":"d","followups":[],"integration_ref":null,"kind":"completion","mergeability_note":"","other_pending_operations":[{"id":"OP-2","kind":"delete","summary":"del"}],"pairs":[],"plan":null,"release_held":true,"releases":[{"head_sha":"abc","id":"OP-1","ref":"https://github.com/o/r/pull/3","summary":"rel"}],"stages":` + emptyStages + `,"supervisor_hands_taken":{},"supervisor_questions":[],"tasks":[],"title":"t","uses_integration":null,"version":4}}`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}

	plan := &core.ApprovalPreview{ChallengeID: "C-1", Title: "t", Kind: core.ApprovalKindPlan, Version: 2, PlanVersion: 3, PlanBody: "B"}
	b, _ = json.Marshal(view.FromApprovalPreview(plan, nil))
	want = `{"approval":{"added_tasks":[],"challenge_id":"C-1","conditions_after_release":[],"conditions_after_release_heading":"` + view.ConditionsAfterReleaseHeading(view.OutlookNotReleasing) + `","conflict_fixes":[],"direct_completion":null,"done_criteria":null,"followups":[],"integration_ref":null,"kind":"plan","mergeability_note":"","other_pending_operations":[],"pairs":[],"plan":{"body":"B","budget_limit_reason":null,"budget_limit_usd":null,"budget_usd":null,"summary":null,"tasks":[],"topics":[],"uses_integration":null,"version":3},"release_held":true,"releases":[],"stages":` + emptyStages + `,"supervisor_hands_taken":{},"supervisor_questions":[],"tasks":[],"title":"t","uses_integration":null,"version":2}}`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
}

// #466: 承認の要約（API）は、完了確認待ちの検証が「本番反映で満たされる見込み」と区分した条件を、CLI と
// 同じ文言の見出し（UI の承認は常に本番反映の保留つきなので、満たされないまま残る側）つきで構造化して載せる。
// 条件が無ければ空配列（null にしない）。
func TestApprovalSummaryCarriesConditionsAfterRelease(t *testing.T) {
	p := &core.ApprovalPreview{
		ChallengeID: "C-1", Title: "t", Kind: core.ApprovalKindCompletion, Version: 4, DoneCriteria: "d",
		ConditionsAfterRelease: []string{"本番で画面に出る", "通知が届く"},
	}
	got := marshal(t, view.FromApprovalPreview(p, nil))
	for _, sub := range []string{
		`"conditions_after_release":["本番で画面に出る","通知が届く"]`,
		`"conditions_after_release_heading":"本番反映で満たされる見込みの条件（この操作では本番反映が承認されないため、満たされないまま残る）:"`,
	} {
		if !strings.Contains(got, sub) {
			t.Errorf("approval summary lacks %s:\n%s", sub, got)
		}
	}

	none := marshal(t, view.FromApprovalPreview(&core.ApprovalPreview{ChallengeID: "C-1", Kind: core.ApprovalKindCompletion}, nil))
	if !strings.Contains(none, `"conditions_after_release":[]`) {
		t.Errorf("no conditions must be an empty array: %s", none)
	}
}

// CLI（承認の要約）と API が同じ見出しを引く 1 か所。文言は CLI の出力どおり（変えない）。
func TestConditionsAfterReleaseHeadingWording(t *testing.T) {
	if got, want := view.ConditionsAfterReleaseHeading(view.OutlookReleasing), "承認で本番反映されると満たされる条件:"; got != want {
		t.Errorf("releasing heading = %q, want %q", got, want)
	}
	if got, want := view.ConditionsAfterReleaseHeading(view.OutlookApprovedRelease), "承認済みの本番反映が走れば満たされる条件:"; got != want {
		t.Errorf("approved-release heading = %q, want %q", got, want)
	}
	if got := view.ConditionsAfterReleaseHeading(view.OutlookNotReleasing); strings.Contains(view.ConditionsAfterReleaseHeading(view.OutlookApprovedRelease), "満たされないまま残る") || got == view.ConditionsAfterReleaseHeading(view.OutlookApprovedRelease) {
		t.Errorf("approved-release heading must differ from the held one: %q", got)
	}
	if view.OutlookFor(true, true) != view.OutlookReleasing || view.OutlookFor(false, true) != view.OutlookApprovedRelease || view.OutlookFor(false, false) != view.OutlookNotReleasing {
		t.Error("OutlookFor mapping is wrong")
	}
	if got, want := view.ConditionsAfterReleaseHeading(view.OutlookNotReleasing), "本番反映で満たされる見込みの条件（この操作では本番反映が承認されないため、満たされないまま残る）:"; got != want {
		t.Errorf("held heading = %q, want %q", got, want)
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
	if !view.FromHold(core.Hold{Question: "q", FromStatus: core.StatusInProgress, RaisedAt: at, Replan: true}).Replan {
		t.Error("FromHold(Replan: true).Replan = false, want true")
	}
	got := marshal(t, view.FromHold(core.Hold{Question: "q", FromStatus: core.StatusUnclassified, RaisedAt: at}))
	if !strings.Contains(got, `"replan":false`) {
		t.Errorf("FromHold(no mark) = %s, want replan:false", got)
	}
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

func TestShowCarriesPairsOutsideStages(t *testing.T) {
	d := &core.ChallengeDetail{Challenge: core.Challenge{StageFields: core.StageFields{
		Expected: []core.StageExpected{{Kind: core.ExpectedKindUI, Scene: "s1", Before: "b", After: "a"}},
	}}}
	for name, got := range map[string]string{
		"show":      marshal(t, view.FromChallengeDetail(d)),
		"show-card": marshal(t, view.FromChallengeDetailCard(d, &core.Card{}, core.TreeChallenge{})),
	} {
		if !strings.Contains(got, `"pairs":[{"actual":null,"expected":{"after":"a","before":"b","kind":"ui","scene":"s1"}}]`) {
			t.Errorf("%s: pairs missing: %s", name, got)
		}
		var m map[string]map[string]json.RawMessage
		_ = json.Unmarshal([]byte(got), &m)
		if _, ok := m["stages"]["pairs"]; ok {
			t.Errorf("%s: pairs must not be inside stages", name)
		}
	}
}

func TestFromOperation_ExecutionKeys(t *testing.T) {
	at := time.Date(2026, 10, 22, 1, 2, 3, 0, time.UTC)
	head, commit := "a1b2c3", "deadbeef"
	res := core.ExecutionResultNotMergeable
	got := marshal(t, view.FromOperation(core.IrreversibleOperation{
		ID: "OP-1", ChallengeID: "C-1", Kind: core.OperationKindRelease, State: core.OperationStateApproved,
		CreatedAt: at, ApprovedHead: &head, ExecutedAt: &at, MergeCommit: &commit,
		LastExecutionResult: &res, ConsecutiveFailures: 2, WaitingSince: &at,
	}))
	for _, want := range []string{
		`"approved_head":"a1b2c3"`, `"executed_at":"2026-10-22T01:02:03.000Z"`, `"merge_commit":"deadbeef"`,
		`"last_execution_result":"not_mergeable"`, `"consecutive_failures":2`, `"waiting_since":"2026-10-22T01:02:03.000Z"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%s does not contain %s", got, want)
		}
	}
	zero := marshal(t, view.FromOperation(core.IrreversibleOperation{ID: "OP-1", CreatedAt: at}))
	for _, want := range []string{`"approved_head":null`, `"executed_at":null`, `"merge_commit":null`, `"last_execution_result":null`, `"consecutive_failures":0`, `"waiting_since":null`} {
		if !strings.Contains(zero, want) {
			t.Errorf("%s does not contain %s", zero, want)
		}
	}
}

func TestFromDiscrepancies_OperationIDOmittedWhenEmpty(t *testing.T) {
	got := marshal(t, view.FromDiscrepancies([]core.Discrepancy{
		{ChallengeID: "C-1", Kinds: []core.DiscrepancyKind{core.DiscrepancyKindUpstreamClosed}},
		{ChallengeID: "C-2", Kinds: []core.DiscrepancyKind{core.DiscrepancyKindMergedUnapproved}, OperationID: "OP-3"},
	}))
	want := `[{"challenge_id":"C-1","kinds":["upstream_closed"]},{"challenge_id":"C-2","kinds":["merged_unapproved"],"operation_id":"OP-3"}]`
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestFromDiscrepancies_ChallengeIDsOmittedUnlessDependencyCycle(t *testing.T) {
	got := marshal(t, view.FromDiscrepancies([]core.Discrepancy{
		{ChallengeID: "C-1", Kinds: []core.DiscrepancyKind{core.DiscrepancyKindDependencyCycle}, ChallengeIDs: []string{"C-1", "C-2"}},
		{ChallengeID: "C-3", Kinds: []core.DiscrepancyKind{core.DiscrepancyKindUpstreamClosed}},
	}))
	want := `[{"challenge_id":"C-1","challenge_ids":["C-1","C-2"],"cycle_only":true,"kinds":["dependency_cycle"]},{"challenge_id":"C-3","kinds":["upstream_closed"]}]`
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestFromActionableChallenges_UnmetDependenciesIsAlwaysAnArray(t *testing.T) {
	got := marshal(t, view.FromActionableChallenges([]core.Challenge{{ID: "C-1"}}, nil))
	if !strings.Contains(got, `"unmet_dependencies":[]`) {
		t.Errorf("got %s, want unmet_dependencies:[]", got)
	}
}

func TestFromShowChallenge_DependenciesAndDependentsAreArraysAndNullStatesStayNull(t *testing.T) {
	got := marshal(t, view.FromShowChallenge(&core.ChallengeDetail{
		Challenge:    core.Challenge{ID: "C-1"},
		Dependencies: []core.DependencyView{{ChallengeID: "C-2", Status: core.StatusDone, Satisfied: true}},
	}))
	for _, want := range []string{
		`"dependencies":[{"challenge_id":"C-2","policy_state":null,"satisfied":true,"status":"done","upstream_state":null}]`,
		`"dependents":[]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%s does not contain %s", got, want)
		}
	}
}

// ShowChallenge・ActionableChallenge は Challenge のフィールドを重複して持つ（gentypes が埋め込みに対応しないため）。
// Challenge に足したキーが落ちないことを、JSON タグの集合で確かめる。
func TestShowAndActionableChallengeCarryEveryChallengeKey(t *testing.T) {
	tags := func(v any) map[string]bool {
		out := map[string]bool{}
		rt := reflect.TypeOf(v)
		for i := 0; i < rt.NumField(); i++ {
			out[strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]] = true
		}
		return out
	}
	base := tags(view.Challenge{})
	for name, extra := range map[string]struct {
		got   map[string]bool
		added []string
	}{
		"ShowChallenge":       {tags(view.ShowChallenge{}), []string{"chat_sessions", "spec_cost_usd", "spec_path", "spec_is_draft", "spec_base_path", "spec_draft", "dependencies", "dependents", "followup_of", "improvement", "integration_ref", "uses_integration"}},
		"ActionableChallenge": {tags(view.ActionableChallenge{}), []string{"unmet_dependencies"}},
	} {
		want := map[string]bool{}
		for k := range base {
			want[k] = true
		}
		for _, k := range extra.added {
			want[k] = true
		}
		if !reflect.DeepEqual(extra.got, want) {
			t.Errorf("%s keys = %v, want Challenge keys plus %v", name, extra.got, extra.added)
		}
	}
}

// ShowChallenge・ActionableChallenge へ写した Challenge の各キーの値が、FromChallenge の値と一致することを確かめる
// （キーの名前だけでなく、別のフィールドから写す・写し落とす誤りを捕まえるため、全項目に互いに異なる値を入れる）。
func TestShowAndActionableChallengeCopyEveryChallengeValue(t *testing.T) {
	str := func(v string) *string { return &v }
	urgency, priority := core.UrgencyHigh, core.PriorityP1
	c := core.Challenge{
		ID: "C-7", Title: "title", Description: "description", DoneCriteria: "done", Urgency: &urgency, Priority: &priority,
		Status: core.StatusDone, Version: 3, Reporter: "alice",
		CreatedAt:   time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
		StageFields: core.StageFields{Headline: str("headline"), Context: str("context")},
	}
	toMap := func(v any) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(marshal(t, v)), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	base := toMap(view.FromChallenge(c))
	for name, got := range map[string]map[string]any{
		"ShowChallenge":       toMap(view.FromShowChallenge(&core.ChallengeDetail{Challenge: c})),
		"ActionableChallenge": toMap(view.FromActionableChallenges([]core.Challenge{c}, nil)[0]),
	} {
		for k, want := range base {
			if !reflect.DeepEqual(got[k], want) {
				t.Errorf("%s.%s = %v, want %v", name, k, got[k], want)
			}
		}
	}
}

func TestFromRun_FlywheelVersion(t *testing.T) {
	v := "abc123+dirty"
	got := marshal(t, view.FromRun(core.Run{ID: "R-1", FlywheelVersion: &v}))
	if !strings.Contains(got, `"flywheel_version":"abc123+dirty"`) {
		t.Errorf("got %s", got)
	}
}

func TestFromShowChallenge_FollowupOf(t *testing.T) {
	if got := marshal(t, view.FromShowChallenge(&core.ChallengeDetail{Challenge: core.Challenge{ID: "C-2"}})); !strings.Contains(got, `"followup_of":null`) {
		t.Errorf("got %s", got)
	}
	src := "C-1"
	got := marshal(t, view.FromShowChallenge(&core.ChallengeDetail{Challenge: core.Challenge{ID: "C-2"}, FollowupOf: &src}))
	if !strings.Contains(got, `"followup_of":"C-1"`) {
		t.Errorf("got %s", got)
	}
}

func TestFromShowChallenge_Improvement(t *testing.T) {
	if got := marshal(t, view.FromShowChallenge(&core.ChallengeDetail{Challenge: core.Challenge{ID: "C-2"}})); !strings.Contains(got, `"improvement":null`) {
		t.Errorf("got %s", got)
	}
	got := marshal(t, view.FromShowChallenge(&core.ChallengeDetail{Challenge: core.Challenge{ID: "C-2"}, Improvement: &core.ImprovementKey{Layer: "prompt", Target: "j1.md"}}))
	if !strings.Contains(got, `"improvement":{"layer":"prompt","target":"j1.md"}`) {
		t.Errorf("got %s", got)
	}
}

// 親が直接行った完了の記録（#452）は、承認の要約に成果物の参照を完了条件と並べて載せ、詳細・show に出る。
func TestDirectCompletionIsCarriedByApprovalSummaryShowAndHold(t *testing.T) {
	note := "親が確かめた"
	at := time.Date(2026, 11, 5, 1, 2, 3, 4_000_000, time.UTC)
	dc := core.DirectCompletion{Artifacts: []string{"https://example.com/result", "o/r#12"}, Note: &note, CreatedAt: at}

	p := &core.ApprovalPreview{ChallengeID: "C-1", Title: "t", Kind: core.ApprovalKindCompletion, Version: 4, DoneCriteria: "d", DirectCompletion: &dc}
	got := marshal(t, view.FromApprovalPreview(p, nil))
	for _, sub := range []string{
		`"direct_completion":{"artifacts":["https://example.com/result","o/r#12"],"created_at":"2026-11-05T01:02:03.004Z","note":"親が確かめた"}`,
		`"done_criteria":"d"`,
	} {
		if !strings.Contains(got, sub) {
			t.Errorf("approval summary lacks %s:\n%s", sub, got)
		}
	}

	d := &core.ChallengeDetail{DirectCompletions: []core.DirectCompletion{dc}, Holds: []core.Hold{{Kind: core.HoldKindParentDirect, Question: "q", FromStatus: core.StatusClassified, RaisedAt: at}}}
	show := marshal(t, view.FromChallengeDetail(d))
	for _, sub := range []string{`"direct_completions":[{"artifacts":["https://example.com/result","o/r#12"]`, `"kind":"parent_direct"`} {
		if !strings.Contains(show, sub) {
			t.Errorf("show lacks %s:\n%s", sub, show)
		}
	}
	if empty := marshal(t, view.FromChallengeDetail(&core.ChallengeDetail{})); !strings.Contains(empty, `"direct_completions":[]`) {
		t.Errorf("an ordinary challenge's show must have an empty direct_completions array: %s", empty)
	}
	card := marshal(t, view.FromChallengeDetailCard(d, &core.Card{}, core.TreeChallenge{}))
	if !strings.Contains(card, `"direct_completions":[{`) {
		t.Errorf("show card lacks direct_completions: %s", card)
	}
}

func TestStatusParentAgentSectionHoldsParentAgentChallenges(t *testing.T) {
	ov := &core.Overview{ParentAgentChallenges: []core.Challenge{{ID: "C-7", Title: "親が直接行う", Status: core.StatusAwaitingHuman}}}
	got := marshal(t, view.FromOverview(ov))
	if !strings.Contains(got, `"parent_agent":{"challenges":[{`) || !strings.Contains(got, `"id":"C-7"`) || !strings.Contains(got, `"status":"awaiting_human"`) {
		t.Errorf("status = %s", got)
	}
	if strings.Contains(marshal(t, view.FromNeedsHuman(ov)), "C-7") {
		t.Error("the parent-agent challenge appears in needs_human")
	}
}

// API: 承認済みで未実行の release が課題にあれば、UI の承認（本番反映の保留つき）でも「満たされないまま残る」ではない見出し。
func TestFromApprovalPreviewHeadingFollowsApprovedRelease(t *testing.T) {
	p := &core.ApprovalPreview{ChallengeID: "C-1", Kind: core.ApprovalKindCompletion, ConditionsAfterRelease: []string{"a"}}
	if got := view.FromApprovalPreview(p, nil).Approval.ConditionsAfterReleaseHeading; got != view.ConditionsAfterReleaseHeading(view.OutlookNotReleasing) {
		t.Errorf("heading = %q, want the held one", got)
	}
	p.HasApprovedRelease = true
	if got := view.FromApprovalPreview(p, nil).Approval.ConditionsAfterReleaseHeading; got != "承認済みの本番反映が走れば満たされる条件:" {
		t.Errorf("heading = %q, want the approved-release one", got)
	}
}

// 完了の承認の要約は、core の要約に計画の版・本文が残っていても計画を出さず（計画は計画の承認のときだけ）、
// 親が直接行った完了の記録を direct_completion に出す（#519。承認画面の API の plan・direct_completion）。
func TestFromApprovalPreview_CompletionWithDirectCompletionHasNoPlan(t *testing.T) {
	p := &core.ApprovalPreview{
		ChallengeID: "C-1",
		Kind:        core.ApprovalKindCompletion,
		PlanVersion: 1,
		PlanBody:    "plan body",
		DirectCompletion: &core.DirectCompletion{
			Artifacts: []string{"https://example.com/1"},
			CreatedAt: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC),
		},
	}
	got := view.FromApprovalPreview(p, nil).Approval
	if got.Plan != nil {
		t.Errorf("Plan = %+v, want nil for a completion approval", got.Plan)
	}
	if got.DirectCompletion == nil || !reflect.DeepEqual(got.DirectCompletion.Artifacts, []string{"https://example.com/1"}) {
		t.Errorf("DirectCompletion = %+v, want the recorded artifact", got.DirectCompletion)
	}
}

// UI の偽のサーバとテストは生成物の CONDITIONS_AFTER_RELEASE_HEADINGS を添字（ConditionsOutlook の値）で引く。
func TestClosedSetsHeadingsIndexedByOutlook(t *testing.T) {
	for _, cs := range view.ClosedSets() {
		if cs.Name != "CONDITIONS_AFTER_RELEASE_HEADINGS" {
			continue
		}
		if len(cs.Codes) != 3 {
			t.Fatalf("見出しは ConditionsOutlook の値の数（3）だけ要る: %d", len(cs.Codes))
		}
		for _, o := range []view.ConditionsOutlook{view.OutlookNotReleasing, view.OutlookReleasing, view.OutlookApprovedRelease} {
			if cs.Codes[int(o)] != view.ConditionsAfterReleaseHeading(o) {
				t.Errorf("添字 %d の見出しが ConditionsAfterReleaseHeading(%d) と違う", int(o), int(o))
			}
		}
		return
	}
	t.Fatal("CONDITIONS_AFTER_RELEASE_HEADINGS が ClosedSets に無い")
}

func TestFromRun_Error(t *testing.T) {
	got := marshal(t, view.FromRun(core.Run{ID: "R-1", Error: "boom"}))
	if !strings.Contains(got, `"error":"boom"`) {
		t.Errorf("got %s", got)
	}
}

// 完了の承認の要約（API・UI）にも、CLI と同じ「マージ可否は未確認」を出す（#629 の決定 3）。
func TestApprovalSummaryCarriesMergeabilityUnconfirmed(t *testing.T) {
	p := &core.ApprovalPreview{ChallengeID: "C-1", Kind: core.ApprovalKindCompletion, MergeabilityUnconfirmed: true}
	if got := view.FromApprovalPreview(p, nil).Approval.MergeabilityNote; got != core.MergeabilityUnconfirmedNote {
		t.Errorf("MergeabilityNote = %q, want %q", got, core.MergeabilityUnconfirmedNote)
	}
	p.MergeabilityUnconfirmed = false
	if got := view.FromApprovalPreview(p, nil).Approval.MergeabilityNote; got != "" {
		t.Errorf("確認できているのに MergeabilityNote = %q", got)
	}
}

func TestFromOperation_LastExecutionDetailKey(t *testing.T) {
	res, detail := core.ExecutionResultLandRefused, "委譲先が書いた理由"
	got := marshal(t, view.FromOperation(core.IrreversibleOperation{ID: "OP-1", LastExecutionResult: &res, LastExecutionDetail: &detail}))
	if !strings.Contains(got, `"last_execution_detail":"委譲先が書いた理由"`) || !strings.Contains(got, `"last_execution_result":"land_refused"`) {
		t.Errorf("%s lacks last_execution_detail/result", got)
	}
	if zero := marshal(t, view.FromOperation(core.IrreversibleOperation{ID: "OP-1"})); !strings.Contains(zero, `"last_execution_detail":null`) {
		t.Errorf("%s: last_execution_detail must be null without an attempt record", zero)
	}
}

func TestFromReleasePhase_ItemRouteIsNullUntilARouteExists(t *testing.T) {
	got := marshal(t, view.FromReleasePhase(false, []core.ReleaseExecItem{{OperationID: "OP-1", ChallengeID: "C-1", Result: core.ExecutionResultLandSent}}, nil, nil))
	if !strings.Contains(got, `"result":"land_sent","route":null`) {
		t.Errorf("%s: item must carry route:null after result", got)
	}
	for _, key := range []string{`"items"`, `"not_started"`, `"tidied"`} {
		if !strings.Contains(got, key) {
			t.Errorf("%s lost the root key %s", got, key)
		}
	}
}

func TestExecutionResults_ClosedSetAndLabelsCoverEveryCoreValue(t *testing.T) {
	var codes []string
	for _, cs := range view.ClosedSets() {
		if cs.Name == "EXECUTION_RESULT_CODES" {
			codes = cs.Codes
		}
	}
	values := core.ExecutionResultValues()
	if len(codes) != len(values) {
		t.Fatalf("EXECUTION_RESULT_CODES = %v, want the %d core values", codes, len(values))
	}
	for i, v := range values {
		if codes[i] != string(v) {
			t.Errorf("code[%d] = %q, want %q", i, codes[i], v)
		}
		if view.ExecutionResultLabel(v) == "" {
			t.Errorf("no label for %q", v)
		}
	}
}

func TestExecutionResultLabel_S3aWordsAreVerbatim(t *testing.T) {
	want := map[core.ExecutionResult]string{
		core.ExecutionResultLanded:          "委譲先が反映先へ入れた",
		core.ExecutionResultLandSent:        "委譲先へ「入れてよい」を返した",
		core.ExecutionResultLandUnconfirmed: "「入れてよい」が届いたか分からない",
		core.ExecutionResultLandRefused:     "委譲先が入れられなかった",
		core.ExecutionResultContentMismatch: "委譲先の中身と、上流の中身が食い違っている",
		core.ExecutionResultReflectStarted:  "反映の依頼を始めた",
		core.ExecutionResultReflectFailed:   "反映の依頼を始められなかった",
		core.ExecutionResultWaitingDelegate: "委譲先が進めている",
		core.ExecutionResultWaitingOutside:  "flywheel の外での反映を待っている",
		core.ExecutionResultDelegateEnded:   "委譲先の実行が、入れずに終わった",
	}
	for r, w := range want {
		if got := view.ExecutionResultLabel(r); got != w {
			t.Errorf("label(%s) = %q, want %q", r, got, w)
		}
	}
}

// 見直し前からある値の語は、コードでの使われ方（release_exec.go ほか）に合わせる（flywheel#856 の差し戻し）。
func TestExecutionResultLabel_EarlierValuesSayWhatHappened(t *testing.T) {
	want := map[core.ExecutionResult]string{
		core.ExecutionResultUpstreamFailed:   "上流を読めなかった",
		core.ExecutionResultClosed:           "成果物が、入らずに閉じられた",
		core.ExecutionResultInterrupted:      "試行が途中で止まった",
		core.ExecutionResultUnsupported:      "flywheel が扱えない形だった",
		core.ExecutionResultNoApprovedHead:   "承認した中身の記録が無い",
		core.ExecutionResultMergedUnapproved: "承認なしで反映された",
	}
	for r, w := range want {
		if got := view.ExecutionResultLabel(r); got != w {
			t.Errorf("label(%s) = %q, want %q", r, got, w)
		}
	}
}

// 画面の文言の表が読む EXECUTION_RESULT_WORDS は、EXECUTION_RESULT_CODES と同じ添字で端末の語を持つ。
func TestClosedSets_ExecutionResultWordsAlignWithCodes(t *testing.T) {
	var codes, words []string
	for _, cs := range view.ClosedSets() {
		switch cs.Name {
		case "EXECUTION_RESULT_CODES":
			codes = cs.Codes
		case "EXECUTION_RESULT_WORDS":
			words = cs.Codes
		}
	}
	if len(codes) == 0 || len(words) != len(codes) {
		t.Fatalf("codes = %d, words = %d", len(codes), len(words))
	}
	seen := map[string]string{}
	for i, c := range codes {
		if want := view.ExecutionResultLabel(core.ExecutionResult(c)); words[i] != want || want == "" {
			t.Errorf("words[%d] = %q, want the label of %s (%q)", i, words[i], c, want)
		}
		if prev, dup := seen[words[i]]; dup {
			t.Errorf("%s と %s が同じ語 %q", prev, c, words[i])
		}
		seen[words[i]] = c
	}
}

// #906: 計画のタスクの枠（実装・レビュー対応）が、`show --plan --json` と承認の要約の JSON に出る。書いていないタスクは null。
func TestPlanTaskFramesAreInTheJSON(t *testing.T) {
	impl, review := 10.0, 5.0
	pv := &core.PlanView{Tasks: []core.PlanTaskView{
		{Key: "a", Title: "A", BudgetImplUSD: &impl, BudgetReviewUSD: &review},
		{Key: "b", Title: "B"},
	}}
	got := marshal(t, view.FromPlanView(pv))
	if !strings.Contains(got, `"budget_impl_usd":10,"budget_review_usd":5,"depends_on"`) || !strings.Contains(got, `"budget_impl_usd":null,"budget_review_usd":null,"depends_on"`) {
		t.Errorf("plan json lacks the task frames: %s", got)
	}
	ap := marshal(t, view.FromApprovalPreview(&core.ApprovalPreview{Kind: core.ApprovalKindPlan, PlanTasks: pv.Tasks}, nil))
	if !strings.Contains(ap, `"budget_impl_usd":10,"budget_review_usd":5,`) {
		t.Errorf("approval json lacks the task frames: %s", ap)
	}
}

func TestFromHoldCarriesOptionsAndAnswerOption(t *testing.T) {
	at := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	chosen := "add"
	h := view.FromHold(core.Hold{Question: "q", FromStatus: core.StatusInProgress, RaisedAt: at,
		Options:      []core.HoldOption{{ID: "add", Label: "足す", Description: "説明"}, {ID: "o2", Label: "足さない"}},
		AnswerOption: &chosen})
	got := marshal(t, h)
	for _, want := range []string{
		`"answer_option":"add"`,
		`"options":[{"description":"説明","id":"add","label":"足す"},{"description":"","id":"o2","label":"足さない"}]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("FromHold = %s, want %s", got, want)
		}
	}
	// 選択肢の無い保留（DB の列は NULL → nil）は、null ではなく空の配列。選んでいなければ answer_option は null。
	got = marshal(t, view.FromHold(core.Hold{Question: "q", FromStatus: core.StatusInProgress, RaisedAt: at}))
	for _, want := range []string{`"answer_option":null`, `"options":[]`} {
		if !strings.Contains(got, want) {
			t.Errorf("FromHold(no options) = %s, want %s", got, want)
		}
	}
}

func TestFromTaskAdditions_UnjudgedValuesAreNull(t *testing.T) {
	total, limit := 15.0, 14.0
	out := view.FromTaskAdditions([]core.TaskAddition{
		{ID: "TA-1", State: core.TaskAdditionPending},
		{ID: "TA-2", State: core.TaskAdditionAwaitingOwner, TotalAfterUSD: &total, LimitUSD: &limit},
	})
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"limit_usd":null`) || !strings.Contains(s, `"total_after_usd":null`) || !strings.Contains(s, `"total_after_usd":15`) || !strings.Contains(s, `"limit_usd":14`) {
		t.Errorf("json = %s, want null for the unjudged one and amounts for the judged one", s)
	}
}

func TestFollowupTaskKeys_AC84_AC85(t *testing.T) {
	fid, tid := "F-3", "T-9"
	var tasks []struct {
		Added *struct {
			FollowupID *string `json:"followup_id"`
		} `json:"added"`
	}
	if err := json.Unmarshal([]byte(marshal(t, view.FromTasks([]core.Task{{ID: "T-1", Added: &core.TaskAdded{Origin: "followup", FollowupID: &fid}}, {ID: "T-2"}}))), &tasks); err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 || tasks[0].Added == nil || tasks[0].Added.FollowupID == nil || *tasks[0].Added.FollowupID != fid || tasks[1].Added != nil {
		t.Errorf("AC-84: tasks = %+v, want T-1.added.followup_id = F-3 and T-2.added = null", tasks)
	}
	var adds []struct {
		FollowupID *string `json:"followup_id"`
	}
	if err := json.Unmarshal([]byte(marshal(t, view.FromTaskAdditions([]core.TaskAddition{{ID: "TA-1", Origin: "followup", FollowupID: &fid}, {ID: "TA-2"}}))), &adds); err != nil {
		t.Fatal(err)
	}
	if len(adds) != 2 || adds[0].FollowupID == nil || *adds[0].FollowupID != fid || adds[1].FollowupID != nil {
		t.Errorf("AC-84: additions = %+v, want [F-3, null]", adds)
	}
	var fus []struct {
		PromotedTaskID *string `json:"promoted_task_id"`
	}
	if err := json.Unmarshal([]byte(marshal(t, view.FromFollowups([]core.Followup{{ID: "F-3", PromotedTaskID: &tid}, {ID: "F-4"}}))), &fus); err != nil {
		t.Fatal(err)
	}
	if len(fus) != 2 || fus[0].PromotedTaskID == nil || *fus[0].PromotedTaskID != tid || fus[1].PromotedTaskID != nil {
		t.Errorf("AC-85: followups = %+v, want [T-9, null]", fus)
	}
}

// 画面の口は、保留に照合の値と選択肢ごとの button を足す。CLI の --json（FromHold）には出さない。
func TestFromHoldForScreenAddsCheckAndButtonsButFromHoldDoesNot(t *testing.T) {
	h := core.Hold{Question: "q", FromStatus: core.StatusInProgress, RaisedAt: time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC),
		Options:       []core.HoldOption{{ID: "add", Label: "足す"}, {ID: "skip", Label: "足さない"}},
		OptionButtons: []bool{false, true}, Check: "chk"}
	plain := marshal(t, view.FromHold(h))
	if strings.Contains(plain, `"check"`) || strings.Contains(plain, `"button"`) {
		t.Errorf("FromHold = %s, want no check/button", plain)
	}
	got := marshal(t, view.FromHoldForScreen(h))
	for _, want := range []string{`"check":"chk"`, `"options":[{"button":false,"description":"","id":"add","label":"足す"},{"button":true,"description":"","id":"skip","label":"足さない"}]`} {
		if !strings.Contains(got, want) {
			t.Errorf("FromHoldForScreen = %s, want %s", got, want)
		}
	}
}

func TestNewCycleScreenKeepsTheStatusAndNeverNullsTheNewLists(t *testing.T) {
	reason := "settled"
	got := marshal(t, view.NewCycleScreen(view.CycleStatus{State: "idle", LastEndReason: &reason}, nil, nil))
	for _, want := range []string{`"state":"idle"`, `"last_end_reason":"settled"`, `"supervisor_actions":[]`, `"costs":[]`} {
		if !strings.Contains(got, want) {
			t.Errorf("NewCycleScreen = %s, want %s", got, want)
		}
	}
}
