package view

import "github.com/masanami/flywheel/internal/core"

// このファイルは閲覧の API（docs/features/m4-ui-server.md §IF / API「閲覧の API」）が新しく
// 定めた応答の型を持つ。CLI の --json と同じ形の応答は view.go にある。

// Card は課題カードの面。Headline・Context は challenge の列と同じ値。SlotIDs は、スロットごとの直近の run がこの課題の run であるスロットの ID（API 専用。CLI の --json には出さない）。
type Card struct {
	Context          *string  `json:"context"`
	Headline         *string  `json:"headline"`
	Modifiers        []string `json:"modifiers"`
	NextHumanActions []string `json:"next_human_actions"`
	SlotIDs          []string `json:"slot_ids"`
	State            string   `json:"state"`
}

// FromCard は core.Card を Card へ変換する（空でも `[]`）。
func FromCard(c *core.Card) Card {
	mods := make([]string, 0, len(c.Modifiers))
	for _, m := range c.Modifiers {
		mods = append(mods, string(m))
	}
	acts := make([]string, 0, len(c.NextHumanActions))
	for _, a := range c.NextHumanActions {
		acts = append(acts, string(a))
	}
	return Card{Context: c.Context, Headline: c.Headline, Modifiers: mods, NextHumanActions: acts, SlotIDs: append([]string{}, c.SlotIDs...), State: string(c.State)}
}

// ChallengeCard は `GET …/challenges` の要素。Challenge の各キーに card を足した形。
type ChallengeCard struct {
	Card         Card    `json:"card"`
	Context      *string `json:"context"`
	CreatedAt    string  `json:"created_at"`
	Description  string  `json:"description"`
	DoneCriteria string  `json:"done_criteria"`
	Headline     *string `json:"headline"`
	ID           string  `json:"id"`
	Priority     *string `json:"priority"`
	Reporter     string  `json:"reporter"`
	Status       string  `json:"status"`
	StatusLabel  string  `json:"status_label"`
	Title        string  `json:"title"`
	UpdatedAt    string  `json:"updated_at"`
	Urgency      *string `json:"urgency"`
	Version      int     `json:"version"`
}

// FromChallengeCard は課題とそのカードを ChallengeCard へ変換する。
func FromChallengeCard(c core.Challenge, card *core.Card) ChallengeCard {
	v := FromChallenge(c)
	return ChallengeCard{
		Card: FromCard(card), Context: v.Context, CreatedAt: v.CreatedAt, Description: v.Description, DoneCriteria: v.DoneCriteria,
		Headline: v.Headline, ID: v.ID, Priority: v.Priority, Reporter: v.Reporter, Status: v.Status, StatusLabel: v.StatusLabel,
		Title: v.Title, UpdatedAt: v.UpdatedAt, Urgency: v.Urgency, Version: v.Version,
	}
}

// ListCardResponse は `GET …/challenges` の本文。
type ListCardResponse struct {
	Challenges []ChallengeCard `json:"challenges"`
}

// ShowCardResponse は `GET …/challenges/{id}` の本文。ShowResponse の最上位に card を足した形。
type ShowCardResponse struct {
	Approvals     []Approval     `json:"approvals"`
	Card          Card           `json:"card"`
	Challenge     Challenge      `json:"challenge"`
	Holds         []Hold         `json:"holds"`
	Operations    []Operation    `json:"operations"`
	Pairs         []StagePair    `json:"pairs"`
	Plans         []PlanDetail   `json:"plans"`
	Runs          []Run          `json:"runs"`
	SourceBinding *SourceBinding `json:"source_binding"`
	Stages        Stages         `json:"stages"`
}

// FromChallengeDetailCard は課題の全体像とそのカードを ShowCardResponse へ変換する。
func FromChallengeDetailCard(d *core.ChallengeDetail, card *core.Card) ShowCardResponse {
	s := FromChallengeDetail(d)
	return ShowCardResponse{
		Approvals: s.Approvals, Card: FromCard(card), Challenge: s.Challenge, Holds: s.Holds,
		Operations: s.Operations, Pairs: s.Pairs, Plans: s.Plans, Runs: s.Runs, SourceBinding: s.SourceBinding, Stages: s.Stages,
	}
}

// WorkspaceNeedsHuman は `GET /api/v1/needs-human` の 1 要素。
type WorkspaceNeedsHuman struct {
	Name       string           `json:"name"`
	NeedsHuman StatusNeedsHuman `json:"needs_human"`
}

// NeedsHumanResponse は `GET /api/v1/needs-human` の本文。
// UnreadableWorkspaces は state が ok と判定されたが、借りる・読むのに失敗して除いたワークスペースの名前。
type NeedsHumanResponse struct {
	UnreadableWorkspaces []string              `json:"unreadable_workspaces"`
	Workspaces           []WorkspaceNeedsHuman `json:"workspaces"`
}

// FromNeedsHuman は status の needs_human の部分だけを取り出す。
func FromNeedsHuman(ov *core.Overview) StatusNeedsHuman {
	return FromOverview(ov).NeedsHuman
}

// Question は `GET /api/v1/questions` の 1 要素（未回答の保留）。
type Question struct {
	ChallengeID string  `json:"challenge_id"`
	FromStatus  string  `json:"from_status"`
	Question    string  `json:"question"`
	RaisedAt    string  `json:"raised_at"`
	RunID       *string `json:"run_id"`
	Title       string  `json:"title"`
	Workspace   string  `json:"workspace"`
}

// QuestionsResponse は `GET /api/v1/questions` の本文。
// UnreadableWorkspaces は state が ok と判定されたが、借りる・読むのに失敗して除いたワークスペースの名前。
type QuestionsResponse struct {
	Questions            []Question `json:"questions"`
	UnreadableWorkspaces []string   `json:"unreadable_workspaces"`
}

// FromUnansweredHold は未回答の保留を、ワークスペース名つきの Question へ変換する。
func FromUnansweredHold(workspace string, h core.UnansweredHold) Question {
	return Question{
		ChallengeID: h.ChallengeID, FromStatus: string(h.FromStatus), Question: h.Question,
		RaisedAt: FormatTimestamp(h.RaisedAt), RunID: h.RunID, Title: h.Title, Workspace: workspace,
	}
}

// RunReport は run の報告のうち人間が読む 2 つ。
type RunReport struct {
	Assumptions []string `json:"assumptions"`
	RunID       string   `json:"run_id"`
	Unverified  []string `json:"unverified"`
}

// RunReportResponse は `GET …/runs/{id}/report` の本文。
type RunReportResponse struct {
	Report RunReport `json:"report"`
}

// FromRunReport は core.RunReport を RunReportResponse へ変換する。
func FromRunReport(r *core.RunReport) RunReportResponse {
	a, u := r.Assumptions, r.Unverified
	if a == nil {
		a = []string{}
	}
	if u == nil {
		u = []string{}
	}
	return RunReportResponse{Report: RunReport{Assumptions: a, RunID: r.RunID, Unverified: u}}
}
