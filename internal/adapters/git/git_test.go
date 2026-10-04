package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// このファイルは実物の git リポジトリ（t.TempDir()）で、adapter の振る舞い（作業ツリーの
// 検査・worktree の払い出し・直列化・GIT_DIR／GIT_WORK_TREE の明示・fetch／clone をしない）を
// 検証する。呼び出しの記録が要るものは、PATH の先頭に置いた git の包み（環境変数と
// サブコマンドを記録し、払い出しには遅延を入れて実際の git を呼ぶ）で見る。
// macOS・Linux の両方でスキップしない。

func realGit(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git is required to run this test: %v", err)
	}
	return p
}

// gitIn は dir で実物の git を実行する（テストの準備用。包みを通さない）。
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(realGit(t), args...)
	cmd.Dir = dir
	cmd.Env = append(cleanEnv(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func cleanEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(name, "GIT_") {
			env = append(env, kv)
		}
	}
	return env
}

// newClone は main ブランチに 1 コミットのある clean なクローン（origin 付き）を作る。
func newClone(t *testing.T, origin string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "base")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "f.txt")
	gitIn(t, dir, "commit", "-q", "-m", "init")
	if origin != "" {
		gitIn(t, dir, "remote", "add", "origin", origin)
	}
	return dir
}

// installWrapper は git の包みを PATH の先頭に置き、記録先のファイルのパスを返す。
// 記録の 1 行は「<種別> <時刻> GIT_DIR=<値> GIT_WORK_TREE=<値> ARGS=<引数>」。
// 引数に worktree add を含む呼び出しは、start・end の 2 行を記録し、間に delay 秒待つ。
func installWrapper(t *testing.T, delay string) string {
	t.Helper()
	gitExe := realGit(t)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "git.log")
	script := fmt.Sprintf(`#!/bin/sh
now() { perl -MTime::HiRes=time -e 'printf "%%.6f", time'; }
args="$*"
case "$args" in
  *"worktree add"*)
    echo "start $(now) GIT_DIR=$GIT_DIR GIT_WORK_TREE=$GIT_WORK_TREE ARGS=$args" >> %[1]q
    sleep %[3]s
    %[2]q "$@"
    rc=$?
    echo "end $(now) GIT_DIR=$GIT_DIR GIT_WORK_TREE=$GIT_WORK_TREE ARGS=$args" >> %[1]q
    exit $rc
    ;;
  *)
    echo "call $(now) GIT_DIR=$GIT_DIR GIT_WORK_TREE=$GIT_WORK_TREE ARGS=$args" >> %[1]q
    exec %[2]q "$@"
    ;;
esac
`, logPath, gitExe, delay)
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

type logLine struct {
	kind, gitDir, workTree, args string
	at                           float64
}

func readLog(t *testing.T, path string) []logLine {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatal(err)
	}
	var out []logLine
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		rest, args, _ := strings.Cut(line, " ARGS=")
		f := strings.Fields(rest)
		if len(f) != 4 {
			t.Fatalf("malformed log line %q", line)
		}
		at, _ := strconv.ParseFloat(f[1], 64)
		out = append(out, logLine{
			kind: f[0], at: at, args: args,
			gitDir: strings.TrimPrefix(f[2], "GIT_DIR="), workTree: strings.TrimPrefix(f[3], "GIT_WORK_TREE="),
		})
	}
	return out
}

func worktreesOf(t *testing.T, base string) string {
	t.Helper()
	return gitIn(t, base, "worktree", "list", "--porcelain")
}

func req(base, path string) core.WorktreeRequest {
	return core.WorktreeRequest{BaseClone: base, Path: path, Ref: "refs/heads/main"}
}

func TestInspect_MissingPathCreatesNothing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no", "such", "clone")
	st, err := New().Inspect(context.Background(), core.SlotTree{Path: missing})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if st.Exists {
		t.Error("Exists = true for a missing path")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(missing))), "no")); err == nil {
		t.Error("Inspect created a directory")
	}
	if _, err := os.Stat(filepath.Dir(missing)); err == nil {
		t.Error("Inspect created a directory")
	}
}

func TestInspect_CloneStatesAndOriginURLIsReturnedVerbatim(t *testing.T) {
	const url = "git@github.com:Owner/Name.git"
	clone := newClone(t, url)
	g := New()

	st, err := g.Inspect(context.Background(), core.SlotTree{Path: clone})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !st.Exists || !st.PointerOK || st.Dirty || st.OriginURL != url {
		t.Errorf("state = %+v, want clean clone with origin %q", st, url)
	}

	// 追跡外のファイルも未コミットの変更として数える。
	if err := os.WriteFile(filepath.Join(clone, "new.txt"), []byte("n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st, _ = g.Inspect(context.Background(), core.SlotTree{Path: clone}); !st.Dirty {
		t.Error("untracked file not reported as dirty")
	}
	// 追跡済みの変更も。
	if err := os.Remove(filepath.Join(clone, "new.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "f.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st, _ = g.Inspect(context.Background(), core.SlotTree{Path: clone}); !st.Dirty {
		t.Error("modified tracked file not reported as dirty")
	}
	// flywheel は変更を消さない。
	if data, _ := os.ReadFile(filepath.Join(clone, "f.txt")); string(data) != "changed\n" {
		t.Errorf("Inspect altered the working tree: %q", data)
	}

	// origin が無ければ OriginURL は ""。
	noOrigin := newClone(t, "")
	if st, err = g.Inspect(context.Background(), core.SlotTree{Path: noOrigin}); err != nil || st.OriginURL != "" || !st.Exists {
		t.Errorf("no-origin state = (%+v, %v)", st, err)
	}
}

func TestInspect_NotAGitDirectoryIsAnError(t *testing.T) {
	plain := t.TempDir()
	if _, err := New().Inspect(context.Background(), core.SlotTree{Path: plain}); err == nil {
		t.Error("Inspect of a non-git directory returned no error")
	}
}

// AC-288: 初回の払い出しで、元のクローンの refs/heads/<既定ブランチ> のコミットを分離した HEAD で指す作業ツリーができる。
func TestEnsureWorktree_CreatesDetachedWorktreeAtDefaultBranch(t *testing.T) {
	base := newClone(t, "https://github.com/o/r.git")
	path := filepath.Join(t.TempDir(), "wt", "SL-1")
	g := New()

	created, err := g.EnsureWorktree(context.Background(), req(base, path))
	if err != nil || !created {
		t.Fatalf("EnsureWorktree = (%v, %v), want created", created, err)
	}
	list := worktreesOf(t, base)
	head := gitIn(t, base, "rev-parse", "refs/heads/main")
	var block string
	for _, b := range strings.Split(list, "\n\n") {
		if strings.Contains(b, "worktree ") && (strings.Contains(b, path) || strings.Contains(b, realPath(path))) {
			block = b
		}
	}
	if block == "" || !strings.Contains(block, "HEAD "+head) || !strings.Contains(block, "detached") {
		t.Errorf("worktree list does not show %s detached at %s:\n%s", path, head, list)
	}

	st, err := g.Inspect(context.Background(), core.SlotTree{Path: path, BaseClone: base})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !st.Exists || !st.PointerOK || st.Dirty || st.OriginURL != "https://github.com/o/r.git" {
		t.Errorf("state = %+v", st)
	}
}

// AC-293: 同じスロットの 2 回目の払い出しでは git worktree add を実行せず、パスは変わらない。
func TestEnsureWorktree_SecondCallDoesNotRunAddAgain(t *testing.T) {
	base := newClone(t, "")
	path := filepath.Join(t.TempDir(), "SL-1")
	logPath := installWrapper(t, "0")
	g := New()

	for i, wantCreated := range []bool{true, false} {
		created, err := g.EnsureWorktree(context.Background(), req(base, path))
		if err != nil || created != wantCreated {
			t.Fatalf("call %d = (%v, %v), want created=%v", i, created, err, wantCreated)
		}
	}
	adds := 0
	for _, l := range readLog(t, logPath) {
		if l.kind == "start" {
			adds++
		}
	}
	if adds != 1 {
		t.Errorf("git worktree add ran %d times, want 1", adds)
	}
}

// AC-289・290: ref が無い・パスにファイルがあると払い出しは失敗する。
func TestEnsureWorktree_FailuresReturnErrors(t *testing.T) {
	base := newClone(t, "")
	g := New()

	_, err := g.EnsureWorktree(context.Background(), core.WorktreeRequest{
		BaseClone: base, Path: filepath.Join(t.TempDir(), "SL-1"), Ref: "refs/heads/nope",
	})
	if err == nil {
		t.Error("missing ref: want an error")
	}

	occupied := filepath.Join(t.TempDir(), "SL-2")
	if err := os.WriteFile(occupied, []byte("a file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := g.EnsureWorktree(context.Background(), req(base, occupied)); err == nil {
		t.Error("path occupied by a file: want an error")
	}
	if data, _ := os.ReadFile(occupied); string(data) != "a file" {
		t.Errorf("the existing file was altered: %q", data)
	}

	if _, err := g.EnsureWorktree(context.Background(), req(filepath.Join(t.TempDir(), "nobase"), filepath.Join(t.TempDir(), "x"))); err == nil {
		t.Error("missing base clone: want an error")
	}
}

// AC-292（2 つのプロセスの代わりに、別々に開いたロックファイルを持つ 2 つの goroutine。flock は
// 開いたファイルごとに排他する）: 同じ元のクローンへの払い出しは、同時に求めても git worktree add が重ならず、両方成功する。
func TestEnsureWorktree_ConcurrentAddsAreSerialized(t *testing.T) {
	base := newClone(t, "")
	logPath := installWrapper(t, "0.4")
	root := t.TempDir()
	paths := []string{filepath.Join(root, "SL-1"), filepath.Join(root, "SL-2")}

	var wg sync.WaitGroup
	errs := make([]error, len(paths))
	created := make([]bool, len(paths))
	for i := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			created[i], errs[i] = New().EnsureWorktree(context.Background(), req(base, paths[i]))
		}()
	}
	wg.Wait()
	for i := range paths {
		if errs[i] != nil || !created[i] {
			t.Fatalf("path %d: (%v, %v), want created", i, created[i], errs[i])
		}
	}
	var starts, ends []float64
	for _, l := range readLog(t, logPath) {
		switch l.kind {
		case "start":
			starts = append(starts, l.at)
		case "end":
			ends = append(ends, l.at)
		}
	}
	if len(starts) != 2 || len(ends) != 2 {
		t.Fatalf("starts=%v ends=%v, want 2 each", starts, ends)
	}
	// 区間が重なっていないこと: 遅い方の start が、早い方の end 以降であること。
	minEnd := ends[0]
	if ends[1] < minEnd {
		minEnd = ends[1]
	}
	maxStart := starts[0]
	if starts[1] > maxStart {
		maxStart = starts[1]
	}
	if maxStart < minEnd {
		t.Errorf("git worktree add ran concurrently: starts=%v ends=%v", starts, ends)
	}
	list := worktreesOf(t, base)
	for _, p := range paths {
		if !strings.Contains(list, filepath.Base(p)) {
			t.Errorf("worktree %s missing from list:\n%s", p, list)
		}
	}
}

// AC-294・296: 作業ツリーの外から呼ぶ git はすべて GIT_DIR と GIT_WORK_TREE を持ち、
// 呼び出し元の環境の GIT_DIR に左右されず、fetch・clone を実行しない。
func TestAllGitCallsCarryExplicitGitDirAndWorkTreeAndNeverFetchOrClone(t *testing.T) {
	base := newClone(t, "git@github.com:o/r.git")
	path := filepath.Join(t.TempDir(), "SL-1")
	other := newClone(t, "")
	logPath := installWrapper(t, "0")
	// 呼び出し元の環境の GIT_DIR・GIT_WORK_TREE は引き継がない。
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	g := New()

	if _, err := g.EnsureWorktree(context.Background(), req(base, path)); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Inspect(context.Background(), core.SlotTree{Path: path, BaseClone: base}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Inspect(context.Background(), core.SlotTree{Path: base}); err != nil {
		t.Fatal(err)
	}

	lines := readLog(t, logPath)
	if len(lines) < 5 {
		t.Fatalf("recorded %d git calls, want at least 5: %+v", len(lines), lines)
	}
	for _, l := range lines {
		if l.gitDir == "" || l.workTree == "" {
			t.Errorf("git %s ran without GIT_DIR/GIT_WORK_TREE: %+v", l.args, l)
		}
		if realPath(l.gitDir) == realPath(filepath.Join(other, ".git")) || realPath(l.workTree) == realPath(other) {
			t.Errorf("git %s inherited the caller's GIT_DIR/GIT_WORK_TREE: %+v", l.args, l)
		}
		sub := strings.Fields(l.args)[0]
		if sub == "fetch" || sub == "clone" || sub == "pull" || sub == "reset" || sub == "clean" || sub == "checkout" {
			t.Errorf("forbidden git subcommand %q was run: %+v", sub, l)
		}
	}
	// 作業ツリーに対する呼び出しの GIT_DIR は元のクローンが持つ管理ディレクトリ。
	var sawWorktreeCall bool
	for _, l := range lines {
		if realPath(l.workTree) == realPath(path) {
			sawWorktreeCall = true
			if !strings.Contains(realPath(l.gitDir), filepath.Join(realPath(base), ".git", "worktrees")) {
				t.Errorf("worktree call GIT_DIR = %s, want an admin dir under the base clone", l.gitDir)
			}
		}
	}
	if !sawWorktreeCall {
		t.Error("no recorded call targeted the worktree")
	}
}

// AC-295: .git のポインタが元のクローンのその作業ツリー以外を指していると PointerOK=false。
func TestInspect_PointerPointingElsewhereIsReported(t *testing.T) {
	base := newClone(t, "")
	otherBase := newClone(t, "")
	g := New()
	mine := filepath.Join(t.TempDir(), "SL-1")
	theirs := filepath.Join(t.TempDir(), "SL-2")
	if _, err := g.EnsureWorktree(context.Background(), req(base, mine)); err != nil {
		t.Fatal(err)
	}
	if _, err := g.EnsureWorktree(context.Background(), req(otherBase, theirs)); err != nil {
		t.Fatal(err)
	}

	pointerTo := func(target string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(mine, ".git"), []byte("gitdir: "+target+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 別のクローンの作業ツリーを指す。
	entries, _ := os.ReadDir(filepath.Join(otherBase, ".git", "worktrees"))
	pointerTo(filepath.Join(otherBase, ".git", "worktrees", entries[0].Name()))
	st, err := g.Inspect(context.Background(), core.SlotTree{Path: mine, BaseClone: base})
	if err != nil || st.PointerOK || st.PointerProblem == "" {
		t.Errorf("pointer to another clone: (%+v, %v), want PointerOK=false with a reason", st, err)
	}
	// 存在しない場所を指す。
	pointerTo(filepath.Join(t.TempDir(), "nowhere"))
	if st, err = g.Inspect(context.Background(), core.SlotTree{Path: mine, BaseClone: base}); err != nil || st.PointerOK {
		t.Errorf("pointer to nowhere: (%+v, %v), want PointerOK=false", st, err)
	}
	// ポインタのファイルが無い（通常のクローンの .git ディレクトリ）。
	st, err = g.Inspect(context.Background(), core.SlotTree{Path: otherBase, BaseClone: base})
	if err != nil || st.PointerOK {
		t.Errorf("a plain clone passed as a worktree: (%+v, %v), want PointerOK=false", st, err)
	}
}

func TestGitNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	clone := t.TempDir()
	if err := os.MkdirAll(filepath.Join(clone, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := New().Inspect(context.Background(), core.SlotTree{Path: clone})
	if !errors.Is(err, ErrGitNotFound) {
		t.Errorf("error = %v, want ErrGitNotFound", err)
	}
}

// status.showUntrackedFiles=no でも、追跡外のファイルを未コミットの変更として数える。
func TestInspect_UntrackedFilesCountEvenWhenConfigHidesThem(t *testing.T) {
	clone := newClone(t, "")
	gitIn(t, clone, "config", "status.showUntrackedFiles", "no")
	if err := os.WriteFile(filepath.Join(clone, "u.txt"), []byte("u"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := New().Inspect(context.Background(), core.SlotTree{Path: clone})
	if err != nil || !st.Dirty {
		t.Errorf("state = (%+v, %v), want Dirty", st, err)
	}
}

// 相対パスのポインタ（worktree.useRelativePaths=true の git が書く形）の作業ツリーも登録済みと
// 認める。git の版に依らず相対パスの分岐を通すため、払い出した後に両端のパスを相対へ書き換える。
// 元のクローンがシンボリックリンク越しでも同じ。
func TestInspect_RelativePathWorktreesAreRecognized(t *testing.T) {
	for _, viaSymlink := range []bool{false, true} {
		t.Run(fmt.Sprintf("symlinked base=%v", viaSymlink), func(t *testing.T) {
			realBase := newClone(t, "")
			base := realBase
			if viaSymlink {
				base = filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(realBase, base); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(t.TempDir(), "SL-1")
			if _, err := New().EnsureWorktree(context.Background(), req(base, path)); err != nil {
				t.Fatal(err)
			}
			admin := filepath.Join(realBase, ".git", "worktrees", "SL-1")
			rel := func(from, to string) string {
				r, err := filepath.Rel(realPath(from), realPath(to))
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			if err := os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: "+rel(path, admin)+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(admin, "gitdir"), []byte(rel(admin, filepath.Join(path, ".git"))+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			st, err := New().Inspect(context.Background(), core.SlotTree{Path: path, BaseClone: base})
			if err != nil || !st.PointerOK {
				t.Errorf("state = (%+v, %v), want PointerOK", st, err)
			}
		})
	}
}

func TestGitNotFoundIsCoreGitUnavailable(t *testing.T) {
	if !errors.Is(ErrGitNotFound, core.ErrGitUnavailable) {
		t.Error("ErrGitNotFound must wrap core.ErrGitUnavailable")
	}
}

// 照合が報告のブランチの無いときに使う、作業ツリーの現在のブランチ。detached HEAD は "" になる。
func TestInspect_ReportsCurrentBranch(t *testing.T) {
	clone := newClone(t, "https://github.com/o/r.git")
	g := New()
	st, err := g.Inspect(context.Background(), core.SlotTree{Path: clone})
	if err != nil || st.Branch != "main" {
		t.Fatalf("Branch = %q, err=%v; want main", st.Branch, err)
	}
	gitIn(t, clone, "checkout", "-b", "feat/x")
	if st, err = g.Inspect(context.Background(), core.SlotTree{Path: clone}); err != nil || st.Branch != "feat/x" {
		t.Fatalf("Branch = %q, err=%v; want feat/x", st.Branch, err)
	}

	base := newClone(t, "https://github.com/o/r.git")
	path := filepath.Join(t.TempDir(), "wt", "SL-1")
	if _, err := g.EnsureWorktree(context.Background(), req(base, path)); err != nil {
		t.Fatal(err)
	}
	st, err = g.Inspect(context.Background(), core.SlotTree{Path: path, BaseClone: base})
	if err != nil || st.Branch != "" || !st.PointerOK {
		t.Fatalf("detached worktree state = %+v, err=%v; want empty Branch", st, err)
	}
}
