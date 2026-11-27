package core

// このファイルは、分けるかの保留（選択肢と問いの文）を作る。

import (
	"context"
	"fmt"
	"strings"
)

// 分けるかの保留の選択肢の識別。
const (
	sliceSplitOptionSplit  = "split"
	sliceSplitOptionSingle = "single"
)

// sliceSplitHoldOptions は分けるかの保留の選択肢（この並び）。先頭が推奨。
func sliceSplitHoldOptions() []HoldOption {
	return []HoldOption{
		{ID: sliceSplitOptionSplit, Label: "この案で分ける"},
		{ID: sliceSplitOptionSingle, Label: "分けずに 1 つの課題で進める"},
	}
}

// sliceSplitQuestion は分けるかの保留の問いの文。区切り 1 つは 1 行。
func sliceSplitQuestion(p sliceSplitProposal, evidencePath string) string {
	lines := []string{fmt.Sprintf("この課題は、区切り 1 つに収まらないと見ました。次の %d つの課題に分ける案を作りました。推奨: %s",
		len(p.Slices), sliceSplitHoldOptions()[0].Label)}
	for i, sl := range p.Slices {
		after := ""
		if len(sl.DependsOn) > 0 {
			after = "。" + sliceNumbers(sl.DependsOn, "・") + " の後"
		}
		lines = append(lines, fmt.Sprintf("%d. %s — %s（リポジトリ: %s%s）", i+1, oneLine(sl.Title), oneLine(sl.Summary), sl.Repo, after))
	}
	lines = append(lines,
		"分ける理由: "+oneLine(p.Reason),
		"調べた根拠: "+evidencePath,
		"案を直したいときは、この案で分けてから、対話の欄で課題を足す・取り下げる・依存を直してください")
	return strings.Join(lines, "\n")
}

// insertSliceSplitHold は分けるかの保留（課題を止める保留。戻り先は分類済）の行を、保留へ進める遷移の書き込みの中で書く。
// 止まり方は渡さないので、スーパーバイザー待ちにならない。どの保留がこれかは、区切りを分ける記録の hold_id で見分ける。
func insertSliceSplitHold(ctx context.Context, tc *transitionCtx, p sliceSplitProposal, evidencePath string) (int64, error) {
	return insertHoldWithOptions(ctx, tc, sliceSplitQuestion(p, evidencePath), HoldKindHuman, StatusClassified, nil,
		HoldOptionsInput{Options: sliceSplitHoldOptions(), Required: true})
}
