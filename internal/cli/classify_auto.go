package cli

import (
	"context"
	"errors"

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
		// design-reviewer 指摘（round1 CONFIRMED）: CLAUDE.md の import の向きの
		// 規約「cli が invoker を import してよいのは…起動不能のエラー
		// （invoker.ErrClaudeNotFound）を CLI のエラーコードへ写すことだけ」に
		// 合わせ、ingest.go の github.ErrGHNotFound と同じ形で errors.Is
		// 判定する（Available() が返しうるのは現状 ErrClaudeNotFound だけだが、
		// 将来別のエラーを返すようになっても internal_error へ fail-closed に
		// 倒す）。
		if errors.Is(err, invoker.ErrClaudeNotFound) {
			return nil, NewError(CodeInvokerUnavailable, err.Error())
		}
		return nil, NewError(CodeInternalError, err.Error())
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
		json: map[string]any{"phase": judgmentPhaseJSON("classify", res)},
		text: judgmentPhaseText("分類の対象はありませんでした\n", res),
	}, nil
}
