package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// このファイルは core.HeadWorktreeGit（J5 が PR の head を読む一時の worktree）の実装を持つ。
// 元のクローンの checkout と index は変えない。`git clone` は実行しない。`git fetch` は J5 と昇格の前の検査が head を
// 取り出すためだけの口（FetchHead）に限り、head の 1 コミットを一時の ref へ取る。他の口は fetch しない。

// HasCommit は core.HeadWorktreeGit の実装。クローンが無いときと、git が「無い」と答えたときは
// (false, nil)。
func (*Git) HasCommit(ctx context.Context, clone, sha string) (bool, error) {
	if _, err := os.Stat(filepath.Join(clone, ".git")); err != nil {
		return false, nil
	}
	_, err := run(ctx, filepath.Join(clone, ".git"), clone, "cat-file", "-e", sha+"^{commit}")
	if err == nil {
		return true, nil
	}
	var re *runError
	if errors.As(err, &re) && ctx.Err() == nil && exitCode(re) > 0 {
		return false, nil
	}
	return false, err
}

// AddHeadWorktree は core.HeadWorktreeGit の実装。
func (*Git) AddHeadWorktree(ctx context.Context, clone, path, sha string) error {
	baseGit := filepath.Join(clone, ".git")
	unlock, err := lockDir(ctx, baseGit)
	if err != nil {
		return err
	}
	defer unlock()
	_, err = run(ctx, baseGit, clone, "worktree", "add", "--detach", path, sha)
	return err
}

// RemoveHeadWorktree は core.HeadWorktreeGit の実装。`git worktree remove` の失敗（path が clone の
// worktree でない・既に無い）は返さず、path を直接消してから prune で記録を除く。ロックを取れなくても
// path は消す（記録は次の prune が除く）。
func (*Git) RemoveHeadWorktree(ctx context.Context, clone, path string) error {
	baseGit := filepath.Join(clone, ".git")
	unlock, lockErr := lockDir(ctx, baseGit)
	if lockErr == nil {
		defer unlock()
		_, _ = run(ctx, baseGit, clone, "worktree", "remove", "--force", path)
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove the worktree %s: %w", path, err)
	}
	if lockErr != nil {
		return lockErr
	}
	_, err := run(ctx, baseGit, clone, "worktree", "prune")
	return err
}

// FetchHead は core.HeadWorktreeGit の実装。origin から sha の 1 コミットだけを ref（一時の ref）へ
// 取る。タグ・他のブランチは取らず、FETCH_HEAD も書かず、自動の gc も走らせない。
func (*Git) FetchHead(ctx context.Context, clone, sha, ref string) error {
	if _, err := os.Stat(filepath.Join(clone, ".git")); err != nil {
		return fmt.Errorf("fetch the head %s: %s is not a clone", sha, clone)
	}
	_, err := run(ctx, filepath.Join(clone, ".git"), clone,
		"-c", "gc.auto=0", "-c", "maintenance.auto=false",
		"fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "origin", sha+":"+ref)
	return err
}

// ListHeadRefs は core.HeadWorktreeGit の実装。prefix の下の ref の完全な名前を返す。
func (*Git) ListHeadRefs(ctx context.Context, clone, prefix string) ([]string, error) {
	if _, err := os.Stat(filepath.Join(clone, ".git")); err != nil {
		return nil, nil
	}
	out, err := run(ctx, filepath.Join(clone, ".git"), clone, "for-each-ref", "--format=%(refname)", prefix)
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			refs = append(refs, l)
		}
	}
	return refs, nil
}

// DeleteHeadRef は core.HeadWorktreeGit の実装。ref が無くてもエラーにしない。
func (*Git) DeleteHeadRef(ctx context.Context, clone, ref string) error {
	if _, err := os.Stat(filepath.Join(clone, ".git")); err != nil {
		return nil
	}
	_, err := run(ctx, filepath.Join(clone, ".git"), clone, "update-ref", "-d", ref)
	return err
}
