package cli

import (
	"os"

	"github.com/masanami/flywheel/internal/core"
)

// confirmHelperEnvVar が "1" のとき、このテストバイナリは通常のテストを実行
// せず、TestMain（busy_test.go）から分岐して「__confirm というテスト専用
// コマンドだけを知る CLI」として振る舞う。#12・#13 が使う疑似端末のテスト
// ヘルパー（confirm_process_test.go）が、このテストバイナリ自身
// （os.Args[0]）を子プロセスとして起動するために使う（busyHolderMain と
// 同じ手法）。
const confirmHelperEnvVar = "FLYWHEEL_CLI_TEST_CONFIRM_HELPER"

// confirmTestSummary は __confirm コマンドが core.Verify に渡す要約の固定文。
// テストは pty 側の出力にこの文字列が含まれるかどうかで、要約が表示された
// （＝/dev/tty まで到達した）ことを判定する。
const confirmTestSummary = "確認テスト: 対象を確認してください"

// confirmHelperMain は confirmHelperEnvVar=1 のときの子プロセスの本体。
// os.Stdin・os.Stdout・os.Stderr をそのまま渡して run(...) を呼び、返った
// 終了コードで os.Exit する。
func confirmHelperMain() {
	commands := append(defaultCommands(), confirmTestCommand())
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, commands))
}

// confirmTestCommand は Issue #11 の「approve などの実コマンドが無い時点で
// Verifier を呼ぶ最小のテスト用の入口」。本番の defaultCommands() には
// 含めない（help.go の usage も変えない）。#12 は実コマンド
// （approve/reject/answer）から core.Verify を直接呼ぶ。
func confirmTestCommand() Command {
	return Command{
		Path:          []string{"__confirm"},
		MinPositional: 1,
		MaxPositional: 1,
		RequiresStore: false,
		Run:           runConfirmTest,
	}
}

// runConfirmTest は core.Verify(core.ChannelCLI, tty_confirm Verifier, ...) を
// 呼び、成立すれば {"actor":…, "channel":"cli", "verification":"tty_confirm"}
// を返す。core.ErrVerificationRejected・ErrTTYRequired・
// ErrConfirmationMismatch は mapCoreErr がそれぞれの ErrorCode へ写像する。
func runConfirmTest(a Args) (any, error) {
	att, err := core.Verify(core.ChannelCLI, newTTYConfirmVerifier(a.Stdin), confirmTestSummary, a.Positional[0])
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return map[string]any{
		"actor":        att.Actor(),
		"channel":      string(att.Channel()),
		"verification": string(att.Verification()),
	}, nil
}
