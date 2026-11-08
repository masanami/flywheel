package view

import "github.com/masanami/flywheel/internal/core"

// このファイルは承認の API（docs/features/m4-ui-server.md §IF / API「書き込みの API」①）の応答の型。
// CLI の approve の要約・--json の形は変えない（core.ApprovalPreview の中身に、release ごとの PR の
// head と release_held を足した形）。

// ConditionsAfterReleaseHeading は、完了の承認の要約に並べる「本番反映で満たされる見込みの条件」の見出し。
// CLI の approve の要約と API（ApprovalSummary.ConditionsAfterReleaseHeading）が同じ文言を引く 1 か所。
// releasing はその操作で本番反映（release）が 1 つでも承認されるか。
func ConditionsAfterReleaseHeading(releasing bool) string {
	if releasing {
		return "承認で本番反映されると満たされる条件:"
	}
	return "本番反映で満たされる見込みの条件（この操作では本番反映が承認されないため、満たされないまま残る）:"
}

// ApprovalPlan は計画承認待ちの要約の計画（最新の版と本文）。
type ApprovalPlan struct {
	Body    string `json:"body"`
	Version int    `json:"version"`
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
// 承認しない（ReleaseHeld）ので、CLI の --hold-release と同じ「満たされないまま残る」側の文言。
type ApprovalSummary struct {
	ChallengeID                   string   `json:"challenge_id"`
	ConditionsAfterRelease        []string `json:"conditions_after_release"`
	ConditionsAfterReleaseHeading string   `json:"conditions_after_release_heading"`
	// DirectCompletion は、この完了確認待ちが親が直接行った完了の記録によるときだけ非 null（成果物の参照を
	// 完了条件と並べて見せる。完了の検証は通っていない）。
	DirectCompletion       *DirectCompletion        `json:"direct_completion"`
	DoneCriteria           *string                  `json:"done_criteria"`
	Kind                   string                   `json:"kind"`
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
	// ReleaseHeld は常に true なので、本番反映が承認されない側の見出し。
	s.ConditionsAfterReleaseHeading = ConditionsAfterReleaseHeading(!s.ReleaseHeld)
	switch p.Kind {
	case core.ApprovalKindPlan:
		s.Plan = &ApprovalPlan{Body: p.PlanBody, Version: p.PlanVersion}
	case core.ApprovalKindCompletion:
		dc := p.DoneCriteria
		s.DoneCriteria = &dc
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
