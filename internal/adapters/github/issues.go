package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/masanami/flywheel/internal/core"
)

// perPage は一覧取得の 1 ページあたりの件数（固定 100。完了条件「全ページを
// 取得する（件数の上限で打ち切らない）」に合わせ、ページ送りの単位だけを
// 固定する）。
const perPage = 100

// listIssuesURL は repo の open な Issue の page ページ目を取得する gh api の
// 引数（GET 専用。state・per_page・page 以外のクエリを持たない）。
func listIssuesURL(repo string, page int) string {
	return fmt.Sprintf("repos/%s/issues?state=open&per_page=%d&page=%d", repo, perPage, page)
}

// getIssueURL は repo の number 番の Issue を 1 件取得する gh api の引数。
func getIssueURL(repo string, number int) string {
	return fmt.Sprintf("repos/%s/issues/%d", repo, number)
}

// ListOpenIssues は core.UpstreamIssueSource の実装。全ページを自前で
// 送り、そのページの生の要素数が 100 未満（空を含む）になった時点で終了する
// （gh の --paginate に頼らない。件数の上限では打ち切らない）。
// pull_request キーを持つ要素は、ページ終了判定（生の要素数）の後で除く
// （ページ境界判定は除外前の件数で行う）。
func (c *Client) ListOpenIssues(ctx context.Context, repo string) ([]core.UpstreamIssue, error) {
	var issues []core.UpstreamIssue

	for page := 1; ; page++ {
		res := c.run(ctx, "api", listIssuesURL(repo, page))
		if res.timedOut {
			return nil, fmt.Errorf("adapters/github: list open issues %s page %d timed out after %s: %w", repo, page, c.timeout, context.DeadlineExceeded)
		}
		if res.ctxErr != nil {
			return nil, fmt.Errorf("adapters/github: list open issues %s page %d: %w", repo, page, res.ctxErr)
		}
		if res.err != nil {
			return nil, fmt.Errorf("adapters/github: list open issues %s page %d: %w (%s)", repo, page, res.err, strings.TrimSpace(string(res.stderr)))
		}

		var raw []json.RawMessage
		if err := json.Unmarshal(res.stdout, &raw); err != nil {
			return nil, fmt.Errorf("adapters/github: decode issue list %s page %d: %w", repo, page, err)
		}

		for _, r := range raw {
			isPR, err := isPullRequestElement(r)
			if err != nil {
				return nil, fmt.Errorf("adapters/github: list open issues %s page %d: %w", repo, page, err)
			}
			if isPR {
				continue
			}
			issue, err := normalizeIssue(r)
			if err != nil {
				return nil, fmt.Errorf("adapters/github: list open issues %s page %d: %w", repo, page, err)
			}
			if !strings.EqualFold(issue.Repo, repo) {
				return nil, fmt.Errorf("adapters/github: list open issues %s page %d: %w", repo, page, repoMismatchError(repo, issue.Repo))
			}
			issues = append(issues, issue)
		}

		if len(raw) < perPage {
			break
		}
	}

	return issues, nil
}

// GetIssue は core.UpstreamIssueSource の実装。gh api -i のステータス行
// （HTTP/1.1・HTTP/2・HTTP/2.0 のいずれの表記でも）から 404・410 を判定し、
// core.ErrUpstreamIssueNotFound を返す。エラーの文言・stderr の部分一致では
// 判定しない。
func (c *Client) GetIssue(ctx context.Context, repo string, number int) (core.UpstreamIssue, error) {
	res := c.run(ctx, "api", "-i", getIssueURL(repo, number))
	if res.timedOut {
		return core.UpstreamIssue{}, fmt.Errorf("adapters/github: get issue %s#%d timed out after %s: %w", repo, number, c.timeout, context.DeadlineExceeded)
	}
	if res.ctxErr != nil {
		return core.UpstreamIssue{}, fmt.Errorf("adapters/github: get issue %s#%d: %w", repo, number, res.ctxErr)
	}

	status, body, parseErr := splitHTTPResponse(res.stdout)
	if parseErr != nil {
		if res.err != nil {
			return core.UpstreamIssue{}, fmt.Errorf("adapters/github: get issue %s#%d: %w (%s)", repo, number, res.err, strings.TrimSpace(string(res.stderr)))
		}
		return core.UpstreamIssue{}, fmt.Errorf("adapters/github: get issue %s#%d: %w", repo, number, parseErr)
	}

	if status == 404 || status == 410 {
		return core.UpstreamIssue{}, fmt.Errorf("adapters/github: get issue %s#%d: http %d: %w", repo, number, status, core.ErrUpstreamIssueNotFound)
	}
	if status < 200 || status >= 300 {
		return core.UpstreamIssue{}, fmt.Errorf("adapters/github: get issue %s#%d: unexpected http status %d", repo, number, status)
	}

	issue, err := normalizeIssue(body)
	if err != nil {
		return core.UpstreamIssue{}, fmt.Errorf("adapters/github: get issue %s#%d: %w", repo, number, err)
	}
	if !strings.EqualFold(issue.Repo, repo) {
		return core.UpstreamIssue{}, fmt.Errorf("adapters/github: get issue %s#%d: %w", repo, number, repoMismatchError(repo, issue.Repo))
	}
	if issue.Number != number {
		return core.UpstreamIssue{}, fmt.Errorf("adapters/github: get issue %s#%d: response is issue number %d", repo, number, issue.Number)
	}
	return issue, nil
}

// repoMismatchError は、応答の Issue の repo が要求した repo と（大文字小文字を
// 無視しても）一致しないことを表す失敗を作る。gh api は GET のリダイレクト
// （リポジトリの改名・Issue の移管で GitHub が返す 301）を辿るため、要求と
// 別の repo の Issue が 200 で返りうる。それを別の外部キーとしてそのまま
// 返すと、同じ Issue の課題が二重に作られうる（internal/core/upstream.go の
// ExternalKey の契約が防ごうとしている事態）。見つからない（404・410）とも
// 断定できないため、ErrUpstreamIssueNotFound ではない「それ以外の失敗」に
// する（呼び出し側は状態を変えない）。
func repoMismatchError(requested, got string) error {
	return fmt.Errorf("response belongs to repository %q, not the requested %q (renamed or transferred?)", got, requested)
}

// CurrentLogin は core.UpstreamIssueSource の実装。gh api user の .login を
// 返す。空なら失敗として扱う。
func (c *Client) CurrentLogin(ctx context.Context) (string, error) {
	res := c.run(ctx, "api", "user")
	if res.timedOut {
		return "", fmt.Errorf("adapters/github: current login timed out after %s: %w", c.timeout, context.DeadlineExceeded)
	}
	if res.ctxErr != nil {
		return "", fmt.Errorf("adapters/github: current login: %w", res.ctxErr)
	}
	if res.err != nil {
		return "", fmt.Errorf("adapters/github: current login: %w (%s)", res.err, strings.TrimSpace(string(res.stderr)))
	}

	var payload struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(res.stdout, &payload); err != nil {
		return "", fmt.Errorf("adapters/github: decode current login: %w", err)
	}
	if payload.Login == "" {
		return "", fmt.Errorf("adapters/github: current login: empty login in response")
	}
	return payload.Login, nil
}

// Client が core.UpstreamIssueSource を実装していることのコンパイル時の
// 検査（AC-105 とは別に、adapter が core の IF を実際に満たすことを保証する）。
var _ core.UpstreamIssueSource = (*Client)(nil)
