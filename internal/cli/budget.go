package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// runBudget は `flywheel budget <C-ID> --impl-usd <額> [--review-usd <額>]` の実装
// （docs/features/m3-invoker-delegation.md §予算ガード。決定 M3H3）。承認と同じ本人確認つきの
// 2 段階（core.PrepareBudget → core.Verify → core.ExecuteBudget）で、課題の承認済みの計画の版の
// 枠を置き換える。枠の置き換えの規則・承認の記録・作業ログは core が持つ。
func runBudget(a Args) (any, error) {
	id := a.Positional[0]
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

	// サイズの既定を引く宣言は、現在の額の表示にだけ使う（読めなくても枠は置き換えられる）。
	decl, derr := core.LoadAgentDeclaration(a.Store.Workspace())
	if derr != nil {
		decl = nil
	}
	preview, err := a.Store.PrepareBudget(context.Background(), id, decl, impl, review)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	att, err := core.Verify(core.ChannelCLI, newTTYConfirmVerifier(a.Stdin), budgetSummaryText(preview), id)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	approval, err := a.Store.ExecuteBudget(context.Background(), core.BudgetRequest{
		ChallengeID:     id,
		ExpectedVersion: preview.Version,
		PlanVersion:     preview.PlanVersion,
		ImplUSD:         impl,
		ReviewUSD:       review,
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
