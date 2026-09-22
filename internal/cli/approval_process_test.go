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
