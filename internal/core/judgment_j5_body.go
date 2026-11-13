package core

// このファイルは、J5 の入力（データの区画）に入れる成果物の説明文の整形を持つ。説明文は子が書いた自己申告で、
// 取得に失敗しても J5 は止めず、失敗したことを区画に書く。

import (
	"context"
	"fmt"
	"strings"
)

// maxArtifactDescriptionRunes は成果物 1 件の説明文の上限（文字数）。超えた分は切り、区画に省略したと書く。
const maxArtifactDescriptionRunes = 20000

// labelArtifactDescription は成果物の説明文の区画のラベル（指示文はこの名前で区画を指す）。
const labelArtifactDescription = "成果物の説明文"

// UpstreamArtifactDescription は成果物の説明文と、その説明文を取得した時点の head の SHA（1 回の取得でまとめて得た値）。
type UpstreamArtifactDescription struct {
	Text    string
	HeadSHA string
}

// UpstreamDescriptionSource は J5 の起動の前に core が呼ぶ、成果物の説明文の取得 IF（読み取りだけ）。
// 実装は接続先の adapter。
type UpstreamDescriptionSource interface {
	// GetArtifactDescription は repo（"<owner>/<name>"）の number 番の成果物の説明文と、そのときの head の SHA を返す。
	GetArtifactDescription(ctx context.Context, repo string, number int) (UpstreamArtifactDescription, error)
}

// truncateRunes は s の先頭 n 文字以内を返す。切ったときは cut が true。
func truncateRunes(s string, n int) (out string, cut bool) {
	r := []rune(s)
	if len(r) <= n {
		return s, false
	}
	return string(r[:n]), true
}

// fetchArtifactDescriptions は targets の説明文の区画の本文を返す。1 件の取得の失敗は、その成果物のブロックに
// 理由を書いて続ける（エラーにしない）。
func fetchArtifactDescriptions(ctx context.Context, src UpstreamDescriptionSource, targets []UpstreamPullRequestChecks) string {
	var blocks []string
	for _, a := range targets {
		repo, number, ok := parsePullRequestURL(a.URL)
		if !ok {
			blocks = append(blocks, fmt.Sprintf("成果物: %s\n説明文の取得に失敗した（URL の形が不正）。", a.URL))
			continue
		}
		got, err := src.GetArtifactDescription(ctx, repo, number)
		if err != nil {
			blocks = append(blocks, fmt.Sprintf("成果物: %s\n説明文の取得に失敗した（%s）。説明文は読めていない前提で判断する。", a.URL, oneLine(err.Error())))
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "成果物: %s\nhead %s の時点で取得\n", a.URL, got.HeadSHA)
		if got.HeadSHA != a.HeadSHA {
			fmt.Fprintf(&b, "注記: 説明文は別の head（%s）のときに読んだもの（検証する head は %s）。\n", got.HeadSHA, a.HeadSHA)
		}
		text, cut := truncateRunes(got.Text, maxArtifactDescriptionRunes)
		if strings.TrimSpace(text) == "" {
			text = "（説明文なし）"
		}
		b.WriteString("\n" + neutralizeDelimiterLines(text))
		if cut {
			fmt.Fprintf(&b, "\n\n（説明文は %d 文字あり、先頭の %d 文字までを入れて残りを省略した。）", len([]rune(got.Text)), maxArtifactDescriptionRunes)
		}
		blocks = append(blocks, b.String())
	}
	return strings.Join(blocks, upstreamBlockSeparator)
}

// neutralizeDelimiterLines は、データの区画の区切り（行頭の "-----"）に見える行へ空白を前置し、子が書いた説明文が
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
