package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// runRuns は `flywheel runs [<C-ID>] [--open]` の実装
// （docs/features/m3-invoker-delegation.md §観測・§IF / API「runs」）。
// core.Store.ListRuns を呼ぶだけで、対象の選び方・回収の規則は core が持つ
// （P2）。
func runRuns(a Args) (any, error) {
	var challengeID *string
	if len(a.Positional) == 1 {
		challengeID = &a.Positional[0]
	}
	runs, err := a.Store.ListRuns(context.Background(), core.RunListOptions{
		ChallengeID: challengeID,
		OpenOnly:    a.Bools["open"],
	})
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{
		json: view.RunsResponse{Runs: view.FromRuns(runs)},
		text: runsText(runs),
	}, nil
}

// runsText は --json 無しの runs の表示（形式の安定は保証しない）。
func runsText(runs []core.Run) string {
	if len(runs) == 0 {
		return "run はありません\n"
	}
	var b strings.Builder
	for _, r := range runs {
		result := r.Result
		if result == "" {
			result = "(実行中)"
		}
		fmt.Fprintf(&b, "%s %s %s challenge=%s result=%s rate_limited=%t\n", r.ID, r.Kind, r.Judgment, r.ChallengeID, result, r.RateLimited)
	}
	return b.String()
}
