package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/flow"
	"github.com/masanami/flywheel/internal/invoker"
)

// このファイルは docs/features/request-to-plan-s2b.md の「文書」の受入基準を、文書の本文で確かめる。
// 指示文の分量の上限の記述（m3 の仕様と prompts.go の値の一致）は internal/invoker の
// TestJ2Prompt_LimitIsDocumentedInTheM3Spec が確かめる。

// 用語集に「下書きづくり」の行がある。
func TestDocsS2b1_GlossaryHasSpecDraftingRow(t *testing.T) {
	cols := glossaryRow(t, "下書きづくり")
	requireAll(t, "the 下書きづくり row", strings.Join(cols, " | "), "`"+flow.SpecDraftFlow+"`", "`spec_draft`")
}

// 「下書きづくり」の行に、許可の形が default の委譲先では下書きを書けないことがある。
func TestDocsS2b1_GlossarySpecDraftingRowSaysDefaultPermissionModeCannotWrite(t *testing.T) {
	requireAll(t, "the meaning of 下書きづくり", glossaryRow(t, "下書きづくり")[1],
		"許可の形（`permission_mode`）が `default` の委譲先では下書きを書けない")
}

// 全部の実装が入った後の一覧に、下書きづくりで足した閉集合の値・宣言のキー・作業ログの種類を足しても、
// 計画を作る判断点の指示文は禁止語の検査を通る。出力の型の項目の名前 spec_draft は指示文に書いてよい。
func TestDocsS2b1_J2PromptPassesForbiddenTermsAfterAllOfSpecDrafting(t *testing.T) {
	body, err := invoker.Instructions(core.JudgmentJ2)
	if err != nil {
		t.Fatal(err)
	}
	terms := invoker.ForbiddenTerms(realForbiddenSources())
	added := []string{
		string(core.J2VerdictNeedsSpec), "spec", flow.SpecDraftFlow, "spec_draft_budget_usd",
		core.ActivityActionSpecDraftRequested, core.ActivityActionSpecDraftDone, core.ActivityActionSpecDraftSkipped,
		"answer", "proceed", "retry", "skip",
	}
	for _, v := range core.SpecDraftHoldReasonValues {
		added = append(added, string(v))
	}
	if hits := invoker.FindForbiddenTerms(body, append(terms, added...)); len(hits) != 0 {
		t.Errorf("j2.md contains forbidden term(s): %v", hits)
	}
}

// 「仕様づくりの費用」の行に、下書きづくりの実行の費用を足すことと、その分はサイクルの費用に数えることがある。
func TestDocsS2b1_GlossarySpecCostRowAddsSpecDraftingRuns(t *testing.T) {
	requireAll(t, "the meaning of 仕様づくりの費用", glossaryRow(t, "仕様づくりの費用")[1],
		"下書きづくりの実行の費用も、仕様づくりの費用に足す")
}

func TestDocsS2b1_GlossarySpecCostRowCountsSpecDraftingInTheCycleCost(t *testing.T) {
	requireAll(t, "the meaning of 仕様づくりの費用", glossaryRow(t, "仕様づくりの費用")[1],
		"下書きづくりの実行の分は、サイクルの費用に数える")
}

// 「フロー」の行に spec-draft がある。
func TestDocsS2b1_GlossaryFlowRowHasSpecDraft(t *testing.T) {
	requireAll(t, "the meaning of フロー", glossaryRow(t, "フロー")[1], "`"+flow.SpecDraftFlow+"`")
}

// 設計書の計画を作る判断点の節に needs_spec がある。
func TestDocsS2b1_ArchPlanningSectionHasNeedsSpec(t *testing.T) {
	sec := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archPlanningSection)
	requireAll(t, "the planning section of architecture.md", sec, "`"+string(core.J2VerdictNeedsSpec)+"`")
}

// 同じ節に spec-draft がある。
func TestDocsS2b1_ArchPlanningSectionHasSpecDraftFlow(t *testing.T) {
	sec := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archPlanningSection)
	requireAll(t, "the planning section of architecture.md", sec, "`"+flow.SpecDraftFlow+"`")
}

// m3 の判断点の出力の、計画を作る判断点の行に needs_spec がある。
func TestDocsS2b1_M3J2OutputRowHasNeedsSpec(t *testing.T) {
	row := docTableRow(t, readRepoDoc(t, "docs", "features", "m3-invoker-delegation.md"), "| J2 |")
	requireAll(t, "the J2 row of the m3 judgment outputs", row, "`"+string(core.J2VerdictNeedsSpec)+"`")
}

// 同じ行に spec_draft がある。
func TestDocsS2b1_M3J2OutputRowHasSpecDraftField(t *testing.T) {
	row := docTableRow(t, readRepoDoc(t, "docs", "features", "m3-invoker-delegation.md"), "| J2 |")
	requireAll(t, "the J2 row of the m3 judgment outputs", row, "`spec_draft`")
}

// flow-port の仕様に spec-draft がある。
func TestDocsS2b1_FlowPortHasSpecDraftFlow(t *testing.T) {
	requireAll(t, "flow-port.md", readRepoDoc(t, "docs", "features", "flow-port.md"), "`"+flow.SpecDraftFlow+"`")
}

// flow-port の仕様に --budget-usd がある。
func TestDocsS2b1_FlowPortHasBudgetUSDFlag(t *testing.T) {
	requireAll(t, "flow-port.md", readRepoDoc(t, "docs", "features", "flow-port.md"), "--budget-usd")
}

// flow-port の仕様に、facts に配列の欄を載せられることがある。
func TestDocsS2b1_FlowPortSaysFactsCarryArrayFields(t *testing.T) {
	requireAll(t, "flow-port.md", readRepoDoc(t, "docs", "features", "flow-port.md"), "`facts` に配列の欄を載せられる")
}

const flowReadmeChangesSection = "## 写した後に振る舞いを変えた点"

// internal/flow/README.md の写した後に変えた点に、facts に配列の欄を載せることがある。
func TestDocsS2b1_FlowReadmeChangesSayFactsCarryArrayFields(t *testing.T) {
	sec := sectionBody(t, readRepoDoc(t, "internal", "flow", "README.md"), flowReadmeChangesSection)
	requireAll(t, "the changes in internal/flow/README.md", sec, "`facts` に配列の欄を載せる")
}

// 同じ節に --budget-usd がある。
func TestDocsS2b1_FlowReadmeChangesHaveBudgetUSDFlag(t *testing.T) {
	sec := sectionBody(t, readRepoDoc(t, "internal", "flow", "README.md"), flowReadmeChangesSection)
	requireAll(t, "the changes in internal/flow/README.md", sec, "--budget-usd")
}
