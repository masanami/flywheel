package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/server"
)

// #988 AC-89・AC-95（Issue #1037。番号は #988 本文の「## 受入基準」の並びの順）: 承認（approve）の後にサイクルを始める処理の扱い。

// planAwaitingApproval は、計画の承認待ちの課題を持つワークスペースを作る。
func planAwaitingApproval(t *testing.T) (ws, id string) {
	t.Helper()
	ws = startWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "承認の対象")
	id = created["challenge"].(map[string]any)["id"].(string)
	runJSON(t, ws, "classify", id, "--priority", "P0")
	runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, "x"))
	return ws, id
}

// #988 AC-89: 始める処理がエラーを返しても、承認の記録そのものは残る（終了コード 0・承認が 1 件・課題は着手中）。
func TestApprove_AC89_StarterErrorKeepsTheApprovalRecord(t *testing.T) {
	parallelTools(t, false)
	ws, id := planAwaitingApproval(t)
	st := &fakeStarter{err: errors.New("claude: executable file not found")}

	code, stdout, stderr := runWithStarter(t, st, "approve", id, "--relayed", "--session", "S", "--quote", "Q", "--workspace", ws, "--json")

	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stdout=%q stderr=%q)", code, stdout, stderr)
	}
	if len(st.calls) != 1 || st.calls[0] != "approve" {
		t.Fatalf("starter calls = %v, want [approve] (the error path must be the one under test)", st.calls)
	}
	if c := decodeCycle(t, stdout); c["reason"] != "unavailable" || c["started"] != false {
		t.Errorf("cycle = %v, want unavailable/false", c)
	}
	show := runJSON(t, ws, "show", id)
	if status := show["challenge"].(map[string]any)["status"]; status != "in_progress" {
		t.Errorf("status = %v, want in_progress", status)
	}
	if aps := show["approvals"].([]any); len(aps) != 1 || aps[0].(map[string]any)["decision"] != "approved" {
		t.Errorf("approvals = %v, want one approved record", aps)
	}
}

// #988 AC-95: 本人確認の失敗（--relayed なし・端末でもない）で承認が成立しなかったとき、始める処理は 0 回で、cycle は出力に無く、
// サイクルの記録も増えない。
func TestApprove_AC95_IdentityFailureCallsNoStarterAndCreatesNoCycle(t *testing.T) {
	parallelTools(t, false)
	ws, id := planAwaitingApproval(t)
	st := &fakeStarter{launch: server.CycleLaunch{Started: true}}
	var out, errb bytes.Buffer
	code := runWithDeps([]string{"approve", id, "--workspace", ws, "--json"}, strings.NewReader(id+"\n"), &out, &errb, defaultCommands(), runDeps{LookPath: lookPathFor(t), CycleStart: st.start})

	if code == 0 {
		t.Fatalf("exit=0, want the identity check to fail (stdout=%q)", out.String())
	}
	if len(st.calls) != 0 {
		t.Errorf("starter calls = %v, want none", st.calls)
	}
	if strings.Contains(out.String(), `"cycle"`) {
		t.Errorf("stdout carries a cycle: %q", out.String())
	}
	show := runJSON(t, ws, "show", id)
	if status := show["challenge"].(map[string]any)["status"]; status != "awaiting_plan_approval" || len(show["approvals"].([]any)) != 0 {
		t.Errorf("state changed despite the failed identity check: %+v", show)
	}
	if n := fuCount(t, ws, `SELECT COUNT(*) FROM cycle`); n != "0" {
		t.Errorf("cycle rows = %s, want 0", n)
	}
}
