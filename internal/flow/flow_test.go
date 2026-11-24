package flow

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/flow/runstate"
	"github.com/masanami/flywheel/internal/flow/workflow"
)

func TestCheckPlugin(t *testing.T) {
	for _, c := range []struct {
		version string
		ok      bool
	}{
		{"4.8.9", false}, {"4.9.0", true}, {"5.3.0", true}, {"5.999.999", true}, {"6.0.0", false}, {"6.0.1", false}, {"4.9.0-rc.1", false},
	} {
		err := CheckPlugin(c.version)
		if (err == nil) != c.ok {
			t.Errorf("CheckPlugin(%q) = %v, want ok=%v", c.version, err, c.ok)
		}
	}
	if err := CheckPlugin("not-a-version"); err == nil || strings.Contains(err.Error(), "対応する範囲の外") {
		t.Errorf("an unreadable version is reported as unreadable, not as out of range: %v", err)
	}
}

// AC-14・AC-15・AC-28・AC-29・AC-30: start は契約の JSON を 1 つ出し、戻った時点で、別の PID の runner が引き継いでいる。
// 実行のフォルダは --workspace の下で、資材は版ごとの置き場へ展開される。HARNESS_STATE_DIR 等は読まない。
func TestStartHandsOverToADetachedRunnerUnderTheWorkspace(t *testing.T) {
	f := newFixture(t, "5.3.0")
	decoy := t.TempDir()
	t.Setenv("HARNESS_STATE_DIR", decoy)
	t.Setenv("HARNESS_DATA_DIR", decoy)
	t.Setenv("XDG_STATE_HOME", decoy)
	t.Setenv("XDG_DATA_HOME", decoy)

	d, errOut, code := f.start()
	if code != ExitOK {
		t.Fatalf("start exit %d\n%s", code, errOut)
	}
	if d.ContractVersion != 1 || d.RunID == nil || *d.RunID == "" {
		t.Fatalf("contract doc = %+v", d)
	}
	id := *d.RunID
	// start が戻った時点で、始めたプロセスとは別の PID の runner が引き継いでいる（待たずに読む）。
	if st := f.load(id); st.PID == 0 || st.PID == os.Getpid() {
		t.Fatalf("at the moment start returned, the run's runner pid is %d (this process is %d)", st.PID, os.Getpid())
	}
	f.waitState(id, "waiting")
	f.waitRunnerExit(id)

	runDir := filepath.Join(f.ws, ".flywheel", "flow", "runs", id)
	if _, err := os.Stat(filepath.Join(runDir, runstate.EventsFile)); err != nil {
		t.Fatalf("the run folder is not under the workspace's .flywheel/flow/runs: %v", err)
	}
	if entries, _ := os.ReadDir(decoy); len(entries) != 0 {
		t.Errorf("something was written under HARNESS_STATE_DIR: %v", entries)
	}
	run, err := runstate.Open(runsDir(f.ws), id)
	if err != nil {
		t.Fatal(err)
	}
	events, _, err := runstate.ReadEvents(filepath.Join(run.Dir, runstate.EventsFile))
	if err != nil {
		t.Fatal(err)
	}
	var runnerPID int
	for _, ev := range events {
		if ev.Type == runstate.EvRunnerStarted {
			runnerPID = ev.RunnerStarted.PID
		}
	}
	if runnerPID == 0 || runnerPID == os.Getpid() {
		t.Fatalf("runner_started pid = %d (this process is %d): the runner must be a different process", runnerPID, os.Getpid())
	}
	if _, err := os.Stat(filepath.Join(f.ws, ".flywheel", "flow", "runtime", "test-version", ".bundle-sha256")); err != nil {
		t.Errorf("the embedded materials were not extracted to the versioned folder: %v", err)
	}
	st := f.load(id)
	if !st.Embedded || st.CLIVersion != "test-version" || !strings.HasPrefix(st.Workflow.Path, filepath.Join(f.ws, ".flywheel", "flow", "runtime", "test-version")) {
		t.Errorf("run_started: embedded %v cli_version %q path %s", st.Embedded, st.CLIVersion, st.Workflow.Path)
	}
}

// AC-16: 進める側が 30 秒（試験では短くする）のうちに引き継がないとき、start は 0 でない終了コードで終わる。
func TestStartFailsWhenTheRunnerDoesNotTakeOver(t *testing.T) {
	f := newFixture(t, "5.3.0")
	stub := filepath.Join(f.root, "slow-runner.sh")
	writeFile(t, stub, "#!/bin/sh\necho $$ > "+filepath.Join(f.root, "stub-pid")+"\nexec sleep 60\n", 0o755)
	oldExe, oldWait := selfExecutable, detachWait
	selfExecutable = func() (string, error) { return stub, nil }
	detachWait = 1500 * time.Millisecond
	t.Cleanup(func() { selfExecutable, detachWait = oldExe, oldWait })
	_ = os.Remove(filepath.Join(f.root, "stub-pid"))

	d, errOut, code := f.start()
	if code == ExitOK {
		t.Fatalf("start exited 0 though the runner never took over\n%s", errOut)
	}
	if !strings.Contains(errOut, "引き継がなかった") {
		t.Errorf("stderr does not say the runner did not take over:\n%s", errOut)
	}
	if d.RunID == nil || *d.RunID == "" {
		t.Errorf("the run id must still be reported so the caller can find the run: %+v", d)
	}
	// 失敗として返した以上、起動した runner は止められている（遅れて引き継いで run を進めない）。
	// 止めた pid は start の標準エラーに出る（負荷時はスタブが pid を書く前に止められうるので、stub-pid には頼らない）。
	m := regexp.MustCompile(`runner（pid (\d+)）`).FindStringSubmatch(errOut)
	if m == nil {
		t.Fatalf("stderr does not name the runner pid:\n%s", errOut)
	}
	pid := atoi(m[1])
	deadline := time.Now().Add(5 * time.Second)
	for runstate.Alive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if runstate.Alive(pid) {
		t.Errorf("the runner that did not take over (pid %d) is still alive", pid)
	}
}

// AC-34・AC-35・AC-36: claude-harness-run が PATH に無いとき、実行のフォルダを作らず、0 でない終了コードで終わり、
// 標準エラーに claude-harness-run の名前が出る。
func TestStartWithoutTheLauncher(t *testing.T) {
	f := newFixture(t, "5.3.0")
	env, out, errb := f.env()
	env.Getenv = func(k string) string {
		if k == "PATH" {
			return t.TempDir() // claude-harness-run の無い PATH
		}
		return os.Getenv(k)
	}
	code := Start(env, StartOptions{Name: "ticket", Inputs: []string{"issue=42"}})
	if code == ExitOK {
		t.Fatal("start exited 0 without claude-harness-run")
	}
	if !strings.Contains(errb.String(), "claude-harness-run") {
		t.Errorf("stderr lacks the name claude-harness-run:\n%s", errb.String())
	}
	if out.Len() != 0 {
		t.Errorf("stdout should stay empty: %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(f.ws, ".flywheel", "flow")); !os.IsNotExist(err) {
		t.Errorf("a folder was created under .flywheel/flow although the start was refused (stat err: %v)", err)
	}
}

// AC-37・AC-38・AC-39・AC-40: プラグインの版が範囲の外（4.8.9・6.0.0）なら、実行のフォルダを作らず、0 でない終了コードで終わり、
// 標準エラーに読んだ版と範囲（4.9.0 以上 6.0.0 未満）が出る。範囲の端（4.9.0）なら始める。
func TestStartChecksThePluginVersion(t *testing.T) {
	for _, v := range []string{"4.8.9", "6.0.0"} {
		t.Run("out of range "+v, func(t *testing.T) {
			f := newFixture(t, v)
			d, errOut, code := f.start()
			if code == ExitOK {
				t.Fatalf("start exited 0 with plugin %s", v)
			}
			for _, want := range []string{v, "4.9.0 以上 6.0.0 未満"} {
				if !strings.Contains(errOut, want) {
					t.Errorf("stderr lacks %q:\n%s", want, errOut)
				}
			}
			if d.RunID != nil {
				t.Errorf("a run was reported: %+v", d)
			}
			if _, err := os.Stat(filepath.Join(f.ws, ".flywheel", "flow")); !os.IsNotExist(err) {
				t.Errorf("a folder was created under .flywheel/flow although the start was refused (stat err: %v)", err)
			}
		})
	}
	t.Run("the lower edge 4.9.0 starts", func(t *testing.T) {
		f := newFixture(t, "4.9.0")
		d, errOut, code := f.start()
		if code != ExitOK || d.RunID == nil {
			t.Fatalf("start exit %d\n%s", code, errOut)
		}
		f.waitState(*d.RunID, "waiting")
		f.waitRunnerExit(*d.RunID)
	})
}

// AC-31・AC-32: .flywheel/.gitignore が在って flow/ の行が無ければ足し、無ければ作らない。
func TestStartGitignore(t *testing.T) {
	t.Run("adds flow/ to an existing .gitignore", func(t *testing.T) {
		f := newFixture(t, "5.3.0")
		gi := filepath.Join(f.ws, ".flywheel", ".gitignore")
		writeFile(t, gi, "flywheel.db\nruns/", 0o644) // 末尾の改行が無い形でも行を壊さない
		d, errOut, code := f.start()
		if code != ExitOK || d.RunID == nil {
			t.Fatalf("start exit %d\n%s", code, errOut)
		}
		f.waitState(*d.RunID, "waiting")
		f.waitRunnerExit(*d.RunID)
		b, _ := os.ReadFile(gi)
		if string(b) != "flywheel.db\nruns/\nflow/\n" {
			t.Errorf(".gitignore = %q", b)
		}
		// 2 回目は足さない
		if err := ensureGitignore(f.ws); err != nil {
			t.Fatal(err)
		}
		if b2, _ := os.ReadFile(gi); string(b2) != string(b) {
			t.Errorf("flow/ was added twice: %q", b2)
		}
	})
	t.Run("does not create a missing .gitignore", func(t *testing.T) {
		f := newFixture(t, "5.3.0")
		d, errOut, code := f.start()
		if code != ExitOK || d.RunID == nil {
			t.Fatalf("start exit %d\n%s", code, errOut)
		}
		f.waitState(*d.RunID, "waiting")
		f.waitRunnerExit(*d.RunID)
		if _, err := os.Stat(filepath.Join(f.ws, ".flywheel", ".gitignore")); !os.IsNotExist(err) {
			t.Errorf(".gitignore was created (stat err: %v)", err)
		}
	})
}

// AC-46: 作業ディレクトリのリポジトリの .claude-harness/overrides/ticket.yaml に上限を変える上書きがあっても、
// 実行の記録の上限は、埋め込んだ定義の値のまま。
func TestStartIgnoresRepositoryOverrides(t *testing.T) {
	f := newFixture(t, "5.3.0")
	writeFile(t, filepath.Join(f.repo, ".claude-harness", "overrides", "ticket.yaml"),
		"schema: harness.workflow-override/v1\nworkflow: ticket\nlimits: { budget_usd: 7, rework: 9 }\n", 0o644)
	d, errOut, code := f.start()
	if code != ExitOK || d.RunID == nil {
		t.Fatalf("start exit %d\n%s", code, errOut)
	}
	id := *d.RunID
	f.waitState(id, "waiting")
	f.waitRunnerExit(id)

	embedded, err := workflow.LoadWithOverrides(filepath.Join(f.ws, ".flywheel", "flow", "runtime", "test-version", "workflows", "ticket.yaml"),
		workflow.Options{ScriptsDir: filepath.Join(f.plugin, "scripts")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := f.load(id)
	for _, l := range embedded.Limits {
		if st.Limits[l.Name] != l.Value {
			t.Errorf("limit %s = %v in the run's record, want the embedded definition's %v", l.Name, st.Limits[l.Name], l.Value)
		}
	}
	if st.Limits["budget_usd"] == 7 || st.Limits["rework"] == 9 {
		t.Errorf("the repository's override was applied: %v", st.Limits)
	}
	if len(st.Overrides.Overrides) != 0 {
		t.Errorf("overrides recorded: %+v", st.Overrides)
	}
}

// AC-17・AC-18・AC-19・AC-20: ticket がレビュー待ちに入ると、runner は終わり、status は waiting・kind review・
// 選べるものが respond・ready・land の 3 つ。
func TestTicketReviewWait(t *testing.T) {
	f := newFixture(t, "5.3.0")
	id := f.runToReview() // runner が終わるまで待つ（AC-17）
	d := f.status(id)
	if d.State != "waiting" {
		t.Fatalf("state = %s", d.State)
	}
	a := d.RequestedAction
	if a == nil || a.Kind != "review" {
		t.Fatalf("requested_action = %+v", a)
	}
	if got := strings.Join(a.Options, ","); got != "respond,ready,land" {
		t.Errorf("options = %s, want respond,ready,land", got)
	}
	// 切り離した runner は、解決したプラグインの版を HARNESS_PLUGIN_VERSION で引き継ぐ（ステップの子まで届く）。
	if b, _ := os.ReadFile(filepath.Join(f.ghDir, "plugin-version")); string(b) != "5.3.0" {
		t.Errorf("HARNESS_PLUGIN_VERSION in the step = %q, want 5.3.0", b)
	}
}

// 偽の gh・origin を、PR が #7 で先頭が sha の状態にする（land の確かめが読む）。
func (f *fixture) setPR(state, head, merge string) {
	f.t.Helper()
	git(f.t, f.origin, "update-ref", "refs/pull/7/head", head)
	mc := "null"
	if merge != "" {
		mc = `{"oid":"` + merge + `"}`
	}
	f.ghFile("pr-view.json", `{"state":"`+state+`","headRefOid":"`+head+`","baseRefName":"main","mergeCommit":`+mc+`,"isDraft":false}`)
}

func (f *fixture) merges() int {
	b, _ := os.ReadFile(filepath.Join(f.ghDir, "calls"))
	return strings.Count(string(b), "pr\tmerge\t")
}

func (f *fixture) originHead() string {
	f.t.Helper()
	fields := strings.Fields(git(f.t, f.repo, "ls-remote", "--heads", f.origin, f.branch))
	if len(fields) == 0 {
		f.t.Fatalf("the branch %s is not on origin", f.branch)
	}
	return fields[0]
}

func (f *fixture) resume(o ResumeOptions) (contractDoc, string, int) {
	f.t.Helper()
	env, out, errb := f.env()
	code := Resume(env, o)
	return decodeDoc(f.t, out.String()), errb.String(), code
}

// AC-21・AC-22: land は、通した中身と PR の先頭が一致するとき、PR を宛先へ入れる gh の呼び出しをちょうど 1 回行い、
// その後 succeeded になる。
func TestLandWithTheApprovedHeadMergesOnceAndSucceeds(t *testing.T) {
	f := newFixture(t, "5.3.0")
	id := f.runToReview()
	head := f.originHead()
	f.setPR("OPEN", head, "")
	f.ghFile("merge.then", `{"state":"MERGED","headRefOid":"`+head+`","baseRefName":"main","mergeCommit":{"oid":"squashed0000000000000000000000000000000000"},"isDraft":false}`)

	d, errOut, code := f.resume(ResumeOptions{ID: id, Unit: "main", Input: "land", HasInput: true, ApprovedRef: head})
	if code != ExitOK {
		t.Fatalf("resume exit %d\n%s", code, errOut)
	}
	if d.RunID == nil || *d.RunID != id {
		t.Fatalf("doc = %+v", d)
	}
	f.waitState(id, "succeeded")
	if n := f.merges(); n != 1 {
		t.Errorf("gh pr merge was called %d times, want exactly 1", n)
	}
}

// AC-23: 通した中身と PR の先頭が一致しないとき、PR を宛先へ入れる gh の呼び出しを行わない。
func TestLandWithAMismatchedHeadDoesNotMerge(t *testing.T) {
	f := newFixture(t, "5.3.0")
	id := f.runToReview()
	head := f.originHead()
	f.setPR("OPEN", head, "")
	other := strings.Repeat("a", 40) // 通した中身（承認した先頭）が PR の先頭と違う

	_, errOut, code := f.resume(ResumeOptions{ID: id, Unit: "main", Input: "land", HasInput: true, ApprovedRef: other})
	if code != ExitOK {
		t.Fatalf("resume exit %d\n%s", code, errOut)
	}
	// land の段は PR を入れずに、レビュー待ちへ戻る
	deadline := time.Now().Add(60 * time.Second)
	for f.status(id).State == "running" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	f.waitRunnerExit(id)
	if n := f.merges(); n != 0 {
		t.Errorf("gh pr merge was called %d times though the approved head did not match", n)
	}
	if d := f.status(id); d.State != "waiting" || d.RequestedAction == nil || d.RequestedAction.Kind != "review" {
		t.Errorf("after a refused land: state %s, requested_action %+v; want waiting at review", d.State, d.RequestedAction)
	}
}

// AC-24: 走っている実行への cancel の後、state は cancelled になり、ステップの子プロセスも止まる。
func TestCancelStopsARunningRun(t *testing.T) {
	f := newFixture(t, "5.3.0")
	writeFile(t, filepath.Join(f.fdir, "responses", "1.sleep"), "", 0o644) // analyze の claude が眠る
	d, errOut, code := f.start()
	if code != ExitOK || d.RunID == nil {
		t.Fatalf("start exit %d\n%s", code, errOut)
	}
	id := *d.RunID
	deadline := time.Now().Add(30 * time.Second)
	for {
		if b, err := os.ReadFile(filepath.Join(f.fdir, "pids")); err == nil && len(strings.Fields(string(b))) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the fake claude did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := f.status(id).State; got != "running" {
		t.Fatalf("state before cancel = %s", got)
	}
	env, out, errb := f.env()
	if code := Cancel(env, id); code != ExitOK {
		t.Fatalf("cancel exit %d\n%s", code, errb.String())
	}
	if got := decodeDoc(t, out.String()).State; got != "cancelled" {
		t.Errorf("state in cancel's output = %s", got)
	}
	if got := f.status(id).State; got != "cancelled" {
		t.Errorf("state after cancel = %s", got)
	}
	f.waitRunnerExit(id)
	b, _ := os.ReadFile(pidsFile(f))
	if len(strings.Fields(string(b))) == 0 {
		t.Fatal("no step process pid was recorded")
	}
	for _, w := range strings.Fields(string(b)) {
		pid := atoi(w)
		// ステップのプロセスグループ（偽の claude が起こした sleep を含む）も残らない
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			if err := syscall.Kill(-pid, 0); err != nil {
				break
			}
		}
		if err := syscall.Kill(-pid, 0); err == nil {
			t.Errorf("the step's process group (pgid %d) is still alive after cancel", pid)
		}
		deadline := time.Now().Add(10 * time.Second)
		for runstate.Alive(pid) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if runstate.Alive(pid) {
			t.Errorf("the step's process (pid %d) is still alive after cancel", pid)
		}
	}
}

func pidsFile(f *fixture) string { return filepath.Join(f.fdir, "pids") }

// AC-25・AC-26・AC-27: 進める側のプロセスを外から止めた後の status は、実行中だったステップを interrupted にし、
// state は waiting で、選べるものは rerun・abort の 2 つ。
func TestKilledRunnerIsRecoveredAsInterrupted(t *testing.T) {
	f := newFixture(t, "5.3.0")
	writeFile(t, filepath.Join(f.fdir, "responses", "1.sleep"), "", 0o644)
	d, errOut, code := f.start()
	if code != ExitOK || d.RunID == nil {
		t.Fatalf("start exit %d\n%s", code, errOut)
	}
	id := *d.RunID
	pidsFile := filepath.Join(f.fdir, "pids")
	var stepPID int
	deadline := time.Now().Add(30 * time.Second)
	for stepPID == 0 {
		if b, err := os.ReadFile(pidsFile); err == nil {
			for _, w := range strings.Fields(string(b)) {
				stepPID = atoi(w)
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the fake claude did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	runner := f.load(id).PID
	if runner == os.Getpid() {
		t.Fatal("the runner is this process")
	}
	if err := syscall.Kill(runner, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	for runstate.Alive(runner) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	killGroup(stepPID) // ステップのプロセスも消える

	w := f.waitState(id, "waiting")
	if a := w.RequestedAction; a == nil || strings.Join(a.Options, ",") != "rerun,abort" {
		t.Fatalf("requested_action = %+v", a)
	}
	var interrupted bool
	for _, u := range f.load(id).Units {
		for _, r := range u.Rounds {
			for _, s := range r.Steps {
				if s.Status == "interrupted" || s.Outcome == "interrupted" {
					interrupted = true
				}
			}
		}
	}
	if !interrupted {
		t.Errorf("no step is recorded as interrupted: %+v", f.load(id).Units)
	}
	// abort で打ち切れる
	d2, errOut, code := f.resume(ResumeOptions{ID: id, Unit: "main", Input: "abort", HasInput: true})
	if code != ExitOK || d2.State != "failed" {
		t.Errorf("resume abort: exit %d state %s\n%s", code, d2.State, errOut)
	}
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// プラグインを使えなくなった（版が範囲の外になった）後の status は、待っている run のゲートの操作ではなく、
// 人が直してから確かめ直す案内（observe）を出す。
func TestStatusOfAWaitingRunWhosePluginIsNoLongerUsable(t *testing.T) {
	f := newFixture(t, "5.3.0")
	id := f.runToReview()
	writeFile(t, filepath.Join(f.plugin, ".claude-plugin", "plugin.json"), `{"name":"claude-harness","version":"6.0.0"}`, 0o644)
	d := f.status(id)
	if d.State != "waiting" || d.RequestedAction == nil || d.RequestedAction.Kind != "observe" || d.RequestedAction.Decider != "human" {
		t.Fatalf("requested_action = %+v", d.RequestedAction)
	}
	if !strings.Contains(d.RequestedAction.Text, "6.0.0") {
		t.Errorf("text does not say why: %s", d.RequestedAction.Text)
	}
	// resume も何も記録せず、同じ案内を返す
	before := f.load(id).LastSeq
	d2, _, code := f.resume(ResumeOptions{ID: id, Unit: "main", Input: "ready", HasInput: true})
	if code != ExitOK || d2.RequestedAction == nil || d2.RequestedAction.Kind != "observe" || f.load(id).LastSeq != before {
		t.Errorf("resume with an unusable plugin: exit %d action %+v, events changed %v", code, d2.RequestedAction, f.load(id).LastSeq != before)
	}
}
