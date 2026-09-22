package cli

import (
	"context"
	"fmt"
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
	ov, err := a.Store.GetOverview(context.Background())
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{
		json: map[string]any{
			"needs_human": map[string]any{
				"challenges": challengesJSON(ov.NeedsHumanChallenges),
				"operations": operationsJSON(ov.NeedsHumanOperations),
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
func overviewText(ov *core.Overview) string {
	var b strings.Builder
	fmt.Fprintf(&b, "人間の操作を待っているもの:\n")
	if len(ov.NeedsHumanChallenges) == 0 && len(ov.NeedsHumanOperations) == 0 {
		fmt.Fprintf(&b, "  (なし)\n")
	}
	for _, c := range ov.NeedsHumanChallenges {
		label, _ := c.Status.Label()
		fmt.Fprintf(&b, "  %s\t%s\t%s\n", c.ID, label, c.Title)
	}
	for _, op := range ov.NeedsHumanOperations {
		fmt.Fprintf(&b, "  %s\t%s\t%s\t%s\n", op.ID, op.ChallengeID, op.Kind, op.Summary)
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
