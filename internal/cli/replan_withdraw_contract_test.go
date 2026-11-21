package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは、計画の作り直しと取り下げ（区切り S3b の「計画の作り直しと取り下げ」）の、取り下げ・計画を作り直す回答の CLI の経路を、
// 偽の接続ツールで確かめる（受入基準 AC-217・AC-218・AC-219・AC-220・AC-221 を、実際の操作の入口から）。

// twoTaskSpec は、統合先を使う 2 個のタスク（a・b）の計画の構造化した出力。
const twoTaskSpec = `{"verdict":"plan","size":"M","uses_integration":true,"tasks":[
  {"key":"a","title":"A","body":"b","done_criteria":"d","repo":"r-repo","operation":"impl"},
  {"key":"b","title":"B","body":"b","done_criteria":"d","repo":"r-repo","operation":"impl"}]}`

// integrationChallengeWithTaskRuns は、承認済みの 2 個のタスクの計画を持つ着手中の課題を作り、タスク a・b に終わっていない契約の実行
// （ext-1・ext-2。状態は states の順）を結び付ける。
func integrationChallengeWithTaskRuns(t *testing.T, ws string, states ...string) (id string, cid int) {
	t.Helper()
	id = createForCase(t, ws)
	cid = challengeIDToInternalID(t, id)
	coretest.InsertApprovedPlan(t, ws, cid, "PLAN-BODY", twoTaskSpec)
	for i, st := range states {
		coretest.InsertTaskContractRun(t, ws, cid, []string{"a", "b"}[i], "tool", fmt.Sprintf("ext-%d", i+1), st)
	}
	return id, cid
}

// putFakeCancelToolOnPATH は status・cancel に応える偽の接続ツール tool を PATH の先頭に置く。cancel は呼ばれた run の ID を logFile へ 1 行ずつ足す。
// failRun と同じ run の cancel は、終わりの状態を返さず失敗する（failRun が空なら全部成功する）。
func putFakeCancelToolOnPATH(t *testing.T, logFile, failRun string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"status) printf '{\"contract_version\":1,\"run_id\":\"%s\",\"state\":\"running\",\"summary\":\"s\"}\\n' \"$2\";;\n" +
		"cancel) echo \"$2\" >> " + logFile + "\n" +
		"  if [ \"$2\" = \"" + failRun + "\" ] && [ -n \"" + failRun + "\" ]; then exit 1; fi\n" +
		"  printf '{\"contract_version\":1,\"run_id\":\"%s\",\"state\":\"cancelled\",\"summary\":\"s\"}\\n' \"$2\";;\n" +
		"*) exit 1;;\n" +
		"esac\n"
	dir := t.TempDir()
	if err := writeExecutable(filepath.Join(dir, "tool"), []byte(script)); err != nil {
		t.Fatalf("write fake tool: %v", err)
	}
	addToolDir(t, dir)
}

func cancelLog(t *testing.T, logFile string) []string {
	t.Helper()
	data, err := os.ReadFile(logFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(data))
}

// AC-217: タスクの実行が 2 本走っている課題を取り下げる（withdraw の入口）と、両方に commands.cancel が呼ばれ、課題は取り下げになる。
func TestWithdrawCommand_AC217_BothTaskRunsAreCancelled(t *testing.T) {
	parallelTools(t, false)
	ws := initializedWorkspace(t)
	writeConnectorsJSONForTest(t, ws, reopenToolConnectorsFixture)
	id, _ := integrationChallengeWithTaskRuns(t, ws, "running", "running")
	logFile := filepath.Join(t.TempDir(), "cancel.log")
	putFakeCancelToolOnPATH(t, logFile, "")
	code, _, stderr := runCLIRaw(t, relayedArgs(ws, "withdraw", id, "--reason", "採らない")...)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	if got := cancelLog(t, logFile); len(got) != 2 || got[0] != "ext-1" || got[1] != "ext-2" {
		t.Errorf("commands.cancel calls = %v, want ext-1 and ext-2", got)
	}
	if got := statusOf(t, ws, id); got != "withdrawn" {
		t.Errorf("status = %s, want withdrawn", got)
	}
}

// 1 本の取り消しが失敗しても、ほかの実行の取り消しは試み、取り下げは conflict で止まる（課題は取り下げにならない）。
func TestWithdrawCommand_AFailedCancelDoesNotStopTheOtherCancelsAndTheWithdrawStops(t *testing.T) {
	parallelTools(t, false)
	ws := initializedWorkspace(t)
	writeConnectorsJSONForTest(t, ws, reopenToolConnectorsFixture)
	id, _ := integrationChallengeWithTaskRuns(t, ws, "running", "running")
	logFile := filepath.Join(t.TempDir(), "cancel.log")
	putFakeCancelToolOnPATH(t, logFile, "ext-1")
	code, _, stderr := runCLIRaw(t, relayedArgs(ws, "withdraw", id, "--reason", "採らない")...)
	if code == 0 || !strings.Contains(stderr, "conflict") {
		t.Fatalf("exit=%d stderr=%s, want a conflict", code, stderr)
	}
	if got := cancelLog(t, logFile); len(got) != 2 {
		t.Errorf("commands.cancel calls = %v, want both runs tried", got)
	}
	if got := statusOf(t, ws, id); got != "in_progress" {
		t.Errorf("status = %s, want in_progress (not withdrawn)", got)
	}
}

// AC-218: 上げる依頼が走っている課題を取り下げる（withdraw の入口）と、上げる依頼に commands.cancel が呼ばれる。
func TestWithdrawCommand_AC218_TheReflectRequestIsCancelled(t *testing.T) {
	parallelTools(t, false)
	ws := initializedWorkspace(t)
	writeConnectorsJSONForTest(t, ws, reopenToolConnectorsFixture)
	id := createWithStatusForCase(t, ws, "verifying")
	cid := challengeIDToInternalID(t, id)
	opID := coretest.InsertReleaseOperation(t, ws, cid, "o/r#1", "pending")
	coretest.InsertReflectContractRun(t, ws, cid, opID, "tool", "ext-9", "waiting")
	logFile := filepath.Join(t.TempDir(), "cancel.log")
	putFakeCancelToolOnPATH(t, logFile, "")
	code, _, stderr := runCLIRaw(t, relayedArgs(ws, "withdraw", id, "--reason", "採らない")...)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	if got := cancelLog(t, logFile); len(got) != 1 || got[0] != "ext-9" {
		t.Errorf("commands.cancel calls = %v, want the reflect request ext-9", got)
	}
	if got := statusOf(t, ws, id); got != "withdrawn" {
		t.Errorf("status = %s, want withdrawn", got)
	}
}

// replanHoldFixture は、着手中の課題に、待ち（waiting）・動いている（running）などの状態のタスクの実行を持たせ、着手中の保留を開く。
func replanHoldFixture(t *testing.T, states ...string) (ws, id, logFile string) {
	t.Helper()
	ws = initializedWorkspace(t)
	writeConnectorsJSONForTest(t, ws, reopenToolConnectorsFixture)
	id, cid := integrationChallengeWithTaskRuns(t, ws, states...)
	coretest.SetChallengeStatus(t, ws, cid, "awaiting_human")
	coretest.InsertHold(t, ws, cid, "範囲を変えたい", "in_progress", "2026-10-22T00:00:00.000Z", nil)
	logFile = filepath.Join(t.TempDir(), "cancel.log")
	putFakeCancelToolOnPATH(t, logFile, "")
	return ws, id, logFile
}

// AC-220: レビュー待ち（waiting）のタスクの実行だけが開いている課題の保留への --replan の回答（answer の入口）は受け付けられ、
// 回答の時点では commands.cancel を呼ばない（取り消しは作り直した計画の承認の後）。
func TestAnswerCommand_AC220_AWaitingTaskRunDoesNotRefuseTheReplanAnswer(t *testing.T) {
	parallelTools(t, false)
	ws, id, logFile := replanHoldFixture(t, "waiting", "waiting")
	code, _, stderr := runCLIRaw(t, relayedArgs(ws, "answer", id, "--answer", "新しい範囲", "--replan")...)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s, want the answer accepted", code, stderr)
	}
	if got := statusOf(t, ws, id); got != "classified" {
		t.Errorf("status = %s, want classified", got)
	}
	if got := cancelLog(t, logFile); len(got) != 0 {
		t.Errorf("commands.cancel calls = %v, want none at the answer", got)
	}
}

// AC-221: 最後に読んだ状態が running のタスクの実行がある課題の保留への --replan の回答（answer の入口）は、validation_failed で断られる。
func TestAnswerCommand_AC221_ARunningTaskRunRefusesTheReplanAnswer(t *testing.T) {
	parallelTools(t, false)
	ws, id, logFile := replanHoldFixture(t, "waiting", "running")
	code, _, stderr := runCLIRaw(t, relayedArgs(ws, "answer", id, "--answer", "新しい範囲", "--replan")...)
	if code == 0 || !strings.Contains(stderr, "validation_failed") {
		t.Fatalf("exit=%d stderr=%s, want validation_failed", code, stderr)
	}
	if got := statusOf(t, ws, id); got != "awaiting_human" {
		t.Errorf("status = %s, want awaiting_human (refused)", got)
	}
	if got := cancelLog(t, logFile); len(got) != 0 {
		t.Errorf("commands.cancel calls = %v, want none", got)
	}
}
