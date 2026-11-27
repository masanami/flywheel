package view

import "github.com/masanami/flywheel/internal/core"

// RequirementCreated は `requirement create --json` の成功の形。
type RequirementCreated struct {
	EvidencePath string `json:"evidence_path"`
	ID           string `json:"id"`
	Origin       string `json:"origin"`
	Title        string `json:"title"`
}

// FromCreatedRequirement は作った要求の記録を RequirementCreated へ変換する。
func FromCreatedRequirement(r core.Requirement) RequirementCreated {
	return RequirementCreated{EvidencePath: r.EvidencePath, ID: r.ID, Origin: string(r.Origin), Title: r.Title}
}

// Requirement は課題の詳細の requirement。chat_session は会話の識別（出どころが chat のとき。それ以外は null）。
type Requirement struct {
	// Challenges は要求の記録を指す課題（ID の昇順。取り下げた元の課題を含まない。無ければ []）。
	Challenges        []RequirementChallenge `json:"challenges"`
	ChatSession       *string                `json:"chat_session"`
	CreatedAt         string                 `json:"created_at"`
	EvidencePath      string                 `json:"evidence_path"`
	ExternalKey       *string                `json:"external_key"`
	ID                string                 `json:"id"`
	Origin            string                 `json:"origin"`
	OriginChallengeID *string                `json:"origin_challenge_id"`
	ResolvedAt        *string                `json:"resolved_at"`
	Title             string                 `json:"title"`
	URL               *string                `json:"url"`
}

// RequirementChallenge は requirement.challenges の要素。
type RequirementChallenge struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Title  string `json:"title"`
}

// FromRequirement は課題の詳細の要求の記録を変換する。nil なら nil。
func FromRequirement(r *core.RequirementDetail) *Requirement {
	if r == nil {
		return nil
	}
	challenges := make([]RequirementChallenge, 0, len(r.Challenges))
	for _, c := range r.Challenges {
		challenges = append(challenges, RequirementChallenge{ID: c.ID, Status: string(c.Status), Title: c.Title})
	}
	var resolvedAt *string
	if r.ResolvedAt != nil {
		v := FormatTimestamp(*r.ResolvedAt)
		resolvedAt = &v
	}
	return &Requirement{
		Challenges: challenges, ChatSession: r.ChatSession, CreatedAt: FormatTimestamp(r.CreatedAt), EvidencePath: r.EvidencePath,
		ExternalKey: r.ExternalKey, ID: r.ID, Origin: string(r.Origin), OriginChallengeID: r.OriginChallengeID,
		ResolvedAt: resolvedAt, Title: r.Title, URL: r.URL,
	}
}

// SliceSplit は課題の詳細の slice_split。reason は案の理由で、案が無ければ reason は null・slices は []。
type SliceSplit struct {
	EvidencePath *string `json:"evidence_path"`
	HoldID       *string `json:"hold_id"`
	Reason       *string `json:"reason"`
	Slices       []Slice `json:"slices"`
	State        string  `json:"state"`
}

// Slice は slice_split.slices の要素。depends_on は前に済ませる区切りの番号（1 始まり。無ければ []）。
type Slice struct {
	// ChallengeID は区切りから作った課題の ID。区切りを分けた後だけ出す。
	ChallengeID string `json:"challenge_id,omitempty"`
	Context     string `json:"context"`
	DependsOn   []int  `json:"depends_on"`
	Headline    string `json:"headline"`
	Repo        string `json:"repo"`
	Scope       string `json:"scope"`
	Summary     string `json:"summary"`
	Title       string `json:"title"`
}

// FromSliceSplit は区切りを分ける記録を変換する。nil なら nil。
func FromSliceSplit(s *core.SliceSplit) *SliceSplit {
	if s == nil {
		return nil
	}
	slices := make([]Slice, 0, len(s.Slices))
	for _, v := range s.Slices {
		slices = append(slices, Slice{
			ChallengeID: v.ChallengeID, Context: v.Context, DependsOn: append([]int{}, v.DependsOn...), Headline: v.Headline, Repo: v.Repo,
			Scope: v.Scope, Summary: v.Summary, Title: v.Title,
		})
	}
	return &SliceSplit{EvidencePath: s.EvidencePath, HoldID: s.HoldID, Reason: s.Reason, Slices: slices, State: string(s.State)}
}
