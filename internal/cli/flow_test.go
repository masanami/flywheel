package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/flow"
)

// flywheel flow（組み込みのフローの入口）の配線の試験。進め方そのものは internal/flow の試験が持つ。ここは、引数の解釈・
// help の表示・終了コードと標準出力が cli を通って届くことを、この試験のバイナリを子プロセスとして起動して確かめる。
// 子プロセスの環境は、委譲の子の目印（FLYWHEEL_DELEGATED_RUN）を外して明示する（目印の拒否を検査する試験だけが付ける）。

// flowChildEnv は flow を子プロセスで起動するときの環境: 親の環境から目印を外し、PATH の先頭に bin を置く。
func flowChildEnv(bin string, extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == core.DelegatedRunEnvVar || k == "PATH" || k == "FLYWHEEL_WORKSPACE" {
			continue
		}
		env = append(env, kv)
	}
	return append(append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH")), extra...)
}

func runFlowChild(t *testing.T, env []string, dir string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"flow"}, args...)...)
	cmd.Env = env
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errb.String(), code
}

// fakeFlowPlugin は、版が version のプラグインを指す claude-harness-run を置いた bin のディレクトリを返す。
func fakeFlowPlugin(t *testing.T, version string) string {
	t.Helper()
	root := t.TempDir()
	plugin := filepath.Join(root, "plugin")
	for p, content := range map[string]string{
		filepath.Join(plugin, ".claude-plugin", "plugin.json"):    fmt.Sprintf(`{"name":"claude-harness","version":%q}`, version),
		filepath.Join(plugin, "agents", "feature-implementer.md"): "---\nname: feature-implementer\ndescription: test double\n---\n",
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\n[ \"$1\" = --plugin-root ] && echo %q\n", plugin)
	if err := os.WriteFile(filepath.Join(bin, "claude-harness-run"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func flowWorkspaceDir(t *testing.T) string {
	t.Helper()
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	return ws
}

// AC-47: flywheel help の一覧に flow が「内部向け」の印つきで出る。AC-48: __run-detached は出ない。
func TestHelp_ListsFlowAsInternalAndHidesTheDetachedEntry(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	if code := run([]string{"help"}, strings.NewReader(""), &out, &errb, defaultCommands()); code != 0 {
		t.Fatalf("help exit %d: %s", code, errb.String())
	}
	help := out.String()
	for _, sub := range []string{"start", "status", "resume", "cancel"} {
		found := false
		for _, line := range strings.Split(help, "\n") {
			if strings.HasPrefix(line, "  flow "+sub+" ") || line == "  flow "+sub {
				found = true
				if !strings.Contains(line, "内部向け") {
					t.Errorf("the help line for flow %s lacks the 内部向け mark: %q", sub, line)
				}
			}
		}
		if !found {
			t.Errorf("flywheel help does not list flow %s", sub)
		}
	}
	if strings.Contains(help, "__run-detached") {
		t.Errorf("flywheel help lists __run-detached:\n%s", help)
	}
}

// flow のコマンドは M1 の IF/API の表（defaultCommands）には載せず、別の表から引く。
func TestFlowCommandsAreResolvedOutsideTheIFAPITable(t *testing.T) {
	t.Parallel()
	for _, c := range defaultCommands() {
		if c.Path[0] == "flow" {
			t.Errorf("defaultCommands() has %v; flow commands live in flowCommands()", c.Path)
		}
	}
	var names []string
	for _, c := range flowCommands() {
		names = append(names, strings.Join(c.Path, " "))
		if c.RequiresStore {
			t.Errorf("%v must not open the store (the paths are decided from --workspace)", c.Path)
		}
	}
	if got, want := strings.Join(names, ","), "flow start,flow status,flow resume,flow cancel,flow __run-detached"; got != want {
		t.Errorf("flow commands = %s, want %s", got, want)
	}
}

// AC-34〜36 を cli 経由で: claude-harness-run が PATH に無いとき、実行のフォルダを作らず、0 でない終了コードで終わり、
// 標準エラーにその名前が出る。
func TestFlowStart_WithoutTheLauncherFailsAndCreatesNothing(t *testing.T) {
	t.Parallel()
	ws := flowWorkspaceDir(t)
	empty := t.TempDir()
	env := flowChildEnv(empty)
	for i, kv := range env { // PATH を空のディレクトリだけにして、本物の claude-harness-run が見つからないようにする
		if strings.HasPrefix(kv, "PATH=") {
			env[i] = "PATH=" + empty
		}
	}
	out, errOut, code := runFlowChild(t, env, t.TempDir(), "start", "--workspace", ws, "--input", "issue=1", "ticket")
	if code == 0 || !strings.Contains(errOut, "claude-harness-run") || out != "" {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(ws, ".flywheel", "flow")); !os.IsNotExist(err) {
		t.Errorf(".flywheel/flow was created (stat err: %v)", err)
	}
}

// AC-37〜39 を cli 経由で: 範囲の外の版は、読んだ版と範囲を標準エラーに出して 0 でない終了コードで終わる。
func TestFlowStart_PluginOutOfRangeReportsVersionAndRange(t *testing.T) {
	t.Parallel()
	ws := flowWorkspaceDir(t)
	bin := fakeFlowPlugin(t, "6.0.0")
	_, errOut, code := runFlowChild(t, flowChildEnv(bin), t.TempDir(), "start", "--workspace", ws, "--input", "issue=1", "ticket")
	if code == 0 {
		t.Fatalf("exit 0, stderr %q", errOut)
	}
	for _, want := range []string{"6.0.0", "4.9.0 以上 6.0.0 未満"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q: %q", want, errOut)
		}
	}
	if _, err := os.Stat(filepath.Join(ws, ".flywheel", "flow")); !os.IsNotExist(err) {
		t.Errorf(".flywheel/flow was created (stat err: %v)", err)
	}
}

// start・status・resume・cancel の引数が flow へ届き、契約の JSON が標準出力に出る（AC-14・AC-28）。
// ステップの中身は要らないので、偽の gh を置かず（Issue を読めない resolve で）すぐ failed になる実行で通す。
func TestFlowCommands_ThroughTheCLI(t *testing.T) {
	t.Parallel()
	ws := flowWorkspaceDir(t)
	bin := fakeFlowPlugin(t, "5.3.0")
	env := flowChildEnv(bin, "FAKE_GH_DIR="+t.TempDir())
	if err := os.Symlink("/usr/bin/false", filepath.Join(bin, "gh")); err != nil {
		t.Skipf("cannot place a failing gh: %v", err)
	}
	// resolve の gh が失敗する → run は failed で終わる

	out, errOut, code := runFlowChild(t, env, t.TempDir(), "start", "--workspace", ws, "--permission-mode", "auto", "--delegated-run", "R-1", "--input", "issue=1", "--input", "challenge_id=C-1", "ticket")
	if code != 0 {
		t.Fatalf("start exit %d\n%s\n%s", code, out, errOut)
	}
	var doc struct {
		ContractVersion int     `json:"contract_version"`
		RunID           *string `json:"run_id"`
		State           string  `json:"state"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc.ContractVersion != 1 || doc.RunID == nil || *doc.RunID == "" {
		t.Fatalf("start stdout is not a contract JSON: %v\n%s", err, out)
	}
	id := *doc.RunID
	if _, err := os.Stat(filepath.Join(ws, ".flywheel", "flow", "runs", id, "events.jsonl")); err != nil {
		t.Fatalf("the run folder is not under the workspace: %v", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	defer func() { // runner が終わってから一時ディレクトリを消す（runner.log は書き終えた後に閉じる）
		end := time.Now().Add(30 * time.Second)
		for time.Now().Before(end) {
			b, _ := os.ReadFile(filepath.Join(ws, ".flywheel", "flow", "runs", id, "events.jsonl"))
			var pid int
			for _, line := range strings.Split(string(b), "\n") {
				if strings.Contains(line, `"runner_started"`) {
					var ev struct {
						RS struct {
							PID int `json:"pid"`
						} `json:"runner_started"`
					}
					if json.Unmarshal([]byte(line), &ev) == nil {
						pid = ev.RS.PID
					}
				}
			}
			if pid == 0 || syscall.Kill(pid, 0) != nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Error("the detached runner did not end")
	}()
	for {
		out, errOut, code = runFlowChild(t, env, t.TempDir(), "status", "--workspace", ws, id)
		if code != 0 {
			t.Fatalf("status exit %d\n%s\n%s", code, out, errOut)
		}
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("status stdout: %v\n%s", err, out)
		}
		if doc.State == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s is %s, want failed", id, doc.State)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 終わった実行への resume・cancel は、状態を変えずに、理由を添えた契約の JSON を返す（フラグは解釈される）。
	out, _, code = runFlowChild(t, env, t.TempDir(), "resume", "--workspace", ws, "--unit", "main", "--input", "land", "--approved-ref", "abc", "--note", "n", "--permission-mode", "auto", "--delegated-run", "R-2", id)
	if code != 0 || !strings.Contains(out, "already ended") {
		t.Errorf("resume of an ended run: exit %d\n%s", code, out)
	}
	out, _, code = runFlowChild(t, env, t.TempDir(), "cancel", "--workspace", ws, id)
	if code != 0 || !strings.Contains(out, "already ended") {
		t.Errorf("cancel of an ended run: exit %d\n%s", code, out)
	}
	// 知らない実行の識別は、failed の契約の JSON（run_id は渡した識別）
	out, _, code = runFlowChild(t, env, t.TempDir(), "status", "--workspace", ws, "no-such-run")
	if code != 0 || !strings.Contains(out, `"no-such-run"`) || !strings.Contains(out, `"state": "failed"`) {
		t.Errorf("status of an unknown run: exit %d\n%s", code, out)
	}
}

// flow のコマンドも、委譲の子の目印のある環境からは実行できない（コマンドの照合より前の検査）。
func TestFlow_IsRejectedFromADelegatedChildEnvironment(t *testing.T) {
	t.Parallel()
	ws := flowWorkspaceDir(t)
	env := append(flowChildEnv(fakeFlowPlugin(t, "5.3.0")), core.DelegatedRunEnvVar+"=R-9")
	out, errOut, code := runFlowChild(t, env, t.TempDir(), "status", "--workspace", ws, "x")
	if code == 0 || out != "" || !strings.Contains(errOut, core.DelegatedRunEnvVar) {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, errOut)
	}
}

// resume の引数（--unit・--input・--note・--approved-ref・--permission-mode）が flow.Resume の引数へ写る。
// 空の文字列の --input・--note も「渡した」ことが伝わる。
func TestFlowResume_MapsEveryFlag(t *testing.T) {
	ws := flowWorkspaceDir(t)
	var got flow.ResumeOptions
	old := flowResume
	flowResume = func(_ flow.Env, o flow.ResumeOptions) int { got = o; return 0 }
	t.Cleanup(func() { flowResume = old })
	var out, errb bytes.Buffer
	args := []string{"flow", "resume", "--workspace", ws, "--unit", "main", "--input", "land", "--note", "", "--approved-ref", "abc123", "--permission-mode", "auto", "--delegated-run", "R-1", "RUN-1"}
	if code := run(args, strings.NewReader(""), &out, &errb, defaultCommands()); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	want := flow.ResumeOptions{ID: "RUN-1", Unit: "main", Input: "land", HasInput: true, HasNote: true, ApprovedRef: "abc123", PermissionMode: "auto"}
	if got != want {
		t.Errorf("flow.Resume got %+v, want %+v", got, want)
	}
}

// ワークスペースでない場所（.flywheel が無い）には、実行の置き場を作らずに断る。
func TestFlow_RefusesADirectoryThatIsNotAWorkspace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, errOut, code := runFlowChild(t, flowChildEnv(fakeFlowPlugin(t, "5.3.0")), t.TempDir(), "start", "--workspace", dir, "--input", "issue=1", "ticket")
	if code == 0 || !strings.Contains(errOut, "ワークスペースではない") {
		t.Fatalf("exit %d stderr %q", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(dir, ".flywheel")); !os.IsNotExist(err) {
		t.Errorf(".flywheel was created (stat err: %v)", err)
	}
}
