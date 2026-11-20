package cli

import (
	"strings"
	"testing"
)

// このファイルは、区切り S3b の文書の受入基準（#891。docs/features/core-redesign.md の「文書」の節）を、基準ごとに文書の本文で確かめる。
// AC-270・AC-271（claude-harness の統合先の受け口の版と組み合わせた通し 2 本）と AC-272（ストアの写しでの起動）は、
// テストにせず、オーケストレーターが区切りの承認の前に行う確かめ。AC-273（make gate が終了コード 0）は make gate の実行で確かめる。

const (
	archSection212 = "### 21.2 承認の単位（D23）"
	archSection213 = "### 21.3 委譲先との境界（D24・D25・D31〜D33）"
)

func m3Doc(t *testing.T) string {
	t.Helper()
	return readRepoDoc(t, "docs", "features", "m3-invoker-delegation.md")
}

// AC-259: m3 の「接続ツールとの契約」に、統合先の成果物の印（語 integration）がある。
func TestSpec_AC259_M3ContractHasIntegrationMark(t *testing.T) {
	body := sectionBody(t, m3Doc(t), m3ContractHeading)
	if !strings.Contains(body, "`integration`") || !strings.Contains(body, "統合先の成果物の印") {
		t.Error("AC-259: the contract section of m3 does not describe the `integration` mark of the integration target's artifacts")
	}
}

// AC-260: m3 の「後半の流れ」に、統合先を使う課題のタスクへ、完了の検証の後に land を返す旨がある。
func TestSpec_AC260_M3SecondHalfLandAfterVerification(t *testing.T) {
	body := sectionBody(t, m3Doc(t), m3SecondHalf)
	docLineWith(t, body, "統合先を使う課題のタスク", "完了の検証", "`land`")
}

// AC-261: 同じ節に、完了でない課題の反映の依頼を、委譲の段が読む旨がある。
func TestSpec_AC261_M3SecondHalfDelegateStageReadsReflectRequest(t *testing.T) {
	body := sectionBody(t, m3Doc(t), m3SecondHalf)
	docLineWith(t, body, "完了でない課題の反映の依頼", "委譲の段が", "読む")
}

// AC-262: 同じ文書に、同時に走らせる数を、タスクの単位で数える旨がある。
func TestSpec_AC262_M3ParallelCountedPerTask(t *testing.T) {
	docLineWith(t, m3Doc(t), "同時に走らせる数", "`max_parallel_runs`", "タスクの単位で数える")
}

// AC-263: architecture.md の §21 の冒頭の注記に、区切り S3b で実装した範囲がある。
func TestSpec_AC263_ArchSection21NoteHasS3b(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archSection21)
	first := strings.SplitN(strings.TrimLeft(body, "\n"), "\n", 2)[0]
	if !strings.Contains(first, "区切り S3b で実装した範囲は") {
		t.Error("AC-263: the opening note of §21 does not list the range implemented in S3b")
	}
}

// AC-264: architecture.md の §21.3 に、統合先を使う課題のリポジトリを 1 つに限る旨の注記がある。
func TestSpec_AC264_ArchSection213OneRepoForIntegration(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archSection213)
	docLineWith(t, body, "区切り S3b", "統合先を使う課題", "リポジトリを 1 つに限る", "`integration_unavailable`")
}

// AC-265: architecture.md の §21.2 に、課題の単位の確かめが満たしていないときは保留にする旨の注記がある。
func TestSpec_AC265_ArchSection212HoldWhenChallengeCheckNotMet(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archSection212)
	docLineWith(t, body, "区切り S3b", "課題の単位の確かめ", "満たしていない", "保留にする")
}

// AC-266: 用語集の「統合先」の行の「いつから」に、S3b で入った旨がある。
func TestSpec_AC266_GlossaryIntegrationSinceS3b(t *testing.T) {
	cols := glossaryRow(t, "統合先")
	if !strings.Contains(cols[4], "S3b") {
		t.Errorf("AC-266: since = %q, want it to mention S3b", cols[4])
	}
}

// AC-267: 用語集の「タスク」の行の「いつから」に、語 S3b がある。
func TestSpec_AC267_GlossaryTaskSinceS3b(t *testing.T) {
	cols := glossaryRow(t, "タスク")
	if !strings.Contains(cols[4], "S3b") {
		t.Errorf("AC-267: since = %q, want it to mention S3b", cols[4])
	}
}

// AC-268: 用語集の「反映」の行に、衝突の解消を承認し直さずに通す形は S3c から、の旨がある。
func TestSpec_AC268_GlossaryReflectConflictFromS3c(t *testing.T) {
	cols := glossaryRow(t, "反映")
	if !strings.Contains(strings.Join(cols, " | "), "衝突の解消を承認し直さずに通す形は、S3c から") {
		t.Errorf("AC-268: the 反映 row does not say the no-reapproval conflict resolution form comes from S3c: %q", cols)
	}
}

// AC-269: m1-core.md の、終わっていない実行の一意の制約の記述に、タスクの単位である旨がある。
func TestSpec_AC269_M1UniqueConstraintPerTask(t *testing.T) {
	row := docTableRow(t, readRepoDoc(t, "docs", "features", "m1-core.md"), "| `run_in_progress` |")
	if !strings.Contains(row, "終わっていない実行の一意の制約は") || !strings.Contains(row, "タスクの単位") {
		t.Errorf("AC-269: the run_in_progress row does not say the unique constraint is per task: %q", row)
	}
}

// 出荷済みの節（前の区切りで足した形の節）は消していない。
func TestSpec_S3b_KeepsShippedCoreRedesignSections(t *testing.T) {
	doc := readRepoDoc(t, "docs", "features", "core-redesign.md")
	if !strings.Contains(doc, "前の区切りで足した形のうち、試験がこの文書の記述を正本として読むもの") {
		t.Error("core-redesign.md lost the section of earlier shapes that tests read as the source of truth")
	}
}

// m1-core.md の遷移表の T15 の行と説明に、区切り S3b からの戻り先（統合先を使う課題の完了の差し戻しは分類済へ送る）がある。
func TestSpec_S3b_M1T15RowAndNoteHaveTheIntegrationReturnTarget(t *testing.T) {
	doc := readRepoDoc(t, "docs", "features", "m1-core.md")
	row := docTableRow(t, doc, "| T15 |")
	for _, want := range []string{"区切り S3b から", "`challenge.uses_integration`", "分類済", "着手中"} {
		if !strings.Contains(row, want) {
			t.Errorf("the T15 row lacks %q: %q", want, row)
		}
	}
	// 説明（差し戻しの戻り先の節）にも、統合先を使う課題の戻り先がある。
	docLineWith(t, doc, "差し戻し（T15）の戻り先", "通常の完了", "区切り S3b から", "統合先を使うなら分類済へ戻し")
}
