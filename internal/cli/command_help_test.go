package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// runHelp は本番のコマンド表で args を実行し、終了コードと出力を返す。
func runHelp(args ...string) (int, string, string) {
	var out, errb strings.Builder
	code := run(args, strings.NewReader(""), &out, &errb, defaultCommands())
	return code, out.String(), errb.String()
}

// 登録されたすべてのコマンドは、`--help` と `-h` で自分の使い方を標準出力へ出して 0 で終わる。
// 一覧はコマンドの定義（defaultCommands）から引くので、足したコマンドも漏れない。
// 必須の引数・フラグは渡さず、ワークスペースの無いディレクトリを指す（使い方はストアを開かずに出る）。
func TestCommandHelp_EveryRegisteredCommandPrintsItsUsageAndExitsZero(t *testing.T) {
	t.Parallel()
	commands := defaultCommands()
	if len(commands) == 0 {
		t.Fatal("no registered commands")
	}
	for _, cmd := range commands {
		path := strings.Join(cmd.Path, " ")
		for _, h := range []string{"--help", "-h"} {
			args := append(append([]string{}, cmd.Path...), "--workspace", t.TempDir(), h)
			code, out, errOut := runHelp(args...)
			if code != 0 || errOut != "" {
				t.Errorf("%s %s: exit %d stderr %q", path, h, code, errOut)
				continue
			}
			hasUsageLine := strings.Contains(out, "\n  flywheel "+path+" ") || strings.Contains(out, "\n  flywheel "+path+"\n")
			if !strings.HasPrefix(out, "flywheel "+path+" - 使い方\n") || !hasUsageLine || !strings.Contains(out, "--workspace <dir>") {
				t.Errorf("%s %s: stdout lacks the usage:\n%s", path, h, out)
			}
			// ほかの登録コマンドの項目は混ざらない（`cycle --help` に `cycle status` の行を出さない）。
			for _, line := range strings.Split(out, "\n") {
				rest, ok := strings.CutPrefix(line, "  flywheel ")
				if !ok {
					continue
				}
				if got, _, found := matchCommand(strings.Fields(rest), commands); !found || strings.Join(got.Path, " ") != path {
					t.Errorf("%s %s: stdout shows another command's usage line: %q", path, h, line)
				}
			}
		}
	}
}

// 使い方は一覧（`flywheel --help`）の項目をそのまま使う。複数の項目と、字下げした続きの行も載せる。
func TestCommandHelp_ShowsEveryEntryAndContinuationLine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"budget", "--help"}, []string{
			"\n  flywheel budget <C-ID> --impl-usd <額> ",
			"\n  flywheel budget <C-ID> --limit <額> ",
		}},
		{[]string{"approve", "--help"}, []string{"\n            （--reopen＝OP-ID だけ。"}},
		{[]string{"cycle", "--help"}, []string{"\n  flywheel cycle [--trigger <t>] [--detach]"}},
		{[]string{"cycle", "status", "--help"}, []string{"\n  flywheel cycle status "}},
		{[]string{"dep", "rm", "-h"}, []string{"\n  flywheel dep rm <C-ID> "}},
	} {
		code, out, errOut := runHelp(tc.args...)
		if code != 0 {
			t.Errorf("%v: exit %d stderr %q", tc.args, code, errOut)
			continue
		}
		for _, want := range tc.want {
			if !strings.Contains(out, want) {
				t.Errorf("%v: stdout lacks %q:\n%s", tc.args, want, out)
			}
		}
	}
}

// ほかの引数・フラグがあっても、足りなくても、誤っていても、使い方を優先する。
func TestCommandHelp_WinsOverOtherArguments(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"create", "--help", "--title", "t"},
		{"create", "--title", "t", "-h"},
		{"show", "C-1", "extra", "--help"},
		{"list", "--no-such-flag", "--help"},
		{"list", "--help=x", "--help"},
		{"list", "--status=x", "--help"},
		{"approve", "--help", "--no-cycle"},
		{"status", "--json", "--help"},
		{"edit", "--title=", "--help"},
	} {
		code, out, errOut := runHelp(args...)
		if code != 0 || errOut != "" || !strings.HasPrefix(out, "flywheel "+args[0]+" - 使い方\n") {
			t.Errorf("%v: exit %d stderr %q stdout:\n%s", args, code, errOut, out)
		}
	}
}

// 値を付けた形（`--help=...`）と、値を取るフラグの値としての --help は、使い方を求めたことにしない。
// 未知のコマンドは、--help があっても未知のコマンドのまま。
func TestCommandHelp_NotAHelpRequestKeepsTheUsageError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"status", "--help=true"}, "未知のフラグ: --help"},
		{[]string{"status", "-h=1"}, "未知のフラグ: --h"},
		{[]string{"create", "--description", "--help"}, "--title は必須です"},
		{[]string{"no-such", "--help"}, "未知のコマンド"},
		{[]string{"dep", "--help"}, "未知のコマンド"},
	} {
		code, out, errOut := runHelp(tc.args...)
		if code != 2 || out != "" || !strings.Contains(errOut, string(CodeUsageError)) || !strings.Contains(errOut, tc.want) {
			t.Errorf("%v: exit %d stdout %q stderr %q", tc.args, code, out, errOut)
		}
	}
}

// 委譲の子の環境でも、使い方は出る（flow の --help と同じ。コマンドは実行しない）。--help が無ければ拒否のまま。
func TestCommandHelp_InADelegatedChildEnvironment(t *testing.T) {
	t.Setenv(core.DelegatedRunEnvVar, "R-1")
	if code, out, errOut := runHelp("status", "--help"); code != 0 || errOut != "" || !strings.Contains(out, "\n  flywheel status\n") {
		t.Errorf("status --help: exit %d stderr %q stdout:\n%s", code, errOut, out)
	}
	for _, args := range [][]string{{"status"}, {"status", "--help=true"}, {"create", "--title", "--help"}} {
		if code, out, errOut := runHelp(args...); code != 1 || out != "" || !strings.Contains(errOut, string(CodeVerificationRejected)) {
			t.Errorf("%v: exit %d stdout %q stderr %q", args, code, out, errOut)
		}
	}
}
