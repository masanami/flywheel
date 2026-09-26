package github

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
)

// statusLineRe は gh api -i の標準出力の 1 行目（ステータス行）を
// HTTP/1.1・HTTP/2・HTTP/2.0 のいずれの表記でも読めるようにする
// （docs/features/m2-github-issue-ingest.md §上流の close の検出、
// Issue #55 本文の実測「HTTP/2.0 404 Not Found」）。
var statusLineRe = regexp.MustCompile(`^HTTP/\d+(?:\.\d+)? (\d{3})`)

// splitHTTPResponse は gh api -i の標準出力を、ステータス行の HTTP
// ステータスコードと、ヘッダに続く本文（JSON）に分ける。ヘッダと本文の
// 区切りは空行（CRLF・LF のどちらでも）とする。ステータス行が読めない場合は
// エラーを返す（呼び出し側はこれを「それ以外の失敗」として扱う。
// エラー文言・stderr の部分一致では判定しない）。
func splitHTTPResponse(raw []byte) (int, []byte, error) {
	normalized := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))

	nlIdx := bytes.IndexByte(normalized, '\n')
	if nlIdx == -1 {
		return 0, nil, fmt.Errorf("adapters/github: no status line in gh -i output (%q)", firstBytes(normalized, 80))
	}
	firstLine := normalized[:nlIdx]

	m := statusLineRe.FindSubmatch(firstLine)
	if m == nil {
		return 0, nil, fmt.Errorf("adapters/github: unrecognized http status line %q", firstLine)
	}
	status, err := strconv.Atoi(string(m[1]))
	if err != nil {
		return 0, nil, fmt.Errorf("adapters/github: parse http status %q: %w", m[1], err)
	}

	sepIdx := bytes.Index(normalized, []byte("\n\n"))
	if sepIdx == -1 {
		return 0, nil, fmt.Errorf("adapters/github: no header/body separator in gh -i output")
	}
	body := normalized[sepIdx+2:]

	return status, body, nil
}

func firstBytes(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[:n]
}
