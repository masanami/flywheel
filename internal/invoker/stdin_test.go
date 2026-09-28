package invoker

import (
	"strings"
	"testing"
)

// AC-35: 課題の本文は標準入力で渡す（引数には現れない、というのは
// args_test.go で確認済み）。
// AC-36: 課題の本文は、区切りの行に囲まれたデータの区画の内側にだけ現れる。
func TestBuildStdin_ExternalContentOnlyInsideDataSection(t *testing.T) {
	body := "課題の本文（固有の文字列 XYZZY-BODY）"
	stdin := string(BuildStdin("これは指示文です。", []DataSection{{Label: "issue_body", Content: body}}))

	if !strings.Contains(stdin, body) {
		t.Fatalf("stdin does not contain the body at all: %q", stdin)
	}

	begin := strings.Index(stdin, dataSectionBeginPrefix+"issue_body"+dataSectionBeginSuffix)
	end := strings.Index(stdin, dataSectionEnd)
	if begin < 0 || end < 0 || end < begin {
		t.Fatalf("data section markers not found in order: begin=%d end=%d\nstdin=%q", begin, end, stdin)
	}
	bodyIdx := strings.Index(stdin, body)
	if bodyIdx < begin || bodyIdx > end {
		t.Fatalf("body at %d is outside the data section [%d,%d]", bodyIdx, begin, end)
	}

	// 区画の外（先頭の指示文の部分）には本文が現れない。
	outside := stdin[:begin]
	if strings.Contains(outside, body) {
		t.Fatalf("body leaked outside the data section: %q", outside)
	}
}

func TestBuildStdin_IncludesGuardNote(t *testing.T) {
	stdin := string(BuildStdin("instructions", nil))
	if !strings.Contains(stdin, dataSectionGuardNote) {
		t.Fatalf("stdin does not contain the guard note: %q", stdin)
	}
}

func TestWrapDataSection_BeginsAndEndsWithDelimiters(t *testing.T) {
	wrapped := WrapDataSection("label", "content")
	if !strings.HasPrefix(wrapped, dataSectionBeginPrefix+"label"+dataSectionBeginSuffix) {
		t.Fatalf("wrapped does not start with the begin marker: %q", wrapped)
	}
	if !strings.Contains(wrapped, dataSectionEnd) {
		t.Fatalf("wrapped does not contain the end marker: %q", wrapped)
	}
}

// 複数のデータ区画を渡したとき、それぞれが自分のラベルの区画にだけ現れる
// （区画をまたいで漏れない）ことを確認する。
func TestBuildStdin_MultipleSectionsDoNotLeakIntoEachOther(t *testing.T) {
	stdin := string(BuildStdin("instructions", []DataSection{
		{Label: "a", Content: "CONTENT-A"},
		{Label: "b", Content: "CONTENT-B"},
	}))
	aBegin := strings.Index(stdin, dataSectionBeginPrefix+"a"+dataSectionBeginSuffix)
	aEnd := strings.Index(stdin[aBegin:], dataSectionEnd) + aBegin
	if strings.Contains(stdin[aBegin:aEnd], "CONTENT-B") {
		t.Fatalf("section a leaked content from section b: %q", stdin[aBegin:aEnd])
	}
}
