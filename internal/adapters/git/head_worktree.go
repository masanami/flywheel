package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// このファイルは core.HeadWorktreeGit（J5 が PR の head を読む一時の worktree）の実装を持つ。
// 元のクローンの checkout と index は変えず、`git fetch`・`git clone` を実行しない。

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
