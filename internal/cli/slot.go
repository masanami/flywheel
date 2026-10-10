package cli

import (
	"context"
	"fmt"

	"github.com/masanami/flywheel/internal/view"
)

// runSlotClear は `flywheel slot clear <SL-ID>` の実装。needs_attention のスロットを
// idle に戻す（人が作業ツリーを確かめた後に使う）。規則は core.ClearSlot が持ち、
// 本人確認は要らず（端末も要らない）、作業ログには載せない。
func runSlotClear(a Args) (any, error) {
	slot, err := a.Store.ClearSlot(context.Background(), a.Positional[0])
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{
		json: view.FromSlot(slot),
		text: fmt.Sprintf("スロット: %s\nリポジトリ: %s\nパス: %s\n状態: %s\n", slot.ID, slot.Repo, slot.Path, slot.State),
	}, nil
}
