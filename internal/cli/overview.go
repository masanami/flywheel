package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// このファイルは `flywheel status`（Issue #14）の実コマンドを持つ。区分の判定
// （どの状態の課題・どの状態の不可逆操作がどの区分に属するか）は core.GetOverview
// が行い、ここでは core.Overview を JSON／テキストへ整形するだけ（親要件チケット
// #4「CLI は core の公開 API だけを呼ぶ。区分の判定を internal/cli に書かない」）。

// runStatus は `flywheel status` の実装。読み取り専用（core.GetOverview は状態・
// 作業ログを変えない）。
func runStatus(a Args) (any, error) {
	// 宣言は cycle と同じ読み込み・検査（core.CheckDeclarations）で確かめる。不備なら
	// config_invalid。agent.json はサイズの既定としても使う。
	decl, err := core.CheckDeclarations(a.Store.Workspace())
	if err != nil {
		return nil, mapCoreErr(err)
	}
	ctx := context.Background()
	ov, err := a.Store.GetOverviewFor(ctx, decl)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	// CI の完了を待っている課題は GitHub の現在の状態（GET だけ）から導く。検証中で PR を持つ課題が
	// 無ければ gh は呼ばれない。
	if ov.WaitingExternal, err = a.Store.ListWaitingExternal(ctx, newCheckSource(a.lookPath())); err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{
		json: view.FromOverview(ov),
		text: overviewText(ov),
	}, nil
}

// overviewText は --json 無しの status の表示。区分を見出しつきで分けて示す。
// 食い違い（needs_human.discrepancies）は JSON と同じく「人間の操作を待っている
// もの」の中に出す（docs/features/m2-github-issue-ingest.md §食い違いの表示）。
func overviewText(ov *core.Overview) string {
	var b strings.Builder
	commit := core.UnknownBuildValue
	if v, err := core.ReadVersion(); err == nil {
		commit = commitText(v)
	}
	fmt.Fprintf(&b, "バイナリのコミット: %s\n", commit)
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
		fmt.Fprintf(&b, "  %s\t食い違い\t%s", d.ChallengeID, strings.Join(kinds, ","))
		if d.OperationID != "" {
			fmt.Fprintf(&b, "\t%s", d.OperationID)
		}
		if len(d.ChallengeIDs) > 0 {
			fmt.Fprintf(&b, "\t%s", strings.Join(d.ChallengeIDs, ","))
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "システムが次に進められるもの:\n")
	if len(ov.ActionableChallenges) == 0 {
		fmt.Fprintf(&b, "  (なし)\n")
	}
	for _, c := range ov.ActionableChallenges {
		label, _ := c.Status.Label()
		fmt.Fprintf(&b, "  %s\t%s\t%s", c.ID, label, c.Title)
		if unmet := ov.UnmetDependencies[c.ID]; len(unmet) > 0 {
			ids := make([]string, 0, len(unmet))
			for _, u := range unmet {
				ids = append(ids, u.ChallengeID)
			}
			fmt.Fprintf(&b, "\t満たされていない依存先: %s", strings.Join(ids, ","))
		}
		b.WriteString("\n")
	}

	// 親のエージェントが動く番（課題 #452）。人間の操作を待っているものとは分けて示す。
	fmt.Fprintf(&b, "親のエージェントが動く番:\n")
	if len(ov.ParentAgentChallenges) == 0 {
		fmt.Fprintf(&b, "  (なし)\n")
	}
	for _, c := range ov.ParentAgentChallenges {
		label, _ := c.Status.Label()
		fmt.Fprintf(&b, "  %s\t%s\t%s\n", c.ID, label, c.Title)
	}

	fmt.Fprintf(&b, "外部を待っているもの:\n")
	if len(ov.WaitingExternal) == 0 {
		fmt.Fprintf(&b, "  (なし)\n")
	}
	for _, w := range ov.WaitingExternal {
		fmt.Fprintf(&b, "  %s\tチェックの完了待ち\t%s\n", w.ChallengeID, w.PRURL)
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
