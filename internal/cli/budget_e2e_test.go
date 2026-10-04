package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 受け入れの通し（M3 S2 #105。AC-218・263・264・336〜346）: 実ストア・実物の git スロット・偽の claude で
// `run` → budget_exhausted → `status` → `run`（止まる）→ `flywheel budget`（端末で本人確認）→ `run`（--resume）。

func budgetE2ERoutes(argvLog, stdinLog string) []fakeClaudeRoute {
	exhausted := fakeClaudeRoute{Match: fakeClaudeDelegateMatch, Tag: "DELEGATE",
		Stdout:      `{"session_id":"s","subtype":"error_max_budget_usd","is_error":true,"total_cost_usd":10}`,
		ArgvLogPath: argvLog, StdinLogPath: stdinLog}
	return []fakeClaudeRoute{exhausted, j3Route("BRIEF-E2E")}
}

func TestBudgetE2E_ExhaustedStopsThenRaisedResumesTheSameSession(t *testing.T) {
	ws := setupRunWorkspace(t)
	id := newInProgressForRun(t, ws, func(m map[string]any) {
		m["budget_impl_usd"] = 50
		m["budget_review_usd"] = 30
	})
	argvLog := filepath.Join(t.TempDir(), "argv.log")
	stdinLog := filepath.Join(t.TempDir(), "stdin.log")
	putRoutedFakeClaudeOnPATH(t, budgetE2ERoutes(argvLog, stdinLog), "")

	// ① 1 回目: 上限は実装枠の残りだけ（50。レビュー対応枠の 30 を足した 80 ではない）。
	phase := phaseOf(t, runJSON(t, ws, "run"))
	items := phase["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["result"] != "budget_exhausted" {
		t.Fatalf("first run phase = %v, want one budget_exhausted item", phase)
	}
	argv := readFileForBudgetE2E(t, argvLog)
	if !strings.Contains(argv, "--max-budget-usd 50") || strings.Contains(argv, "--resume") {
		t.Errorf("first argv = %q, want --max-budget-usd 50 and no --resume", argv)
	}
	if st := runJSON(t, ws, "show", id)["challenge"].(map[string]any)["status"]; st != "in_progress" {
		t.Fatalf("status = %v, want in_progress (unchanged)", st)
	}

	// ② status に出る（実装枠の残りは 0 以下として扱う）。
	be := budgetExhaustedOf(t, ws)
	if len(be) != 1 || be[0].(map[string]any)["challenge_id"] != id || be[0].(map[string]any)["impl_remaining_usd"] != float64(0) ||
		be[0].(map[string]any)["review_remaining_usd"] != float64(30) {
		t.Fatalf("budget_exhausted = %v", be)
	}

	// ③ 次の周は起動しない（run_budget）。ID を指定した run は budget_exceeded（終了コード 1）。
	phase = phaseOf(t, runJSON(t, ws, "run"))
	ns := phase["not_started"].([]any)
	if len(ns) != 1 || ns[0].(map[string]any)["reason"] != "run_budget" || len(phase["items"].([]any)) != 0 {
		t.Fatalf("second run phase = %v, want run_budget and nothing launched", phase)
	}
	requireJSONErrorEnvelope(t, []string{"run", id, "--workspace", ws}, 1, CodeBudgetExceeded)
	if n := strings.Count(readFileForBudgetE2E(t, argvLog), "\n"); n != 1 {
		t.Fatalf("delegations launched = %d, want 1", n)
	}

	// ④ 端末で本人確認して増額する（実装枠だけ）。
	code, ptyOut, stdout, stderr := runConfirmedChild(t, []string{"budget", id, "--impl-usd", "80", "--workspace", ws, "--json"}, id)
	if code != 0 {
		t.Fatalf("budget exit=%d (stdout=%q stderr=%q pty=%q)", code, stdout, stderr, ptyOut)
	}
	if be := budgetExhaustedOf(t, ws); len(be) != 0 {
		t.Fatalf("budget_exhausted after the raise = %v, want []", be)
	}

	// ⑤ 次の委譲は、その run の session_id へ --resume し、固定の文面で続行を求める。上限は 80 − 10。
	phase = phaseOf(t, runJSON(t, ws, "run"))
	if len(phase["items"].([]any)) != 1 {
		t.Fatalf("run after the raise: phase = %v, want a launch", phase)
	}
	runs := delegateRunsOf(t, ws)
	if len(runs) != 2 {
		t.Fatalf("delegate runs = %d, want 2", len(runs))
	}
	lines := strings.Split(strings.TrimSpace(readFileForBudgetE2E(t, argvLog)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], "--resume "+runs[0]["session_id"].(string)) || !strings.Contains(lines[1], "--max-budget-usd 70") {
		t.Errorf("resume argv = %q, want --resume <first session> and --max-budget-usd 70", lines)
	}
	if strings.Contains(lines[1], "--session-id") {
		t.Errorf("a resume must not start a new session: %q", lines[1])
	}
	stdin := readFileForBudgetE2E(t, stdinLog)
	if !strings.Contains(stdin, "費用の上限") || !strings.Contains(stdin, "続けて終わらせる") {
		t.Errorf("resume stdin lacks the fixed budget text:\n%s", stdin)
	}
}

func readFileForBudgetE2E(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
