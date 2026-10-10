// Package view は CLI の `--json` と API が共有する JSON の形の型と、core の型からの変換を持つ。
//
// 構造体のフィールドは JSON のキーの辞書順に並べる（以前の map[string]any の出力と
// バイト単位で同じにするため。encoding/json は map のキーを辞書順で出力する）。
// import してよいのは internal/core だけ。
package view

import (
	"encoding/json"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

// FormatTimestamp は t を UTC・ミリ秒固定の RFC 3339 文字列にする。
func FormatTimestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func ts(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := FormatTimestamp(*t)
	return &s
}

// strOrNil は空文字を null にする。
func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Challenge は課題の形。
type Challenge struct {
	CreatedAt    string  `json:"created_at"`
	Description  string  `json:"description"`
	DoneCriteria string  `json:"done_criteria"`
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

// FromChallenge は core.Challenge を Challenge へ変換する。
func FromChallenge(c core.Challenge) Challenge {
	label, _ := c.Status.Label()
	out := Challenge{
		CreatedAt:    FormatTimestamp(c.CreatedAt),
		Description:  c.Description,
		DoneCriteria: c.DoneCriteria,
		ID:           c.ID,
		Reporter:     c.Reporter,
		Status:       string(c.Status),
		StatusLabel:  label,
		Title:        c.Title,
		UpdatedAt:    FormatTimestamp(c.UpdatedAt),
		Version:      c.Version,
	}
	if c.Urgency != nil {
		s := string(*c.Urgency)
		out.Urgency = &s
	}
	if c.Priority != nil {
		s := string(*c.Priority)
		out.Priority = &s
	}
	return out
}

// FromChallenges は課題の一覧を変換する（空でも `[]`）。
func FromChallenges(cs []core.Challenge) []Challenge {
	out := make([]Challenge, 0, len(cs))
	for _, c := range cs {
		out = append(out, FromChallenge(c))
	}
	return out
}

// Plan は単発の `plan` の成功出力の計画の形。
type Plan struct {
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	Version   int    `json:"version"`
}

// FromPlan は core.Plan を Plan へ変換する。
func FromPlan(p core.Plan) Plan {
	return Plan{Body: p.Body, CreatedAt: FormatTimestamp(p.CreatedAt), Version: p.Version}
}

// PlanDetail は show の plans の要素（Plan に J2 の構造化した出力 spec を足した形）。
type PlanDetail struct {
	Body      string          `json:"body"`
	CreatedAt string          `json:"created_at"`
	Spec      json.RawMessage `json:"spec"` // 無ければ null
	Version   int             `json:"version"`
}

// FromPlans は show の plans を変換する。
func FromPlans(ps []core.Plan) []PlanDetail {
	out := make([]PlanDetail, 0, len(ps))
	for _, p := range ps {
		d := PlanDetail{Body: p.Body, CreatedAt: FormatTimestamp(p.CreatedAt), Version: p.Version}
		if p.Spec != nil {
			d.Spec = json.RawMessage(*p.Spec)
		}
		out = append(out, d)
	}
	return out
}

// Approval は承認の形。
type Approval struct {
	Actor         string  `json:"actor"`
	Channel       string  `json:"channel"`
	DecidedAt     string  `json:"decided_at"`
	Decision      string  `json:"decision"`
	Kind          string  `json:"kind"`
	OperationID   *string `json:"operation_id"`
	Reason        *string `json:"reason"`
	TargetVersion int     `json:"target_version"`
	Verification  string  `json:"verification"`
}

// FromApproval は core.Approval を Approval へ変換する。
func FromApproval(a core.Approval) Approval {
	return Approval{
		Actor:         a.Actor,
		Channel:       a.Channel,
		DecidedAt:     FormatTimestamp(a.DecidedAt),
		Decision:      string(a.Decision),
		Kind:          string(a.Kind),
		OperationID:   a.OperationID,
		Reason:        a.Reason,
		TargetVersion: a.TargetVersion,
		Verification:  a.Verification,
	}
}

// FromApprovals は承認の一覧を変換する。
func FromApprovals(as []core.Approval) []Approval {
	out := make([]Approval, 0, len(as))
	for _, a := range as {
		out = append(out, FromApproval(a))
	}
	return out
}

// Hold は保留の形。
type Hold struct {
	Answer          *string `json:"answer"`
	AnsweredAt      *string `json:"answered_at"`
	AnsweredBy      *string `json:"answered_by"`
	FromStatus      string  `json:"from_status"`
	FromStatusLabel string  `json:"from_status_label"`
	Question        string  `json:"question"`
	RaisedAt        string  `json:"raised_at"`
}

// FromHold は core.Hold を Hold へ変換する。
func FromHold(h core.Hold) Hold {
	label, _ := h.FromStatus.Label()
	return Hold{
		Answer:          h.Answer,
		AnsweredAt:      ts(h.AnsweredAt),
		AnsweredBy:      h.AnsweredBy,
		FromStatus:      string(h.FromStatus),
		FromStatusLabel: label,
		Question:        h.Question,
		RaisedAt:        FormatTimestamp(h.RaisedAt),
	}
}

// FromHolds は保留の一覧を変換する。
func FromHolds(hs []core.Hold) []Hold {
	out := make([]Hold, 0, len(hs))
	for _, h := range hs {
		out = append(out, FromHold(h))
	}
	return out
}

// Operation は不可逆操作の形。
type Operation struct {
	ChallengeID string  `json:"challenge_id"`
	CreatedAt   string  `json:"created_at"`
	ID          string  `json:"id"`
	Kind        string  `json:"kind"`
	Ref         *string `json:"ref"`
	State       string  `json:"state"`
	Summary     string  `json:"summary"`
	Version     int     `json:"version"`
}

// FromOperation は core.IrreversibleOperation を Operation へ変換する。
func FromOperation(op core.IrreversibleOperation) Operation {
	return Operation{
		ChallengeID: op.ChallengeID,
		CreatedAt:   FormatTimestamp(op.CreatedAt),
		ID:          op.ID,
		Kind:        string(op.Kind),
		Ref:         op.Ref,
		State:       string(op.State),
		Summary:     op.Summary,
		Version:     op.Version,
	}
}

// FromOperations は不可逆操作の一覧を変換する。
func FromOperations(ops []core.IrreversibleOperation) []Operation {
	out := make([]Operation, 0, len(ops))
	for _, op := range ops {
		out = append(out, FromOperation(op))
	}
	return out
}

// SourceBinding は課題と取り込み元の対応の形。
type SourceBinding struct {
	CommentsCount         int     `json:"comments_count"`
	CreatedAt             string  `json:"created_at"`
	ExternalKey           string  `json:"external_key"`
	Fingerprint           string  `json:"fingerprint"`
	PolicyState           string  `json:"policy_state"`
	ReadCommentsCount     int     `json:"read_comments_count"`
	ReadUpstreamUpdatedAt *string `json:"read_upstream_updated_at"`
	SourceID              string  `json:"source_id"`
	UpdatedAt             string  `json:"updated_at"`
	UpstreamState         string  `json:"upstream_state"`
	UpstreamUpdatedAt     *string `json:"upstream_updated_at"`
	URL                   string  `json:"url"`
}

// FromSourceBinding は core.SourceBinding を変換する。対応が無い（nil）なら nil（JSON では null）。
func FromSourceBinding(b *core.SourceBinding) *SourceBinding {
	if b == nil {
		return nil
	}
	return &SourceBinding{
		CommentsCount:         b.CommentsCount,
		CreatedAt:             FormatTimestamp(b.CreatedAt),
		ExternalKey:           b.ExternalKey,
		Fingerprint:           b.Fingerprint,
		PolicyState:           b.PolicyState,
		ReadCommentsCount:     b.ReadCommentsCount,
		ReadUpstreamUpdatedAt: strOrNil(b.ReadUpstreamUpdatedAt),
		SourceID:              b.SourceID,
		UpdatedAt:             FormatTimestamp(b.UpdatedAt),
		UpstreamState:         b.UpstreamState,
		UpstreamUpdatedAt:     strOrNil(b.UpstreamUpdatedAt),
		URL:                   b.URL,
	}
}

// Run は run の形。
type Run struct {
	ChallengeID    *string  `json:"challenge_id"`
	CostSource     *string  `json:"cost_source"`
	CostUSD        *float64 `json:"cost_usd"`
	CycleBudgetUSD *float64 `json:"cycle_budget_usd"`
	CycleID        *string  `json:"cycle_id"`
	EndedAt        *string  `json:"ended_at"`
	ID             string   `json:"id"`
	Judgment       *string  `json:"judgment"`
	Kind           string   `json:"kind"`
	MaxBudgetUSD   float64  `json:"max_budget_usd"`
	RateLimited    bool     `json:"rate_limited"`
	Result         *string  `json:"result"`
	SessionID      *string  `json:"session_id"`
	StartedAt      string   `json:"started_at"`
}

// FromRun は core.Run を Run へ変換する。
func FromRun(r core.Run) Run {
	return Run{
		ChallengeID:    strOrNil(r.ChallengeID),
		CostSource:     strOrNil(string(r.CostSource)),
		CostUSD:        r.CostUSD,
		CycleBudgetUSD: r.CycleBudgetUSD,
		CycleID:        r.CycleID,
		EndedAt:        ts(r.EndedAt),
		ID:             r.ID,
		Judgment:       strOrNil(string(r.Judgment)),
		Kind:           string(r.Kind),
		MaxBudgetUSD:   r.MaxBudgetUSD,
		RateLimited:    r.RateLimited,
		Result:         strOrNil(string(r.Result)),
		SessionID:      strOrNil(r.SessionID),
		StartedAt:      FormatTimestamp(r.StartedAt),
	}
}

// FromRuns は run の一覧を変換する（空でも `[]`）。
func FromRuns(rs []core.Run) []Run {
	out := make([]Run, 0, len(rs))
	for _, r := range rs {
		out = append(out, FromRun(r))
	}
	return out
}

// Activity は作業ログの 1 件の形。
type Activity struct {
	Action       string          `json:"action"`
	Actor        string          `json:"actor"`
	After        json.RawMessage `json:"after"`
	At           string          `json:"at"`
	Before       json.RawMessage `json:"before"`
	Channel      string          `json:"channel"`
	Entity       string          `json:"entity"`
	EntityID     string          `json:"entity_id"`
	Verification string          `json:"verification"`
}

// FromActivities は作業ログを変換する（空でも `[]`）。
func FromActivities(as []core.Activity) []Activity {
	out := make([]Activity, 0, len(as))
	for _, a := range as {
		out = append(out, Activity{
			Action:       a.Action,
			Actor:        a.Actor,
			After:        a.After,
			At:           FormatTimestamp(a.At),
			Before:       a.Before,
			Channel:      a.Channel,
			Entity:       a.Entity,
			EntityID:     a.EntityID,
			Verification: a.Verification,
		})
	}
	return out
}

// 最上位のオブジェクト（コマンド・エンドポイントごとの成功の形）。

// ChallengeResponse は create・edit・遷移系の `{"challenge": …}`。
type ChallengeResponse struct {
	Challenge Challenge `json:"challenge"`
}

// ChallengeWithPlanResponse は `plan` の `{"challenge": …, "plan": …}`。
type ChallengeWithPlanResponse struct {
	Challenge Challenge `json:"challenge"`
	Plan      Plan      `json:"plan"`
}

// ChallengeApprovalResponse は課題の approve・reject の `{"approval": …, "challenge": …}`。
type ChallengeApprovalResponse struct {
	Approval  Approval  `json:"approval"`
	Challenge Challenge `json:"challenge"`
}

// ChallengeHoldResponse は answer の `{"challenge": …, "hold": …}`。
type ChallengeHoldResponse struct {
	Challenge Challenge `json:"challenge"`
	Hold      Hold      `json:"hold"`
}

// OperationResponse は op add の `{"operation": …}`。
type OperationResponse struct {
	Operation Operation `json:"operation"`
}

// OperationApprovalResponse は操作の approve・reject の `{"approval": …, "operation": …}`。
type OperationApprovalResponse struct {
	Approval  Approval  `json:"approval"`
	Operation Operation `json:"operation"`
}

// ListResponse は `list` の `{"challenges": […]}`。
type ListResponse struct {
	Challenges []Challenge `json:"challenges"`
}

// ShowResponse は `show` の成功の形。
type ShowResponse struct {
	Approvals     []Approval     `json:"approvals"`
	Challenge     Challenge      `json:"challenge"`
	Holds         []Hold         `json:"holds"`
	Operations    []Operation    `json:"operations"`
	Plans         []PlanDetail   `json:"plans"`
	Runs          []Run          `json:"runs"`
	SourceBinding *SourceBinding `json:"source_binding"`
}

// FromChallengeDetail は core.ChallengeDetail を ShowResponse へ変換する。
func FromChallengeDetail(d *core.ChallengeDetail) ShowResponse {
	return ShowResponse{
		Approvals:     FromApprovals(d.Approvals),
		Challenge:     FromChallenge(d.Challenge),
		Holds:         FromHolds(d.Holds),
		Operations:    FromOperations(d.Operations),
		Plans:         FromPlans(d.Plans),
		Runs:          FromRuns(d.Runs),
		SourceBinding: FromSourceBinding(d.SourceBinding),
	}
}

// LogResponse は `log` の `{"activities": […]}`。
type LogResponse struct {
	Activities []Activity `json:"activities"`
}

// RunsResponse は `runs` の `{"runs": […]}`。
type RunsResponse struct {
	Runs []Run `json:"runs"`
}

// Workspace は `GET /api/v1/workspaces` の 1 要素。Error は State が "ok" のとき null。
type Workspace struct {
	Error *string `json:"error"`
	Name  string  `json:"name"`
	Path  string  `json:"path"`
	State string  `json:"state"`
}

// WorkspaceList は `GET /api/v1/workspaces` の本文。
type WorkspaceList struct {
	Workspaces []Workspace `json:"workspaces"`
}
