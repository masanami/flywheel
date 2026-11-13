package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/masanami/flywheel/internal/core"
)

// J5 の起動の前の PR のチェックの取得（core.UpstreamCheckSource）。呼ぶのは `gh api` の
// GET だけ（-X・--method・-f・-F を渡さない）。チェックが完了したかの判定は core が持ち、
// ここは取得と正規化だけを行う。

func getPullURL(repo string, number int) string {
	return fmt.Sprintf("repos/%s/pulls/%d", repo, number)
}

func listCheckRunsURL(repo, sha string, page int) string {
	q := url.Values{}
	q.Set("per_page", fmt.Sprintf("%d", perPage))
	q.Set("page", fmt.Sprintf("%d", page))
	return fmt.Sprintf("repos/%s/commits/%s/check-runs?%s", repo, url.PathEscape(sha), q.Encode())
}

func listCommitStatusesURL(repo, sha string, page int) string {
	q := url.Values{}
	q.Set("per_page", fmt.Sprintf("%d", perPage))
	q.Set("page", fmt.Sprintf("%d", page))
	return fmt.Sprintf("repos/%s/commits/%s/status?%s", repo, url.PathEscape(sha), q.Encode())
}

// ghGet は gh api の GET を 1 回行い、応答の本文を返す。失敗は what を添えたエラーにする。
func (c *Client) ghGet(ctx context.Context, what, endpoint string) ([]byte, error) {
	res := c.run(ctx, "api", endpoint)
	if err := c.resultErr(what, res); err != nil {
		return nil, err
	}
	return res.stdout, nil
}

// resultErr は gh の起動の結果の失敗を、what を添えたエラーにする（成功なら nil）。
func (c *Client) resultErr(what string, res runResult) error {
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

type rawPullHead struct {
	HTMLURL  string  `json:"html_url"`
	Title    string  `json:"title"`
	State    string  `json:"state"`
	MergedAt *string `json:"merged_at"`
	Base     struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"base"`
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Mergeable      *bool  `json:"mergeable"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	MergeableState string `json:"mergeable_state"`
}

type rawCheckRuns struct {
	CheckRuns []struct {
		Name       string  `json:"name"`
		Status     string  `json:"status"`
		Conclusion *string `json:"conclusion"`
	} `json:"check_runs"`
}

type rawCombinedStatus struct {
	TotalCount int `json:"total_count"`
	Statuses   []struct {
		Context string `json:"context"`
		State   string `json:"state"`
	} `json:"statuses"`
}

// GetPullRequestChecks は core.UpstreamCheckSource の実装。PR の状態（head のコミットを含む）を
// 取得し、そのコミットの check run と旧式のコミットステータスを全ページ集める。
func (c *Client) GetPullRequestChecks(ctx context.Context, repo string, number int) (core.UpstreamPullRequestChecks, error) {
	what := fmt.Sprintf("get pull request %s#%d", repo, number)
	body, err := c.ghGet(ctx, what, getPullURL(repo, number))
	if err != nil {
		return core.UpstreamPullRequestChecks{}, err
	}
	var pr rawPullHead
	if err := json.Unmarshal(body, &pr); err != nil {
		return core.UpstreamPullRequestChecks{}, fmt.Errorf("adapters/github: decode %s: %w", what, err)
	}
	if pr.HTMLURL == "" || pr.Base.Ref == "" || pr.Head.SHA == "" {
		return core.UpstreamPullRequestChecks{}, fmt.Errorf("adapters/github: %s: the pull request lacks html_url, base.ref or head.sha", what)
	}
	state := pr.State
	if pr.MergedAt != nil && *pr.MergedAt != "" {
		state = "merged"
	}
	if state != "open" && state != "closed" && state != "merged" {
		return core.UpstreamPullRequestChecks{}, fmt.Errorf("adapters/github: %s: unknown state %q", what, pr.State)
	}
	out := core.UpstreamPullRequestChecks{URL: pr.HTMLURL, Title: pr.Title, State: state, Base: pr.Base.Ref, BaseSHA: pr.Base.SHA, HeadSHA: pr.Head.SHA,
		Mergeable: pr.Mergeable, MergeCommitSHA: pr.MergeCommitSHA, MergeableState: strings.ToLower(pr.MergeableState)}

	checks, err := c.listCommitChecks(ctx, repo, pr.Head.SHA)
	if err != nil {
		return core.UpstreamPullRequestChecks{}, err
	}
	out.Checks = checks
	return out, nil
}

// GetCommitChecks は core.UpstreamCommitChecksSource の実装。sha のコミットの check run と旧式のコミットステータスを全ページ集める。
func (c *Client) GetCommitChecks(ctx context.Context, repo, sha string) ([]core.UpstreamCheck, error) {
	return c.listCommitChecks(ctx, repo, sha)
}

func (c *Client) listCommitChecks(ctx context.Context, repo, sha string) ([]core.UpstreamCheck, error) {
	var checks []core.UpstreamCheck
	for page := 1; ; page++ {
		what := fmt.Sprintf("list check runs %s@%s page %d", repo, sha, page)
		b, err := c.ghGet(ctx, what, listCheckRunsURL(repo, sha, page))
		if err != nil {
			return nil, err
		}
		var raw rawCheckRuns
		if err := json.Unmarshal(b, &raw); err != nil {
			return nil, fmt.Errorf("adapters/github: decode %s: %w", what, err)
		}
		for _, r := range raw.CheckRuns {
			if r.Status == "" {
				return nil, fmt.Errorf("adapters/github: %s: a check run lacks status", what)
			}
			chk := core.UpstreamCheck{Name: r.Name, Status: r.Status}
			if r.Conclusion != nil {
				chk.Conclusion = *r.Conclusion
			}
			checks = append(checks, chk)
		}
		if len(raw.CheckRuns) < perPage {
			break
		}
	}

	for page := 1; ; page++ {
		what := fmt.Sprintf("list commit statuses %s@%s page %d", repo, sha, page)
		b, err := c.ghGet(ctx, what, listCommitStatusesURL(repo, sha, page))
		if err != nil {
			return nil, err
		}
		var raw rawCombinedStatus
		if err := json.Unmarshal(b, &raw); err != nil {
			return nil, fmt.Errorf("adapters/github: decode %s: %w", what, err)
		}
		for _, s := range raw.Statuses {
			// 完了したかの判定は core が持つ。生の state をそのまま渡す。
			checks = append(checks, core.UpstreamCheck{Name: s.Context, Status: core.UpstreamCheckStatusLegacy, Conclusion: s.State})
		}
		if len(raw.Statuses) < perPage {
			break
		}
	}
	return checks, nil
}

var _ core.UpstreamCommitChecksSource = (*Client)(nil)

// Client が core.UpstreamCheckSource を実装していることのコンパイル時の検査。
var _ core.UpstreamCheckSource = (*Client)(nil)
