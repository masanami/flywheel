package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// このファイルは docs/features/request-to-plan.md の「文書」の受入基準を、文書の本文で確かめる。
// 指示文の分量の上限の記述は internal/invoker、「着手しなかった」の理由の一覧は
// cycle_closedsets_test.go の閉集合の照合が確かめる。

const archPlanningSection = "### 計画を作る判断点（J2）: 分解は 1 回・受入基準の網羅を検査する"

// 判断点の出力の表の、計画を作る判断点の行に、受け持ちの 2 つの項目がある。
func TestDocsRequestToPlan_M3J2OutputRowHasCoverageFields(t *testing.T) {
	row := docTableRow(t, readRepoDoc(t, "docs", "features", "m3-invoker-delegation.md"), "| J2 |")
	requireAll(t, "the J2 row of the m3 judgment outputs", row, "`covers`", "`deferred_criteria`")
}

// 計画を作る判断点の節に、受入基準の網羅の検査がある。不正な出力の理由の語は、実装が理由の文に使う語と同じ。
func TestDocsRequestToPlan_ArchPlanningSectionDescribesCoverageCheck(t *testing.T) {
	sec := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archPlanningSection)
	requireAll(t, "the coverage check in architecture.md", sec,
		"受入基準の網羅の検査", "区画「"+core.AcceptanceCriteriaSectionLabel+"」", "`covers`", "`deferred_criteria`",
		"`criteria not covered`", "`unknown criterion`", "`duplicate criterion`",
		"`"+string(core.NotStartedSpecUnreadable)+"`", "request-to-plan.md")
}

// 同じ節に、分解は計画を作る判断点の 1 回だけである旨がある。
func TestDocsRequestToPlan_ArchPlanningSectionSaysDecompositionHappensOnce(t *testing.T) {
	sec := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archPlanningSection)
	requireAll(t, "the single decomposition in architecture.md", sec, "分解は計画を作る判断点の 1 回だけ")
}

// 用語集に「受入基準」と「仕様の文書」の行がある。
func TestDocsRequestToPlan_GlossaryHasAcceptanceCriteriaAndSpecDocument(t *testing.T) {
	for term, want := range map[string]string{"受入基準": "`deferred_criteria`", "仕様の文書": "`challenge.spec_path`"} {
		cols := glossaryRow(t, term)
		if !strings.Contains(cols[3], want) {
			t.Errorf("the code name of %q = %q, want %q", term, cols[3], want)
		}
	}
}

// CLAUDE.md の internal/core の行に、仕様の文書から受入基準を読むことがある。
func TestDocsRequestToPlan_ClaudeMdCoreRowReadsAcceptanceCriteria(t *testing.T) {
	row := docTableRow(t, readRepoDoc(t, "CLAUDE.md"), "| `internal/core` |")
	requireAll(t, "the internal/core row of CLAUDE.md", row, "仕様の文書", "受入基準を読む")
}
