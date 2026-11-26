package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// runCreate は `flywheel create` の実装（T1）。core.CreateChallenge を呼ぶだけで、
// 検証・遷移・作業ログの規則は core が持つ（P2・P4）。
func runCreate(a Args) (any, error) {
	in := core.CreateInput{
		Title:        a.Values["title"],
		Description:  a.Values["description"],
		DoneCriteria: a.Values["done-criteria"],
		Spec:         a.Values["spec"],
		SpecBase:     a.Values["spec-base"],
	}
	if v, ok := a.Values["urgency"]; ok {
		in.Urgency = &v
	}
	in.Chat = a.Chat.coreMark()
	c, err := a.Store.CreateChallenge(context.Background(), core.ChannelCLI, in)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: view.ChallengeResponse{Challenge: view.FromChallenge(*c)}, text: challengeText(*c)}, nil
}

// runShow は `flywheel show <C-ID>` の実装。
func runShow(a Args) (any, error) {
	ctx := context.Background()
	if a.Bools["plan"] {
		// サイズごとの既定を引く宣言は、予算の上限の表示にだけ使う（読めなくても計画は出す）。
		decl, _ := core.LoadAgentDeclaration(a.Store.Workspace())
		pv, err := a.Store.GetPlanView(ctx, a.Positional[0], decl)
		if err != nil {
			return nil, mapCoreErr(err)
		}
		return textOutput{json: view.FromPlanView(pv), text: planViewText(pv)}, nil
	}
	detail, err := a.Store.GetChallenge(ctx, a.Positional[0])
	if err != nil {
		return nil, mapCoreErr(err)
	}
	changes := releaseHeadChanges(ctx, a.lookPath(), detail)
	resp := view.FromChallengeDetail(detail)
	for _, c := range changes {
		resp.ReleaseHeadChanges = append(resp.ReleaseHeadChanges, view.ReleaseHeadChange{OperationID: c.opID, HeadChange: view.FromReleaseHeadAssessment(c.assessment)})
	}
	text := challengeDetailText(detail)
	if len(changes) > 0 {
		var b strings.Builder
		b.WriteString(text)
		b.WriteString("検証の後に head が動いた承認待ちの release:\n")
		for _, c := range changes {
			fmt.Fprintf(&b, "  %s\n", c.opID)
			writeHeadChangeLines(&b, "    ", c.assessment)
		}
		text = b.String()
	}
	return textOutput{json: resp, text: text}, nil
}

// headChangeOf は承認待ちの release 1 件の、検証の後に head が動いたことの評価。
type headChangeOf struct {
	opID       string
	assessment *core.ReleaseHeadAssessment
}

// releaseHeadChanges は課題の承認待ちの release のうち、PR の head が J5 の検証の head から動いたものを評価する
// （#485）。GitHub を読めない release・head が動いていない release は載せない（show を止めない）。
func releaseHeadChanges(ctx context.Context, lp lookPathFunc, d *core.ChallengeDetail) []headChangeOf {
	var out []headChangeOf
	for _, op := range d.Operations {
		if op.Kind != core.OperationKindRelease || op.State != core.OperationStatePending || op.Ref == nil || op.VerifiedHeadSHA == nil {
			continue
		}
		if _, _, ok := core.ParseReleasePullRequestRef(*op.Ref); !ok {
			continue
		}
		a, err := core.AssessReleaseHead(ctx, newCheckSource(lp), newCompareSource(lp), *op.Ref, *op.VerifiedHeadSHA)
		if err != nil || a.Change.Kind == "" {
			continue
		}
		out = append(out, headChangeOf{opID: op.ID, assessment: a})
	}
	return out
}

// runList は `flywheel list [--status <状態>]` の実装。
func runList(a Args) (any, error) {
	opt := core.ListOptions{}
	if v, ok := a.Values["status"]; ok {
		opt.Status = &v
	}
	challenges, err := a.Store.ListChallenges(context.Background(), opt)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: view.ListResponse{Challenges: view.FromChallenges(challenges)}, text: challengeListText(challenges)}, nil
}

// runEdit は `flywheel edit <C-ID> […]` の実装。編集できる欄のフラグが1つも
// 指定されていなければ usage_error（引数の誤り）で拒否する。core の検証
// （閉集合・存在・terminal_state・差分の有無）はここに書かない。
func runEdit(a Args) (any, error) {
	in := core.EditInput{}
	targets := []struct {
		flag string
		dst  **string
	}{
		{"title", &in.Title},
		{"description", &in.Description},
		{"done-criteria", &in.DoneCriteria},
		{"urgency", &in.Urgency},
		{"headline", &in.Headline},
		{"context", &in.Context},
		{"expected", &in.Expected},
		{"actual", &in.Actual},
		{"non-goals", &in.NonGoals},
		{"figure", &in.Figure},
		{"spec", &in.Spec},
		{"spec-base", &in.SpecBase},
	}
	specified := false
	for _, t := range targets {
		if v, ok := a.Values[t.flag]; ok {
			*t.dst = &v
			specified = true
		}
	}
	if !specified {
		return nil, NewError(CodeUsageError, "edit には --title / --description / --done-criteria / --urgency / --headline / --context / --expected / --actual / --non-goals / --figure / --spec / --spec-base のいずれか1つ以上を指定してください")
	}

	c, err := a.Store.EditChallenge(context.Background(), core.ChannelCLI, a.Positional[0], in)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: view.ChallengeResponse{Challenge: view.FromChallenge(*c)}, text: challengeText(*c)}, nil
}

// runDepAdd は `flywheel dep add <C-ID> <依存先>...` の実装。検査（自己参照・存在・終端の状態・
// 循環）と書き込みは core に任せる。成功時は edit と同じ形の課題を返す。
func runDepAdd(a Args) (any, error) {
	c, err := a.Store.AddDependencies(context.Background(), core.ChannelCLI, a.Positional[0], a.Positional[1:])
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: view.ChallengeResponse{Challenge: view.FromChallenge(*c)}, text: challengeText(*c)}, nil
}

// runDepRm は `flywheel dep rm <C-ID> <依存先>...` の実装。
func runDepRm(a Args) (any, error) {
	c, err := a.Store.RemoveDependencies(context.Background(), core.ChannelCLI, a.Positional[0], a.Positional[1:])
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: view.ChallengeResponse{Challenge: view.FromChallenge(*c)}, text: challengeText(*c)}, nil
}

// runLog は `flywheel log [<C-ID>]` の実装。
func runLog(a Args) (any, error) {
	var challengeID *string
	if len(a.Positional) == 1 {
		challengeID = &a.Positional[0]
	}
	activities, err := a.Store.ListActivities(context.Background(), challengeID)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: view.LogResponse{Activities: view.FromActivities(activities)}, text: activitiesText(activities)}, nil
}

func nilableString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// challengeText は --json 無しの create・edit の表示（ID を 1 行）。
func challengeText(c core.Challenge) string {
	return c.ID + "\n"
}

// challengeListText は --json 無しの list の表示（1 件 1 行: ID・状態の表示名・タイトル）。
func challengeListText(challenges []core.Challenge) string {
	var b strings.Builder
	for _, c := range challenges {
		label, _ := c.Status.Label()
		fmt.Fprintf(&b, "%s\t%s\t%s\n", c.ID, label, c.Title)
	}
	return b.String()
}

// writeChatSpec は仕様づくりの費用（課題の予算には数えない）と、紐づいた対話の欄の会話を書く。
func writeChatSpec(b *strings.Builder, d *core.ChallengeDetail) {
	fmt.Fprintf(b, "仕様づくりの費用: %s USD（予算には数えない）\n", formatUSD(d.SpecCostUSD))
	fmt.Fprintf(b, "紐づいた会話:  %d 件\n", len(d.ChatSessions))
	for _, s := range d.ChatSessions {
		fmt.Fprintf(b, "  %s 費用: %s USD 最初の発言: %s\n", FormatTimestamp(s.StartedAt), formatUSD(s.CostUSD), escapeControl(s.FirstMessage))
	}
}

// challengeDetailText は --json 無しの show の表示。
func challengeDetailText(d *core.ChallengeDetail) string {
	var b strings.Builder
	c := d.Challenge
	vc := view.FromChallenge(c)
	label, _ := c.Status.Label()
	fmt.Fprintf(&b, "ID:            %s\n", c.ID)
	fmt.Fprintf(&b, "タイトル:      %s\n", c.Title)
	fmt.Fprintf(&b, "状態:          %s (%s)\n", label, c.Status)
	fmt.Fprintf(&b, "版:            %d\n", c.Version)
	fmt.Fprintf(&b, "起票者:        %s\n", c.Reporter)
	fmt.Fprintf(&b, "緊急度:        %s\n", textOrDash(nilableString(vc.Urgency)))
	fmt.Fprintf(&b, "優先度:        %s\n", textOrDash(nilableString(vc.Priority)))
	fmt.Fprintf(&b, "説明:          %s\n", c.Description)
	fmt.Fprintf(&b, "完了条件:      %s\n", c.DoneCriteria)
	if c.SpecPath != nil {
		draftMark := ""
		if c.SpecIsDraft() {
			draftMark = "（下書き）"
		}
		fmt.Fprintf(&b, "仕様の文書の場所: %s%s\n", escapeControl(*c.SpecPath), draftMark)
	}
	if c.SpecBasePath != nil {
		fmt.Fprintf(&b, "置き換える元の文書の場所: %s\n", escapeControl(*c.SpecBasePath))
	}
	fmt.Fprintf(&b, "作成:          %s\n", FormatTimestamp(c.CreatedAt))
	fmt.Fprintf(&b, "更新:          %s\n", FormatTimestamp(c.UpdatedAt))
	deps := make([]string, 0, len(d.Dependencies))
	for _, dep := range d.Dependencies {
		state := "満たされていない"
		if dep.Satisfied {
			state = "満たされている"
		}
		deps = append(deps, dep.ChallengeID+"("+state+")")
	}
	fmt.Fprintf(&b, "依存先:        %s\n", textOrDash(joinOrNil(deps)))
	fmt.Fprintf(&b, "依存元:        %s\n", textOrDash(joinOrNil(d.Dependents)))
	fmt.Fprintf(&b, "統合先:        %s\n", usesIntegrationText(d.UsesIntegration))
	if d.IntegrationRef != nil {
		fmt.Fprintf(&b, "統合先の識別:  %s\n", escapeControl(*d.IntegrationRef))
	}
	fmt.Fprintf(&b, "予算:          %s\n", budgetDetailText(d.Budget))
	writeChatSpec(&b, d)
	fmt.Fprintf(&b, "タスク:        %d 件\n", len(d.Tasks))
	for _, t := range d.Tasks {
		fmt.Fprintf(&b, "  %s %s [%s (%s)] 枠: 実装 %s USD・レビュー対応 %s USD / 使った額: %s USD\n", t.ID, escapeControl(t.Title), t.Status.Label(), t.Status,
			formatUSD(t.BudgetImplUSD), formatUSD(t.BudgetReviewUSD), formatUSD(t.SpentUSD))
	}
	if c.Status == core.StatusAwaitingCompletionApproval {
		writeAddedTasks(&b, d.AddedTasks)
	}
	if len(d.ConflictFixes) > 0 {
		fmt.Fprintf(&b, "通したコンフリクトの解消: %d 件\n", len(d.ConflictFixes))
		for _, f := range d.ConflictFixes {
			dest, id := "統合先", ""
			if f.TaskID != nil {
				id = *f.TaskID
			}
			if f.Target == core.ConflictFixTargetRelease {
				dest = "反映先"
				if f.OperationID != nil {
					id = *f.OperationID
				}
			}
			fmt.Fprintf(&b, "  %s %s %s 直したファイル: %d 件\n", FormatTimestamp(f.At), dest, id, len(f.Files))
		}
	}
	fmt.Fprintf(&b, "計画:          %d 版\n", len(d.Plans))
	// 計画は全版の本文まで出す（受入基準 17・35 の「show で読める」をテキスト
	// 出力にも適用する【決定 2026-09-23（オーナー）】）。
	for _, p := range d.Plans {
		fmt.Fprintf(&b, "  v%d (%s)\n", p.Version, FormatTimestamp(p.CreatedAt))
		b.WriteString(indentLines(p.Body, "    "))
	}
	fmt.Fprintf(&b, "承認・差し戻し: %d 件\n", len(d.Approvals))
	for _, a := range d.Approvals {
		fmt.Fprintf(&b, "  %s %s %s by %s (%s)", a.ID, a.Kind, a.Decision, a.Actor, FormatTimestamp(a.DecidedAt))
		if a.OperationID != nil {
			fmt.Fprintf(&b, " %s", *a.OperationID)
		}
		switch {
		case a.SupersededAt != nil && a.SupersededBy != nil:
			fmt.Fprintf(&b, " [置き換えられた %s: %s → %s]", FormatTimestamp(*a.SupersededAt), textOrDash(nilableString(a.SupersededReason)), *a.SupersededBy)
		case a.SupersededAt != nil:
			fmt.Fprintf(&b, " [置き換えられた %s: %s]", FormatTimestamp(*a.SupersededAt), textOrDash(nilableString(a.SupersededReason)))
		}
		if a.MergeInVerifiedHead != nil {
			fmt.Fprintf(&b, " [検証の head %s から取り込みだけと判定して承認]", shortHead(*a.MergeInVerifiedHead))
		}
		b.WriteString("\n")
		if a.Reason != nil {
			b.WriteString(indentLines("理由: "+*a.Reason, "    "))
		}
	}
	// 保留は問いと回答（未回答ならその旨）を出す（決定は計画の本文と同じ）。
	fmt.Fprintf(&b, "保留:          %d 件\n", len(d.Holds))
	taskTitles := map[string]string{}
	for _, t := range d.Tasks {
		taskTitles[t.ID] = t.Title
	}
	openAnswerable := 0
	for _, h := range d.Holds {
		fromLabel, _ := h.FromStatus.Label()
		fmt.Fprintf(&b, "  %s から (%s)\n", fromLabel, FormatTimestamp(h.RaisedAt))
		fmt.Fprintf(&b, "    保留: %s (%s)\n", h.HoldID, h.ScopeLabel())
		if h.TaskID != nil {
			title := taskTitles[*h.TaskID]
			if title == "" {
				title = *h.TaskID
			}
			fmt.Fprintf(&b, "    タスク: %s\n", escapeControl(title))
		}
		if h.Open() { // core の回答の相手の数え方（種類を問わない）と同じ
			openAnswerable++
		}
		if h.Kind == core.HoldKindParentDirect {
			b.WriteString("    種類: 親が直接行う\n")
		}
		if h.StopKind != "" {
			fmt.Fprintf(&b, "    止まり方: %s\n", h.StopKind)
		}
		if h.SupervisorState != "" {
			fmt.Fprintf(&b, "    スーパーバイザーの状態: %s\n", h.SupervisorState)
		}
		b.WriteString(indentLines("問い: "+h.Question, "    "))
		if h.SupervisorState == core.SupervisorStateEscalated {
			if h.SupervisorFindings != "" {
				b.WriteString(indentLines("調べたこと: "+h.SupervisorFindings, "    "))
			}
			if h.SupervisorRecommendation != "" {
				b.WriteString(indentLines("推奨: "+h.SupervisorRecommendation, "    "))
			}
		}
		writeHoldOptions(&b, h.Options, "    ")
		if h.Answer == nil {
			if !h.Open() {
				fmt.Fprintf(&b, "    回答: (回答なしで閉じた %s)\n", FormatTimestamp(*h.ClosedAt))
			} else {
				b.WriteString("    回答: (未回答)\n")
			}
			continue
		}
		answeredBy, answeredAt := "-", "-"
		if h.AnsweredBy != nil {
			answeredBy = *h.AnsweredBy
		}
		if h.AnsweredAt != nil {
			answeredAt = FormatTimestamp(*h.AnsweredAt)
		}
		b.WriteString(indentLines("回答: "+*h.Answer, "    "))
		if h.AnswerOption != nil {
			fmt.Fprintf(&b, "    選んだ選択肢: %s\n", escapeControl(*h.AnswerOption))
		}
		fmt.Fprintf(&b, "    回答者: %s (%s)\n", answeredBy, answeredAt)
	}
	if openAnswerable >= 2 {
		fmt.Fprintf(&b, "  開いている保留が %d 件あります。答える保留を指定してください: flywheel answer %s --hold <H-ID> --answer <回答>\n", openAnswerable, d.ID)
	}
	if len(d.SupervisorActions) > 0 {
		fmt.Fprintf(&b, "スーパーバイザーの対処: %d 件\n", len(d.SupervisorActions))
		for _, a := range d.SupervisorActions {
			hand := "(判断できず)"
			if a.Hand != nil {
				hand = string(*a.Hand)
			}
			fmt.Fprintf(&b, "  %s %s 止まり方: %s 手: %s 結果: %s\n", FormatTimestamp(a.CreatedAt), a.CycleID, a.StopKind, hand, a.Result)
			if a.Findings != "" {
				b.WriteString(indentLines("調べたこと: "+a.Findings, "    "))
			}
		}
	}
	if line := supervisorHandsTakenText(d.SupervisorHandsTaken); line != "" {
		b.WriteString(line)
	}
	b.WriteString(supervisorQuestionsText(d.SupervisorQuestions))
	if len(d.DirectCompletions) > 0 {
		fmt.Fprintf(&b, "親が直接行った記録: %d 件\n", len(d.DirectCompletions))
		for _, dc := range d.DirectCompletions {
			fmt.Fprintf(&b, "  (%s)\n", FormatTimestamp(dc.CreatedAt))
			for _, ref := range dc.Artifacts {
				fmt.Fprintf(&b, "    成果物: %s\n", escapeControl(ref))
			}
			if dc.Note != nil {
				b.WriteString(indentLines("説明: "+escapeControl(*dc.Note), "    "))
			}
		}
	}
	if len(d.Followups) > 0 {
		b.WriteString(followupCountsText(d.Followups))
	}
	fmt.Fprintf(&b, "不可逆操作:    %d 件\n", len(d.Operations))
	for _, op := range d.Operations {
		fmt.Fprintf(&b, "  %s %s %s [%s]", op.ID, op.Kind, op.Summary, op.State)
		if op.ClosedReason != nil {
			fmt.Fprintf(&b, " (閉じた理由: %s)", *op.ClosedReason)
		}
		if op.SupersededByOperationID != nil {
			fmt.Fprintf(&b, " → %s に置き換え", *op.SupersededByOperationID)
		}
		if op.SupersedesOperationID != nil {
			fmt.Fprintf(&b, " ← %s を置き換え", *op.SupersedesOperationID)
		}
		b.WriteString("\n")
		// 取り消して（承認待ちへ戻して）人の判断を待っている操作は、直前の試行の結果と理由を出す（#658。上限で止めた理由を show で読めるように）。
		if op.State == core.OperationStatePending && op.LastExecutionResult != nil && isCancelResult(*op.LastExecutionResult) {
			reason := "-"
			if op.LastExecutionDetail != nil {
				reason = escapeControl(*op.LastExecutionDetail)
			}
			fmt.Fprintf(&b, "    直前の試行: %s（%s）\n", *op.LastExecutionResult, reason)
			fmt.Fprintf(&b, "      意味: %s\n", view.ExecutionResultLabel(*op.LastExecutionResult))
		} else if op.Kind == core.OperationKindRelease && op.LastExecutionResult != nil {
			// 反映の操作の最後の試行の記録（結果の語と詳細）。取り消さずに待っている結果も、何を待っているかを読めるように出す。
			fmt.Fprintf(&b, "    最後の試行: %s（%s）\n", *op.LastExecutionResult, view.ExecutionResultLabel(*op.LastExecutionResult))
			if op.LastExecutionDetail != nil {
				fmt.Fprintf(&b, "      詳細: %s\n", escapeControl(*op.LastExecutionDetail))
			}
		}
	}
	// GitHub Issue との対応（#56）も JSON と同じ項目を出す（m2 仕様 §食い違いの
	// 表示「show は、対応のある課題について対応の記録…を出力する」）。
	if sb := d.SourceBinding; sb == nil {
		b.WriteString("取り込み元:    -\n")
	} else {
		fmt.Fprintf(&b, "取り込み元:    %s %s\n", sb.SourceID, sb.ExternalKey)
		fmt.Fprintf(&b, "  URL: %s\n", sb.URL)
		fmt.Fprintf(&b, "  上流: %s  ポリシー: %s  fingerprint: %s\n", sb.UpstreamState, sb.PolicyState, sb.Fingerprint)
		fmt.Fprintf(&b, "  対応の作成: %s  更新: %s\n", FormatTimestamp(sb.CreatedAt), FormatTimestamp(sb.UpdatedAt))
	}
	if len(d.SourceChildren) > 0 {
		fmt.Fprintf(&b, "取り込み元の子: %d 件\n", len(d.SourceChildren))
	}
	return b.String()
}

// activitiesText は --json 無しの log の表示（1 件 1 行）。
func activitiesText(activities []core.Activity) string {
	var b strings.Builder
	for _, a := range activities {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\n",
			FormatTimestamp(a.At), a.Actor, a.Channel+"/"+a.Verification, a.EntityID, a.Action, string(a.After))
	}
	return b.String()
}

// indentLines は s の各行の先頭に prefix を付け、各行を改行で終える
// （複数行の本文・回答を show のテキスト出力で字下げして示すため）。
func indentLines(s, prefix string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString(prefix)
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// joinOrNil は items を `,` で連ねる。空なら nil（textOrDash が `-` にする）。
func joinOrNil(items []string) any {
	if len(items) == 0 {
		return nil
	}
	return strings.Join(items, ",")
}

func textOrDash(v any) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprint(v)
}

// isCancelResult は、試行の結果が承認を取り消す結果（limit_reached・head_changed 等）か。
func isCancelResult(r core.ExecutionResult) bool {
	c, _ := r.Class()
	return c == core.ExecutionClassCancel
}

// usesIntegrationText は統合先を使うかの表示（未決か過去の課題は「未決」）。
func usesIntegrationText(v *bool) string {
	switch {
	case v == nil:
		return "未決"
	case *v:
		return "使う"
	}
	return "使わない"
}

// writeAddedTasks は完了の承認の要約へ、計画の後に足したタスクの一覧（題名・出どころ）を書く。無ければ何も書かない。
func writeAddedTasks(b *strings.Builder, ts []core.AddedTask) {
	if len(ts) == 0 {
		return
	}
	fmt.Fprintf(b, "計画の後に足したタスク: %d 件\n", len(ts))
	for _, t := range ts {
		fmt.Fprintf(b, "  %s %s（出どころ: %s）\n", t.TaskID, escapeControl(t.Title), t.Origin)
	}
}

// budgetDetailText は `show` の予算の行の値（見込みと上限。上限の理由があれば添える）。見込みが不明なら「不明」。
func budgetDetailText(b core.ChallengeDetailBudget) string {
	if b.EstimateUSD == nil || b.LimitUSD == nil {
		return "不明（枠を決められないタスクがある）"
	}
	text := fmt.Sprintf("見込み %s USD・上限 %s USD", formatUSD(*b.EstimateUSD), formatUSD(*b.LimitUSD))
	if b.LimitReason != nil {
		text += "（上限の理由: " + escapeControl(*b.LimitReason) + "）"
	}
	return text
}

// planViewText は `show --plan` の表示。最新の計画の方針・完了したら見えるもの・やらないこと・問いと推奨・
// タスクのツリー・予算の見込み・統合先を使うかを文で出す。承認の前はタスクの前提を計画の中の識別子（key）で、
// 承認の後はタスクの ID で出す（core の PlanTaskView が識別子を決める）。構造化した出力を持たない計画は本文だけを出す。
func planViewText(p *core.PlanView) string {
	var b strings.Builder
	label, _ := p.Status.Label()
	fmt.Fprintf(&b, "%s  %s   [%s]\n", p.ChallengeID, escapeControl(p.Title), label)
	fmt.Fprintf(&b, "計画の版: %d\n", p.Version)
	if !p.Structured {
		b.WriteString("（この計画は構造化した出力を持たないので、本文だけを出します）\n")
		b.WriteString(indentLines(escapeControlKeepLines(p.Body), "  "))
		return b.String()
	}
	if p.Summary != nil {
		fmt.Fprintf(&b, "方針: %s\n", escapeControl(*p.Summary))
	}
	if len(p.Expected) > 0 {
		b.WriteString("完了したら見えるもの:\n")
		for _, e := range p.Expected {
			fmt.Fprintf(&b, "  - %s\n      変更前: %s\n      変更後: %s\n", escapeControl(e.Scene), escapeControl(e.Before), escapeControl(e.After))
		}
	}
	if len(p.NonGoals) > 0 {
		b.WriteString("やらないこと:\n")
		for _, g := range p.NonGoals {
			fmt.Fprintf(&b, "  - %s\n", escapeControl(g))
		}
	}
	writePlanTopics(&b, p.Topics)
	budget := "未設定"
	if p.BudgetUSD != nil {
		budget = strconv.FormatFloat(*p.BudgetUSD, 'f', -1, 64) + " USD"
	}
	fmt.Fprintf(&b, "タスクのツリー（%d 件・見込み %s・統合先を使うか: %s）:\n", len(p.Tasks), budget, usesIntegrationText(p.UsesIntegration))
	for _, t := range p.Tasks {
		line := fmt.Sprintf("  %s %s", t.Identifier(), escapeControl(t.Title))
		if t.ID != "" {
			line += fmt.Sprintf(" [%s]", t.Status.Label())
		}
		if t.Source != "" {
			line += "（取り込み元の子: " + escapeControl(t.Source) + "）"
		}
		if len(t.DependsOn) > 0 {
			line += "（前提: " + strings.Join(t.DependsOn, "・") + "）"
		}
		line += taskFrameText(t)
		b.WriteString(line + "\n")
	}
	return b.String()
}

// writePlanTopics は論点ごとに、問いと選択肢・推奨の印・答え（記録があれば）を書く。論点が無ければ何も書かない。
func writePlanTopics(b *strings.Builder, topics []core.PlanTopicView) {
	if len(topics) == 0 {
		return
	}
	b.WriteString("問いと推奨:\n")
	for _, t := range topics {
		fmt.Fprintf(b, "  論点: %s\n", escapeControl(t.Title))
		for _, q := range t.Questions {
			fmt.Fprintf(b, "    Q%d %s\n", q.Number, escapeControl(q.Question))
			for j, o := range q.Options {
				mark := ""
				if j+1 == q.Recommended {
					mark = "（推奨）"
				}
				fmt.Fprintf(b, "      %d) %s%s\n", j+1, escapeControl(o.Label), mark)
			}
			switch a := q.Answer; {
			case a == nil:
			case a.Text != nil:
				fmt.Fprintf(b, "      答え（文）: %s\n", escapeControl(*a.Text))
			case a.Choice != nil:
				lbl := fmt.Sprintf("選択肢 %d", *a.Choice)
				if a.Label != nil {
					lbl = fmt.Sprintf("%d) %s", *a.Choice, escapeControl(*a.Label))
				}
				note := "（推奨と違う）"
				if a.Recommended {
					note = "（推奨と同じ）"
				}
				fmt.Fprintf(b, "      答え: %s%s\n", lbl, note)
			}
		}
	}
}

// formatUSD は USD の額を、末尾の 0 を付けずに文にする。
func formatUSD(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// writeHoldOptions は保留の選択肢（識別と見出し。説明があれば次の行に説明）を indent の字下げで書く。
// 選択肢が無いときは何も書かない。
func writeHoldOptions(b *strings.Builder, opts []core.HoldOption, indent string) {
	if len(opts) == 0 {
		return
	}
	fmt.Fprintf(b, "%s選択肢:\n", indent)
	for _, o := range opts {
		fmt.Fprintf(b, "%s  %s: %s\n", indent, escapeControl(o.ID), escapeControl(o.Label))
		if o.Description != "" {
			b.WriteString(indentLines(escapeControlKeepLines(o.Description), indent+"    "))
		}
	}
}

// supervisorHandsTakenText は打った手の件数（結果が executed の対処の記録を、手ごとに数えた値）の 1 行。手が無ければ空。
func supervisorHandsTakenText(m map[core.SupervisorHand]int) string {
	parts := make([]string, 0, len(m))
	for _, h := range core.SupervisorHandValues() {
		if n := m[h]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s: %d", h, n))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "スーパーバイザーが打った手: " + strings.Join(parts, "・") + "\n"
}

// supervisorQuestionsText は、スーパーバイザーが答えた子の問い（core が結果 executed の対処の記録から集めたもの）を、
// 問い・答え・根拠の順に並べる。完了の承認のときにオーナーが中身を確かめるための表示。無ければ空。
func supervisorQuestionsText(qs []core.SupervisorQuestion) string {
	if len(qs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("スーパーバイザーが答えた子の問い:\n")
	for _, q := range qs {
		b.WriteString(indentLines("問い: "+escapeControl(q.Question), "  "))
		b.WriteString(indentLines("答え: "+escapeControl(q.Answer), "  "))
		for _, bs := range q.Basis {
			if bs.Locator == "" {
				fmt.Fprintf(&b, "    根拠: %s\n", escapeControl(bs.Path))
				continue
			}
			fmt.Fprintf(&b, "    根拠: %s（%s）\n", escapeControl(bs.Path), escapeControl(bs.Locator))
		}
	}
	return b.String()
}
