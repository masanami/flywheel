package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/masanami/flywheel/internal/adapters/github"
	"github.com/masanami/flywheel/internal/core"
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
// 対象の選び方・判定の順・マージの依頼・試行の記録は core.Store.ExecuteReleases に委ねる。
// 承認を作らないので端末での確認を求めず、claude も起動しない。
//
// 読み込み順: agent.json の読み込み・検証 → connectors.json の読み込み・検証（無ければ
// config_not_found、merge_method が閉集合の外なら config_invalid）→ 周の開始（trigger "op exec"。
// 排他ロックは取らない）→ ExecuteReleases → EndCycle。
func runOpExec(a Args) (any, error) {
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
		Checks:     newCheckSource(),
		Merger:     newPullRequestMerger(),
		Connectors: conn,
		Agent:      decl,
		Trigger:    core.ReleaseExecTriggerOpExec,
		CycleID:    cyc.ID,
	})

	endResult := core.CycleResultCompleted
	if runErr != nil {
		endResult = core.CycleResultAborted
	}
	// 周は必ず終える（run と同じ）。
	if _, endErr := a.Store.EndCycle(ctx, cyc.ID, endResult); endErr != nil && runErr == nil {
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
		json: view.PhaseResponse{Phase: view.FromReleasePhase(false, res.Items, res.NotStarted)},
		text: releasePhaseText(res),
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
func releasePhaseText(res *core.ReleaseExecResult) string {
	if len(res.Items) == 0 && len(res.NotStarted) == 0 {
		return "実行の対象はありませんでした\n"
	}
	var b strings.Builder
	for _, it := range res.Items {
		fmt.Fprintf(&b, "%s\t%s\t%s", it.OperationID, it.ChallengeID, it.Result)
		if it.MergeCommit != "" {
			b.WriteString("\t" + it.MergeCommit)
		}
		if it.Detail != "" {
			b.WriteString("\t" + it.Detail)
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

// newPullRequestMerger はマージの依頼（gh pr merge）を組み立てる。gh が PATH に無いときは、
// 依頼が失敗する実装を返す（依頼まで到達するのは、読み取りが成功した後だけ）。
func newPullRequestMerger() core.UpstreamPullRequestMerger {
	client, err := github.New(github.Options{Timeout: ingestGHTimeout})
	if err != nil {
		return unavailableMerger{err: err}
	}
	return client
}

type unavailableMerger struct{ err error }

func (u unavailableMerger) MergePullRequest(context.Context, string, string, string) error {
	return fmt.Errorf("上流へ依頼できません: %w", u.err)
}
