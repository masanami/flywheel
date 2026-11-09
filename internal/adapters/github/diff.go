package github

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/masanami/flywheel/internal/core"
)

// diffUnavailableMarkers は、上流がこの差分を返さないときの応答を gh が標準エラーへ書く形。
// 大きすぎる差分は 422（実測）か 406、コミットが見つからないときは 404 で返り、やり直しても変わらない。
var diffUnavailableMarkers = []string{"HTTP 404", "HTTP 406", "HTTP 422"}

func compareURL(repo, baseSHA, headSHA string) string {
	return fmt.Sprintf("repos/%s/compare/%s...%s", repo, url.PathEscape(baseSHA), url.PathEscape(headSHA))
}

// GetCompareDiff は core.UpstreamDiffSource の実装（`gh pr diff` は PR のその時の head の差分を返し、
// 固定した SHA を指せないので、compare の API を使う）。
func (c *Client) GetCompareDiff(ctx context.Context, repo, baseSHA, headSHA string) (string, error) {
	what := fmt.Sprintf("compare %s %s...%s", repo, baseSHA, headSHA)
	res := c.run(ctx, "api", "-H", "Accept: application/vnd.github.diff", compareURL(repo, baseSHA, headSHA))
	if res.err != nil && !res.timedOut && res.ctxErr == nil {
		for _, m := range diffUnavailableMarkers {
			if strings.Contains(string(res.stderr), m) {
				return "", fmt.Errorf("adapters/github: %s: %w (%s)", what, core.ErrUpstreamDiffUnavailable, strings.TrimSpace(string(res.stderr)))
			}
		}
	}
	if err := c.resultErr(what, res); err != nil {
		return "", err
	}
	return string(res.stdout), nil
}
