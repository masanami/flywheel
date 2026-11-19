package view

import "github.com/masanami/flywheel/internal/core"

// このファイルは承認の API（docs/features/m4-ui-server.md §IF / API「書き込みの API」①）の応答の型。
// CLI の approve の要約・--json の形は変えない（core.ApprovalPreview の中身に、release ごとの PR の
// head と release_held を足した形）。

// ConditionsOutlook は、完了の承認の要約で「本番反映で満たされる見込みの条件」が満たされる見通し。
type ConditionsOutlook int

const (
	// OutlookNotReleasing は、この操作で本番反映が承認されず、待っている本番反映も無い（満たされないまま残る）。
	OutlookNotReleasing ConditionsOutlook = iota
	// OutlookReleasing は、この操作で本番反映（release）が 1 つ以上承認される。
	OutlookReleasing
	// OutlookApprovedRelease は、この操作では承認されないが、承認済みで未実行の release が課題にある
	// （それが走れば満たされる）。
	OutlookApprovedRelease
)

// OutlookFor は、この操作で承認される release があるか（releasing）と、課題に承認済みで未実行の
// release があるか（approvedRelease）から見通しを決める。CLI と API が同じ規則を引く 1 か所。
func OutlookFor(releasing, approvedRelease bool) ConditionsOutlook {
	switch {
	case releasing:
		return OutlookReleasing
	case approvedRelease:
		return OutlookApprovedRelease
	}
	return OutlookNotReleasing
}

// ConditionsAfterReleaseHeading は、完了の承認の要約に並べる「本番反映で満たされる見込みの条件」の見出し。
// CLI の approve の要約と API（ApprovalSummary.ConditionsAfterReleaseHeading）と UI の偽のサーバが
// 同じ文言を引く 1 か所。
func ConditionsAfterReleaseHeading(o ConditionsOutlook) string {
	switch o {
	case OutlookReleasing:
		return "承認で本番反映されると満たされる条件:"
	case OutlookApprovedRelease:
		return "承認済みの本番反映が走れば満たされる条件:"
	}
	return "本番反映で満たされる見込みの条件（この操作では本番反映が承認されないため、満たされないまま残る）:"
}

// ApprovalPlan は計画承認待ちの要約の計画（最新の版と本文、論点と問い、タスク、統合先を使うか）。
// 構造化した出力を持たない計画では topics・tasks は []、uses_integration は null。
type ApprovalPlan struct {
	Body string `json:"body"`
	// BudgetUSD・Summary は `show --plan --json` の plan.budget_usd・plan.summary と同じ値（API だけ。端末の approve の要約と --json には無い）。
	BudgetUSD       *float64           `json:"budget_usd"`
	Summary         *string            `json:"summary"`
	Tasks           []ApprovalPlanTask `json:"tasks"`
	Topics          []PlanTopic        `json:"topics"`
	UsesIntegration *bool              `json:"uses_integration"`
	Version         int                `json:"version"`
}

// ApprovalPlanTask は計画承認待ちの要約のタスク 1 件。承認の前はタスクの ID が無いので、計画の中の識別子（key）と、
// 前提のタスクの key で出す。phase は表示の区分（承認の前なので next か blocked）。source は対応する取り込み元の子の external_key（対応しなければ null）。
type ApprovalPlanTask struct {
	DependsOn []string `json:"depends_on"`
	Key       string   `json:"key"`
	Phase     string   `json:"phase"`
	Source    *string  `json:"source"`
	Title     string   `json:"title"`
}

// ApprovalRelease は完了の承認で扱う未承認の release 1 件。HeadSHA は ref が PR の URL でない・
// 取得に失敗したとき null。
type ApprovalRelease struct {
	HeadSHA *string `json:"head_sha"`
	ID      string  `json:"id"`
	Ref     *string `json:"ref"`
	Summary string  `json:"summary"`
}

// ApprovalOtherOperation は完了の承認では承認されない未承認の不可逆操作。
type ApprovalOtherOperation struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
}

// ApprovalSummary は承認の①の要約。Plan は計画承認待ちのときだけ、DoneCriteria は完了確認待ちの
// ときだけ非 null。ReleaseHeld は常に true（UI の承認は本番反映の保留つきの完了の承認だけ）。
//
// ConditionsAfterRelease は、完了確認待ちへ進めた検証が「本番反映で満たされる見込み」と区分した条件
// （無い・完了確認待ち以外は空配列）。ConditionsAfterReleaseHeading はその見出しで、UI の承認は本番反映を
// 承認しない（ReleaseHeld）ので、承認済みで未実行の release が無ければ CLI の --hold-release と同じ
// 「満たされないまま残る」側、あれば「承認済みの本番反映が走れば満たされる」側の文言。
type ApprovalSummary struct {
	ChallengeID                   string   `json:"challenge_id"`
	ConditionsAfterRelease        []string `json:"conditions_after_release"`
	ConditionsAfterReleaseHeading string   `json:"conditions_after_release_heading"`
	// DirectCompletion は、この完了確認待ちが親が直接行った完了の記録によるときだけ非 null（成果物の参照を
	// 完了条件と並べて見せる。完了の検証は通っていない）。
	DirectCompletion *DirectCompletion `json:"direct_completion"`
	DoneCriteria     *string           `json:"done_criteria"`
	Kind             string            `json:"kind"`
	// MergeabilityNote は、検証が PR のマージ可否を確かめられないまま完了確認待ちへ進めたときだけ
	// core.MergeabilityUnconfirmedNote（「マージ可否は未確認」）。それ以外は ""。CLI の要約の「PR のマージ可否」の行と同じ。
	MergeabilityNote       string                   `json:"mergeability_note"`
	OtherPendingOperations []ApprovalOtherOperation `json:"other_pending_operations"`
	Pairs                  []StagePair              `json:"pairs"`
	Plan                   *ApprovalPlan            `json:"plan"`
	ReleaseHeld            bool                     `json:"release_held"`
	Releases               []ApprovalRelease        `json:"releases"`
	Stages                 Stages                   `json:"stages"`
	Title                  string                   `json:"title"`
	Version                int                      `json:"version"`
}

// ApprovalSummaryResponse は `GET …/challenges/{id}/approval` の本文。
type ApprovalSummaryResponse struct {
	Approval ApprovalSummary `json:"approval"`
}

// FromApprovalPreview は core.ApprovalPreview と release ごとの head（core.ResolveReleaseHeads。
// p.PendingReleases と同じ順序・件数）を ApprovalSummaryResponse へ変換する。
func FromApprovalPreview(p *core.ApprovalPreview, heads []core.ReleaseHead) ApprovalSummaryResponse {
	s := ApprovalSummary{
		ChallengeID:            p.ChallengeID,
		ConditionsAfterRelease: append([]string{}, p.ConditionsAfterRelease...),
		Kind:                   string(p.Kind),
		OtherPendingOperations: make([]ApprovalOtherOperation, 0, len(p.OtherPendingOperations)),
		Pairs:                  FromPairs(p.Pairs),
		ReleaseHeld:            true,
		Releases:               make([]ApprovalRelease, 0, len(heads)),
		Stages:                 FromStages(p.StageFields, p.IssuesAnswer),
		Title:                  p.Title,
		Version:                p.Version,
	}
	// ReleaseHeld は常に true なので、この操作では本番反映が承認されない。承認済みで未実行の release が
	// あるときだけ、それが走れば満たされる側の見出し。
	s.ConditionsAfterReleaseHeading = ConditionsAfterReleaseHeading(OutlookFor(false, p.HasApprovedRelease))
	switch p.Kind {
	case core.ApprovalKindPlan:
		tasks := make([]ApprovalPlanTask, 0, len(p.PlanTasks))
		for i, t := range p.PlanTasks {
			phase := ""
			if i < len(p.PlanTaskPhases) {
				phase = string(p.PlanTaskPhases[i])
			}
			tasks = append(tasks, ApprovalPlanTask{DependsOn: append([]string{}, t.DependsOn...), Key: t.Key, Phase: phase, Source: strOrNil(t.Source), Title: t.Title})
		}
		s.Plan = &ApprovalPlan{Body: p.PlanBody, BudgetUSD: p.PlanBudgetUSD, Summary: p.PlanSummary, Tasks: tasks, Topics: FromPlanTopics(p.PlanTopics), UsesIntegration: p.PlanUsesIntegration, Version: p.PlanVersion}
	case core.ApprovalKindCompletion:
		dc := p.DoneCriteria
		s.DoneCriteria = &dc
		if p.MergeabilityUnconfirmed {
			s.MergeabilityNote = core.MergeabilityUnconfirmedNote
		}
		if p.DirectCompletion != nil {
			d := FromDirectCompletion(*p.DirectCompletion)
			s.DirectCompletion = &d
		}
	}
	for _, h := range heads {
		s.Releases = append(s.Releases, ApprovalRelease{HeadSHA: h.HeadSHA, ID: h.Operation.ID, Ref: h.Operation.Ref, Summary: h.Operation.Summary})
	}
	for _, op := range p.OtherPendingOperations {
		s.OtherPendingOperations = append(s.OtherPendingOperations, ApprovalOtherOperation{ID: op.ID, Kind: string(op.Kind), Summary: op.Summary})
	}
	return ApprovalSummaryResponse{Approval: s}
}
