package cli

import (
	"strconv"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// このファイルは docs/features/request-to-plan-s2b.md の S2b-2 の「文書」の受入基準（AC-397〜404）のうち、コードの値と文書を照らすものを確かめる。

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

// AC-402: 同じ記述に、残った問いをスーパーバイザーが先に見ることがある。
func TestDocsS2b2_AC402_ArchSaysSupervisorSeesQuestionsFirst(t *testing.T) {
	sec := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archPlanningSection)
	docLineWith(t, sec, "残った問いは、まずスーパーバイザーが見る", "`"+string(core.StopKindSpecQuestion)+"`")
}
