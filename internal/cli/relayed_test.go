package cli

import (
	"bytes"
	"encoding/json"
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

// activityAfters は log --json のうち action が action の作業ログの after を返す。
func activityAfters(t *testing.T, ws, action string) []map[string]any {
	t.Helper()
	var doc struct {
		Activities []struct {
			Action string         `json:"action"`
			After  map[string]any `json:"after"`
		} `json:"activities"`
	}
	if err := json.Unmarshal([]byte(relayedLog(t, ws)), &doc); err != nil {
		t.Fatalf("log is not JSON: %v", err)
	}
	var out []map[string]any
	for _, a := range doc.Activities {
		if a.Action == action && a.After != nil {
			out = append(out, a.After)
		}
	}
	return out
}

// AC-230: 完了の承認を --relayed --hold-release で受けた後も、未承認の release は未承認のまま残る。
func TestRelayed_AC230_HoldReleaseLeavesReleasePending(t *testing.T) {
	t.Setenv(claudeCodeEnvVar, "1")
	ws := initializedWorkspace(t)
	id, release1, release2, del := setUpChallengeAwaitingCompletionWithOperationsForCLI(t, ws)

	runJSON(t, ws, "approve", id, "--relayed", "--session", "sess-hold", "--quote", "release は止めて", "--hold-release")

	if got := workStatus(t, ws, id); got != "done" {
		t.Fatalf("status = %q, want done (completion approved)", got)
	}
	states := map[string]string{}
	for _, o := range runJSON(t, ws, "show", id)["operations"].([]any) {
		m := o.(map[string]any)
		states[m["id"].(string)] = m["state"].(string)
	}
	for _, op := range []string{release1, release2, del} {
		if states[op] != "pending" {
			t.Errorf("operation %s state = %q, want pending (kept by --hold-release)", op, states[op])
		}
	}
	requireRelayedRecorded(t, ws, "sess-hold", "release は止めて")
}

// AC-246: 予算の使い切りの課題に budget --relayed で実装の枠を増やすと、使い切りから外れる
// （ストアに保存された枠を status で読み直して確かめる）。
func TestRelayed_AC246_BudgetRaisesImplBudget(t *testing.T) {
	t.Setenv(claudeCodeEnvVar, "1")
	ws := initializedWorkspace(t)
	id := createExhaustedForCase(t, ws, "0.5")
	if be := budgetExhaustedOf(t, ws); len(be) != 1 {
		t.Fatalf("budget_exhausted = %v, want one entry before the budget", be)
	}

	got := runJSON(t, ws, "budget", id, "--impl-usd", "80", "--relayed", "--session", "sess-bud", "--quote", "80 に増やして")
	if got["impl_usd"] != float64(80) {
		t.Fatalf("impl_usd = %v, want 80", got["impl_usd"])
	}
	if be := budgetExhaustedOf(t, ws); len(be) != 0 {
		t.Fatalf("budget_exhausted = %v, want none after raising the impl budget", be)
	}
	afters := activityAfters(t, ws, "approve")
	var found bool
	for _, a := range afters {
		if a["approval_kind"] == "budget" {
			found = true
			if a["impl_budget_usd"] != float64(80) || a["relay_session"] != "sess-bud" || a["relay_quote"] != "80 に増やして" {
				t.Errorf("budget log after = %v", a)
			}
		}
	}
	if !found {
		t.Fatalf("no budget approval in the log: %v", afters)
	}
}

// 差し戻し（reject --relayed）の作業ログの after に relay_session・relay_quote が載る。
func TestRelayed_RejectLogAfterCarriesEvidence(t *testing.T) {
	t.Setenv(claudeCodeEnvVar, "1")
	ws := initializedWorkspace(t)
	id := planPendingChallenge(t, ws)

	runJSON(t, ws, "reject", id, "--reason", "no", "--relayed", "--session", "sess-rej", "--quote", "差し戻して")

	var found bool
	for _, a := range activityAfters(t, ws, "reject") {
		if a["relay_session"] == "sess-rej" && a["relay_quote"] == "差し戻して" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no reject log after carries relay_session/relay_quote: %s", relayedLog(t, ws))
	}
}
