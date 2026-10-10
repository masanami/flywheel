package cli

import (
	"context"

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
