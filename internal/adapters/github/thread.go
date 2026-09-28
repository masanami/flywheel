package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/masanami/flywheel/internal/core"
)

// listCommentsURL は repo の number 番の Issue のコメントの page ページ目を
// 取得する gh api の引数（GET 専用。ListOpenIssues と同じくページ送りの単位
// だけを固定する）。
func listCommentsURL(repo string, number, page int) string {
	return fmt.Sprintf("repos/%s/issues/%d/comments?per_page=%d&page=%d", repo, number, perPage, page)
}

// listAllComments は repo の number 番の Issue の全コメントを、API の順
// （＝作成順）のまま返す。ListOpenIssues と同じ流儀で自前のページ送りを行い、
// そのページの要素数が perPage 未満（空を含む）になった時点で終了する。
func (c *Client) listAllComments(ctx context.Context, repo string, number int) ([]core.UpstreamComment, error) {
	var comments []core.UpstreamComment

	for page := 1; ; page++ {
		res := c.run(ctx, "api", listCommentsURL(repo, number, page))
		if res.timedOut {
			return nil, fmt.Errorf("adapters/github: list comments %s#%d page %d timed out after %s: %w", repo, number, page, c.timeout, context.DeadlineExceeded)
		}
		if res.ctxErr != nil {
			return nil, fmt.Errorf("adapters/github: list comments %s#%d page %d: %w", repo, number, page, res.ctxErr)
		}
		if res.err != nil {
			return nil, fmt.Errorf("adapters/github: list comments %s#%d page %d: %w (%s)", repo, number, page, res.err, strings.TrimSpace(string(res.stderr)))
		}

		var raw []json.RawMessage
		if err := json.Unmarshal(res.stdout, &raw); err != nil {
			return nil, fmt.Errorf("adapters/github: decode comments %s#%d page %d: %w", repo, number, page, err)
		}

		for _, r := range raw {
			comment, err := normalizeComment(r)
			if err != nil {
				return nil, fmt.Errorf("adapters/github: decode comments %s#%d page %d: %w", repo, number, page, err)
			}
			comments = append(comments, comment)
		}

		if len(raw) < perPage {
			break
		}
	}

	return comments, nil
}

// GetIssueThread は core.UpstreamThreadSource の実装。本文は既存 GetIssue の
// 経路を再利用し（404・410・移管の判定を GetIssue と共有する）、コメントは
// listAllComments で自前にページ送りして取得する。
func (c *Client) GetIssueThread(ctx context.Context, repo string, number int) (core.UpstreamIssueThread, error) {
	issue, err := c.GetIssue(ctx, repo, number)
	if err != nil {
		return core.UpstreamIssueThread{}, err
	}

	comments, err := c.listAllComments(ctx, repo, number)
	if err != nil {
		return core.UpstreamIssueThread{}, err
	}

	return core.UpstreamIssueThread{Issue: issue, Comments: comments}, nil
}

// GetReferencedIssue は core.UpstreamThreadSource の実装。参照先 Issue の本文
// だけを返す（コメントは取得しない＝AC-90）。GetIssue と fetchIssueRaw
// （404・410・移管の sentinel の判定）を共有するが、単純なエイリアスにはしない
// （self-review 指摘: 応答が Pull Request（pull_request キーを持つ）だった
// 場合、core の語彙に PR が無い＝§機能全体の設計 という前提を、URL 形式の
// 参照が明示的に "/pull/" を対象外にしているのと揃える必要がある。書き方に
// よって PR の扱いが食い違わないよう、"#<番号>" が PR を指していた場合も
// 「Issue としては見つからない」＝ErrUpstreamIssueNotFound として扱い、
// FetchUpstreamContext のスキップ経路に乗せる）。
func (c *Client) GetReferencedIssue(ctx context.Context, repo string, number int) (core.UpstreamIssue, error) {
	body, err := c.fetchIssueRaw(ctx, repo, number, "get referenced issue")
	if err != nil {
		return core.UpstreamIssue{}, err
	}

	isPR, err := isPullRequestElement(body)
	if err != nil {
		return core.UpstreamIssue{}, fmt.Errorf("adapters/github: get referenced issue %s#%d: %w", repo, number, err)
	}
	if isPR {
		return core.UpstreamIssue{}, fmt.Errorf("adapters/github: get referenced issue %s#%d: response is a pull request: %w", repo, number, core.ErrUpstreamIssueNotFound)
	}

	issue, err := normalizeIssue(body)
	if err != nil {
		return core.UpstreamIssue{}, fmt.Errorf("adapters/github: get referenced issue %s#%d: %w", repo, number, err)
	}
	if !strings.EqualFold(issue.Repo, repo) {
		return core.UpstreamIssue{}, fmt.Errorf("adapters/github: get referenced issue %s#%d: %w: %w", repo, number, repoMismatchError(repo, issue.Repo), core.ErrUpstreamIssueTransferred)
	}
	if issue.Number != number {
		return core.UpstreamIssue{}, fmt.Errorf("adapters/github: get referenced issue %s#%d: response is issue number %d", repo, number, issue.Number)
	}
	return issue, nil
}

// Client が core.UpstreamThreadSource を実装していることのコンパイル時の検査。
var _ core.UpstreamThreadSource = (*Client)(nil)
