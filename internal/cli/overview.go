package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/masanami/flywheel/internal/adapters/github"
	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// このファイルは `flywheel status`（Issue #14）の実コマンドを持つ。区分の判定
// （どの状態の課題・どの状態の不可逆操作がどの区分に属するか）は core.GetOverview
// が行い、ここでは core.Overview を JSON／テキストへ整形するだけ（親要件チケット
// #4「CLI は core の公開 API だけを呼ぶ。区分の判定を internal/cli に書かない」）。

// runStatus は `flywheel status` の実装。読み取り専用（core.GetOverview は状態・
// 作業ログを変えない）。
func runStatus(a Args) (any, error) {
	// 宣言は cycle と同じ読み込み・検査（core.CheckDeclarations）で確かめる。不備なら
	// config_invalid。agent.json はサイズの既定としても使う。
	decl, err := core.CheckDeclarations(a.Store.Workspace())
	if err != nil {
		return nil, mapCoreErr(err)
	}
	ctx := context.Background()
	ov, err := a.Store.GetOverviewFor(ctx, decl)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	// CI の完了を待っている課題は GitHub の現在の状態（GET だけ）から導く。検証中で PR を持つ課題が
	// 無ければ gh は呼ばれない。
	if ov.WaitingExternal, err = a.Store.ListWaitingExternal(ctx, newCheckSource(a.lookPath())); err != nil {
		return nil, mapCoreErr(err)
	}
	ov.BinaryCommit = binaryCommit(a)
	if ov.CycleWork, err = a.Store.CountCycleWorkInWorkspace(ctx, decl); err != nil {
		return nil, mapCoreErr(err)
	}
	if ov.Ingestable, err = statusIngestable(ctx, a); err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{
		json: view.FromOverview(ov),
		text: overviewText(ov),
	}, nil
}

// statusIngestable は取り込めるもの（ingestable）を読む。sources.json が無ければ items は空。上流を読めない
// （gh が無い等）ときは error に理由を入れ、status は失敗にしない。
func statusIngestable(ctx context.Context, a Args) (*core.Ingestable, error) {
	decl, err := core.LoadSourcesDeclaration(a.Store.Workspace())
	if err != nil {
		if errors.Is(err, core.ErrConfigNotFound) {
			return &core.Ingestable{Items: []core.IngestableItem{}}, nil
		}
		return nil, err
	}
	if len(decl.Sources) == 0 { // 取り込み元が無ければ上流の口を組み立てない（API と同じ。gh が無くても error にしない）
		return &core.Ingestable{Items: []core.IngestableItem{}}, nil
	}
	st, err := a.Store.ReadIngestableState(ctx)
	if err != nil {
		return nil, err
	}
	client, cerr := github.New(github.Options{Timeout: ingestGHTimeout, LookPath: a.lookPath()})
	if cerr != nil {
		return core.IngestableUnavailable(cerr), nil
	}
	return core.ResolveIngestable(ctx, st, decl.Sources, client), nil
}

// overviewText は --json 無しの status の表示。見出し「いま進められること」の下に、あなたを待っているもの
// （承認・回答・そのほかの操作）・取り込めるもの・サイクルを回すと進むもの・オーケストレーターの番・外を
// 待っているもの・承認済みの不可逆操作・未のフォローアップの件数を出す。区分の判定は core が行う。
func overviewText(ov *core.Overview) string {
	var b strings.Builder
	commit := ov.BinaryCommit
	if commit == "" {
		commit = core.UnknownBuildValue
	}
	fmt.Fprintf(&b, "バイナリのコミット: %s\n", commit)
	fmt.Fprintf(&b, "いま進められること\n")

	var approvals, answers []core.Challenge
	for _, c := range ov.NeedsHumanChallenges {
		if c.Status == core.StatusAwaitingHuman {
			answers = append(answers, c)
		} else {
			approvals = append(approvals, c)
		}
	}
	other := len(ov.NeedsHumanOperations) + len(ov.NeedsHumanTriage) + len(ov.NeedsHumanSlots) + len(ov.NeedsHumanBudgetExhausted) + len(ov.Discrepancies)
	ingestable := 0
	if ov.Ingestable != nil {
		ingestable = len(ov.Ingestable.Items)
	}
	cycleWork := 0
	if ov.CycleWork != nil {
		cycleWork = ov.CycleWork.Total
	}
	if len(approvals)+len(answers)+other+ingestable+cycleWork == 0 {
		fmt.Fprintf(&b, "いま進められるものはありません\n")
	}

	challengeSection := func(title string, cs []core.Challenge) {
		fmt.Fprintf(&b, "%s:\n", title)
		if len(cs) == 0 {
			fmt.Fprintf(&b, "  (なし)\n")
		}
		for _, c := range cs {
			label, _ := c.Status.Label()
			fmt.Fprintf(&b, "  %s\t%s\t%s\n", c.ID, label, c.Title)
		}
	}
	challengeSection("あなたの承認を待っているもの", approvals)
	challengeSection("あなたの回答を待っているもの", answers)

	if other > 0 {
		fmt.Fprintf(&b, "そのほかの、あなたの操作を待っているもの:\n")
		for _, op := range ov.NeedsHumanOperations {
			fmt.Fprintf(&b, "  %s\t%s\t%s\t%s\n", op.ID, op.ChallengeID, op.Kind, op.Summary)
		}
		for _, tr := range ov.NeedsHumanTriage {
			fmt.Fprintf(&b, "  %s\t仕分け\t%s\t%s\n", tr.ChallengeID, tr.RunID, tr.Reason)
		}
		for _, sl := range ov.NeedsHumanSlots {
			fmt.Fprintf(&b, "  %s\t要確認のスロット\t%s\t%s\t%s\n", sl.SlotID, sl.Repo, sl.Path, sl.Reason)
		}
		for _, be := range ov.NeedsHumanBudgetExhausted {
			fmt.Fprintf(&b, "  %s\t予算切れ\t計画 v%d\t実装枠の残り %s USD\tレビュー対応枠の残り %s USD\n",
				be.ChallengeID, be.PlanVersion, strconv.FormatFloat(be.ImplRemainingUSD, 'f', -1, 64), strconv.FormatFloat(be.ReviewRemainingUSD, 'f', -1, 64))
		}
		for _, d := range ov.Discrepancies {
			kinds := make([]string, 0, len(d.Kinds))
			for _, k := range d.Kinds {
				kinds = append(kinds, string(k))
			}
			fmt.Fprintf(&b, "  %s\t食い違い\t%s", d.ChallengeID, strings.Join(kinds, ","))
			if d.OperationID != "" {
				fmt.Fprintf(&b, "\t%s", d.OperationID)
			}
			if len(d.ChallengeIDs) > 0 {
				fmt.Fprintf(&b, "\t%s", strings.Join(d.ChallengeIDs, ","))
			}
			b.WriteString("\n")
		}
	}

	fmt.Fprintf(&b, "取り込めるもの:\n")
	if ov.Ingestable != nil && ov.Ingestable.Error != nil {
		fmt.Fprintf(&b, "  (読めませんでした: %s)\n", *ov.Ingestable.Error)
	}
	if ingestable == 0 && (ov.Ingestable == nil || ov.Ingestable.Error == nil) {
		fmt.Fprintf(&b, "  (なし)\n")
	}
	if ov.Ingestable != nil {
		for _, it := range ov.Ingestable.Items {
			fmt.Fprintf(&b, "  #%d\t%s", it.Number, it.Title)
			if len(it.Children) > 0 {
				fmt.Fprintf(&b, "\t子 %d 件", len(it.Children))
			}
			fmt.Fprintf(&b, "\t%s\n", it.ExternalKey)
		}
	}

	if cycleWork > 0 {
		fmt.Fprintf(&b, "サイクルを回すと進むもの: %d 件\n", cycleWork)
	} else {
		fmt.Fprintf(&b, "サイクルを回すと進むもの: (なし)\n")
	}

	challengeSection("オーケストレーターの番", ov.ParentAgentChallenges)

	fmt.Fprintf(&b, "外を待っているもの:\n")
	if len(ov.WaitingExternal) == 0 {
		fmt.Fprintf(&b, "  (なし)\n")
	}
	for _, w := range ov.WaitingExternal {
		fmt.Fprintf(&b, "  %s\tチェックの完了待ち\t%s\n", w.ChallengeID, w.PRURL)
	}

	fmt.Fprintf(&b, "承認済みの不可逆操作:\n")
	if len(ov.ApprovedOperations) == 0 {
		fmt.Fprintf(&b, "  (なし)\n")
	}
	for _, op := range ov.ApprovedOperations {
		fmt.Fprintf(&b, "  %s\t%s\t%s\t%s\n", op.ID, op.ChallengeID, op.Kind, op.Summary)
	}

	fmt.Fprintf(&b, "未のフォローアップ: %d 件\n", ov.FollowupsOpen)
	return b.String()
}
