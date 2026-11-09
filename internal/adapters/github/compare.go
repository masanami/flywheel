package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/masanami/flywheel/internal/core"
)

// release の head の変化の判定（core.UpstreamCompareSource）に使う、2 つのコミットの比較の取得。
// 呼ぶのは `gh api` の GET だけ。取り込みかどうかの規則は core が持ち、ここは取得と正規化だけを行う。

// compareFilesPerPage は compare の 1 ページの件数（API の上限）。変更ファイルがこの件数に達したら、
// 打ち切られているかもしれないとして返す。
const compareFilesPerPage = 100

type rawCompare struct {
	Status       string `json:"status"`
	TotalCommits int    `json:"total_commits"`
	Commits      []struct {
		SHA     string `json:"sha"`
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	} `json:"commits"`
	Files []struct {
		Filename string `json:"filename"`
		Patch    string `json:"patch"`
	} `json:"files"`
}

// CompareCommits は core.UpstreamCompareSource の実装。base...head の 1 ページ目（100 件）を返す。
// コミットが 100 件を超えるときは TotalCommits が Commits の件数より大きくなり、変更ファイルが
// 100 件に達したときは FilesTruncated が真になる（打ち切りの扱いは core が決める）。
func (c *Client) CompareCommits(ctx context.Context, repo, base, head string) (core.UpstreamComparison, error) {
	what := fmt.Sprintf("compare %s %s...%s", repo, base, head)
	endpoint := fmt.Sprintf("repos/%s/compare/%s...%s?per_page=%d&page=1", repo, url.PathEscape(base), url.PathEscape(head), compareFilesPerPage)
	body, err := c.ghGet(ctx, what, endpoint)
	if err != nil {
		return core.UpstreamComparison{}, err
	}
	var raw rawCompare
	if err := json.Unmarshal(body, &raw); err != nil {
		return core.UpstreamComparison{}, fmt.Errorf("adapters/github: %s: decode: %w", what, err)
	}
	out := core.UpstreamComparison{Status: raw.Status, TotalCommits: raw.TotalCommits, FilesTruncated: len(raw.Files) >= compareFilesPerPage}
	for _, rc := range raw.Commits {
		uc := core.UpstreamCommit{SHA: rc.SHA, Message: rc.Commit.Message}
		for _, p := range rc.Parents {
			uc.Parents = append(uc.Parents, p.SHA)
		}
		out.Commits = append(out.Commits, uc)
	}
	for _, rf := range raw.Files {
		out.Files = append(out.Files, core.UpstreamFile{Filename: rf.Filename, Patch: rf.Patch})
	}
	return out, nil
}
