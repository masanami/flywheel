package invoker

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// このファイルは docs/features/request-to-plan.md の、計画を作る判断点の指示文と対話の欄の指示文の基準を、
// 文書を読む試験で確かめる。

// promptSection は body の見出し heading の節（次の「## 」の見出しの前まで）を返す。
func promptSection(t *testing.T, body, heading string) string {
	t.Helper()
	_, rest, ok := strings.Cut(body, "\n"+heading+"\n")
	if !ok {
		t.Fatalf("the prompt lacks the section %q", heading)
	}
	sec, _, _ := strings.Cut(rest, "\n## ")
	return sec
}

// promptLine は sec のうち prefix で始まる行を返す。
func promptLine(t *testing.T, sec, prefix string) string {
	t.Helper()
	for _, l := range strings.Split(sec, "\n") {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	t.Fatalf("no line starts with %q", prefix)
	return ""
}

func requirePhrases(t *testing.T, where, body string, phrases ...string) {
	t.Helper()
	for _, p := range phrases {
		if !strings.Contains(body, p) {
			t.Errorf("%s lacks %q", where, p)
		}
	}
}

func j2Instructions(t *testing.T) string {
	t.Helper()
	got, err := Instructions(core.JudgmentJ2)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// 分け方は 3 つの観点で比べてから 1 つに決め、決めた分け方と理由を方針に書く。
func TestInstructions_J2ComparesTaskSplitsFromThreeViewpoints(t *testing.T) {
	line := promptLine(t, promptSection(t, j2Instructions(t), "## 選び方の指針"), "- タスクへの分け方は")
	requirePhrases(t, "the task split rule", line,
		"依存を最小にする", "機能を縦に切る", "層ごとに分ける",
		"分け方を比べ", "1 つに決め", "3 つの観点の名前", "決めた分け方と理由", "要約（方針）に書く")
}

// 「読むもの」は区画「受入基準」を説明する。
func TestInstructions_J2DescribesTheAcceptanceCriteriaSection(t *testing.T) {
	line := promptLine(t, promptSection(t, j2Instructions(t), "## 読むもの"), "- 「受入基準」:")
	requirePhrases(t, "the 「受入基準」 bullet", line, "仕様の文書", "あるときだけ", "識別", "先頭 80 文字")
}

// covers には区画の識別を写して作らず、受け持たない基準は deferred_criteria に理由つきで書く。
func TestInstructions_J2DescribesCoversAndDeferredCriteria(t *testing.T) {
	line := promptLine(t, promptSection(t, j2Instructions(t), "## 判断"), "   - 受け持つ受入基準")
	requirePhrases(t, "the covers rule", line,
		"covers", "そのまま写す", "作らない", "deferred_criteria", "理由（reason", "2000 文字以内",
		"どれにも無い基準があると不正", "区画に無い識別", "区画が無いときは",
		"済みのタスクの受け持ちは件数しか出ない", "確かめられない基準は covers か deferred_criteria に入れる")
}

// 仕様の文書は読むだけで、直しはタスクにする。
func TestInstructions_J2OnlyReadsTheSpecDocument(t *testing.T) {
	line := promptLine(t, promptSection(t, j2Instructions(t), "## 判断"), "   - 仕様の文書")
	requirePhrases(t, "the spec document rule", line, "読むだけ", "タスクにする以外の形で直さない")
}

// 「足す」の形の呼び出しの節も covers を述べる。
func TestInstructions_J2ExpandSectionDescribesCovers(t *testing.T) {
	sec := promptSection(t, j2Instructions(t), "## 足す形の呼び出し")
	requirePhrases(t, "the section 足す形の呼び出し", sec, "covers", "区画「承認した計画」に出る識別だけをそのまま写す", "推測で作らず")
}

// j2.md の上限は、本文に余白を足した値以下で、19 KiB を超えない（本文が上限以下であることは
// TestCheckPromptSizes_RealEmbedIsWithinLimits、余白の下限は TestJ2Prompt_HasHeadroomBelowLimit）。
func TestJ2Prompt_LimitIsRaisedOnlyByTheAddedText(t *testing.T) {
	size := len(j2Instructions(t))
	limit := judgmentPromptLimit["j2.md"]
	if limit > size+j2HeadroomBytes {
		t.Errorf("j2.md limit %d bytes, want <= %d (size %d + %d)", limit, size+j2HeadroomBytes, size, j2HeadroomBytes)
	}
	if limit > 19456 {
		t.Errorf("j2.md limit %d bytes, want <= 19456", limit)
	}
}

// 対話の欄は、仕様づくりのスキルの後に Issue を作らず、仕様の文書の場所を課題へ渡す。
func TestChatInstructions_AfterTheSpecSkill(t *testing.T) {
	ins, err := ChatInstructions()
	if err != nil {
		t.Fatal(err)
	}
	sec := promptSection(t, ins, "## 仕様づくりの後")
	requirePhrases(t, "chat.md の仕様づくりの後", sec,
		"仕様づくりのスキル", "終わりの案内", "Issue を作らない", "Issue に分けるスキルを呼ばない", "--spec")
}

func TestChatPrompt_LimitIsRaisedWithinTheCap(t *testing.T) {
	fsys, err := promptsSubFS()
	if err != nil {
		t.Fatal(err)
	}
	info, err := fs.Stat(fsys, chatPromptFile)
	if err != nil {
		t.Fatal(err)
	}
	limit := judgmentPromptLimit[chatPromptFile]
	if limit <= 4682 || limit > 5632 {
		t.Errorf("chat.md limit %d bytes, want > 4682 and <= 5632", limit)
	}
	if info.Size() > int64(limit) {
		t.Errorf("chat.md is %d bytes, exceeds the limit %d", info.Size(), limit)
	}
}
