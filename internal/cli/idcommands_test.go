package cli

import "testing"

// idCommandCases は「ID を引数に取る実装済みコマンド」の一覧である
// （docs/features/m1-core.md AC「存在しない ID を指定したコマンドは、終了コード1・
// not_found で終わる（§IF / API のうち ID を引数に取る全コマンドを…列挙して
// 検証する）」の枠組み）。後続チケット（#12 answer/approve/reject・#13 op
// add/approve(OP-ID)/reject(OP-ID)）が実装され次第、この一覧に行を足すだけで
// 閉包できるようにする。
//
// args は --workspace/--json を除いた、対象 ID を含むコマンド引数。入力の検証
// （閉集合・必須値）が ID の存在チェックより先に core で判定される操作
// （classify・verify・hold）は、検証を通過する値を extraFlags に添えている
// （空値だと not_found より先に validation_failed になってしまうため）。
// plan は body の内容が無いと ID の存在チェックへ到達できず、この汎用の
// 枠組み（stdin を常に空文字列で叩く requireErrorCode）では検証できないため、
// 専用のテスト（TestRunPlan_NotFoundForMissingOrMalformedID）で別途検証する。
var idCommandCases = [][]string{
	{"show"},
	{"edit", "--title", "x"},
	{"log"},
	{"classify", "--priority", "P0"},
	{"submit"},
	{"verify", "--result", "met"},
	{"hold", "--question", "why?"},
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
