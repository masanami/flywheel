package cli

import (
	"context"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/invoker"
)

// runClassifyAuto は `flywheel classify --auto [<C-ID>]` の実装
// （#84。docs/features/m3-invoker-delegation.md §J1 分類）。宣言の読み込み・
// invoker の組み立て・周の開始と終了だけを行い、対象の選び方・出力の写像・
// 予算の評価は core.Store.ClassifyAutoJ1 に委ねる（P2「CLI は core の公開 API
// だけを呼ぶ」）。
//
// 読み込み順（技術的な指示「宣言の読み込み順」）: agent.json の読み込み・
// 検証 → RequirePositionFile → claude の有無 → 周の開始
// （BeginCycle trigger "classify --auto"）→ ClassifyAutoJ1 → EndCycle。
func runClassifyAuto(a Args) (any, error) {
	ctx := context.Background()
	ws := a.Store.Workspace()

	decl, err := core.LoadAgentDeclaration(ws)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	if err := decl.RequirePositionFile(ws); err != nil {
		return nil, mapCoreErr(err)
	}

	launcher := invoker.NewLauncher()
	if err := launcher.Available(ctx); err != nil {
		return nil, NewError(CodeInvokerUnavailable, err.Error())
	}

	cyc, err := a.Store.BeginCycle(ctx, core.BeginCycleInput{
		Trigger:   "classify --auto",
		BudgetUSD: decl.CycleBudgetUSD,
		Exclusive: false,
	})
	if err != nil {
		return nil, mapCoreErr(err)
	}

	var challengeID *string
	if len(a.Positional) == 1 {
		challengeID = &a.Positional[0]
	}

	res, runErr := a.Store.ClassifyAutoJ1(ctx, core.J1AutoInput{
		ChallengeID: challengeID,
		AgentDecl:   decl,
		Invoker:     launcher,
		CycleID:     cyc.ID,
	})

	endResult := core.CycleResultCompleted
	if runErr != nil {
		endResult = core.CycleResultAborted
	}
	// 周は起動できたかどうかに関わらず必ず終える（§一括の操作（サイクル）
	// 「ロックの取得…ロックの解放」と同じく、開始した周を終えずに残さない。
	// classify --auto は排他ロックを取らないが、cycle 行と予約額の後始末は
	// 必要）。EndCycle 自体の失敗は、runErr が無ければそれを返す。
	if _, endErr := a.Store.EndCycle(ctx, cyc.ID, endResult); endErr != nil && runErr == nil {
		return nil, mapCoreErr(endErr)
	}
	if runErr != nil {
		return nil, mapCoreErr(runErr)
	}

	return textOutput{
		json: map[string]any{"phase": j1PhaseJSON(res)},
		text: j1PhaseText(res),
	}, nil
}

// j1PhaseJSON は J1AutoResult を §IF / API「`--auto` の個別の操作…は cycle の
// phases の1要素と同じ形を {"phase": {…}} で返す」の中身へ変換する。
func j1PhaseJSON(res *core.J1AutoResult) map[string]any {
	return map[string]any{
		"phase":       "classify",
		"skipped":     false,
		"items":       j1ItemsJSON(res.Items),
		"not_started": notStartedJSON(res.NotStarted),
	}
}

func j1ItemsJSON(items []core.J1AutoItem) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{
			"challenge_id": it.ChallengeID,
			"run_id":       it.RunID,
			"result":       string(it.Result),
			"outcome":      nullableString(it.Outcome),
			"status":       nullableStringPtr(it.Status),
		})
	}
	return out
}

func notStartedJSON(items []core.NotStarted) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{
			"challenge_id": it.ChallengeID,
			"reason":       string(it.Reason),
		})
	}
	return out
}

// j1PhaseText は --json 無しの classify --auto の表示（形式の安定は保証しない）。
func j1PhaseText(res *core.J1AutoResult) string {
	if len(res.Items) == 0 && len(res.NotStarted) == 0 {
		return "分類の対象はありませんでした\n"
	}
	s := ""
	for _, it := range res.Items {
		s += it.ChallengeID + "\t" + it.RunID + "\t" + string(it.Result) + "\t" + it.Outcome + "\n"
	}
	for _, ns := range res.NotStarted {
		s += ns.ChallengeID + "\t(未起動)\t" + string(ns.Reason) + "\n"
	}
	return s
}
