package flow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/flow/runstate"
)

// 切り離した runner は flywheel 自身の実行ファイルを `flow __run-detached --workspace <ws> <id>` で起動し直す。
// 試験では実行ファイルがテストのバイナリなので、その形の起動をここで受けて RunDetached へ渡す。
func TestMain(m *testing.M) {
	if len(os.Args) == 6 && os.Args[1] == "flow" && os.Args[2] == DetachedRunnerCmd && os.Args[3] == "--workspace" {
		os.Exit(RunDetached(Env{Workspace: os.Args[4], Stdout: os.Stdout, Stderr: os.Stderr}, os.Args[5]))
	}
	os.Exit(m.Run())
}

var gitIdentity = []string{
	"GIT_AUTHOR_NAME=flow-test", "GIT_AUTHOR_EMAIL=flow-test@example.invalid",
	"GIT_COMMITTER_NAME=flow-test", "GIT_COMMITTER_EMAIL=flow-test@example.invalid",
	"GIT_CONFIG_NOSYSTEM=1",
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), gitIdentity...), "HOME="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func abs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// fixture は ticket フローを、偽の claude・gh・プラグインと、一時リポジトリ（bare の origin つき）で通す準備。
// 環境変数は t.Setenv で足す（切り離した runner は環境を引き継ぐ）ので、試験は並列にしない。
type fixture struct {
	t                  *testing.T
	root, repo, origin string
	ws                 string // ワークスペース（.flywheel を持つ）
	plugin             string // 偽のプラグインのルート
	bin                string // PATH の先頭に置く偽の実行ファイル（claude-harness-run・gh）
	fdir, ghDir        string // 偽の claude・gh の作業場所
	branch             string
}

const issueJSON = `{"number":42,"title":"Add the widget","body":"## 背景\nwidget が要る\n","state":"OPEN"}`

func claudeStructured(v any, cost float64) string {
	b, _ := json.Marshal(v)
	return fmt.Sprintf(`{"type":"result","subtype":"success","is_error":false,"session_id":"@SID@","total_cost_usd":%v,"structured_output":%s}`, cost, b)
}

func (f *fixture) respond(n int, stdout string) {
	f.t.Helper()
	writeFile(f.t, filepath.Join(f.fdir, "responses", fmt.Sprint(n)), fmt.Sprintf("0\n%s\n", stdout), 0o644)
}

func (f *fixture) ghFile(name, content string) {
	f.t.Helper()
	writeFile(f.t, filepath.Join(f.ghDir, name), content, 0o644)
}

// newFixture はプラグインの版を pluginVersion にした偽のプラグインを PATH の claude-harness-run に結ぶ。
func newFixture(t *testing.T, pluginVersion string) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, root: root, branch: "feature/issue-42-add-widget"}
	f.origin, f.repo = filepath.Join(root, "origin.git"), filepath.Join(root, "repo")
	git(t, root, "init", "-q", "--bare", "-b", "main", f.origin)
	git(t, root, "init", "-q", "-b", "main", f.repo)
	writeFile(t, filepath.Join(f.repo, "README.md"), "widget\n", 0o644)
	writeFile(t, filepath.Join(f.repo, ".claude-harness", "quality-gate.json"), `{"test": "true"}`+"\n", 0o644)
	git(t, f.repo, "add", ".")
	git(t, f.repo, "commit", "-q", "-m", "init")
	git(t, f.repo, "remote", "add", "origin", f.origin)
	git(t, f.repo, "push", "-q", "-u", "origin", "main")

	f.ws = filepath.Join(root, "ws")
	writeFile(t, filepath.Join(f.ws, ".flywheel", "keep"), "", 0o644)

	// 偽のプラグイン: 版・scripts（品質ゲートの runner と CI の観測）・agents（実在の検査だけに使う）。
	f.plugin = filepath.Join(root, "plugin")
	writeFile(t, filepath.Join(f.plugin, ".claude-plugin", "plugin.json"), fmt.Sprintf(`{"name":"claude-harness","version":%q}`, pluginVersion), 0o644)
	writeFile(t, filepath.Join(f.plugin, "scripts", "quality-check-runner.sh"),
		"#!/bin/bash\nprintf '%s' \"${HARNESS_PLUGIN_VERSION:-unset}\" > \"$FAKE_GH_DIR/plugin-version\"\nprintf '%s' \"${FLYWHEEL_DELEGATED_RUN-UNSET}\" > \"$FAKE_GH_DIR/script-marker\"\nprintf '%s\\n' '{\"result\":\"pass\",\"auto_fix\":{},\"gates\":{\"test\":{\"status\":\"pass\"}}}'\n", 0o755)
	writeFile(t, filepath.Join(f.plugin, "scripts", "ci-wait.sh"),
		"#!/bin/bash\nprintf '%s\\n' '{\"ci\":\"green\",\"failed_checks\":[],\"failure_log_excerpt\":\"\",\"pr_url\":\"https://github.com/o/r/pull/7\",\"pr_number\":7,\"pr_exists\":true}'\n", 0o755)
	// 作業ツリーの払い出しと片付け（実物のプラグインのスクリプトと同じ引数・出力の形。git の作業ツリーを作る）。
	writeFile(t, filepath.Join(f.plugin, "scripts", "worktree-setup.sh"), `#!/bin/bash
set -eu
case "$1" in *[!0-9]*) issue=null; key="$1" ;; *) issue="$1"; key="issue-$1" ;; esac
top=$(git rev-parse --show-toplevel)
wt="$(dirname "$top")/$(basename "$top")-worktrees/$key"
git fetch -q origin "$3"
git worktree add -q -b "$2" "$wt" "origin/$3"
wt=$(cd "$wt" && pwd -P)
printf '{"issue":%s,"key":"%s","branch":"%s","base":"%s","worktree_path":"%s","created":true,"reused":false,"branch_existed":false}\n' "$issue" "$key" "$2" "$3" "$wt"
`, 0o755)
	writeFile(t, filepath.Join(f.plugin, "scripts", "worktree-cleanup.sh"), `#!/bin/bash
set -u
git worktree remove --force "$1" >/dev/null 2>&1
printf '{"worktree_path":"%s","removed":true,"skipped":false,"dirty":false,"reason":null}\n' "$1"
`, 0o755)
	writeFile(t, filepath.Join(f.plugin, "agents", "feature-implementer.md"), "---\nname: feature-implementer\ndescription: test double\n---\n", 0o644)

	f.bin = filepath.Join(root, "bin")
	writeFile(t, filepath.Join(f.bin, LauncherName), fmt.Sprintf("#!/bin/sh\n[ \"$1\" = --plugin-root ] && echo %q\n", f.plugin), 0o755)
	if err := os.Symlink(abs(t, "testdata/scripts/fake-gh.sh"), filepath.Join(f.bin, "gh")); err != nil {
		t.Fatal(err)
	}
	f.fdir, f.ghDir = filepath.Join(root, "fclaude"), filepath.Join(root, "fgh")
	writeFile(t, filepath.Join(f.fdir, "responses", ".keep"), "", 0o644)
	f.ghFile("issue.json", issueJSON)

	t.Setenv("PATH", f.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", root)
	t.Setenv("FAKE_CLAUDE_DIR", f.fdir)
	t.Setenv("FAKE_GH_DIR", f.ghDir)
	t.Setenv("HARNESS_CLAUDE_BIN", abs(t, "testdata/scripts/fake-claude.sh"))
	t.Setenv("POLL_SLEEP_CMD", "true")
	for _, kv := range gitIdentity {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}

	// analyze → implement → commit。CI は偽のプラグインの ci-wait.sh が green を返す。
	f.respond(1, claudeStructured(map[string]any{"outcome": "ok", "e2e_target": "no", "e2e_reason": "internal only", "branch": f.branch, "critical_design": ""}, 0.3))
	f.respond(2, claudeStructured(map[string]any{"outcome": "pass", "self_review": "complete", "pr_title": "feat: add the widget", "summary": "Adds the widget.",
		"behaviors": []map[string]string{}, "residual_findings": []map[string]string{}, "unverified": []string{}, "cross_repo_attestation": "", "deviation_report": ""}, 4.0))
	f.respond(3, claudeStructured(map[string]any{"outcome": "committed", "commit_sha": "abc", "summary": "committed"}, 4.5))
	return f
}

// env は cwd をリポジトリにした Env と、標準出力・標準エラーの取り込み先を返す。
func (f *fixture) env() (Env, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return Env{Workspace: f.ws, Stdout: &out, Stderr: &errb, Getwd: func() (string, error) { return f.repo, nil }, Version: "test-version"}, &out, &errb
}

func (f *fixture) start(extra ...string) (contractDoc, string, int) {
	f.t.Helper()
	return f.startWith(StartOptions{PermissionMode: "auto", DelegatedRun: "R-start"}, extra...)
}

// startWith は o（Name・Inputs は足す）で ticket を始める。
func (f *fixture) startWith(o StartOptions, extra ...string) (contractDoc, string, int) {
	f.t.Helper()
	env, out, errb := f.env()
	o.Name, o.Inputs = "ticket", append([]string{"issue=42"}, extra...)
	code := Start(env, o)
	return decodeDoc(f.t, out.String()), errb.String(), code
}

func decodeDoc(t *testing.T, out string) contractDoc {
	t.Helper()
	var d contractDoc
	if strings.TrimSpace(out) == "" {
		return d // 標準出力が空（始める前の確かめに通らなかった start）
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("stdout is not a contract JSON: %v\n%s", err, out)
	}
	return d
}

func (f *fixture) status(id string) contractDoc {
	f.t.Helper()
	env, out, _ := f.env()
	if code := Status(env, id); code != ExitOK {
		f.t.Fatalf("status exit %d", code)
	}
	return decodeDoc(f.t, out.String())
}

// waitState は status の state が want になるまで待つ。
func (f *fixture) waitState(id, want string) contractDoc {
	f.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		d := f.status(id)
		if d.State == want {
			return d
		}
		if d.State == "failed" || d.State == "succeeded" || d.State == "cancelled" {
			f.dumpRun(id)
			f.t.Fatalf("run %s ended as %s (%s), want %s", id, d.State, d.Summary, want)
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("run %s is %s (%s), want %s", id, d.State, d.Summary, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (f *fixture) load(id string) *runstate.State {
	f.t.Helper()
	run, err := runstate.Open(runsDir(f.ws), id)
	if err != nil {
		f.t.Fatal(err)
	}
	st, _, err := run.Load()
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

// waitRunnerExit は、待ちか終端に達した run の runner が終わるまで待つ（一時ディレクトリを消す前に書き終えさせる）。
func (f *fixture) waitRunnerExit(id string) {
	f.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		pid := f.load(id).PID
		if pid == os.Getpid() || !runstate.Alive(pid) {
			return
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("the runner (pid %d) of run %s did not end", pid, id)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// runToReview は ticket をレビュー待ちまで進める。
func (f *fixture) runToReview() string {
	f.t.Helper()
	d, errOut, code := f.start()
	if code != ExitOK || d.RunID == nil || *d.RunID == "" {
		f.t.Fatalf("start exit %d\n%s", code, errOut)
	}
	id := *d.RunID
	w := f.waitState(id, "waiting")
	if w.RequestedAction == nil || w.RequestedAction.Kind != "review" {
		f.t.Fatalf("waiting but not at review: %+v (%s)", w.RequestedAction, w.Summary)
	}
	f.waitRunnerExit(id)
	return id
}

func killGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// dumpRun は失敗の調べに、run の記録と runner.log を出す。
func (f *fixture) dumpRun(id string) {
	dir := filepath.Join(runsDir(f.ws), id)
	for _, n := range []string{runstate.EventsFile, runstate.RunnerLogFile} {
		b, _ := os.ReadFile(filepath.Join(dir, n))
		if len(b) > 3000 {
			b = b[len(b)-3000:]
		}
		f.t.Logf("--- %s (tail)\n%s", n, b)
	}
}
