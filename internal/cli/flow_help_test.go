package cli

import (
	"strings"
	"testing"
)

// `flywheel flow [<サブコマンド>] --help`（-h）は、そのコマンドの使い方を標準出力へ出して 0 で終わる。
// 文は `flywheel --help` の一覧の flow の行と同じ。ストアもワークスペースも要らない。
func TestFlowHelp_PrintsTheUsageAndExitsZero(t *testing.T) {
	t.Parallel()
	usageLine := func(sub string) string {
		for _, l := range strings.Split(usageText, "\n") {
			if strings.HasPrefix(l, "  flow "+sub+" ") {
				return strings.TrimPrefix(l, "  ")
			}
		}
		t.Fatalf("usageText has no line for flow %s", sub)
		return ""
	}
	subs := []string{"start", "status", "resume", "cancel", "export", "check"}
	for _, sub := range subs {
		for _, h := range []string{"--help", "-h"} {
			for _, args := range [][]string{
				{"flow", sub, h},
				{"flow", sub, "--workspace", t.TempDir(), h},
			} {
				var out, errb strings.Builder
				code := run(args, strings.NewReader(""), &out, &errb, defaultCommands())
				if code != 0 || errb.Len() != 0 {
					t.Errorf("%v: exit %d stderr %q", args, code, errb.String())
					continue
				}
				if !strings.Contains(out.String(), "  flywheel "+usageLine(sub)+"\n") || !strings.Contains(out.String(), "--workspace <dir>") {
					t.Errorf("%v: stdout lacks the usage line:\n%s", args, out.String())
				}
				for _, other := range subs {
					if other != sub && strings.Contains(out.String(), "flywheel flow "+other+" ") {
						t.Errorf("%v: stdout also shows flow %s:\n%s", args, other, out.String())
					}
				}
			}
		}
	}
	// flow check の続きの行（字下げした 2 行目）も載せる。
	var out, errb strings.Builder
	if code := run([]string{"flow", "check", "--help"}, strings.NewReader(""), &out, &errb, defaultCommands()); code != 0 ||
		!strings.Contains(out.String(), "ワークスペースに置いていない名前は組み込みを検査する") {
		t.Errorf("flow check --help: exit %d\n%s", code, out.String())
	}

	// flow --help は flow のコマンドをすべて出す（内部の入口は出さない）。
	for _, h := range []string{"--help", "-h"} {
		var out, errb strings.Builder
		if code := run([]string{"flow", h}, strings.NewReader(""), &out, &errb, defaultCommands()); code != 0 || errb.Len() != 0 {
			t.Fatalf("flow %s: exit %d stderr %q", h, code, errb.String())
		}
		for _, sub := range subs {
			if !strings.Contains(out.String(), "  flywheel "+usageLine(sub)+"\n") {
				t.Errorf("flow %s lacks flow %s:\n%s", h, sub, out.String())
			}
		}
		if strings.Contains(out.String(), "__run-detached") {
			t.Errorf("flow %s shows the internal entry:\n%s", h, out.String())
		}
	}
}

// 値を取るフラグの値としての --help は、使い方を求めたことにしない（いつもの引数の解釈に進む）。
func TestFlowHelp_AFlagValueIsNotAHelpRequest(t *testing.T) {
	t.Parallel()
	if _, ok := flowHelp([]string{"flow", "resume", "--note", "--help", "RUN-1"}); ok {
		t.Error("--note --help must not be read as a help request")
	}
	if _, ok := flowHelp([]string{"flow", "resume", "--note=x", "--help"}); !ok {
		t.Error("--help after an inline value must be a help request")
	}
	if _, ok := flowHelp([]string{"flow", "no-such", "--help"}); ok {
		t.Error("an unknown subcommand must not print a usage")
	}
	if _, ok := flowHelp([]string{"flow", "__run-detached", "--help"}); ok {
		t.Error("the internal entry is not listed, so it has no usage")
	}
}
