package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// このファイルは、区切り S4c-1 の文書の受入基準（AC-225〜AC-229・AC-236）を、文書の本文と指示文で確かめる。
// 指示文の大きさの検査（AC-230）は internal/invoker の CheckPromptSizes の試験が担う。

func promptText(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "invoker", "prompts", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// AC-225: architecture.md の §21 の実装済みの範囲と §21.2 の注記に、S4c-1 で実装した範囲（記録と API の値まで。知らせる口は S4d-2）がある。
func TestSpec_AC225_ArchitectureHasS4c1Range(t *testing.T) {
	first := archS21First(t)
	requireAll(t, "architecture §21 の冒頭", first,
		"区切り S4c-1〔予算の上限と、計画の後にタスクを足すこと〕の範囲も実装済み", "区切り S4c-1 で実装した範囲は", "`task_addition`", "記録と API の値までで、知らせる口は区切り S4d-2")
	sec := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), "### 21.2 承認の単位（D23）")
	requireAll(t, "architecture §21.2", sec, "S4c-1 で実装済みなのは、足したことの記録と API の値まで", "知らせる口は区切り S4d-2")
}

// AC-226: 用語集に、「見込み」「上限」「足す依頼」の行があり、枠の合計を指す「課題の上限」は見込みの旧称として残る。
func TestSpec_AC226_GlossaryHasEstimateLimitAndAddition(t *testing.T) {
	for _, term := range []string{"見込み", "上限", "足す依頼"} {
		if cols := glossaryRow(t, term); strings.TrimSpace(cols[1]) == "" {
			t.Errorf("AC-226: the row for %q has no definition", term)
		}
	}
	if !strings.Contains(glossaryRow(t, "見込み")[1], "旧称: 課題の上限") {
		t.Error("AC-226: the 見込み row does not note the former name")
	}
}

// AC-227: 対話の欄の指示文と案内の文（flywheel の使い方の出力）に、オーナーの発言を選択肢の識別に直して記録する旨の文がある。
func TestSpec_AC227_ChatPromptAndHelpSayToRecordTheOptionIdentifier(t *testing.T) {
	const want = "オーナーの発言を、選択肢の識別に直して"
	if !strings.Contains(promptText(t, "chat.md"), want) {
		t.Errorf("AC-227: chat.md lacks %q", want)
	}
	if !strings.Contains(usageText, want) {
		t.Errorf("AC-227: the usage text lacks %q", want)
	}
}

// AC-228: 同じ 2 か所に、どの選択肢か決められないときは記録せずに確かめる旨の文がある。
func TestSpec_AC228_ChatPromptAndHelpSayToAskWhenTheOptionIsUnclear(t *testing.T) {
	for where, body := range map[string]string{"chat.md": promptText(t, "chat.md"), "usage": usageText} {
		if !strings.Contains(body, "どの選択肢か決められないときは、記録せずに") {
			t.Errorf("AC-228: %s lacks the sentence", where)
		}
	}
}

// AC-229: j2.md に、区画「呼び出しの形」の値で読む節（「足す形の呼び出し」）がある。
func TestSpec_AC229_J2PromptHasTheCallShapeSection(t *testing.T) {
	sec := sectionBody(t, promptText(t, "j2.md"), "## 足す形の呼び出し")
	requireAll(t, "j2.md の足す形の呼び出し", sec, "区画「呼び出しの形」が「足す」のときだけ")
	body := promptText(t, "j2.md")
	requireAll(t, "j2.md の計画の項目", body, "上限（budget_limit_usd）と理由（budget_limit_reason）", "続きを決めるタスク（expand_after）")
}

// AC-236: j2.md の「足す」の形の指示に、やらないことに触れるタスクは足さない・迷ったら ask・判断や人の作業が要る点が出たら ask がある。
func TestSpec_AC236_J2PromptHasTheAddRules(t *testing.T) {
	sec := sectionBody(t, promptText(t, "j2.md"), "## 足す形の呼び出し")
	requireAll(t, "j2.md の足す形の呼び出し", sec,
		"「やらないこと」に触れるタスクは足さない", "方針の外かどうか迷ったら ask にする", "判断や人の作業が要る点が出たら ask にする")
}
