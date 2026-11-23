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

// FromChallenge は core.Challenge を Challenge へ変換する。
func FromChallenge(c core.Challenge) Challenge {
	label, _ := c.Status.Label()
	out := Challenge{
		Context:      c.Context,
		CreatedAt:    FormatTimestamp(c.CreatedAt),
		Description:  c.Description,
		DoneCriteria: c.DoneCriteria,
		Headline:     c.Headline,
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
	Actor     string `json:"actor"`
	Channel   string `json:"channel"`
	DecidedAt string `json:"decided_at"`
	Decision  string `json:"decision"`
	// ID は承認の行の ID（"AP-<n>"）。
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// MergeInVerifiedHead は release を「検証の head から既定ブランチを取り込んだだけ」と判定して承認したときの検証の head。
	MergeInVerifiedHead *string  `json:"merge_in_verified_head"`
	OperationID         *string  `json:"operation_id"`
	Reason              *string  `json:"reason"`
	RejectedFields      []string `json:"rejected_fields" ts:"nullable"`
	RelayQuote          *string  `json:"relay_quote"`
	RelaySession        *string  `json:"relay_session"`
	// SupersededAt・SupersededReason・SupersededBy は、完了の承認が本番反映のやり直しで置き換えられた日時・理由
	// （not_mergeable・limit_reached・content_changed）・置き換えた次の完了の承認の ID。有効な承認は null。
	SupersededAt     *string `json:"superseded_at"`
	SupersededBy     *string `json:"superseded_by"`
	SupersededReason *string `json:"superseded_reason"`
	TargetVersion    int     `json:"target_version"`
	Verification     string  `json:"verification"`
}

// FromApproval は core.Approval を Approval へ変換する。
func FromApproval(a core.Approval) Approval {
	return Approval{
		Actor:               a.Actor,
		Channel:             a.Channel,
		DecidedAt:           FormatTimestamp(a.DecidedAt),
		Decision:            string(a.Decision),
		ID:                  a.ID,
		Kind:                string(a.Kind),
		MergeInVerifiedHead: a.MergeInVerifiedHead,
		OperationID:         a.OperationID,
		Reason:              a.Reason,
		RejectedFields:      nilIfEmpty(a.RejectedFields),
		RelayQuote:          a.RelayQuote,
		RelaySession:        a.RelaySession,
		SupersededAt:        optionalTimestamp(a.SupersededAt),
		SupersededBy:        a.SupersededBy,
		SupersededReason:    a.SupersededReason,
		TargetVersion:       a.TargetVersion,
		Verification:        a.Verification,
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

// HoldOption は保留の選択肢 1 つの形。説明が無い選択肢の description は空文字。
type HoldOption struct {
	Description string `json:"description"`
	ID          string `json:"id"`
	Label       string `json:"label"`
}

// FromHoldOptions は保留の選択肢の並びを変換する。選択肢が無い（nil）ときは、null ではなく空の配列にする。
func FromHoldOptions(opts []core.HoldOption) []HoldOption {
	out := make([]HoldOption, 0, len(opts))
	for _, o := range opts {
		out = append(out, HoldOption{Description: o.Description, ID: o.ID, Label: o.Label})
	}
	return out
}

// Hold は保留の形。
type Hold struct {
	Answer *string `json:"answer"`
	// AnswerOption は回答で選んだ選択肢の識別。選択肢で答えていない保留は null。
	AnswerOption         *string `json:"answer_option"`
	AnsweredAt           *string `json:"answered_at"`
	AnsweredBy           *string `json:"answered_by"`
	AnsweredChannel      *string `json:"answered_channel"`
	AnsweredVerification *string `json:"answered_verification"`
	ClosedAt             *string `json:"closed_at"`
	FromStatus           string  `json:"from_status"`
	FromStatusLabel      string  `json:"from_status_label"`
	// HoldID は保留の表示用の ID（"H-<番号>"）。閉じた保留・過去の保留も同じ形で出る。
	HoldID string `json:"hold_id"`
	// Kind は保留の種類（human＝人の対応を待つ・parent_direct＝親が直接行う）。
	Kind string `json:"kind"`
	// Options は保留の選択肢の並び。選択肢が無い保留は空の配列。
	Options      []HoldOption `json:"options"`
	Question     string       `json:"question"`
	RaisedAt     string       `json:"raised_at"`
	RelayQuote   *string      `json:"relay_quote"`
	RelaySession *string      `json:"relay_session"`
	// Replan は回答に付いた「範囲が変わる」印。真なら、その回答で課題は計画の作り直しへ戻った。
	Replan bool    `json:"replan"`
	RunID  *string `json:"run_id"`
	// Scope は保留の止める範囲。タスクを止める保留は task、課題を止める保留は challenge。
	Scope string `json:"scope"`
	// StopKind は保留を作る呼び出し元が渡した止まり方（STOP_KIND_CODES の値）。渡されなかった保留と過去の行は null。
	StopKind *string `json:"stop_kind"`
	// SupervisorFindings・SupervisorRecommendation は、スーパーバイザーがオーナーへ上げたときに残した、調べたことと推奨。無ければ null。
	SupervisorFindings       *string `json:"supervisor_findings"`
	SupervisorRecommendation *string `json:"supervisor_recommendation"`
	// SupervisorState はスーパーバイザーの処理の状態（SUPERVISOR_STATE_CODES の値）。対象にしない保留と過去の行は null。
	SupervisorState *string `json:"supervisor_state"`
	// TaskID は保留が結び付くタスクの ID（"T-<n>"）。課題の単位の保留と過去の行は null。
	TaskID *string `json:"task_id"`
}

// FromHold は core.Hold を Hold へ変換する。
func FromHold(h core.Hold) Hold {
	label, _ := h.FromStatus.Label()
	return Hold{
		Answer:               h.Answer,
		AnswerOption:         h.AnswerOption,
		AnsweredAt:           ts(h.AnsweredAt),
		AnsweredBy:           h.AnsweredBy,
		AnsweredChannel:      h.AnsweredChannel,
		AnsweredVerification: h.AnsweredVerification,
		ClosedAt:             ts(h.ClosedAt),
		FromStatus:           string(h.FromStatus),
		FromStatusLabel:      label,
		HoldID:               h.HoldID,
		Kind:                 string(h.Kind),
		Options:              FromHoldOptions(h.Options),
		Question:             h.Question,
		RaisedAt:             FormatTimestamp(h.RaisedAt),
		RelayQuote:           h.RelayQuote,
		RelaySession:         h.RelaySession,
		Replan:               h.Replan,
		RunID:                h.RunID,
		Scope:                h.ScopeLabel(),
		TaskID:               h.TaskID,

		StopKind:                 strOrNil(string(h.StopKind)),
		SupervisorFindings:       strOrNil(h.SupervisorFindings),
		SupervisorRecommendation: strOrNil(h.SupervisorRecommendation),
		SupervisorState:          strOrNil(string(h.SupervisorState)),
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

// DirectCompletion は親が直接行った完了の記録の形（成果物の参照は run を介さない）。
type DirectCompletion struct {
	// Artifacts は成果物の参照（URL・PR・Issue コメントなど）。1 件以上。
	Artifacts []string `json:"artifacts"`
	CreatedAt string   `json:"created_at"`
	// Note は短い説明。無ければ null。
	Note *string `json:"note"`
}

// FromDirectCompletion は core.DirectCompletion を DirectCompletion へ変換する。
func FromDirectCompletion(d core.DirectCompletion) DirectCompletion {
	return DirectCompletion{Artifacts: append([]string{}, d.Artifacts...), CreatedAt: FormatTimestamp(d.CreatedAt), Note: d.Note}
}

// FromDirectCompletions は親が直接行った完了の記録の一覧を変換する（空でも `[]`）。
func FromDirectCompletions(ds []core.DirectCompletion) []DirectCompletion {
	out := make([]DirectCompletion, 0, len(ds))
	for _, d := range ds {
		out = append(out, FromDirectCompletion(d))
	}
	return out
}

// Operation は不可逆操作の形。
type Operation struct {
	ApprovedHead *string `json:"approved_head"`
	ChallengeID  string  `json:"challenge_id"`
	// ClosedAt・ClosedReason は state が closed の操作の、閉じた日時と理由（superseded・merged_outside・merged_unapproved・pr_closed・challenge_rejected）。
	ClosedAt            *string `json:"closed_at"`
	ClosedReason        *string `json:"closed_reason"`
	ConsecutiveFailures int     `json:"consecutive_failures"`
	CreatedAt           string  `json:"created_at"`
	ExecutedAt          *string `json:"executed_at"`
	ID                  string  `json:"id"`
	Kind                string  `json:"kind"`
	// LastExecutionDetail は最後の試行の記録の詳細（land_refused のときの委譲先の summary など）。試行が無い・詳細が空なら null。
	LastExecutionDetail *string `json:"last_execution_detail"`
	LastExecutionResult *string `json:"last_execution_result"`
	MergeCommit         *string `json:"merge_commit"`
	Ref                 *string `json:"ref"`
	State               string  `json:"state"`
	Summary             string  `json:"summary"`
	// SupersededBy はこの操作を置き換えた新しい操作の ID、Supersedes はこの操作が置き換えた古い操作の ID（本番反映のやり直し）。
	SupersededBy *string `json:"superseded_by"`
	Supersedes   *string `json:"supersedes"`
	Version      int     `json:"version"`
	WaitingSince *string `json:"waiting_since"`
}

// FromOperation は core.IrreversibleOperation を Operation へ変換する。
func FromOperation(op core.IrreversibleOperation) Operation {
	var last *string
	if op.LastExecutionResult != nil {
		v := string(*op.LastExecutionResult)
		last = &v
	}
	var closedReason *string
	if op.ClosedReason != nil {
		v := string(*op.ClosedReason)
		closedReason = &v
	}
	return Operation{
		ApprovedHead:        op.ApprovedHead,
		ChallengeID:         op.ChallengeID,
		ClosedAt:            optionalTimestamp(op.ClosedAt),
		ClosedReason:        closedReason,
		ConsecutiveFailures: op.ConsecutiveFailures,
		CreatedAt:           FormatTimestamp(op.CreatedAt),
		ExecutedAt:          optionalTimestamp(op.ExecutedAt),
		ID:                  op.ID,
		Kind:                string(op.Kind),
		LastExecutionDetail: op.LastExecutionDetail,
		LastExecutionResult: last,
		MergeCommit:         op.MergeCommit,
		Ref:                 op.Ref,
		State:               string(op.State),
		Summary:             op.Summary,
		SupersededBy:        op.SupersededByOperationID,
		Supersedes:          op.SupersedesOperationID,
		Version:             op.Version,
		WaitingSince:        optionalTimestamp(op.WaitingSince),
	}
}

// optionalTimestamp は t が nil なら nil、そうでなければ FormatTimestamp の文字列へのポインタを返す。
func optionalTimestamp(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := FormatTimestamp(*t)
	return &v
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

// SourceChild は課題の取り込み元の子の形。
type SourceChild struct {
	ExternalKey       string `json:"external_key"`
	ParentExternalKey string `json:"parent_external_key"`
	Title             string `json:"title"`
	UpstreamState     string `json:"upstream_state"`
	URL               string `json:"url"`
}

// FromSourceChildren は core.SourceChild の一覧を変換する（無ければ []）。
func FromSourceChildren(cs []core.SourceChild) []SourceChild {
	out := make([]SourceChild, 0, len(cs))
	for _, c := range cs {
		out = append(out, SourceChild{ExternalKey: c.ExternalKey, ParentExternalKey: c.ParentExternalKey, Title: c.Title, UpstreamState: c.UpstreamState, URL: c.URL})
	}
	return out
}

// Run は run の形。
type Run struct {
	ChallengeID     *string  `json:"challenge_id"`
	CostSource      *string  `json:"cost_source"`
	CostUSD         *float64 `json:"cost_usd"`
	CycleBudgetUSD  *float64 `json:"cycle_budget_usd"`
	CycleID         *string  `json:"cycle_id"`
	EndedAt         *string  `json:"ended_at"`
	Error           *string  `json:"error"`
	FlywheelVersion *string  `json:"flywheel_version"`
	ID              string   `json:"id"`
	Judgment        *string  `json:"judgment"`
	Kind            string   `json:"kind"`
	MaxBudgetUSD    float64  `json:"max_budget_usd"`
	RateLimited     bool     `json:"rate_limited"`
	Result          *string  `json:"result"`
	SessionID       *string  `json:"session_id"`
	StartedAt       string   `json:"started_at"`
	// TaskID は run が結び付くタスクの ID（"T-<n>"）。課題の単位の実行と過去の run は null。
	TaskID *string `json:"task_id"`
}

// FromRun は core.Run を Run へ変換する。
func FromRun(r core.Run) Run {
	return Run{
		ChallengeID:     strOrNil(r.ChallengeID),
		CostSource:      strOrNil(string(r.CostSource)),
		CostUSD:         r.CostUSD,
		CycleBudgetUSD:  r.CycleBudgetUSD,
		CycleID:         r.CycleID,
		EndedAt:         ts(r.EndedAt),
		Error:           strOrNil(r.Error),
		FlywheelVersion: r.FlywheelVersion,
		ID:              r.ID,
		Judgment:        strOrNil(string(r.Judgment)),
		Kind:            string(r.Kind),
		MaxBudgetUSD:    r.MaxBudgetUSD,
		RateLimited:     r.RateLimited,
		Result:          strOrNil(string(r.Result)),
		SessionID:       strOrNil(r.SessionID),
		StartedAt:       FormatTimestamp(r.StartedAt),
		TaskID:          r.TaskID,
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
//
// 完了の承認では、同時に承認した release（Releases）と外した release（ExcludedReleases）も持つ
// （release が 1 件も無いときは両方とも出さない）。
type ChallengeApprovalResponse struct {
	Approval         Approval          `json:"approval"`
	Challenge        Challenge         `json:"challenge"`
	ExcludedReleases []ExcludedRelease `json:"excluded_releases,omitempty"`
	Releases         []ApprovedRelease `json:"releases,omitempty"`
	// RunningReleases・WithdrawnReleases は完了の差し戻しだけが持つ。前者は試行が終わっていない（実行中の）ため
	// 止めなかった release の ID（マージされうる）、後者は取り消した（closed・challenge_rejected）未実行の release の ID。
	RunningReleases   []string `json:"running_releases,omitempty"`
	WithdrawnReleases []string `json:"withdrawn_releases,omitempty"`
}

// ApprovedRelease は完了の承認で同時に承認した release 1 件と、承認の記録に残した head（完全形）。
type ApprovedRelease struct {
	HeadSHA string `json:"head_sha"`
	// MergeIn は、検証の head から「既定ブランチを取り込んだだけ（CI は通っている）」と判定して承認した release の評価
	// （検証の head・今の head・後から入ったコミット・直した差分）。検証の head と一致した承認は出さない。
	MergeIn     *HeadChange `json:"merge_in,omitempty"`
	OperationID string      `json:"operation_id"`
}

// ExcludedRelease は完了の承認で同時には承認せず pending のまま残した release 1 件と、外した理由の名前。
type ExcludedRelease struct {
	// Detail は外した理由の根拠（検証の後に入った取り込み以外のコミット・判定できなかった理由・CI が通っていないこと）。無ければ出さない。
	Detail      string `json:"detail,omitempty"`
	OperationID string `json:"operation_id"`
	Reason      string `json:"reason"`
}

// ChallengeHoldResponse は answer の `{"challenge": …, "hold": …}`。
type ChallengeHoldResponse struct {
	Challenge Challenge `json:"challenge"`
	Hold      Hold      `json:"hold"`
}

// ChallengeDirectCompletionResponse は complete-direct の `{"challenge": …, "direct_completion": …}`。
type ChallengeDirectCompletionResponse struct {
	Challenge        Challenge        `json:"challenge"`
	DirectCompletion DirectCompletion `json:"direct_completion"`
}

// OperationResponse は op add の `{"operation": …}`。
type OperationResponse struct {
	Operation Operation `json:"operation"`
}

// OperationApprovalResponse は操作の approve・reject の `{"approval": …, "operation": …}`。
type OperationApprovalResponse struct {
	Approval Approval `json:"approval"`
	// MergeIn は release を「検証の head から既定ブランチを取り込んだだけ（CI は通っている）」と判定して承認したときの評価。
	MergeIn   *HeadChange `json:"merge_in,omitempty"`
	Operation Operation   `json:"operation"`
	// Undeterminable は release を「取り込みだけとも中身の変更とも確かめられない（人間待ち）」まま、人が根拠を見て承認したときの
	// 評価（kind は undeterminable、detail に判定できなかった理由）。
	Undeterminable *HeadChange `json:"undeterminable,omitempty"`
}

// OperationReopenResponse は `approve <OP-ID> --reopen` の結果（人間待ちの release の課題を完了から着手中へ戻し、release を置き換えた）。
type OperationReopenResponse struct {
	ChallengeID       string `json:"challenge_id"`
	ClosedOperationID string `json:"closed_operation_id"`
	NewOperationID    string `json:"new_operation_id"`
	Reason            string `json:"reason"`
}

// ListResponse は `list` の `{"challenges": […]}`。
type ListResponse struct {
	Challenges []Challenge `json:"challenges"`
}

// ShowRun は show（UI の詳細の API を含む）の runs[] の要素。Run の各キーに fix_pending_ref を足した形
// （`runs` コマンドと run の一覧の API の形は変えない）。
type ShowRun struct {
	ChallengeID    *string  `json:"challenge_id"`
	CostSource     *string  `json:"cost_source"`
	CostUSD        *float64 `json:"cost_usd"`
	CycleBudgetUSD *float64 `json:"cycle_budget_usd"`
	CycleID        *string  `json:"cycle_id"`
	EndedAt        *string  `json:"ended_at"`
	Error          *string  `json:"error"`
	// FixPendingRef は、この run が始めた契約の実行が持つ確かめ待ちの中身。無ければ null。
	FixPendingRef   *string `json:"fix_pending_ref"`
	FlywheelVersion *string `json:"flywheel_version"`
	ID              string  `json:"id"`
	Judgment        *string `json:"judgment"`
	Kind            string  `json:"kind"`
	MaxBudgetUSD    float64 `json:"max_budget_usd"`
	RateLimited     bool    `json:"rate_limited"`
	Result          *string `json:"result"`
	SessionID       *string `json:"session_id"`
	StartedAt       string  `json:"started_at"`
	TaskID          *string `json:"task_id"`
}

// FromShowRuns は課題の詳細の run の一覧を変換する（空でも `[]`）。
func FromShowRuns(rs []core.Run) []ShowRun {
	out := make([]ShowRun, 0, len(rs))
	for _, r := range rs {
		v := FromRun(r)
		out = append(out, ShowRun{
			ChallengeID: v.ChallengeID, CostSource: v.CostSource, CostUSD: v.CostUSD, CycleBudgetUSD: v.CycleBudgetUSD, CycleID: v.CycleID,
			EndedAt: v.EndedAt, Error: v.Error, FixPendingRef: r.FixPendingRef, FlywheelVersion: v.FlywheelVersion, ID: v.ID, Judgment: v.Judgment,
			Kind: v.Kind, MaxBudgetUSD: v.MaxBudgetUSD, RateLimited: v.RateLimited, Result: v.Result, SessionID: v.SessionID,
			StartedAt: v.StartedAt, TaskID: v.TaskID,
		})
	}
	return out
}

// PassedConflictFix は通したコンフリクトの解消 1 件（show の conflict_fixes[] の要素）。承認の要約の ConflictFix（取り込みのマージごとの、
// 直したファイル）とは別の型。
type PassedConflictFix struct {
	// At は作業ログの記録の時刻。
	At string `json:"at"`
	// Files は直したファイルの名前の配列。
	Files   []string `json:"files"`
	FromRef string   `json:"from_ref"`
	// OperationID は反映先宛てのとき反映の操作の ID。統合先宛てのとき null。
	OperationID *string `json:"operation_id"`
	RunID       string  `json:"run_id"`
	// Target は `integration`（統合先宛て）か `release`（反映先宛て）。
	Target string `json:"target"`
	// TaskID は統合先宛てのときタスクの ID。反映先宛てのとき null。
	TaskID *string `json:"task_id"`
	ToRef  string  `json:"to_ref"`
}

// FromConflictFixes は通したコンフリクトの解消の一覧を変換する（空でも `[]`）。
func FromConflictFixes(fs []core.ConflictFixPassed) []PassedConflictFix {
	out := make([]PassedConflictFix, 0, len(fs))
	for _, f := range fs {
		files := append([]string{}, f.Files...)
		out = append(out, PassedConflictFix{
			At: FormatTimestamp(f.At), Files: files, FromRef: f.FromRef, OperationID: f.OperationID, RunID: f.RunID,
			Target: f.Target, TaskID: f.TaskID, ToRef: f.ToRef,
		})
	}
	return out
}

// Budget は課題の詳細の budget。estimate_usd は見込み（取りやめでないタスクの枠の合計）、limit_usd は上限
// （実装の枠とレビュー対応の枠を足した 1 つの額。記録が無い課題は見込みと同じ額）、limit_reason は上限の理由（記録が無ければ null）。
// 見込みが不明の課題（枠を決められないタスクがある）は estimate_usd・limit_usd が null。計画の承認待ちの課題は、承認を待っている版の計画の値。
type Budget struct {
	EstimateUSD *float64 `json:"estimate_usd"`
	LimitReason *string  `json:"limit_reason"`
	LimitUSD    *float64 `json:"limit_usd"`
}

// FromChallengeDetailBudget は課題の詳細の予算を Budget へ変換する。
func FromChallengeDetailBudget(b core.ChallengeDetailBudget) Budget {
	return Budget{EstimateUSD: b.EstimateUSD, LimitReason: b.LimitReason, LimitUSD: b.LimitUSD}
}

// ShowResponse は `show` の成功の形。
type ShowResponse struct {
	// Additions は課題の足す依頼（閉じたものも含む。無ければ []）。
	Additions []TaskAddition `json:"additions"`
	Approvals []Approval     `json:"approvals"`
	Budget    Budget         `json:"budget"`
	Challenge ShowChallenge  `json:"challenge"`
	// ConflictFixes は通したコンフリクトの解消（古い順。無ければ []）。
	ConflictFixes []PassedConflictFix `json:"conflict_fixes"`
	// DirectCompletions は親が直接行った完了の記録（古い順。無ければ []）。1 件以上あることが「親が直接行った」印。
	DirectCompletions []DirectCompletion `json:"direct_completions"`
	// Followups は課題のフォローアップ（状態を問わず全部。無ければ []）。
	Followups  []Followup   `json:"followups"`
	Holds      []Hold       `json:"holds"`
	Operations []Operation  `json:"operations"`
	Pairs      []StagePair  `json:"pairs"`
	Plans      []PlanDetail `json:"plans"`
	// ReleaseHeadChanges は承認待ちの release のうち、PR の head が J5 の検証の head から動いたものの評価
	// （CLI の show だけが足す。GitHub を読めなかった release・head が動いていない release は載せない。無ければ出さない）。
	ReleaseHeadChanges []ReleaseHeadChange `json:"release_head_changes,omitempty"`
	Runs               []ShowRun           `json:"runs"`
	SourceBinding      *SourceBinding      `json:"source_binding"`
	SourceChildren     []SourceChild       `json:"source_children"`
	Stages             Stages              `json:"stages"`
	// SupervisorActions は課題の対処の記録（古い順。無ければ []）。
	SupervisorActions []SupervisorAction `json:"supervisor_actions"`
	// SupervisorHandsTaken は打った手の件数（手 → 結果が executed の対処の記録の数。failed は入れない。無ければ {}）。
	SupervisorHandsTaken map[string]int `json:"supervisor_hands_taken"`
	// Tasks は課題のタスク（ID の昇順。無ければ []）。
	Tasks []Task `json:"tasks"`
}

// FromChallengeDetail は core.ChallengeDetail を ShowResponse へ変換する。
func FromChallengeDetail(d *core.ChallengeDetail) ShowResponse {
	return ShowResponse{
		Additions:            FromTaskAdditions(d.Additions),
		Approvals:            FromApprovals(d.Approvals),
		Budget:               FromChallengeDetailBudget(d.Budget),
		Challenge:            FromShowChallenge(d),
		ConflictFixes:        FromConflictFixes(d.ConflictFixes),
		DirectCompletions:    FromDirectCompletions(d.DirectCompletions),
		Followups:            FromFollowups(d.Followups),
		Holds:                FromHolds(d.Holds),
		Operations:           FromOperations(d.Operations),
		Pairs:                FromPairs(core.Pairs(d.Expected, d.Actual)),
		Plans:                FromPlans(d.Plans),
		Runs:                 FromShowRuns(d.Runs),
		SourceBinding:        FromSourceBinding(d.SourceBinding),
		SourceChildren:       FromSourceChildren(d.SourceChildren),
		Stages:               FromStages(d.StageFields, d.IssuesAnswer),
		SupervisorActions:    FromSupervisorActions(d.SupervisorActions),
		SupervisorHandsTaken: FromSupervisorHandsTaken(d.SupervisorHandsTaken),
		Tasks:                FromTasks(d.Tasks),
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

// WorkspaceChanged は `GET /api/v1/events`（SSE）が送る通知。Type は常に "workspace_changed"。
type WorkspaceChanged struct {
	Type      string `json:"type"`
	Workspace string `json:"workspace"`
}
