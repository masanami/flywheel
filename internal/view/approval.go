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
	// BudgetLimitUSD は計画の上限（計画が書かなければ見込みと同じ額。見込みが不明なら null）、BudgetLimitReason は上限が見込みより大きいときだけの
	// 上限の理由（同じ額なら null）。budget_usd は見込み。
	BudgetLimitReason *string            `json:"budget_limit_reason"`
	BudgetLimitUSD    *float64           `json:"budget_limit_usd"`
	BudgetUSD         *float64           `json:"budget_usd"`
	Summary           *string            `json:"summary"`
	Tasks             []ApprovalPlanTask `json:"tasks"`
	Topics            []PlanTopic        `json:"topics"`
	UsesIntegration   *bool              `json:"uses_integration"`
	Version           int                `json:"version"`
}

// ApprovalPlanTask は計画承認待ちの要約のタスク 1 件。承認の前はタスクの ID が無いので、計画の中の識別子（key）と、
// 前提のタスクの key で出す。budget_impl_usd・budget_review_usd はタスクの枠（計画が書いたときだけ。書いていなければ null）。phase は表示の区分（承認の前なので next か blocked）。source は対応する取り込み元の子の external_key（対応しなければ null）。
type ApprovalPlanTask struct {
	BudgetImplUSD   *float64 `json:"budget_impl_usd"`
	BudgetReviewUSD *float64 `json:"budget_review_usd"`
	DependsOn       []string `json:"depends_on"`
	Key             string   `json:"key"`
	Phase           string   `json:"phase"`
	Source          *string  `json:"source"`
	Title           string   `json:"title"`
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
//
// ConflictFixes・Followups・IntegrationRef・Tasks・UsesIntegration は完了承認画面が使う値で、完了確認待ちのときだけ中身が入る
// （それ以外は空配列か null）。要約と同じ読み取りで返すので、画面は課題の詳細を別に読んで版を見比べなくてよい。
// Followups は課題にぶら下がる未（open）のものだけ。
type ApprovalSummary struct {
	// AddedTasks は計画の後に足されたタスク（完了確認待ちのときだけ中身が入る。無ければ空配列）。
	AddedTasks                    []AddedTask         `json:"added_tasks"`
	ChallengeID                   string              `json:"challenge_id"`
	ConditionsAfterRelease        []string            `json:"conditions_after_release"`
	ConditionsAfterReleaseHeading string              `json:"conditions_after_release_heading"`
	ConflictFixes                 []PassedConflictFix `json:"conflict_fixes"`
	// DirectCompletion は、この完了確認待ちが親が直接行った完了の記録によるときだけ非 null（成果物の参照を
	// 完了条件と並べて見せる。完了の検証は通っていない）。
	DirectCompletion *DirectCompletion `json:"direct_completion"`
	DoneCriteria     *string           `json:"done_criteria"`
	Followups        []Followup        `json:"followups"`
	IntegrationRef   *string           `json:"integration_ref"`
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
	// SupervisorHandsTaken は打った手の件数（手 → 結果が executed の対処の記録の数。failed は入れない。無ければ {}）。
	SupervisorHandsTaken map[string]int `json:"supervisor_hands_taken"`
	// SupervisorQuestions はスーパーバイザーが答えた子の問い（問い・答え・根拠。無ければ []）。完了の承認のときにオーナーへ見せる。
	SupervisorQuestions []SupervisorQuestion     `json:"supervisor_questions"`
	Tasks               []ApprovalCompletionTask `json:"tasks"`
	Title               string                   `json:"title"`
	UsesIntegration     *bool                    `json:"uses_integration"`
	Version             int                      `json:"version"`
}

// AddedTask は計画の後に足されたタスク 1 件（課題・題名・出どころ）。完了の承認の要約と cycle の出力が使う。
type AddedTask struct {
	ChallengeID string `json:"challenge_id"`
	Origin      string `json:"origin"`
	TaskID      string `json:"task_id"`
	Title       string `json:"title"`
}

// FromAddedTasks は足されたタスクの一覧を変換する（空でも `[]`）。
func FromAddedTasks(ts []core.AddedTask) []AddedTask {
	out := make([]AddedTask, 0, len(ts))
	for _, t := range ts {
		out = append(out, AddedTask{ChallengeID: t.ChallengeID, Origin: string(t.Origin), TaskID: t.TaskID, Title: t.Title})
	}
	return out
}

// ApprovalCompletionTask は完了承認画面に並べるタスク 1 件（core.CompletionTask）。Phase はタスクの表示の区分（ツリーと同じ値）。
// InTree が false のタスク（計画を作り直す前の版の済みのタスク）は詳細の画面が開けない。
type ApprovalCompletionTask struct {
	BudgetUSD float64 `json:"budget_usd"` // 実装の枠とレビュー対応の枠の合計
	ID        string  `json:"id"`
	InTree    bool    `json:"in_tree"`
	Phase     string  `json:"phase"`
	SpentUSD  float64 `json:"spent_usd"`
	Title     string  `json:"title"`
}

// ApprovalSummaryResponse は `GET …/challenges/{id}/approval` の本文。
type ApprovalSummaryResponse struct {
	Approval ApprovalSummary `json:"approval"`
}

// FromApprovalPreview は core.ApprovalPreview と release ごとの head（core.ResolveReleaseHeads。
// p.PendingReleases と同じ順序・件数）を ApprovalSummaryResponse へ変換する。
func FromApprovalPreview(p *core.ApprovalPreview, heads []core.ReleaseHead) ApprovalSummaryResponse {
	s := ApprovalSummary{
		AddedTasks:             FromAddedTasks(p.AddedTasks),
		ChallengeID:            p.ChallengeID,
		ConditionsAfterRelease: append([]string{}, p.ConditionsAfterRelease...),
		Kind:                   string(p.Kind),
		OtherPendingOperations: make([]ApprovalOtherOperation, 0, len(p.OtherPendingOperations)),
		Pairs:                  FromPairs(p.Pairs),
		ReleaseHeld:            true,
		Releases:               make([]ApprovalRelease, 0, len(heads)),
		Stages:                 FromStages(p.StageFields, p.IssuesAnswer),
		Tasks:                  make([]ApprovalCompletionTask, 0, len(p.CompletionTasks)),
		Title:                  p.Title,
		Version:                p.Version,
		ConflictFixes:          FromConflictFixes(p.ConflictFixes),
		SupervisorHandsTaken:   FromSupervisorHandsTaken(p.SupervisorHandsTaken),
		SupervisorQuestions:    FromSupervisorQuestions(p.SupervisorQuestions),
		Followups:              FromFollowups(p.OpenFollowups),
		IntegrationRef:         p.IntegrationRef,
		UsesIntegration:        p.UsesIntegration,
	}
	for _, t := range p.CompletionTasks {
		s.Tasks = append(s.Tasks, ApprovalCompletionTask{BudgetUSD: t.BudgetImplUSD + t.BudgetReviewUSD, ID: t.ID, InTree: t.InTree, Phase: string(t.Phase), SpentUSD: t.SpentUSD, Title: t.Title})
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
			tasks = append(tasks, ApprovalPlanTask{BudgetImplUSD: t.BudgetImplUSD, BudgetReviewUSD: t.BudgetReviewUSD, DependsOn: append([]string{}, t.DependsOn...), Key: t.Key, Phase: phase, Source: strOrNil(t.Source), Title: t.Title})
		}
		s.Plan = &ApprovalPlan{Body: p.PlanBody, BudgetLimitReason: p.PlanBudgetLimitReason, BudgetLimitUSD: p.PlanBudgetLimitUSD, BudgetUSD: p.PlanBudgetUSD, Summary: p.PlanSummary, Tasks: tasks, Topics: FromPlanTopics(p.PlanTopics), UsesIntegration: p.PlanUsesIntegration, Version: p.PlanVersion}
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
