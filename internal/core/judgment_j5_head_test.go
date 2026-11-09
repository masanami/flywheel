package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// J5 が PR の head の中身を読めること（#465）を、実ストア・本物の git の一時の worktree・偽の claude
// （JudgmentInvoker の偽の実装。起動された作業ディレクトリのファイルを読んで判定を返す）で検証する。

const (
	headOnlyFile     = "docs/pr-only.md"
	headOnlySentence = "PR の head にしか無い文"
	headOnlyDiff     = "+" + headOnlySentence
)

// fakeDiffs は UpstreamDiffSource の偽の実装。
type fakeDiffs struct {
	diff  string
	err   error
	calls []string
}

func (f *fakeDiffs) GetCompareDiff(_ context.Context, repo, baseSHA, headSHA string) (string, error) {
	f.calls = append(f.calls, repo+" "+baseSHA+"..."+headSHA)
	return f.diff, f.err
}

// noHeadGit は HeadWorktreeGit の偽の実装（手元のクローンにコミットが無い）。
type noHeadGit struct{ added int }

func (*noHeadGit) HasCommit(context.Context, string, string) (bool, error) { return false, nil }
func (g *noHeadGit) AddHeadWorktree(context.Context, string, string, string) error {
	g.added++
	return nil
}
func (*noHeadGit) RemoveHeadWorktree(context.Context, string, string) error { return nil }

// execHeadGit は HeadWorktreeGit を本物の git で実装する（core は internal/adapters/git を import
// できない。adapter 自身の検査は internal/adapters/git のテストが持つ）。
type execHeadGit struct {
	t       *testing.T
	addErr  error
	removed []string
}

func (g *execHeadGit) HasCommit(_ context.Context, clone, sha string) (bool, error) {
	return exec.Command("git", "-C", clone, "cat-file", "-e", sha+"^{commit}").Run() == nil, nil
}

func (g *execHeadGit) AddHeadWorktree(_ context.Context, clone, path, sha string) error {
	if g.addErr != nil {
		return g.addErr
	}
	gitOut(g.t, clone, "worktree", "add", "--detach", path, sha)
	return nil
}

func (g *execHeadGit) RemoveHeadWorktree(_ context.Context, clone, path string) error {
	g.removed = append(g.removed, path)
	_ = exec.Command("git", "-C", clone, "worktree", "remove", "--force", path).Run()
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	return exec.Command("git", "-C", clone, "worktree", "prune").Run()
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// headRepo は元のクローン（ワークスペースの base/）を作る。既定ブランチに headOnlyFile は無く、
// head のコミットだけが持つ。戻り値は元のクローンのパスと head の SHA。
func headRepo(t *testing.T, ws, content string) (clone, sha string) {
	t.Helper()
	clone = filepath.Join(ws, "base")
	if err := os.MkdirAll(filepath.Join(clone, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitOut(t, clone, "init", "-b", "main")
	gitOut(t, clone, "commit", "--allow-empty", "-m", "root")
	gitOut(t, clone, "checkout", "-b", "feat/x")
	if err := os.WriteFile(filepath.Join(clone, headOnlyFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, clone, "add", "-A")
	gitOut(t, clone, "commit", "-m", "head")
	sha = gitOut(t, clone, "rev-parse", "HEAD")
	gitOut(t, clone, "checkout", "main")
	return clone, sha
}

// worktreeConnectors は o/r を worktree のスロット（元のクローンは base/）にした宣言を返す。
func worktreeConnectors() *ConnectorsDeclaration {
	return &ConnectorsDeclaration{Repos: []ConnectorRepo{{Name: "main-repo", Remote: "o/r", DefaultBranch: "main",
		Slots: ConnectorRepoSlots{Provider: string(slotProviderWorktree), Base: "base"}}}}
}

type prHeadFixture struct {
	*j5Fixture
	clone, sha string
	git        *execHeadGit
	diffs      *fakeDiffs
	workDir    string
	readErr    error
}

// newPRHeadFixture は C-1 を検証中にし、PR の head を本物のコミットにしたフィクスチャ。偽の claude は
// 起動されたディレクトリの headOnlyFile を読み、headOnlySentence があれば met、無ければ not_met を返す。
func newPRHeadFixture(t *testing.T, content string) *prHeadFixture {
	t.Helper()
	f := &prHeadFixture{j5Fixture: newJ5Fixture(t, "impl"), git: &execHeadGit{t: t}, diffs: &fakeDiffs{diff: headOnlyDiff}}
	f.delegateToVerifying(t, "C-1")
	f.clone, f.sha = headRepo(t, f.s.Workspace(), content)
	f.setPRHead(f.sha)
	prev := f.j5.onInvoke
	f.j5.onInvoke = func(in JudgmentLaunchInput) {
		prev(in)
		f.workDir = in.WorkDir
		var b []byte
		b, f.readErr = os.ReadFile(filepath.Join(in.WorkDir, headOnlyFile))
		if strings.Contains(string(b), headOnlySentence) {
			f.j5.result = j5Out("met", nil, nil)
		} else {
			f.j5.result = j5Out("not_met", "「"+headOnlySentence+"」が無い", nil)
		}
	}
	return f
}

func (f *prHeadFixture) verifyHead(ctx context.Context, t *testing.T) (*JudgmentAutoResult, error) {
	t.Helper()
	in := f.input(f.cycle(t, 300), nil)
	in.Diffs, in.HeadGit, in.Connectors = f.diffs, f.git, worktreeConnectors()
	return f.s.VerifyAutoJ5(ctx, in)
}

// assertWorktreeGone は ⑤ 終了後に worktree が消えていることを確かめる。
func (f *prHeadFixture) assertWorktreeGone(t *testing.T) {
	t.Helper()
	if f.workDir == "" {
		t.Fatal("J5 was not launched in a worktree")
	}
	if _, err := os.Stat(f.workDir); !os.IsNotExist(err) {
		t.Errorf("the worktree %s still exists (stat: %v)", f.workDir, err)
	}
	if list := gitOut(t, f.clone, "worktree", "list", "--porcelain"); strings.Contains(list, j5HeadsDirName) {
		t.Errorf("the clone still lists the worktree:\n%s", list)
	}
}

// verifiedHeadsLogged は課題の作業ログの最後の判定に残った head の一覧を返す。
func (f *prHeadFixture) verifiedHeadsLogged(t *testing.T) []map[string]string {
	t.Helper()
	acts := activitiesFor(t, f.s, "C-1")
	var after struct {
		Heads []map[string]string `json:"verified_heads"`
	}
	if err := json.Unmarshal(acts[len(acts)-1].After, &after); err != nil {
		t.Fatal(err)
	}
	return after.Heads
}

// ① ② ③ ④ ⑤ を満たした判定で固定する。
func TestVerifyAutoJ5_ReadsThePullRequestHead_Met(t *testing.T) {
	f := newPRHeadFixture(t, headOnlySentence+"\n")
	mainHead := gitOut(t, f.clone, "rev-parse", "HEAD")

	var gotHead string
	inner := f.j5.onInvoke
	f.j5.onInvoke = func(in JudgmentLaunchInput) {
		inner(in)
		gotHead = gitOut(t, in.WorkDir, "rev-parse", "HEAD")
		// J5 の起動の最中に PR へ push されても、読む版と記録する版は固定した SHA のまま。
		f.setPRHead("pushed-after-the-start")
	}
	if _, err := f.verifyHead(context.Background(), t); err != nil {
		t.Fatal(err)
	}

	// ①
	if f.readErr != nil {
		t.Fatalf("the file only on the head is not in the launch directory: %v", f.readErr)
	}
	if gotHead != f.sha {
		t.Errorf("worktree HEAD = %s, want the fixed head %s", gotHead, f.sha)
	}
	if in := f.j5Inputs[0]; len(in.AddDirs) != 1 || in.AddDirs[0] != f.s.Workspace() || in.SettingSources != "user" {
		t.Errorf("AddDirs = %v, SettingSources = %q", in.AddDirs, in.SettingSources)
	}
	if !strings.HasPrefix(f.workDir, filepath.Join(f.s.Workspace(), ".flywheel", "worktrees", j5HeadsDirName)+string(filepath.Separator)) {
		t.Errorf("WorkDir = %s", f.workDir)
	}
	// 主クローンの checkout は変わらない。
	if got := gitOut(t, f.clone, "rev-parse", "HEAD"); got != mainHead {
		t.Errorf("the clone's HEAD moved: %s -> %s", mainHead, got)
	}
	if _, err := os.Stat(filepath.Join(f.clone, headOnlyFile)); !os.IsNotExist(err) {
		t.Errorf("the clone's checkout has the head-only file (stat: %v)", err)
	}
	// ②
	in := f.j5Inputs[0]
	var diffSection string
	for _, s := range in.Sections {
		if s.Label == labelPullRequestDiff {
			diffSection = s.Content
		}
	}
	if !strings.Contains(diffSection, headOnlyDiff) || !strings.Contains(diffSection, f.sha) {
		t.Errorf("the diff section = %q", diffSection)
	}
	if want := []string{"o/r base-sha..." + f.sha}; len(f.diffs.calls) != 1 || f.diffs.calls[0] != want[0] {
		t.Errorf("diff calls = %v, want %v", f.diffs.calls, want)
	}
	if got := f.stdin(t); !strings.Contains(got, "作業ディレクトリは "+j5PRURL+" の head（"+f.sha+"）") {
		t.Errorf("no notice of the working directory:\n%s", got)
	}
	// ③ ④
	if got := f.status(t); got != StatusAwaitingCompletionApproval {
		t.Fatalf("status = %s, want awaiting completion approval", got)
	}
	if got := f.queryString(t, verifiedHeadQuery); got != f.sha {
		t.Errorf("verified_head_sha = %q, want %q", got, f.sha)
	}
	if got := f.verifiedHeadsLogged(t); len(got) != 1 || got[0]["head"] != f.sha || got[0]["pr"] != j5PRURL {
		t.Errorf("verified_heads = %v", got)
	}
	// ⑤
	f.assertWorktreeGone(t)
}

// ③ ④ ⑤ を、文が無くて未達の判定で固定する（ファイルはあるが文が無い・ファイルも無い）。
func TestVerifyAutoJ5_ReadsThePullRequestHead_NotMet(t *testing.T) {
	f := newPRHeadFixture(t, "別の文\n")
	if _, err := f.verifyHead(context.Background(), t); err != nil {
		t.Fatal(err)
	}
	if f.readErr != nil {
		t.Fatalf("read: %v", f.readErr)
	}
	if got := f.status(t); got != StatusInProgress {
		t.Fatalf("status = %s, want in progress (not met)", got)
	}
	if got := f.verifiedHeadsLogged(t); len(got) != 1 || got[0]["head"] != f.sha {
		t.Errorf("verified_heads = %v, want the fixed head %s", got, f.sha)
	}
	f.assertWorktreeGone(t)
}

func TestVerifyAutoJ5_FileMissingOnTheHead_NotMet(t *testing.T) {
	f := newPRHeadFixture(t, "x\n")
	// head を、ファイルを持たない既定ブランチのコミットにする。
	f.sha = gitOut(t, f.clone, "rev-parse", "main")
	f.setPRHead(f.sha)
	if _, err := f.verifyHead(context.Background(), t); err != nil {
		t.Fatal(err)
	}
	if !os.IsNotExist(f.readErr) {
		t.Fatalf("read err = %v, want not exist", f.readErr)
	}
	if got := f.status(t); got != StatusInProgress {
		t.Fatalf("status = %s, want in progress (not met)", got)
	}
	f.assertWorktreeGone(t)
}

// ⑤ を、失敗（claude が異常終了）と中断（ctx の取り消し）の終わり方で固定する。
func TestVerifyAutoJ5_RemovesTheWorktreeOnFailureAndCancel(t *testing.T) {
	t.Run("failed run", func(t *testing.T) {
		f := newPRHeadFixture(t, headOnlySentence)
		inner := f.j5.onInvoke
		f.j5.onInvoke = func(in JudgmentLaunchInput) {
			inner(in)
			f.j5.result = JudgmentLaunchOutput{Result: RunResultErrored, ErrorSummary: "boom"}
		}
		if _, err := f.verifyHead(context.Background(), t); err != nil {
			t.Fatal(err)
		}
		f.assertWorktreeGone(t)
	})
	t.Run("canceled", func(t *testing.T) {
		f := newPRHeadFixture(t, headOnlySentence)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		inner := f.j5.onInvoke
		f.j5.onInvoke = func(in JudgmentLaunchInput) {
			inner(in)
			f.j5.result = JudgmentLaunchOutput{Result: RunResultErrored, ErrorSummary: "signal: killed"}
			cancel()
		}
		_, _ = f.verifyHead(ctx, t)
		f.assertWorktreeGone(t)
		if runs := f.j5Runs(t); len(runs) != 1 || runs[0].Result != RunResultInterrupted {
			t.Errorf("runs = %+v, want one interrupted run", runs)
		}
	})
}

// 強制終了で残った worktree は、次の取り出しが消す。実行中のプロセスのものは消さない。
func TestVerifyAutoJ5_SweepsWorktreesOfDeadProcesses(t *testing.T) {
	f := newPRHeadFixture(t, headOnlySentence)
	dir := filepath.Join(f.s.Workspace(), ".flywheel", "worktrees", j5HeadsDirName, "main-repo")
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	dead := filepath.Join(dir, fmt.Sprintf("%d-%s-left", cmd.Process.Pid, j5HeadHostTag()))
	live := filepath.Join(dir, fmt.Sprintf("%d-%s-running", os.Getppid(), j5HeadHostTag()))
	otherHost := filepath.Join(dir, fmt.Sprintf("%d-00000000-other", cmd.Process.Pid))
	gitOut(t, f.clone, "worktree", "add", "--detach", dead, f.sha)
	gitOut(t, f.clone, "worktree", "add", "--detach", live, f.sha)
	gitOut(t, f.clone, "worktree", "add", "--detach", otherHost, f.sha)
	if _, err := f.verifyHead(context.Background(), t); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Errorf("the worktree of the dead process remains (stat: %v)", err)
	}
	for _, kept := range []string{live, otherHost} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s was removed: %v", kept, err)
		}
	}
}

// 取り出せないときは、差分を渡して理由を注記し、ワークスペースで起動する。
func TestVerifyAutoJ5_HeadNotCheckedOut_FallsBackToTheDiff(t *testing.T) {
	noCommit := func(in *J5AutoInput) { in.HeadGit = &noHeadGit{} }
	cases := map[string]struct {
		mut    func(in *J5AutoInput)
		reason string
	}{
		"commit missing":     {noCommit, "手元のクローンに head のコミットが無い"},
		"clone slots":        {func(in *J5AutoInput) { in.Connectors.Repos[0].Slots.Provider = string(slotProviderClone) }, "元のクローンを持たない"},
		"undeclared repo":    {func(in *J5AutoInput) { in.Connectors.Repos[0].Remote = "o/other" }, "接続の宣言に無い"},
		"no connectors file": {func(in *J5AutoInput) { in.Connectors = nil }, "接続の宣言に無い"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newPRHeadFixture(t, headOnlySentence)
			in := f.input(f.cycle(t, 300), nil)
			in.Diffs, in.HeadGit, in.Connectors = f.diffs, f.git, worktreeConnectors()
			tc.mut(&in)
			if _, err := f.s.VerifyAutoJ5(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			if f.workDir != "" {
				t.Errorf("WorkDir = %q, want the workspace", f.workDir)
			}
			got := f.stdin(t)
			if !strings.Contains(got, tc.reason) || !strings.Contains(got, headOnlyDiff) {
				t.Errorf("stdin lacks the reason or the diff:\n%s", got)
			}
		})
	}
}

// 差分の取得・worktree の作成に失敗したら、J5 を起動せず課題も変えない。
func TestVerifyAutoJ5_PreparationFailure_DoesNotStart(t *testing.T) {
	cases := map[string]func(f *prHeadFixture){
		"diff":     func(f *prHeadFixture) { f.diffs.err = errors.New("gh down") },
		"worktree": func(f *prHeadFixture) { f.git.addErr = errors.New("locked") },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			f := newPRHeadFixture(t, headOnlySentence)
			mut(f)
			res, err := f.verifyHead(context.Background(), t)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedUpstreamFetchFailed {
				t.Fatalf("not_started = %+v", res.NotStarted)
			}
			if len(f.j5Runs(t)) != 0 || f.status(t) != StatusVerifying {
				t.Errorf("runs = %d, status = %s", len(f.j5Runs(t)), f.status(t))
			}
			entries, _ := os.ReadDir(filepath.Join(f.s.Workspace(), ".flywheel", "worktrees", j5HeadsDirName, "main-repo"))
			if len(entries) != 0 {
				t.Errorf("left over: %v", entries)
			}
		})
	}
}

func TestVerifyAutoJ5_LargeDiffs(t *testing.T) {
	notice := func(f *prHeadFixture) (diff, note string) {
		for _, s := range f.j5Inputs[0].Sections {
			switch s.Label {
			case labelPullRequestDiff:
				diff = s.Content
			case labelPullRequestHeadNotice:
				note = s.Content
			}
		}
		return diff, note
	}
	t.Run("over the cap is cut and noted", func(t *testing.T) {
		f := newPRHeadFixture(t, headOnlySentence)
		f.diffs.diff = strings.Repeat("あ", maxPullRequestDiffBytes)
		if _, err := f.verifyHead(context.Background(), t); err != nil {
			t.Fatal(err)
		}
		diff, note := notice(f)
		if len(diff) > maxPullRequestDiffBytes || !strings.Contains(note, "残りを省略した") {
			t.Errorf("diff bytes = %d, note = %q", len(diff), note)
		}
	})
	t.Run("not returned by the upstream is noted", func(t *testing.T) {
		f := newPRHeadFixture(t, headOnlySentence)
		f.diffs.err = fmt.Errorf("x: %w", ErrUpstreamDiffUnavailable)
		if _, err := f.verifyHead(context.Background(), t); err != nil {
			t.Fatal(err)
		}
		diff, note := notice(f)
		if diff != "" || !strings.Contains(note, "上流が返さなかった") || f.workDir == "" {
			t.Errorf("diff = %q, note = %q, workDir = %q", diff, note, f.workDir)
		}
	})
}

// PR が複数なら open の 1 件を取り出し、差分はすべて渡す。閉じられた未マージの PR は読まない。
func TestPrepareJ5Head_ManyPullRequests(t *testing.T) {
	f := newPRHeadFixture(t, headOnlySentence)
	pr := func(n int, state, sha string) UpstreamPullRequestChecks {
		return UpstreamPullRequestChecks{URL: fmt.Sprintf("https://github.com/o/r/pull/%d", n), State: state, Base: "main", BaseSHA: "b", HeadSHA: sha}
	}
	noBase := func(p UpstreamPullRequestChecks) UpstreamPullRequestChecks { p.BaseSHA = ""; return p }
	main := gitOut(t, f.clone, "rev-parse", "main")
	in := J5AutoInput{Diffs: f.diffs, HeadGit: f.git, Connectors: worktreeConnectors()}
	head, ns := f.s.prepareJ5Head(context.Background(), in, "C-1", []UpstreamPullRequestChecks{pr(1, "closed", "dead"), noBase(pr(2, "merged", main)), pr(3, "open", f.sha), pr(4, "open", f.sha)})
	if ns != nil {
		t.Fatalf("not started: %+v", ns)
	}
	defer head.remove(context.Background())
	if got := gitOut(t, head.WorkDir, "rev-parse", "HEAD"); got != f.sha {
		t.Errorf("checked out %s, want the open pull request's head %s", got, f.sha)
	}
	if len(f.diffs.calls) != 2 {
		t.Errorf("diff calls = %v", f.diffs.calls)
	}
	note := head.Sections[len(head.Sections)-1].Content
	if !strings.Contains(note, "pull/2 の head は取り出していない") || !strings.Contains(note, "比較の起点") {
		t.Errorf("note = %q", note)
	}
}

// 読む PR が無い（閉じられた未マージの PR だけ）ときは、今までどおりワークスペースで起動する。
func TestVerifyAutoJ5_NoReadablePullRequest_RunsInTheWorkspace(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.checks.prs["o/r#1"] = prChecks("closed", checkDone)
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if in := f.j5Inputs[0]; in.WorkDir != "" || in.AddDirs != nil || in.SettingSources != "" {
		t.Errorf("WorkDir = %q, AddDirs = %v, SettingSources = %q", in.WorkDir, in.AddDirs, in.SettingSources)
	}
	if got := f.stdin(t); strings.Contains(got, labelPullRequestDiff) || strings.Contains(got, labelPullRequestHeadNotice) {
		t.Errorf("stdin has pull request sections:\n%s", got)
	}
}

// 中断で止めた委譲の run は interrupted になり、起動していない（launch_failed）run はそのまま残る。
func TestRunDelegation_CanceledRunIsInterrupted(t *testing.T) {
	cases := map[string]struct {
		out  JudgmentLaunchOutput
		want RunResult
	}{
		"killed":        {JudgmentLaunchOutput{Result: RunResultErrored, ErrorSummary: "signal: killed"}, RunResultInterrupted},
		"never started": {JudgmentLaunchOutput{Result: RunResultLaunchFailed, ErrorSummary: "no claude"}, RunResultLaunchFailed},
		"cost reported": {JudgmentLaunchOutput{Result: RunResultErrored, ReportedTotalCostUSD: float64Ptr(0.2)}, RunResultErrored},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newDelegateFixture(t)
			id := f.newInProgress(t, "t", "P1", planSpec(nil))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.deleg.result = tc.out
			f.deleg.onInvoke = func(DelegateLaunchInput) { cancel() }
			_, _ = f.s.RunDelegation(ctx, f.input(f.cycle(t, 300), &id))
			if runs := f.delegateRuns(t); len(runs) != 1 || runs[0].Result != tc.want {
				t.Fatalf("runs = %+v, want one %s run", runs, tc.want)
			}
		})
	}
}
