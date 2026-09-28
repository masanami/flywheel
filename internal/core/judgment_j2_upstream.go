package core

// このファイルは #85（親要件チケット #77・docs/features/m3-invoker-delegation.md
// §J2 計画・決定 M3P14）が持つ、J2 の標準入力へ組み込む「上流の最新の状態」
// （課題自身の Issue の本文と全コメント・参照先の Issue の本文）の組み立てと、
// 合計 200,000 バイトへの切り詰めを持つ。取得そのもの（FetchUpstreamContext）は
// #80 の範囲で、ここは取得済みの UpstreamContext を区画へ整形するだけである。

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// maxUpstreamInputBytes は J2 の入力に含める上流の本文・コメント・参照先の
// 本文の合計の上限（バイト。§J2「上流の入力の合計が 200,000 バイトを超える
// ときは…」）。各ブロックの見出しの行と、同じ区画の中のブロックを区切る空行を
// 含めて数え、この値ちょうどは超えていないものとして扱う。
const maxUpstreamInputBytes = 200_000

// 上流の入力の区画のラベル（指示文はこの名前で区画を指す）。
const (
	labelUpstreamIssue     = "上流の Issue"
	labelUpstreamComments  = "上流のコメント"
	labelUpstreamRefs      = "参照先の Issue"
	labelUpstreamNotice    = "上流の入力についての注記"
	upstreamNoticeCapLabel = "200000 バイト"
)

// j2UpstreamInput は buildUpstreamInput の結果。
type j2UpstreamInput struct {
	// Sections は J2 の標準入力へ足すデータの区画（本文・コメント・参照先・
	// 落としたことの注記）。
	Sections []JudgmentDataSection
	// CommentsTotal は取得したコメントの件数、CommentsDropped はそのうち上限の
	// ために落とした（古い方の）件数。
	CommentsTotal   int
	CommentsDropped int
	// ReferencesDropped は上限のために落とした参照先の Issue の件数。
	ReferencesDropped int
	// BodyTruncated は、課題自身の本文だけで上限を超え、本文を途中で切った
	// ことを表す（仕様に定めが無い端の扱い。読んだ記録は付けない側に倒す）。
	BodyTruncated bool
	// ObservedCommentsCount・ObservedUpdatedAt は、課題自身の Issue の取得で得た
	// 観測値（コメント数・更新日時。J2 が読んだ記録に使う値。UpstreamThreadSource
	// の契約により、コメント一覧の取得より前に得た値）。
	ObservedCommentsCount int
	ObservedUpdatedAt     string
}

// fullyRead は、最新のコメントをすべて入力へ含めた（コメントも本文も落として
// いない）かを返す。読んだ記録を付けてよい条件の一部（§J2・QH14）。
func (u j2UpstreamInput) fullyRead() bool {
	return u.CommentsDropped == 0 && !u.BodyTruncated
}

func formatUpstreamIssueBlock(is UpstreamIssue, body string) string {
	return "Issue: " + is.ExternalKey + "\nURL: " + is.URL + "\n状態: " + is.State +
		"\nタイトル: " + is.Title + "\n\n本文:\n" + body
}

func formatUpstreamCommentBlock(index, total int, c UpstreamComment) string {
	return fmt.Sprintf("コメント %d/%d（%s・%s）:\n%s", index, total, c.Author, c.CreatedAt, c.Body)
}

func formatUpstreamReferenceBlock(r UpstreamReferencedIssue) string {
	is := r.Issue
	return "参照先: " + is.ExternalKey + "\nURL: " + is.URL + "\n状態: " + is.State +
		"\nタイトル: " + is.Title + "\n\n本文:\n" + is.Body
}

// upstreamBlockSeparator は同じ区画の中で、コメント・参照先の各ブロックを区切る文字列。
const upstreamBlockSeparator = "\n\n"

// joinedLen は blocks を upstreamBlockSeparator で連結した文字列の長さ（バイト）。
func joinedLen(blocks []string) int {
	if len(blocks) == 0 {
		return 0
	}
	n := len(upstreamBlockSeparator) * (len(blocks) - 1)
	for _, b := range blocks {
		n += len(b)
	}
	return n
}

// truncateUTF8 は s の先頭 n バイト以内を、UTF-8 の文字の途中で切らずに返す。
func truncateUTF8(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// buildUpstreamInput は取得済みの上流の状態 uc を J2 の入力の区画へ整形し、
// 合計を maxUpstreamInputBytes 以内に収める（§J2・決定 M3P14）。
//
// 収め方は次の順（上から順に、収まるまで）:
//  1. 参照先の Issue の本文を、後ろ（出現順の最後）から落とす。
//  2. 課題自身の本文とコメントだけで超えるときは、古いコメントから落とす。
//  3. それでも課題自身の本文だけで超えるときは、本文を上限まで切り詰める
//     （仕様に定めの無い端の扱い。読んだ記録は付けない側に倒す）。
//
// 落とした・切ったことは、注記の区画に明記する（§J2「落としたことを入力に
// 明記する」）。取得できなかった参照（FetchUpstreamContext が Fetched=false で
// 返したもの）も注記に列挙する。
func buildUpstreamInput(uc UpstreamContext) j2UpstreamInput {
	is := uc.Thread.Issue
	body := is.Body
	comments := uc.Thread.Comments

	in := j2UpstreamInput{
		CommentsTotal:         len(comments),
		ObservedCommentsCount: is.Comments,
		ObservedUpdatedAt:     is.UpdatedAt,
	}

	own := formatUpstreamIssueBlock(is, body)
	commentBlocks := make([]string, len(comments))
	for i, c := range comments {
		commentBlocks[i] = formatUpstreamCommentBlock(i+1, len(comments), c)
	}
	var refBlocks []string
	var unfetched []string
	for _, r := range uc.References {
		if !r.Fetched {
			unfetched = append(unfetched, fmt.Sprintf("%s#%d", r.Reference.Repo, r.Reference.Number))
			continue
		}
		refBlocks = append(refBlocks, formatUpstreamReferenceBlock(r))
	}

	// 合計は、実際に区画へ入れる文字列の長さ（同じ区画の中のブロックを区切る
	// 空行 "\n\n" を含む）で数える。
	firstKept := 0
	total := func() int {
		return len(own) + joinedLen(commentBlocks[firstKept:]) + joinedLen(refBlocks)
	}

	for total() > maxUpstreamInputBytes && len(refBlocks) > 0 {
		refBlocks = refBlocks[:len(refBlocks)-1]
		in.ReferencesDropped++
	}
	for total() > maxUpstreamInputBytes && firstKept < len(commentBlocks) {
		firstKept++
	}
	in.CommentsDropped = firstKept
	if excess := total() - maxUpstreamInputBytes; excess > 0 {
		// ここでは total() == len(own)（参照先もコメントも残っていない）。
		body = truncateUTF8(body, len(body)-excess)
		own = formatUpstreamIssueBlock(is, body)
		in.BodyTruncated = true
	}

	in.Sections = append(in.Sections, JudgmentDataSection{Label: labelUpstreamIssue, Content: own})
	if firstKept < len(commentBlocks) {
		in.Sections = append(in.Sections, JudgmentDataSection{
			Label: labelUpstreamComments, Content: strings.Join(commentBlocks[firstKept:], upstreamBlockSeparator),
		})
	}
	if len(refBlocks) > 0 {
		in.Sections = append(in.Sections, JudgmentDataSection{
			Label: labelUpstreamRefs, Content: strings.Join(refBlocks, upstreamBlockSeparator),
		})
	}

	var notes []string
	if in.CommentsDropped > 0 {
		notes = append(notes, fmt.Sprintf("上流のコメントは全 %d 件のうち、古い %d 件を入力の上限（%s）を超えるため省略した（残りは新しい %d 件）。",
			len(comments), in.CommentsDropped, upstreamNoticeCapLabel, len(comments)-in.CommentsDropped))
	}
	if in.ReferencesDropped > 0 {
		notes = append(notes, fmt.Sprintf("参照先の Issue のうち、出現順の後ろの %d 件を入力の上限（%s）を超えるため省略した。",
			in.ReferencesDropped, upstreamNoticeCapLabel))
	}
	if in.BodyTruncated {
		notes = append(notes, fmt.Sprintf("上流の Issue の本文は入力の上限（%s）を超えるため、途中で切り詰めた。", upstreamNoticeCapLabel))
	}
	if len(unfetched) > 0 {
		notes = append(notes, "取得できなかった参照（見つからない・移管された・Issue ではない）: "+strings.Join(unfetched, "、")+"。")
	}
	if len(notes) > 0 {
		in.Sections = append(in.Sections, JudgmentDataSection{Label: labelUpstreamNotice, Content: strings.Join(notes, "\n")})
	}
	return in
}
