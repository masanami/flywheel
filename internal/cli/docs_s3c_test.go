package cli

import (
	"strings"
	"testing"
)

// このファイルは、区切り S3c の指示文の受入基準（#926。AC-91〜AC-93）を、J5 の指示文（internal/invoker/prompts/j5.md）で確かめる。

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
