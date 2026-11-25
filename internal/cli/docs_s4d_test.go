package cli

import (
	"strings"
	"testing"
)

// このファイルは、区切り S4d の文書の受入基準（AC-179〜AC-184。docs/features/core-redesign-s4d.md の「文書」の節）を、
// 基準ごとに文書の本文で確かめる。
// AC-179・AC-181・AC-183 の期待は、対話の欄の作り替え（docs/features/chat-session.md）で改めた記述に合わせてある。

func requireAll(t *testing.T, where, body string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("%s: missing %q", where, w)
		}
	}
}

// AC-179: 設計書 §9 に、経路「対話の欄」・受ける操作・受け入れた危険がある。
func TestDocsS4dArchitectureApprovalRouteChat(t *testing.T) {
	doc := readRepoDoc(t, "docs", "architecture.md")
	sec := sectionBody(t, doc, "## 9. 承認ゲート")
	requireAll(t, "architecture §9", sec,
		"対話の欄", "`chat`", "chat_approvable_operations", "`release`",
		"承認・差し戻し・回答・取り下げ・予算の増額・フォローアップ",
		"#### 受け入れた危険\n", "(`chat`, `relayed`)")
	// 経路の表の行は、表の続き（直前の行が表の行）でなければならない。
	lines := strings.Split(sec, "\n")
	found := false
	for i, l := range lines {
		if strings.HasPrefix(l, "| 画面の対話の欄（`chat`・`relayed`）") {
			found = true
			if i == 0 || !strings.HasPrefix(lines[i-1], "| ") {
				t.Errorf("経路の表の chat の行が、表の続きになっていない")
			}
		}
	}
	if !found {
		t.Error("経路の表に chat の行が無い")
	}
}

// AC-180: 設計書 §4 に、サーバが対話の欄のために実行エンジンを起動することがある。
func TestDocsS4dArchitectureServerStartsEngine(t *testing.T) {
	sec := sectionBody(t, readRepoDoc(t, "docs", "architecture.md"), "## 4. 全体構成")
	requireAll(t, "architecture §4", sec, "対話の欄のために、サーバが実行エンジン", "起動")
}

// AC-181: CLAUDE.md の internal/server の行に、動いている Claude Code のプロセスと入出力の管と時計が、ストアの外の状態の例外としてある。
func TestDocsS4dClaudeMDServerRowException(t *testing.T) {
	for _, line := range strings.Split(readRepoDoc(t, "CLAUDE.md"), "\n") {
		if strings.HasPrefix(line, "| `internal/server` |") {
			requireAll(t, "CLAUDE.md internal/server", line, "動いている Claude Code のプロセス", "入出力の管", "休ませるための時計", "ストアの外")
			return
		}
	}
	t.Fatal("CLAUDE.md has no internal/server row")
}

// AC-182: ux.md の「未決・見送り」に、対話の欄の分をこの区切りで決めた旨がある。
func TestDocsS4dUXUndecidedRecordsChatDecision(t *testing.T) {
	sec := sectionBody(t, readRepoDoc(t, "docs", "features", "core-redesign", "ux.md"), "## 未決・見送り")
	requireAll(t, "ux.md 未決・見送り", sec, "決定 27", "S4d の区切りで決めた", "`chat`")
}

// AC-183: m3 の宣言の項目の一覧に、対話の欄の項目と、無くした 3 項目がある。
func TestDocsS4dM3DeclarationListsChatItems(t *testing.T) {
	doc := readRepoDoc(t, "docs", "features", "m3-invoker-delegation.md")
	requireAll(t, "m3 宣言の項目", doc,
		"- `chat_tools`・`chat_max_turns`・`chat_timeout_sec`", "**無くした項目**", "- `chat_approvable_operations`")
}

// AC-184: 用語集に「対話の欄」の行がある。
func TestDocsS4dGlossaryHasChatPane(t *testing.T) {
	row := glossaryRow(t, "対話の欄")
	requireAll(t, "glossary 対話の欄", strings.Join(row, " "), "経路 `chat`", "S4d")
}
