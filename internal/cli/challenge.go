package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/masanami/flywheel/internal/core"
)

// runCreate は `flywheel create` の実装（T1）。core.CreateChallenge を呼ぶだけで、
// 検証・遷移・作業ログの規則は core が持つ（P2・P4）。
func runCreate(a Args) (any, error) {
	in := core.CreateInput{
		Title:        a.Values["title"],
		Description:  a.Values["description"],
		DoneCriteria: a.Values["done-criteria"],
	}
	if v, ok := a.Values["urgency"]; ok {
		in.Urgency = &v
	}
	c, err := a.Store.CreateChallenge(context.Background(), core.ChannelCLI, in)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: map[string]any{"challenge": challengeJSON(*c)}, text: challengeText(*c)}, nil
}

// runShow は `flywheel show <C-ID>` の実装。
func runShow(a Args) (any, error) {
	detail, err := a.Store.GetChallenge(context.Background(), a.Positional[0])
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{
		json: map[string]any{
			"challenge":  challengeJSON(detail.Challenge),
			"plans":      plansJSON(detail.Plans),
			"approvals":  approvalsJSON(detail.Approvals),
			"holds":      holdsJSON(detail.Holds),
			"operations": operationsJSON(detail.Operations),
		},
		text: challengeDetailText(detail),
	}, nil
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
	return textOutput{json: map[string]any{"challenges": challengesJSON(challenges)}, text: challengeListText(challenges)}, nil
}

// runEdit は `flywheel edit <C-ID> […]` の実装。人間記入欄のフラグが1つも
// 指定されていなければ usage_error（引数の誤り）で拒否する。core の検証
// （閉集合・存在・terminal_state・差分の有無）はここに書かない。
func runEdit(a Args) (any, error) {
	title, hasTitle := a.Values["title"]
	description, hasDescription := a.Values["description"]
	doneCriteria, hasDoneCriteria := a.Values["done-criteria"]
	urgency, hasUrgency := a.Values["urgency"]

	if !hasTitle && !hasDescription && !hasDoneCriteria && !hasUrgency {
		return nil, NewError(CodeUsageError, "edit には --title / --description / --done-criteria / --urgency のいずれか1つ以上を指定してください")
	}

	in := core.EditInput{}
	if hasTitle {
		in.Title = &title
	}
	if hasDescription {
		in.Description = &description
	}
	if hasDoneCriteria {
		in.DoneCriteria = &doneCriteria
	}
	if hasUrgency {
		in.Urgency = &urgency
	}

	c, err := a.Store.EditChallenge(context.Background(), core.ChannelCLI, a.Positional[0], in)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: map[string]any{"challenge": challengeJSON(*c)}, text: challengeText(*c)}, nil
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
	out := make([]map[string]any, 0, len(activities))
	for _, act := range activities {
		out = append(out, map[string]any{
			"at":           FormatTimestamp(act.At),
			"actor":        act.Actor,
			"channel":      act.Channel,
			"verification": act.Verification,
			"entity":       act.Entity,
			"entity_id":    act.EntityID,
			"action":       act.Action,
			"before":       act.Before,
			"after":        act.After,
		})
	}
	return textOutput{json: map[string]any{"activities": out}, text: activitiesText(activities)}, nil
}

// challengeJSON は core.Challenge を「成功時の JSON 出力の規約」の課題の形へ
// 変換する（create・edit・show・list が共有する）。
func challengeJSON(c core.Challenge) map[string]any {
	label, _ := c.Status.Label()
	return map[string]any{
		"id":            c.ID,
		"title":         c.Title,
		"description":   c.Description,
		"done_criteria": c.DoneCriteria,
		"urgency":       urgencyJSON(c.Urgency),
		"priority":      priorityJSON(c.Priority),
		"status":        string(c.Status),
		"status_label":  label,
		"version":       c.Version,
		"reporter":      c.Reporter,
		"created_at":    FormatTimestamp(c.CreatedAt),
		"updated_at":    FormatTimestamp(c.UpdatedAt),
	}
}

func urgencyJSON(u *core.Urgency) any {
	if u == nil {
		return nil
	}
	return string(*u)
}

func priorityJSON(p *core.Priority) any {
	if p == nil {
		return nil
	}
	return string(*p)
}

func nilableString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func plansJSON(plans []core.Plan) []map[string]any {
	out := make([]map[string]any, 0, len(plans))
	for _, p := range plans {
		out = append(out, planJSON(p))
	}
	return out
}

// approvalsJSON・holdsJSON は show の一覧を組み立てる。要素の形は
// approvalJSON・holdJSON（internal/cli/approval.go。#12 が approve/reject/
// answer の単発の成功出力と共有するために切り出した）と同じ。
func approvalsJSON(approvals []core.Approval) []map[string]any {
	out := make([]map[string]any, 0, len(approvals))
	for _, ap := range approvals {
		out = append(out, approvalJSON(ap))
	}
	return out
}

func holdsJSON(holds []core.Hold) []map[string]any {
	out := make([]map[string]any, 0, len(holds))
	for _, h := range holds {
		out = append(out, holdJSON(h))
	}
	return out
}

// operationJSON は core.IrreversibleOperation を「成功時の JSON 出力の規約」の
// operation オブジェクトの形へ変換する（show の operations の要素・
// op add・approve/reject <OP-ID> の単発の成功出力が共有する。internal/cli/
// operation.go の #13 が実コマンドから使う）。
func operationJSON(op core.IrreversibleOperation) map[string]any {
	return map[string]any{
		"id":           op.ID,
		"challenge_id": op.ChallengeID,
		"kind":         string(op.Kind),
		"summary":      op.Summary,
		"ref":          nilableString(op.Ref),
		"state":        string(op.State),
		"version":      op.Version,
		"created_at":   FormatTimestamp(op.CreatedAt),
	}
}

func operationsJSON(ops []core.IrreversibleOperation) []map[string]any {
	out := make([]map[string]any, 0, len(ops))
	for _, op := range ops {
		out = append(out, operationJSON(op))
	}
	return out
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

// challengeDetailText は --json 無しの show の表示。
func challengeDetailText(d *core.ChallengeDetail) string {
	var b strings.Builder
	c := d.Challenge
	label, _ := c.Status.Label()
	fmt.Fprintf(&b, "ID:            %s\n", c.ID)
	fmt.Fprintf(&b, "タイトル:      %s\n", c.Title)
	fmt.Fprintf(&b, "状態:          %s (%s)\n", label, c.Status)
	fmt.Fprintf(&b, "版:            %d\n", c.Version)
	fmt.Fprintf(&b, "起票者:        %s\n", c.Reporter)
	fmt.Fprintf(&b, "緊急度:        %s\n", textOrDash(urgencyJSON(c.Urgency)))
	fmt.Fprintf(&b, "優先度:        %s\n", textOrDash(priorityJSON(c.Priority)))
	fmt.Fprintf(&b, "説明:          %s\n", c.Description)
	fmt.Fprintf(&b, "完了条件:      %s\n", c.DoneCriteria)
	fmt.Fprintf(&b, "作成:          %s\n", FormatTimestamp(c.CreatedAt))
	fmt.Fprintf(&b, "更新:          %s\n", FormatTimestamp(c.UpdatedAt))
	fmt.Fprintf(&b, "計画:          %d 版\n", len(d.Plans))
	// 計画は全版の本文まで出す（受入基準 17・35 の「show で読める」をテキスト
	// 出力にも適用する【決定 2026-09-23（オーナー）】）。
	for _, p := range d.Plans {
		fmt.Fprintf(&b, "  v%d (%s)\n", p.Version, FormatTimestamp(p.CreatedAt))
		b.WriteString(indentLines(p.Body, "    "))
	}
	fmt.Fprintf(&b, "承認・差し戻し: %d 件\n", len(d.Approvals))
	for _, a := range d.Approvals {
		fmt.Fprintf(&b, "  %s %s by %s (%s)", a.Kind, a.Decision, a.Actor, FormatTimestamp(a.DecidedAt))
		if a.OperationID != nil {
			fmt.Fprintf(&b, " %s", *a.OperationID)
		}
		b.WriteString("\n")
		if a.Reason != nil {
			b.WriteString(indentLines("理由: "+*a.Reason, "    "))
		}
	}
	// 保留は問いと回答（未回答ならその旨）を出す（決定は計画の本文と同じ）。
	fmt.Fprintf(&b, "保留:          %d 件\n", len(d.Holds))
	for _, h := range d.Holds {
		fromLabel, _ := h.FromStatus.Label()
		fmt.Fprintf(&b, "  %s から (%s)\n", fromLabel, FormatTimestamp(h.RaisedAt))
		b.WriteString(indentLines("問い: "+h.Question, "    "))
		if h.Answer == nil {
			b.WriteString("    回答: (未回答)\n")
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
		fmt.Fprintf(&b, "    回答者: %s (%s)\n", answeredBy, answeredAt)
	}
	fmt.Fprintf(&b, "不可逆操作:    %d 件\n", len(d.Operations))
	for _, op := range d.Operations {
		fmt.Fprintf(&b, "  %s %s %s [%s]\n", op.ID, op.Kind, op.Summary, op.State)
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

func textOrDash(v any) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprint(v)
}
