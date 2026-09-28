package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/masanami/flywheel/internal/core"
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
		json: map[string]any{"runs": runsJSON(runs)},
		text: runsText(runs),
	}, nil
}

// runsJSON は run の一覧を §IF / API「runs」の形（要素ごと）へ変換する:
// {"runs": [{"id", "kind", "judgment", "challenge_id", "cycle_id",
// "cycle_budget_usd", "session_id", "result", "rate_limited", "cost_usd",
// "cost_source", "max_budget_usd", "started_at", "ended_at"}]}
func runsJSON(runs []core.Run) []map[string]any {
	out := make([]map[string]any, 0, len(runs))
	for _, r := range runs {
		out = append(out, runEntryJSON(r))
	}
	return out
}

// runEntryJSON は run.go(テスト)の runJSON（`flywheel <args>` を実行して
// stdoutのJSONを返すテストヘルパー）と名前が衝突しないよう別名にする。
func runEntryJSON(r core.Run) map[string]any {
	return map[string]any{
		"id":               r.ID,
		"kind":             string(r.Kind),
		"judgment":         nullableString(string(r.Judgment)),
		"challenge_id":     r.ChallengeID,
		"cycle_id":         nullableStringPtr(r.CycleID),
		"cycle_budget_usd": nullableFloatPtr(r.CycleBudgetUSD),
		"session_id":       r.SessionID,
		"result":           nullableString(string(r.Result)),
		"rate_limited":     r.RateLimited,
		"cost_usd":         nullableFloatPtr(r.CostUSD),
		"cost_source":      nullableString(string(r.CostSource)),
		"max_budget_usd":   r.MaxBudgetUSD,
		"started_at":       FormatTimestamp(r.StartedAt),
		"ended_at":         nullableTimePtr(r.EndedAt),
	}
}

// nullableFloatPtr は p が nil なら null、そうでなければ *p を返す。
func nullableFloatPtr(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// nullableTimePtr は p が nil なら null、そうでなければ FormatTimestamp(*p) を
// 返す。
func nullableTimePtr(p *time.Time) any {
	if p == nil {
		return nil
	}
	return FormatTimestamp(*p)
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
