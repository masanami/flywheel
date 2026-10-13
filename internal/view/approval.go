package view

import "github.com/masanami/flywheel/internal/core"

// このファイルは承認の API（docs/features/m4-ui-server.md §IF / API「書き込みの API」①）の応答の型。
// CLI の approve の要約・--json の形は変えない（core.ApprovalPreview の中身に、release ごとの PR の
// head と release_held を足した形）。

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
type ApprovalSummary struct {
	ChallengeID            string                   `json:"challenge_id"`
	DoneCriteria           *string                  `json:"done_criteria"`
	Kind                   string                   `json:"kind"`
	OtherPendingOperations []ApprovalOtherOperation `json:"other_pending_operations"`
	Plan                   *ApprovalPlan            `json:"plan"`
	ReleaseHeld            bool                     `json:"release_held"`
	Releases               []ApprovalRelease        `json:"releases"`
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
		Kind:                   string(p.Kind),
		OtherPendingOperations: make([]ApprovalOtherOperation, 0, len(p.OtherPendingOperations)),
		ReleaseHeld:            true,
		Releases:               make([]ApprovalRelease, 0, len(heads)),
		Title:                  p.Title,
		Version:                p.Version,
	}
	switch p.Kind {
	case core.ApprovalKindPlan:
		s.Plan = &ApprovalPlan{Body: p.PlanBody, Version: p.PlanVersion}
	case core.ApprovalKindCompletion:
		dc := p.DoneCriteria
		s.DoneCriteria = &dc
	}
	for _, h := range heads {
		s.Releases = append(s.Releases, ApprovalRelease{HeadSHA: h.HeadSHA, ID: h.Operation.ID, Ref: h.Operation.Ref, Summary: h.Operation.Summary})
	}
	for _, op := range p.OtherPendingOperations {
		s.OtherPendingOperations = append(s.OtherPendingOperations, ApprovalOtherOperation{ID: op.ID, Kind: string(op.Kind), Summary: op.Summary})
	}
	return ApprovalSummaryResponse{Approval: s}
}
