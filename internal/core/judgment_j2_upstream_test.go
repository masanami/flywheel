package core

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// このファイルは #85（親要件チケット #77・docs/features/m3-invoker-delegation.md
// §J2 計画・決定 M3P14）の、J2 の入力へ組み込む上流の最新の状態（本文・全コメント・
// 参照先の Issue）の組み立てと、200,000 バイトの切り詰め（AC-91〜93 の入力側）を
// 検証する。

func sectionsText(secs []JudgmentDataSection) string {
	var b strings.Builder
	for _, s := range secs {
		b.WriteString("[" + s.Label + "]\n" + s.Content + "\n")
	}
	return b.String()
}

func sectionByLabel(secs []JudgmentDataSection, label string) (JudgmentDataSection, bool) {
	for _, s := range secs {
		if s.Label == label {
			return s, true
		}
	}
	return JudgmentDataSection{}, false
}

func testUpstreamContext(body string, comments ...string) UpstreamContext {
	uc := UpstreamContext{Thread: UpstreamIssueThread{Issue: UpstreamIssue{
		ExternalKey: "o/r#7", Repo: "o/r", Number: 7, Title: "T-7", Body: body, State: "open",
		URL: "https://github.com/o/r/issues/7", Comments: len(comments), UpdatedAt: "2026-09-25T00:00:00Z",
	}}}
	for i, c := range comments {
		uc.Thread.Comments = append(uc.Thread.Comments, UpstreamComment{Author: "u", Body: c, CreatedAt: "2026-09-2" + string(rune('0'+i%10)) + "T00:00:00Z"})
	}
	return uc
}

func TestBuildUpstreamInput_IncludesBodyCommentsAndReferences(t *testing.T) {
	uc := testUpstreamContext("BODY-MARKER", "COMMENT-1-MARKER", "COMMENT-2-MARKER")
	uc.References = []UpstreamReferencedIssue{
		{Reference: UpstreamReference{Repo: "o/r", Number: 9}, Fetched: true, Issue: UpstreamIssue{ExternalKey: "o/r#9", Title: "REF-TITLE", Body: "REF-BODY-MARKER", State: "open"}},
	}

	in := buildUpstreamInput(uc)
	all := sectionsText(in.Sections)
	for _, want := range []string{"BODY-MARKER", "COMMENT-1-MARKER", "COMMENT-2-MARKER", "REF-BODY-MARKER", "o/r#7", "o/r#9"} {
		if !strings.Contains(all, want) {
			t.Errorf("sections do not contain %q:\n%s", want, all)
		}
	}
	if in.CommentsDropped != 0 || in.ReferencesDropped != 0 || in.BodyTruncated {
		t.Errorf("nothing should be dropped: %+v", in)
	}
	if !in.fullyRead() {
		t.Errorf("fullyRead() = false, want true when no comment is dropped")
	}
	if in.ObservedCommentsCount != 2 || in.ObservedUpdatedAt != "2026-09-25T00:00:00Z" {
		t.Errorf("observation = (%d, %q), want the values from the issue fetched (2, 2026-09-25T00:00:00Z)", in.ObservedCommentsCount, in.ObservedUpdatedAt)
	}
	if _, ok := sectionByLabel(in.Sections, "上流の入力についての注記"); ok {
		t.Errorf("a notice section must not be added when nothing was dropped")
	}
}

// AC-92: 課題自身の本文とコメントだけで 200,000 バイトを超えるときは、古い
// コメントから落とし、落としたことを入力に明記する。
func TestBuildUpstreamInput_DropsOldestCommentsWhenOverCap(t *testing.T) {
	big := strings.Repeat("あ", 30000) // 90,000 バイト
	uc := testUpstreamContext("BODY", "OLDEST-"+big, "MIDDLE-"+big, "NEWEST-"+big)

	in := buildUpstreamInput(uc)
	all := sectionsText(in.Sections)
	if strings.Contains(all, "OLDEST-") {
		t.Errorf("the oldest comment must be dropped first")
	}
	if !strings.Contains(all, "MIDDLE-") || !strings.Contains(all, "NEWEST-") {
		t.Errorf("the newer comments must be kept")
	}
	if in.CommentsDropped != 1 {
		t.Errorf("CommentsDropped = %d, want 1", in.CommentsDropped)
	}
	if in.fullyRead() {
		t.Errorf("fullyRead() = true, want false when a comment was dropped")
	}
	notice, ok := sectionByLabel(in.Sections, "上流の入力についての注記")
	if !ok {
		t.Fatalf("a notice section stating that comments were dropped is missing:\n%s", all)
	}
	if !strings.Contains(notice.Content, "コメント") || !strings.Contains(notice.Content, "省略") {
		t.Errorf("the notice must say comments were omitted, got %q", notice.Content)
	}
	if got := totalUpstreamBytes(in); got > maxUpstreamInputBytes {
		t.Errorf("upstream input = %d bytes, want <= %d", got, maxUpstreamInputBytes)
	}
}

// totalUpstreamBytes は本文・コメント・参照先の区画の合計バイト数（注記の区画を除く）。
func totalUpstreamBytes(in j2UpstreamInput) int {
	n := 0
	for _, s := range in.Sections {
		switch s.Label {
		case "上流の Issue", "上流のコメント", "参照先の Issue":
			n += len(s.Content)
		}
	}
	return n
}

func TestBuildUpstreamInput_DropsReferencesFromTheEndBeforeComments(t *testing.T) {
	medium := strings.Repeat("a", 60000)
	uc := testUpstreamContext("BODY", "COMMENT-KEPT-"+medium)
	for i, name := range []string{"FIRST", "SECOND", "THIRD"} {
		uc.References = append(uc.References, UpstreamReferencedIssue{
			Reference: UpstreamReference{Repo: "o/r", Number: 10 + i}, Fetched: true,
			Issue: UpstreamIssue{ExternalKey: "o/r#" + string(rune('0'+i)), Title: name, Body: name + "-REF-BODY-" + medium, State: "open"},
		})
	}

	in := buildUpstreamInput(uc)
	all := sectionsText(in.Sections)
	if !strings.Contains(all, "COMMENT-KEPT-") {
		t.Errorf("comments must be kept while dropping references is enough")
	}
	if !strings.Contains(all, "FIRST-REF-BODY-") {
		t.Errorf("the first reference must be kept")
	}
	if strings.Contains(all, "THIRD-REF-BODY-") {
		t.Errorf("the last reference must be dropped first")
	}
	if in.ReferencesDropped == 0 {
		t.Errorf("ReferencesDropped = 0, want > 0")
	}
	if in.CommentsDropped != 0 || !in.fullyRead() {
		t.Errorf("no comment may be dropped when dropping references suffices: %+v", in)
	}
	if got := totalUpstreamBytes(in); got > maxUpstreamInputBytes {
		t.Errorf("upstream input = %d bytes, want <= %d", got, maxUpstreamInputBytes)
	}
	notice, ok := sectionByLabel(in.Sections, "上流の入力についての注記")
	if !ok || !strings.Contains(notice.Content, "参照先") {
		t.Errorf("the notice must say referenced issues were omitted: %+v", notice)
	}
}

func TestBuildUpstreamInput_ExactlyAtCapIsNotDropped(t *testing.T) {
	// 区画の形式に依らず境界を確かめるため、上限ちょうどになる本文を探索して作る。
	uc := testUpstreamContext("")
	overhead := totalUpstreamBytes(buildUpstreamInput(uc))
	uc = testUpstreamContext(strings.Repeat("x", maxUpstreamInputBytes-overhead))
	in := buildUpstreamInput(uc)
	if got := totalUpstreamBytes(in); got != maxUpstreamInputBytes {
		t.Fatalf("test setup: total = %d, want exactly %d", got, maxUpstreamInputBytes)
	}
	if in.BodyTruncated || in.CommentsDropped != 0 {
		t.Errorf("exactly at the cap must not drop or truncate anything: %+v", in)
	}

	over := buildUpstreamInput(testUpstreamContext(strings.Repeat("x", maxUpstreamInputBytes-overhead+1)))
	if !over.BodyTruncated {
		t.Errorf("one byte over the cap with no comment to drop must truncate the body")
	}
	if got := totalUpstreamBytes(over); got > maxUpstreamInputBytes {
		t.Errorf("truncated input = %d bytes, want <= %d", got, maxUpstreamInputBytes)
	}
}

func TestBuildUpstreamInput_TruncatedBodyStaysValidUTF8AndIsNotFullyRead(t *testing.T) {
	uc := testUpstreamContext(strings.Repeat("あ", 100000)) // 300,000 バイト・コメント無し
	in := buildUpstreamInput(uc)
	if !in.BodyTruncated {
		t.Fatalf("BodyTruncated = false, want true")
	}
	if in.fullyRead() {
		t.Errorf("fullyRead() = true, want false: a truncated body was not fully read")
	}
	issue, ok := sectionByLabel(in.Sections, "上流の Issue")
	if !ok || !utf8.ValidString(issue.Content) {
		t.Errorf("the truncated issue section must be valid UTF-8 (ok=%v)", ok)
	}
	if got := totalUpstreamBytes(in); got > maxUpstreamInputBytes {
		t.Errorf("upstream input = %d bytes, want <= %d", got, maxUpstreamInputBytes)
	}
	notice, ok := sectionByLabel(in.Sections, "上流の入力についての注記")
	if !ok || !strings.Contains(notice.Content, "本文") {
		t.Errorf("the notice must say the body was truncated: %+v", notice)
	}
}

func TestBuildUpstreamInput_NotesReferencesThatCouldNotBeFetched(t *testing.T) {
	uc := testUpstreamContext("BODY")
	uc.References = []UpstreamReferencedIssue{
		{Reference: UpstreamReference{Repo: "o/r", Number: 404}, Fetched: false},
	}
	in := buildUpstreamInput(uc)
	notice, ok := sectionByLabel(in.Sections, "上流の入力についての注記")
	if !ok || !strings.Contains(notice.Content, "o/r#404") {
		t.Errorf("the notice must list the reference that could not be fetched, got %+v", notice)
	}
	if !in.fullyRead() {
		t.Errorf("an unfetched reference does not make the comments unread")
	}
}

// 同じ区画の中のブロックを区切る空行も上限に数える: ブロックの長さの合計だけなら
// 上限以内でも、区切りを足すと超える入力では、コメントを落とす。
func TestBuildUpstreamInput_CountsBlockSeparatorsAgainstTheCap(t *testing.T) {
	const n = 10
	base := testUpstreamContext("b")
	own := len(formatUpstreamIssueBlock(base.Thread.Issue, "b"))
	sumPrefix := 0
	for i := 1; i <= n; i++ {
		sumPrefix += len(formatUpstreamCommentBlock(i, n, UpstreamComment{Author: "u", CreatedAt: "2026-09-25T00:00:00Z"}))
	}
	bodyLen := (maxUpstreamInputBytes - own - sumPrefix) / n // ブロックの長さの合計が上限以内で最大になる本文の長さ

	var comments []string
	for i := 0; i < n; i++ {
		comments = append(comments, strings.Repeat("x", bodyLen))
	}
	uc := testUpstreamContext("b", comments...)
	for i := range uc.Thread.Comments {
		uc.Thread.Comments[i].CreatedAt = "2026-09-25T00:00:00Z"
	}
	blocksOnly := own
	for i, c := range uc.Thread.Comments {
		blocksOnly += len(formatUpstreamCommentBlock(i+1, n, c))
	}
	if blocksOnly > maxUpstreamInputBytes || blocksOnly+2*(n-1) <= maxUpstreamInputBytes {
		t.Fatalf("test setup: blocks=%d must be <= cap but blocks+separators must exceed it", blocksOnly)
	}

	in := buildUpstreamInput(uc)
	if in.CommentsDropped == 0 {
		t.Errorf("CommentsDropped = 0, want >= 1 (the separators between the comments push the section over the cap)")
	}
	if got := totalUpstreamBytes(in); got > maxUpstreamInputBytes {
		t.Errorf("upstream input = %d bytes, want <= %d", got, maxUpstreamInputBytes)
	}
}
