// このファイルは課題カードの「現在の状態」と「人間の次の操作」を導く公開 API
// （DeriveCard）を持つ。値は読むたびに決定的に導き、保存しない。表示文は持たず、
// 閉集合のコードだけを返す（表示文は UI が持つ）。ストアへは書かず、中断した run の
// 回収（ReapInterruptedRuns）も呼ばない。

package core

import (
	"context"
	"database/sql"
	"time"
)

// CardAction は Card.NextHumanActions の値（閉集合。導く順に並べる）。
type CardAction string

// CardAction の値。
const (
	CardActionApprovePlan       CardAction = "approve_plan"
	CardActionApproveCompletion CardAction = "approve_completion"
	CardActionAnswerQuestion    CardAction = "answer_question"
	CardActionTriage            CardAction = "triage"
	CardActionIncreaseBudget    CardAction = "increase_budget"
	CardActionApproveOperation  CardAction = "approve_operation"
	CardActionReviewDiscrepancy CardAction = "review_discrepancy"
	CardActionClearSlot         CardAction = "clear_slot"
)

// CardActionValues は CardAction の閉集合（導く順）の写しを返す。
func CardActionValues() []CardAction {
	return []CardAction{
		CardActionApprovePlan, CardActionApproveCompletion, CardActionAnswerQuestion, CardActionTriage,
		CardActionIncreaseBudget, CardActionApproveOperation, CardActionReviewDiscrepancy, CardActionClearSlot,
	}
}

// CardModifier は Card.Modifiers の値（閉集合。導く順に並べる）。
type CardModifier string

// CardModifier の値。
const (
	CardModifierRunning         CardModifier = "running"
	CardModifierUnresponsive    CardModifier = "unresponsive"
	CardModifierWaitingCI       CardModifier = "waiting_ci"
	CardModifierBudgetExhausted CardModifier = "budget_exhausted"
	CardModifierDiscrepancy     CardModifier = "discrepancy"
)

// CardModifierValues は CardModifier の閉集合（導く順）の写しを返す。
func CardModifierValues() []CardModifier {
	return []CardModifier{
		CardModifierRunning, CardModifierUnresponsive, CardModifierWaitingCI,
		CardModifierBudgetExhausted, CardModifierDiscrepancy,
	}
}

// Card は課題カードの導出結果。Headline・Context は S1 では常に nil。
type Card struct {
	State            Status
	Modifiers        []CardModifier
	NextHumanActions []CardAction
	// SlotIDs は、スロットごとの直近の run（run.slot_id が一致する run のうち最新）がこの課題の
	// run であるスロットの ID（昇順の文字列）。過去にスロットを使っただけの課題には載らない。
	SlotIDs  []string
	Headline *string
	Context  *string
}

// CardRuns は、課題のカードの導出に要る run（終了済みを含む）を、ストアから読んだ時点のまま持つ。
// ストアの読み取りと、外部（GitHub）の応答を待つ導出を、別の借りに分けずに済ませるために使う。
type CardRuns struct {
	runs  []*runRow
	slots []string
	stale time.Duration
}

// LoadCardRuns は challenge の run をストアから読む。
func (s *Store) LoadCardRuns(ctx context.Context, challenge Challenge) (*CardRuns, error) {
	var runs []*runRow
	var slots []string
	if id, ok := parseChallengeID(challenge.ID); ok {
		err := s.db.Read(ctx, func(tx *sql.Tx) error {
			rows, err := tx.QueryContext(ctx, runSelectColumns+" WHERE challenge_id = ? ORDER BY started_at ASC", id)
			if err != nil {
				return err
			}
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				r, err := scanRunRow(rows)
				if err != nil {
					return err
				}
				runs = append(runs, r)
			}
			if err := rows.Err(); err != nil {
				return err
			}
			// スロットごとの直近の run がこの課題の run であるスロット（同時刻は id の大きい方を新しいとする）。
			srows, err := tx.QueryContext(ctx, `SELECT DISTINCT r.slot_id FROM run r WHERE r.challenge_id = ? AND r.slot_id IS NOT NULL
				AND NOT EXISTS (SELECT 1 FROM run r2 WHERE r2.slot_id = r.slot_id
					AND (r2.started_at > r.started_at OR (r2.started_at = r.started_at AND r2.id > r.id)))
				ORDER BY r.slot_id ASC`, id)
			if err != nil {
				return err
			}
			defer func() { _ = srows.Close() }()
			for srows.Next() {
				var n int64
				if err := srows.Scan(&n); err != nil {
					return err
				}
				slots = append(slots, formatSlotID(n))
			}
			return srows.Err()
		})
		if err = classifyReadWriteErr(err); err != nil {
			return nil, err
		}
	}
	return &CardRuns{runs: runs, slots: slots, stale: s.effectiveStaleAfter()}, nil
}

// Derive は読んだ run から challenge のカードを導く（ストアに触れない）。
func (r *CardRuns) Derive(challenge Challenge, st *Overview, now time.Time) *Card {
	return deriveCard(challenge, st, r.runs, r.slots, now, r.stale)
}

// DeriveCard は challenge のカードを導く。st は status の結果（GetOverviewFor の結果に
// ListWaitingExternal の結果を WaitingExternal へ入れたもの）、now は現在時刻。
// 課題の run（終了済みを含む）だけをストアから読む。
func (s *Store) DeriveCard(ctx context.Context, challenge Challenge, st *Overview, now time.Time) (*Card, error) {
	r, err := s.LoadCardRuns(ctx, challenge)
	if err != nil {
		return nil, err
	}
	return r.Derive(challenge, st, now), nil
}

func deriveCard(c Challenge, st *Overview, runs []*runRow, ownSlots []string, now time.Time, stale time.Duration) *Card {
	if st == nil {
		st = &Overview{}
	}
	card := &Card{State: c.Status, Modifiers: []CardModifier{}, NextHumanActions: []CardAction{}, SlotIDs: []string{}}

	running, unresponsive := false, false
	linkedSlots := map[string]bool{}
	for _, id := range ownSlots {
		linkedSlots[id] = true
		card.SlotIDs = append(card.SlotIDs, id)
	}
	for _, r := range runs {
		if r.EndedAt == nil && r.Result == "" {
			running = true
			if now.Sub(r.HeartbeatAt) > stale {
				unresponsive = true
			}
		}
	}

	inTriage, inBudget, inDisc, waiting := false, false, false, false
	for _, t := range st.NeedsHumanTriage {
		inTriage = inTriage || t.ChallengeID == c.ID
	}
	for _, b := range st.NeedsHumanBudgetExhausted {
		inBudget = inBudget || b.ChallengeID == c.ID
	}
	for _, d := range st.Discrepancies {
		inDisc = inDisc || d.ChallengeID == c.ID
	}
	for _, w := range st.WaitingExternal {
		waiting = waiting || w.ChallengeID == c.ID
	}
	pendingOp := false
	for _, o := range st.NeedsHumanOperations {
		pendingOp = pendingOp || (o.ChallengeID == c.ID && o.State == OperationStatePending)
	}
	slotAttention := false
	for _, sl := range st.NeedsHumanSlots {
		slotAttention = slotAttention || linkedSlots[sl.SlotID]
	}

	addA := func(cond bool, a CardAction) {
		if cond {
			card.NextHumanActions = append(card.NextHumanActions, a)
		}
	}
	addA(c.Status == StatusAwaitingPlanApproval, CardActionApprovePlan)
	addA(c.Status == StatusAwaitingCompletionApproval, CardActionApproveCompletion)
	addA(c.Status == StatusAwaitingHuman, CardActionAnswerQuestion)
	addA(inTriage, CardActionTriage)
	addA(inBudget, CardActionIncreaseBudget)
	addA(pendingOp, CardActionApproveOperation)
	addA(inDisc, CardActionReviewDiscrepancy)
	addA(slotAttention, CardActionClearSlot)

	addM := func(cond bool, m CardModifier) {
		if cond {
			card.Modifiers = append(card.Modifiers, m)
		}
	}
	addM(running, CardModifierRunning)
	addM(unresponsive, CardModifierUnresponsive)
	addM(waiting, CardModifierWaitingCI)
	addM(inBudget, CardModifierBudgetExhausted)
	addM(inDisc, CardModifierDiscrepancy)
	return card
}
