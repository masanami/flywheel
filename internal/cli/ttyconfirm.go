package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/masanami/flywheel/internal/core"
)

// controlTTYPath は制御端末のパス。テストはこの定数を差し替えない
// （internal/cli/abspath_test.go の絶対パス検査「/Users/ または /home/ で
// 始まる文字列リテラルを禁止する」には該当しない）。
const controlTTYPath = "/dev/tty"

// claudeCodeEnvVar は tty_confirm の成立条件③が見る環境変数名
// （H10。値は見ず設定の有無だけを見る）。本番コードとテストヘルパー
// （confirm_process_test.go）の双方がこの定数を参照する。文字列リテラルを
// 2 箇所に独立して持つと、名前が変わったときに片方だけ更新され検査が
// 効かなくなる（self-review 指摘）。
const claudeCodeEnvVar = "CLAUDECODE"

// ttyConfirmVerifier は core.Verifier の tty_confirm 実装
// （docs/features/m1-core.md §クリティカル設計決定 1）。
type ttyConfirmVerifier struct {
	stdin io.Reader
}

// newTTYConfirmVerifier は stdin を判定対象にする core.Verifier を作る。
// #12 の承認コマンド（approve/reject/answer）は Args.Stdin をそのまま渡す。
func newTTYConfirmVerifier(stdin io.Reader) core.Verifier {
	return &ttyConfirmVerifier{stdin: stdin}
}

func (v *ttyConfirmVerifier) Method() core.Verification {
	return core.VerificationTTYConfirm
}

// Confirm は tty_confirm の成立条件（①標準入力が端末 ②制御端末を開ける
// ③ CLAUDECODE が未設定）を検査し、成立すれば要約の表示と確認の読み取りを
// /dev/tty に対して行う（標準入力からは読まない。パイプで流し込んだ入力を
// 確認として使わせない＝AC-45）。
func (v *ttyConfirmVerifier) Confirm(summary, expectedID string) (string, error) {
	// 条件①: 標準入力が端末であること。*os.File でなければこの時点で
	// 端末ではないと判定できる（パイプ・strings.Reader 等）。
	stdinFile, ok := v.stdin.(*os.File)
	if !ok {
		return "", fmt.Errorf("%w: standard input is not backed by an *os.File", core.ErrTTYRequired)
	}
	if !term.IsTerminal(int(stdinFile.Fd())) {
		return "", fmt.Errorf("%w: standard input is not a terminal", core.ErrTTYRequired)
	}

	// 条件③: CLAUDECODE は補助の歯止めであり、未設定であることを本人確認の
	// 根拠にはしない（H10）。値は見ず、設定の有無だけを見る（fail-closed:
	// 空文字であっても設定されていれば拒否する）。
	if _, set := os.LookupEnv(claudeCodeEnvVar); set {
		return "", fmt.Errorf("%w: %s is set", core.ErrTTYRequired, claudeCodeEnvVar)
	}

	// 条件②: 制御端末（/dev/tty）を開けること。
	tty, err := os.OpenFile(controlTTYPath, os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("%w: could not open the controlling terminal: %w", core.ErrTTYRequired, err)
	}
	defer func() { _ = tty.Close() }()

	if _, err := fmt.Fprintf(tty, "%s\n\n%s", summary, confirmPrompt(expectedID)); err != nil {
		return "", fmt.Errorf("%w: could not write to the controlling terminal: %w", core.ErrTTYRequired, err)
	}

	reader := bufio.NewReader(tty)
	line, readErr := reader.ReadString('\n')
	if line == "" && readErr != nil {
		// 入力の終端（1 行も読めない EOF）は不一致として扱う。
		return "", core.ErrConfirmationMismatch
	}

	// 行末の \n／\r\n だけを除き、それ以外は一切トリムしない。\r\n を単位で
	// 先に試し、それが無ければ \n だけを試す（\n を伴わない孤立した末尾 \r は
	// 対象外＝self-review 指摘: 無条件の2段 TrimSuffix だと孤立した \r まで
	// 削ってしまい「\n／\r\n だけを除く」という仕様より緩くなっていた）。
	input := strings.TrimSuffix(line, "\r\n")
	if input == line {
		input = strings.TrimSuffix(line, "\n")
	}

	// 空行は（expectedID が空でない通常運用はもちろん）常に不一致として扱う
	// （self-review 指摘: expectedID が空文字のケースで「空行がそのまま
	// 一致してしまう」fail-open な抜け道を、expectedID の値に関係なく
	// 塞ぐ。空行・"y"・別の ID・入力の終端はいずれも confirmation_mismatch
	// という仕様の通り）。
	if input == "" || input != expectedID {
		return "", core.ErrConfirmationMismatch
	}

	actor, err := core.CurrentActor()
	if err != nil {
		return "", err
	}
	return actor, nil
}

func confirmPrompt(expectedID string) string {
	return fmt.Sprintf("確認のため対象の ID（%s）を入力してください: ", expectedID)
}
