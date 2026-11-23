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
		"区切り S4c-1〔予算の上限と、計画の後にタスクを足すこと〕の範囲も実装済み", "区切り S4c-1 で実装した範囲は", "`task_addition`", "記録と API の値までで、画面に知らせを出すのは区切り S4d-2")
	sec := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), "### 21.2 承認の単位（D23）")
	requireAll(t, "architecture §21.2", sec, "S4c-1 で実装したのは、足したことの記録と API の値まで", "区切り S4d-2 で、記録から作る知らせ")
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

// flywheel の使い方の出力の answer の行は、保留への回答のフラグ（--hold・--option）を載せている（並びはここに固定で書く。コマンドの定義とは照らさない）。
func TestUsage_AnswerLineListsTheHoldAndOptionFlags(t *testing.T) {
	var line string
	for _, l := range strings.Split(usageText, "\n") {
		if strings.HasPrefix(l, "  answer <ID> ") {
			line = l
		}
	}
	for _, want := range []string{"--hold <H-ID>", "--option <識別>", "--answer <a>", "--replan", "--choice", "--text"} {
		if !strings.Contains(line, want) {
			t.Errorf("the answer line of the usage text lacks %q: %q", want, line)
		}
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

// AC-89: architecture.md の §21 の実装済みの範囲と §21.2 の注記に、S4c-2 で実装した範囲がある。
func TestSpec_AC89_ArchitectureHasS4c2Range(t *testing.T) {
	first := archS21First(t)
	requireAll(t, "architecture §21 の冒頭", first,
		"区切り S4c-2〔フォローアップを、開いている課題のタスクに上げる〕の範囲も実装済み", "区切り S4c-2 で実装した範囲は",
		"`followup.promoted_task_id`", "`--raise-limit`", "区切り S4d-2 で、記録から作る知らせとして画面に出す")
	sec := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), "### 21.2 承認の単位（D23）")
	requireAll(t, "architecture §21.2", sec, "S4c-2 で実装済みなのは", "上限の引き上げの承認として扱う")
}

// AC-139: architecture.md の §21 の実装済みの範囲に、区切り S4c-3 と、保留を課題に重ねて持つ旨がある。
func TestSpec_AC139_ArchitectureHasS4c3Range(t *testing.T) {
	first := archS21First(t)
	requireAll(t, "architecture §21 の冒頭", first,
		"区切り S4c-3〔保留中の課題のほかのタスクを進める〕の範囲も実装済み", "区切り S4c-3 で実装した範囲は", "保留を課題に重ねて持つこと",
		"`hold.scope`", "スキーマ版 38", "`flywheel answer --hold <H-ID>`")
}

// AC-140: architecture.md の遷移の説明に、T11 の人間対応待ちからの行がある。
func TestSpec_AC140_ArchitectureHasTheT11RowFromAwaitingHuman(t *testing.T) {
	doc := readRepoDoc(t, "docs", "architecture.md")
	requireAll(t, "architecture の遷移の説明", doc,
		"遷移 T11 に、人間対応待ちからの行を足した", "開いている保留が、どれもタスクを止める保留で、戻り先が着手中",
		"回答 T12 と T20 は、ほかの開いている保留が残っていれば、遷移先を人間対応待ちにする")
}

// AC-141・AC-144・AC-145: 用語集の「保留」が課題か課題の 1 つのタスクが止まっていることに改まり、「タスクを止める保留」「課題を止める保留」が載る。
func TestSpec_AC141_AC144_AC145_GlossaryHasHoldScopes(t *testing.T) {
	doc := readRepoDoc(t, "docs", "glossary.md")
	requireAll(t, "glossary", doc,
		"| 保留 | 課題か、課題の 1 つのタスクが、", "| タスクを止める保留 |", "| 課題を止める保留 |")
}

// AC-142: core-redesign.md の保留中の課題の決まり（S3b）の所に、S4c-3 で改めた旨がある。
func TestSpec_AC142_CoreRedesignS3bHoldRuleNotesS4c3(t *testing.T) {
	doc := readRepoDoc(t, "docs", "features", "core-redesign.md")
	requireAll(t, "core-redesign", doc,
		"保留中の課題では、新しいタスクを起動せず、上げる依頼も始めない。", "S4c-3 で改めた: 「保留中の課題」を、課題を止める保留が開いている課題に限る。")
}

// AC-143: 案内の文（web/src/guidance.json）の回答のコマンドに、--hold の形が載っている。
func TestSpec_AC143_GuidanceAnswerCommandHasTheHoldForm(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "web", "src", "guidance.json"))
	if err != nil {
		t.Fatal(err)
	}
	requireAll(t, "guidance.json", string(data), `"flywheel answer <C-ID> --workspace <パス> --hold <H-ID> --answer <回答>"`)
}

// AC-146: 対話の欄の指示文に、開いている保留が 2 つ以上のときは、どの保留への回答かを確かめてから --hold を付けて記録する旨がある。
func TestSpec_AC146_ChatPromptSaysToConfirmTheHoldBeforeRecording(t *testing.T) {
	requireAll(t, "chat.md", promptText(t, "chat.md"),
		"開いている保留が 2 つ以上ある課題への回答は", "どの保留", "オーナーに確かめてから", "`answer --hold <H-ID>` を付けて記録する")
}
