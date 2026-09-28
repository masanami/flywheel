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

// fetchIssueRaw は GetIssue・GetReferencedIssue（#80）が共有する「gh api -i で
// 1 件取得し、ステータス行から 404・410・その他の失敗を判定する」処理
// （self-review 指摘: GetReferencedIssue を GetIssue の単純なエイリアスに
// せず、Pull Request の除外という異なる後処理を持たせるための共通部分の
// 切り出し）。label はエラー文言の呼び出し元識別（"get issue" 等）に使う。
// 成功時は応答本文（正規化前の json.RawMessage）を返す。
func (c *Client) fetchIssueRaw(ctx context.Context, repo string, number int, label string) (json.RawMessage, error) {
	res := c.run(ctx, "api", "-i", getIssueURL(repo, number))
	if res.timedOut {
		return nil, fmt.Errorf("adapters/github: %s %s#%d timed out after %s: %w", label, repo, number, c.timeout, context.DeadlineExceeded)
	}
	if res.ctxErr != nil {
		return nil, fmt.Errorf("adapters/github: %s %s#%d: %w", label, repo, number, res.ctxErr)
	}

	status, body, parseErr := splitHTTPResponse(res.stdout)
	if parseErr != nil {
		if res.err != nil {
			return nil, fmt.Errorf("adapters/github: %s %s#%d: %w (%s)", label, repo, number, res.err, strings.TrimSpace(string(res.stderr)))
		}
		return nil, fmt.Errorf("adapters/github: %s %s#%d: %w", label, repo, number, parseErr)
	}

	if status == 404 || status == 410 {
		return nil, fmt.Errorf("adapters/github: %s %s#%d: http %d: %w", label, repo, number, status, core.ErrUpstreamIssueNotFound)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("adapters/github: %s %s#%d: unexpected http status %d", label, repo, number, status)
	}
	return body, nil
}

// GetIssue は core.UpstreamIssueSource の実装。gh api -i のステータス行
// （HTTP/1.1・HTTP/2・HTTP/2.0 のいずれの表記でも）から 404・410 を判定し、
// core.ErrUpstreamIssueNotFound を返す。エラーの文言・stderr の部分一致では
// 判定しない。応答が改名・移管の転送を辿って要求と別の repo の Issue を
// 返した場合は core.ErrUpstreamIssueTransferred を返す（#69。同じ repo で
// 番号だけ違う場合はこの sentinel を返さない）。
func (c *Client) GetIssue(ctx context.Context, repo string, number int) (core.UpstreamIssue, error) {
	body, err := c.fetchIssueRaw(ctx, repo, number, "get issue")
	if err != nil {
		return core.UpstreamIssue{}, err
	}

	issue, err := normalizeIssue(body)
	if err != nil {
		return core.UpstreamIssue{}, fmt.Errorf("adapters/github: get issue %s#%d: %w", repo, number, err)
	}
	if !strings.EqualFold(issue.Repo, repo) {
		// #69: 別リポジトリの応答は core.ErrUpstreamIssueTransferred で core 側に
		// 伝える（404・410 とは別の sentinel。confirmOpenListAbsence が両方を
		// missing に写す）。同じ repo で番号だけ違う場合（下の issue.Number の
		// 分岐）はこの sentinel を返さない（移管ではないため）。
		return core.UpstreamIssue{}, fmt.Errorf("adapters/github: get issue %s#%d: %w: %w", repo, number, repoMismatchError(repo, issue.Repo), core.ErrUpstreamIssueTransferred)
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
// ExternalKey の契約が防ごうとしている事態）。
//
// GetIssue はこのメッセージに core.ErrUpstreamIssueTransferred を重ねて
// 返し、core 側が 404・410（ErrUpstreamIssueNotFound）と区別しつつ
// upstream_state を missing に写せるようにする（#69）。ListOpenIssues は
// 一覧の 1 要素が別 repo だったときにこのメッセージだけを使い、一覧全体を
// 失敗として返す（sentinel は付けない。一覧の部分成功は core の関心事では
// なく、この経路は #69 のスコープ外＝従来どおり sentinel の無い失敗のまま）。
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
