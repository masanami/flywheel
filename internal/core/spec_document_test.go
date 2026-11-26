package core

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func criteriaTexts(t *testing.T, doc string) []string {
	t.Helper()
	got, err := extractAcceptanceCriteria(doc)
	if err != nil {
		t.Fatalf("extractAcceptanceCriteria: %v", err)
	}
	texts := []string{}
	for _, c := range got {
		texts = append(texts, c.Text)
	}
	return texts
}

func TestExtractAcceptanceCriteria(t *testing.T) {
	for _, tc := range []struct {
		name, doc string
		want      []string
	}{
		{"unchecked", "## 受入基準\n\n- [ ] 基準 a\n- [ ] 基準 b\n", []string{"基準 a", "基準 b"}},
		{"checked", "## 受入基準\n- [x] 小文字\n- [X] 大文字\n", []string{"小文字", "大文字"}},
		{"indented lines are skipped", "## 受入基準\n- [ ] 親\n  - [ ] 子\n\t- [x] タブの子\n - [ ] 1 字下げ\n", []string{"親"}},
		{"other list lines are skipped", "## 受入基準\n- 印なし\n- [] 空の印\n- [y] 別の印\n* [ ] 別の記号\n- [ ]空白なし\n- [ ] \n- [ ] 　\n本文\n", []string{}},
		{"parenthesized heading is skipped", "## 受入基準（S1・実装済み）\n- [ ] 済み\n## 受入基準 (S2)\n- [ ] 半角\n", []string{}},
		{"other headings are skipped", "## 完了条件\n- [ ] 完了\n## 受入基準の粒度\n- [ ] 粒度\n### 受入基準\n- [ ] 小見出し\n# 受入基準\n- [ ] 大見出し\n  ## 受入基準\n- [ ] 字下げ\n##  受入基準\n- [ ] 空白 2 つ\n", []string{}},
		{"heading with trailing spaces", "## 受入基準 \t\n- [ ] a\n", []string{"a"}},
		{"section ends at the next h2", "- [ ] 前\n## 受入基準\n- [ ] 中\n## 次の節\n- [ ] 後\n", []string{"中"}},
		{"h3 stays inside the section", "## 受入基準\n- [ ] a\n### 画面\n- [ ] b\n#### 細目\n- [ ] c\n", []string{"a", "b", "c"}},
		{"every matching section is read", "## 受入基準\n- [ ] a\n## 他\n- [ ] x\n## 受入基準\n- [ ] b\n", []string{"a", "b"}},
		{"CRLF", "## 受入基準\r\n\r\n- [ ] a\r\n- [x] b\r\n## 次\r\n- [ ] c\r\n", []string{"a", "b"}},
		{"whitespace is normalized", "## 受入基準\n- [ ]   a \t b　　c  \n", []string{"a b c"}},
		{"whitespace-only differences count once", "## 受入基準\n- [ ] 同じ 基準\n- [x]    同じ \t  基準   \n- [ ] 別の基準\n", []string{"同じ 基準", "別の基準"}},
		{"empty document", "", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := criteriaTexts(t, tc.doc)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("texts = %q, want %q", got, tc.want)
			}
			for _, s := range got {
				if strings.Contains(s, "\r") {
					t.Errorf("text %q holds CR", s)
				}
			}
		})
	}
}

// 期待する識別は、実装と独立に `printf '%s' 'ログイン できる' | shasum -a 256` で求めた値。
func TestExtractAcceptanceCriteria_IDIsHashOfNormalizedText(t *testing.T) {
	got, err := extractAcceptanceCriteria("## 受入基準\n- [ ]  ログイン \t できる \n")
	if err != nil {
		t.Fatal(err)
	}
	want := []acceptanceCriterion{{ID: "AC-16be1d8c", Text: "ログイン できる"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("criteria = %+v, want %+v", got, want)
	}
}

func TestExtractAcceptanceCriteria_InsertingALineKeepsOtherIDs(t *testing.T) {
	before, err := extractAcceptanceCriteria("## 受入基準\n- [ ] a\n- [ ] b\n")
	if err != nil {
		t.Fatal(err)
	}
	after, err := extractAcceptanceCriteria("## 受入基準\n- [ ] a\n- [ ] 足した基準\n- [ ] b\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 2 || len(after) != 3 || after[0] != before[0] || after[2] != before[1] || after[1].ID == before[1].ID {
		t.Errorf("before = %+v, after = %+v", before, after)
	}
}

// 先頭 8 桁（7b32deba）が一致する、文の違う 2 行。総当たりで前もって探した組。
const collidingDoc = "## 受入基準\n- [ ] 衝突の試験の基準 6129\n- [ ] 衝突の試験の基準 116837\n"

func TestExtractAcceptanceCriteria_CollidingIDsAreAnError(t *testing.T) {
	got, err := extractAcceptanceCriteria(collidingDoc)
	if !errors.Is(err, errSpecDuplicateID) || got != nil {
		t.Errorf("got %+v, %v; want errSpecDuplicateID", got, err)
	}
}

func writeWorkspaceFile(t *testing.T, ws, rel, content string) {
	t.Helper()
	p := filepath.Join(ws, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadSpecCriteria(t *testing.T) {
	const doc = "## 受入基準\n- [ ] a\n"
	ws := t.TempDir()
	writeWorkspaceFile(t, ws, "docs/x.md", doc)
	got, err := readSpecCriteria(ws, "docs/x.md")
	if err != nil || len(got) != 1 || got[0].Text != "a" {
		t.Fatalf("got %+v, %v", got, err)
	}

	pad := func(n int) string { return doc + strings.Repeat("x", n-len(doc)) }
	writeWorkspaceFile(t, ws, "limit.md", pad(2<<20))
	if _, err := readSpecCriteria(ws, "limit.md"); err != nil {
		t.Errorf("exactly 2 MiB: %v", err)
	}
	writeWorkspaceFile(t, ws, "over.md", pad(2<<20+1))
	writeWorkspaceFile(t, ws, "none.md", "## 受入基準\n本文だけ\n")
	writeWorkspaceFile(t, ws, "collide.md", collidingDoc)
	outside := t.TempDir()
	writeWorkspaceFile(t, outside, "o.md", doc)
	// 書き込みのときは無かったリンクが、後でワークスペースの外を指す場合。
	if err := os.Symlink(filepath.Join(outside, "o.md"), filepath.Join(ws, "escape.md")); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]error{
		"missing.md":  errSpecUnreadable,
		"docs":        errSpecUnreadable,
		"over.md":     errSpecTooLarge,
		"none.md":     errSpecNoCriteria,
		"collide.md":  errSpecDuplicateID,
		"escape.md":   errSpecPathOutside,
		"../x.md":     errSpecPathOutside,
		"/etc/passwd": errSpecPathAbsolute,
	} {
		if got, err := readSpecCriteria(ws, path); !errors.Is(err, want) || !errors.Is(err, errSpecUnreadable) || got != nil {
			t.Errorf("%s: got %+v, %v; want %v", path, got, err, want)
		}
	}
}

func TestReadSpecCriteria_NoReadPermission(t *testing.T) {
	ws := t.TempDir()
	writeWorkspaceFile(t, ws, "x.md", "## 受入基準\n- [ ] a\n")
	p := filepath.Join(ws, "x.md")
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	if f, err := os.Open(p); err == nil {
		_ = f.Close()
		t.Skip("this user ignores file permissions")
	}
	if _, err := readSpecCriteria(ws, "x.md"); !errors.Is(err, errSpecUnreadable) {
		t.Errorf("err = %v, want errSpecUnreadable", err)
	}
}
