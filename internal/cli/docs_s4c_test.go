package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// このファイルは、区切り S4c の受入基準のうち、指示文・使い方の出力・画面の案内の文を読むものを確かめる。
// 指示文の大きさの検査（AC-230）は internal/invoker の CheckPromptSizes の試験が担う。

func promptText(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "invoker", "prompts", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
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
