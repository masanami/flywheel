package cli

import (
	"bytes"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// `flywheel budget`（M3 S2 #105。AC-342〜346・364）と、status.needs_human.budget_exhausted（AC-339）。

func createExhaustedForCase(t *testing.T, ws string, implUSD string) string {
	t.Helper()
	id := createForCase(t, ws)
	spec := `{"verdict":"plan","size":"M","budget_impl_usd":` + implUSD + `,"budget_review_usd":30}`
	coretest.InsertApprovedPlan(t, ws, challengeIDToInternalID(t, id), "PLAN-BODY", spec)
	return id
}

func budgetExhaustedOf(t *testing.T, ws string) []any {
	t.Helper()
	out := runJSON(t, ws, "status")
	doc := loadDocumentedJSON(t)
	assertDocumentedJSON(t, doc, "status", out)
	assertDocumentedEntities(t, doc, "status", out)
	return out["needs_human"].(map[string]any)["budget_exhausted"].([]any)
}

// 端末から本人確認を通すと枠が置き換わり、承認の種類 budget の記録が残り、budget_exhausted から外れる。
func TestBudget_ConfirmedFromATerminalReplacesTheBucketsAndLeavesAnApproval(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createExhaustedForCase(t, ws, "0.5")

	be := budgetExhaustedOf(t, ws)
	if len(be) != 1 {
		t.Fatalf("budget_exhausted = %v, want one entry", be)
	}
	e := be[0].(map[string]any)
	if e["challenge_id"] != id || e["plan_version"] != float64(1) || e["impl_remaining_usd"] != 0.5 || e["review_remaining_usd"] != float64(30) {
		t.Errorf("budget_exhausted[0] = %v", e)
	}

	code, pty, stdout, stderr := runConfirmedChild(t, []string{"budget", id, "--impl-usd", "80", "--review-usd", "20", "--workspace", ws, "--json"}, id)
	if code != 0 {
		t.Fatalf("exit=%d (stdout=%q stderr=%q pty=%q)", code, stdout, stderr, pty)
	}
	for _, want := range []string{"予算（budget）", "v1", "置き換え後の実装枠", "80 USD", "20 USD"} {
		if !strings.Contains(pty, want) {
			t.Errorf("summary lacks %q:\n%s", want, pty)
		}
	}
	if be := budgetExhaustedOf(t, ws); len(be) != 0 {
		t.Errorf("budget_exhausted after the raise = %v, want []", be)
	}
	show := runJSON(t, ws, "show", id)
	var kinds []string
	for _, a := range show["approvals"].([]any) {
		m := a.(map[string]any)
		kinds = append(kinds, m["kind"].(string))
		if m["kind"] == "budget" && (m["decision"] != "approved" || m["verification"] != "tty_confirm" || m["target_version"] != float64(1)) {
			t.Errorf("budget approval = %v", m)
		}
	}
	if strings.Join(kinds, ",") != "plan,budget" {
		t.Errorf("approval kinds = %v, want plan,budget", kinds)
	}
	if st := show["challenge"].(map[string]any)["status"]; st != "in_progress" {
		t.Errorf("status = %v, want in_progress (unchanged)", st)
	}
}

// 標準入力が端末でなければ拒否し、枠は変わらない。
func TestBudget_NoTerminalStdinIsRejectedAndLeavesTheBucketsUnchanged(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createExhaustedForCase(t, ws, "0.5")
	cmd, stdout, stderr := newChildCmd([]string{"budget", id, "--impl-usd", "80", "--workspace", ws, "--json"})
	cmd.Stdin = strings.NewReader(id + "\n")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if code := waitChild(t, cmd, 10*time.Second); code != 1 {
		t.Fatalf("exit=%d, want 1 (stdout=%q stderr=%q)", code, stdout.String(), stderr.String())
	}
	if got := readErrorCode(t, stderr.String()); got != string(CodeTTYRequired) {
		t.Fatalf("error code = %q, want %q", got, CodeTTYRequired)
	}
	assertBudgetUnchanged(t, ws, id)
}

// 端末があっても CLAUDECODE が設定されていれば拒否し、要約を端末に出さず、枠は変わらない。
func TestBudget_ClaudeCodeEnvIsRejectedAndLeavesTheBucketsUnchanged(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createExhaustedForCase(t, ws, "0.5")
	cmd, stdout, stderr := newChildCmd([]string{"budget", id, "--impl-usd", "80", "--workspace", ws, "--json"}, claudeCodeEnvVar+"=1")
	master, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		t.Fatalf("pty.StartWithAttrs: %v", err)
	}
	defer func() { _ = master.Close() }()
	drain := startPTYDrain(master)
	code := waitChild(t, cmd, 10*time.Second)
	ptyOutput := drain.waitDone(5 * time.Second)
	if code != 1 || readErrorCode(t, stderr.String()) != string(CodeTTYRequired) {
		t.Fatalf("exit=%d stderr=%q stdout=%q, want 1 / tty_required", code, stderr.String(), stdout.String())
	}
	if strings.Contains(ptyOutput, "置き換え後") {
		t.Errorf("the summary must not be shown when CLAUDECODE is set: %q", ptyOutput)
	}
	assertBudgetUnchanged(t, ws, id)
}

func assertBudgetUnchanged(t *testing.T, ws, id string) {
	t.Helper()
	if be := budgetExhaustedOf(t, ws); len(be) != 1 {
		t.Errorf("budget_exhausted = %v, want the challenge still listed", be)
	}
	for _, a := range runJSON(t, ws, "show", id)["approvals"].([]any) {
		if a.(map[string]any)["kind"] == "budget" {
			t.Errorf("a budget approval was recorded: %v", a)
		}
	}
}

// --impl-usd の無い budget は終了コード 2・usage_error（ストアは変わらない）。
func TestBudget_WithoutImplUSDIsUsageError(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createExhaustedForCase(t, ws, "0.5")
	for _, args := range [][]string{
		{"budget", id},
		{"budget", id, "--review-usd", "20"},
	} {
		var stdout, stderr bytes.Buffer
		code := run(append(append([]string{}, args...), "--workspace", ws, "--json"), strings.NewReader(""), &stdout, &stderr, defaultCommands())
		if code != 2 || readErrorCode(t, stderr.String()) != string(CodeUsageError) {
			t.Errorf("args=%v exit=%d stderr=%q, want 2 / usage_error", args, code, stderr.String())
		}
	}
	assertBudgetUnchanged(t, ws, id)
}

// 0 以下の額は端末を開く前に validation_failed で拒否する。
func TestBudget_NonPositiveAmountIsRejectedBeforeAskingForConfirmation(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createExhaustedForCase(t, ws, "0.5")
	for _, amount := range []string{"0", "-5"} {
		var stdout, stderr bytes.Buffer
		code := run([]string{"budget", id, "--impl-usd=" + amount, "--workspace", ws, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
		if code != 1 || readErrorCode(t, stderr.String()) != string(CodeValidationFailed) {
			t.Errorf("--impl-usd %s: exit=%d stderr=%q, want 1 / validation_failed", amount, code, stderr.String())
		}
	}
	assertBudgetUnchanged(t, ws, id)
}
