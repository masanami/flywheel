package cli

import (
	"context"
	"errors"

	"github.com/masanami/flywheel/internal/adapters/git"
	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/invoker"
)

// runRun は `flywheel run [<C-ID>]` の実装（#102。docs/features/m3-invoker-delegation.md
// §J3 ブリーフと委譲の起動）。宣言の読み込み・invoker・上流の取得・git の組み立て・周の開始と
// 終了だけを行い、対象の選び方・意思決定者の判定・J3・スロットの割り当て・委譲の run の記録は
// core.Store.RunDelegation に委ねる（P2「CLI は core の公開 API だけを呼ぶ」）。
//
// 読み込み順（plan --auto と同じ形）: agent.json の読み込み・検証 → connectors.json の読み込み・
// 検証（無ければ config_not_found）→ claude の有無 → 周の開始（trigger "run"）→
// RunDelegation → EndCycle。どの段で失敗しても、それより後の段（claude の起動・上流の取得）は
// 行わない。
func runRun(a Args) (any, error) {
	ctx := context.Background()
	ws := a.Store.Workspace()

	decl, err := core.LoadAgentDeclaration(ws)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	conn, err := core.LoadConnectorsDeclaration(ws)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	launcher := invoker.NewLauncher()
	if err := launcher.Available(ctx); err != nil {
		if errors.Is(err, invoker.ErrClaudeNotFound) {
			return nil, NewError(CodeInvokerUnavailable, err.Error())
		}
		return nil, NewError(CodeInternalError, err.Error())
	}

	cyc, err := a.Store.BeginCycle(ctx, core.BeginCycleInput{
		Trigger:   "run",
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

	res, runErr := a.Store.RunDelegation(ctx, core.DelegateInput{
		ChallengeID: challengeID,
		AgentDecl:   decl,
		ConnDecl:    conn,
		Judgment:    launcher,
		Delegate:    launcher,
		Upstream:    newUpstreamThreadSource(),
		Git:         git.New(),
		CycleID:     cyc.ID,
	})

	endResult := core.CycleResultCompleted
	if runErr != nil {
		endResult = core.CycleResultAborted
	}
	// 周は起動できたかどうかに関わらず必ず終える（plan --auto と同じ）。
	if _, endErr := a.Store.EndCycle(ctx, cyc.ID, endResult); endErr != nil && runErr == nil {
		return nil, mapCoreErr(endErr)
	}
	if runErr != nil {
		return nil, mapCoreErr(runErr)
	}

	return textOutput{
		json: map[string]any{"phase": judgmentPhaseJSON("run", res)},
		text: judgmentPhaseText("委譲の対象はありませんでした\n", res),
	}, nil
}
