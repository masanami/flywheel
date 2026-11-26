package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/flow"
)

// flow start の --budget-usd の配線と使い方の誤り。

// AC-141・AC-142・AC-143: 使い方の誤り（終了コード 2）で、実行のフォルダを作らず、標準出力には何も出さない。
func TestFlowStart_BudgetUSDAndOverrideUsageErrors(t *testing.T) {
	t.Parallel()
	bin := fakeFlowPlugin(t, "5.3.0")
	for _, c := range []struct {
		name, flow string
		args       []string
		want       string
	}{
		{"AC-141 ticket", "ticket", []string{"--budget-usd", "8"}, "--budget-usd"},
		{"AC-141 ticket-light", "ticket-light", []string{"--budget-usd", "8"}, "--budget-usd"},
		{"AC-141 reflect", "reflect", []string{"--budget-usd", "8"}, "--budget-usd"},
		{"AC-142 zero", "spec-draft", []string{"--budget-usd", "0"}, "--budget-usd"},
		{"negative", "spec-draft", []string{"--budget-usd", "-1"}, "--budget-usd"},
		{"not a number", "spec-draft", []string{"--budget-usd", "abc"}, "--budget-usd"},
		{"empty", "spec-draft", []string{"--budget-usd", ""}, "--budget-usd"},
		{"NaN", "spec-draft", []string{"--budget-usd", "NaN"}, "--budget-usd"},
		{"infinity", "spec-draft", []string{"--budget-usd", "Inf"}, "--budget-usd"},
		{"AC-143 override", "spec-draft", []string{"--override", "x.yaml"}, "--override"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ws := flowWorkspaceDir(t)
			args := append([]string{"start", "--workspace", ws, "--permission-mode", "auto", "--delegated-run", "R-1"}, c.args...)
			out, errOut, code := runFlowChild(t, flowChildEnv(bin), t.TempDir(), append(args, c.flow)...)
			if code != 2 || out != "" || !strings.Contains(errOut, "usage_error") || !strings.Contains(errOut, c.want) {
				t.Fatalf("exit %d stdout %q stderr %q, want a usage error (exit 2) about %s", code, out, errOut, c.want)
			}
			if _, err := os.Stat(filepath.Join(ws, ".flywheel", "flow")); !os.IsNotExist(err) {
				t.Errorf(".flywheel/flow was created (stat err: %v)", err)
			}
		})
	}
}

// AC-139・AC-140（CLI の配線）: 渡した額が flow へ届き、付けなければ 0（定義の値）。記録は internal/flow の試験が確かめる。
func TestFlowStart_PassesTheBudgetToTheFlow(t *testing.T) {
	t.Setenv(core.DelegatedRunEnvVar, "") // 後片付けで元の値へ戻すために先に置いてから外す
	if err := os.Unsetenv(core.DelegatedRunEnvVar); err != nil {
		t.Fatal(err)
	}
	var got flow.StartOptions
	old := flowStart
	flowStart = func(_ flow.Env, o flow.StartOptions) int { got = o; return 0 }
	t.Cleanup(func() { flowStart = old })
	for _, c := range []struct {
		args []string
		want float64
	}{{[]string{"--budget-usd", "8"}, 8}, {[]string{"--budget-usd=2.5"}, 2.5}, {nil, 0}} {
		args := append([]string{"flow", "start", "--workspace", flowWorkspaceDir(t), "--permission-mode", "auto", "--delegated-run", "R-1"}, c.args...)
		got = flow.StartOptions{}
		var out, errb bytes.Buffer
		if code := run(append(args, "spec-draft"), strings.NewReader(""), &out, &errb, defaultCommands()); code != 0 {
			t.Fatalf("%v: exit %d %s", c.args, code, errb.String())
		}
		if got.Name != "spec-draft" || got.BudgetUSD != c.want {
			t.Errorf("%v: options = %+v, want budget %v", c.args, got, c.want)
		}
	}
}

// AC-144
func TestHelp_FlowStartUsageShowsBudgetUSD(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	if code := run([]string{"help"}, strings.NewReader(""), &out, &errb, defaultCommands()); code != 0 {
		t.Fatalf("help exit %d %s", code, errb.String())
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "flow start ") {
			if !strings.Contains(line, "[--budget-usd <額>]") {
				t.Fatalf("flow start usage line lacks --budget-usd: %s", line)
			}
			return
		}
	}
	t.Fatalf("no flow start usage line:\n%s", out.String())
}
