package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// runBudget は `flywheel budget <C-ID> --impl-usd <額> [--review-usd <額>] [--task <T-ID>]` の実装
// （docs/features/m3-invoker-delegation.md §予算ガード。決定 M3H3）。承認と同じ本人確認つきの
// 2 段階（core.PrepareBudgetForTask → core.Verify → core.ExecuteBudget）で、枠を置き換える（足し合わせない）。
// 置き換える枠は、そのタスクの記録の枠（実装・レビュー対応）で（ほかのタスクの枠は変えない。`--task` で指定する。
// 省略できるのは、未完のタスクがちょうど 1 個のときだけで、2 個以上のときは指定が要る。タスクを持たない過去の課題は、承認済みの
// 計画の版の枠を置き換える）。枠の置き換えの規則・承認の記録・作業ログは core が持つ。
func runBudget(a Args) (any, error) {
	id := a.Positional[0]
	verifier, err := verifierFor(a)
	if err != nil {
		return nil, err
	}
	limitText, hasLimit := a.Values["limit"]
	_, hasImpl := a.Values["impl-usd"]
	_, hasReview := a.Values["review-usd"]
	_, hasTask := a.Values["task"]
	switch {
	case hasLimit && (hasImpl || hasReview || hasTask):
		return nil, NewError(CodeValidationFailed, "--limit は、タスクの枠の指定（--impl-usd・--review-usd・--task）と同時に指定できません")
	case hasLimit:
		return runBudgetLimit(a, id, verifier, limitText)
	case !hasImpl:
		return nil, NewError(CodeUsageError, "--impl-usd か --limit のどちらかが必要です")
	}
	impl, err := parseBudgetFlag("impl-usd", a.Values["impl-usd"])
	if err != nil {
		return nil, err
	}
	var review *float64
	if v, ok := a.Values["review-usd"]; ok {
		r, err := parseBudgetFlag("review-usd", v)
		if err != nil {
			return nil, err
		}
		review = &r
	}

	// サイズの既定を引く宣言は、現在の額の表示と見込みの計算に使う（読めなくても枠は置き換えられる。core は渡されなければ自分で読む）。
	decl, derr := core.LoadAgentDeclaration(a.Store.Workspace())
	if derr != nil {
		decl = nil
	}
	preview, err := a.Store.PrepareBudgetForTask(context.Background(), id, a.Values["task"], decl, impl, review)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	att, err := core.Verify(channelFor(a), verifier, budgetSummaryText(preview), id)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	approval, err := a.Store.ExecuteBudget(context.Background(), core.BudgetRequest{
		ChallengeID:     id,
		ExpectedVersion: preview.Version,
		PlanVersion:     preview.PlanVersion,
		TaskID:          preview.TaskID,
		ImplUSD:         impl,
		ReviewUSD:       review,
		Agent:           decl,
	}, att)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: view.BudgetResponse{
		Approval:    view.FromApproval(*approval),
		ChallengeID: id,
		ImplUSD:     impl,
		PlanVersion: preview.PlanVersion,
		ReviewUSD:   review,
	}, text: id + "\n"}, nil
}

// runBudgetLimit は `flywheel budget <C-ID> --limit <USD>` の実装。課題の上限だけを変える（枠・課題の状態と版は変えない）。
// 本人確認は枠の増額と同じ。見込みより小さい額は、端末を開く前に検証の誤りで断る。規則は core が持つ。
func runBudgetLimit(a Args, id string, verifier core.Verifier, limitText string) (any, error) {
	limit, err := parseBudgetFlag("limit", limitText)
	if err != nil {
		return nil, err
	}
	decl, derr := core.LoadAgentDeclaration(a.Store.Workspace())
	if derr != nil {
		decl = nil
	}
	preview, err := a.Store.PrepareBudgetLimit(context.Background(), id, decl, limit)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	att, err := core.Verify(channelFor(a), verifier, budgetLimitSummaryText(preview), id)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	approval, err := a.Store.ExecuteBudget(context.Background(), core.BudgetRequest{
		ChallengeID:     id,
		ExpectedVersion: preview.Version,
		PlanVersion:     preview.PlanVersion,
		LimitUSD:        &limit,
		Agent:           decl,
	}, att)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: view.BudgetResponse{
		Approval:    view.FromApproval(*approval),
		ChallengeID: id,
		PlanVersion: preview.PlanVersion,
	}, text: id + "\n"}, nil
}

// budgetLimitSummaryText は flywheel budget --limit が確認の前に端末へ表示する要約。
func budgetLimitSummaryText(p *core.BudgetPreview) string {
	var b strings.Builder
	usd := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) + " USD" }
	fmt.Fprintf(&b, "課題 ID:     %s\n", p.ChallengeID)
	fmt.Fprintf(&b, "タイトル:    %s\n", p.Title)
	fmt.Fprintf(&b, "承認の種類:  予算（budget）の上限\n")
	fmt.Fprintf(&b, "計画の版:    v%d\n", p.PlanVersion)
	fmt.Fprintf(&b, "見込み:      %s\n", usd(p.EstimateUSD))
	fmt.Fprintf(&b, "現在の上限:  %s\n", usd(p.CurrentLimitUSD))
	fmt.Fprintf(&b, "変更後の上限: %s\n", usd(p.NewLimitUSD))
	return b.String()
}

// parseBudgetFlag は金額のフラグの値を数に変える。数でなければ usage_error。
func parseBudgetFlag(name, value string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0, NewError(CodeUsageError, fmt.Sprintf("--%s は数で指定してください: %q", name, value))
	}
	return v, nil
}

// budgetSummaryText は flywheel budget が確認の前に端末へ表示する要約（課題・計画の版・置き換える額）。
func budgetSummaryText(p *core.BudgetPreview) string {
	var b strings.Builder
	usd := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) + " USD" }
	fmt.Fprintf(&b, "課題 ID:     %s\n", p.ChallengeID)
	fmt.Fprintf(&b, "タイトル:    %s\n", p.Title)
	fmt.Fprintf(&b, "承認の種類:  予算（budget）\n")
	if p.TaskID != "" {
		fmt.Fprintf(&b, "タスク:      %s\n", p.TaskID)
	}
	fmt.Fprintf(&b, "計画の版:    v%d\n", p.PlanVersion)
	if p.CurrentKnown {
		fmt.Fprintf(&b, "現在の実装枠:        %s（残り %s）\n", usd(p.CurrentImplUSD), usd(p.ImplRemainingUSD))
		fmt.Fprintf(&b, "現在のレビュー対応枠: %s（残り %s）\n", usd(p.CurrentReviewUSD), usd(p.ReviewRemainingUSD))
	}
	fmt.Fprintf(&b, "置き換え後の実装枠:        %s\n", usd(p.NewImplUSD))
	if p.ReviewUnchanged {
		fmt.Fprintf(&b, "置き換え後のレビュー対応枠: 変更なし\n")
	} else {
		fmt.Fprintf(&b, "置き換え後のレビュー対応枠: %s\n", usd(p.NewReviewUSD))
	}
	return b.String()
}
