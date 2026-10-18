package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// planPendingChallenge は計画承認待ちの課題を作って ID を返す。
func planPendingChallenge(t *testing.T, ws string) string {
	t.Helper()
	created := runJSON(t, ws, "create", "--title", "t", "--done-criteria", "ok")
	id := created["challenge"].(map[string]any)["id"].(string)
	runJSON(t, ws, "classify", id, "--priority", "P0")
	planPath := filepath.Join(t.TempDir(), "plan.txt")
	if err := os.WriteFile(planPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	runJSON(t, ws, "plan", id, "--file", planPath)
	return id
}

func workStatus(t *testing.T, ws, id string) string {
	t.Helper()
	return runJSON(t, ws, "show", id)["challenge"].(map[string]any)["status"].(string)
}

func TestRelayed_ApproveRecordsEvidenceWithoutTTYEvenWithClaudeCode(t *testing.T) {
	t.Setenv(claudeCodeEnvVar, "1")
	ws := initializedWorkspace(t)
	id := planPendingChallenge(t, ws)

	runJSON(t, ws, "approve", id, "--relayed", "--session", "sess-1", "--quote", "承認してください")

	if got := workStatus(t, ws, id); got == "awaiting_plan_approval" {
		t.Fatalf("status = %q, approve did not take effect", got)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"log", id, "--workspace", ws, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands()); code != 0 {
		t.Fatalf("log exit=%d %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"relayed", "sess-1", "承認してください"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q: %s", want, out)
		}
	}
}

func TestRelayed_UsageErrorsWriteNothing(t *testing.T) {
	ws := initializedWorkspace(t)
	id := planPendingChallenge(t, ws)
	cases := [][]string{
		{"approve", id, "--relayed"},
		{"approve", id, "--relayed", "--session", "s"},
		{"approve", id, "--relayed", "--quote", "q"},
		{"approve", id, "--relayed", "--session", "  ", "--quote", "q"},
		{"approve", id, "--relayed", "--session", "s", "--quote", " "},
		{"reject", id, "--reason", "r", "--relayed", "--session", "s", "--quote", ""},
		{"approve", id, "--session", "s"},
		{"approve", id, "--quote", "q"},
		{"reject", id, "--reason", "r", "--session", "s"},
		{"answer", id, "--answer", "a", "--relayed", "--session", "s"},
		{"budget", id, "--impl-usd", "1", "--quote", "q"},
		{"approve", "OP-1", "--relayed"},
	}
	dbPath := filepath.Join(ws, ".flywheel", "flywheel.db")
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		requireErrorCode(t, append(append([]string{}, c...), "--workspace", ws), 2, CodeUsageError)
	}
	after, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("usage_error must not change the store file")
	}
	if got := workStatus(t, ws, id); got != "awaiting_plan_approval" {
		t.Errorf("status = %q, want unchanged", got)
	}
}

func TestRelayed_RejectRecordsAndNonTTYApproveStillTTYRequired(t *testing.T) {
	ws := initializedWorkspace(t)
	id := planPendingChallenge(t, ws)
	requireErrorCode(t, []string{"approve", id, "--workspace", ws}, 1, CodeTTYRequired)
	requireErrorCode(t, []string{"reject", id, "--reason", "no", "--workspace", ws}, 1, CodeTTYRequired)

	runJSON(t, ws, "reject", id, "--reason", "no", "--relayed", "--session", "s-r", "--quote", "q-r")
	if got := workStatus(t, ws, id); got == "awaiting_plan_approval" {
		t.Errorf("status = %q, reject did not take effect", got)
	}
	requireRelayedRecorded(t, ws, "s-r", "q-r")
}

func TestRelayedVerifier_Contract(t *testing.T) {
	v := &relayedVerifier{evidence: core.RelayEvidence{Session: "s", Quote: "q"}}
	if v.Method() != core.VerificationRelayed || v.Evidence().Session != "s" || v.Evidence().Quote != "q" {
		t.Fatalf("unexpected verifier: %+v", v)
	}
	actor, err := v.Confirm("summary", "C-1")
	if err != nil {
		t.Fatal(err)
	}
	if want := resolveExpectedActor(t); actor != want {
		t.Errorf("actor = %q, want %q", actor, want)
	}
}

// relayedLog は課題・操作の ID を問わない作業ログ全体の JSON 文字列を返す。
func relayedLog(t *testing.T, ws string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"log", "--workspace", ws, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands()); code != 0 {
		t.Fatalf("log exit=%d %s", code, stderr.String())
	}
	return stdout.String()
}

func requireRelayedRecorded(t *testing.T, ws, session, quote string) {
	t.Helper()
	out := relayedLog(t, ws)
	for _, want := range []string{"relayed", session, quote} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q: %s", want, out)
		}
	}
}

// relayed の成功経路が、コマンドごとに relayed の Verifier を使っていること
// （tty_confirm に戻っていれば、端末の無いこのテストでは tty_required で落ちる）。
func TestRelayed_SuccessForEveryCommand(t *testing.T) {
	t.Setenv(claudeCodeEnvVar, "1")
	rel := func(sess, quote string) []string { return []string{"--relayed", "--session", sess, "--quote", quote} }

	t.Run("reject", func(t *testing.T) {
		ws := initializedWorkspace(t)
		id := planPendingChallenge(t, ws)
		runJSON(t, ws, append([]string{"reject", id, "--reason", "no"}, rel("sess-rej", "q-rej")...)...)
		if got := workStatus(t, ws, id); got == "awaiting_plan_approval" {
			t.Errorf("status = %q", got)
		}
		requireRelayedRecorded(t, ws, "sess-rej", "q-rej")
	})
	t.Run("answer", func(t *testing.T) {
		ws := initializedWorkspace(t)
		id := createForCase(t, ws)
		coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "verifying")
		runJSON(t, ws, "verify", id, "--result", "uncertain", "--question", "why?")
		runJSON(t, ws, append([]string{"answer", id, "--answer", "because"}, rel("sess-ans", "q-ans")...)...)
		if got := workStatus(t, ws, id); got != "verifying" {
			t.Errorf("status = %q, want verifying", got)
		}
		requireRelayedRecorded(t, ws, "sess-ans", "q-ans")
	})
	t.Run("budget", func(t *testing.T) {
		ws := initializedWorkspace(t)
		id := createBudgetTargetForCase(t, ws)
		runJSON(t, ws, append([]string{"budget", id, "--impl-usd", "80"}, rel("sess-bud", "q-bud")...)...)
		requireRelayedRecorded(t, ws, "sess-bud", "q-bud")
	})
	t.Run("approve and reject OP-ID", func(t *testing.T) {
		ws := initializedWorkspace(t)
		id := createForCase(t, ws)
		op1 := runJSON(t, ws, "op", "add", id, "--kind", "release", "--summary", "a")["operation"].(map[string]any)["id"].(string)
		op2 := runJSON(t, ws, "op", "add", id, "--kind", "release", "--summary", "b")["operation"].(map[string]any)["id"].(string)
		runJSON(t, ws, append([]string{"approve", op1}, rel("sess-op1", "q-op1")...)...)
		runJSON(t, ws, append([]string{"reject", op2, "--reason", "no"}, rel("sess-op2", "q-op2")...)...)
		requireRelayedRecorded(t, ws, "sess-op1", "q-op1")
		requireRelayedRecorded(t, ws, "sess-op2", "q-op2")
	})
	t.Run("approve completion with and without --hold-release", func(t *testing.T) {
		for _, hold := range []bool{false, true} {
			ws := initializedWorkspace(t)
			id, _, _, _ := setUpChallengeAwaitingCompletionWithOperationsForCLI(t, ws)
			args := append([]string{"approve", id}, rel("sess-c", "q-c")...)
			if hold {
				args = append(args, "--hold-release")
			}
			runJSON(t, ws, args...)
			if got := workStatus(t, ws, id); got == "awaiting_completion_approval" {
				t.Errorf("hold=%v status = %q", hold, got)
			}
			requireRelayedRecorded(t, ws, "sess-c", "q-c")
		}
	})
	t.Run("without --relayed a non-terminal is still tty_required", func(t *testing.T) {
		ws := initializedWorkspace(t)
		id := planPendingChallenge(t, ws)
		requireErrorCode(t, []string{"reject", id, "--reason", "no", "--workspace", ws}, 1, CodeTTYRequired)
	})
}

func TestRelayed_DelegatedEnvRejectsEvenWithRelayed(t *testing.T) {
	ws := initializedWorkspace(t)
	id := planPendingChallenge(t, ws)
	t.Setenv(core.DelegatedRunEnvVar, "run-1")
	requireErrorCode(t, []string{"approve", id, "--relayed", "--session", "s", "--quote", "q", "--workspace", ws}, 1, CodeVerificationRejected)
	if err := os.Unsetenv(core.DelegatedRunEnvVar); err != nil {
		t.Fatal(err)
	}
	if got := workStatus(t, ws, id); got != "awaiting_plan_approval" {
		t.Errorf("status = %q, want unchanged", got)
	}
}
