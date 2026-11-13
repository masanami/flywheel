package core

// このファイルは、J5 の入力（データの区画）に入れる PR の本文の整形を持つ。本文は子が書いた自己申告で、
// 取得に失敗しても J5 は止めず、失敗したことを区画に書く。

import (
	"context"
	"fmt"
	"strings"
)

// maxPullRequestBodyRunes は PR 1 件の本文の上限（文字数）。超えた分は切り、区画に省略したと書く。
const maxPullRequestBodyRunes = 20000

// labelPullRequestBody は PR の本文の区画のラベル（指示文はこの名前で区画を指す）。
const labelPullRequestBody = "PR の本文"

// UpstreamPullRequestBody は PR の本文と、その本文を取得した時点の head の SHA（1 回の取得でまとめて得た値）。
type UpstreamPullRequestBody struct {
	Body    string
	HeadSHA string
}

// UpstreamBodySource は J5 の起動の前に core が呼ぶ、PR の本文の取得 IF（読み取りだけ）。実装は
// internal/adapters/github。
type UpstreamBodySource interface {
	// GetPullRequestBody は repo（"<owner>/<name>"）の number 番の PR の本文と、そのときの head の SHA を返す。
	GetPullRequestBody(ctx context.Context, repo string, number int) (UpstreamPullRequestBody, error)
}

// truncateRunes は s の先頭 n 文字以内を返す。切ったときは cut が true。
func truncateRunes(s string, n int) (out string, cut bool) {
	r := []rune(s)
	if len(r) <= n {
		return s, false
	}
	return string(r[:n]), true
}

// fetchPullRequestBodies は targets の本文の区画の本文を返す。1 件の取得の失敗は、その PR のブロックに
// 理由を書いて続ける（エラーにしない）。
func fetchPullRequestBodies(ctx context.Context, src UpstreamBodySource, targets []UpstreamPullRequestChecks) string {
	var blocks []string
	for _, pr := range targets {
		repo, number, ok := parsePullRequestURL(pr.URL)
		if !ok {
			blocks = append(blocks, fmt.Sprintf("PR: %s\n本文の取得に失敗した（URL の形が不正）。", pr.URL))
			continue
		}
		got, err := src.GetPullRequestBody(ctx, repo, number)
		if err != nil {
			blocks = append(blocks, fmt.Sprintf("PR: %s\n本文の取得に失敗した（%s）。本文は読めていない前提で判断する。", pr.URL, oneLine(err.Error())))
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "PR: %s\nhead %s の時点で取得\n", pr.URL, got.HeadSHA)
		if got.HeadSHA != pr.HeadSHA {
			fmt.Fprintf(&b, "注記: 本文は別の head（%s）のときに読んだもの（検証する head は %s）。\n", got.HeadSHA, pr.HeadSHA)
		}
		body, cut := truncateRunes(got.Body, maxPullRequestBodyRunes)
		if strings.TrimSpace(body) == "" {
			body = "（本文なし）"
		}
		b.WriteString("\n" + neutralizeDelimiterLines(body))
		if cut {
			fmt.Fprintf(&b, "\n\n（本文は %d 文字あり、先頭の %d 文字までを入れて残りを省略した。）", len([]rune(got.Body)), maxPullRequestBodyRunes)
		}
		blocks = append(blocks, b.String())
	}
	return strings.Join(blocks, upstreamBlockSeparator)
}

// neutralizeDelimiterLines は、データの区画の区切り（行頭の "-----"）に見える行へ空白を前置し、子が書いた本文が
// 区画の境界を偽装できないようにする。
func neutralizeDelimiterLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "-----") {
			lines[i] = " " + l
		}
	}
	return strings.Join(lines, "\n")
}
