package cli

import "testing"

// idCommandCases は「ID を引数に取る実装済みコマンド」の一覧である
// （docs/features/m1-core.md AC「存在しない ID を指定したコマンドは、終了コード1・
// not_found で終わる（§IF / API のうち ID を引数に取る全コマンドを…列挙して
// 検証する）」の枠組み）。後続チケット（#10 classify・#11 plan/submit/verify・
// #12 hold/answer/approve/reject・#13 op add/approve(OP-ID)/reject(OP-ID)）が
// 実装され次第、この一覧に行を足すだけで閉包できるようにする。
//
// args は --workspace/--json を除いた、対象 ID を含むコマンド引数。
var idCommandCases = [][]string{
	{"show"},
	{"edit", "--title", "x"},
	{"log"},
}

// TestIDCommands_NotFoundForMissingAndIrreversibleOperationID は、存在しない
// 課題 ID（C-999）と、不可逆操作の ID 形式（OP-1。#9 の時点ではまだ登録できない
// ためやはり存在しない）の両方が not_found（終了コード1）になることを検証する。
func TestIDCommands_NotFoundForMissingAndIrreversibleOperationID(t *testing.T) {
	ws := initializedWorkspace(t)
	for _, id := range []string{"C-999", "OP-1"} {
		for _, base := range idCommandCases {
			cmdName, extraFlags := base[0], base[1:]
			args := append([]string{cmdName, id}, extraFlags...)
			args = append(args, "--workspace", ws)
			requireErrorCode(t, args, 1, CodeNotFound)
		}
	}
}
