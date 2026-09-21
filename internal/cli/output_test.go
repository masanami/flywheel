package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestWriteJSONSuccess_NoHTMLEscapingAndSingleTrailingNewline(t *testing.T) {
	var buf bytes.Buffer
	payload := map[string]any{"challenge": map[string]any{"title": "<a> & 日本語"}}
	if err := writeJSONSuccess(&buf, payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "<a> & ") {
		t.Fatalf("HTML characters were escaped (expected literal <a> & unescaped): %q", out)
	}
	wantEscaped := []string{"\\u003c", "\\u0026"}
	for _, esc := range wantEscaped {
		if strings.Contains(out, esc) {
			t.Fatalf("output contains HTML-escaped sequence %q: %q", esc, out)
		}
	}
	if !strings.Contains(out, "日本語") {
		t.Fatalf("non-ASCII characters were escaped: %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("expected a trailing newline, got %q", out)
	}
	if strings.HasSuffix(out, "\n\n") {
		t.Fatalf("expected exactly one trailing newline, got %q", out)
	}
}

func TestWriteJSONSuccess_RejectsNonObjectTopLevel(t *testing.T) {
	cases := []any{
		[]string{"a", "b"},
		"just a string",
		42,
		nil,
	}
	for _, v := range cases {
		var buf bytes.Buffer
		err := writeJSONSuccess(&buf, v)
		if err == nil {
			t.Errorf("writeJSONSuccess(%#v): expected error for non-object top-level payload", v)
		}
		if buf.Len() != 0 {
			t.Errorf("writeJSONSuccess(%#v): buffer should stay empty on rejection, got %q", v, buf.String())
		}
	}
}

func TestWriteJSONSuccess_AcceptsObjectTopLevel(t *testing.T) {
	var buf bytes.Buffer
	if err := writeJSONSuccess(&buf, map[string]any{"challenges": []any{}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected output, got none")
	}
}

func TestWriteJSONError_Envelope(t *testing.T) {
	var buf bytes.Buffer
	if err := writeJSONError(&buf, CodeNotFound, "x not found"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"error":{"code":"not_found","message":"x not found"}}` + "\n"
	if buf.String() != want {
		t.Fatalf("got %q, want %q", buf.String(), want)
	}
}

func TestWriteTextError_Format(t *testing.T) {
	var buf bytes.Buffer
	writeTextError(&buf, CodeNotFound, "x not found")
	want := "flywheel: x not found (not_found)\n"
	if buf.String() != want {
		t.Fatalf("got %q, want %q", buf.String(), want)
	}
}

func TestFormatTimestamp_UTCWithMillisAndZ(t *testing.T) {
	tm := time.Date(2026, 9, 21, 8, 30, 0, 123000000, time.UTC)
	got := FormatTimestamp(tm)
	want := "2026-09-21T08:30:00.123Z"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFormatTimestamp_ConvertsNonUTCInputToUTC(t *testing.T) {
	jst := time.FixedZone("JST", 9*3600)
	tm := time.Date(2026, 9, 21, 17, 30, 0, 0, jst)
	got := FormatTimestamp(tm)
	want := "2026-09-21T08:30:00.000Z"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
