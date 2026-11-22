package view

import (
	"encoding/json"
	"fmt"

	"github.com/masanami/flywheel/internal/core"
)

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

// IngestedItem は `ingest <指定>...` の ingested の要素（選んで課題にした Issue 1 件）。
type IngestedItem struct {
	ChallengeID string `json:"challenge_id"`
	Children    int    `json:"children"`
	ExternalKey string `json:"external_key"`
}

// IngestedResponse は `ingest <指定>...` の成功の形。
type IngestedResponse struct {
	Ingested []IngestedItem `json:"ingested"`
}

// FromIngestSelectedResult は core.IngestSelectedResult を IngestedResponse へ変換する。
func FromIngestSelectedResult(res *core.IngestSelectedResult) IngestedResponse {
	items := make([]IngestedItem, 0, len(res.Ingested))
	for _, c := range res.Ingested {
		items = append(items, IngestedItem{ChallengeID: c.ChallengeID, Children: c.Children, ExternalKey: c.ExternalKey})
	}
	return IngestedResponse{Ingested: items}
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

// PhaseNotStarted は判断の段の not_started の要素。detail は空文字のとき持たない。
// challenge_id は improve の段の failure_limit だけが持たない（それ以外の段は常に課題を持つ）。
type PhaseNotStarted struct {
	ChallengeID string `json:"challenge_id,omitempty"`
	Detail      string `json:"detail,omitempty"`
	Reason      string `json:"reason"`
}

// ReleaseNotStarted は release の段と `op exec` の not_started の要素。operation_id を常に持つ。
type ReleaseNotStarted struct {
	ChallengeID string `json:"challenge_id"`
	Detail      string `json:"detail,omitempty"`
	OperationID string `json:"operation_id"`
	Reason      string `json:"reason"`
}

// ReleaseItem は release の段と `op exec` の items の要素。head とマージのコミットは無ければ null。
type ReleaseItem struct {
	ApprovedHead        *string `json:"approved_head"`
	ChallengeID         string  `json:"challenge_id"`
	ConsecutiveFailures int     `json:"consecutive_failures"`
	MergeCommit         *string `json:"merge_commit"`
	ObservedHead        *string `json:"observed_head"`
	OperationID         string  `json:"operation_id"`
	Result              string  `json:"result"`
	// Route は反映の依頼の経路（waiting_run | reflect_request | outside）。対応する契約の実行が無い・対応しない
	// 実行を待つときは null。
	Route *string `json:"route"`
}

// ReleaseTidied は release の段の、承認待ちの release の片付け 1 件。action は closed（PR がマージ済み、またはマージされずに閉じられていて closed にした。
// closed_reason は merged_outside | merged_unapproved | pr_closed）か reopened（検証の後に中身が変わった PR の release を置き換え、
// 完了の課題を検証中へ戻した。reopen_reason は content_changed、new_operation_id は新しい release）か needs_human（取り込みだけとも
// 中身の変更とも確かめられず、人間待ちにした）か skipped（検証中へ戻す前の読みで戻せず、飛ばした）。needs_human・skipped は wait_reason を持つ。
type ReleaseTidied struct {
	Action string `json:"action"`
	// CancelNote は action が closed のとき、対応する契約の実行を取り消せなかった理由（取り消せたか不要なら出さない）。
	CancelNote     *string `json:"cancel_note,omitempty"`
	ChallengeID    string  `json:"challenge_id"`
	ClosedReason   *string `json:"closed_reason,omitempty"`
	NewOperationID *string `json:"new_operation_id,omitempty"`
	OperationID    string  `json:"operation_id"`
	ReopenReason   *string `json:"reopen_reason,omitempty"`
	// WaitReason は action が needs_human（検証の後に動いた head が取り込みだけとも中身の変更とも確かめられず、人間待ちにした。
	// 人が承認するか検証中へ戻すかを選ぶ）か skipped（完了の課題を検証中へ戻す前の、タスクの契約の実行の読みで戻せず、飛ばした）のときの、理由。
	WaitReason *string `json:"wait_reason,omitempty"`
}

// ReleasePhase は release の段（cycle の phases の要素と `op exec` の phase が共有する）の形。
type ReleasePhase struct {
	Items      []ReleaseItem       `json:"items"`
	NotStarted []ReleaseNotStarted `json:"not_started"`
	Phase      string              `json:"phase"`
	Skipped    bool                `json:"skipped"`
	Tidied     []ReleaseTidied     `json:"tidied"`
}

// JudgmentPhase は分類・計画・検証の段の形（cycle の phases の要素と `--auto` の phase が共有する）。
type JudgmentPhase struct {
	Items      []PhaseItem       `json:"items"`
	NotStarted []PhaseNotStarted `json:"not_started"`
	Phase      string            `json:"phase"`
	Skipped    bool              `json:"skipped"`
}

// DelegationPhase は委譲の段（run）の形。
type DelegationPhase struct {
	Items      []PhaseItem       `json:"items"`
	NotStarted []PhaseNotStarted `json:"not_started"`
	Phase      string            `json:"phase"`
	Skipped    bool              `json:"skipped"`
}

// ImproveItem は improve の段と `improve` の items の要素。
type ImproveItem struct {
	Created []string `json:"created"`
	Linked  []string `json:"linked"`
	Result  string   `json:"result"`
	RunID   string   `json:"run_id"`
}

// ImprovePhase は improve の段の形（cycle の phases の要素と `improve` の phase が共有する）。
type ImprovePhase struct {
	Items      []ImproveItem     `json:"items"`
	NotStarted []PhaseNotStarted `json:"not_started"`
	Phase      string            `json:"phase"`
	Skipped    bool              `json:"skipped"`
}

// BackupResult は backup の段の result（outcome は committed|unchanged|not_git|ignored|failed）。
type BackupResult struct {
	Commit  *string `json:"commit"`
	Detail  *string `json:"detail"`
	Outcome string  `json:"outcome"`
}

// BackupPhase は backup の段の形（cycle の phases の最後の要素）。
type BackupPhase struct {
	Phase   string       `json:"phase"`
	Result  BackupResult `json:"result"`
	Skipped bool         `json:"skipped"`
}

// FromBackupPhase は backup の段の結果を変換する。
func FromBackupPhase(r core.BackupResult) BackupPhase {
	return BackupPhase{
		Phase:   string(core.CyclePhaseBackup),
		Result:  BackupResult{Commit: strOrNil(r.Commit), Detail: strOrNil(r.Detail), Outcome: string(r.Outcome)},
		Skipped: false,
	}
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
		out = append(out, PhaseNotStarted{ChallengeID: it.ChallengeID, Detail: it.Detail, Reason: string(it.Reason)})
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
		Items:      phaseItems(res.Items),
		NotStarted: phaseNotStarted(res.NotStarted),
		Phase:      "run",
		Skipped:    skipped,
	}
}

// FromImprovePhase は improve の段の結果を変換する。
func FromImprovePhase(items []core.ImproveItem, notStarted []core.NotStarted) ImprovePhase {
	out := make([]ImproveItem, 0, len(items))
	for _, it := range items {
		out = append(out, ImproveItem{
			Created: append([]string{}, it.Created...),
			Linked:  append([]string{}, it.Linked...),
			Result:  string(it.Result),
			RunID:   it.RunID,
		})
	}
	return ImprovePhase{Items: out, NotStarted: phaseNotStarted(notStarted), Phase: string(core.CyclePhaseImprove), Skipped: false}
}

// FromReleasePhase は release の段の結果を変換する。
func FromReleasePhase(skipped bool, items []core.ReleaseExecItem, notStarted []core.NotStarted, tidied []core.ReleaseTidyItem) ReleasePhase {
	outItems := make([]ReleaseItem, 0, len(items))
	for _, it := range items {
		ri := ReleaseItem{
			ApprovedHead:        strOrNil(it.ApprovedHead),
			ChallengeID:         it.ChallengeID,
			ConsecutiveFailures: it.ConsecutiveFailures,
			MergeCommit:         strOrNil(it.MergeCommit),
			ObservedHead:        strOrNil(it.ObservedHead),
			OperationID:         it.OperationID,
			Result:              string(it.Result),
			Route:               strOrNil(it.Route),
		}
		outItems = append(outItems, ri)
	}
	outTidied := make([]ReleaseTidied, 0, len(tidied))
	for _, t := range tidied {
		outTidied = append(outTidied, ReleaseTidied{
			Action:         string(t.Action),
			ChallengeID:    t.ChallengeID,
			ClosedReason:   strOrNil(string(t.ClosedReason)),
			NewOperationID: strOrNil(t.NewOperationID),
			OperationID:    t.OperationID,
			ReopenReason:   strOrNil(string(t.ReopenReason)),
			WaitReason:     strOrNil(t.WaitReason),
			CancelNote:     strOrNil(t.CancelNote),
		})
	}
	outNS := make([]ReleaseNotStarted, 0, len(notStarted))
	for _, it := range notStarted {
		outNS = append(outNS, ReleaseNotStarted{ChallengeID: it.ChallengeID, Detail: it.Detail, OperationID: it.OperationID, Reason: string(it.Reason)})
	}
	return ReleasePhase{Items: outItems, NotStarted: outNS, Phase: string(core.CyclePhaseRelease), Skipped: skipped, Tidied: outTidied}
}

// FromCyclePhase は cycle の段 1 つを phases の要素へ変換する。
func FromCyclePhase(p core.CyclePhaseResult) any {
	if p.Phase == core.CyclePhaseRelease {
		return FromReleasePhase(p.Skipped, p.ReleaseItems, p.NotStarted, p.ReleaseTidied)
	}
	if p.Phase == core.CyclePhaseBackup && p.Backup != nil {
		return FromBackupPhase(*p.Backup)
	}
	if p.Phase == core.CyclePhaseImprove {
		return FromImprovePhase(p.ImproveItems, p.NotStarted)
	}
	if p.Phase == core.CyclePhaseIngest {
		out := IngestPhase{Phase: string(p.Phase), Skipped: p.Skipped}
		if p.Ingest != nil {
			r := FromIngestResult(p.Ingest)
			out.Result = &r
		}
		return out
	}
	res := &core.JudgmentAutoResult{Items: p.Items, NotStarted: p.NotStarted}
	if p.Phase == core.CyclePhaseRun {
		return FromDelegationPhase(p.Skipped, res)
	}
	return FromJudgmentPhase(string(p.Phase), p.Skipped, res)
}

// Cycle は cycle の周の記録。
type Cycle struct {
	BudgetUSD float64 `json:"budget_usd"`
	// EndReason は終わった理由（`settled`・`budget`・`stopped`・`rate_limited`・`stalled`・`error`・`interrupted`）。理由を持たない周は null。
	EndReason *string `json:"end_reason"`
	EndedAt   *string `json:"ended_at"`
	ID        string  `json:"id"`
	Result    string  `json:"result"`
	// Rounds は通した回りの数。
	Rounds    int     `json:"rounds"`
	SpentUSD  float64 `json:"spent_usd"`
	StartedAt string  `json:"started_at"`
	Trigger   string  `json:"trigger"`
}

// CycleResponse は `cycle` の成功の形。
// 周を始めなかったとき（進められるものが無い）は、started が false・not_started_reason が理由・cycle が null・phases が空。
type CycleResponse struct {
	ConfigDefaultsUsed []string `json:"config_defaults_used"`
	Cycle              *Cycle   `json:"cycle"`
	NotStartedReason   *string  `json:"not_started_reason"`
	Phases             []any    `json:"phases"`
	RateLimited        bool     `json:"rate_limited"`
	Started            bool     `json:"started"`
}

// FromCycleNotStarted は、周を始めなかった（サイクルを回すと進むものが無い）ときの CycleResponse。
func FromCycleNotStarted(defaultsUsed []string, reason string) CycleResponse {
	if defaultsUsed == nil {
		defaultsUsed = []string{}
	}
	return CycleResponse{
		ConfigDefaultsUsed: defaultsUsed,
		NotStartedReason:   &reason,
		Phases:             []any{},
	}
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
		Started:            true,
		Cycle: &Cycle{
			BudgetUSD: c.BudgetUSD,
			EndReason: endReasonPtr(c.EndReason),
			EndedAt:   ts(c.EndedAt),
			ID:        c.ID,
			Result:    string(c.Result),
			Rounds:    c.Rounds,
			SpentUSD:  c.SpentUSD,
			StartedAt: FormatTimestamp(c.StartedAt),
			Trigger:   c.Trigger,
		},
		Phases:      phases,
		RateLimited: res.RateLimited,
	}
}

// endReasonPtr は終わった理由を JSON の値へ写す（空は null）。
func endReasonPtr(r core.CycleEndReason) *string {
	if r == "" {
		return nil
	}
	v := string(r)
	return &v
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

// CycleStarted は `POST /api/v1/workspaces/{name}/cycle` の成功の形。
// started が false のときは、サイクルを回すと進むものが無く、周を始めなかった。
type CycleStarted struct {
	Started bool `json:"started"`
}

// CycleStart は、操作（承認・回答など）が成立した後にサイクルを始めたかの結果（操作の出力のキー `cycle`）。
// reason は started・already_running・no_work・disabled・unavailable（CYCLE_START_REASON_CODES）。
type CycleStart struct {
	Reason  string `json:"reason"`
	Started bool   `json:"started"`
}

// FromCycleStartReason は理由から CycleStart を作る（started は理由が started のときだけ真）。
func FromCycleStartReason(r core.CycleStartReason) CycleStart {
	return CycleStart{Reason: string(r), Started: r == core.CycleStartStarted}
}

// WithCycle は、操作の出力（最上位がオブジェクトの Body）に、キー `cycle` を足した形。
type WithCycle struct {
	Body  any
	Cycle CycleStart
}

// MarshalJSON は Body のオブジェクトの末尾に "cycle" を足す（Body が空のオブジェクトなら cycle だけ）。
func (w WithCycle) MarshalJSON() ([]byte, error) {
	body, err := json.Marshal(w.Body)
	if err != nil {
		return nil, err
	}
	if len(body) < 2 || body[0] != '{' || body[len(body)-1] != '}' {
		return nil, fmt.Errorf("view: WithCycle の Body はオブジェクトでなければならない: %T", w.Body)
	}
	cyc, err := json.Marshal(w.Cycle)
	if err != nil {
		return nil, err
	}
	out := append([]byte{}, body[:len(body)-1]...)
	if len(body) > 2 {
		out = append(out, ',')
	}
	out = append(out, `"cycle":`...)
	out = append(out, cyc...)
	return append(out, '}'), nil
}

// CycleWatching は running.watching[] の要素（サイクルが見ている、終わっていない実行 1 件）。
// state は working・verifying・waiting_owner。task_id・title は、タスクに結び付かない実行では課題の値。
type CycleWatching struct {
	ChallengeID  string  `json:"challenge_id"`
	RunStartedAt string  `json:"run_started_at"`
	SpentUSD     float64 `json:"spent_usd"`
	State        string  `json:"state"`
	TaskID       *string `json:"task_id"`
	Title        string  `json:"title"`
}

// CycleRunning は走っているサイクルの様子（`running`）。
type CycleRunning struct {
	ActiveRuns      int             `json:"active_runs"`
	BudgetUSD       float64         `json:"budget_usd"`
	MaxParallelRuns int             `json:"max_parallel_runs"`
	Rounds          int             `json:"rounds"`
	SpentUSD        float64         `json:"spent_usd"`
	StartedAt       string          `json:"started_at"`
	StopRequested   bool            `json:"stop_requested"`
	Trigger         string          `json:"trigger"`
	Watching        []CycleWatching `json:"watching"`
}

// CycleStatus は `GET /api/v1/workspaces/{name}/cycle` と `flywheel cycle status --json` の形。
// state は idle・running・stale。running は走っているサイクルの様子（走っていなければ null）、
// last_end_reason は最後に終わったサイクルの終わった理由（無ければ null）。
type CycleStatus struct {
	LastEndReason *string       `json:"last_end_reason"`
	LastEndedAt   *string       `json:"last_ended_at"`
	LastResult    *string       `json:"last_result"`
	LastStartedAt *string       `json:"last_started_at"`
	Running       *CycleRunning `json:"running"`
	State         string        `json:"state"`
}

// FromCycleStatus は core.CycleStatus を CycleStatus へ変換する。
func FromCycleStatus(s *core.CycleStatus) CycleStatus {
	out := CycleStatus{State: string(s.State), LastStartedAt: ts(s.LastStartedAt), LastEndedAt: ts(s.LastEndedAt)}
	if s.LastResult != nil {
		r := string(*s.LastResult)
		out.LastResult = &r
	}
	if s.LastEndReason != nil {
		r := string(*s.LastEndReason)
		out.LastEndReason = &r
	}
	if r := s.Running; r != nil {
		run := &CycleRunning{
			ActiveRuns: r.ActiveRuns, BudgetUSD: r.BudgetUSD, MaxParallelRuns: r.MaxParallelRuns, Rounds: r.Rounds,
			SpentUSD: r.SpentUSD, StartedAt: FormatTimestamp(r.StartedAt), StopRequested: r.StopRequested, Trigger: r.Trigger,
			Watching: []CycleWatching{},
		}
		for _, w := range r.Watching {
			run.Watching = append(run.Watching, CycleWatching{
				ChallengeID: w.ChallengeID, RunStartedAt: FormatTimestamp(w.RunStartedAt), SpentUSD: w.SpentUSD,
				State: string(w.State), TaskID: strOrNil(w.TaskID), Title: w.Title,
			})
		}
		out.Running = run
	}
	return out
}

// CycleStop は `flywheel cycle stop --json` の形。requested は止める指示を書いたか（走っているサイクルが無ければ false）。
type CycleStop struct {
	CycleID   *string `json:"cycle_id"`
	Requested bool    `json:"requested"`
}

// FromCycleStopResult は core.CycleStopResult を CycleStop へ変換する。
func FromCycleStopResult(r *core.CycleStopResult) CycleStop {
	return CycleStop{CycleID: strOrNil(r.CycleID), Requested: r.Requested}
}

// ExportBody は `export` の成功の形の中身（書き出したファイルの絶対パスとスキーマの版）。
type ExportBody struct {
	Path          string `json:"path"`
	SchemaVersion int    `json:"schema_version"`
}

// ExportResponse は `export` の成功の形。
type ExportResponse struct {
	Export ExportBody `json:"export"`
}

// FromExportResult は core.ExportResult を ExportResponse へ変換する。
func FromExportResult(r *core.ExportResult) ExportResponse {
	return ExportResponse{Export: ExportBody{Path: r.Path, SchemaVersion: r.SchemaVersion}}
}

// ImportBody は `import` の成功の形の中身（読み込んだファイルの絶対パスとスキーマの版）。
type ImportBody struct {
	Path          string `json:"path"`
	SchemaVersion int    `json:"schema_version"`
}

// ImportResponse は `import` の成功の形。
type ImportResponse struct {
	Import ImportBody `json:"import"`
}

// FromImportResult は core.ImportResult を ImportResponse へ変換する。
func FromImportResult(r *core.ImportResult) ImportResponse {
	return ImportResponse{Import: ImportBody{Path: r.Path, SchemaVersion: r.SchemaVersion}}
}
