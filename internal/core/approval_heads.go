package core

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

// ReleaseHead は完了の承認の要約に載せる release 1 件と、その ref が指す PR の head の
// コミット。HeadSHA が nil なのは、ref が PR の URL でない・取得に失敗した場合
// （承認を止めない）。
type ReleaseHead struct {
	Operation IrreversibleOperation
	HeadSHA   *string
}

// parseReleasePullRequestRef は ref が https://github.com/<owner>/<repo>/pull/<番号> の形
// （末尾の /files 等の続きは無視）のときだけ repo と番号を返す。
func parseReleasePullRequestRef(ref string) (repo string, number int, ok bool) {
	u, err := url.Parse(ref)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") {
		return "", 0, false
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) < 4 || segs[2] != "pull" || segs[0] == "" || segs[1] == "" {
		return "", 0, false
	}
	n, err := strconv.Atoi(segs[3])
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return segs[0] + "/" + segs[1], n, true
}

// ResolveReleaseHeads は releases（ApprovalPreview.PendingReleases）の ref が指す PR の
// head を src から取り、releases と同じ順序・件数で返す。ストアに触れない（呼び出し側は
// ストアの読み取りの借りを返してから呼ぶ）。取得できない release は HeadSHA が nil。
func ResolveReleaseHeads(ctx context.Context, releases []IrreversibleOperation, src UpstreamCheckSource) []ReleaseHead {
	out := make([]ReleaseHead, 0, len(releases))
	for _, op := range releases {
		rh := ReleaseHead{Operation: op}
		if src != nil && op.Ref != nil {
			if repo, n, ok := parseReleasePullRequestRef(*op.Ref); ok {
				if pr, err := src.GetPullRequestChecks(ctx, repo, n); err == nil && pr.HeadSHA != "" {
					sha := pr.HeadSHA
					rh.HeadSHA = &sha
				}
			}
		}
		out = append(out, rh)
	}
	return out
}
