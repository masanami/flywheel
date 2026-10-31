package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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

// 発見の結果は dir ごとにキャッシュする。core の呼び出し順（1 回の backup の最初に IsWorkTree）に
// 依存し、IsWorkTree を呼ばずに他のメソッドを呼んでも、ここで突き止める。
//
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
//
// git の起動は 1 回（`rev-parse --git-path …（途中の状態の 5 つ）--git-path HEAD`）。
// 出力の先頭 5 行が各パス、最後の行が HEAD のファイルで、`ref:` で始まらなければ detached HEAD。
// まだコミットが無いブランチ（unborn）でも HEAD のファイルは読めるため通常の状態になる。
func (w *Workspace) InterruptedState(ctx context.Context, dir string) (string, error) {
	r, err := w.repoFor(ctx, dir)
	if err != nil {
		return "", err
	}
	marks := []struct{ path, reason string }{
		{"rebase-merge", "rebase in progress"},
		{"rebase-apply", "rebase in progress"},
		{"MERGE_HEAD", "merge in progress"},
		{"CHERRY_PICK_HEAD", "cherry-pick in progress"},
		{"REVERT_HEAD", "revert in progress"},
	}
	args := []string{"rev-parse"}
	for _, m := range marks {
		args = append(args, "--git-path", m.path)
	}
	args = append(args, "--git-path", "HEAD")
	out, err := r.run(ctx, args...)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != len(marks)+1 {
		return "", fmt.Errorf("git rev-parse: unexpected output %q", out)
	}
	for i, m := range marks {
		p := strings.TrimSpace(lines[i])
		if !filepath.IsAbs(p) {
			p = filepath.Join(r.root, p)
		}
		if _, err := os.Lstat(p); err == nil {
			return m.reason, nil
		}
	}
	hp := strings.TrimSpace(lines[len(marks)])
	if !filepath.IsAbs(hp) {
		hp = filepath.Join(r.root, hp)
	}
	head, err := os.ReadFile(hp)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(strings.TrimSpace(string(head)), "ref:") {
		return "detached HEAD", nil
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
	// add・commit は本物の index のコピー（一時的な GIT_INDEX_FILE）で行う。commit が失敗
	// （期限切れ・フックの拒否）しても、対象のファイルが本物の index に残らない。
	tmpIndex, cleanup, err := r.tempIndex()
	if err != nil {
		return "", err
	}
	defer cleanup()
	// HEAD の移動を見るため、コミットの前の HEAD を控える（まだコミットが無ければ空）。
	before, _ := r.run(ctx, "rev-parse", "-q", "--verify", "HEAD")
	if _, err := r.runWithIndex(ctx, tmpIndex, append([]string{"add", "--"}, rel...)...); err != nil {
		return "", err
	}
	_, commitErr := r.runWithIndex(ctx, tmpIndex, append([]string{"commit", "--only", "-m", message, "--"}, rel...)...)
	// ここから先は ctx の期限切れで失敗にしない（短い期限を別に持つ）。コミットの後のフック
	// の最中に期限が切れても、HEAD が進んでいれば本物の index を合わせる。
	done, cancel := context.WithTimeout(context.WithoutCancel(ctx), afterCommitTimeout)
	defer cancel()
	sha, shaErr := r.run(done, "rev-parse", "HEAD")
	if shaErr == nil && strings.TrimSpace(sha) != strings.TrimSpace(before) {
		// 本物の index の対象パスを新しい HEAD に合わせる（ほかのパスのステージは変えない。
		// 作業ツリーには触れない）。失敗してもコミットは有効なので結果は変えないが、次の
		// 通常のコミットが対象パスを巻き戻しうる。
		_, _ = r.run(done, append([]string{"reset", "-q", "--"}, rel...)...)
	}
	if commitErr != nil {
		return "", commitErr
	}
	if shaErr != nil {
		return "", shaErr
	}
	return strings.TrimSpace(sha), nil
}

// afterCommitTimeout は、コミットが済んだ後の後始末（HEAD の取得・index の同期）に使う期限。
var afterCommitTimeout = 10 * time.Second

// tempIndex は本物の index のコピーを gitDir に作り、そのパスと後始末の関数を返す
// （index がまだ無ければ空のまま）。
func (r repo) tempIndex() (string, func(), error) {
	f, err := os.CreateTemp(r.gitDir, "flywheel-index-*")
	if err != nil {
		return "", nil, err
	}
	name := f.Name()
	cleanup := func() { _ = os.Remove(name); _ = os.Remove(name + ".lock") }
	data, err := os.ReadFile(filepath.Join(r.gitDir, "index"))
	switch {
	case err == nil:
		_, err = f.Write(data)
	case os.IsNotExist(err):
		// 空のファイルは壊れた index として拒否されるため、無ければ git に新しく作らせる。
		err = os.Remove(name)
		if err == nil {
			return name, cleanup, f.Close()
		}
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return name, cleanup, nil
}

// runWithIndex は run と同じだが、index に GIT_INDEX_FILE を指定する。
func (r repo) runWithIndex(ctx context.Context, index string, args ...string) (string, error) {
	args = append([]string{"--literal-pathspecs"}, args...)
	cmd, err := newCmd(ctx, r.gitDir, r.root, args...)
	if err != nil {
		return "", err
	}
	cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
	return execute(cmd, args)
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
