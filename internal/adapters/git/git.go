// Package git は core の SlotGit（internal/core/slot_git.go）を `git` の子プロセスで
// 実装する adapter である（親要件チケット #98 §実行スロット・§プロバイダ worktree。
// M3H8・M3P45・M3P47）。
//
// 規則（何を満たせば割り当てるか・origin の正規化と比較）は core が持ち、ここは
// 入出力だけを担う。ストアを import せず、core の IF（SlotGit とその型）だけに
// 依存する（internal/adapters/github と同じ形）。
//
// 守ること:
//   - 作業ツリーの外側から呼ぶ git は、必ず GIT_DIR と GIT_WORK_TREE を明示する
//     （作業ツリーの .git は書き換え可能なポインタのファイルのため。worktree は
//     元のクローンが持つ管理ディレクトリを自分で突き止めて GIT_DIR にする）。
//   - git fetch・git clone を実行しない。作業ツリーの変更を消さない（reset・
//     clean・checkout -- をしない）。ネットワークを使わない。
//   - `git worktree add` は、同じ元のクローンについてプロセスをまたいで直列に
//     実行する（共有の .git を触るため。元のクローンの .git 配下のロックファイルで
//     排他する）。
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

// ErrGitNotFound は `git` が PATH に無いことを表す。
var ErrGitNotFound = fmt.Errorf("git: git executable not found in PATH: %w", core.ErrGitUnavailable)

// lockFileName は元のクローンの .git 配下に置く、払い出しの排他用のファイル名。
const lockFileName = "flywheel-worktree-add.lock"

// lockPollInterval は排他の取得を待つ間隔（ctx の取り消しに応じられるよう
// ブロックせずに待つ）。
const lockPollInterval = 10 * time.Millisecond

// Git は core.SlotGit の実装。ゼロ値で使える。
type Git struct{}

// New は Git を返す。
func New() *Git { return &Git{} }

var _ core.SlotGit = (*Git)(nil)

// clearedEnv は子プロセスへ引き継がない git の環境変数（呼び出し元の環境が
// 作業ツリーの解決を変えないようにする）。
var clearedEnv = map[string]bool{
	"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true, "GIT_COMMON_DIR": true,
	"GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	"GIT_NAMESPACE": true, "GIT_PREFIX": true, "GIT_CEILING_DIRECTORIES": true,
	"GIT_DISCOVERY_ACROSS_FILESYSTEM": true, "GIT_OPTIONAL_LOCKS": true, "GIT_TERMINAL_PROMPT": true,
}

func gitEnv(gitDir, workTree string) []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !clearedEnv[name] {
			env = append(env, kv)
		}
	}
	return append(env,
		"GIT_DIR="+gitDir,
		"GIT_WORK_TREE="+workTree,
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
	)
}

// run は GIT_DIR・GIT_WORK_TREE を明示して git を実行し、標準出力を返す。
// PATH は呼び出しごとに引く。
func run(ctx context.Context, gitDir, workTree string, args ...string) (string, error) {
	exe, err := exec.LookPath("git")
	if err != nil {
		return "", ErrGitNotFound
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = workTree
	cmd.Env = gitEnv(gitDir, workTree)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), &runError{args: args, msg: msg, err: err}
	}
	return stdout.String(), nil
}

type runError struct {
	args []string
	msg  string
	err  error
}

func (e *runError) Error() string { return fmt.Sprintf("git %s: %s", e.args[0], e.msg) }
func (e *runError) Unwrap() error { return e.err }

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// realPath は p のシンボリックリンクを解決した絶対パス（解決できなければ Clean した p）。
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// verifyWorktreePointer は worktree の作業ツリー path の .git のポインタが、元の
// クローン base のその作業ツリーを指していることを確かめる。OK なら元のクローンが
// 持つその作業ツリーの管理ディレクトリ（GIT_DIR に使う）を返す。違えば problem に
// 理由を返す。ポインタの内容を GIT_DIR に使わず、元のクローン側の登録から突き
// 止める。
func verifyWorktreePointer(base, path string) (adminDir, problem string) {
	baseGit := filepath.Join(base, ".git")
	if fi, err := os.Stat(baseGit); err != nil || !fi.IsDir() {
		return "", "the base clone has no .git directory: " + base
	}
	pointer := filepath.Join(path, ".git")
	data, err := os.ReadFile(pointer)
	if err != nil {
		return "", "the working tree has no .git pointer file: " + pointer
	}
	target, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return "", "the .git file is not a gitdir pointer"
	}
	target = strings.TrimSpace(target)
	if !filepath.IsAbs(target) {
		// git は相対パスを両端の実パスから作る。シンボリックリンクを解決してから結合する。
		target = filepath.Join(realPath(path), target)
	}

	entries, _ := os.ReadDir(filepath.Join(baseGit, "worktrees"))
	for _, e := range entries {
		admin := filepath.Join(baseGit, "worktrees", e.Name())
		back, err := os.ReadFile(filepath.Join(admin, "gitdir"))
		if err != nil {
			continue
		}
		backPath := strings.TrimSpace(string(back))
		if !filepath.IsAbs(backPath) {
			backPath = filepath.Join(realPath(admin), backPath) // worktree.useRelativePaths
		}
		if realPath(backPath) != realPath(pointer) {
			continue
		}
		if realPath(target) != realPath(admin) {
			return "", fmt.Sprintf("the .git pointer points at %s, not at %s", target, admin)
		}
		return admin, ""
	}
	return "", fmt.Sprintf("%s is not a worktree registered in the base clone %s (the .git pointer points at %s)", path, base, target)
}

// Inspect は core.SlotGit の実装。
func (*Git) Inspect(ctx context.Context, tree core.SlotTree) (core.SlotTreeState, error) {
	var st core.SlotTreeState
	if _, err := os.Lstat(tree.Path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return st, nil
		}
		return st, err
	}
	st.Exists = true

	gitDir := filepath.Join(tree.Path, ".git")
	if tree.BaseClone != "" {
		admin, problem := verifyWorktreePointer(tree.BaseClone, tree.Path)
		if problem != "" {
			st.PointerProblem = problem
			return st, nil
		}
		gitDir = admin
	}
	st.PointerOK = true

	out, err := run(ctx, gitDir, tree.Path, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return st, err
	}
	st.Dirty = strings.TrimSpace(out) != ""

	url, err := run(ctx, gitDir, tree.Path, "config", "--get", "remote.origin.url")
	if err != nil {
		if exitCode(err) == 1 { // origin が無い
			return st, nil
		}
		return st, err
	}
	st.OriginURL = strings.TrimSpace(url)
	return st, nil
}

// EnsureWorktree は core.SlotGit の実装。
func (*Git) EnsureWorktree(ctx context.Context, req core.WorktreeRequest) (bool, error) {
	baseGit := filepath.Join(req.BaseClone, ".git")
	if fi, err := os.Stat(baseGit); err != nil || !fi.IsDir() {
		return false, fmt.Errorf("the base clone has no .git directory: %s", req.BaseClone)
	}
	unlock, err := lockDir(ctx, baseGit)
	if err != nil {
		return false, err
	}
	defer unlock()

	// 払い出し済み（作業ツリーの .git のポインタのファイルがある）なら作り直さない。
	// ポインタが元のクローンのその作業ツリーを指しているかは作り直す理由ではなく、
	// Inspect が検査して割り当てずに知らせる（ポインタが書き換わった作業ツリーを
	// git worktree add で上書きしようとしない）。パスがファイルなど、払い出し済みと
	// 言えないものは git worktree add に任せ、失敗はそのまま返す。
	if fi, err := os.Lstat(filepath.Join(req.Path, ".git")); err == nil && !fi.IsDir() {
		return false, nil
	}
	if _, err := run(ctx, baseGit, req.BaseClone, "worktree", "add", "--detach", req.Path, req.Ref); err != nil {
		return false, err
	}
	return true, nil
}

// lockDir は dir の下のロックファイルへ排他ロック（flock）を取り、解放関数を返す。
func lockDir(ctx context.Context, dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, lockFileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(lockPollInterval):
		}
	}
}
