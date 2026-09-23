package cli

import (
	"strings"
	"testing"
)

// AC-18・AC-73: ID を引数に取る全コマンド（登録表で MaxPositional ≥ 1 のもの）は、存在しない課題の ID と不可逆操作の
// ID のそれぞれで not_found（終了コード 1）。失敗時の --json は標準エラーに
// error.code と error.message を書き、標準出力は空。本人確認つきのコマンドも
// 端末を開く前に not_found で決着する（端末なしで検証できる）。対象 ID 以外の
// 引数は allCommandSuccessCases の成功経路のものを流用する（plan の本文のように、
// 入力の検証が ID の存在チェックより先に行われる引数も妥当な値になる）。
func TestIDCommands_NotFoundForMissingChallengeAndOperationID(t *testing.T) {
	tested := 0
	for _, name := range sortedCommandNames(registeredCommands()) {
		cmd := registeredCommands()[name]
		tc, ok := allCommandSuccessCases[name]
		if !ok || cmd.MaxPositional < 1 {
			continue
		}
		t.Run(name, func(t *testing.T) {
			ws := initializedWorkspace(t)
			args := tc.setup(t, ws)
			idAt := len(cmd.Path)
			if idAt >= len(args) || strings.HasPrefix(args[idAt], "--") {
				t.Fatalf("success case for %q does not put the ID at index %d: %v", name, idAt, args)
			}
			for _, missing := range []string{"C-999", "OP-999"} {
				a := append([]string{}, args...)
				a[idAt] = missing
				requireJSONErrorEnvelope(t, append(a, "--workspace", ws), 1, CodeNotFound)
			}
		})
		tested++
	}
	if tested == 0 {
		t.Fatal("no ID-taking commands were derived from the registration table")
	}
}
