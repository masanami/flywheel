package core

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// 区切り S4b-2 の土台（止まり方・手・保留を作る入口・対処の記録・宣言・SV の出力・手の実行の雛形）。実ストアで検証する。

func TestSupervisorKinds_ClosedSetsAndTheHandTable(t *testing.T) {
	t.Parallel()
	kinds := StopKindValues()
	if len(kinds) != 9 {
		t.Fatalf("stop kinds = %v, want 9", kinds)
	}
	for _, k := range kinds {
		if k == "child_question" {
			t.Error("child_question is S4b-3")
		}
		if got, ok := ParseStopKind(string(k)); !ok || got != k {
			t.Errorf("ParseStopKind(%q) = %q, %v", k, got, ok)
		}
	}
	if _, ok := ParseStopKind("child_question"); ok {
		t.Error("ParseStopKind accepted child_question")
	}
	hands := SupervisorHandValues()
	if len(hands) != 7 {
		t.Fatalf("hands = %v, want 7", hands)
	}
	if _, ok := ParseSupervisorHand("answer"); ok {
		t.Error("ParseSupervisorHand accepted answer (S4b-3)")
	}
	if !reflect.DeepEqual(SupervisorActionResultValues(), []SupervisorActionResult{"executed", "failed", "rejected"}) ||
		!reflect.DeepEqual(SupervisorRejectReasonValues(), []SupervisorRejectReason{"state_changed", "owner_answered"}) ||
		!reflect.DeepEqual(SupervisorStateValues(), []SupervisorState{"pending", "handled", "escalated"}) {
		t.Error("a closed set of results, reject reasons or supervisor states differs from the specification")
	}
	// 止まり方ごとに選べる手の表（設計の表。answer と child_question は入れない）。
	want := map[StopKind][]SupervisorHand{
		"verify_uncertain":    {"reverify", "send_back", "split_followup", "replan", "defer", "escalate"},
		"artifact_missing":    {"reverify", "retry", "defer", "escalate"},
		"delegate_stopped":    {"retry", "replan", "defer", "escalate"},
		"facts_unmet":         {"send_back", "escalate"},
		"checks_failed":       {"send_back", "escalate"},
		"rework_limit":        {"send_back", "replan", "escalate"},
		"judgment_failed":     {"retry", "escalate"},
		"intake_unverified":   {"reverify", "defer", "escalate"},
		"upstream_unreadable": {"defer", "escalate"},
	}
	for _, k := range kinds {
		got := SupervisorHandsFor(k)
		if !reflect.DeepEqual(got, want[k]) {
			t.Errorf("hands for %s = %v, want %v", k, got, want[k])
		}
		// どの止まり方も、オーナーへ上げる手を選べる。
		if !supervisorHandAllowed(k, HandEscalate) {
			t.Errorf("%s cannot escalate", k)
		}
	}
	if SupervisorHandsFor("nope") != nil {
		t.Error("an unknown stop kind must have no hands")
	}
	// 返した一覧を書き換えても表は変わらない。
	got := SupervisorHandsFor(StopKindFactsUnmet)
	got[0] = HandDefer
	if SupervisorHandsFor(StopKindFactsUnmet)[0] != HandSendBack {
		t.Error("SupervisorHandsFor must return a copy")
	}
	if string(CyclePhaseSupervise) != "supervise" {
		t.Errorf("CyclePhaseSupervise = %q", CyclePhaseSupervise)
	}
}

// 保留を作る入口で保留を作り、課題の保留を返す。
func holdViaEntrance(t *testing.T, s *Store, status Status, kind HoldKind, in HoldOptionsInput) (string, error) {
	t.Helper()
	c := createAndAdvance(t, s, status)
	_, err := s.transition(context.Background(), ChannelCLI, c.ID, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		_, err := insertHoldWithOptions(ctx, tc, "どうする？", kind, tc.current.Status, nil, in)
		return err
	})
	return c.ID, err
}

func holdColumns(t *testing.T, s *Store, id string) (kind, state sql.NullString) {
	t.Helper()
	cid, _ := parseChallengeID(id)
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT stop_kind, supervisor_state FROM hold WHERE challenge_id = ?`, cid).Scan(&kind, &state)
	}); err != nil {
		t.Fatal(err)
	}
	return
}

// AC-31: 止まり方を渡さずに作った保留は、stop_kind も supervisor_state も NULL（宣言の supervisor が真でも）。
func TestHoldEntrance_WithoutAStopKindLeavesBothColumnsNull(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	id, err := holdViaEntrance(t, s, StatusInProgress, HoldKindHuman, HoldOptionsInput{SupervisorEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if kind, state := holdColumns(t, s, id); kind.Valid || state.Valid {
		t.Errorf("stop_kind=%v supervisor_state=%v, want NULL, NULL", kind, state)
	}
	// 既存の作る関数（insertHold）も NULL のまま。
	c := createAndAdvance(t, s, StatusInProgress)
	if _, err := s.transition(context.Background(), ChannelCLI, c.ID, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		return insertHold(ctx, tc, "q")
	}); err != nil {
		t.Fatal(err)
	}
	if kind, state := holdColumns(t, s, c.ID); kind.Valid || state.Valid {
		t.Errorf("insertHold: stop_kind=%v supervisor_state=%v, want NULL, NULL", kind, state)
	}
}

// 対象の止まり方が渡され、宣言の supervisor が真のとき、保留は同じ書き込みの中でスーパーバイザー待ち（pending）になる。偽のときは止まり方だけを記録する。
func TestHoldEntrance_StopKindMakesAPendingHoldOnlyWhenTheDeclarationEnablesIt(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	for _, enabled := range []bool{true, false} {
		id, err := holdViaEntrance(t, s, StatusVerifying, HoldKindHuman, HoldOptionsInput{StopKind: StopKindVerifyUncertain, SupervisorEnabled: enabled})
		if err != nil {
			t.Fatal(err)
		}
		kind, state := holdColumns(t, s, id)
		if kind.String != "verify_uncertain" {
			t.Errorf("enabled=%v: stop_kind = %v, want verify_uncertain", enabled, kind)
		}
		if enabled != (state.Valid && state.String == "pending") || (!enabled && state.Valid) {
			t.Errorf("enabled=%v: supervisor_state = %v", enabled, state)
		}
		c, err := s.GetChallenge(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		// 課題の状態の遷移は、いまの保留と同じ（人間対応待ち）。
		if c.Status != StatusAwaitingHuman || len(c.Holds) != 1 || c.Holds[0].StopKind != StopKindVerifyUncertain {
			t.Errorf("enabled=%v: status=%s holds=%+v", enabled, c.Status, c.Holds)
		}
		if enabled && c.Holds[0].SupervisorState != SupervisorStatePending {
			t.Errorf("Hold.SupervisorState = %q, want pending", c.Holds[0].SupervisorState)
		}
	}
}

// 発生つきの入口（insertHoldWithOccurrenceOptions）も同じ入口へ届く。
func TestHoldEntrance_TheOccurrenceEntranceReachesTheSameEntrance(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	c := createAndAdvance(t, s, StatusInProgress)
	if _, err := s.transition(context.Background(), ChannelCLI, c.ID, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		_, err := s.insertHoldWithOccurrenceOptions(ctx, tc, "q", holdReasonFactsUnmet, "", HoldOptionsInput{StopKind: StopKindFactsUnmet, SupervisorEnabled: true})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if kind, state := holdColumns(t, s, c.ID); kind.String != "facts_unmet" || state.String != "pending" {
		t.Errorf("stop_kind=%v supervisor_state=%v, want facts_unmet, pending", kind, state)
	}
}

// 閉集合の外の止まり方は断り（ErrValidation）、何も書かない。
func TestHoldEntrance_UnknownStopKindIsRejectedAndWritesNothing(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	id, err := holdViaEntrance(t, s, StatusInProgress, HoldKindHuman, HoldOptionsInput{StopKind: "child_question", SupervisorEnabled: true})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	c, err := s.GetChallenge(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != StatusInProgress || len(c.Holds) != 0 {
		t.Errorf("status=%s holds=%d, want in_progress and no hold", c.Status, len(c.Holds))
	}
}

// AC-37: 計画承認待ちの課題に、対話のセッションが引き取る保留でない保留を作ろうとすると、いまと同じ誤り（成立しない遷移）で断られる。
// 止まり方を渡しても同じ。対話のセッションが引き取る保留は作れる。
func TestHoldEntrance_AwaitingPlanApprovalOnlyTakesAParentDirectHold(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	for _, in := range []HoldOptionsInput{{}, {StopKind: StopKindVerifyUncertain, SupervisorEnabled: true}} {
		if _, err := holdViaEntrance(t, s, StatusAwaitingPlanApproval, HoldKindHuman, in); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("human hold from awaiting_plan_approval (%+v): err = %v, want ErrInvalidTransition", in, err)
		}
	}
	// 関数経由（既存の作る関数・発生つき）も同じ誤り。
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	if _, err := s.transition(context.Background(), ChannelCLI, c.ID, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		return insertHold(ctx, tc, "q")
	}); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("insertHold: err = %v, want ErrInvalidTransition", err)
	}
	if _, err := s.transition(context.Background(), ChannelCLI, c.ID, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		return s.insertHoldWithOccurrence(ctx, tc, "q", holdReasonTasksBlocked, "")
	}); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("insertHoldWithOccurrence: err = %v, want ErrInvalidTransition", err)
	}
	if _, err := holdViaEntrance(t, s, StatusAwaitingPlanApproval, HoldKindParentDirect, HoldOptionsInput{}); err != nil {
		t.Errorf("a parent_direct hold from awaiting_plan_approval: %v, want it to be accepted", err)
	}
}

// 対処の記録の API: 書く・タスクで引く・課題で古い順に引く・数える。
func TestSupervisorAction_RecordListAndCount(t *testing.T) {
	t.Parallel()
	s, _ := filledStore(t) // 課題 1・2、タスク 1・2（どちらも課題 1）、保留 1・2。対処の記録は 2 件（課題 1 のタスク 1 の split_followup と、課題 2 の断った記録）が入っている
	ctx := context.Background()
	rec := func(in SupervisorActionInput) *SupervisorAction {
		t.Helper()
		if in.CycleID == "" {
			in.CycleID = "Y-1"
		}
		a, err := s.RecordSupervisorAction(ctx, in)
		if err != nil {
			t.Fatalf("RecordSupervisorAction(%+v): %v", in, err)
		}
		return a
	}
	hold := int64(1)
	a1 := rec(SupervisorActionInput{ChallengeID: "C-1", TaskID: "T-1", HoldID: &hold, StopKind: StopKindVerifyUncertain, Hand: HandReverify, Findings: "f1", Note: "n1",
		Basis: []SupervisorBasis{{Path: "docs/a.md", Locator: "節 1"}}, Result: ActionExecuted})
	a2 := rec(SupervisorActionInput{ChallengeID: "C-1", TaskID: "T-1", StopKind: StopKindVerifyUncertain, Hand: HandSplitFollowup, Findings: "f2", Note: "n2",
		Conditions: []SupervisorCondition{{Number: 2, Text: "条件の文"}}, Result: ActionExecuted})
	rec(SupervisorActionInput{ChallengeID: "C-1", TaskID: "T-1", StopKind: StopKindVerifyUncertain, Hand: HandDefer, Findings: "f", Note: "n", Result: ActionExecuted})
	rec(SupervisorActionInput{ChallengeID: "C-1", TaskID: "T-1", StopKind: StopKindVerifyUncertain, Findings: "判断できなかった", Result: ActionFailed})
	rec(SupervisorActionInput{ChallengeID: "C-1", TaskID: "T-1", StopKind: StopKindVerifyUncertain, Hand: HandReplan, Findings: "f", Note: "n", Result: ActionRejected, RejectReason: RejectStateChanged})
	rec(SupervisorActionInput{ChallengeID: "C-1", TaskID: "T-2", StopKind: StopKindVerifyUncertain, Hand: HandReverify, Findings: "f", Note: "n", Result: ActionExecuted})
	rec(SupervisorActionInput{ChallengeID: "C-1", StopKind: StopKindVerifyUncertain, Hand: HandReverify, Findings: "f", Note: "n", Result: ActionExecuted}) // タスクの無い保留
	rec(SupervisorActionInput{ChallengeID: "C-1", TaskID: "T-1", StopKind: StopKindArtifactMissing, Hand: HandRetry, Findings: "f", Result: ActionExecuted})

	if a1.ID == 0 || a1.ChallengeID != "C-1" || a1.TaskID == nil || *a1.TaskID != "T-1" || a1.HoldID == nil || *a1.HoldID != 1 || a1.RunID != nil ||
		a1.Hand == nil || *a1.Hand != HandReverify || a1.RejectReason != nil || !reflect.DeepEqual(a1.Basis, []SupervisorBasis{{Path: "docs/a.md", Locator: "節 1"}}) || a1.Conditions != nil {
		t.Errorf("recorded action = %+v", a1)
	}
	if !reflect.DeepEqual(a2.Conditions, []SupervisorCondition{{Number: 2, Text: "条件の文"}}) {
		t.Errorf("conditions = %+v", a2.Conditions)
	}

	byTask, err := s.ListSupervisorActionsByTask(ctx, "T-1")
	if err != nil || len(byTask) != 7 {
		t.Fatalf("ListSupervisorActionsByTask(T-1) = %d actions, %v; want 7 (6 recorded here and 1 of the fixture)", len(byTask), err)
	}
	byChallenge, err := s.ListSupervisorActionsByChallenge(ctx, "C-1")
	if err != nil || len(byChallenge) != 9 {
		t.Fatalf("ListSupervisorActionsByChallenge(C-1) = %d actions, %v; want 9", len(byChallenge), err)
	}
	for i := 1; i < len(byChallenge); i++ {
		if byChallenge[i-1].ID >= byChallenge[i].ID {
			t.Errorf("not oldest first: %d then %d", byChallenge[i-1].ID, byChallenge[i].ID)
		}
	}
	if only, err := s.ListSupervisorActionsByChallenge(ctx, "C-2"); err != nil || len(only) != 1 || only[0].Result != ActionRejected || only[0].Hand != nil || only[0].RejectReason == nil || *only[0].RejectReason != RejectStateChanged {
		t.Errorf("C-2 = %+v, %v; want the fixture's one rejected record without a hand", only, err)
	}
	if _, err := s.ListSupervisorActionsByChallenge(ctx, "C-99"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown challenge: %v, want ErrNotFound", err)
	}

	count := func(q SupervisorActionCountQuery) int {
		t.Helper()
		n, err := s.CountSupervisorActions(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	// 数えはタスクと止まり方で。
	if n := count(SupervisorActionCountQuery{ChallengeID: "C-1", TaskID: "T-1", StopKind: StopKindVerifyUncertain}); n != 6 {
		t.Errorf("T-1 verify_uncertain = %d, want 6", n)
	}
	if n := count(SupervisorActionCountQuery{ChallengeID: "C-1", TaskID: "T-2", StopKind: StopKindVerifyUncertain}); n != 1 {
		t.Errorf("T-2 verify_uncertain = %d, want 1", n)
	}
	if n := count(SupervisorActionCountQuery{ChallengeID: "C-1", StopKind: StopKindVerifyUncertain}); n != 1 {
		t.Errorf("no task verify_uncertain = %d, want 1 (a hold without a task counts by challenge)", n)
	}
	// 回数の上限の数え: 結果が executed で、手が見送り・上げるでない（手の無い記録・断った記録・失敗は数えない）。
	if n := count(SupervisorHandledCountQuery("C-1", "T-1", StopKindVerifyUncertain)); n != 3 {
		t.Errorf("handled count of T-1 verify_uncertain = %d, want 3 (the fixture's split_followup, reverify, split_followup)", n)
	}
	if n := count(SupervisorActionCountQuery{ChallengeID: "C-1", TaskID: "T-1", StopKind: StopKindArtifactMissing, Hands: []SupervisorHand{HandRetry}, ExecutedOnly: true}); n != 1 {
		t.Errorf("executed retry = %d, want 1", n)
	}
	// 最後のオーナーの回答より後だけを数える。supervisor が閉じた回答は、オーナーの回答に数えない。
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at, answer, answered_at, answered_by) VALUES (1, 'q', 'verifying', '2999-01-01T00:00:00.000Z', 'a', '2999-01-01T00:00:00.000Z', ?)`, supervisorActorName); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := count(SupervisorHandledCountQuery("C-1", "T-1", StopKindVerifyUncertain)); n != 3 {
		t.Errorf("after a supervisor's own answer: %d, want 3 (it is not an owner answer)", n)
	}
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at, answer, answered_at, answered_by) VALUES (1, 'q', 'verifying', '2000-01-01T00:00:00.000Z', 'a', '2999-01-01T00:00:00.000Z', 'alice')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := count(SupervisorHandledCountQuery("C-1", "T-1", StopKindVerifyUncertain)); n != 0 {
		t.Errorf("after the owner's answer: %d, want 0 (records before it are not counted)", n)
	}
	if n := count(SupervisorActionCountQuery{ChallengeID: "C-1", TaskID: "T-1", StopKind: StopKindVerifyUncertain}); n != 6 {
		t.Errorf("without AfterLastOwnerAnswer: %d, want 6", n)
	}
}

func TestSupervisorAction_RejectsMalformedInput(t *testing.T) {
	t.Parallel()
	s, _ := filledStore(t)
	ok := SupervisorActionInput{CycleID: "Y-1", ChallengeID: "C-1", StopKind: StopKindFactsUnmet, Findings: "f", Result: ActionExecuted, Hand: HandSendBack}
	cases := map[string]func(in *SupervisorActionInput){
		"unknown stop kind":          func(in *SupervisorActionInput) { in.StopKind = "child_question" },
		"unknown hand":               func(in *SupervisorActionInput) { in.Hand = "answer" },
		"unknown result":             func(in *SupervisorActionInput) { in.Result = "done" },
		"rejected without a reason":  func(in *SupervisorActionInput) { in.Result = ActionRejected },
		"a reason without rejection": func(in *SupervisorActionInput) { in.RejectReason = RejectStateChanged },
		"unknown reject reason":      func(in *SupervisorActionInput) { in.Result = ActionRejected; in.RejectReason = "other" },
		"bad challenge id":           func(in *SupervisorActionInput) { in.ChallengeID = "1" },
		"bad task id":                func(in *SupervisorActionInput) { in.TaskID = "x" },
		"empty cycle":                func(in *SupervisorActionInput) { in.CycleID = "" },
		"condition number below one": func(in *SupervisorActionInput) { in.Conditions = []SupervisorCondition{{Number: 0, Text: "x"}} },
		"bad run id":                 func(in *SupervisorActionInput) { in.RunID = "x" },
	}
	for name, mut := range cases {
		in := ok
		mut(&in)
		if _, err := s.RecordSupervisorAction(context.Background(), in); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: err = %v, want ErrValidation", name, err)
		}
	}
	before, _ := s.ListSupervisorActionsByChallenge(context.Background(), "C-1")
	if _, err := s.RecordSupervisorAction(context.Background(), ok); err != nil {
		t.Fatal(err)
	}
	after, _ := s.ListSupervisorActionsByChallenge(context.Background(), "C-1")
	if len(after) != len(before)+1 {
		t.Errorf("valid record: %d -> %d actions, want +1 (the invalid ones wrote nothing)", len(before), len(after))
	}
}

// AC-34・AC-33: 宣言の supervisor は、無ければ真、真偽値でなければ読み込みのエラー（config_invalid）。
func TestAgentDeclaration_SupervisorKey(t *testing.T) {
	t.Parallel()
	d, err := LoadAgentDeclaration(writeAgentJSON(t, `{}`))
	if err != nil || !d.Supervisor {
		t.Fatalf("no supervisor key: Supervisor=%v err=%v, want true", d != nil && d.Supervisor, err)
	}
	if d, _ := LoadAgentDeclaration(t.TempDir()); !d.Supervisor {
		t.Error("no agent.json: Supervisor must default to true")
	}
	d, err = LoadAgentDeclaration(writeAgentJSON(t, `{"supervisor": false}`))
	if err != nil || d.Supervisor {
		t.Errorf(`"supervisor": false: Supervisor=%v err=%v`, d != nil && d.Supervisor, err)
	}
	d, err = LoadAgentDeclaration(writeAgentJSON(t, `{"supervisor": true}`))
	if err != nil || !d.Supervisor {
		t.Errorf(`"supervisor": true: Supervisor=%v err=%v`, d != nil && d.Supervisor, err)
	}
	for _, v := range []string{`"yes"`, `1`, `null`, `0`, `"false"`, `[]`} {
		if _, err := LoadAgentDeclaration(writeAgentJSON(t, `{"supervisor": `+v+`}`)); !errors.Is(err, ErrConfigInvalid) {
			t.Errorf("supervisor=%s: err = %v, want ErrConfigInvalid", v, err)
		}
	}
	// 検証済みの宣言のキーの一覧に supervisor がある（禁止語の生成元）。
	found := false
	for _, k := range AgentDeclarationKeys() {
		found = found || k == "supervisor"
	}
	if !found {
		t.Error("AgentDeclarationKeys() lacks supervisor")
	}
}

// AC-66・AC-67: スーパーバイザーの判断点の費用の上限は、宣言の judgment_budget_usd.SV の値で、無ければ 5 USD。J4 のキーは読めるまま。
func TestAgentDeclaration_SVBudget(t *testing.T) {
	t.Parallel()
	d, err := LoadAgentDeclaration(writeAgentJSON(t, `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := d.JudgmentBudgetFor(JudgmentSV); got != 5 {
		t.Errorf("default SV budget = %v, want 5", got)
	}
	d, err = LoadAgentDeclaration(writeAgentJSON(t, `{"judgment_budget_usd": {"SV": 7.5}}`))
	if err != nil || d.JudgmentBudgetFor(JudgmentSV) != 7.5 {
		t.Errorf("SV = 7.5: budget=%v err=%v", d != nil && d.JudgmentBudgetFor(JudgmentSV) == 7.5, err)
	}
	if d.JudgmentBudgetFor(JudgmentJ1) != 1 {
		t.Errorf("other judgment budgets must keep their defaults: J1 = %v", d.JudgmentBudgetFor(JudgmentJ1))
	}
	// J4 は宣言に書いてあっても読める（使わない）。
	d, err = LoadAgentDeclaration(writeAgentJSON(t, `{"judgment_budget_usd": {"J4": 3, "SV": 2}}`))
	if err != nil || d.JudgmentBudgetFor(JudgmentJ4) != 3 || d.JudgmentBudgetFor(JudgmentSV) != 2 {
		t.Errorf("J4 and SV: err=%v", err)
	}
	for _, v := range []string{`0`, `-1`, `"5"`, `null`} {
		if _, err := LoadAgentDeclaration(writeAgentJSON(t, `{"judgment_budget_usd": {"SV": `+v+`}}`)); !errors.Is(err, ErrConfigInvalid) {
			t.Errorf("SV=%s: err = %v, want ErrConfigInvalid", v, err)
		}
	}
	if JudgmentSV.valid() == false || JudgmentSV.hasNoChallenge() {
		t.Error("SV is a judgment point that belongs to a challenge")
	}
}

func TestParseSVOutput(t *testing.T) {
	t.Parallel()
	all := SupervisorHandValues()
	good := map[string]string{
		"reverify":       `{"hand":"reverify","findings":"f","note":"n","basis":[{"path":"a.md","locator":"1"}],"recommendation":"","options":[],"conditions":[]}`,
		"defer":          `{"hand":"defer","findings":"f","note":"待つ","basis":[],"recommendation":"","options":[],"conditions":[]}`,
		"retry":          `{"hand":"retry","findings":"f","note":"","basis":[],"recommendation":"","options":[],"conditions":[]}`,
		"split_followup": `{"hand":"split_followup","findings":"f","note":"n","basis":[],"recommendation":"","options":[],"conditions":[2,1]}`,
		"escalate":       `{"hand":"escalate","findings":"f","note":"","basis":[],"recommendation":"推奨","options":[{"label":"足す","description":""},{"label":"足さない","description":"説明"}],"conditions":[]}`,
	}
	for name, raw := range good {
		out, err := ParseSVOutput([]byte(raw), all)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if string(out.Hand) != name {
			t.Errorf("%s: hand = %q", name, out.Hand)
		}
	}
	bad := map[string]string{
		"unknown hand":                      `{"hand":"answer","findings":"f","note":"n","basis":[],"recommendation":"","options":[],"conditions":[]}`,
		"empty findings":                    `{"hand":"retry","findings":" ","note":"","basis":[],"recommendation":"","options":[],"conditions":[]}`,
		"reverify without a note":           `{"hand":"reverify","findings":"f","note":"","basis":[],"recommendation":"","options":[],"conditions":[]}`,
		"escalate without a recommendation": `{"hand":"escalate","findings":"f","note":"","basis":[],"recommendation":"","options":[],"conditions":[]}`,
		"split without conditions":          `{"hand":"split_followup","findings":"f","note":"n","basis":[],"recommendation":"","options":[],"conditions":[]}`,
		"split with a duplicate":            `{"hand":"split_followup","findings":"f","note":"n","basis":[],"recommendation":"","options":[],"conditions":[1,1]}`,
		"split with zero":                   `{"hand":"split_followup","findings":"f","note":"n","basis":[],"recommendation":"","options":[],"conditions":[0]}`,
		"options on a non-escalate":         `{"hand":"reverify","findings":"f","note":"n","basis":[],"recommendation":"","options":[{"id":"a","label":"x","description":""}],"conditions":[]}`,
		"a bad option":                      `{"hand":"escalate","findings":"f","note":"","basis":[],"recommendation":"r","options":[{"id":"a","label":"","description":""}],"conditions":[]}`,
		"an unknown key":                    `{"hand":"retry","findings":"f","note":"","basis":[],"recommendation":"","options":[],"conditions":[],"answers":[]}`,
		"not JSON":                          `nope`,
		"trailing data":                     `{"hand":"retry","findings":"f"} {}`,
	}
	for name, raw := range bad {
		if _, err := ParseSVOutput([]byte(raw), all); !errors.Is(err, ErrSVOutputInvalid) {
			t.Errorf("%s: err = %v, want ErrSVOutputInvalid", name, err)
		}
	}
	// 入力に渡した選べる手の外の手は誤り。
	if _, err := ParseSVOutput([]byte(good["reverify"]), []SupervisorHand{HandDefer, HandEscalate}); !errors.Is(err, ErrSVOutputInvalid) {
		t.Errorf("a hand outside the offered ones: %v, want ErrSVOutputInvalid", err)
	}
	if out, err := ParseSVOutput([]byte(good["escalate"]), all); err != nil || !reflect.DeepEqual(out.HoldOptions(), []HoldOption{{Label: "足す"}, {Label: "足さない", Description: "説明"}}) {
		t.Errorf("HoldOptions = %+v, %v (ids are left to the entrance)", out, err)
	}
	// スキーマは手の閉集合から作る。
	schema := string(SVOutputSchema())
	for _, h := range all {
		if !strings.Contains(schema, `"`+string(h)+`"`) {
			t.Errorf("the output schema lacks the hand %q", h)
		}
	}
	if strings.Contains(schema, `"answer"`) {
		t.Error("the output schema must not offer answer (S4b-3)")
	}
}

// 手の実行の入口: 振り分けは 1 か所の switch。escalate 以外の手は、まだ無い（雛形）。未知の手は検証の誤り。
func TestExecuteSupervisorHand_OnlyEscalateIsImplementedYet(t *testing.T) {
	t.Parallel()
	s, _ := newWorkspaceStore(t)
	for _, h := range SupervisorHandValues() {
		if h == HandEscalate || h == HandDefer { // defer は supervisor_defer_test.go が検証する
			continue
		}
		_, err := s.executeSupervisorHand(context.Background(), supervisorHandInput{Output: SVOutput{Hand: h}})
		if !errors.Is(err, ErrSupervisorHandNotImplemented) {
			t.Errorf("hand %s: err = %v, want ErrSupervisorHandNotImplemented", h, err)
		}
	}
	if _, err := s.executeSupervisorHand(context.Background(), supervisorHandInput{Output: SVOutput{Hand: "answer"}}); !errors.Is(err, ErrValidation) {
		t.Errorf("an unknown hand: err = %v, want ErrValidation", err)
	}
}

// フォローアップは、出どころ supervisor とぶら下がるタスクを持てる。
func TestFollowup_SupervisorSourceAndTask(t *testing.T) {
	t.Parallel()
	s, _ := filledStore(t)
	var id int64
	if err := s.mutate(context.Background(), ChannelCLI, func(tx *sql.Tx, rec *activityRecorder) error {
		var err error
		id, err = createFollowup(context.Background(), tx, rec, FollowupInput{ChallengeID: "C-1", Title: "残った条件", Source: FollowupSourceSupervisor, TaskID: "T-1"})
		if err != nil {
			return err
		}
		if _, err := createFollowup(context.Background(), tx, rec, FollowupInput{ChallengeID: "C-1", Title: "x", Source: "other"}); !errors.Is(err, ErrValidation) {
			t.Errorf("an unknown source: %v, want ErrValidation", err)
		}
		if _, err := createFollowup(context.Background(), tx, rec, FollowupInput{ChallengeID: "C-1", Title: "x", Source: FollowupSourceSupervisor, TaskID: "bad"}); !errors.Is(err, ErrValidation) {
			t.Errorf("a bad task id: %v, want ErrValidation", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var source string
	var taskID sql.NullInt64
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT source, task_id FROM followup WHERE id = ?`, id).Scan(&source, &taskID)
	}); err != nil {
		t.Fatal(err)
	}
	if source != "supervisor" || !taskID.Valid || taskID.Int64 != 1 {
		t.Errorf("source=%q task_id=%v, want supervisor and 1", source, taskID)
	}
	if !reflect.DeepEqual(FollowupSourceValues(), []FollowupSource{"learn", "supervisor"}) {
		t.Errorf("FollowupSourceValues = %v", FollowupSourceValues())
	}
}
