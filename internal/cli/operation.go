package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/masanami/flywheel/internal/adapters/git"
	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/invoker"
	"github.com/masanami/flywheel/internal/view"
)

// このファイルは不可逆操作の登録 `op add` の実コマンドを持つ（Issue #13）。
// 単独の承認・差し戻し（approve <OP-ID> / reject <OP-ID>）は、本人確認を
// 要する経路のため internal/cli/approval.go に置く（approve・reject <C-ID> と
// 同じ「PrepareXxx を呼ぶ→要約を組む→core.Verify→ExecuteXxx」の形を共有する）。

// runOpAdd は `flywheel op add <C-ID> --kind <k> --summary <s> [--ref <r>]` の
// 実装。閉集合の検査・terminal_state の判定・課題の版の増加・作業ログの記録は
// core が持つ（core.CreateOperation）。
func runOpAdd(a Args) (any, error) {
	id := a.Positional[0]
	var ref *string
	if v, ok := a.Values["ref"]; ok {
		ref = &v
	}

	op, err := a.Store.CreateOperation(context.Background(), core.ChannelCLI, core.OperationInput{
		ChallengeID: id,
		Kind:        a.Values["kind"],
		Summary:     a.Values["summary"],
		Ref:         ref,
	})
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{
		json: view.OperationResponse{Operation: view.FromOperation(*op)},
		text: op.ID + "\n",
	}, nil
}

// runOpExec は `flywheel op exec [<OP-ID>] [--json]` の実装（docs/features/m4.5-learning-and-self-improvement.md
// §承認済みの release の実行）。宣言の読み込み・gh の組み立て・周の開始と終了だけを行い、
// 対象の選び方・成果物の確認と道の選択・試行の記録は core.Store.ExecuteReleases に委ねる（反映先へは書かない。
// 対応する契約の実行へ「入れてよい」を返す契約のコマンドの起動の口だけを組み立てて渡す）。
// 承認を作らないので端末での確認を求めず、claude も起動しない。
//
// 読み込み順: agent.json の読み込み・検証 → connectors.json の読み込み・検証（無ければ
// config_not_found、無くした項目が残っていれば config_invalid）→ 周の開始（trigger "op exec"。
// 排他ロックは取らない）→ ExecuteReleases → EndCycle。
func runOpExec(a Args) (any, error) {
	// フルの検査は別のプロセスグループで長く走るので、SIGINT・SIGTERM は ctx の取り消しにして、検査を止めてから返る。
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

	var ids []string
	if len(a.Positional) == 1 {
		ids = []string{a.Positional[0]}
	}

	cyc, err := a.Store.BeginCycle(ctx, core.BeginCycleInput{
		Trigger:   core.ReleaseExecTriggerOpExec,
		BudgetUSD: decl.CycleBudgetUSD,
		Exclusive: false,
	})
	if err != nil {
		return nil, mapCoreErr(err)
	}

	res, runErr := a.Store.ExecuteReleases(ctx, ids, core.ReleaseExecOptions{
		Checks:     newCheckSource(a.lookPath()),
		Compare:    newCompareSource(a.lookPath()),
		Connectors: conn,
		Agent:      decl,
		// 契約のコマンド（status・resume）の起動。claude は起動しない。
		Contract: invoker.NewLauncherWithLookPath(a.LookPath),
		// 統合ブランチを反映先へ上げる前のフルの検査（宣言の full_check）の起動と、検査する中身を取り出す口。
		FullCheck: invoker.NewLauncherWithLookPath(a.LookPath),
		HeadGit:   git.New(),
		Trigger:   core.ReleaseExecTriggerOpExec,
		CycleID:   cyc.ID,
	})

	endResult := core.CycleResultCompleted
	if runErr != nil {
		endResult = core.CycleResultAborted
	}
	// 周は必ず終える（run と同じ）。
	if _, endErr := a.Store.EndCycle(context.WithoutCancel(ctx), cyc.ID, endResult); endErr != nil && runErr == nil {
		return nil, mapCoreErr(endErr)
	}
	if runErr != nil {
		var items []core.ReleaseExecItem
		if res != nil {
			items = res.Items
		}
		return nil, withExecutedReleases(mapCoreErr(runErr), items)
	}

	return textOutput{
		json: view.PhaseResponse{Phase: view.FromReleasePhase(false, res.Items, res.NotStarted, res.Tidied)},
		text: releasePhaseText(res, a.Chat != nil),
	}, nil
}

// withExecutedReleases は、release の段が途中でエラーになったときに、それまでに実行済みの結果を
// エラーの文言へ残す（マージ済みの release がエラーの陰に隠れないように）。
func withExecutedReleases(e *Error, items []core.ReleaseExecItem) *Error {
	if e == nil || len(items) == 0 {
		return e
	}
	parts := make([]string, 0, len(items))
	for _, it := range items {
		p := it.OperationID + " " + string(it.Result)
		if it.MergeCommit != "" {
			p += " " + it.MergeCommit
		}
		parts = append(parts, p)
	}
	e.Message += "（途中までの結果: " + strings.Join(parts, "、") + "）"
	return e
}

// releasePhaseText は --json 無しの `op exec` の表示（形式の安定は保証しない）。
func releasePhaseText(res *core.ReleaseExecResult, chat bool) string {
	if len(res.Items) == 0 && len(res.NotStarted) == 0 && len(res.Tidied) == 0 {
		return "実行の対象はありませんでした\n"
	}
	var b strings.Builder
	for _, t := range res.Tidied {
		switch t.Action {
		case core.ReleaseTidyReopened:
			fmt.Fprintf(&b, "%s\t%s\t(立て直し)\t課題を完了から検証中へ戻し、%s に置き換えた（%s）\n", t.OperationID, t.ChallengeID, t.NewOperationID, t.ReopenReason)
		case core.ReleaseTidyNeedsHuman:
			if chat {
				fmt.Fprintf(&b, "%s\t%s\t(人間待ち)\t取り込みだけとも中身の変更とも確かめられません（%s）。承認するか、検証中へ戻すか（戻すのはターミナルで行ってください。対話の欄からは戻せません）を選んでください\n", t.OperationID, t.ChallengeID, escapeControl(t.WaitReason))
			} else {
				fmt.Fprintf(&b, "%s\t%s\t(人間待ち)\t取り込みだけとも中身の変更とも確かめられません（%s）。承認するか、approve %s --reopen で検証中へ戻すかを選んでください\n", t.OperationID, t.ChallengeID, escapeControl(t.WaitReason), t.OperationID)
			}
		case core.ReleaseTidySkipped:
			fmt.Fprintf(&b, "%s\t%s\t(飛ばした)\t課題を検証中へ戻せませんでした（%s）。次の反映の段でやり直します\n", t.OperationID, t.ChallengeID, escapeControl(t.WaitReason))
		default:
			fmt.Fprintf(&b, "%s\t%s\t(閉じた)\t%s", t.OperationID, t.ChallengeID, t.ClosedReason)
			if t.CancelNote != "" {
				fmt.Fprintf(&b, "（委譲先の実行を取り消せませんでした: %s）", escapeControl(t.CancelNote))
			}
			b.WriteString("\n")
		}
	}
	for _, it := range res.Items {
		fmt.Fprintf(&b, "%s\t%s\t%s", it.OperationID, it.ChallengeID, it.Result)
		if it.MergeCommit != "" {
			b.WriteString("\t" + it.MergeCommit)
		}
		b.WriteString("\n")
	}
	for _, ns := range res.NotStarted {
		fmt.Fprintf(&b, "%s\t%s\t(未起動)\t%s", ns.OperationID, ns.ChallengeID, ns.Reason)
		if ns.Detail != "" {
			b.WriteString("\t" + ns.Detail)
		}
		b.WriteString("\n")
	}
	return b.String()
}
