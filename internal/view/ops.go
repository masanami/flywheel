package view

import "github.com/masanami/flywheel/internal/core"

// IngestItem は ingest の items の要素。対応の情報を積まない失敗の要素では
// challenge_id・upstream_state・policy_state・comments_count・upstream_updated_at が null。
type IngestItem struct {
	ChallengeID       *string  `json:"challenge_id"`
	CommentsCount     *int     `json:"comments_count"`
	Error             *string  `json:"error"`
	ExternalKey       string   `json:"external_key"`
	PolicyState       *string  `json:"policy_state"`
	Result            string   `json:"result"`
	Unread            []string `json:"unread"`
	UpstreamState     *string  `json:"upstream_state"`
	UpstreamUpdatedAt *string  `json:"upstream_updated_at"`
}

// IngestRepo は ingest の repos の要素。
type IngestRepo struct {
	Error    *string      `json:"error"`
	Excluded int          `json:"excluded"`
	Items    []IngestItem `json:"items"`
	Repo     string       `json:"repo"`
}

// IngestSource は ingest の sources の要素。
type IngestSource struct {
	ID                    string       `json:"id"`
	Repos                 []IngestRepo `json:"repos"`
	SelfAssigneesResolved bool         `json:"self_assignees_resolved"`
}

// IngestResponse は `ingest` の成功の形（cycle の取り込みの段の result も同じ）。
type IngestResponse struct {
	Sources []IngestSource `json:"sources"`
}

// FromIngestResult は core.IngestResult を IngestResponse へ変換する。
func FromIngestResult(res *core.IngestResult) IngestResponse {
	sources := make([]IngestSource, 0, len(res.Sources))
	for _, sr := range res.Sources {
		repos := make([]IngestRepo, 0, len(sr.Repos))
		for _, rr := range sr.Repos {
			items := make([]IngestItem, 0, len(rr.Items))
			for _, item := range rr.Items {
				// 対応の情報を積まない反映失敗（challenge_id が空）では comments_count も「不明」なので null。
				var commentsCount *int
				if item.ChallengeID != "" {
					c := item.CommentsCount
					commentsCount = &c
				}
				items = append(items, IngestItem{
					ChallengeID:       strOrNil(item.ChallengeID),
					CommentsCount:     commentsCount,
					Error:             item.Error,
					ExternalKey:       item.ExternalKey,
					PolicyState:       strOrNil(item.PolicyState),
					Result:            string(item.Result),
					Unread:            DiscrepancyKinds(item.Unread),
					UpstreamState:     strOrNil(item.UpstreamState),
					UpstreamUpdatedAt: strOrNil(item.UpstreamUpdatedAt),
				})
			}
			repos = append(repos, IngestRepo{Error: rr.Error, Excluded: rr.Excluded, Items: items, Repo: rr.Repo})
		}
		sources = append(sources, IngestSource{ID: sr.ID, Repos: repos, SelfAssigneesResolved: sr.SelfAssigneesResolved})
	}
	return IngestResponse{Sources: sources}
}

// PhaseItem は判断の段の items の要素。
type PhaseItem struct {
	ChallengeID string  `json:"challenge_id"`
	Outcome     *string `json:"outcome"`
	Result      string  `json:"result"`
	RunID       string  `json:"run_id"`
	Status      *string `json:"status"`
}

// PhaseNotStarted は判断の段の not_started の要素。
type PhaseNotStarted struct {
	ChallengeID string `json:"challenge_id"`
	Reason      string `json:"reason"`
}

// SerialGroup は委譲の段の serial_groups の要素。
type SerialGroup struct {
	Challenges        []string `json:"challenges"`
	PredictionHeadSHA *string  `json:"prediction_head_sha"`
	Reasons           []string `json:"reasons"`
	Repo              string   `json:"repo"`
}

// JudgmentPhase は分類・計画・検証の段の形（cycle の phases の要素と `--auto` の phase が共有する）。
type JudgmentPhase struct {
	Items      []PhaseItem       `json:"items"`
	NotStarted []PhaseNotStarted `json:"not_started"`
	Phase      string            `json:"phase"`
	Skipped    bool              `json:"skipped"`
}

// DelegationPhase は委譲の段（run）の形。JudgmentPhase に serial_groups を足した形。
type DelegationPhase struct {
	Items        []PhaseItem       `json:"items"`
	NotStarted   []PhaseNotStarted `json:"not_started"`
	Phase        string            `json:"phase"`
	SerialGroups []SerialGroup     `json:"serial_groups"`
	Skipped      bool              `json:"skipped"`
}

// IngestPhase は取り込みの段の形。skipped なら result は null。
type IngestPhase struct {
	Phase   string          `json:"phase"`
	Result  *IngestResponse `json:"result"`
	Skipped bool            `json:"skipped"`
}

// PhaseResponse は `--auto` の個別の操作と `run` の `{"phase": …}`。
type PhaseResponse struct {
	Phase any `json:"phase"`
}

func phaseItems(items []core.JudgmentAutoItem) []PhaseItem {
	out := make([]PhaseItem, 0, len(items))
	for _, it := range items {
		out = append(out, PhaseItem{
			ChallengeID: it.ChallengeID,
			Outcome:     strOrNil(it.Outcome),
			Result:      string(it.Result),
			RunID:       it.RunID,
			Status:      it.Status,
		})
	}
	return out
}

func phaseNotStarted(items []core.NotStarted) []PhaseNotStarted {
	out := make([]PhaseNotStarted, 0, len(items))
	for _, it := range items {
		out = append(out, PhaseNotStarted{ChallengeID: it.ChallengeID, Reason: string(it.Reason)})
	}
	return out
}

func serialGroups(groups []core.SerialGroup) []SerialGroup {
	out := make([]SerialGroup, 0, len(groups))
	for _, g := range groups {
		reasons := make([]string, 0, len(g.Reasons))
		for _, r := range g.Reasons {
			reasons = append(reasons, string(r))
		}
		out = append(out, SerialGroup{
			Challenges:        append([]string{}, g.Challenges...),
			PredictionHeadSHA: g.PredictionHeadSHA,
			Reasons:           reasons,
			Repo:              g.Repo,
		})
	}
	return out
}

// FromJudgmentPhase は判断の段の結果を変換する。phase は `classify | plan | verify` の値。
func FromJudgmentPhase(phase string, skipped bool, res *core.JudgmentAutoResult) JudgmentPhase {
	return JudgmentPhase{Items: phaseItems(res.Items), NotStarted: phaseNotStarted(res.NotStarted), Phase: phase, Skipped: skipped}
}

// FromDelegationPhase は委譲の段（run）の結果を変換する。
func FromDelegationPhase(skipped bool, res *core.JudgmentAutoResult) DelegationPhase {
	return DelegationPhase{
		Items:        phaseItems(res.Items),
		NotStarted:   phaseNotStarted(res.NotStarted),
		Phase:        "run",
		SerialGroups: serialGroups(res.SerialGroups),
		Skipped:      skipped,
	}
}

// FromCyclePhase は cycle の段 1 つを phases の要素へ変換する。
func FromCyclePhase(p core.CyclePhaseResult) any {
	if p.Phase == core.CyclePhaseIngest {
		out := IngestPhase{Phase: string(p.Phase), Skipped: p.Skipped}
		if p.Ingest != nil {
			r := FromIngestResult(p.Ingest)
			out.Result = &r
		}
		return out
	}
	res := &core.JudgmentAutoResult{Items: p.Items, NotStarted: p.NotStarted, SerialGroups: p.SerialGroups}
	if p.Phase == core.CyclePhaseRun {
		return FromDelegationPhase(p.Skipped, res)
	}
	return FromJudgmentPhase(string(p.Phase), p.Skipped, res)
}

// Cycle は cycle の周の記録。
type Cycle struct {
	BudgetUSD float64 `json:"budget_usd"`
	EndedAt   *string `json:"ended_at"`
	ID        string  `json:"id"`
	Result    string  `json:"result"`
	SpentUSD  float64 `json:"spent_usd"`
	StartedAt string  `json:"started_at"`
	Trigger   string  `json:"trigger"`
}

// CycleResponse は `cycle` の成功の形。
type CycleResponse struct {
	ConfigDefaultsUsed []string `json:"config_defaults_used"`
	Cycle              Cycle    `json:"cycle"`
	Phases             []any    `json:"phases"`
	RateLimited        bool     `json:"rate_limited"`
}

// FromCycleRunResult は core.CycleRunResult を CycleResponse へ変換する。
func FromCycleRunResult(res *core.CycleRunResult) CycleResponse {
	c := res.Cycle
	phases := make([]any, 0, len(res.Phases))
	for _, p := range res.Phases {
		phases = append(phases, FromCyclePhase(p))
	}
	return CycleResponse{
		ConfigDefaultsUsed: res.ConfigDefaultsUsed,
		Cycle: Cycle{
			BudgetUSD: c.BudgetUSD,
			EndedAt:   ts(c.EndedAt),
			ID:        c.ID,
			Result:    string(c.Result),
			SpentUSD:  c.SpentUSD,
			StartedAt: FormatTimestamp(c.StartedAt),
			Trigger:   c.Trigger,
		},
		Phases:      phases,
		RateLimited: res.RateLimited,
	}
}

// SlotCleared は slot clear の slot オブジェクト。
type SlotCleared struct {
	Path   string `json:"path"`
	Repo   string `json:"repo"`
	SlotID string `json:"slot_id"`
	State  string `json:"state"`
}

// SlotResponse は `slot clear` の成功の形。
type SlotResponse struct {
	Slot SlotCleared `json:"slot"`
}

// FromSlot は core.Slot を SlotResponse へ変換する。
func FromSlot(s *core.Slot) SlotResponse {
	return SlotResponse{Slot: SlotCleared{Path: s.Path, Repo: s.Repo, SlotID: s.ID, State: s.State}}
}

// InitResponse は `init` の成功の形。
type InitResponse struct {
	Created   bool   `json:"created"`
	StorePath string `json:"store_path"`
	Workspace string `json:"workspace"`
}

// FromInitResult は core.InitResult を InitResponse へ変換する。
func FromInitResult(r *core.InitResult) InitResponse {
	return InitResponse{Created: r.Created, StorePath: r.StorePath, Workspace: r.Workspace}
}

// MarkReadResponse は `mark-read` の成功の形。
type MarkReadResponse struct {
	ChallengeID   string         `json:"challenge_id"`
	Changed       bool           `json:"changed"`
	SourceBinding *SourceBinding `json:"source_binding"`
}

// FromMarkReadResult は core.MarkReadResult を変換する。
func FromMarkReadResult(r *core.MarkReadResult) MarkReadResponse {
	return MarkReadResponse{ChallengeID: r.ChallengeID, Changed: r.Changed, SourceBinding: FromSourceBinding(r.SourceBinding)}
}

// BudgetResponse は `budget` の成功の形。review_usd は変えていなければ null。
type BudgetResponse struct {
	Approval    Approval `json:"approval"`
	ChallengeID string   `json:"challenge_id"`
	ImplUSD     float64  `json:"impl_usd"`
	PlanVersion int      `json:"plan_version"`
	ReviewUSD   *float64 `json:"review_usd"`
}
