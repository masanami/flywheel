package cli

import (
	"context"
	"errors"

	"github.com/masanami/flywheel/internal/adapters/git"
	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/invoker"
	"github.com/masanami/flywheel/internal/view"
)

// runLockedMessage は、周（または別の run）がサイクルの排他を持っているため run を断ったときの表示。
// 理由（周が実行中）と次にすること（周が終わってから打ち直す・その課題は周に任せる）を示す。
const runLockedMessage = "周（flywheel cycle）または別の flywheel run が実行中のため、run を断りました（スロットの作業ツリーには触れていません）。" +
	"次にすること: 周が終わってから flywheel run を打ち直すか、その課題の委譲は周に任せてください。"

// runRun は `flywheel run [<C-ID>]` の実装（#102。docs/features/m3-invoker-delegation.md
// §J3 ブリーフと委譲の起動）。宣言の読み込み・invoker・上流の取得・git の組み立て・周の開始と
// 終了だけを行い、対象の選び方・意思決定者の判定・J3・スロットの割り当て・委譲の run の記録は
// core.Store.RunDelegation に委ねる（P2「CLI は core の公開 API だけを呼ぶ」）。
//
// run は周（`flywheel cycle`）と同じサイクルの排他を取る（周が動いていれば待たず locked で断り、
// スロットの作業ツリーには触れない。run の間は周も断られる）。個別の操作で排他を取るのは run だけ。
//
// 読み込み順（plan --auto と同じ形）: agent.json の読み込み・検証 → connectors.json の読み込み・
// 検証（無ければ config_not_found）→ claude の有無 → 周の開始（trigger "run"）→
// RunDelegation → EndCycle。どの段で失敗しても、それより後の段（claude の起動・上流の取得）は
// 行わない。
func runRun(a Args) (any, error) {
	ctx, stop := interruptContext()
	defer stop()
	ws := a.Store.Workspace()

	decl, err := core.LoadAgentDeclaration(ws)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	conn, err := core.LoadConnectorsDeclaration(ws)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	launcher := invoker.NewLauncherWithLookPath(a.LookPath)
	if err := launcher.Available(ctx); err != nil {
		if errors.Is(err, invoker.ErrClaudeNotFound) {
			return nil, NewError(CodeInvokerUnavailable, err.Error())
		}
		return nil, NewError(CodeInternalError, err.Error())
	}

	cyc, err := a.Store.BeginCycle(ctx, core.BeginCycleInput{
		Trigger:   "run",
		BudgetUSD: decl.CycleBudgetUSD,
		Exclusive: true,
	})
	if errors.Is(err, core.ErrLocked) {
		// 断った理由と次にすることを示す（コードは locked のまま）。
		return nil, NewError(CodeLocked, runLockedMessage)
	}
	if err != nil {
		return nil, mapCoreErr(err)
	}
	// 周と同じく、run の間はロックの heartbeat を更新し続ける（止めないと stale として回収される）。
	stopHeartbeat := a.Store.KeepCycleLock(ctx, cyc.ID)

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
		Contract:    launcher,
		Upstream:    newUpstreamThreadSource(a.lookPath()),
		Git:         git.New(),
		Reconcile:   newBranchSource(a.lookPath()),
		Predictor:   launcher,
		CycleID:     cyc.ID,
	})

	endResult := core.CycleResultCompleted
	if runErr != nil {
		endResult = core.CycleResultAborted
	}
	stopHeartbeat()
	// 中断（SIGINT・SIGTERM）で ctx が取り消されても、周の終了と排他の解放は必ず行う。
	endCtx := context.WithoutCancel(ctx)
	// 周は起動できたかどうかに関わらず必ず終える（plan --auto と同じ）。
	if _, endErr := a.Store.EndCycle(endCtx, cyc.ID, endResult); endErr != nil && runErr == nil {
		return nil, mapCoreErr(endErr)
	}
	if runErr != nil {
		return nil, mapCoreErr(runErr)
	}

	return textOutput{
		json: view.PhaseResponse{Phase: view.FromDelegationPhase(false, res)},
		text: judgmentPhaseText("委譲の対象はありませんでした\n", res),
	}, nil
}
