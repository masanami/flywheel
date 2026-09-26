package core

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// AC-62: 現行 ingest-fp.sh のゴールデン値のフィクスチャ（claude-flywheel の
// scripts/tests/fixtures/ingest-fp/issue-130-body.txt を internal/core/testdata へ
// 写したもの）の fingerprint が "2:9c56b5c0a92d" になる。
func TestFingerprint_GoldenFixtureIssue130(t *testing.T) {
	path := filepath.Join("testdata", "ingest-fp", "issue-130-body.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	got := Fingerprint(string(data))
	want := "2:9c56b5c0a92d"
	if got != want {
		t.Fatalf("Fingerprint(golden fixture) = %q, want %q", got, want)
	}
}

// TestFingerprintGoldenFixture_ByteCountAndSHA256 は写したフィクスチャ自体が
// 親要件チケットの指示どおりの内容であることを固定する（8,591 バイト・
// ファイル全体の SHA-256 が 5daa7cdc6e728b82956d267e96d3c17112ce1bc976669f4ff9eeb9e7ec891980）。
func TestFingerprintGoldenFixture_ByteCountAndSHA256(t *testing.T) {
	path := filepath.Join("testdata", "ingest-fp", "issue-130-body.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if len(data) != 8591 {
		t.Fatalf("fixture byte count = %d, want 8591", len(data))
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	want := "5daa7cdc6e728b82956d267e96d3c17112ce1bc976669f4ff9eeb9e7ec891980"
	if got != want {
		t.Fatalf("fixture sha256 = %q, want %q", got, want)
	}
}

// AC-62（境界値の既知ベクタ）: 本文 "hello" は 2cf24dba5fb0、空の本文と空白だけの
// 本文は e3b0c44298fc になる。
func TestFingerprint_KnownVectorsAndBoundaries(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"hello", "hello", "2:2cf24dba5fb0"},
		{"empty body", "", "2:e3b0c44298fc"},
		{"whitespace-only body", "  \n\t\n", "2:e3b0c44298fc"},
		// self-review 指摘: 正規化3の cutset は仕様どおり「スペース・タブ・LF・
		// VT・FF」の5種。VT(\v)・FF(\f) を含む境界は上のケースでは1つも
		// 使っておらず、cutset を " \t\n" に狭めても検出できなかった
		// （ミューテーションテストの取りこぼし）。
		{"whitespace-only body with VT and FF", "\v\f", "2:e3b0c44298fc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Fingerprint(c.body); got != c.want {
				t.Errorf("Fingerprint(%q) = %q, want %q", c.body, got, c.want)
			}
		})
	}
}

// TestFingerprint_LeadingTrailingVerticalTabAndFormFeedAreTrimmed は
// TestFingerprint_KnownVectorsAndBoundaries を補い、VT・FF が本文の途中では
// 意味を持ち、先頭・末尾でだけ trim されることを固定する（"a" との同値比較）。
func TestFingerprint_LeadingTrailingVerticalTabAndFormFeedAreTrimmed(t *testing.T) {
	want := Fingerprint("a")
	got := Fingerprint("\v\fa\f\v")
	if got != want {
		t.Errorf("Fingerprint(%q) = %q, want %q (= Fingerprint(%q))", "\v\fa\f\v", got, want, "a")
	}
}

// AC-63・AC-64・AC-65（正規化のケース。現行 ingest-fp.test.sh §3 の正規化ケースを
// Go のテストへ移植したもの）。base は "a\nb" の fingerprint。
func TestFingerprint_NormalizationSameValue(t *testing.T) {
	base := Fingerprint("a\nb")
	cases := []struct {
		name string
		body string
	}{
		{"CRLF は LF と同値", "a\r\nb"},
		{"行末のスペースは無視", "a   \nb"},
		{"行末のタブは無視", "a\t\nb"},
		{"末尾の改行は無視", "a\nb\n"},
		{"末尾の改行が複数でも無視", "a\nb\n\n\n"},
		{"先頭の空行・空白は無視", "\n  \na\nb"},
		{"先頭行のインデントは全体trimに含まれる", "  a\nb"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Fingerprint(c.body); got != base {
				t.Errorf("Fingerprint(%q) = %q, want %q (base)", c.body, got, base)
			}
		})
	}
}

// "a\rb" は CR 除去後 "ab" になる（改行が無くなる）ため、"a\nb" とは別の値になる。
// 上のテーブルにこの期待だけ食い違いが出るため、独立したテストで固定する。
func TestFingerprint_LoneCRRemovesTheCRWithoutInsertingNewline(t *testing.T) {
	got := Fingerprint("a\rb")
	want := Fingerprint("ab")
	if got != want {
		t.Errorf("Fingerprint(%q) = %q, want %q (= Fingerprint(%q))", "a\rb", got, want, "ab")
	}
}

func TestFingerprint_NormalizationDifferentValue(t *testing.T) {
	base := Fingerprint("a\nb")
	cases := []struct {
		name string
		body string
	}{
		{"本文中の行頭インデントは意味のある差", "a\n  b"},
		{"本文中の空行は意味のある差", "a\n\nb"},
		{"大文字小文字は意味のある差", "A\nb"},
		{"引用マーカーは除去しない", "> a\nb"},
		{"リストマーカーは除去しない", "- a\nb"},
		{"本文の1文字変更で値が変わる", "a\nc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Fingerprint(c.body); got == base {
				t.Errorf("Fingerprint(%q) = %q, must differ from base %q", c.body, got, base)
			}
		})
	}
}

// AC-65: 末尾に全角空白（U+3000）を持つ本文と持たない本文は違う fingerprint に
// なる（strings.TrimSpace を使っていないことの直接的な検証）。
func TestFingerprint_TrailingFullWidthSpaceIsSignificant(t *testing.T) {
	withoutFullWidthSpace := Fingerprint("a\nb")
	withFullWidthSpace := Fingerprint("a\nb　")
	if withoutFullWidthSpace == withFullWidthSpace {
		t.Fatalf("Fingerprint with trailing U+3000 must differ from without, got equal %q", withoutFullWidthSpace)
	}
}

// AC-57 の前提: hasKnownFingerprintVersion は記録された fingerprint の版が
// FingerprintVersion（2）と一致するかを判定する（#57 の fail-closed 分岐が使う）。
func TestHasKnownFingerprintVersion(t *testing.T) {
	cases := []struct {
		name string
		fp   string
		want bool
	}{
		{"版2", "2:abcdef012345", true},
		{"版1", "1:abcdef012345", false},
		{"版なし・区切りが無い", "abcdef012345", false},
		{"空文字列", "", false},
		{"版が3", "3:abcdef012345", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasKnownFingerprintVersion(c.fp); got != c.want {
				t.Errorf("hasKnownFingerprintVersion(%q) = %v, want %v", c.fp, got, c.want)
			}
		})
	}
}

// 決定性: 同じ入力を2回通しても同じ値。
func TestFingerprint_Deterministic(t *testing.T) {
	body := "some body\nwith multiple lines\n"
	first := Fingerprint(body)
	second := Fingerprint(body)
	if first != second {
		t.Fatalf("Fingerprint is not deterministic for the same input: %q != %q", first, second)
	}
}
