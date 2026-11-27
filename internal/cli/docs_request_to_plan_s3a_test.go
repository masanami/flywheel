package cli

import (
	"strings"
	"testing"
)

// このファイルは docs/features/request-to-plan-s3.md の S3a の「文書」の受入基準（AC-241〜247）を、文書の本文で確かめる。
// 指示文の分量の上限の記述（AC-248。m3 の仕様と prompts.go の値の一致）は internal/invoker の
// TestSliceSplitPromptLimitsAreDocumentedInTheM3Spec が確かめる。

const (
	archCycleSection      = "## 7. サイクルの再設計"
	archJudgmentSection   = "### 21.6 判断点の整理（D28）"
	archUIButtonSection   = "### 画面のボタンの回答の限り（経路 `ui`。区切り S4d-2）"
	archUIButtonRiskTitle = "#### 受け入れた危険（画面のボタンを広げたこと）"
)

// AC-241
func TestDocsRequestToPlanS3a_AC241_GlossaryRequirementRow(t *testing.T) {
	cols := glossaryRow(t, "要求")
	requireAll(t, "the code name of 要求", cols[3], "`requirement`", "表示は `RQ-<番号>`")
	if cols[4] != "S3a で入った" {
		t.Errorf("since = %q, want %q", cols[4], "S3a で入った")
	}
	requireAll(t, "the meaning of 要求", cols[1],
		"要求の記録", "`issue`", "`chat`", "`manual`", "証跡の場所", "指す課題がすべて")
}

// AC-242
func TestDocsRequestToPlanS3a_AC242_GlossaryHasSliceSplitJudgmentRow(t *testing.T) {
	row := strings.Join(glossaryRow(t, "区切りを分ける判断点"), " | ")
	requireAll(t, "the 区切りを分ける判断点 row", row, "J10", "読み取りだけ", "分けないときは、オーナーへ問わず")
}

// AC-243
func TestDocsRequestToPlanS3a_AC243_ArchJudgmentTableHasJ10(t *testing.T) {
	sec := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archJudgmentSection)
	docTableRow(t, sec, "| 区切りを分ける（J10） |")
}

// AC-244
func TestDocsRequestToPlanS3a_AC244_ArchCyclePhaseOrderHasSettle(t *testing.T) {
	sec := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archCycleSection)
	docLineWith(t, sec, "段の並び", "反映（`release`）→ 片付け（`settle`）→ 取り込み（`ingest`）")
}

// AC-245
func TestDocsRequestToPlanS3a_AC245_ArchUIButtonSectionHasTheSplitException(t *testing.T) {
	sec := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archUIButtonSection)
	limits, _, ok := strings.Cut(sec, archUIButtonRiskTitle)
	if !ok {
		t.Fatalf("the section lacks %q", archUIButtonRiskTitle)
	}
	docLineWith(t, limits, "取り下げを伴う回答の例外", "「この案で分ける」", "`split`", "分類済の課題", "計画の承認")
}

// AC-246
func TestDocsRequestToPlanS3a_AC246_ArchUIButtonRiskHasTheSplit(t *testing.T) {
	sec := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archUIButtonRiskTitle)
	requireAll(t, "the accepted risk of the UI buttons", sec, "「この案で分ける」")
}

// AC-247
func TestDocsRequestToPlanS3a_AC247_M3OutputTableHasFitsOneSliceAndJ10(t *testing.T) {
	doc := readRepoDoc(t, "docs", "features", "m3-invoker-delegation.md")
	sec := sectionBody(t, doc, "#### 判断点の出力（`--json-schema` に渡す形の要点）")
	requireAll(t, "the J1 row", docTableRow(t, sec, "| J1 |"), `"fits_one_slice": bool\|null`)
	requireAll(t, "the J10 row", docTableRow(t, sec, "| J10"),
		"区切りを分ける判断点", `split\|single`, `"slices"`, `"evidence"`, `"depends_on"`)
}
