package view

import "github.com/masanami/flywheel/internal/core"

// Discrepancy は食い違いの形。
type Discrepancy struct {
	ChallengeID string   `json:"challenge_id"`
	Kinds       []string `json:"kinds"`
}

// Triage は status.needs_human.triage の要素。
type Triage struct {
	ChallengeID string `json:"challenge_id"`
	Reason      string `json:"reason"`
	RunID       string `json:"run_id"`
}

// BudgetExhausted は status.needs_human.budget_exhausted の要素。
type BudgetExhausted struct {
	ChallengeID        string  `json:"challenge_id"`
	ImplRemainingUSD   float64 `json:"impl_remaining_usd"`
	PlanVersion        int     `json:"plan_version"`
	ReviewRemainingUSD float64 `json:"review_remaining_usd"`
}

// WaitingExternal は status.waiting_external.challenges の要素。
type WaitingExternal struct {
	ChallengeID string `json:"challenge_id"`
	Checks      string `json:"checks"`
	PRURL       string `json:"pr_url"`
}

// Slot は status.needs_human.slots の要素。
type Slot struct {
	Path   string  `json:"path"`
	Repo   string  `json:"repo"`
	RunID  *string `json:"run_id"`
	SlotID string  `json:"slot_id"`
}

// DiscrepancyKinds は食い違いの種類を文字列の一覧へ変換する（空でも `[]`）。
func DiscrepancyKinds(kinds []core.DiscrepancyKind) []string {
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, string(k))
	}
	return out
}

// FromDiscrepancies は食い違いの一覧を変換する。
func FromDiscrepancies(ds []core.Discrepancy) []Discrepancy {
	out := make([]Discrepancy, 0, len(ds))
	for _, d := range ds {
		out = append(out, Discrepancy{ChallengeID: d.ChallengeID, Kinds: DiscrepancyKinds(d.Kinds)})
	}
	return out
}

// StatusNeedsHuman は status.needs_human。
type StatusNeedsHuman struct {
	BudgetExhausted []BudgetExhausted `json:"budget_exhausted"`
	Challenges      []Challenge       `json:"challenges"`
	Discrepancies   []Discrepancy     `json:"discrepancies"`
	Operations      []Operation       `json:"operations"`
	Slots           []Slot            `json:"slots"`
	Triage          []Triage          `json:"triage"`
}

// StatusActionable は status.actionable。
type StatusActionable struct {
	Challenges []Challenge `json:"challenges"`
}

// StatusApproved は status.approved。
type StatusApproved struct {
	Operations []Operation `json:"operations"`
}

// StatusWaitingExternal は status.waiting_external。
type StatusWaitingExternal struct {
	Challenges []WaitingExternal `json:"challenges"`
}

// StatusResponse は `status` の成功の形。
type StatusResponse struct {
	Actionable      StatusActionable      `json:"actionable"`
	Approved        StatusApproved        `json:"approved"`
	NeedsHuman      StatusNeedsHuman      `json:"needs_human"`
	WaitingExternal StatusWaitingExternal `json:"waiting_external"`
}

// FromOverview は core.Overview を StatusResponse へ変換する。
func FromOverview(ov *core.Overview) StatusResponse {
	triage := make([]Triage, 0, len(ov.NeedsHumanTriage))
	for _, it := range ov.NeedsHumanTriage {
		triage = append(triage, Triage{ChallengeID: it.ChallengeID, Reason: it.Reason, RunID: it.RunID})
	}
	budget := make([]BudgetExhausted, 0, len(ov.NeedsHumanBudgetExhausted))
	for _, it := range ov.NeedsHumanBudgetExhausted {
		budget = append(budget, BudgetExhausted{
			ChallengeID:        it.ChallengeID,
			ImplRemainingUSD:   it.ImplRemainingUSD,
			PlanVersion:        it.PlanVersion,
			ReviewRemainingUSD: it.ReviewRemainingUSD,
		})
	}
	slots := make([]Slot, 0, len(ov.NeedsHumanSlots))
	for _, it := range ov.NeedsHumanSlots {
		slots = append(slots, Slot{Path: it.Path, Repo: it.Repo, RunID: it.RunID, SlotID: it.SlotID})
	}
	waiting := make([]WaitingExternal, 0, len(ov.WaitingExternal))
	for _, it := range ov.WaitingExternal {
		waiting = append(waiting, WaitingExternal{ChallengeID: it.ChallengeID, Checks: "pending", PRURL: it.PRURL})
	}
	return StatusResponse{
		Actionable: StatusActionable{Challenges: FromChallenges(ov.ActionableChallenges)},
		Approved:   StatusApproved{Operations: FromOperations(ov.ApprovedOperations)},
		NeedsHuman: StatusNeedsHuman{
			BudgetExhausted: budget,
			Challenges:      FromChallenges(ov.NeedsHumanChallenges),
			Discrepancies:   FromDiscrepancies(ov.Discrepancies),
			Operations:      FromOperations(ov.NeedsHumanOperations),
			Slots:           slots,
			Triage:          triage,
		},
		WaitingExternal: StatusWaitingExternal{Challenges: waiting},
	}
}
