package core

import (
	"context"
	"database/sql"
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

// ParseReleasePullRequestRef は release の ref が GitHub の PR の URL のときだけ repo（<owner>/<repo>）と
// 番号を返す（末尾の /files 等の続きは無視）。外す理由の判定（not_a_pr_url）に CLI が使う。
func ParseReleasePullRequestRef(ref string) (repo string, number int, ok bool) {
	return parseReleasePullRequestRef(ref)
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

// latestVerifiedHead は ref が指す PR について、J5 が検証の入力にした最新の head を返す。
// 成果物の行のうち (repo, 番号) が ref と一致し verified_head_sha が NULL でない行で、
// run_artifact.id が最大のものの値。ref が PR の URL でない・該当する行が無いときは nil
// （検証の head の記録なし。照合せず承認できる）。
func latestVerifiedHead(ctx context.Context, tx *sql.Tx, ref *string) (*string, error) {
	if ref == nil {
		return nil, nil
	}
	repo, number, ok := parseReleasePullRequestRef(*ref)
	if !ok {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT ref, verified_head_sha FROM run_artifact WHERE verified_head_sha IS NOT NULL ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var aref, head string
		if err := rows.Scan(&aref, &head); err != nil {
			return nil, err
		}
		arepo, anum, ok := parsePullRequestURL(aref)
		if ok && anum == number && strings.EqualFold(arepo, repo) {
			return &head, nil
		}
	}
	return nil, rows.Err()
}
