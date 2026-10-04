package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/masanami/flywheel/internal/core"
)

// 委譲の後の照合（core.UpstreamBranchSource）の取得。呼ぶのは `gh api` の GET
// だけ（-X・--method・-f・-F を渡さない）。取り込みの規則は持たない。

// getBranchURL は repo の branch を 1 件取得する gh api の引数。
func getBranchURL(repo, branch string) string {
	segs := strings.Split(branch, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return fmt.Sprintf("repos/%s/branches/%s", repo, strings.Join(segs, "/"))
}

// listPullsByHeadURL は repo の、head が <owner>:<branch> の PR（state=all）の
// page ページ目を取得する gh api の引数。
func listPullsByHeadURL(repo, branch string, page int) string {
	owner := repo
	if i := strings.Index(repo, "/"); i >= 0 {
		owner = repo[:i]
	}
	q := url.Values{}
	q.Set("head", owner+":"+branch)
	q.Set("state", "all")
	q.Set("per_page", fmt.Sprintf("%d", perPage))
	q.Set("page", fmt.Sprintf("%d", page))
	return fmt.Sprintf("repos/%s/pulls?%s", repo, q.Encode())
}

// BranchExists は core.UpstreamBranchSource の実装。HTTP 404 だけを「無い」とし、
// それ以外の失敗はエラーで返す。
func (c *Client) BranchExists(ctx context.Context, repo, branch string) (bool, error) {
	res := c.run(ctx, "api", "-i", getBranchURL(repo, branch))
	if res.timedOut {
		return false, fmt.Errorf("adapters/github: get branch %s %q timed out after %s: %w", repo, branch, c.timeout, context.DeadlineExceeded)
	}
	if res.ctxErr != nil {
		return false, fmt.Errorf("adapters/github: get branch %s %q: %w", repo, branch, res.ctxErr)
	}
	status, _, parseErr := splitHTTPResponse(res.stdout)
	if parseErr != nil {
		if res.err != nil {
			return false, fmt.Errorf("adapters/github: get branch %s %q: %w (%s)", repo, branch, res.err, strings.TrimSpace(string(res.stderr)))
		}
		return false, fmt.Errorf("adapters/github: get branch %s %q: %w", repo, branch, parseErr)
	}
	switch {
	case status == 404:
		return false, nil
	case status >= 200 && status < 300:
		return true, nil
	}
	return false, fmt.Errorf("adapters/github: get branch %s %q: unexpected http status %d", repo, branch, status)
}

type rawPull struct {
	HTMLURL  string  `json:"html_url"`
	Title    string  `json:"title"`
	State    string  `json:"state"`
	MergedAt *string `json:"merged_at"`
	Base     struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// ListPullRequestsByHead は core.UpstreamBranchSource の実装。全ページを自前で送る
// （ListOpenIssues と同じ流儀）。
func (c *Client) ListPullRequestsByHead(ctx context.Context, repo, branch string) ([]core.UpstreamPullRequest, error) {
	var out []core.UpstreamPullRequest
	for page := 1; ; page++ {
		res := c.run(ctx, "api", listPullsByHeadURL(repo, branch, page))
		if res.timedOut {
			return nil, fmt.Errorf("adapters/github: list pull requests %s %q page %d timed out after %s: %w", repo, branch, page, c.timeout, context.DeadlineExceeded)
		}
		if res.ctxErr != nil {
			return nil, fmt.Errorf("adapters/github: list pull requests %s %q page %d: %w", repo, branch, page, res.ctxErr)
		}
		if res.err != nil {
			return nil, fmt.Errorf("adapters/github: list pull requests %s %q page %d: %w (%s)", repo, branch, page, res.err, strings.TrimSpace(string(res.stderr)))
		}
		var raw []rawPull
		if err := json.Unmarshal(res.stdout, &raw); err != nil {
			return nil, fmt.Errorf("adapters/github: decode pull requests %s %q page %d: %w", repo, branch, page, err)
		}
		for _, p := range raw {
			if p.HTMLURL == "" || p.Base.Ref == "" {
				return nil, fmt.Errorf("adapters/github: list pull requests %s %q page %d: a pull request lacks html_url or base.ref", repo, branch, page)
			}
			state := p.State
			if p.MergedAt != nil && *p.MergedAt != "" {
				state = "merged"
			}
			if state != "open" && state != "closed" && state != "merged" {
				return nil, fmt.Errorf("adapters/github: list pull requests %s %q page %d: unknown state %q", repo, branch, page, p.State)
			}
			out = append(out, core.UpstreamPullRequest{URL: p.HTMLURL, Title: p.Title, State: state, Base: p.Base.Ref})
		}
		if len(raw) < perPage {
			break
		}
	}
	return out, nil
}

// Client が core.UpstreamBranchSource を実装していることのコンパイル時の検査。
var _ core.UpstreamBranchSource = (*Client)(nil)
