package cli

import (
	"context"
	"fmt"
	"sort"
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
	if f.PromotedChallengeID != nil {
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

// followupCountsText は show の文の出力に出す、課題のフォローアップの未・済み・捨てたの件数（状態だけで数える）。
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
	return fmt.Sprintf("フォローアップ: 未 %d 件・済み %d 件・捨てた %d 件\n", open, done, dropped)
}
