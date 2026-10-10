package cli

import (
	"context"
	"fmt"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// runMarkRead は `flywheel mark-read <C-ID>` の実装。core.Store.MarkRead を
// 呼ぶだけで、拒否の判定（not_found・validation_failed・terminal_state）・
// 未読の更新の有無の判定は core が持つ（docs/features/m2-github-issue-ingest.md
// §IF / API「`flywheel mark-read <C-ID>`」）。
func runMarkRead(a Args) (any, error) {
	res, err := a.Store.MarkRead(context.Background(), core.ChannelCLI, a.Positional[0])
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{
		json: view.FromMarkReadResult(res),
		text: markReadText(res),
	}, nil
}

// markReadText は --json 無しの mark-read の表示（形式の安定は保証しない）。
func markReadText(res *core.MarkReadResult) string {
	return fmt.Sprintf("%s changed=%t\n", res.ChallengeID, res.Changed)
}
