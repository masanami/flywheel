package github

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/masanami/flywheel/internal/core"
)

type rawPullBody struct {
	Body *string `json:"body"`
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

// GetArtifactDescription は core.UpstreamDescriptionSource の実装（成果物の説明文＝PR の本文）。PR の取得（`gh api` の GET 1 回）の応答から、
// 本文とそのときの head の SHA をまとめて読む。読み取りだけで、書き込みはしない。
func (c *Client) GetArtifactDescription(ctx context.Context, repo string, number int) (core.UpstreamArtifactDescription, error) {
	what := fmt.Sprintf("get pull request body %s#%d", repo, number)
	body, err := c.ghGet(ctx, what, getPullURL(repo, number))
	if err != nil {
		return core.UpstreamArtifactDescription{}, err
	}
	var pr rawPullBody
	if err := json.Unmarshal(body, &pr); err != nil {
		return core.UpstreamArtifactDescription{}, fmt.Errorf("adapters/github: decode %s: %w", what, err)
	}
	if pr.Head.SHA == "" {
		return core.UpstreamArtifactDescription{}, fmt.Errorf("adapters/github: %s: the pull request lacks head.sha", what)
	}
	out := core.UpstreamArtifactDescription{HeadSHA: pr.Head.SHA}
	if pr.Body != nil {
		out.Text = *pr.Body
	}
	return out, nil
}
