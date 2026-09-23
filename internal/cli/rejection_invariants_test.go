package cli

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは masanami/flywheel#29 の AC-37 を、既存テストファイル
// （approval_process_test.go）への追記ではなく新規ファイルとして固定する。
//
// AC-37: 端末なしで approve・reject・answer を実行すると、終了コード 1・
// tty_required で終わり、状態・承認の記録・作業ログが変わらない。
//
// 既存の TestReject_NoTerminalStdin_ReturnsTTYRequired・
// TestAnswer_NoTerminalStdin_ReturnsTTYRequired（approval_process_test.go）は
// 終了コードとエラーコードしか見ていない。TestApprove_NoTerminalStdin_
// ReturnsTTYRequired は状態と承認の記録までは見ているが、作業ログは見て
// いない。ここでは 3 コマンドとも、実行前後の show（人間記入欄・状態・
// 承認の記録・保留の記録・不可逆操作）と log（作業ログ）の JSON を丸ごと
// 構造的に比較し、「状態・承認の記録・作業ログのいずれも変わっていない」を
// 個別のフィールドの見落としなく確認する。

// snapshotChallengeAndLog は show <id>・log <id> の JSON ドキュメントを
// そのまま返す。AC-37 の「状態・承認の記録・作業ログ」をまとめて比較する
// ための前後のスナップショットに使う。
func snapshotChallengeAndLog(t *testing.T, ws, id string) (show, log map[string]any) {
	t.Helper()
	return runJSON(t, ws, "show", id), runJSON(t, ws, "log", id)
}

func TestApprove_NoTerminalStdin_StateApprovalsAndActivityLogUnchanged(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	runJSON(t, ws, "classify", id, "--priority", "P0")
	runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, "x"))

	beforeShow, beforeLog := snapshotChallengeAndLog(t, ws, id)

	cmd, stdout, stderr := newChildCmd([]string{"approve", "--workspace", ws, "--json", id})
	cmd.Stdin = strings.NewReader(id + "\n")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	code := waitChild(t, cmd, 10*time.Second)
	if code != 1 {
		t.Fatalf("exit=%d, want 1 (stdout=%q stderr=%q)", code, stdout.String(), stderr.String())
	}
	if got := readErrorCode(t, stderr.String()); got != string(CodeTTYRequired) {
		t.Fatalf("error code = %q, want %q", got, CodeTTYRequired)
	}

	afterShow, afterLog := snapshotChallengeAndLog(t, ws, id)
	if !reflect.DeepEqual(beforeShow, afterShow) {
		t.Errorf("show (state + approval records) changed despite tty_required:\nbefore=%+v\nafter=%+v", beforeShow, afterShow)
	}
	if !reflect.DeepEqual(beforeLog, afterLog) {
		t.Errorf("log (activity log) changed despite tty_required:\nbefore=%+v\nafter=%+v", beforeLog, afterLog)
	}
}

func TestReject_NoTerminalStdin_StateApprovalsAndActivityLogUnchanged(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	runJSON(t, ws, "classify", id, "--priority", "P0")
	runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, "x"))

	beforeShow, beforeLog := snapshotChallengeAndLog(t, ws, id)

	cmd, stdout, stderr := newChildCmd([]string{"reject", "--workspace", ws, "--json", "--reason", "r", id})
	cmd.Stdin = strings.NewReader(id + "\n")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	code := waitChild(t, cmd, 10*time.Second)
	if code != 1 {
		t.Fatalf("exit=%d, want 1 (stdout=%q stderr=%q)", code, stdout.String(), stderr.String())
	}
	if got := readErrorCode(t, stderr.String()); got != string(CodeTTYRequired) {
		t.Fatalf("error code = %q, want %q", got, CodeTTYRequired)
	}

	afterShow, afterLog := snapshotChallengeAndLog(t, ws, id)
	if !reflect.DeepEqual(beforeShow, afterShow) {
		t.Errorf("show (state + approval records) changed despite tty_required:\nbefore=%+v\nafter=%+v", beforeShow, afterShow)
	}
	if !reflect.DeepEqual(beforeLog, afterLog) {
		t.Errorf("log (activity log) changed despite tty_required:\nbefore=%+v\nafter=%+v", beforeLog, afterLog)
	}
}

func TestAnswer_NoTerminalStdin_StateApprovalsAndActivityLogUnchanged(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "verifying")
	runJSON(t, ws, "verify", id, "--result", "uncertain", "--question", "why?")

	beforeShow, beforeLog := snapshotChallengeAndLog(t, ws, id)

	cmd, stdout, stderr := newChildCmd([]string{"answer", "--workspace", ws, "--json", "--answer", "a", id})
	cmd.Stdin = strings.NewReader(id + "\n")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	code := waitChild(t, cmd, 10*time.Second)
	if code != 1 {
		t.Fatalf("exit=%d, want 1 (stdout=%q stderr=%q)", code, stdout.String(), stderr.String())
	}
	if got := readErrorCode(t, stderr.String()); got != string(CodeTTYRequired) {
		t.Fatalf("error code = %q, want %q", got, CodeTTYRequired)
	}

	afterShow, afterLog := snapshotChallengeAndLog(t, ws, id)
	if !reflect.DeepEqual(beforeShow, afterShow) {
		t.Errorf("show (state + hold records) changed despite tty_required:\nbefore=%+v\nafter=%+v", beforeShow, afterShow)
	}
	if !reflect.DeepEqual(beforeLog, afterLog) {
		t.Errorf("log (activity log) changed despite tty_required:\nbefore=%+v\nafter=%+v", beforeLog, afterLog)
	}
}
