package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// このファイルは、docs_*_test.go が共有する助け関数を置く。

func readRepoDoc(t *testing.T, rel ...string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(append([]string{repoRoot(t)}, rel...)...))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// glossaryRow は用語集の表の、語（先頭の列）が term の行を、列に分けて返す（先頭と末尾の空の列は除く）。
func glossaryRow(t *testing.T, term string) []string {
	t.Helper()
	for _, line := range strings.Split(readRepoDoc(t, "docs", "glossary.md"), "\n") {
		if !strings.HasPrefix(line, "| "+term+" |") {
			continue
		}
		cols := strings.Split(strings.Trim(line, "| "), " | ")
		if len(cols) != 5 {
			t.Fatalf("glossary row %q has %d columns, want 5", term, len(cols))
		}
		return cols
	}
	t.Fatalf("glossary has no row for %q", term)
	return nil
}

// sectionBody は、見出しの行が heading の節の本文（次の同じ深さ以上の見出しまで）を返す。
func sectionBody(t *testing.T, doc, heading string) string {
	t.Helper()
	lines := strings.Split(doc, "\n")
	depth := len(heading) - len(strings.TrimLeft(heading, "#"))
	var out []string
	in := false
	for _, l := range lines {
		if !in {
			in = l == heading
			continue
		}
		if strings.HasPrefix(l, "#") {
			d := len(l) - len(strings.TrimLeft(l, "#"))
			if d <= depth {
				break
			}
		}
		out = append(out, l)
	}
	if !in {
		t.Fatalf("no heading %q", heading)
	}
	return strings.Join(out, "\n")
}

// docTableRow は、文書の表の、prefix で始まる行をちょうど 1 つ返す。
func docTableRow(t *testing.T, doc, prefix string) string {
	t.Helper()
	var rows []string
	for _, l := range strings.Split(doc, "\n") {
		if strings.HasPrefix(l, prefix) {
			rows = append(rows, l)
		}
	}
	if len(rows) != 1 {
		t.Fatalf("want exactly one row starting with %q, got %d", prefix, len(rows))
	}
	return rows[0]
}

// docLineWith は、文書の行のうち、all の語をすべて含む最初の行を返す。
func docLineWith(t *testing.T, doc string, all ...string) string {
	t.Helper()
	for _, l := range strings.Split(doc, "\n") {
		ok := true
		for _, w := range all {
			if !strings.Contains(l, w) {
				ok = false
				break
			}
		}
		if ok {
			return l
		}
	}
	t.Fatalf("no line contains all of %q", all)
	return ""
}

func requireAll(t *testing.T, where, body string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("%s: missing %q", where, w)
		}
	}
}
