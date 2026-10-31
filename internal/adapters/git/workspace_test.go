package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// このファイルは Workspace（core.WorkspaceGit の実装）を実物の git で検証する。

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	write(t, filepath.Join(dir, "README.md"), "hi\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspace_CommitPathsOnlyGivenPathsAndKeepsOtherState(t *testing.T) {
	dir := newRepo(t)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	// ほかのファイル: ステージ済み・未ステージの変更・追跡外
	write(t, filepath.Join(dir, "staged.txt"), "s\n")
	gitIn(t, dir, "add", "staged.txt")
	write(t, filepath.Join(dir, "README.md"), "changed\n")
	write(t, filepath.Join(dir, "untracked.txt"), "u\n")
	// コミット対象
	exp := filepath.Join(dir, ".flywheel", "export.json")
	mem := filepath.Join(dir, "memory")
	write(t, exp, "{}\n")
	write(t, filepath.Join(mem, "a.md"), "a\n")
	before := gitIn(t, dir, "status", "--porcelain")

	w := NewWorkspace()
	ctx := context.Background()
	has, err := w.HasChanges(ctx, dir, []string{exp, mem})
	if err != nil || !has {
		t.Fatalf("HasChanges = %v, %v; want true", has, err)
	}
	sha, err := w.CommitPaths(ctx, dir, "chore(flywheel): backup X", []string{exp, mem})
	if err != nil {
		t.Fatal(err)
	}
	if head := gitIn(t, dir, "rev-parse", "HEAD"); sha != head {
		t.Errorf("sha = %q, HEAD = %q", sha, head)
	}
	files := gitIn(t, dir, "show", "--name-only", "--pretty=format:", "HEAD")
	if got := strings.Fields(files); len(got) != 2 || got[0] != ".flywheel/export.json" || got[1] != "memory/a.md" {
		t.Errorf("committed files = %v", got)
	}
	if msg := gitIn(t, dir, "log", "-1", "--format=%s"); msg != "chore(flywheel): backup X" {
		t.Errorf("subject = %q", msg)
	}
	after := gitIn(t, dir, "status", "--porcelain")
	wantAfter := []string{}
	for _, l := range strings.Split(before, "\n") {
		if !strings.Contains(l, ".flywheel") && !strings.Contains(l, "memory") {
			wantAfter = append(wantAfter, l)
		}
	}
	if after != strings.Join(wantAfter, "\n") {
		t.Errorf("status after = %q, want %q", after, strings.Join(wantAfter, "\n"))
	}
	if has, err := w.HasChanges(ctx, dir, []string{exp, mem}); err != nil || has {
		t.Errorf("HasChanges after commit = %v, %v; want false", has, err)
	}
}

func TestWorkspace_DoesNotPushToBareRemote(t *testing.T) {
	dir := newRepo(t)
	bare := filepath.Join(t.TempDir(), "remote.git")
	gitIn(t, filepath.Dir(bare), "init", "-q", "--bare", bare)
	gitIn(t, dir, "remote", "add", "origin", bare)
	gitIn(t, dir, "push", "-q", "origin", "main")
	refs := func() string { return gitIn(t, bare, "for-each-ref") }
	b := refs()
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	p := filepath.Join(dir, "export.json")
	write(t, p, "{}")
	if _, err := NewWorkspace().CommitPaths(context.Background(), dir, "m", []string{p}); err != nil {
		t.Fatal(err)
	}
	if refs() != b {
		t.Errorf("bare refs changed")
	}
}

func TestWorkspace_IsWorkTreeIsIgnored(t *testing.T) {
	dir := newRepo(t)
	w := NewWorkspace()
	ctx := context.Background()
	if ok, err := w.IsWorkTree(ctx, dir); err != nil || !ok {
		t.Errorf("IsWorkTree(repo) = %v, %v", ok, err)
	}
	plain := t.TempDir()
	if ok, err := w.IsWorkTree(ctx, plain); err != nil || ok {
		t.Errorf("IsWorkTree(plain) = %v, %v", ok, err)
	}
	write(t, filepath.Join(dir, ".gitignore"), ".flywheel/\n")
	ign := filepath.Join(dir, ".flywheel", "export.json")
	write(t, ign, "{}")
	if ok, err := w.IsIgnored(ctx, dir, ign); err != nil || !ok {
		t.Errorf("IsIgnored(ignored) = %v, %v", ok, err)
	}
	if ok, err := w.IsIgnored(ctx, dir, filepath.Join(dir, "memory", "x.md")); err != nil || ok {
		t.Errorf("IsIgnored(normal) = %v, %v", ok, err)
	}
}

func TestWorkspace_HasChangesFalseWhenClean(t *testing.T) {
	dir := newRepo(t)
	if has, err := NewWorkspace().HasChanges(context.Background(), dir, []string{filepath.Join(dir, "README.md"), filepath.Join(dir, "missing")}); err != nil || has {
		t.Errorf("= %v, %v; want false", has, err)
	}
}

func TestWorkspace_FailingPreCommitHookReturnsReason(t *testing.T) {
	dir := newRepo(t)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	gitIn(t, dir, "config", "core.hooksPath", filepath.Join(dir, ".git", "hooks"))
	hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
	write(t, hook, "#!/bin/sh\necho hook-says-no >&2\nexit 1\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "export.json")
	write(t, p, "{}")
	head := gitIn(t, dir, "rev-parse", "HEAD")
	_, err := NewWorkspace().CommitPaths(context.Background(), dir, "m", []string{p})
	if err == nil || !strings.Contains(err.Error(), "hook-says-no") {
		t.Fatalf("err = %v; want an error carrying the hook's reason", err)
	}
	if gitIn(t, dir, "rev-parse", "HEAD") != head {
		t.Errorf("HEAD moved despite the failing hook")
	}
}

// 仮定: ワークスペースが親のディレクトリのリポジトリの中にあるとき、見つかった
// リポジトリへ渡したパスだけをコミットする。
func TestWorkspace_WorkspaceInsideParentRepo(t *testing.T) {
	parent := newRepo(t)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	ws := filepath.Join(parent, "sub", "ws")
	exp := filepath.Join(ws, ".flywheel", "export.json")
	write(t, exp, "{}")
	write(t, filepath.Join(parent, "sub", "other.txt"), "o")
	w := NewWorkspace()
	ctx := context.Background()
	if ok, err := w.IsWorkTree(ctx, ws); err != nil || !ok {
		t.Fatalf("IsWorkTree = %v, %v", ok, err)
	}
	if _, err := w.CommitPaths(ctx, ws, "m", []string{exp}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(gitIn(t, parent, "show", "--name-only", "--pretty=format:", "HEAD")); got != "sub/ws/.flywheel/export.json" {
		t.Errorf("committed = %q", got)
	}
	if st := gitIn(t, parent, "status", "--porcelain"); !strings.Contains(st, "other.txt") {
		t.Errorf("other.txt should stay untracked: %q", st)
	}
}

func TestWorkspace_InheritedGitEnvIsIgnored(t *testing.T) {
	other := newRepo(t)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	if ok, err := NewWorkspace().IsWorkTree(context.Background(), t.TempDir()); err != nil || ok {
		t.Errorf("IsWorkTree = %v, %v; want false (env must not leak)", ok, err)
	}
}

func TestWorkspace_CommitPathsSkipsMissingUntrackedPathAndIsLiteral(t *testing.T) {
	dir := newRepo(t)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	exp := filepath.Join(dir, "m[1]")
	write(t, exp, "x")
	emptyDir := filepath.Join(dir, "emptymem")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "m1"), "y") // glob なら m[1] に一致してしまう
	w := NewWorkspace()
	if _, err := w.CommitPaths(context.Background(), dir, "m", []string{exp, filepath.Join(dir, "nomemory"), emptyDir}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(gitIn(t, dir, "show", "--name-only", "--pretty=format:", "HEAD")); got != "m[1]" {
		t.Errorf("committed = %q", got)
	}
	// ワークツリーのルートそのものは受け付けない
	if _, err := w.CommitPaths(context.Background(), dir, "m", []string{dir}); err == nil {
		t.Errorf("root path must be rejected")
	}
}

func gitIdentity(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
}

func TestWorkspace_InterruptedState(t *testing.T) {
	gitIdentity(t)
	ctx := context.Background()

	t.Run("normal", func(t *testing.T) {
		dir := newRepo(t)
		if got, err := NewWorkspace().InterruptedState(ctx, dir); err != nil || got != "" {
			t.Errorf("InterruptedState = %q, %v; want empty", got, err)
		}
	})
	t.Run("detached", func(t *testing.T) {
		dir := newRepo(t)
		gitIn(t, dir, "checkout", "-q", "--detach")
		if got, err := NewWorkspace().InterruptedState(ctx, dir); err != nil || got != "detached HEAD" {
			t.Errorf("InterruptedState = %q, %v; want detached HEAD", got, err)
		}
	})
	// want の理由は、衝突で止めた操作ごとの途中の状態。
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"merge", []string{"merge", "other"}, "merge in progress"},
		// rebase の途中は HEAD が detached になるが、理由は rebase を優先する。
		{"rebase", []string{"rebase", "other"}, "rebase in progress"},
		{"rebase-apply", []string{"rebase", "--apply", "other"}, "rebase in progress"},
		{"cherry-pick", []string{"cherry-pick", "other"}, "cherry-pick in progress"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := conflictingBranches(t)
			gitStop(t, dir, c.args...)
			if got, err := NewWorkspace().InterruptedState(ctx, dir); err != nil || got != c.want {
				t.Errorf("InterruptedState = %q, %v; want %s", got, err, c.want)
			}
		})
	}
	t.Run("revert", func(t *testing.T) {
		dir := newRepo(t)
		write(t, filepath.Join(dir, "README.md"), "a\n")
		gitIn(t, dir, "commit", "-q", "-am", "a")
		write(t, filepath.Join(dir, "README.md"), "b\n")
		gitIn(t, dir, "commit", "-q", "-am", "b")
		gitStop(t, dir, "revert", "--no-edit", "HEAD~1")
		if got, err := NewWorkspace().InterruptedState(ctx, dir); err != nil || got != "revert in progress" {
			t.Errorf("InterruptedState = %q, %v; want revert in progress", got, err)
		}
	})
}

// gitStop は dir で git を実行し、衝突で止まる（非 0 で終わる）ことを確かめる。
func gitStop(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(realGit(t), args...)
	cmd.Dir = dir
	cmd.Env = append(cleanEnv(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_EDITOR=true")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("git %s did not stop on a conflict:\n%s", strings.Join(args, " "), out)
	}
}

// installHook は dir のリポジトリにフック name を置く。
func installHook(t *testing.T, dir, name, script string) {
	t.Helper()
	gitIn(t, dir, "config", "core.hooksPath", filepath.Join(dir, ".git", "hooks"))
	hook := filepath.Join(dir, ".git", "hooks", name)
	write(t, hook, script)
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
}

func shortWaitDelay(t *testing.T) {
	t.Helper()
	old := waitDelay
	waitDelay = 300 * time.Millisecond
	t.Cleanup(func() { waitDelay = old })
}

// 期限切れでは git を SIGTERM で止め（git が index.lock を消せる）、フックが出力の管を握っていても
// waitDelay の後に戻る。
func TestWorkspace_CommitPathsStopsAtTheDeadlineWithoutLeavingLocks(t *testing.T) {
	gitIdentity(t)
	shortWaitDelay(t)
	dir := newRepo(t)
	installHook(t, dir, "pre-commit", "#!/bin/sh\nsleep 60\n")
	p := filepath.Join(dir, "export.json")
	write(t, p, "{}")
	w := NewWorkspace()
	if ok, err := w.IsWorkTree(context.Background(), dir); err != nil || !ok {
		t.Fatalf("IsWorkTree = %v, %v", ok, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := w.CommitPaths(ctx, dir, "m", []string{p})
	if err == nil {
		t.Fatal("CommitPaths succeeded despite the deadline")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("CommitPaths returned after %s, want within a few seconds", d)
	}
	locks, _ := filepath.Glob(filepath.Join(dir, ".git", "*.lock"))
	if len(locks) != 0 {
		t.Errorf("lock files left: %v", locks)
	}
}

// git が 0 で終わった後にフックの子孫が出力の管を握り続けても（exec.ErrWaitDelay）、成功として扱う。
func TestWorkspace_CommitPathsSucceedsWhenAHookChildHoldsThePipes(t *testing.T) {
	gitIdentity(t)
	shortWaitDelay(t)
	dir := newRepo(t)
	installHook(t, dir, "post-commit", "#!/bin/sh\nsleep 30 &\n")
	p := filepath.Join(dir, "export.json")
	write(t, p, "{}")
	sha, err := NewWorkspace().CommitPaths(context.Background(), dir, "m", []string{p})
	if err != nil {
		t.Fatalf("CommitPaths: %v", err)
	}
	if head := gitIn(t, dir, "rev-parse", "HEAD"); sha != head {
		t.Errorf("sha = %q, HEAD = %q", sha, head)
	}
}

// conflictingBranches は main と other が README.md を別々に変えたリポジトリ（main を checkout 済み）。
func conflictingBranches(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	gitIn(t, dir, "checkout", "-q", "-b", "other")
	write(t, filepath.Join(dir, "README.md"), "other\n")
	gitIn(t, dir, "commit", "-q", "-am", "other")
	gitIn(t, dir, "checkout", "-q", "main")
	write(t, filepath.Join(dir, "README.md"), "main\n")
	gitIn(t, dir, "commit", "-q", "-am", "main")
	return dir
}

// IsWorkTree が突き止めた作業ツリーは続く呼び出しが使い回し、作業ツリーでない dir は覚えない。
func TestWorkspace_DiscoveryIsRememberedByIsWorkTree(t *testing.T) {
	dir := newRepo(t)
	ctx := context.Background()
	w := NewWorkspace()
	if ok, err := w.IsWorkTree(ctx, dir); err != nil || !ok {
		t.Fatalf("IsWorkTree = %v, %v", ok, err)
	}
	w.mu.Lock()
	_, cached := w.repos[dir]
	w.mu.Unlock()
	if !cached {
		t.Fatal("IsWorkTree did not remember the discovered work tree")
	}
	plain := t.TempDir()
	if ok, err := w.IsWorkTree(ctx, plain); err != nil || ok {
		t.Fatalf("IsWorkTree(plain) = %v, %v", ok, err)
	}
	w.mu.Lock()
	_, cached = w.repos[plain]
	w.mu.Unlock()
	if cached {
		t.Error("a non-work-tree must not be cached")
	}
}

// commit が失敗（フックの拒否）しても、対象のファイルは本物の index に残らない。
func TestWorkspace_FailedCommitLeavesTheRealIndexUntouched(t *testing.T) {
	gitIdentity(t)
	dir := newRepo(t)
	installHook(t, dir, "pre-commit", "#!/bin/sh\nexit 1\n")
	write(t, filepath.Join(dir, "staged.txt"), "s\n")
	gitIn(t, dir, "add", "staged.txt")
	p := filepath.Join(dir, ".flywheel", "export.json")
	write(t, p, "{}")
	before := gitIn(t, dir, "status", "--porcelain")
	if _, err := NewWorkspace().CommitPaths(context.Background(), dir, "m", []string{p}); err == nil {
		t.Fatal("CommitPaths succeeded despite the failing hook")
	}
	if after := gitIn(t, dir, "status", "--porcelain"); after != before {
		t.Errorf("status changed by the failed commit:\nbefore %q\nafter  %q", before, after)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".git", "flywheel-index-*")); len(left) != 0 {
		t.Errorf("temporary index left: %v", left)
	}
}

// まだコミットが無いブランチ（unborn）でも、途中の状態の検査は通常の状態を返し、最初のコミットを作れる。
func TestWorkspace_UnbornBranch(t *testing.T) {
	gitIdentity(t)
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	w := NewWorkspace()
	ctx := context.Background()
	if got, err := w.InterruptedState(ctx, dir); err != nil || got != "" {
		t.Fatalf("InterruptedState = %q, %v; want empty", got, err)
	}
	p := filepath.Join(dir, "export.json")
	write(t, p, "{}")
	sha, err := w.CommitPaths(ctx, dir, "m", []string{p})
	if err != nil {
		t.Fatal(err)
	}
	if head := gitIn(t, dir, "rev-parse", "HEAD"); sha != head {
		t.Errorf("sha = %q, HEAD = %q", sha, head)
	}
	if st := gitIn(t, dir, "status", "--porcelain"); st != "" {
		t.Errorf("status after = %q", st)
	}
}

// git commit が成功した後に ctx が切れても、後始末（HEAD の取得・index の同期）は別の期限で
// 続き、失敗（期限切れ）にならずに SHA を返す。post-commit フックの子が出力の管を握っている間
// （git 自身は 0 で終了済み）に、フックが作った印のファイルを見て ctx を取り消す。
func TestWorkspace_CommitPathsKeepsTheCommitWhenCtxEndsAfterIt(t *testing.T) {
	gitIdentity(t)
	dir := newRepo(t)
	marker := filepath.Join(t.TempDir(), "committed")
	installHook(t, dir, "post-commit", "#!/bin/sh\n(sleep 0.5; touch '"+marker+"'; sleep 2) &\n")
	p := filepath.Join(dir, "export.json")
	write(t, p, "{}")
	w := NewWorkspace()
	if ok, err := w.IsWorkTree(context.Background(), dir); err != nil || !ok {
		t.Fatalf("IsWorkTree = %v, %v", ok, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			if _, err := os.Stat(marker); err == nil {
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	sha, err := w.CommitPaths(ctx, dir, "m", []string{p})
	if ctx.Err() == nil {
		t.Fatal("ctx was not cancelled during the commit; the test did not exercise the case")
	}
	if err != nil {
		t.Fatalf("CommitPaths: %v; want the commit kept despite ctx ending after it", err)
	}
	if head := gitIn(t, dir, "rev-parse", "HEAD"); sha != head {
		t.Errorf("sha = %q, HEAD = %q", sha, head)
	}
	if st := gitIn(t, dir, "status", "--porcelain", "--", "export.json"); st != "" {
		t.Errorf("real index does not match the new HEAD for the committed path: %q", st)
	}
}
