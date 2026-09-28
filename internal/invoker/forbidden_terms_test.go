package invoker

import "testing"

// TestFindForbiddenTerms_ASCIITerm_ExactWordMatches は、ASCII の語が
// 独立した単語として現れると検出されることを検証する
// （§決定済みの設計「ASCIIの語は…前後が[A-Za-z0-9_]でない位置での一致」）。
func TestFindForbiddenTerms_ASCIITerm_ExactWordMatches(t *testing.T) {
	got := FindForbiddenTerms("これは classify の説明である。", []string{"classify"})
	if len(got) != 1 || got[0] != "classify" {
		t.Errorf("FindForbiddenTerms(...) = %v, want [\"classify\"]", got)
	}
}

// TestFindForbiddenTerms_ASCIITerm_SubstringOfLargerWordDoesNotMatch は、
// 禁止語が別の単語の内部（例: awaiting_plan_approval の中の plan）に
// 現れても検出しないことを検証する（AC の例そのもの）。
func TestFindForbiddenTerms_ASCIITerm_SubstringOfLargerWordDoesNotMatch(t *testing.T) {
	got := FindForbiddenTerms("状態は awaiting_plan_approval である。", []string{"plan"})
	if len(got) != 0 {
		t.Errorf("FindForbiddenTerms(...) = %v, want none (plan is inside a larger word)", got)
	}
}

// TestFindForbiddenTerms_ASCIITerm_SingleLetterSizeMatchesWhenStandalone は、
// S単独・Sサイズのように前後が非単語文字なら1文字の禁止語も検出することを
// 検証する（AC の例そのもの）。
func TestFindForbiddenTerms_ASCIITerm_SingleLetterSizeMatchesWhenStandalone(t *testing.T) {
	if got := FindForbiddenTerms("サイズは S である。", []string{"S"}); len(got) != 1 {
		t.Errorf("FindForbiddenTerms(standalone S) = %v, want [\"S\"]", got)
	}
	if got := FindForbiddenTerms("Sサイズを選ぶ。", []string{"S"}); len(got) != 1 {
		t.Errorf("FindForbiddenTerms(Sサイズ) = %v, want [\"S\"]", got)
	}
}

// TestFindForbiddenTerms_ASCIITerm_CaseSensitive は、ASCII の語の一致が
// 大文字小文字を区別することを検証する。
func TestFindForbiddenTerms_ASCIITerm_CaseSensitive(t *testing.T) {
	got := FindForbiddenTerms("この plan は小文字である。", []string{"PLAN"})
	if len(got) != 0 {
		t.Errorf("FindForbiddenTerms(...) = %v, want none (case must match)", got)
	}
}

// TestFindForbiddenTerms_NonASCIITerm_SubstringMatches は、非ASCII
// （状態の表示名等）が部分文字列として一致することを検証する
// （§決定済みの設計「非ASCII（表示名）は部分文字列一致」）。
func TestFindForbiddenTerms_NonASCIITerm_SubstringMatches(t *testing.T) {
	got := FindForbiddenTerms("この課題はまだ完了していない予定である。", []string{"完了"})
	if len(got) != 1 || got[0] != "完了" {
		t.Errorf("FindForbiddenTerms(...) = %v, want [\"完了\"]", got)
	}
}

// TestFindForbiddenTerms_NonASCIITerm_DoesNotMatchWithoutFullSubstring は、
// ラベル全体（例: 分類済）ではなく一部（分類）だけなら一致しないことを
// 検証する（分類済 は StatusClassified のラベルで forbidden、分類 単体は
// forbidden ではない、という区別）。
func TestFindForbiddenTerms_NonASCIITerm_DoesNotMatchWithoutFullSubstring(t *testing.T) {
	got := FindForbiddenTerms("この課題を分類する。", []string{"分類済"})
	if len(got) != 0 {
		t.Errorf("FindForbiddenTerms(...) = %v, want none (分類 is not 分類済)", got)
	}
}

// TestFindForbiddenTerms_MoneyPattern_DetectsAllExampleForms は、
// $ か USD を伴う数のすべての例示された書き方を検出することを検証する
// （§決定済みの設計「金額は$かUSD(大文字小文字無視)を伴う数」）。
func TestFindForbiddenTerms_MoneyPattern_DetectsAllExampleForms(t *testing.T) {
	cases := []string{"$5", "$ 5", "5 USD", "5USD", "USD 5", "1.5 usd"}
	for _, c := range cases {
		got := FindForbiddenTerms("上限は"+c+"である。", nil)
		if len(got) == 0 {
			t.Errorf("FindForbiddenTerms(%q) found no money amount, want a hit", c)
		}
	}
}

// TestFindForbiddenTerms_NoMoneyPattern_PlainNumberDoesNotMatch は、
// $ も USD も伴わない単なる数は検出しないことを検証する。
func TestFindForbiddenTerms_NoMoneyPattern_PlainNumberDoesNotMatch(t *testing.T) {
	got := FindForbiddenTerms("優先度はP1、件数は5件である。", nil)
	if len(got) != 0 {
		t.Errorf("FindForbiddenTerms(plain number) = %v, want none", got)
	}
}

// TestFindForbiddenTerms_CleanContentReturnsNoHits は、禁止語も金額も
// 含まない内容では何も検出しないことを検証する。
func TestFindForbiddenTerms_CleanContentReturnsNoHits(t *testing.T) {
	got := FindForbiddenTerms("この判断点の指示文の本文は、後続のチケットで書く。", []string{"classify", "完了", "plan"})
	if len(got) != 0 {
		t.Errorf("FindForbiddenTerms(clean content) = %v, want none", got)
	}
}

// TestForbiddenTerms_CombinesAllSourcesAndDedups は ForbiddenTerms が
// ForbiddenSources の全フィールドを1つの一覧へ合成し、重複を除くことを
// 検証する（§決定済みの設計「生成関数は入力を引数で受け取る純粋関数にする」）。
func TestForbiddenTerms_CombinesAllSourcesAndDedups(t *testing.T) {
	src := ForbiddenSources{
		StateCodesAndLabels: []string{"unclassified", "未分類"},
		Subcommands:         []string{"classify", "plan"},
		ConfigKeys:          []string{"cycle_budget_usd", "S"},
		OutputClosedValues:  []string{"mine", "plan"}, // "plan" は Subcommands とも重複
	}
	got := ForbiddenTerms(src)

	want := map[string]bool{
		"unclassified": true, "未分類": true,
		"classify": true, "plan": true,
		"cycle_budget_usd": true, "S": true,
		"mine": true,
	}
	if len(got) != len(want) {
		t.Fatalf("ForbiddenTerms(...) = %v (len %d), want %d unique terms", got, len(got), len(want))
	}
	seen := map[string]bool{}
	for _, term := range got {
		if seen[term] {
			t.Errorf("ForbiddenTerms(...) contains duplicate %q", term)
		}
		seen[term] = true
		if !want[term] {
			t.Errorf("ForbiddenTerms(...) contains unexpected term %q", term)
		}
	}
}

// TestForbiddenTerms_AddingAStateVocabValuePropagates は AC「禁止語の一覧は…
// コードの定義から生成される（状態の語彙に値を1つ足したテスト用の定義で、
// 一覧にその値が現れることで検証する）」を、ForbiddenSources
// に直接テスト用の定義を渡す形で検証する（core.StatusVocabulary 自体は
// 変更しない。呼び出し元が都度コードの定義から組み立てる、という契約の検証）。
func TestForbiddenTerms_AddingAStateVocabValuePropagates(t *testing.T) {
	extended := []string{"unclassified", "classified", "test_added_state_value"}
	got := ForbiddenTerms(ForbiddenSources{StateCodesAndLabels: extended})

	found := false
	for _, term := range got {
		if term == "test_added_state_value" {
			found = true
		}
	}
	if !found {
		t.Errorf("ForbiddenTerms(...) = %v, want it to contain the newly added state vocabulary value", got)
	}
}
