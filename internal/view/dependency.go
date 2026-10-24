package view

import "github.com/masanami/flywheel/internal/core"

// このファイルは課題の依存（M4.5 S2）のキーを持つ専用の型を置く。共有の Challenge には足さない
// （list・create・edit・遷移系の応答・needs_human の challenge の形を変えないため）。
// gentypes は埋め込みの構造体に対応しないので、Challenge のフィールドを重複して持つ
// （Challenge に足したら、ここの型とその変換にも足す。view_test.go が一致を検査する）。

// Dependency は show の challenge.dependencies の要素（依存先 1 つ）。
// UpstreamState・PolicyState は依存先に取り込みの対応が無ければ null。
type Dependency struct {
	ChallengeID   string  `json:"challenge_id"`
	PolicyState   *string `json:"policy_state"`
	Satisfied     bool    `json:"satisfied"`
	Status        string  `json:"status"`
	UpstreamState *string `json:"upstream_state"`
}

// UnmetDependency は status の actionable.challenges[].unmet_dependencies の要素（満たされていない依存先）。
type UnmetDependency struct {
	ChallengeID   string  `json:"challenge_id"`
	PolicyState   *string `json:"policy_state"`
	Status        string  `json:"status"`
	UpstreamState *string `json:"upstream_state"`
}

// ShowChallenge は show（UI の詳細の API を含む）の challenge。Challenge の各キーに dependencies・dependents を足した形。
type ShowChallenge struct {
	Context      *string      `json:"context"`
	CreatedAt    string       `json:"created_at"`
	Dependencies []Dependency `json:"dependencies"`
	Dependents   []string     `json:"dependents"`
	Description  string       `json:"description"`
	DoneCriteria string       `json:"done_criteria"`
	Headline     *string      `json:"headline"`
	ID           string       `json:"id"`
	Priority     *string      `json:"priority"`
	Reporter     string       `json:"reporter"`
	Status       string       `json:"status"`
	StatusLabel  string       `json:"status_label"`
	Title        string       `json:"title"`
	UpdatedAt    string       `json:"updated_at"`
	Urgency      *string      `json:"urgency"`
	Version      int          `json:"version"`
}

// ActionableChallenge は status の actionable.challenges[] の要素。Challenge の各キーに unmet_dependencies を足した形。
type ActionableChallenge struct {
	Context           *string           `json:"context"`
	CreatedAt         string            `json:"created_at"`
	Description       string            `json:"description"`
	DoneCriteria      string            `json:"done_criteria"`
	Headline          *string           `json:"headline"`
	ID                string            `json:"id"`
	Priority          *string           `json:"priority"`
	Reporter          string            `json:"reporter"`
	Status            string            `json:"status"`
	StatusLabel       string            `json:"status_label"`
	Title             string            `json:"title"`
	UnmetDependencies []UnmetDependency `json:"unmet_dependencies"`
	UpdatedAt         string            `json:"updated_at"`
	Urgency           *string           `json:"urgency"`
	Version           int               `json:"version"`
}

// FromDependencies は依存先の一覧を変換する（空でも `[]`）。
func FromDependencies(ds []core.DependencyView) []Dependency {
	out := make([]Dependency, 0, len(ds))
	for _, d := range ds {
		out = append(out, Dependency{ChallengeID: d.ChallengeID, PolicyState: d.PolicyState, Satisfied: d.Satisfied, Status: string(d.Status), UpstreamState: d.UpstreamState})
	}
	return out
}

// FromUnmetDependencies は満たされていない依存先の一覧を変換する（空でも `[]`）。
func FromUnmetDependencies(ds []core.DependencyView) []UnmetDependency {
	out := make([]UnmetDependency, 0, len(ds))
	for _, d := range ds {
		out = append(out, UnmetDependency{ChallengeID: d.ChallengeID, PolicyState: d.PolicyState, Status: string(d.Status), UpstreamState: d.UpstreamState})
	}
	return out
}

// FromShowChallenge は課題の詳細の challenge を ShowChallenge へ変換する。
func FromShowChallenge(d *core.ChallengeDetail) ShowChallenge {
	c := FromChallenge(d.Challenge)
	dependents := append([]string{}, d.Dependents...)
	return ShowChallenge{
		Context: c.Context, CreatedAt: c.CreatedAt, Dependencies: FromDependencies(d.Dependencies), Dependents: dependents,
		Description: c.Description, DoneCriteria: c.DoneCriteria, Headline: c.Headline, ID: c.ID, Priority: c.Priority,
		Reporter: c.Reporter, Status: c.Status, StatusLabel: c.StatusLabel, Title: c.Title, UpdatedAt: c.UpdatedAt,
		Urgency: c.Urgency, Version: c.Version,
	}
}

// FromActionableChallenges は status の actionable の課題を、満たされていない依存先つきで変換する。
func FromActionableChallenges(cs []core.Challenge, unmet map[string][]core.DependencyView) []ActionableChallenge {
	out := make([]ActionableChallenge, 0, len(cs))
	for _, ch := range cs {
		c := FromChallenge(ch)
		out = append(out, ActionableChallenge{
			Context: c.Context, CreatedAt: c.CreatedAt, Description: c.Description, DoneCriteria: c.DoneCriteria,
			Headline: c.Headline, ID: c.ID, Priority: c.Priority, Reporter: c.Reporter, Status: c.Status,
			StatusLabel: c.StatusLabel, Title: c.Title, UnmetDependencies: FromUnmetDependencies(unmet[ch.ID]),
			UpdatedAt: c.UpdatedAt, Urgency: c.Urgency, Version: c.Version,
		})
	}
	return out
}
