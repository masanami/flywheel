package cli

import (
	"strings"
	"testing"
)

// このファイルは、区切り S3c の文書と指示文の受入基準（#926）を、基準ごとに文書の本文で確かめる。
// AC-91〜AC-93 は J5 の指示文（internal/invoker/prompts/j5.md）、AC-136〜AC-141 は文書を読む。

const j5ConflictFixHeading = "## コンフリクトの解消の確かめ"

func j5Prompt(t *testing.T) string {
	t.Helper()
	return readRepoDoc(t, "internal", "invoker", "prompts", "j5.md")
}

func j5ConflictFixSection(t *testing.T) string {
	t.Helper()
	return sectionBody(t, j5Prompt(t), j5ConflictFixHeading)
}

// AC-91: J5 の指示文に、直した差分だけを確かめる（全体を検証し直さない）旨がある。
func TestSpec_AC91_J5PromptChecksOnlyTheFixDiff(t *testing.T) {
	body := j5ConflictFixSection(t)
	docLineWith(t, body, "直した差分が", "通した中身の意図を保ったまま", "宛先の変更と両立させているかだけ")
	if !strings.Contains(body, "全体を検証し直さない") {
		t.Error("AC-91: the conflict-fix section does not say not to re-verify the whole")
	}
}

// AC-92: 同じ節に、差分がコンフリクトの解消の範囲を超えていれば「満たしていない」を返す旨がある。
func TestSpec_AC92_J5PromptNotMetWhenBeyondConflictFix(t *testing.T) {
	docLineWith(t, j5ConflictFixSection(t), "解消の範囲を超えて", "振る舞いを足している", "消している", "満たしていないを選ぶ")
}

// AC-93: 同じ節に、差分を読めないファイルがあって確かめられなければ「判断できない」を返す旨がある。
func TestSpec_AC93_J5PromptUncertainWhenUnreadable(t *testing.T) {
	docLineWith(t, j5ConflictFixSection(t), "差分を読めないファイル", "確かめられない", "判断できないを選ぶ")
}

// J5 の出力の形は変えない旨がある。
func TestSpec_S3c_J5PromptKeepsOutputShape(t *testing.T) {
	docLineWith(t, j5ConflictFixSection(t), "出力の形は変えない")
}

// AC-136: architecture.md の §21 の実装済みの範囲の記述に、S3c（コンフリクトの解消を、J5 が差分で確かめて通す）がある。
func TestSpec_AC136_ArchSection21NoteHasS3c(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archSection21)
	first := strings.SplitN(strings.TrimLeft(body, "\n"), "\n", 2)[0]
	for _, want := range []string{"区切り S3c〔コンフリクトの解消を、J5 が差分で確かめて通す〕の範囲は実装済み", "区切り S3c で実装した範囲は"} {
		if !strings.Contains(first, want) {
			t.Errorf("AC-136: the opening note of §21 lacks %q", want)
		}
	}
}

// AC-137: 同じ節に、対話の欄での知らせは S4 である旨がある。
func TestSpec_AC137_ArchSection21NotifyInConversationIsS4(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), archSection21)
	docLineWith(t, body, "区切り S3c で実装した範囲は", "記録して課題の詳細で読める所までが実装と一致し", "対話の欄での知らせは区切り S4")
}

// AC-138: m1-core.md の、作業ログの action の一覧に、conflict_fix_passed がある。
func TestSpec_AC138_M1ActivityActionListHasConflictFixPassed(t *testing.T) {
	doc := readRepoDoc(t, "docs", "features", "m1-core.md")
	docLineWith(t, doc, "| `action` | string |", "`integration_ref_set`\\|`conflict_fix_passed`\\|`backup`", "`conflict_fix_passed` は区切り S3c で追加した", "統合先宛てならタスク、反映先宛てなら反映の操作")
}

// AC-139: m3-invoker-delegation.md の、J5 の節に、コンフリクトの解消の確かめへの参照がある。
func TestSpec_AC139_M3J5SectionRefersToConflictFixCheck(t *testing.T) {
	body := sectionBody(t, m3Doc(t), "### J5 検証 [S2]")
	docLineWith(t, body, "コンフリクトの解消の確かめ", "対象", "入力", "結果", "j5.md")
}

// AC-140: 用語集に、「コンフリクトの解消の確かめ」の行がある。
func TestSpec_AC140_GlossaryHasConflictFixCheck(t *testing.T) {
	cols := glossaryRow(t, "コンフリクトの解消の確かめ")
	if !strings.Contains(cols[1], "直した差分だけを確かめる") {
		t.Errorf("AC-140: the definition = %q", cols[1])
	}
}

// AC-141: 用語集の「反映」の行が、コンフリクトの解消を承認し直さずに通す形は S3c で入った、と書いている。
func TestSpec_AC141_GlossaryReflectConflictEnteredInS3c(t *testing.T) {
	cols := glossaryRow(t, "反映")
	if !strings.Contains(cols[4], "コンフリクトの解消を承認し直さずに通す形は、S3c で入った") {
		t.Errorf("AC-141: since = %q", cols[4])
	}
	if strings.Contains(cols[4], "S3c から") {
		t.Errorf("AC-141: since still says S3c is ahead: %q", cols[4])
	}
}

// 出荷済みの節（前の区切りで足した形の節）と、S3a・S3b の受入基準の試験の正本は消していない。
func TestSpec_S3c_KeepsShippedSections(t *testing.T) {
	doc := readRepoDoc(t, "docs", "features", "core-redesign.md")
	for _, want := range []string{
		"前の区切りで足した形のうち、試験がこの文書の記述を正本として読むもの",
		"### S3b で入れた要件（出荷済み）",
		"### S3a で入れた要件（出荷済み）",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("core-redesign.md lost %q, which tests read as the source of truth", want)
		}
	}
}
