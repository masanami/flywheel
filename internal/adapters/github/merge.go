package github

import (
	"context"
	"fmt"
	"strings"

	"github.com/masanami/flywheel/internal/core"
)

// 承認済みの release の実行の口だけが呼ぶ、PR のマージの依頼（core.UpstreamPullRequestMerger）。依頼は GitHub の自動マージの
// 予約（--auto）で、マージは必須のチェック（CI）が通ったときに GitHub が行う。
// このパッケージの書き込みは、このマージの依頼と、PR が BEHIND のときの base の取り込みの依頼（UpdatePullRequestBranch）だけ。
// マージの依頼に渡す引数は PR の URL・--auto・方式のフラグ・--match-head-commit だけで、
// --admin・--delete-branch は付けない（ブランチの削除は GitHub のリポジトリ設定に任せる）。

var mergeFlags = map[string]string{
	core.MergeMethodSquash: "--squash",
	core.MergeMethodMerge:  "--merge",
	core.MergeMethodRebase: "--rebase",
}

// MergePullRequest は `gh pr merge <prURL> --auto --<方式> --match-head-commit <matchHeadSHA>` を 1 回起動する。
// --match-head-commit は予約の時点の head の一致を GitHub に比べさせる。予約の後に別の head が来てマージされたことは
// 防げないので、core が読み取りで検出する（承認した head と違うマージは merged_unapproved）。
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
	res := c.run(ctx, "pr", "merge", prURL, "--auto", flag, "--match-head-commit", matchHeadSHA)
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

// UpdatePullRequestBranch は core.UpstreamPullRequestBranchUpdater の実装。PR のブランチへ base の最新を取り込む
// 依頼（PUT pulls/<番号>/update-branch）を 1 回起動する。expected_head_sha で、取り込みの前の head の一致を
// GitHub 側でも比べさせる。repo・head が空のときは gh を起動せずエラーにする。
func (c *Client) UpdatePullRequestBranch(ctx context.Context, repo string, number int, expectedHeadSHA string) error {
	if repo == "" || number <= 0 || expectedHeadSHA == "" {
		return fmt.Errorf("adapters/github: update pull request branch: repo, number and head SHA are required")
	}
	what := fmt.Sprintf("update pull request branch %s#%d", repo, number)
	res := c.run(ctx, "api", "-X", "PUT", fmt.Sprintf("repos/%s/pulls/%d/update-branch", repo, number), "-f", "expected_head_sha="+expectedHeadSHA)
	return c.resultErr(what, res)
}

var _ core.UpstreamPullRequestBranchUpdater = (*Client)(nil)
