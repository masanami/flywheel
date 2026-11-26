package invoker

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// このファイルは docs/features/request-to-plan-s2b.md の、計画を作る判断点の指示文の基準（下書きづくりへ回す判定）を、
// 文書を読む試験で確かめる。禁止語の検査は internal/cli の TestRealPrompts_ContainNoForbiddenTerms が持つ。

// j2JudgmentLine は j2.md の節「判断」のうち prefix で始まる行を返す。
func j2JudgmentLine(t *testing.T, prefix string) string {
	t.Helper()
	return promptLine(t, promptSection(t, j2Instructions(t), "## 判断"), prefix)
}

// 「読むもの」は、core が置く区画を名前で指し、区画の 2 つの形を説明する。
func TestInstructions_J2DescribesTheSpecDraftAvailabilitySection(t *testing.T) {
	line := promptLine(t, promptSection(t, j2Instructions(t), "## 読むもの"), "- 「"+core.SpecDraftSectionName+"」:")
	requirePhrases(t, "the bullet of the spec draft section", line, "「回せる」", "対象にできるリポジトリ", "「回せない」", "理由")
}

// 判断の選択肢の 4 つ目は下書きづくりへ回すことで、出力の欄 spec_draft の書き方を持つ。spec_draft は 4 だけで書く。
func TestInstructions_J2DescribesTheSpecDraftOutput(t *testing.T) {
	sec := promptSection(t, j2Instructions(t), "## 判断")
	requirePhrases(t, "the section 判断", sec, "次の 4 つのうち、どれか 1 つだけを選ぶ", "2〜4 では、ほかの項目は空にしてよい", "spec_draft は 4 でだけ書く")
	line := j2JudgmentLine(t, "4. 下書きづくりへ回す")
	requirePhrases(t, "the spec draft choice", line,
		"計画は立てない", "spec_draft", "（repo。区画「"+core.SpecDraftSectionName+"」に並ぶ名前から 1 つ）", "base_path",
		"（reason。空白だけは不正。2000 文字以内）", "リポジトリの中の相対のパス", "500 文字", "新しい文書なら null")
}

// AC-398・AC-399: 回すものは 2 つ。
func TestInstructions_J2RoutesNewMultiTaskFeaturesAndSpecBehaviorChanges(t *testing.T) {
	line := j2JudgmentLine(t, "   - 回すもの:")
	t.Run("AC-398", func(t *testing.T) {
		requirePhrases(t, "the routed list", line, "新しい機能で、複数のタスクに分かれる大きさ")
	})
	t.Run("AC-399", func(t *testing.T) {
		requirePhrases(t, "the routed list", line, "リポジトリにある仕様の文書が受け持つ振る舞いを変える")
	})
}

// AC-400・AC-401・AC-402: 回さないものは 3 つ。
func TestInstructions_J2DoesNotRouteSmallOrBehaviorPreservingChanges(t *testing.T) {
	line := j2JudgmentLine(t, "   - 回さないもの:")
	t.Run("AC-400", func(t *testing.T) { requirePhrases(t, "the excluded list", line, "文書だけの変更") })
	t.Run("AC-401", func(t *testing.T) { requirePhrases(t, "the excluded list", line, "振る舞いを変えない直し") })
	t.Run("AC-402", func(t *testing.T) {
		requirePhrases(t, "the excluded list", line, "1〜2 ファイルの小さな不具合の直し")
	})
}

// AC-403: 回すかに迷ったら回す。
func TestInstructions_J2RoutesWhenInDoubt(t *testing.T) {
	requirePhrases(t, "the doubt rule", j2JudgmentLine(t, "   - 回すかどうか"), "迷ったら回す")
}

// AC-404: 区画が「回せない」なら回さない。
func TestInstructions_J2DoesNotRouteWhenUnavailable(t *testing.T) {
	requirePhrases(t, "the availability rule", j2JudgmentLine(t, "   - 区画「"+core.SpecDraftSectionName+"」"),
		"「回せない」のときは回さない")
}

// AC-405: 既存の文書の直しなら、置き換える元の文書の場所を書く。
func TestInstructions_J2WritesTheBaseDocumentOfAnExistingSpec(t *testing.T) {
	requirePhrases(t, "the spec draft choice", j2JudgmentLine(t, "4. 下書きづくりへ回す"),
		"既存の文書の直しなら、「置き換える元の文書の場所」を")
}

// 区画「受入基準」に下書きを作ったリポジトリの行があるときは、入れるタスクの対象リポジトリをその名前にする
// （違うと core が不正な出力にする）。
func TestInstructions_J2LandsTheSpecDraftInTheRecordedRepo(t *testing.T) {
	requirePhrases(t, "the spec draft rule", j2SpecDraftLine(t),
		"「対象のリポジトリ」の行があるときは、lands_spec を真にするタスクの対象リポジトリをその名前にする", "違うと不正")
}

// 「足す」の形の呼び出しは、判断の選択をしない。
func TestInstructions_J2ExpandCallSkipsAllFourChoices(t *testing.T) {
	requirePhrases(t, "the section 足す形の呼び出し", promptSection(t, j2Instructions(t), "## 足す形の呼び出し"),
		"上の「判断」の 4 つの選択をせず")
}

// AC-409: スーパーバイザーの指示文と、その分量の上限は、この区切りの前と変わらない。
// 後の区切りで sv.md を変えるときは、この試験の値を書き換える。
func TestSupervisorPrompt_IsUnchangedBySpecDraftRouting(t *testing.T) {
	body, err := Instructions(core.JudgmentSV)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	const want = "404223857c445f18dbcf3e0d76134c8f8548428d5960d4e1457377f3cb0cbc0c"
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Errorf("sv.md sha256 = %s, want %s", got, want)
	}
	if limit := judgmentPromptLimit["sv.md"]; limit != 6144 {
		t.Errorf("sv.md limit %d bytes, want 6144", limit)
	}
}
