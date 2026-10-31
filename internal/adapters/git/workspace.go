package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/masanami/flywheel/internal/core"
)

// Workspace は core.WorkspaceGit の実装（internal/core/workspace_git.go）。ゼロ値で
// 使える。push・`add -A`・fetch・clone をしない。
type Workspace struct {
	mu    sync.Mutex
	repos map[string]repo
}

// NewWorkspace は Workspace を返す。
func NewWorkspace() *Workspace { return &Workspace{} }

// repoFor は dir の作業ツリーを返す。IsWorkTree が突き止めた結果があれば使い回し、
// 無ければ突き止める（1 回の backup の中で `git rev-parse` を 1 回にするため）。
func (w *Workspace) repoFor(ctx context.Context, dir string) (repo, error) {
	w.mu.Lock()
	r, cached := w.repos[dir]
	w.mu.Unlock()
	if cached {
		return r, nil
	}
	r, ok, err := discover(ctx, dir)
	if err != nil {
		return r, err
	}
	if !ok {
		return r, fmt.Errorf("git: %s is not in a git work tree", dir)
	}
	w.remember(dir, r)
	return r, nil
}

func (w *Workspace) remember(dir string, r repo) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.repos == nil {
		w.repos = map[string]repo{}
	}
	w.repos[dir] = r
}

var _ core.WorkspaceGit = (*Workspace)(nil)

// repo は dir を含む作業ツリーの GIT_DIR と作業ツリーのルート（実パス）。
type repo struct{ gitDir, root string }

// discover は dir を含む作業ツリーを突き止める。継承した GIT_* は落とす。作業ツリー
// でなければ ok=false。
func discover(ctx context.Context, dir string) (r repo, ok bool, err error) {
	args := []string{"rev-parse", "--show-toplevel", "--absolute-git-dir"}
	cmd, err := newCmd(ctx, "", "", args...)
	if err != nil {
		return r, false, err
	}
	cmd.Dir = dir
	// git の応答の文言で判定するため、ロケールを固定する。
	cmd.Env = append(dropGitDirVars(cmd.Env), "LC_ALL=C", "LANGUAGE=")
	out, err := execute(cmd, args[:1])
	if err != nil {
		var re *runError
		msg := err.Error()
		if errors.As(err, &re) {
			msg = re.msg
		}
		// 作業ツリーでない（bare・.git の中を含む）ことを示す git の応答だけを (false, nil) にする。
		// safe.directory の拒否・壊れた設定など、ほかの 128 は error にする。
		if exitCode(err) == 128 && (strings.Contains(msg, "not a git repository") || strings.Contains(msg, "must be run in a work tree")) {
			return r, false, nil
		}
		return r, false, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		return r, false, fmt.Errorf("git rev-parse: unexpected output %q", out)
	}
	return repo{gitDir: lines[1], root: realPath(lines[0])}, true, nil
}

// dropGitDirVars は gitEnv が足した空の GIT_DIR・GIT_WORK_TREE を外す。ワークスペースは
// 親のディレクトリのリポジトリの中にありうるため、発見だけは dir からの探索で行い、
// 見つけた後の呼び出しはすべて GIT_DIR・GIT_WORK_TREE を明示する（git.go の規約の例外は
// この発見の 1 回だけ）。
func dropGitDirVars(env []string) []string {
	var out []string
	for _, kv := range env {
		if kv == "GIT_DIR=" || kv == "GIT_WORK_TREE=" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// relPaths は絶対パスを作業ツリーのルートからの相対パスにする（存在しないパスも、
// 存在する祖先の実パスを使って解決する）。ルートの外なら error。
func (r repo) relPaths(paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		// パス自身がシンボリックリンクでもリンク先へ解決せず、親だけを解決する。
		rel, err := filepath.Rel(r.root, filepath.Join(resolveExisting(filepath.Dir(abs)), filepath.Base(abs)))
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("git: %s is outside the work tree %s", p, r.root)
		}
		out = append(out, rel)
	}
	return out, nil
}

// resolveExisting は存在する最長の祖先のシンボリックリンクを解決して残りを結合する。
func resolveExisting(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for {
		if _, err := os.Lstat(p); err == nil {
			return filepath.Join(realPath(p), rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// run は pathspec をリテラルとして扱う（glob・マジックで指定外のファイルに一致させない）。
func (r repo) run(ctx context.Context, args ...string) (string, error) {
	return run(ctx, r.gitDir, r.root, append([]string{"--literal-pathspecs"}, args...)...)
}

// IsWorkTree は core.WorkspaceGit の実装。
//
// 発見の結果は同じ dir への続く呼び出し（IsIgnored・HasChanges・CommitPaths・InterruptedState）が
// 使い回す。呼ぶたびに突き止め直すので、backup の段が最初に呼べば、古い結果は残らない。
func (w *Workspace) IsWorkTree(ctx context.Context, dir string) (bool, error) {
	r, ok, err := discover(ctx, dir)
	if err != nil {
		return false, err
	}
	if ok {
		w.remember(dir, r)
	} else {
		w.mu.Lock()
		delete(w.repos, dir)
		w.mu.Unlock()
	}
	return ok, nil
}

// InterruptedState は core.WorkspaceGit の実装。
func (w *Workspace) InterruptedState(ctx context.Context, dir string) (string, error) {
	r, err := w.repoFor(ctx, dir)
	if err != nil {
		return "", err
	}
	for _, m := range []struct{ path, reason string }{
		{"rebase-merge", "rebase in progress"},
		{"rebase-apply", "rebase in progress"},
		{"MERGE_HEAD", "merge in progress"},
		{"CHERRY_PICK_HEAD", "cherry-pick in progress"},
		{"REVERT_HEAD", "revert in progress"},
	} {
		out, err := r.run(ctx, "rev-parse", "--git-path", m.path)
		if err != nil {
			return "", err
		}
		p := strings.TrimSpace(out)
		if !filepath.IsAbs(p) {
			p = filepath.Join(r.root, p)
		}
		if _, err := os.Lstat(p); err == nil {
			return m.reason, nil
		}
	}
	// HEAD がブランチを指していない（detached HEAD）と、symbolic-ref -q は 1 で終わる。
	if _, err := r.run(ctx, "symbolic-ref", "-q", "HEAD"); err != nil {
		if exitCode(err) == 1 {
			return "detached HEAD", nil
		}
		return "", err
	}
	return "", nil
}

// IsIgnored は core.WorkspaceGit の実装。
func (w *Workspace) IsIgnored(ctx context.Context, dir, path string) (bool, error) {
	r, err := w.repoFor(ctx, dir)
	if err != nil {
		return false, err
	}
	rel, err := r.relPaths([]string{path})
	if err != nil {
		return false, err
	}
	// check-ignore は literal pathspec に未対応のため、--literal-pathspecs を付けずに呼ぶ。
	_, err = run(ctx, r.gitDir, r.root, "check-ignore", "-q", "--", rel[0])
	switch {
	case err == nil:
		return true, nil
	case exitCode(err) == 1:
		return false, nil
	}
	return false, err
}

// HasChanges は core.WorkspaceGit の実装。
func (w *Workspace) HasChanges(ctx context.Context, dir string, paths []string) (bool, error) {
	r, err := w.repoFor(ctx, dir)
	if err != nil {
		return false, err
	}
	rel, err := r.relPaths(paths)
	if err != nil {
		return false, err
	}
	out, err := r.run(ctx, append([]string{"status", "--porcelain", "--untracked-files=all", "--"}, rel...)...)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// CommitPaths は core.WorkspaceGit の実装。
func (w *Workspace) CommitPaths(ctx context.Context, dir, message string, paths []string) (string, error) {
	if len(paths) == 0 {
		return "", errors.New("git: no paths to commit")
	}
	r, err := w.repoFor(ctx, dir)
	if err != nil {
		return "", err
	}
	rel, err := r.relPaths(paths)
	if err != nil {
		return "", err
	}
	rel, err = r.stageable(ctx, rel)
	if err != nil {
		return "", err
	}
	if len(rel) == 0 {
		return "", errors.New("git: none of the paths has anything to commit")
	}
	if _, err := r.run(ctx, append([]string{"add", "--"}, rel...)...); err != nil {
		return "", err
	}
	if _, err := r.run(ctx, append([]string{"commit", "--only", "-m", message, "--"}, rel...)...); err != nil {
		return "", err
	}
	sha, err := r.run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sha), nil
}

// stageable は、git がコミットの対象にできるパスだけを残す（追跡されているか、追跡外で
// 除外の対象でないファイルを含むもの）。実在しない・空のディレクトリ・除外されたファイル
// だけのパスは `git commit --only` を失敗させ、ほかのパスのコミットまで巻き込むため。
func (r repo) stageable(ctx context.Context, rel []string) ([]string, error) {
	var out []string
	for _, p := range rel {
		tracked, err := r.run(ctx, "ls-files", "--", p)
		if err != nil {
			return nil, err
		}
		untracked, err := r.run(ctx, "ls-files", "--others", "--exclude-standard", "--", p)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(tracked) != "" || strings.TrimSpace(untracked) != "" {
			out = append(out, p)
		}
	}
	return out, nil
}
