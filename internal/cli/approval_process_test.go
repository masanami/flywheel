package cli

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは approve・reject・answer が端末を必要とする経路（成功・
// tty_required・confirmation_mismatch・conflict）を、confirm_process_test.go
// と同じ手法（このテストバイナリ自身を FLYWHEEL_CLI_TEST_CONFIRM_HELPER=1 の
// 子プロセスとして起動し、疑似端末または標準入力のパイプを割り当てる）で
// 検証する。子プロセスは confirmHelperMain 経由で defaultCommands()（本物の
// approve/reject/answer を含む）を実行する。runtime.GOOS によるスキップは
// しない（macOS・Linux の両方の CI で通す）。

func writePlanFileForTest(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.txt")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write plan file: %v", err)
	}
	return path
}

// --- 成功経路（T5・T6・T12・T13・T15） ---

func TestApprove_Success_PlanApproval(t *testing.T) {
	ws := initializedWorkspace(t)
	// タイトルは計画本文の部分文字列にならない、判別可能な値にする
	// （self-review 指摘: 旧タイトル "t" は "do the thing" の部分文字列として
	// 常に真になり、要約にタイトルが実際に出ているかを検証できていなかった）。
	const title = "distinctive-approval-title"
	created := runJSON(t, ws, "create", "--title", title)
	id := created["challenge"].(map[string]any)["id"].(string)
	runJSON(t, ws, "classify", id, "--priority", "P0")
	runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, "do the thing"))

	cmd, stdout, stderr := newChildCmd([]string{"approve", "--workspace", ws, "--json", id})
	master, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		t.Fatalf("pty.StartWithAttrs: %v", err)
	}
	defer func() { _ = master.Close() }()
	drain := startPTYDrain(master)

	if _, err := master.Write([]byte(id + "\n")); err != nil {
		t.Fatalf("write to master: %v", err)
	}

	code := waitChild(t, cmd, 10*time.Second)
	ptyOutput := drain.waitDone(5 * time.Second)
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stdout=%q stderr=%q pty=%q)", code, stdout.String(), stderr.String(), ptyOutput)
	}
	// AC-39: 課題の ID・タイトル・承認の種類・計画の本文と版を確認前に表示する。
	// ID の存在は confirmPrompt(id) の完全な文字列で検査する（self-review
	// 指摘: 素の id の部分一致だと、master.Write で書き込んだ確認入力自体の
	// 疑似端末ローカルエコーで無条件に満たされてしまい、要約に ID が実際に
	// 出ているかを検証できていなかった。confirm_process_test.go の既存の
	// 教訓と同じ）。
	for _, want := range []string{confirmPrompt(id), title, "計画", "do the thing"} {
		if !strings.Contains(ptyOutput, want) {
			t.Errorf("pty output = %q, want it to contain %q", ptyOutput, want)
		}
	}
	if strings.Contains(stdout.String(), "do the thing") {
		t.Errorf("stdout must not contain the summary: %q", stdout.String())
	}

	show := runJSON(t, ws, "show", id)
	c := show["challenge"].(map[string]any)
	if c["status"] != "in_progress" {
		t.Errorf("status = %v, want in_progress", c["status"])
	}
	approvals := show["approvals"].([]any)
	if len(approvals) != 1 {
		t.Fatalf("approvals = %+v, want 1 entry", approvals)
	}
	ap := approvals[0].(map[string]any)
	// create=1 -> classify=2 -> plan=3 -> target_version は approve 直前の3。
	if ap["kind"] != "plan" || ap["decision"] != "approved" || ap["channel"] != "cli" || ap["verification"] != "tty_confirm" || ap["target_version"] != float64(3) {
		t.Errorf("approval = %+v, want kind=plan decision=approved channel=cli verification=tty_confirm target_version=3", ap)
	}
	if ap["actor"] == "" {
		t.Errorf("approval.actor is empty")
	}
}

func TestApprove_Success_CompletionApproval(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t", "--done-criteria", "it works")
	id := created["challenge"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "awaiting_completion_approval")

	cmd, stdout, stderr := newChildCmd([]string{"approve", "--workspace", ws, "--json", id})
	master, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		t.Fatalf("pty.StartWithAttrs: %v", err)
	}
	defer func() { _ = master.Close() }()
	drain := startPTYDrain(master)

	if _, err := master.Write([]byte(id + "\n")); err != nil {
		t.Fatalf("write to master: %v", err)
	}

	code := waitChild(t, cmd, 10*time.Second)
	ptyOutput := drain.waitDone(5 * time.Second)
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stdout=%q stderr=%q pty=%q)", code, stdout.String(), stderr.String(), ptyOutput)
	}
	for _, want := range []string{confirmPrompt(id), "it works"} {
		if !strings.Contains(ptyOutput, want) {
			t.Errorf("pty output = %q, want it to contain %q", ptyOutput, want)
		}
	}

	show := runJSON(t, ws, "show", id)
	c := show["challenge"].(map[string]any)
	if c["status"] != "done" {
		t.Errorf("status = %v, want done", c["status"])
	}
	approvals := show["approvals"].([]any)
	if len(approvals) != 1 || approvals[0].(map[string]any)["kind"] != "completion" {
		t.Errorf("approvals = %+v, want 1 entry with kind=completion", approvals)
	}
}

func TestReject_Success_PlanApprovalRecordsReason(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	runJSON(t, ws, "classify", id, "--priority", "P0")
	runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, "do the thing"))

	cmd, stdout, stderr := newChildCmd([]string{"reject", "--workspace", ws, "--json", "--reason", "not ready", id})
	master, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		t.Fatalf("pty.StartWithAttrs: %v", err)
	}
	defer func() { _ = master.Close() }()
	drain := startPTYDrain(master)

	if _, err := master.Write([]byte(id + "\n")); err != nil {
		t.Fatalf("write to master: %v", err)
	}

	code := waitChild(t, cmd, 10*time.Second)
	ptyOutput := drain.waitDone(5 * time.Second)
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stdout=%q stderr=%q pty=%q)", code, stdout.String(), stderr.String(), ptyOutput)
	}
	// AC-41: 対応する承認と同じ要約（計画の本文）と入力された理由を表示する。
	for _, want := range []string{confirmPrompt(id), "do the thing", "not ready"} {
		if !strings.Contains(ptyOutput, want) {
			t.Errorf("pty output = %q, want it to contain %q", ptyOutput, want)
		}
	}

	show := runJSON(t, ws, "show", id)
	c := show["challenge"].(map[string]any)
	if c["status"] != "classified" {
		t.Errorf("status = %v, want classified", c["status"])
	}
	approvals := show["approvals"].([]any)
	if len(approvals) != 1 {
		t.Fatalf("approvals = %+v, want 1 entry", approvals)
	}
	ap := approvals[0].(map[string]any)
	if ap["decision"] != "rejected" || ap["reason"] != "not ready" {
		t.Errorf("approval = %+v, want decision=rejected reason='not ready'", ap)
	}
}

func TestReject_Success_CompletionApproval(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t", "--done-criteria", "it works")
	id := created["challenge"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "awaiting_completion_approval")

	cmd, stdout, stderr := newChildCmd([]string{"reject", "--workspace", ws, "--json", "--reason", "needs more work", id})
	master, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		t.Fatalf("pty.StartWithAttrs: %v", err)
	}
	defer func() { _ = master.Close() }()
	startPTYDrain(master)

	if _, err := master.Write([]byte(id + "\n")); err != nil {
		t.Fatalf("write to master: %v", err)
	}

	code := waitChild(t, cmd, 10*time.Second)
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stdout=%q stderr=%q)", code, stdout.String(), stderr.String())
	}

	show := runJSON(t, ws, "show", id)
	c := show["challenge"].(map[string]any)
	if c["status"] != "in_progress" {
		t.Errorf("status = %v, want in_progress", c["status"])
	}
}

func TestAnswer_Success_ReturnsToVerifyingAndDisplaysQuestionAndAnswer(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "verifying")
	runJSON(t, ws, "verify", id, "--result", "uncertain", "--question", "why uncertain?")

	cmd, stdout, stderr := newChildCmd([]string{"answer", "--workspace", ws, "--json", "--answer", "because I checked", id})
	master, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		t.Fatalf("pty.StartWithAttrs: %v", err)
	}
	defer func() { _ = master.Close() }()
	drain := startPTYDrain(master)

	if _, err := master.Write([]byte(id + "\n")); err != nil {
		t.Fatalf("write to master: %v", err)
	}

	code := waitChild(t, cmd, 10*time.Second)
	ptyOutput := drain.waitDone(5 * time.Second)
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stdout=%q stderr=%q pty=%q)", code, stdout.String(), stderr.String(), ptyOutput)
	}
	// AC-42: 問いと入力された回答を表示する。
	for _, want := range []string{confirmPrompt(id), "why uncertain?", "because I checked"} {
		if !strings.Contains(ptyOutput, want) {
			t.Errorf("pty output = %q, want it to contain %q", ptyOutput, want)
		}
	}

	show := runJSON(t, ws, "show", id)
	c := show["challenge"].(map[string]any)
	if c["status"] != "verifying" {
		t.Errorf("status = %v, want verifying (T12 returns to the preceding hold status)", c["status"])
	}
	holds := show["holds"].([]any)
	if len(holds) != 1 {
		t.Fatalf("holds = %+v, want 1 entry", holds)
	}
	h := holds[0].(map[string]any)
	if h["answer"] != "because I checked" {
		t.Errorf("hold.answer = %v, want %q", h["answer"], "because I checked")
	}
}

// --- AC-37: 端末なしで approve・reject・answer を実行すると tty_required ---

func TestApprove_NoTerminalStdin_ReturnsTTYRequired(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	runJSON(t, ws, "classify", id, "--priority", "P0")
	runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, "x"))

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

	show := runJSON(t, ws, "show", id)
	c := show["challenge"].(map[string]any)
	if c["status"] != "awaiting_plan_approval" || len(show["approvals"].([]any)) != 0 {
		t.Errorf("state changed despite tty_required: %+v", show)
	}
}

func TestReject_NoTerminalStdin_ReturnsTTYRequired(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	runJSON(t, ws, "classify", id, "--priority", "P0")
	runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, "x"))

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
}

func TestAnswer_NoTerminalStdin_ReturnsTTYRequired(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "verifying")
	runJSON(t, ws, "verify", id, "--result", "uncertain", "--question", "why?")

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
}

// --- AC-38: 端末ありでも CLAUDECODE が設定されていると approve は tty_required ---

func TestApprove_ClaudeCodeEnvSet_ReturnsTTYRequiredWithoutShowingSummary(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	runJSON(t, ws, "classify", id, "--priority", "P0")
	runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, "secret plan body"))

	cmd, stdout, stderr := newChildCmd([]string{"approve", "--workspace", ws, "--json", id}, claudeCodeEnvVar+"=1")
	master, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		t.Fatalf("pty.StartWithAttrs: %v", err)
	}
	defer func() { _ = master.Close() }()
	drain := startPTYDrain(master)

	code := waitChild(t, cmd, 10*time.Second)
	ptyOutput := drain.waitDone(5 * time.Second)
	if code != 1 {
		t.Fatalf("exit=%d, want 1 (stdout=%q stderr=%q pty=%q)", code, stdout.String(), stderr.String(), ptyOutput)
	}
	if got := readErrorCode(t, stderr.String()); got != string(CodeTTYRequired) {
		t.Fatalf("error code = %q, want %q", got, CodeTTYRequired)
	}
	if strings.Contains(ptyOutput, "secret plan body") {
		t.Fatalf("pty output must not contain the summary when CLAUDECODE is set: %q", ptyOutput)
	}
}

// --- AC-44: 確認の入力として対象の ID 以外を与えると confirmation_mismatch ---

func TestApprove_MismatchedConfirmation(t *testing.T) {
	cases := []struct {
		name  string
		input []byte
	}{
		{"empty line", []byte("\n")},
		{"y", []byte("y\n")},
		{"different id", []byte("C-999\n")},
		{"eof", []byte("\x04")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := initializedWorkspace(t)
			created := runJSON(t, ws, "create", "--title", "t")
			id := created["challenge"].(map[string]any)["id"].(string)
			runJSON(t, ws, "classify", id, "--priority", "P0")
			runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, "x"))

			cmd, stdout, stderr := newChildCmd([]string{"approve", "--workspace", ws, "--json", id})
			master, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{Setsid: true, Setctty: true})
			if err != nil {
				t.Fatalf("pty.StartWithAttrs: %v", err)
			}
			defer func() { _ = master.Close() }()
			drain := startPTYDrain(master)

			if _, err := master.Write(tc.input); err != nil {
				t.Fatalf("write to master: %v", err)
			}

			code := waitChild(t, cmd, 10*time.Second)
			ptyOutput := drain.waitDone(5 * time.Second)
			if code != 1 {
				t.Fatalf("exit=%d, want 1 (stdout=%q stderr=%q pty=%q)", code, stdout.String(), stderr.String(), ptyOutput)
			}
			if got := readErrorCode(t, stderr.String()); got != string(CodeConfirmationMismatch) {
				t.Fatalf("error code = %q, want %q (stderr=%s)", got, CodeConfirmationMismatch, stderr.String())
			}

			show := runJSON(t, ws, "show", id)
			c := show["challenge"].(map[string]any)
			// create=1 -> classify=2 -> plan=3。confirmation_mismatch では不変。
			if c["status"] != "awaiting_plan_approval" || c["version"] != float64(3) {
				t.Errorf("challenge changed despite confirmation_mismatch: %+v", c)
			}
			if len(show["approvals"].([]any)) != 0 {
				t.Errorf("approvals = %+v, want none", show["approvals"])
			}
			log := runJSON(t, ws, "log", id)
			if len(log["activities"].([]any)) != 3 { // create + classify + plan の3件から増えない
				t.Errorf("activities = %+v, want unchanged count (3)", log["activities"])
			}
		})
	}
}

// --- AC-46: 要約の表示後・確認の入力前に対象が変わると conflict ---

func TestApprove_ConflictWhenChallengeEditedAfterSummaryShown(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t", "--done-criteria", "original")
	id := created["challenge"].(map[string]any)["id"].(string)
	runJSON(t, ws, "classify", id, "--priority", "P0")
	runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, "x"))

	cmd, stdout, stderr := newChildCmd([]string{"approve", "--workspace", ws, "--json", id})
	master, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		t.Fatalf("pty.StartWithAttrs: %v", err)
	}
	defer func() { _ = master.Close() }()
	drain := startPTYDrain(master)

	// 子プロセスが要約を書き終える（＝対象の版を読み終えた）まで、pty の
	// 出力に対象 ID が現れるのを待ってから、親プロセス側で edit を実行する。
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(drainSnapshot(drain), id) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the summary to appear on the pty")
		}
		time.Sleep(20 * time.Millisecond)
	}

	runJSON(t, ws, "edit", id, "--done-criteria", "changed by another process")

	if _, err := master.Write([]byte(id + "\n")); err != nil {
		t.Fatalf("write to master: %v", err)
	}

	code := waitChild(t, cmd, 10*time.Second)
	ptyOutput := drain.waitDone(5 * time.Second)
	if code != 1 {
		t.Fatalf("exit=%d, want 1 (stdout=%q stderr=%q pty=%q)", code, stdout.String(), stderr.String(), ptyOutput)
	}
	if got := readErrorCode(t, stderr.String()); got != string(CodeConflict) {
		t.Fatalf("error code = %q, want %q (stderr=%s)", got, CodeConflict, stderr.String())
	}

	show := runJSON(t, ws, "show", id)
	if len(show["approvals"].([]any)) != 0 {
		t.Errorf("approvals = %+v, want none (conflict must not record an approval)", show["approvals"])
	}
	c := show["challenge"].(map[string]any)
	if c["status"] != "awaiting_plan_approval" {
		t.Errorf("status = %v, want awaiting_plan_approval (approval must not have applied)", c["status"])
	}
}

// drainSnapshot はロックを取って ptyDrain の現時点までの内容を返す
// （waitDone と異なり、EOF や timeout を待たずポーリングに使う）。
func drainSnapshot(d *ptyDrain) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.buf.String()
}

// runConfirmedChild は args を疑似端末つきの子プロセスとして起動し、対象 ID
// （confirmInput）を確認として書き込んでから終了を待つ。#13 の新規テスト
// （D12・T14・OP-ID の承認・差し戻し）が、approval_process_test.go 冒頭の
// 成功経路テストと同じ手順を繰り返し書かずに使う共通ヘルパー。
func runConfirmedChild(t *testing.T, args []string, confirmInput string) (code int, ptyOutput, stdout, stderr string) {
	t.Helper()
	cmd, stdoutBuf, stderrBuf := newChildCmd(args)
	master, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		t.Fatalf("pty.StartWithAttrs: %v", err)
	}
	defer func() { _ = master.Close() }()
	drain := startPTYDrain(master)

	if _, err := master.Write([]byte(confirmInput + "\n")); err != nil {
		t.Fatalf("write to master: %v", err)
	}

	code = waitChild(t, cmd, 10*time.Second)
	ptyOutput = drain.waitDone(5 * time.Second)
	return code, ptyOutput, stdoutBuf.String(), stderrBuf.String()
}

// summaryLine は疑似端末の出力から prefix で始まる行（前後の空白・CR を除いた
// もの）を返す。見つからなければテストを失敗させる。self-review 指摘の再発防止:
// 「OP-ID が出力のどこかに現れる」だけの検査では、同時に承認される release と
// 承認されない不可逆操作の 2 つの一覧を入れ替えても緑のままだった。
func summaryLine(t *testing.T, ptyOutput, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(ptyOutput, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return strings.TrimSpace(line)
		}
	}
	t.Fatalf("pty output = %q, want a line starting with %q", ptyOutput, prefix)
	return ""
}

const (
	approvedReleasesPrefix      = "同時に承認される本番反映 (release):"
	notApprovedOperationsPrefix = "同時には承認されない不可逆操作:"
)

// assertOperationListing は、見出し prefix の行に wantIDs がすべて現れ、
// notWantIDs が 1 つも現れないことを確かめる。
func assertOperationListing(t *testing.T, ptyOutput, prefix string, wantIDs, notWantIDs []string) {
	t.Helper()
	line := summaryLine(t, ptyOutput, prefix)
	for _, id := range wantIDs {
		if !strings.Contains(line, id) {
			t.Errorf("line %q (%s) = want it to contain %q", line, prefix, id)
		}
	}
	for _, id := range notWantIDs {
		if strings.Contains(line, id) {
			t.Errorf("line %q (%s) = want it NOT to contain %q", line, prefix, id)
		}
	}
}

// --- D12: 完了の承認は未承認の release を同じトランザクションで一括承認する（#13） ---

func setUpChallengeAwaitingCompletionWithOperationsForCLI(t *testing.T, ws string) (id, release1, release2, del string) {
	t.Helper()
	created := runJSON(t, ws, "create", "--title", "t", "--done-criteria", "it works")
	id = created["challenge"].(map[string]any)["id"].(string)
	release1 = runJSON(t, ws, "op", "add", id, "--kind", "release", "--summary", "ship A")["operation"].(map[string]any)["id"].(string)
	release2 = runJSON(t, ws, "op", "add", id, "--kind", "release", "--summary", "ship B")["operation"].(map[string]any)["id"].(string)
	del = runJSON(t, ws, "op", "add", id, "--kind", "delete", "--summary", "remove old data")["operation"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "awaiting_completion_approval")
	return id, release1, release2, del
}

func TestApprove_Success_CompletionApprovalAutoApprovesPendingReleasesButNotOtherKinds(t *testing.T) {
	ws := initializedWorkspace(t)
	id, release1, release2, del := setUpChallengeAwaitingCompletionWithOperationsForCLI(t, ws)

	code, ptyOutput, stdout, stderr := runConfirmedChild(t, []string{"approve", "--workspace", ws, "--json", id}, id)
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stdout=%q stderr=%q pty=%q)", code, stdout, stderr, ptyOutput)
	}
	// AC「完了の承認は…同時に承認される release・同時には承認されない不可逆操作を表示する」。
	for _, want := range []string{confirmPrompt(id), "it works"} {
		if !strings.Contains(ptyOutput, want) {
			t.Errorf("pty output = %q, want it to contain %q", ptyOutput, want)
		}
	}
	// 2 つの一覧は見出しごとに分かれていること（D12・A1 の核心。どちらの行に
	// 出るかまで検査する）。
	assertOperationListing(t, ptyOutput, approvedReleasesPrefix, []string{release1, release2}, []string{del})
	assertOperationListing(t, ptyOutput, notApprovedOperationsPrefix, []string{del}, []string{release1, release2})

	show := runJSON(t, ws, "show", id)
	c := show["challenge"].(map[string]any)
	if c["status"] != "done" {
		t.Errorf("status = %v, want done", c["status"])
	}
	ops := map[string]map[string]any{}
	for _, o := range show["operations"].([]any) {
		m := o.(map[string]any)
		ops[m["id"].(string)] = m
	}
	if ops[release1]["state"] != "approved" || ops[release2]["state"] != "approved" {
		t.Errorf("releases = %+v, want both approved", ops)
	}
	if ops[del]["state"] != "pending" {
		t.Errorf("delete = %+v, want pending (unchanged; not auto-approved)", ops[del])
	}

	var completionApprovals, releaseApprovals int
	for _, a := range show["approvals"].([]any) {
		m := a.(map[string]any)
		switch m["kind"] {
		case "completion":
			completionApprovals++
		case "release":
			releaseApprovals++
			if m["operation_id"] == nil {
				t.Errorf("release approval missing operation_id: %+v", m)
			}
		}
	}
	if completionApprovals != 1 {
		t.Errorf("completion approvals = %d, want 1", completionApprovals)
	}
	if releaseApprovals != 2 {
		t.Errorf("release approvals = %d, want 2", releaseApprovals)
	}

	// AC-56: 作業ログには完了の承認と release ごとの承認が別々のエントリとして
	// （それぞれ actor・経路・本人確認の方式つきで）残る。
	log := runJSON(t, ws, "log", id)
	var challengeApproveEntries, operationApproveEntries int
	for _, e := range log["activities"].([]any) {
		m := e.(map[string]any)
		if m["entity"] == "challenge" && m["action"] == "approve" {
			challengeApproveEntries++
			if m["verification"] != "tty_confirm" || m["actor"] == "" {
				t.Errorf("challenge approve entry missing actor/verification: %+v", m)
			}
		}
		if m["entity"] == "operation" && m["action"] == "approve" {
			operationApproveEntries++
			if m["verification"] != "tty_confirm" || m["actor"] == "" {
				t.Errorf("operation approve entry missing actor/verification: %+v", m)
			}
		}
	}
	if challengeApproveEntries != 1 {
		t.Errorf("challenge-entity approve entries = %d, want 1", challengeApproveEntries)
	}
	if operationApproveEntries != 2 {
		t.Errorf("operation-entity approve entries = %d, want 2", operationApproveEntries)
	}
}

// T14: `approve --hold-release` は完了だけを承認し release は未承認のまま残す。
func TestApprove_Success_CompletionApprovalWithHoldRelease(t *testing.T) {
	ws := initializedWorkspace(t)
	id, release1, release2, _ := setUpChallengeAwaitingCompletionWithOperationsForCLI(t, ws)

	code, ptyOutput, stdout, stderr := runConfirmedChild(t, []string{"approve", "--hold-release", "--workspace", ws, "--json", id}, id)
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stdout=%q stderr=%q pty=%q)", code, stdout, stderr, ptyOutput)
	}
	if !strings.Contains(ptyOutput, confirmPrompt(id)) {
		t.Errorf("pty output = %q, want it to contain the confirm prompt", ptyOutput)
	}
	// self-review 指摘の再発防止: --hold-release では release は 1 件も承認
	// されないため、要約も「同時に承認される」側へ出してはならない（本人確認の
	// 要約は人間が承認判断の根拠にする唯一のテキスト＝H9）。
	assertOperationListing(t, ptyOutput, approvedReleasesPrefix, nil, []string{release1, release2})
	assertOperationListing(t, ptyOutput, notApprovedOperationsPrefix, []string{release1, release2}, nil)

	show := runJSON(t, ws, "show", id)
	c := show["challenge"].(map[string]any)
	if c["status"] != "done" {
		t.Errorf("status = %v, want done", c["status"])
	}
	ops := map[string]map[string]any{}
	for _, o := range show["operations"].([]any) {
		m := o.(map[string]any)
		ops[m["id"].(string)] = m
	}
	if ops[release1]["state"] != "pending" || ops[release2]["state"] != "pending" {
		t.Errorf("releases = %+v, want both still pending (--hold-release)", ops)
	}
	for _, a := range show["approvals"].([]any) {
		m := a.(map[string]any)
		if m["kind"] == "release" {
			t.Errorf("unexpected release approval despite --hold-release: %+v", m)
		}
	}
}

// AC-46（D12 の conflict の一方）: 完了の承認の要約表示後・確認の入力前に、
// 別のプロセスが対象の release を approve <OP-ID> で先に承認すると、完了の
// 承認は成立しない。
func TestApprove_ConflictWhenReleaseApprovedStandaloneDuringCompletionApprovalWindow(t *testing.T) {
	ws := initializedWorkspace(t)
	id, release1, _, _ := setUpChallengeAwaitingCompletionWithOperationsForCLI(t, ws)

	cmd, stdout, stderr := newChildCmd([]string{"approve", "--workspace", ws, "--json", id})
	master, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		t.Fatalf("pty.StartWithAttrs: %v", err)
	}
	defer func() { _ = master.Close() }()
	drain := startPTYDrain(master)

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(drainSnapshot(drain), id) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the summary to appear on the pty")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 別プロセス: 対象の release を単独で先に承認する（別の子プロセス・別の
	// 疑似端末で approve <OP-ID> を成立させる）。
	opCode, opPTY, opStdout, opStderr := runConfirmedChild(t, []string{"approve", "--workspace", ws, "--json", release1}, release1)
	if opCode != 0 {
		t.Fatalf("standalone approve <OP-ID> exit=%d, want 0 (stdout=%q stderr=%q pty=%q)", opCode, opStdout, opStderr, opPTY)
	}

	if _, err := master.Write([]byte(id + "\n")); err != nil {
		t.Fatalf("write to master: %v", err)
	}

	code := waitChild(t, cmd, 10*time.Second)
	ptyOutput := drain.waitDone(5 * time.Second)
	if code != 1 {
		t.Fatalf("exit=%d, want 1 (stdout=%q stderr=%q pty=%q)", code, stdout.String(), stderr.String(), ptyOutput)
	}
	if got := readErrorCode(t, stderr.String()); got != string(CodeConflict) {
		t.Fatalf("error code = %q, want %q (stderr=%s)", got, CodeConflict, stderr.String())
	}

	show := runJSON(t, ws, "show", id)
	c := show["challenge"].(map[string]any)
	if c["status"] != "awaiting_completion_approval" {
		t.Errorf("status = %v, want awaiting_completion_approval (completion must not have applied)", c["status"])
	}
	for _, a := range show["approvals"].([]any) {
		m := a.(map[string]any)
		if m["kind"] == "completion" {
			t.Errorf("unexpected completion approval despite conflict: %+v", m)
		}
	}
}

// AC-46（D12 の conflict のもう一方）: 完了の承認の要約表示後・確認の入力前に、
// 別のプロセスが op add で不可逆操作を足すと、完了の承認は成立しない。
func TestApprove_ConflictWhenOpAddHappensDuringCompletionApprovalWindow(t *testing.T) {
	ws := initializedWorkspace(t)
	id, _, _, _ := setUpChallengeAwaitingCompletionWithOperationsForCLI(t, ws)

	cmd, stdout, stderr := newChildCmd([]string{"approve", "--workspace", ws, "--json", id})
	master, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		t.Fatalf("pty.StartWithAttrs: %v", err)
	}
	defer func() { _ = master.Close() }()
	drain := startPTYDrain(master)

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(drainSnapshot(drain), id) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the summary to appear on the pty")
		}
		time.Sleep(20 * time.Millisecond)
	}

	runJSON(t, ws, "op", "add", id, "--kind", "release", "--summary", "late addition")

	if _, err := master.Write([]byte(id + "\n")); err != nil {
		t.Fatalf("write to master: %v", err)
	}

	code := waitChild(t, cmd, 10*time.Second)
	ptyOutput := drain.waitDone(5 * time.Second)
	if code != 1 {
		t.Fatalf("exit=%d, want 1 (stdout=%q stderr=%q pty=%q)", code, stdout.String(), stderr.String(), ptyOutput)
	}
	if got := readErrorCode(t, stderr.String()); got != string(CodeConflict) {
		t.Fatalf("error code = %q, want %q (stderr=%s)", got, CodeConflict, stderr.String())
	}
}

// --- 不可逆操作の単独の承認・差し戻し（approve <OP-ID> / reject <OP-ID>）（#13） ---

func TestApproveOperationID_Success(t *testing.T) {
	ws := initializedWorkspace(t)
	// タイトルは他の出力（"state"・"tty" など）に偶然含まれない語にする
	// （self-review 指摘: "t" 1 文字では何を出力しても通る空虚な検査だった）。
	const title = "deploy the billing job"
	created := runJSON(t, ws, "create", "--title", title)
	id := created["challenge"].(map[string]any)["id"].(string)
	op := runJSON(t, ws, "op", "add", id, "--kind", "release", "--summary", "ship it", "--ref", "https://example.com/pr/9")
	opID := op["operation"].(map[string]any)["id"].(string)

	code, ptyOutput, stdout, stderr := runConfirmedChild(t, []string{"approve", "--workspace", ws, "--json", opID}, opID)
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stdout=%q stderr=%q pty=%q)", code, stdout, stderr, ptyOutput)
	}
	// AC: 操作の ID・種類・要約・参照と、課題の ID・タイトル・状態を表示する。
	for _, want := range []string{confirmPrompt(opID), "release", "ship it", "https://example.com/pr/9", id, title} {
		if !strings.Contains(ptyOutput, want) {
			t.Errorf("pty output = %q, want it to contain %q", ptyOutput, want)
		}
	}
	// 課題の状態の行（AC の「状態」。要約から落ちても気付けるよう、行ごと検査する）。
	statusLine := summaryLine(t, ptyOutput, "課題の状態:")
	wantStatus := runJSON(t, ws, "show", id)["challenge"].(map[string]any)["status"].(string)
	if !strings.Contains(statusLine, wantStatus) {
		t.Errorf("challenge status line = %q, want it to contain the challenge status %q", statusLine, wantStatus)
	}
	// stdout は成功時の JSON 出力（operation.summary を含む）であり、summary が
	// そこに含まれること自体は「成功時の JSON 出力の規約」どおりで問題ない
	// （approve <C-ID> の計画本文のように、要約専用の情報ではない）。

	show := runJSON(t, ws, "show", id)
	var found map[string]any
	for _, o := range show["operations"].([]any) {
		m := o.(map[string]any)
		if m["id"] == opID {
			found = m
		}
	}
	if found == nil || found["state"] != "approved" {
		t.Errorf("operation = %+v, want state=approved", found)
	}
	// approve <OP-ID> でも課題の版は1増える（親要件チケット #4 §アーキテクチャ決定）。
	c := show["challenge"].(map[string]any)
	// create=1 -> op add=2 -> approve <OP-ID>=3。
	if c["version"] != float64(3) {
		t.Errorf("challenge version = %v, want 3", c["version"])
	}
}

func TestRejectOperationID_Success(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	op := runJSON(t, ws, "op", "add", id, "--kind", "external_send", "--summary", "notify partner")
	opID := op["operation"].(map[string]any)["id"].(string)

	cmd, stdout, stderr := newChildCmd([]string{"reject", "--workspace", ws, "--json", "--reason", "not yet", opID})
	master, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		t.Fatalf("pty.StartWithAttrs: %v", err)
	}
	defer func() { _ = master.Close() }()
	drain := startPTYDrain(master)

	if _, err := master.Write([]byte(opID + "\n")); err != nil {
		t.Fatalf("write to master: %v", err)
	}

	code := waitChild(t, cmd, 10*time.Second)
	ptyOutput := drain.waitDone(5 * time.Second)
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stdout=%q stderr=%q pty=%q)", code, stdout.String(), stderr.String(), ptyOutput)
	}
	for _, want := range []string{confirmPrompt(opID), "not yet"} {
		if !strings.Contains(ptyOutput, want) {
			t.Errorf("pty output = %q, want it to contain %q", ptyOutput, want)
		}
	}

	show := runJSON(t, ws, "show", id)
	var found map[string]any
	for _, o := range show["operations"].([]any) {
		m := o.(map[string]any)
		if m["id"] == opID {
			found = m
		}
	}
	if found == nil || found["state"] != "rejected" {
		t.Errorf("operation = %+v, want state=rejected", found)
	}
	var rejectedApproval map[string]any
	for _, a := range show["approvals"].([]any) {
		m := a.(map[string]any)
		if m["operation_id"] == opID {
			rejectedApproval = m
		}
	}
	if rejectedApproval == nil || rejectedApproval["reason"] != "not yet" || rejectedApproval["decision"] != "rejected" {
		t.Errorf("approval = %+v, want decision=rejected reason='not yet'", rejectedApproval)
	}
}

// 未承認の release は課題が完了した後でも単独に承認できる（保留した本番反映のため）。
func TestApproveOperationID_Success_AfterChallengeIsDone(t *testing.T) {
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t")
	id := created["challenge"].(map[string]any)["id"].(string)
	op := runJSON(t, ws, "op", "add", id, "--kind", "release", "--summary", "s")
	opID := op["operation"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "done")

	code, ptyOutput, stdout, stderr := runConfirmedChild(t, []string{"approve", "--workspace", ws, "--json", opID}, opID)
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stdout=%q stderr=%q pty=%q)", code, stdout, stderr, ptyOutput)
	}

	show := runJSON(t, ws, "show", id)
	if m := findOperation(t, show, opID); m["state"] != "approved" {
		t.Errorf("operation = %+v, want state=approved", m)
	}
}
