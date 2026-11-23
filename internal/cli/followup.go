package cli

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// runFollowupList は `flywheel followup list [--status open|done|dropped|all] [--challenge <C-ID>]` の実装。
// 既定の状態は open。絞り込みの規則は core.ListFollowups が持つ（閉集合外の状態は usage_error、
// 存在しない課題は not_found に写す）。
func runFollowupList(a Args) (any, error) {
	status := "open"
	if v, ok := a.Values["status"]; ok {
		status = strings.TrimSpace(v)
		if status != "all" {
			if _, ok := core.ParseFollowupStatus(status); !ok {
				return nil, NewError(CodeUsageError, "--status は open・done・dropped・all のいずれかを指定してください")
			}
		}
	}
	fs, err := a.Store.ListFollowups(context.Background(), core.FollowupFilter{Status: status, ChallengeID: a.Values["challenge"]})
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: view.FromFollowupList(fs), text: followupListText(fs)}, nil
}

// followupListText は --json 無しの followup list の表示。課題ごとにまとめ（課題の ID の数の昇順）、
// 課題に属さないものを最後に出す。0 件なら何も出さない。
func followupListText(fs []core.Followup) string {
	var order []string
	groups := map[string][]core.Followup{}
	var orphans []core.Followup
	for _, f := range fs {
		if f.ChallengeID == nil {
			orphans = append(orphans, f)
			continue
		}
		if _, ok := groups[*f.ChallengeID]; !ok {
			order = append(order, *f.ChallengeID)
		}
		groups[*f.ChallengeID] = append(groups[*f.ChallengeID], f)
	}
	sortChallengeIDs(order)
	var b strings.Builder
	for _, cid := range order {
		fmt.Fprintf(&b, "%s\n", cid)
		for _, f := range groups[cid] {
			writeFollowupLine(&b, f)
		}
	}
	if len(orphans) > 0 {
		b.WriteString("課題に属さない\n")
		for _, f := range orphans {
			writeFollowupLine(&b, f)
		}
	}
	return b.String()
}

func writeFollowupLine(b *strings.Builder, f core.Followup) {
	fmt.Fprintf(b, "  %s\t%s\t%s", f.ID, f.Status, escapeControl(f.Title))
	// 上げた先は、タスクの列に値があればタスク、無ければ課題。
	switch {
	case f.PromotedTaskID != nil && f.PromotedTaskTitle != nil:
		fmt.Fprintf(b, "\t（タスク %s「%s」に上げた）", *f.PromotedTaskID, escapeControl(*f.PromotedTaskTitle))
	case f.PromotedTaskID != nil:
		fmt.Fprintf(b, "\t（タスク %s に上げた）", *f.PromotedTaskID)
	case f.PromotedChallengeID != nil:
		fmt.Fprintf(b, "\t（%s に上げた）", *f.PromotedChallengeID)
	}
	b.WriteString("\n")
}

// sortChallengeIDs は "C-<n>" を番号の昇順に並べる。
func sortChallengeIDs(ids []string) {
	num := func(s string) int {
		var n int
		_, _ = fmt.Sscanf(s, "C-%d", &n)
		return n
	}
	sort.SliceStable(ids, func(i, j int) bool { return num(ids[i]) < num(ids[j]) })
}

// followupCountsText は show の文の出力に出す、課題のフォローアップの未・済み・捨てたの件数（状態だけで数える）。あわせて、スーパーバイザーが条件から作ったフォローアップは 1 件ずつ並べる。
func followupCountsText(fs []core.Followup) string {
	var open, done, dropped int
	for _, f := range fs {
		switch f.Status {
		case core.FollowupStatusOpen:
			open++
		case core.FollowupStatusDone:
			done++
		case core.FollowupStatusDropped:
			dropped++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "フォローアップ: 未 %d 件・済み %d 件・捨てた %d 件\n", open, done, dropped)
	for _, f := range fs {
		if f.Source == core.FollowupSourceSupervisor { // スーパーバイザーが条件から作ったものは、題名も並べる
			fmt.Fprintf(&b, "  %s\t%s\t%s（出どころ: %s）\n", f.ID, f.Status, escapeControl(f.Title), f.Source)
		}
	}
	return b.String()
}

// rejectDuplicateFollowupIDs は同じ ID を 2 度並べた指定を usage_error にする（ストアを変える前）。
func rejectDuplicateFollowupIDs(ids []string) error {
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return NewError(CodeUsageError, "同じ ID を 2 度指定できません: "+id)
		}
		seen[id] = true
	}
	return nil
}

// runFollowupDone は `flywheel followup done <ID>... --note <確かめた結果>` の実装。本人確認は求めない
// （操作した人と経路は core が作業ログへ残す）。--note が無ければ必須フラグの欠落（usage_error）、
// 空なら core が validation_failed にする。複数の ID は core が全部検査してから変える。
func runFollowupDone(a Args) (any, error) {
	if err := rejectDuplicateFollowupIDs(a.Positional); err != nil {
		return nil, err
	}
	fs, err := a.Store.CompleteFollowups(context.Background(), core.ChannelCLI, a.Positional, a.Values["note"])
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: view.FromFollowupList(fs), text: followupIDsText(fs)}, nil
}

// runFollowupDrop は `flywheel followup drop <ID>... --reason <理由> [--relayed …]` の実装。本人確認は
// 1 回で、要約に指定した全部の ID と題名を出し、打ち込む対象の文字列は core.FollowupAttestationTarget が
// 決める（番号の小さい順にカンマでつないだもの）。本人確認が通ってから core が全件を検査して変える。
func runFollowupDrop(a Args) (any, error) {
	if err := rejectDuplicateFollowupIDs(a.Positional); err != nil {
		return nil, err
	}
	reason := a.Values["reason"]
	verifier, err := verifierFor(a)
	if err != nil {
		return nil, err
	}
	// 存在・未であること・理由の検査は core が持つ（本人確認を求める前に終える）。
	targets, err := a.Store.PrepareFollowupDrop(context.Background(), a.Positional, reason)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	target, ok := core.FollowupAttestationTarget(a.Positional)
	if !ok {
		return nil, NewError(CodeNotFound, "フォローアップが見つかりません")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "次のフォローアップを捨てます（%d 件）。\n", len(targets))
	for _, f := range targets {
		fmt.Fprintf(&b, "  %s\t%s\n", f.ID, escapeControl(f.Title))
	}
	fmt.Fprintf(&b, "理由: %s\n", escapeControl(reason))
	att, err := core.Verify(channelFor(a), verifier, b.String(), target)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	fs, err := a.Store.DropFollowups(context.Background(), a.Positional, reason, att)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: view.FromFollowupList(fs), text: followupIDsText(fs)}, nil
}

// followupPromoteStartsCycle は、上げた後にだけサイクルを始める（--preview は何も書かないので始めない）。
func followupPromoteStartsCycle(a Args) string {
	if a.Bools["preview"] {
		return ""
	}
	return core.CycleTriggerFollowupPromote
}

// runFollowupPromote は `flywheel followup promote <ID> [--preview] [--new-challenge] [--budget-impl-usd <額>] [--budget-review-usd <額>] [--raise-limit <額>] [--relayed …]` の実装
// （1 回に 1 件）。上げる先（新しい課題か元の課題のタスクか）は core が決める。
func runFollowupPromote(a Args) (any, error) {
	id := a.Positional[0]
	preview := a.Bools["preview"]
	var verifier core.Verifier
	if !preview { // --preview は本人確認なし（何も書かない）
		v, err := verifierFor(a)
		if err != nil {
			return nil, err
		}
		verifier = v
	}
	opts := core.FollowupPromoteOptions{NewChallenge: a.Bools["new-challenge"]}
	if v, ok := a.Values["budget-impl-usd"]; ok {
		x, err := parseBudgetFlag("budget-impl-usd", v)
		if err != nil {
			return nil, err
		}
		opts.BudgetImplUSD = &x
	}
	if v, ok := a.Values["budget-review-usd"]; ok {
		x, err := parseBudgetFlag("budget-review-usd", v)
		if err != nil {
			return nil, err
		}
		opts.BudgetReviewUSD = &x
	}
	if v, ok := a.Values["raise-limit"]; ok {
		x, err := parseBudgetFlag("raise-limit", v)
		if err != nil {
			return nil, err
		}
		opts.RaiseLimitUSD = &x
	}
	decl, derr := core.LoadAgentDeclaration(a.Store.Workspace())
	if derr != nil {
		return nil, mapCoreErr(derr)
	}
	plan, err := a.Store.PrepareFollowupPromote(context.Background(), id, opts, decl)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	if preview {
		return textOutput{json: view.FromFollowupPromotePlan(plan), text: followupPromoteSummary(plan)}, nil
	}
	// 上限の引き上げの検査（額の照合・対話で受けた記録での指定の必須）は core が持つ。本人確認を求める前に断る。
	if err := plan.CheckRaiseLimit(verifier.Method() == core.VerificationRelayed); err != nil {
		return nil, mapCoreErr(err)
	}
	att, err := core.Verify(channelFor(a), verifier, followupPromoteSummary(plan), id)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	res, err := a.Store.PromoteFollowup(context.Background(), plan, att, decl)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: view.FromFollowupPromote(*res), text: followupPromoteText(res)}, nil
}

// followupPromoteSummary は上げる前の本人確認の要約（上げるフォローアップ・上げる先・足すタスクの枠と足した後の見込み・上限）。
func followupPromoteSummary(p *core.FollowupPromotePlan) string {
	usd := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) + " USD" }
	var b strings.Builder
	if p.Dest == core.FollowupPromoteToTask {
		fmt.Fprintf(&b, "次のフォローアップを、課題 %s「%s」のタスクとして上げます（足す依頼を作る）。\n", p.TargetChallengeID, escapeControl(p.TargetChallengeTitle))
	} else {
		fmt.Fprintf(&b, "次のフォローアップを新しい課題（未分類）に上げます。\n")
	}
	fmt.Fprintf(&b, "  %s\t%s\n", p.Followup.ID, escapeControl(p.Followup.Title))
	if p.Dest == core.FollowupPromoteToTask {
		fmt.Fprintf(&b, "足すタスクの枠:   実装 %s・レビュー対応 %s\n", usd(p.BudgetImplUSD), usd(p.BudgetReviewUSD))
		fmt.Fprintf(&b, "足した後の見込み: %s（上限 %s）\n", usd(p.EstimateAfterUSD), usd(p.LimitUSD))
		if p.OverLimit() {
			fmt.Fprintf(&b, "上限を超える額:   %s（上げた後の上限 %s）\n", usd(p.ExceedsByUSD()), usd(p.LimitAfterUSD()))
		}
	} else if p.NewChallengeReason != "" {
		fmt.Fprintf(&b, "タスクにしなかった理由: %s\n", escapeControl(p.NewChallengeReason))
	}
	return b.String()
}

// followupPromoteText は上げた結果のテキスト出力（1 行目は新しい課題の ID か足す依頼の ID。タスクにしなかった理由があれば続けて出す）。
func followupPromoteText(r *core.FollowupPromoteResult) string {
	if r.Addition != nil {
		return r.Addition.ID + "\n"
	}
	out := r.Challenge.ID + "\n"
	if r.NewChallengeReason != "" {
		out += "タスクにしなかった理由: " + escapeControl(r.NewChallengeReason) + "\n"
	}
	return out
}

// followupIDsText は変えたフォローアップの ID を 1 行ずつ出す。
func followupIDsText(fs []core.Followup) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString(f.ID + "\n")
	}
	return b.String()
}
