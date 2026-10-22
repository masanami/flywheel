package github

import (
	"context"
	"fmt"
	"strings"

	"github.com/masanami/flywheel/internal/core"
)

// 承認済みの release の実行の口だけが呼ぶ、PR のマージの依頼（core.UpstreamPullRequestMerger）。
// このパッケージで唯一の書き込み。渡す引数は PR の URL・方式のフラグ・--match-head-commit だけで、
// --admin・--auto・--delete-branch は付けない（ブランチの削除は GitHub のリポジトリ設定に任せる）。

var mergeFlags = map[string]string{
	core.MergeMethodSquash: "--squash",
	core.MergeMethodMerge:  "--merge",
	core.MergeMethodRebase: "--rebase",
}

// MergePullRequest は `gh pr merge <prURL> --<方式> --match-head-commit <matchHeadSHA>` を 1 回起動する。
// 方式が閉集合の外、URL・head が空のときは gh を起動せずエラーにする。終了コードが 0 でなければ失敗を返す。
func (c *Client) MergePullRequest(ctx context.Context, prURL, method, matchHeadSHA string) error {
	flag, ok := mergeFlags[method]
	if !ok {
		return fmt.Errorf("adapters/github: merge pull request: unknown merge method %q", method)
	}
	if prURL == "" || matchHeadSHA == "" {
		return fmt.Errorf("adapters/github: merge pull request: pull request URL and head SHA are required")
	}
	what := fmt.Sprintf("merge pull request %s", prURL)
	res := c.run(ctx, "pr", "merge", prURL, flag, "--match-head-commit", matchHeadSHA)
	if res.timedOut {
		return fmt.Errorf("adapters/github: %s timed out after %s: %w", what, c.timeout, context.DeadlineExceeded)
	}
	if res.ctxErr != nil {
		return fmt.Errorf("adapters/github: %s: %w", what, res.ctxErr)
	}
	if res.err != nil {
		return fmt.Errorf("adapters/github: %s: %w (%s)", what, res.err, strings.TrimSpace(string(res.stderr)))
	}
	return nil
}

var _ core.UpstreamPullRequestMerger = (*Client)(nil)
