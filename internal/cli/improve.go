package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/masanami/flywheel/internal/adapters/git"
	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/invoker"
	"github.com/masanami/flywheel/internal/view"
)

// runImprove は `flywheel improve [--json]` の実装（J8。docs/features/
// m4.5-learning-and-self-improvement.md §IF / API）。宣言の読み込み・invoker の組み立て・
// 周の開始と終了だけを行い、起動の規則・入力の組み立て・出力の検査と写像・改修課題の起票は
// core.Store.ImproveJ8 に委ねる（CLI は core の公開 API だけを呼ぶ）。
//
// 読み込み順（learn と同じ形）: agent.json の読み込み・検証 → connectors.json（無くても動く。
// 不備なら config_invalid）→ claude の有無 → 周の開始（BeginCycle trigger "improve"）→ ImproveJ8 → EndCycle。
// 承認を作らないので端末は求めない。connectors.json があるときだけ git.New() を組み立てて core へ渡す
// （knowledge の層の経路の判定に使う）。
func runImprove(a Args) (any, error) {
	ctx := context.Background()
	ws := a.Store.Workspace()

	decl, err := core.LoadAgentDeclaration(ws)
	if err != nil {
		return nil, mapCoreErr(err)
	}

	var conn *core.ConnectorsDeclaration
	var slotGit core.SlotGit
	connDecl, err := core.LoadConnectorsDeclaration(ws)
	switch {
	case err == nil:
		conn = connDecl
		slotGit = git.New()
	case errors.Is(err, core.ErrConfigNotFound):
		// connectors.json が無い: 成果物の経路はどの層も Issue（J8 は呼ぶ）。
	default:
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
		Trigger:   "improve",
		BudgetUSD: decl.CycleBudgetUSD,
		Exclusive: false,
	})
	if err != nil {
		return nil, mapCoreErr(err)
	}

	res, runErr := a.Store.ImproveJ8(ctx, core.ImproveJ8Input{
		Manual:    true,
		AgentDecl: decl,
		ConnDecl:  conn,
		Invoker:   launcher,
		SlotGit:   slotGit,
		CycleID:   cyc.ID,
	})

	endResult := core.CycleResultCompleted
	if runErr != nil {
		endResult = core.CycleResultAborted
	}
	// 周は起動できたかどうかに関わらず必ず終える（learn と同じ）。
	if _, endErr := a.Store.EndCycle(ctx, cyc.ID, endResult); endErr != nil && runErr == nil {
		return nil, mapCoreErr(endErr)
	}
	if runErr != nil {
		return nil, mapCoreErr(runErr)
	}

	return textOutput{
		json: view.PhaseResponse{Phase: view.FromImprovePhase(res.ImproveItems(), res.NotStarted)},
		text: improveText(res),
	}, nil
}

// improveText は --json 無しの `improve` の表示（形式の安定は保証しない）。
func improveText(res *core.ImproveJ8Result) string {
	if !res.Called {
		if len(res.NotStarted) == 0 {
			return fmt.Sprintf("改修の提案は呼びませんでした（起動の件数 %d）\n", res.Triggering)
		}
		reasons := make([]string, 0, len(res.NotStarted))
		for _, ns := range res.NotStarted {
			r := string(ns.Reason)
			if ns.Detail != "" {
				r += "（" + ns.Detail + "）"
			}
			reasons = append(reasons, r)
		}
		return fmt.Sprintf("改修の提案は呼びませんでした（起動の件数 %d・理由: %s）\n", res.Triggering, strings.Join(reasons, ", "))
	}
	s := fmt.Sprintf("%s\t%s", res.RunID, res.Result)
	if len(res.Filed) > 0 {
		s += "\t起票: " + strings.Join(res.Filed, ",")
	}
	if len(res.Linked) > 0 {
		s += "\t紐づけ: " + strings.Join(res.Linked, ",")
	}
	if res.Note != "" {
		s += "\t" + res.Note
	}
	return s + "\n"
}
