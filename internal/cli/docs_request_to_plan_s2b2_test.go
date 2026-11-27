package cli

import (
	"strconv"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// このファイルは docs/features/request-to-plan-s2b.md の S2b-2 の「文書」の受入基準（AC-397〜404）を、文書の本文で確かめる。

// AC-397: 用語集の「止まり方」の行に spec_question がある。
func TestDocsS2b2_AC397_GlossaryStopKindRowHasSpecQuestion(t *testing.T) {
	requireAll(t, "the meaning of 止まり方", glossaryRow(t, "止まり方")[1], "`"+string(core.StopKindSpecQuestion)+"`")
}

// AC-398: 「止まり方」の行の数が 11 で、コードの閉集合の数・行に並べた項目の数と合う。
func TestDocsS2b2_AC398_GlossaryStopKindRowCountsEleven(t *testing.T) {
	meaning := glossaryRow(t, "止まり方")[1]
	n := len(core.StopKindValues())
	if n != 11 {
		t.Fatalf("the code has %d stop kinds, want 11", n)
	}
	_, rest, ok := strings.Cut(meaning, strconv.Itoa(n)+" つ（")
	if !ok {
		t.Fatalf("the 止まり方 row does not say %d つ: %s", n, meaning)
	}
	list, _, ok := strings.Cut(rest, "）")
	if !ok {
		t.Fatalf("the list of stop kinds is not closed: %s", rest)
	}
	if items := strings.Split(list, "・"); len(items) != n {
		t.Errorf("the 止まり方 row lists %d items, want %d: %q", len(items), n, items)
	}
}

// AC-399: 「下書きづくり」の行に、残った問いをスーパーバイザーが先に見ることがある。
func TestDocsS2b2_AC399_GlossarySpecDraftingRowSaysSupervisorSeesQuestionsFirst(t *testing.T) {
	requireAll(t, "the meaning of 下書きづくり", glossaryRow(t, "下書きづくり")[1],
		"残った問いは、まずスーパーバイザーが見る")
}

// AC-400: 「仕様づくりの費用」の行の最初の文が、対話の欄の会話と下書きづくりの実行の両方を指す。
func TestDocsS2b2_AC400_GlossarySpecCostRowFirstSentenceCoversBoth(t *testing.T) {
	first, _, ok := strings.Cut(glossaryRow(t, "仕様づくりの費用")[1], "。")
	if !ok {
		t.Fatal("the meaning of 仕様づくりの費用 has no sentence")
	}
	requireAll(t, "the first sentence of 仕様づくりの費用", first, "対話の欄の会話", "下書きづくりの実行")
}

// AC-401: 設計書の下書きづくりの記述に、同時に走らせる数や費用の上限に当たると開始を持ち越すことがある。
func TestDocsS2b2_AC401_ArchSaysStartIsCarriedOver(t *testing.T) {
	sec := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archPlanningSection)
	docLineWith(t, sec, "同じ回りの委譲の段で", "同時に走らせる数", "サイクルの費用の上限", "開始を持ち越す")
}

// AC-402: 同じ記述に、残った問いをスーパーバイザーが先に見ることがある。
func TestDocsS2b2_AC402_ArchSaysSupervisorSeesQuestionsFirst(t *testing.T) {
	sec := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archPlanningSection)
	docLineWith(t, sec, "残った問いは、まずスーパーバイザーが見る", "`"+string(core.StopKindSpecQuestion)+"`")
}

// AC-403: 設計書に、本文を返す口のパスがある。
func TestDocsS2b2_AC403_ArchHasSpecBodyEndpoint(t *testing.T) {
	sec := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archPlanningSection)
	requireAll(t, "the planning section of architecture.md", sec, "/challenges/{id}/spec")
}

// AC-404: flow-port の下書きづくりのフローの節に spec_step_summary がある。
func TestDocsS2b2_AC404_FlowPortSpecDraftSectionHasStepSummaryFact(t *testing.T) {
	sec := sectionBodyByPrefix(t, readRepoDoc(t, "docs", "features", "flow-port.md"), "## 下書きづくりのフロー")
	requireAll(t, "the spec-draft section of flow-port.md", sec, "`spec_step_summary`")
}
