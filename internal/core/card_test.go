package core

import (
	"context"
	"database/sql"
	"os"
	"reflect"
	"testing"
	"time"
)

func cardChallenge(t *testing.T, s *Store, status Status) Challenge {
	t.Helper()
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t", Description: "d", DoneCriteria: "d"})
	if err != nil {
		t.Fatal(err)
	}
	n, _ := parseChallengeID(ch.ID)
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET status = ? WHERE id = ?`, string(status), n)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got.Challenge
}

func cardHost(t *testing.T) string {
	t.Helper()
	h, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func cardInsertRun(t *testing.T, s *Store, c Challenge, hb time.Time, slotID *int64) *runRow {
	t.Helper()
	cid, _ := parseChallengeID(c.ID)
	var r *runRow
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		r, err = insertRun(context.Background(), tx, cid, insertRunInput{Kind: runKindDelegate, ChallengeVersion: 1,
			SessionID: "55555555-5555-4555-8555-555555555555", PID: 2147483646, Host: cardHost(t), HeartbeatAt: hb, StartedAt: hb,
			MaxBudgetUSD: 1_000_000, BudgetBucket: budgetBucketImpl, SlotID: slotID})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDeriveCard_HeartbeatBoundary(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		sec  int
		want []CardModifier
	}{
		{299, []CardModifier{CardModifierRunning}},
		{300, []CardModifier{CardModifierRunning}},
		{301, []CardModifier{CardModifierRunning, CardModifierUnresponsive}},
	} {
		s := newStoreForTest(t)
		c := cardChallenge(t, s, StatusInProgress)
		cardInsertRun(t, s, c, now.Add(-time.Duration(tc.sec)*time.Second), nil)
		card, err := s.DeriveCard(context.Background(), c, &Overview{}, now)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(card.Modifiers, tc.want) {
			t.Errorf("%ds: modifiers = %v, want %v", tc.sec, card.Modifiers, tc.want)
		}
		if card.State != StatusInProgress || card.Headline != nil || card.Context != nil || len(card.NextHumanActions) != 0 {
			t.Errorf("%ds: card = %+v", tc.sec, card)
		}
	}
}

func TestDeriveCard_NoRunNoModifiersAndEmptyLists(t *testing.T) {
	s := newStoreForTest(t)
	c := cardChallenge(t, s, StatusClassified)
	card, err := s.DeriveCard(context.Background(), c, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if card.Modifiers == nil || card.NextHumanActions == nil || len(card.Modifiers) != 0 || len(card.NextHumanActions) != 0 {
		t.Errorf("card = %+v, want empty non-nil lists", card)
	}
}

// 8 つのうち 3 つ以上（計画承認待ち・triage・予算切れ・未承認操作・食い違い）が重なる課題は定義順で並ぶ。
func TestDeriveCard_ActionsOrderMultiple(t *testing.T) {
	c := Challenge{ID: "C-1", Status: StatusAwaitingPlanApproval}
	st := &Overview{
		NeedsHumanTriage:          []TriageItem{{ChallengeID: "C-1"}},
		NeedsHumanBudgetExhausted: []BudgetExhausted{{ChallengeID: "C-1"}},
		NeedsHumanOperations:      []IrreversibleOperation{{ChallengeID: "C-1", State: OperationStatePending}, {ChallengeID: "C-1", State: OperationStatePending}},
		Discrepancies:             []Discrepancy{{ChallengeID: "C-1"}},
	}
	card := deriveCard(c, st, nil, time.Now(), defaultStaleAfter)
	want := []CardAction{CardActionApprovePlan, CardActionTriage, CardActionIncreaseBudget, CardActionApproveOperation, CardActionReviewDiscrepancy}
	if !reflect.DeepEqual(card.NextHumanActions, want) {
		t.Errorf("actions = %v, want %v", card.NextHumanActions, want)
	}
}

func TestDeriveCard_OtherChallengesIgnoredAndCompletedPendingOp(t *testing.T) {
	st := &Overview{
		NeedsHumanTriage:     []TriageItem{{ChallengeID: "C-2"}},
		NeedsHumanOperations: []IrreversibleOperation{{ChallengeID: "C-1", State: OperationStatePending}},
	}
	card := deriveCard(Challenge{ID: "C-1", Status: StatusDone}, st, nil, time.Now(), defaultStaleAfter)
	if !reflect.DeepEqual(card.NextHumanActions, []CardAction{CardActionApproveOperation}) {
		t.Errorf("actions = %v", card.NextHumanActions)
	}
}

func TestDeriveCard_StateActions(t *testing.T) {
	for st, want := range map[Status]CardAction{
		StatusAwaitingPlanApproval:       CardActionApprovePlan,
		StatusAwaitingCompletionApproval: CardActionApproveCompletion,
		StatusAwaitingHuman:              CardActionAnswerQuestion,
	} {
		card := deriveCard(Challenge{ID: "C-1", Status: st}, &Overview{}, nil, time.Now(), defaultStaleAfter)
		if !reflect.DeepEqual(card.NextHumanActions, []CardAction{want}) {
			t.Errorf("%s: %v", st, card.NextHumanActions)
		}
	}
}

// 5 つの modifier がすべて成り立つ課題は定義順で並び、スロットは run.slot_id 経由で結ぶ。
func TestDeriveCard_AllModifiersAndSlotLink(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	c := cardChallenge(t, s, StatusAwaitingHuman)

	var slotID int64
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		r, err := insertSlot(ctx, tx, insertSlotInput{Repo: "r", Provider: slotProviderWorktree, Path: "/p"})
		if err != nil {
			return err
		}
		n, _ := parseSlotID(r.ID)
		slotID = n
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// 終了済みの run がスロットを持ち、スロットは needs_attention。
	old := cardInsertRun(t, s, c, now.Add(-time.Hour), &slotID)
	oid, _ := parseRunID(old.ID)
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := updateRunEnd(ctx, tx, oid, updateRunEndInput{EndedAt: now.Add(-time.Minute), Result: RunResultInterrupted, CostSource: CostSourceUnknown}); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE slot SET state = 'needs_attention', attention_reason = 'x' WHERE id = ?`, slotID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	cardInsertRun(t, s, c, now.Add(-10*time.Minute), nil) // 応答なしの未終了 run

	st, err := s.GetOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.NeedsHumanSlots) != 1 {
		t.Fatalf("slots = %+v", st.NeedsHumanSlots)
	}
	st.NeedsHumanBudgetExhausted = []BudgetExhausted{{ChallengeID: c.ID}}
	st.Discrepancies = []Discrepancy{{ChallengeID: c.ID}}
	st.WaitingExternal = []WaitingExternal{{ChallengeID: c.ID}}

	card, err := s.DeriveCard(ctx, c, st, now)
	if err != nil {
		t.Fatal(err)
	}
	wantM := []CardModifier{CardModifierRunning, CardModifierUnresponsive, CardModifierWaitingCI, CardModifierBudgetExhausted, CardModifierDiscrepancy}
	if !reflect.DeepEqual(card.Modifiers, wantM) {
		t.Errorf("modifiers = %v, want %v", card.Modifiers, wantM)
	}
	wantA := []CardAction{CardActionAnswerQuestion, CardActionIncreaseBudget, CardActionReviewDiscrepancy, CardActionClearSlot}
	if !reflect.DeepEqual(card.NextHumanActions, wantA) {
		t.Errorf("actions = %v, want %v", card.NextHumanActions, wantA)
	}

	// 導出は書かない: 未終了 run は回収されないまま。
	var open int
	if err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM run WHERE result IS NULL`).Scan(&open)
	}); err != nil || open != 1 {
		t.Errorf("open runs = %d, err = %v", open, err)
	}
	// 対照: 同じ run は ReapInterruptedRuns なら回収される（上の確認が恒真でないことの根拠）。
	if err := s.ReapInterruptedRuns(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM run WHERE result IS NULL`).Scan(&open)
	}); err != nil || open != 0 {
		t.Errorf("after reap open runs = %d, err = %v", open, err)
	}
}

// 他の課題の行・他の課題の run に結んだスロット・終了済みの古い run だけでは、何も付かない。
func TestDeriveCard_OtherChallengesAndEndedRunsDoNotLeak(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	c := cardChallenge(t, s, StatusInProgress)
	other := cardChallenge(t, s, StatusInProgress)

	var slotID int64
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		r, err := insertSlot(ctx, tx, insertSlotInput{Repo: "r", Provider: slotProviderWorktree, Path: "/p"})
		if err != nil {
			return err
		}
		slotID, _ = parseSlotID(r.ID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// c には古い heartbeat の終了済み run だけ。スロットは other の run に結ぶ。
	ended := cardInsertRun(t, s, c, now.Add(-time.Hour), nil)
	otherRun := cardInsertRun(t, s, other, now, &slotID)
	for _, r := range []*runRow{ended, otherRun} {
		id, _ := parseRunID(r.ID)
		if err := s.db.Write(ctx, func(tx *sql.Tx) error {
			_, err := updateRunEnd(ctx, tx, id, updateRunEndInput{EndedAt: now, Result: RunResultInterrupted, CostSource: CostSourceUnknown})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE slot SET state = 'needs_attention', attention_reason = 'x' WHERE id = ?`, slotID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	st, err := s.GetOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st.NeedsHumanBudgetExhausted = []BudgetExhausted{{ChallengeID: other.ID}}
	st.Discrepancies = []Discrepancy{{ChallengeID: other.ID}}
	st.WaitingExternal = []WaitingExternal{{ChallengeID: other.ID}}
	st.NeedsHumanOperations = []IrreversibleOperation{{ChallengeID: other.ID, State: OperationStatePending}}

	card, err := s.DeriveCard(ctx, c, st, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(card.Modifiers) != 0 || len(card.NextHumanActions) != 0 {
		t.Errorf("card = %+v, want no modifiers/actions", card)
	}
	otherCard, err := s.DeriveCard(ctx, other, st, now)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(otherCard.NextHumanActions, []CardAction{CardActionIncreaseBudget, CardActionApproveOperation, CardActionReviewDiscrepancy, CardActionClearSlot}) {
		t.Errorf("other actions = %v", otherCard.NextHumanActions)
	}
}

// 閾値は s.staleAfter（ReapInterruptedRuns と同じ値）に従う。
func TestDeriveCard_UsesStoreStaleThreshold(t *testing.T) {
	s := newStoreForTest(t)
	s.staleAfter = 10 * time.Second
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	c := cardChallenge(t, s, StatusInProgress)
	cardInsertRun(t, s, c, now.Add(-11*time.Second), nil)
	card, err := s.DeriveCard(context.Background(), c, &Overview{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(card.Modifiers, []CardModifier{CardModifierRunning, CardModifierUnresponsive}) {
		t.Errorf("modifiers = %v", card.Modifiers)
	}
}
