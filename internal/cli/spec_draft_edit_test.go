package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは、下書きづくりの途中の課題への `edit --spec` の取り消しを、コマンドの入口から偽の接続ツールで確かめる。

// runningSpecDraftChallenge は、実行中の下書きづくりの記録と、終わっていない種類 spec の実行 ext-9 を持つ分類済の課題を作る。
func runningSpecDraftChallenge(t *testing.T, ws string) string {
	t.Helper()
	id := createWithStatusForCase(t, ws, "classified")
	cid := challengeIDToInternalID(t, id)
	insertSpecDraftRow(t, ws, id)
	coretest.Exec(t, ws, `UPDATE spec_draft SET state = 'running' WHERE challenge_id = ?`, cid)
	opID := coretest.InsertReleaseOperation(t, ws, cid, "o/r#1", "pending")
	coretest.InsertReflectContractRun(t, ws, cid, opID, "tool", "ext-9", "running")
	coretest.Exec(t, ws, `UPDATE contract_run SET kind = 'spec', operation_id = NULL WHERE external_run_id = 'ext-9'`)
	return id
}

func TestEditSpecCommand_CancelsTheSpecDraftRun(t *testing.T) { // AC-249・AC-250・AC-261
	parallelTools(t, false)
	ws := initializedWorkspace(t)
	writeConnectorsJSONForTest(t, ws, reopenToolConnectorsFixture)
	id := runningSpecDraftChallenge(t, ws)
	logFile := filepath.Join(t.TempDir(), "cancel.log")
	putFakeCancelToolOnPATH(t, logFile, "")
	runJSON(t, ws, "edit", id, "--spec", "docs/spec.md")
	if got := cancelLog(t, logFile); len(got) != 1 || got[0] != "ext-9" {
		t.Errorf("commands.cancel calls = %v, want ext-9", got)
	}
	ch := showChallengeDoc(t, ws, id)
	if ch["spec_path"] != "docs/spec.md" || ch["spec_draft"].(map[string]any)["state"] != "skipped" {
		t.Errorf("spec_path = %v, spec_draft = %v", ch["spec_path"], ch["spec_draft"])
	}
}

func TestEditSpecCommand_AFailedCancelIsAConflict(t *testing.T) { // AC-252・AC-253
	parallelTools(t, false)
	ws := initializedWorkspace(t)
	writeConnectorsJSONForTest(t, ws, reopenToolConnectorsFixture)
	id := runningSpecDraftChallenge(t, ws)
	putFakeCancelToolOnPATH(t, filepath.Join(t.TempDir(), "cancel.log"), "ext-9")
	code, _, stderr := runCLIRaw(t, "edit", id, "--spec", "docs/spec.md", "--workspace", ws)
	if code == 0 || !strings.Contains(stderr, "conflict") {
		t.Fatalf("exit=%d stderr=%s, want a conflict", code, stderr)
	}
	ch := showChallengeDoc(t, ws, id)
	if ch["spec_path"] != nil || ch["spec_draft"].(map[string]any)["state"] != "running" {
		t.Errorf("spec_path = %v, spec_draft = %v", ch["spec_path"], ch["spec_draft"])
	}
}

func TestEditSpecCommand_WithoutARecordNeedsNoConnectorsDeclaration(t *testing.T) { // AC-255
	t.Parallel()
	ws := initializedWorkspace(t)
	if err := os.Remove(filepath.Join(ws, ".flywheel", "connectors.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	id := createForCase(t, ws)
	runJSON(t, ws, "edit", id, "--spec", "docs/spec.md")
	if got := showChallengeDoc(t, ws, id)["spec_path"]; got != "docs/spec.md" {
		t.Errorf("spec_path = %v", got)
	}
}

func TestWithdrawCommand_CancelsTheSpecDraftRunAndRemovesTheDraft(t *testing.T) { // AC-392・AC-394
	parallelTools(t, false)
	ws := initializedWorkspace(t)
	writeConnectorsJSONForTest(t, ws, reopenToolConnectorsFixture)
	id := runningSpecDraftChallenge(t, ws)
	draft := filepath.Join(ws, ".flywheel", "specs", "c-1.md")
	if err := os.MkdirAll(filepath.Dir(draft), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(draft, []byte("# 下書き\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(t.TempDir(), "cancel.log")
	putFakeCancelToolOnPATH(t, logFile, "")
	if code, _, stderr := runCLIRaw(t, relayedArgs(ws, "withdraw", id, "--reason", "採らない")...); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	if got := cancelLog(t, logFile); len(got) != 1 || got[0] != "ext-9" {
		t.Errorf("commands.cancel calls = %v, want ext-9", got)
	}
	if _, err := os.Lstat(draft); !os.IsNotExist(err) {
		t.Errorf("the draft remains: %v", err)
	}
}
