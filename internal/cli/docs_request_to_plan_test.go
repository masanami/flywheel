package cli

import (
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// このファイルは docs/features/request-to-plan.md の「文書」の受入基準のうち、コードの値と文書を照らすものを確かめる。
// 指示文の分量の上限の記述は internal/invoker、「着手しなかった」の理由の一覧は
// cycle_closedsets_test.go の閉集合の照合が確かめる。

const archPlanningSection = "### 計画を作る判断点（J2）: 分解は 1 回・受入基準の網羅を検査する"

// 計画を作る判断点の節に、受入基準の網羅の検査がある。不正な出力の理由の語は、実装が理由の文に使う語と同じ。
func TestDocsRequestToPlan_ArchPlanningSectionDescribesCoverageCheck(t *testing.T) {
	sec := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archPlanningSection)
	requireAll(t, "the coverage check in architecture.md", sec,
		"受入基準の網羅の検査", "区画「"+core.AcceptanceCriteriaSectionLabel+"」", "`covers`", "`deferred_criteria`",
		"`criteria not covered`", "`unknown criterion`", "`duplicate criterion`",
		"`"+string(core.NotStartedSpecUnreadable)+"`", "request-to-plan.md")
}
