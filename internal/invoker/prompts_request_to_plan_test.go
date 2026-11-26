package invoker

import (
	"fmt"
	"os"
	"path/filepath"
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
		"どれにも無い基準があると不正", "区画に無い識別", "区画が無いときは")
}

// 受け持ち済みの基準は、済みの印で見分け、covers にも deferred_criteria にも書かなくてよい。
func TestInstructions_J2DescribesTheDoneMark(t *testing.T) {
	line := promptLine(t, promptSection(t, j2Instructions(t), "## 判断"), "   - 受け持つ受入基準")
	requirePhrases(t, "the covers rule", line,
		"印「済み」の付いた基準は受け持ち済み", "済みの印の基準は covers にも deferred_criteria にも書かなくてよい")
	for _, old := range []string{"件数しか出ない", "確かめられない基準", "区画「承認した計画」に出る識別"} {
		if strings.Contains(j2Instructions(t), old) {
			t.Errorf("j2.md still contains %q", old)
		}
	}
}

// 仕様の文書は読むだけで、直しはタスクにする。
func TestInstructions_J2OnlyReadsTheSpecDocument(t *testing.T) {
	line := promptLine(t, promptSection(t, j2Instructions(t), "## 判断"), "   - 仕様の文書")
	requirePhrases(t, "the spec document rule", line, "読むだけ", "タスクにする以外の形で直さない")
}

// 「足す」の形の呼び出しの節は、区画「受入基準」を読み、covers の識別をそこから写すことを述べる。
func TestInstructions_J2ExpandSectionDescribesCovers(t *testing.T) {
	sec := promptSection(t, j2Instructions(t), "## 足す形の呼び出し")
	requirePhrases(t, "the section 足す形の呼び出し", sec, "「受入基準」（あるときだけ）", "covers には、区画「受入基準」の識別をそのまま写す", "区画が無ければ空")
}

// j2SpecDraftLine は j2.md の、下書きをリポジトリへ入れるタスクの決め方の行。
func j2SpecDraftLine(t *testing.T) string {
	t.Helper()
	return promptLine(t, promptSection(t, j2Instructions(t), "## 判断"), "   - 下書きを入れるタスク")
}

// AC-115: 印「下書き」があるときは、下書きをリポジトリへ入れるタスクを 1 件だけ決めて lands_spec を真にする。
func TestInstructions_J2DecidesExactlyOneTaskThatLandsTheSpecDraft(t *testing.T) {
	requirePhrases(t, "the spec draft rule", j2SpecDraftLine(t),
		"印「下書き」があるとき", "下書きをリポジトリへ入れるタスクを 1 件だけ決め", "lands_spec を真にする")
}

// AC-116: タスクが 1 件の計画では、そのタスクが文書を入れることと実装の両方を行う。
func TestInstructions_J2TheOnlyTaskLandsTheSpecAndImplements(t *testing.T) {
	requirePhrases(t, "the spec draft rule", j2SpecDraftLine(t),
		"タスクが 1 件の計画では、そのタスクが文書を入れることと実装の両方を行う")
}

// AC-117: タスクが 2 件以上の計画では、文書を入れるタスクを最初に置き、ほかのタスクの前提にする。
func TestInstructions_J2PutsTheSpecLandingTaskFirstAsAPrerequisite(t *testing.T) {
	requirePhrases(t, "the spec draft rule", j2SpecDraftLine(t),
		"2 件以上の計画では、文書を入れるタスクを最初に置き", "ほかのすべてのタスクの前提にする")
}

// AC-148（Issue #1343）: 区画「受入基準」に「リポジトリへ入れた」の行があるときは、どのタスクの lands_spec も真にしない。
// 下書きでない課題（印が無い）でも同じ（計画の検査がどちらも不正にする）。
func TestInstructions_J2MarksNoTaskWithoutADraftOrAfterItIsLanded(t *testing.T) {
	requirePhrases(t, "the spec draft rule", j2SpecDraftLine(t),
		"区画「受入基準」に「リポジトリへ入れた」の行があるときと、印が無いときは、どのタスクの lands_spec も真にしない")
}

// AC-149（Issue #1343）: 下書きを入れた後かどうかを、前の計画の本文から見分けさせない。
func TestInstructions_J2DoesNotReadTheLandingFromThePreviousPlanBody(t *testing.T) {
	if old := "前の計画の本文の「リポジトリへ入れるのは」"; strings.Contains(j2Instructions(t), old) {
		t.Errorf("j2.md still contains %q", old)
	}
}

// AC-118・AC-407: j2.md の上限は、本文のバイト数に 300 を足した値。
func TestJ2Prompt_LimitIsTheBodySizePlusTheHeadroom(t *testing.T) {
	size := len(j2Instructions(t))
	if limit := judgmentPromptLimit["j2.md"]; limit != size+j2HeadroomBytes {
		t.Errorf("j2.md limit %d bytes, want %d (size %d + %d)", limit, size+j2HeadroomBytes, size, j2HeadroomBytes)
	}
}

// AC-408（docs/features/request-to-plan-s2b.md。S2a の天井 20 KiB を置き換える）: j2.md の上限は 21 KiB を超えない。
func TestJ2Prompt_LimitIsWithinTheCap(t *testing.T) {
	if limit := judgmentPromptLimit["j2.md"]; limit > 21504 {
		t.Errorf("j2.md limit %d bytes, want <= 21504", limit)
	}
}

// docs/features/m3-invoker-delegation.md の分量の記述は、j2.md のいまの上限を書く。
func TestJ2Prompt_LimitIsDocumentedInTheM3Spec(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "features", "m3-invoker-delegation.md"))
	if err != nil {
		t.Fatal(err)
	}
	line := promptLine(t, string(data), "- [ ] 指示文の分量の上限をテストで固定する")
	requirePhrases(t, "the size limit line of the m3 spec", line, fmt.Sprintf("J2: %d バイト", judgmentPromptLimit["j2.md"]))
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

// chatAfterTheSpecSkill は chat.md の節「仕様づくりの後」を返す。
func chatAfterTheSpecSkill(t *testing.T) string {
	t.Helper()
	ins, err := ChatInstructions()
	if err != nil {
		t.Fatal(err)
	}
	return promptSection(t, ins, "## 仕様づくりの後")
}

// AC-121: 仕様の文書は、対象のリポジトリではなく下書きの置き場に書く。
func TestChatInstructions_WritesTheSpecUnderTheDraftDirectory(t *testing.T) {
	line := promptLine(t, chatAfterTheSpecSkill(t), "- 仕様の文書は")
	requirePhrases(t, "chat.md の下書きの置き場", line,
		"対象のリポジトリではなく", "下書きの置き場（`.flywheel/specs/` の下）に書く")
}

// AC-122: 仕様づくりのスキルの保存先は、下書きの置き場にする。
func TestChatInstructions_SetsTheSaveLocationOfTheSpecSkillToTheDraftDirectory(t *testing.T) {
	requirePhrases(t, "chat.md の仕様づくりの後", chatAfterTheSpecSkill(t),
		"仕様づくりのスキルを使うときは、「保存先」を下書きの置き場にする")
}

// AC-123: 既存の文書を直すときは、全文を写してから直し、--spec-base を付ける。
func TestChatInstructions_CopiesTheWholeDocumentAndSetsSpecBase(t *testing.T) {
	line := promptLine(t, chatAfterTheSpecSkill(t), "- 既存の文書を直すとき")
	requirePhrases(t, "chat.md の既存の文書の直し", line,
		"全文を写してから直し", "`--spec-base <パス>`", "置き換える元の文書", "リポジトリの中の相対のパス")
}

// AC-124: 課題を作るときは、--spec で下書きの場所を付ける。
func TestChatInstructions_SetsSpecToTheDraftWhenCreatingTheChallenge(t *testing.T) {
	line := promptLine(t, chatAfterTheSpecSkill(t), "- 仕様の文書から課題を作るとき")
	requirePhrases(t, "chat.md の課題の作成", line,
		"`flywheel create`", "`--spec <下書きのパス>`", "ワークスペースから見た相対のパス")
}

// AC-125: 仕様づくりのスキルの案内に従って Issue を作らない案内が残る。
func TestChatInstructions_KeepsTheRuleNotToCreateAnIssue(t *testing.T) {
	requirePhrases(t, "chat.md の仕様づくりの後", chatAfterTheSpecSkill(t),
		"仕様づくりのスキルの終わりの案内に従わず、Issue を作らない")
}

// AC-126: Issue に分けるスキルを呼ばない案内が残る。
func TestChatInstructions_KeepsTheRuleNotToCallTheSplittingSkill(t *testing.T) {
	requirePhrases(t, "chat.md の仕様づくりの後", chatAfterTheSpecSkill(t), "Issue に分けるスキルを呼ばない")
}

// AC-127: chat.md の上限は、前の値（5,060 バイト）より大きく、本文のバイト数以上。
func TestChatPrompt_LimitIsRaisedAndCoversTheBody(t *testing.T) {
	ins, err := ChatInstructions()
	if err != nil {
		t.Fatal(err)
	}
	limit := judgmentPromptLimit[chatPromptFile]
	if limit <= 5060 || limit < len(ins) {
		t.Errorf("chat.md limit %d bytes, want > 5060 and >= %d", limit, len(ins))
	}
}

// AC-128: chat.md の上限は 5.5 KiB を超えない。
func TestChatPrompt_LimitIsWithinTheCap(t *testing.T) {
	if limit := judgmentPromptLimit[chatPromptFile]; limit > 5632 {
		t.Errorf("chat.md limit %d bytes, want <= 5632", limit)
	}
}
