package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは #72（親要件チケット #51 §上流の更新の観測と既読）の
// 受入基準のうち、CLI レベルの `mark-read` 固有の項目（AC-137・AC-138）を
// 検証する。jsondoc（AC-162）は allcommands_test.go・jsondoc_test.go が
// `allCommandSuccessCases["mark-read"]` 経由で検証する。

func bindUnreadChallengeForMarkRead(t *testing.T, ws string) string {
	t.Helper()
	id := createForCase(t, ws)
	bindSourceForDiscrepancyCase(t, ws, id, "o/r#1", "open", "in_policy")
	coretest.SetSourceBindingObservation(t, ws, challengeIDToInternalID(t, id), 2, "2026-09-25T08:00:00.000Z", 0, "")
	return id
}

// AC-137: mark-read は gh を呼ばない（偽の gh への呼び出しが 0 回）。
func TestMarkRead_DoesNotInvokeGH(t *testing.T) {
	ws := initializedWorkspace(t)
	id := bindUnreadChallengeForMarkRead(t, ws)
	calls := withFakeGHRoutesOnPATH(t, nil)

	doc := runJSON(t, ws, "mark-read", id)
	if doc["changed"] != true {
		t.Fatalf("mark-read result = %+v, want changed=true (test の前提)", doc)
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("fake gh invocations = %v, want none (AC-137)", got)
	}
}

// AC-138: mark-read は、標準入力と標準出力が端末でなくても成功する
// （本人確認 none）。
func TestMarkRead_SucceedsWithoutATerminal(t *testing.T) {
	ws := initializedWorkspace(t)
	id := bindUnreadChallengeForMarkRead(t, ws)

	var stdout, stderr bytes.Buffer
	code := run([]string{"mark-read", id, "--workspace", ws, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stdout=%q stderr=%q) (AC-138)", code, stdout.String(), stderr.String())
	}
}
