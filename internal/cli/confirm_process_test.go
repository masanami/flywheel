package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// このファイルは Issue #11 の完了条件「テストは、疑似端末を割り当てた子
// プロセスとして CLI を起動する場合（端末あり）と、標準入力をパイプにして
// 起動する場合（端末なし）の両方で行う。後続チケット（#12・#13）が使える
// テスト用のヘルパーとして置く」の実装である。子プロセスは
// confirm_test.go の confirmHelperMain（このテストバイナリ自身、
// os.Args[0]、環境変数 FLYWHEEL_CLI_TEST_CONFIRM_HELPER=1）として起動する。
//
// runtime.GOOS によるスキップは行わない（macOS・Linux の両方の CI で通す）。

// confirmChildEnv は __confirm 子プロセス用の環境を組み立てる。開発者や CI の
// 実行環境（特に Claude Code のセッション内）に CLAUDECODE がたまたま設定
// されていても、AC-38 以外のテストが不安定にならないよう既定で取り除く。
func confirmChildEnv(extra ...string) []string {
	env := make([]string, 0, len(os.Environ())+len(extra)+1)
	claudeCodePrefix := claudeCodeEnvVar + "="
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, claudeCodePrefix) {
			continue
		}
		env = append(env, e)
	}
	env = append(env, confirmHelperEnvVar+"=1")
	env = append(env, extra...)
	return env
}

// confirmChildArgs は __confirm --json <id> の引数列を返す。
func confirmChildArgs(id string) []string {
	return []string{"__confirm", "--json", id}
}

// newChildCmd は os.Args[0]（このテストバイナリ自身）を、
// FLYWHEEL_CLI_TEST_CONFIRM_HELPER=1（confirmHelperMain＝defaultCommands()＋
// __confirm を知る CLI）として args で起動する *exec.Cmd を、stdout/stderr を
// バッファに向けた状態で組み立てる。stdin・SysProcAttr・ExtraFiles は呼び出し
// 側が設定する。#12（approve・reject・answer の実コマンド）の疑似端末テスト
// （approval_process_test.go）が、__confirm 専用だった newConfirmChildCmd と
// 同じ子プロセス起動の手法を任意のコマンドへ使うために切り出した。
func newChildCmd(args []string, extraEnv ...string) (cmd *exec.Cmd, stdout, stderr *bytes.Buffer) {
	cmd = exec.Command(os.Args[0], args...)
	cmd.Env = confirmChildEnv(extraEnv...)
	stdout = &bytes.Buffer{}
	stderr = &bytes.Buffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd, stdout, stderr
}

// newConfirmChildCmd は __confirm 子プロセス起動用の newChildCmd の薄い
// ラッパー（既存テストの呼び出し形を変えないために残す）。
func newConfirmChildCmd(id string, extraEnv ...string) (cmd *exec.Cmd, stdout, stderr *bytes.Buffer) {
	return newChildCmd(confirmChildArgs(id), extraEnv...)
}

// waitChild は cmd.Wait() を timeout つきで待ち、終了コードを返す（0 は
// 正常終了）。ハングでテスト全体を止めないよう、上限に達したらプロセスを
// kill してテストを失敗させる。
func waitChild(t *testing.T, cmd *exec.Cmd, timeout time.Duration) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return 0
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		t.Fatalf("cmd.Wait: %v", err)
		return -1
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		t.Fatalf("child process did not exit within %s", timeout)
		return -1
	}
}

// readErrorCode は {"error":{"code":"…"}} 形式の JSON から code を取り出す。
func readErrorCode(t *testing.T, stderr string) string {
	t.Helper()
	var doc struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stderr), &doc); err != nil {
		t.Fatalf("stderr is not valid JSON: %v (%q)", err, stderr)
	}
	return doc.Error.Code
}

// readConfirmSuccess は __confirm の成功時 JSON payload を解析する。
func readConfirmSuccess(t *testing.T, stdout string) (actor, channel, verification string) {
	t.Helper()
	var doc struct {
		Actor        string `json:"actor"`
		Channel      string `json:"channel"`
		Verification string `json:"verification"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (%q)", err, stdout)
	}
	return doc.Actor, doc.Channel, doc.Verification
}

// ptyDrain は pty の master 側を起動直後からバックグラウンドで読み続ける。
//
// 実測での注意（macOS・Linux 双方の疑似端末に共通する一般的な性質）: 誰も
// master を読まないまま子プロセスの終了を待つと、子が /dev/tty へ書いた
// 出力（要約・プロンプト）や、書き込んだ確認入力に対する端末のローカル
// エコーが疑似端末の出力キューに滞留し、キューが（小さいため）満杯になると
// 子プロセス側の write が block し、子プロセスが終了できずテストがハングする
// （このヘルパーを持たない素朴な実装で実際に発生した）。そのため、master は
// 子プロセスを起動した直後からこの ptyDrain で継続的に読み切っておく。
type ptyDrain struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	done chan struct{}
}

// startPTYDrain は master の読み取りを開始する。子プロセスが終了しすべての
// 参照（このプロセス側は Start 後に閉じている想定）が無くなると、master の
// 読み取りは最終的に EOF となり内部ゴルーチンが終了する。
func startPTYDrain(master *os.File) *ptyDrain {
	d := &ptyDrain{done: make(chan struct{})}
	go func() {
		defer close(d.done)
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				d.mu.Lock()
				d.buf.Write(buf[:n])
				d.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	return d
}

// waitDone は内部ゴルーチンが EOF を観測するまで（上限 timeout まで）待ち、
// それまでに読み取れた内容を返す。子が端末を一切開かないケース（EOF が
// すぐには来ない）でも timeout でテストを止めずに戻る。
func (d *ptyDrain) waitDone(timeout time.Duration) string {
	select {
	case <-d.done:
	case <-time.After(timeout):
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.buf.String()
}

// TestConfirm_Success_ControllingTerminalWithMatchingID は成功経路: 疑似端末を
// 制御端末（かつ標準入力）にして起動し、対象 ID と完全一致する入力を書き込むと
// 終了 0・actor が非空・channel/verification が cli/tty_confirm になること。
// 標準出力（JSON）には要約が含まれず、pty 側の出力には要約とプロンプトが
// 含まれることを確認する（/dev/tty への表示・標準入出力を使わないこと＝AC-45
// の裏付け）。
func TestConfirm_Success_ControllingTerminalWithMatchingID(t *testing.T) {
	cmd, stdout, stderr := newConfirmChildCmd("C-12")

	master, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		t.Fatalf("pty.StartWithAttrs: %v", err)
	}
	defer func() { _ = master.Close() }()
	drain := startPTYDrain(master)

	if _, err := master.Write([]byte("C-12\n")); err != nil {
		t.Fatalf("write to master: %v", err)
	}

	code := waitChild(t, cmd, 10*time.Second)
	ptyOutput := drain.waitDone(5 * time.Second)

	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stdout=%q stderr=%q pty=%q)", code, stdout.String(), stderr.String(), ptyOutput)
	}
	if stdout.Len() == 0 {
		t.Fatalf("stdout is empty, want a JSON success payload (pty=%q)", ptyOutput)
	}
	if strings.Contains(stdout.String(), confirmTestSummary) {
		t.Fatalf("stdout must not contain the summary (it belongs on /dev/tty, not stdout): %q", stdout.String())
	}
	actor, channel, verification := readConfirmSuccess(t, stdout.String())
	if actor == "" {
		t.Fatal("actor is empty")
	}
	if channel != "cli" {
		t.Fatalf("channel = %q, want %q", channel, "cli")
	}
	if verification != "tty_confirm" {
		t.Fatalf("verification = %q, want %q", verification, "tty_confirm")
	}
	if !strings.Contains(ptyOutput, confirmTestSummary) {
		t.Fatalf("pty output = %q, want it to contain the summary", ptyOutput)
	}
	// confirmPrompt("C-12") を丸ごと検索する（self-review 指摘: 素の "C-12" の
	// 部分一致だと、書き込んだ確認入力 "C-12\n" 自体の疑似端末ローカルエコーで
	// 無条件に満たされてしまい、confirmPrompt が対象 ID を実際に組み込んで
	// いるかを検証できていなかった。完全なプロンプト文字列はプログラム自身の
	// 出力にしか現れず、エコーでは再現されない）。
	if !strings.Contains(ptyOutput, confirmPrompt("C-12")) {
		t.Fatalf("pty output = %q, want it to contain the full prompt referencing the target ID", ptyOutput)
	}
}

// TestConfirm_NoTerminalStdin_ReturnsTTYRequired は AC-37: 標準入力がパイプ
// （端末なし）の場合、対象 ID を流し込んでも tty_required で拒否される。
func TestConfirm_NoTerminalStdin_ReturnsTTYRequired(t *testing.T) {
	cmd, stdout, stderr := newConfirmChildCmd("C-12")
	cmd.Stdin = strings.NewReader("C-12\n")

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	code := waitChild(t, cmd, 10*time.Second)
	if code != 1 {
		t.Fatalf("exit=%d, want 1 (stdout=%q stderr=%q)", code, stdout.String(), stderr.String())
	}
	if got := readErrorCode(t, stderr.String()); got != string(CodeTTYRequired) {
		t.Fatalf("error code = %q, want %q (stderr=%s)", got, CodeTTYRequired, stderr.String())
	}
}

// TestConfirm_PipedStdinWithControllingTerminalPresent_ReturnsTTYRequired は
// AC-45: 標準入力がパイプなら、制御端末（/dev/tty）を別に持っていても
// tty_required になる（条件①は標準入力そのものの端末性を見る）。
func TestConfirm_PipedStdinWithControllingTerminalPresent_ReturnsTTYRequired(t *testing.T) {
	ctlMaster, ctlSlave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer func() { _ = ctlMaster.Close() }()
	drain := startPTYDrain(ctlMaster)

	cmd, stdout, stderr := newConfirmChildCmd("C-12")
	cmd.Stdin = strings.NewReader("C-12\n")
	cmd.ExtraFiles = []*os.File{ctlSlave}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 3}

	if err := cmd.Start(); err != nil {
		_ = ctlSlave.Close()
		t.Fatalf("start: %v", err)
	}
	_ = ctlSlave.Close()

	code := waitChild(t, cmd, 10*time.Second)
	ptyOutput := drain.waitDone(5 * time.Second)
	if code != 1 {
		t.Fatalf("exit=%d, want 1 (stdout=%q stderr=%q pty=%q)", code, stdout.String(), stderr.String(), ptyOutput)
	}
	if got := readErrorCode(t, stderr.String()); got != string(CodeTTYRequired) {
		t.Fatalf("error code = %q, want %q", got, CodeTTYRequired)
	}
}

// TestConfirm_ReadsFromControllingTerminalNotFromStdinPTY は AC-45 の直接検証:
// 標準入力が（制御端末とは別の）疑似端末であっても、確認は /dev/tty
// （制御端末側）からだけ読まれる。標準入力側の疑似端末へ正しい ID を、
// 制御端末側へ別の値を書き込むと confirmation_mismatch になることで、
// 標準入力からは読んでいないことを直接示す。
func TestConfirm_ReadsFromControllingTerminalNotFromStdinPTY(t *testing.T) {
	stdinMaster, stdinSlave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open (stdin): %v", err)
	}
	defer func() { _ = stdinMaster.Close() }()
	stdinDrain := startPTYDrain(stdinMaster)

	ctlMaster, ctlSlave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open (ctl): %v", err)
	}
	defer func() { _ = ctlMaster.Close() }()
	ctlDrain := startPTYDrain(ctlMaster)

	cmd, stdout, stderr := newConfirmChildCmd("C-12")
	cmd.Stdin = stdinSlave
	cmd.ExtraFiles = []*os.File{ctlSlave}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 3}

	if err := cmd.Start(); err != nil {
		_ = stdinSlave.Close()
		_ = ctlSlave.Close()
		t.Fatalf("start: %v", err)
	}
	_ = stdinSlave.Close()
	_ = ctlSlave.Close()

	if _, err := stdinMaster.Write([]byte("C-12\n")); err != nil {
		t.Fatalf("write to stdin pty: %v", err)
	}
	if _, err := ctlMaster.Write([]byte("nope\n")); err != nil {
		t.Fatalf("write to ctl pty: %v", err)
	}

	code := waitChild(t, cmd, 10*time.Second)
	_ = stdinDrain.waitDone(5 * time.Second)
	ctlOutput := ctlDrain.waitDone(5 * time.Second)
	if code != 1 {
		t.Fatalf("exit=%d, want 1 (stdout=%q stderr=%q ctl_pty=%q)", code, stdout.String(), stderr.String(), ctlOutput)
	}
	if got := readErrorCode(t, stderr.String()); got != string(CodeConfirmationMismatch) {
		t.Fatalf("error code = %q, want %q (the stdin pty had the matching ID; confirmation must come from the controlling terminal, not stdin)", got, CodeConfirmationMismatch)
	}
}

// TestConfirm_SessionWithoutControllingTerminal_ReturnsTTYRequired は成立条件
// ②の単独検証: 標準入力は端末（疑似端末）だが、セッションに制御端末が無い
// （Setsid のみ、Setctty なし）場合は /dev/tty を開けず tty_required になる。
func TestConfirm_SessionWithoutControllingTerminal_ReturnsTTYRequired(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer func() { _ = master.Close() }()
	drain := startPTYDrain(master)

	cmd, stdout, stderr := newConfirmChildCmd("C-12")
	cmd.Stdin = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: false}

	if err := cmd.Start(); err != nil {
		_ = slave.Close()
		t.Fatalf("start: %v", err)
	}
	_ = slave.Close()

	if _, err := master.Write([]byte("C-12\n")); err != nil {
		t.Fatalf("write to master: %v", err)
	}

	code := waitChild(t, cmd, 10*time.Second)
	ptyOutput := drain.waitDone(5 * time.Second)
	if code != 1 {
		t.Fatalf("exit=%d, want 1 (stdout=%q stderr=%q pty=%q)", code, stdout.String(), stderr.String(), ptyOutput)
	}
	if got := readErrorCode(t, stderr.String()); got != string(CodeTTYRequired) {
		t.Fatalf("error code = %q, want %q (stdin is a terminal, but this session has no controlling terminal)", got, CodeTTYRequired)
	}
}

// TestConfirm_ClaudeCodeEnvSet_ReturnsTTYRequiredWithoutShowingSummary は
// AC-38: 端末ありでも CLAUDECODE が設定されていれば tty_required になり、
// 要約は表示されない（H10: 補助の歯止めであり、/dev/tty を開く前に拒否する）。
func TestConfirm_ClaudeCodeEnvSet_ReturnsTTYRequiredWithoutShowingSummary(t *testing.T) {
	for _, val := range []string{"1", ""} {
		t.Run(claudeCodeEnvVar+"="+val, func(t *testing.T) {
			cmd, stdout, stderr := newConfirmChildCmd("C-12", claudeCodeEnvVar+"="+val)

			master, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{Setsid: true, Setctty: true})
			if err != nil {
				t.Fatalf("pty.StartWithAttrs: %v", err)
			}
			defer func() { _ = master.Close() }()
			drain := startPTYDrain(master)

			code := waitChild(t, cmd, 10*time.Second)
			ptyOutput := drain.waitDone(5 * time.Second)

			if code != 1 {
				t.Fatalf("exit=%d, want 1 (stdout=%q stderr=%q pty=%q)", code, stdout.String(), stderr.String(), ptyOutput)
			}
			if got := readErrorCode(t, stderr.String()); got != string(CodeTTYRequired) {
				t.Fatalf("error code = %q, want %q", got, CodeTTYRequired)
			}
			if strings.Contains(ptyOutput, confirmTestSummary) {
				t.Fatalf("pty output must be empty of the summary when CLAUDECODE is set (must reject before opening /dev/tty): %q", ptyOutput)
			}
		})
	}
}

// TestConfirm_MismatchedConfirmation は AC-44: 空行・"y"・別の ID・入力の終端
// （EOF、行頭で \x04 = VEOF）のいずれも confirmation_mismatch で拒否される。
func TestConfirm_MismatchedConfirmation(t *testing.T) {
	cases := []struct {
		name  string
		input []byte
	}{
		{"empty line", []byte("\n")},
		{"y", []byte("y\n")},
		{"different id", []byte("C-13\n")},
		{"eof", []byte("\x04")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, stdout, stderr := newConfirmChildCmd("C-12")

			master, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{Setsid: true, Setctty: true})
			if err != nil {
				t.Fatalf("pty.StartWithAttrs: %v", err)
			}
			defer func() { _ = master.Close() }()
			drain := startPTYDrain(master)

			if _, err := master.Write(tc.input); err != nil {
				t.Fatalf("write to master: %v", err)
			}

			code := waitChild(t, cmd, 10*time.Second)
			ptyOutput := drain.waitDone(5 * time.Second)
			if code != 1 {
				t.Fatalf("exit=%d, want 1 (stdout=%q stderr=%q pty=%q)", code, stdout.String(), stderr.String(), ptyOutput)
			}
			if got := readErrorCode(t, stderr.String()); got != string(CodeConfirmationMismatch) {
				t.Fatalf("error code = %q, want %q (stderr=%s)", got, CodeConfirmationMismatch, stderr.String())
			}
		})
	}
}

// TestConfirm_EmptyExpectedIDNeverMatchesEmptyInput は self-review 指摘の
// 再発防止テスト: 対象の ID が空文字であっても、空行の確認入力で成立しては
// ならない（空行は expectedID の値に関わらず常に confirmation_mismatch。
// AC-44「空行…は confirmation_mismatch」は expectedID の値で条件分岐しない）。
// このテストが無い状態では、__confirm "" に対して空行を送ると誤って成立
// していた（ttyconfirm.go の修正前の fail-open）。
func TestConfirm_EmptyExpectedIDNeverMatchesEmptyInput(t *testing.T) {
	cmd, stdout, stderr := newConfirmChildCmd("")

	master, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		t.Fatalf("pty.StartWithAttrs: %v", err)
	}
	defer func() { _ = master.Close() }()
	startPTYDrain(master)

	if _, err := master.Write([]byte("\n")); err != nil {
		t.Fatalf("write to master: %v", err)
	}

	code := waitChild(t, cmd, 10*time.Second)
	if code != 1 {
		t.Fatalf("exit=%d, want 1 (stdout=%q stderr=%q)", code, stdout.String(), stderr.String())
	}
	if got := readErrorCode(t, stderr.String()); got != string(CodeConfirmationMismatch) {
		t.Fatalf("error code = %q, want %q (an empty confirmation line must never match, even for an empty expected ID; stderr=%s)", got, CodeConfirmationMismatch, stderr.String())
	}
}

// AC-50 の CLI 側（mapCoreErr(core.ErrVerificationRejected) が
// verification_rejected・終了コード 1 になること）は
// internal/cli/errors_test.go の TestMapCoreErr_VerificationErrors が担う。
// core 側の登録簿の網羅は internal/core/verification_test.go が担う。
