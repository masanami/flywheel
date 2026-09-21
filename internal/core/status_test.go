package core

import "testing"

// TestParseStatus_AcceptsExactCode は、状態語彙のコード（例: "unclassified"）と
// 前後の空白を除いて完全一致したときだけ受理することを検証する。
func TestParseStatus_AcceptsExactCode(t *testing.T) {
	got, ok := ParseStatus("unclassified")
	if !ok {
		t.Fatalf("ParseStatus(%q) ok = false, want true", "unclassified")
	}
	if got != StatusUnclassified {
		t.Errorf("ParseStatus(%q) = %q, want %q", "unclassified", got, StatusUnclassified)
	}
}

// TestParseStatus_AcceptsExactLabel は、表示名（日本語）との完全一致も受理することを検証する。
func TestParseStatus_AcceptsExactLabel(t *testing.T) {
	got, ok := ParseStatus("未分類")
	if !ok {
		t.Fatalf("ParseStatus(%q) ok = false, want true", "未分類")
	}
	if got != StatusUnclassified {
		t.Errorf("ParseStatus(%q) = %q, want %q", "未分類", got, StatusUnclassified)
	}
}

// TestParseStatus_TrimsSurroundingWhitespaceOnly は、前後の空白は除いて一致判定する
// （内部の空白は除かない）ことを検証する（現行 validate-artifact の振る舞いの引き継ぎ）。
func TestParseStatus_TrimsSurroundingWhitespaceOnly(t *testing.T) {
	got, ok := ParseStatus("  unclassified\t\n")
	if !ok {
		t.Fatalf("ParseStatus with surrounding whitespace: ok = false, want true")
	}
	if got != StatusUnclassified {
		t.Errorf("ParseStatus with surrounding whitespace = %q, want %q", got, StatusUnclassified)
	}
}

// TestParseStatus_RejectsPartialMatch は、部分一致（前後に文字列が付く場合）を拒否することを検証する。
// 現行 validate-artifact のテストが固定する「計画承認待ち（未分類 → … → 完了）」の拒否に相当。
func TestParseStatus_RejectsPartialMatch(t *testing.T) {
	if _, ok := ParseStatus("計画承認待ち（未分類 → … → 完了）"); ok {
		t.Errorf("ParseStatus with extra surrounding text: ok = true, want false")
	}
}

// TestParseStatus_RejectsUnknownValue は、語彙に無い値（現行の「レビュー待ち」相当）を拒否することを検証する。
func TestParseStatus_RejectsUnknownValue(t *testing.T) {
	if _, ok := ParseStatus("レビュー待ち"); ok {
		t.Errorf("ParseStatus(unknown): ok = true, want false")
	}
}

// TestParseStatus_RejectsCaseVariant は、大文字小文字の違いを一致とみなさないことを検証する。
func TestParseStatus_RejectsCaseVariant(t *testing.T) {
	if _, ok := ParseStatus("UNCLASSIFIED"); ok {
		t.Errorf("ParseStatus(case variant): ok = true, want false")
	}
}

// TestParseStatus_RejectsInternalWhitespaceVariant は、内部の空白違いを一致とみなさないことを検証する。
func TestParseStatus_RejectsInternalWhitespaceVariant(t *testing.T) {
	if _, ok := ParseStatus("un classified"); ok {
		t.Errorf("ParseStatus(internal whitespace variant): ok = true, want false")
	}
}

// TestParseStatus_RejectsEmpty は、空文字列（空白のみを含む）を拒否することを検証する。
func TestParseStatus_RejectsEmpty(t *testing.T) {
	if _, ok := ParseStatus("   "); ok {
		t.Errorf("ParseStatus(whitespace only): ok = true, want false")
	}
}

// TestStatusVocabulary_Has8Entries は、語彙が完了条件どおり8値であることを検証する。
func TestStatusVocabulary_Has8Entries(t *testing.T) {
	if len(StatusVocabulary) != 8 {
		t.Fatalf("len(StatusVocabulary) = %d, want 8", len(StatusVocabulary))
	}
}

// TestStatusVocabulary_MatchesSpecCodesAndLabels は、語彙の全8行が
// docs/features/m1-core.md §状態機械 の表（コード・表示名）と一致することを検証する。
func TestStatusVocabulary_MatchesSpecCodesAndLabels(t *testing.T) {
	want := []StatusVocabEntry{
		{Code: StatusUnclassified, Label: "未分類"},
		{Code: StatusClassified, Label: "分類済"},
		{Code: StatusAwaitingPlanApproval, Label: "計画承認待ち"},
		{Code: StatusInProgress, Label: "着手中"},
		{Code: StatusVerifying, Label: "検証中"},
		{Code: StatusAwaitingCompletionApproval, Label: "完了確認待ち"},
		{Code: StatusDone, Label: "完了"},
		{Code: StatusAwaitingHuman, Label: "人間対応待ち"},
	}
	if len(StatusVocabulary) != len(want) {
		t.Fatalf("len(StatusVocabulary) = %d, want %d", len(StatusVocabulary), len(want))
	}
	for i, w := range want {
		if StatusVocabulary[i] != w {
			t.Errorf("StatusVocabulary[%d] = %+v, want %+v", i, StatusVocabulary[i], w)
		}
	}
}

// TestStatusLabel_ReturnsSpecLabel は、Label がコードに対応する表示名を返すことを検証する。
func TestStatusLabel_ReturnsSpecLabel(t *testing.T) {
	got, ok := StatusDone.Label()
	if !ok {
		t.Fatalf("StatusDone.Label() ok = false, want true")
	}
	if got != "完了" {
		t.Errorf("StatusDone.Label() = %q, want %q", got, "完了")
	}
}

// TestStatusLabel_UnknownCodeReturnsFalse は、語彙に無いコードでは ok=false を返すことを検証する。
func TestStatusLabel_UnknownCodeReturnsFalse(t *testing.T) {
	if _, ok := Status("bogus").Label(); ok {
		t.Errorf("Status(bogus).Label() ok = true, want false")
	}
}
