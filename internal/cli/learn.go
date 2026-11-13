package cli

import (
	"context"
	"errors"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/invoker"
	"github.com/masanami/flywheel/internal/view"
)

// runLearn は `flywheel learn [<C-ID>] [--json]` の実装（J6。docs/features/
// m4.5-learning-and-self-improvement.md §IF / API）。宣言の読み込み・invoker の組み立て・
// 周の開始と終了だけを行い、対象の選び方・入力・出力の検査と写像・記憶の書き込みは
// core.Store.LearnJ6 に委ねる（CLI は core の公開 API だけを呼ぶ）。
//
// 読み込み順（verify --auto と同じ形）: agent.json の読み込み・検証 → claude の有無 → 周の開始
// （BeginCycle trigger "learn"）→ LearnJ6 → EndCycle。接続ツールの宣言（connectors.json）は
// 使わないので、無くても動く。
func runLearn(a Args) (any, error) {
	ctx := context.Background()
	ws := a.Store.Workspace()

	decl, err := core.LoadAgentDeclaration(ws)
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
		Trigger:   "learn",
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

	res, runErr := a.Store.LearnJ6(ctx, core.J6AutoInput{
		ChallengeID: challengeID,
		AgentDecl:   decl,
		Invoker:     launcher,
		CycleID:     cyc.ID,
	})

	endResult := core.CycleResultCompleted
	if runErr != nil {
		endResult = core.CycleResultAborted
	}
	// 周は起動できたかどうかに関わらず必ず終える（verify --auto と同じ）。
	if _, endErr := a.Store.EndCycle(ctx, cyc.ID, endResult); endErr != nil && runErr == nil {
		return nil, mapCoreErr(endErr)
	}
	if runErr != nil {
		return nil, mapCoreErr(runErr)
	}

	return textOutput{
		json: view.PhaseResponse{Phase: view.FromJudgmentPhase(string(core.CyclePhaseLearn), false, res)},
		text: judgmentPhaseText("学習の対象はありませんでした\n", res),
	}, nil
}
