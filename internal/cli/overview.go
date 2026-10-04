package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/masanami/flywheel/internal/core"
)

// このファイルは `flywheel status`（Issue #14）の実コマンドを持つ。区分の判定
// （どの状態の課題・どの状態の不可逆操作がどの区分に属するか）は core.GetOverview
// が行い、ここでは core.Overview を JSON／テキストへ整形するだけ（親要件チケット
// #4「CLI は core の公開 API だけを呼ぶ。区分の判定を internal/cli に書かない」）。

// runStatus は `flywheel status` の実装。読み取り専用（core.GetOverview は状態・
// 作業ログを変えない）。
func runStatus(a Args) (any, error) {
	// サイズの既定を引く宣言。読めなくても status は読み取り専用の表示として動かす
	// （その場合、枠の額を明記しない計画の課題は budget_exhausted の判定の対象外になる）。
	decl, derr := core.LoadAgentDeclaration(a.Store.Workspace())
	if derr != nil {
		decl = nil
	}
	ov, err := a.Store.GetOverviewFor(context.Background(), decl)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{
		json: map[string]any{
			"needs_human": map[string]any{
				"challenges":       challengesJSON(ov.NeedsHumanChallenges),
				"operations":       operationsJSON(ov.NeedsHumanOperations),
				"discrepancies":    discrepanciesJSON(ov.Discrepancies),
				"triage":           triageJSON(ov.NeedsHumanTriage),
				"slots":            slotsJSON(ov.NeedsHumanSlots),
				"budget_exhausted": budgetExhaustedJSON(ov.NeedsHumanBudgetExhausted),
			},
			"actionable": map[string]any{
				"challenges": challengesJSON(ov.ActionableChallenges),
			},
			"approved": map[string]any{
				"operations": operationsJSON(ov.ApprovedOperations),
			},
		},
		text: overviewText(ov),
	}, nil
}

// discrepanciesJSON は食い違いの一覧を「成功時の JSON 出力の規約」の形
// （`{"challenge_id", "kinds"}`。§食い違いの表示）へ変換する。
func discrepanciesJSON(discrepancies []core.Discrepancy) []map[string]any {
	out := make([]map[string]any, 0, len(discrepancies))
	for _, d := range discrepancies {
		out = append(out, map[string]any{"challenge_id": d.ChallengeID, "kinds": discrepancyKindsJSON(d.Kinds)})
	}
	return out
}

// discrepancyKindsJSON は core.DiscrepancyKind の一覧を文字列の一覧へ変換する
// （空でも `[]`。`status` の `kinds` と `ingest` の `unread` が共有する。
// docs/features/m2-github-issue-ingest.md §IF / API「`unread`…`status` の
// `kinds` と同じ判定」）。
func discrepancyKindsJSON(kinds []core.DiscrepancyKind) []string {
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, string(k))
	}
	return out
}

// triageJSON は §IF / API「status.needs_human.triage（S1）:
// [{"challenge_id", "run_id", "reason"}]」の形へ変換する（#84）。
func triageJSON(items []core.TriageItem) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{
			"challenge_id": it.ChallengeID,
			"run_id":       it.RunID,
			"reason":       it.Reason,
		})
	}
	return out
}

// budgetExhaustedJSON は §IF / API「status.needs_human.budget_exhausted（S2）:
// [{"challenge_id", "plan_version", "impl_remaining_usd", "review_remaining_usd"}]」の形へ変換する。
func budgetExhaustedJSON(items []core.BudgetExhausted) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{
			"challenge_id":         it.ChallengeID,
			"plan_version":         it.PlanVersion,
			"impl_remaining_usd":   it.ImplRemainingUSD,
			"review_remaining_usd": it.ReviewRemainingUSD,
		})
	}
	return out
}

// slotsJSON は §IF / API「status.needs_human.slots（S2）:
// [{"slot_id", "repo", "path", "run_id"}]」の形へ変換する（run_id は使用中の run。
// needs_attention のスロットは使用中でないため通常 null）。
func slotsJSON(items []core.SlotAttention) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		var runID any
		if it.RunID != nil {
			runID = *it.RunID
		}
		out = append(out, map[string]any{
			"slot_id": it.SlotID,
			"repo":    it.Repo,
			"path":    it.Path,
			"run_id":  runID,
		})
	}
	return out
}

// challengesJSON は課題の一覧を「成功時の JSON 出力の規約」の課題の形へ変換する
// （runList はこれまで同じ変換をインラインで書いていたが、runStatus も同じ変換を
// 2箇所で必要とするため、ここで共有できるよう切り出す）。
func challengesJSON(challenges []core.Challenge) []map[string]any {
	out := make([]map[string]any, 0, len(challenges))
	for _, c := range challenges {
		out = append(out, challengeJSON(c))
	}
	return out
}

// overviewText は --json 無しの status の表示。3区分を見出しつきで分けて示す。
// 食い違い（needs_human.discrepancies）は JSON と同じく「人間の操作を待っている
// もの」の中に出す（docs/features/m2-github-issue-ingest.md §食い違いの表示）。
func overviewText(ov *core.Overview) string {
	var b strings.Builder
	fmt.Fprintf(&b, "人間の操作を待っているもの:\n")
	if len(ov.NeedsHumanChallenges) == 0 && len(ov.NeedsHumanOperations) == 0 && len(ov.Discrepancies) == 0 && len(ov.NeedsHumanTriage) == 0 && len(ov.NeedsHumanSlots) == 0 && len(ov.NeedsHumanBudgetExhausted) == 0 {
		fmt.Fprintf(&b, "  (なし)\n")
	}
	for _, c := range ov.NeedsHumanChallenges {
		label, _ := c.Status.Label()
		fmt.Fprintf(&b, "  %s\t%s\t%s\n", c.ID, label, c.Title)
	}
	for _, op := range ov.NeedsHumanOperations {
		fmt.Fprintf(&b, "  %s\t%s\t%s\t%s\n", op.ID, op.ChallengeID, op.Kind, op.Summary)
	}
	for _, tr := range ov.NeedsHumanTriage {
		fmt.Fprintf(&b, "  %s\t仕分け\t%s\t%s\n", tr.ChallengeID, tr.RunID, tr.Reason)
	}
	for _, sl := range ov.NeedsHumanSlots {
		fmt.Fprintf(&b, "  %s\t要確認のスロット\t%s\t%s\t%s\n", sl.SlotID, sl.Repo, sl.Path, sl.Reason)
	}
	for _, be := range ov.NeedsHumanBudgetExhausted {
		fmt.Fprintf(&b, "  %s\t予算切れ\t計画 v%d\t実装枠の残り %s USD\tレビュー対応枠の残り %s USD\n",
			be.ChallengeID, be.PlanVersion, strconv.FormatFloat(be.ImplRemainingUSD, 'f', -1, 64), strconv.FormatFloat(be.ReviewRemainingUSD, 'f', -1, 64))
	}
	for _, d := range ov.Discrepancies {
		kinds := make([]string, 0, len(d.Kinds))
		for _, k := range d.Kinds {
			kinds = append(kinds, string(k))
		}
		fmt.Fprintf(&b, "  %s\t食い違い\t%s\n", d.ChallengeID, strings.Join(kinds, ","))
	}

	fmt.Fprintf(&b, "システムが次に進められるもの:\n")
	if len(ov.ActionableChallenges) == 0 {
		fmt.Fprintf(&b, "  (なし)\n")
	}
	for _, c := range ov.ActionableChallenges {
		label, _ := c.Status.Label()
		fmt.Fprintf(&b, "  %s\t%s\t%s\n", c.ID, label, c.Title)
	}

	fmt.Fprintf(&b, "承認済みの不可逆操作:\n")
	if len(ov.ApprovedOperations) == 0 {
		fmt.Fprintf(&b, "  (なし)\n")
	}
	for _, op := range ov.ApprovedOperations {
		fmt.Fprintf(&b, "  %s\t%s\t%s\t%s\n", op.ID, op.ChallengeID, op.Kind, op.Summary)
	}

	return b.String()
}
