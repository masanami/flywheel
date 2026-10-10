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

// Card は課題カードの導出結果。Headline・Context は S1 では常に nil。
type Card struct {
	State            Status
	Modifiers        []CardModifier
	NextHumanActions []CardAction
	Headline         *string
	Context          *string
}

// DeriveCard は challenge のカードを導く。st は status の結果（GetOverviewFor の結果に
// ListWaitingExternal の結果を WaitingExternal へ入れたもの）、now は現在時刻。
// 課題の run（終了済みを含む）だけをストアから読む。
func (s *Store) DeriveCard(ctx context.Context, challenge Challenge, st *Overview, now time.Time) (*Card, error) {
	var runs []*runRow
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
			return rows.Err()
		})
		if err = classifyReadWriteErr(err); err != nil {
			return nil, err
		}
	}
	return deriveCard(challenge, st, runs, now, s.effectiveStaleAfter()), nil
}

func deriveCard(c Challenge, st *Overview, runs []*runRow, now time.Time, stale time.Duration) *Card {
	if st == nil {
		st = &Overview{}
	}
	card := &Card{State: c.Status, Modifiers: []CardModifier{}, NextHumanActions: []CardAction{}}

	running, unresponsive := false, false
	linkedSlots := map[string]bool{}
	for _, r := range runs {
		if r.SlotID != nil {
			linkedSlots[*r.SlotID] = true
		}
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
