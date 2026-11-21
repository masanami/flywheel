package cli

import (
	"strings"
	"testing"
)

// このファイルは、区切り S3d の文書の受入基準（#935。AC-130〜AC-132）を、文書の本文で確かめる。

// AC-130: m4-ui-server.md の画面の一覧に、完了承認画面がある。
func TestSpec_AC130_M4ScreenListHasCompletionApproval(t *testing.T) {
	doc := readRepoDoc(t, "docs", "features", "m4-ui-server.md")
	body := sectionBody(t, doc, "### 画面の一覧（S3d 時点）")
	docLineWith(t, body, "完了承認画面", "`approve_completion`")
}

// AC-131: core-redesign.md の「画面・API設計」に、tasks[] に足した 5 つのキーがある。
func TestSpec_AC131_CoreRedesignScreenAPIHasFiveTaskKeys(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "features", "core-redesign.md"), "## 画面・API設計")
	for _, k := range []string{"artifacts", "delegate", "facts", "landing", "verification"} {
		if !strings.Contains(body, "- `"+k+"`") {
			t.Errorf("AC-131: the screen/API section does not describe the tasks[] key %q", k)
		}
	}
	docLineWith(t, body, "`tasks[]`", "5 つのキー")
}

// AC-132: 同じ節に、ツリーの progress がある。
func TestSpec_AC132_CoreRedesignScreenAPIHasTreeProgress(t *testing.T) {
	body := sectionBody(t, readRepoDoc(t, "docs", "features", "core-redesign.md"), "## 画面・API設計")
	docLineWith(t, body, "ツリーの API", "`progress`", "`working`", "`verifying`", "`landing`", "`reflecting`", "`checking`")
}
