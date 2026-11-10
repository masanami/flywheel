package core

// このファイルは、完了確認で人間が差し戻した理由（遷移 T15）を、次の J3・委譲の再開・J5 の
// 入力へ渡すための収集と整形を持つ。理由は approval の行（kind=completion・decision=rejected）の
// reason にあり、`--relayed` で記録した差し戻しでも同じ（relay_quote は会話の手がかりで、理由ではない）。

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// CompletionRejection は完了確認での差し戻し 1 件（人間が書いた理由と、差し戻した日時）。
type CompletionRejection struct {
	DecidedAt time.Time
	Reason    string
}

// loadCompletionRejections は課題の完了確認での差し戻しを、古い順（記録した順）に返す。
// 計画の承認での差し戻し・完了の承認は含めない。
func loadCompletionRejections(ctx context.Context, tx *sql.Tx, challengeID int64) ([]CompletionRejection, error) {
	approvals, err := loadApprovals(ctx, tx, challengeID)
	if err != nil {
		return nil, err
	}
	var out []CompletionRejection
	for _, a := range approvals {
		if a.Kind != ApprovalKindCompletion || a.Decision != ApprovalDecisionRejected || a.Reason == nil {
			continue
		}
		if reason := strings.TrimSpace(*a.Reason); reason != "" {
			out = append(out, CompletionRejection{DecidedAt: a.DecidedAt, Reason: reason})
		}
	}
	return out, nil
}

// CompletionRejectionsSectionLabel は、差し戻しの理由を判断点の入力・委譲の標準入力へ入れるときの区画の名前
// （J3・J5 の指示文がこの名前で区画を指す）。
const CompletionRejectionsSectionLabel = "人間の差し戻し"

// formatCompletionRejectionsSection は差し戻しの理由を、日付つきで古い順のテキストへ整形する。
// 差し戻しが無ければ空文字列を返す（呼び出し側はこの場合に区画を作らない）。
func formatCompletionRejectionsSection(rs []CompletionRejection) string {
	if len(rs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("人間が成果物を確認して差し戻した理由。元の達成条件に足した、満たすべき条件である。古い順に並べている。" +
		"後の差し戻しが前の差し戻しの方針を置き換えているときは、後のものに従う。")
	for i, r := range rs {
		fmt.Fprintf(&b, "\n\n%d. %s\n%s", i+1, r.DecidedAt.UTC().Format("2006-01-02"), r.Reason)
	}
	return b.String()
}
