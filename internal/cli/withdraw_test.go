package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは課題の取り下げ（docs/features/m4.5-learning-and-self-improvement.md S4。
// M45H13・M45H28・M45H29）の CLI の受入基準を確かめる。本人確認は --relayed で通す
// （端末の確認の経路は allcommands・worklog の登録表テストが疑似端末で確かめる）。

func withdrawRelayed(id string, extra ...string) []string {
	return append([]string{"withdraw", id, "--reason", "採らない", "--relayed", "--session", "s1", "--quote", "q1"}, extra...)
}

func activitiesOf(t *testing.T, ws, id string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, a := range runJSON(t, ws, "log", id)["activities"].([]any) {
		out = append(out, a.(map[string]any))
	}
	return out
}

func statusOf(t *testing.T, ws, id string) string {
	t.Helper()
	return runJSON(t, ws, "show", id)["challenge"].(map[string]any)["status"].(string)
}

func TestWithdrawCommand_MatchesM45IFAPITable(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "features", "m4.5-learning-and-self-improvement.md"))
	if err != nil {
		t.Fatalf("read m4.5 spec: %v", err)
	}
	sigs, ok := parseIFAPISignatures(t, string(data))["withdraw"]
	if !ok || len(sigs) != 1 {
		t.Fatalf("m4.5 spec's IF/API table has %d `withdraw` rows, want 1", len(sigs))
	}
	want := sigs[0]
	cmd, ok := registeredCommands()["withdraw"]
	if !ok {
		t.Fatal("withdraw is not registered in defaultCommands()")
	}
	if cmd.MinPositional != want.minPositional || cmd.MaxPositional != 1 {
		t.Errorf("withdraw: Min/MaxPositional = %d/%d, IF/API table says min %d and one <C-ID>", cmd.MinPositional, cmd.MaxPositional, want.minPositional)
	}
	got := map[string]bool{}
	for _, f := range cmd.Flags {
		if f.Required {
			got[f.Name] = true
		}
	}
	if !reflect.DeepEqual(got, want.required) {
		t.Errorf("withdraw: required flags = %v, IF/API table says %v", got, want.required)
	}
}

// AC-372 ほか: 完了以外の 7 状態から取り下げに移り、版を 1 上げ、作業ログと証跡を残し、approval を作らない。
func TestWithdraw_FromEveryNonTerminalStatus_MovesToWithdrawnAndLogsEvidence(t *testing.T) {
	for _, from := range []string{"unclassified", "classified", "awaiting_plan_approval", "in_progress", "verifying", "awaiting_completion_approval", "awaiting_human"} {
		t.Run(from, func(t *testing.T) {
			ws := initializedWorkspace(t)
			id := createWithStatusForCase(t, ws, from)

			out := runJSON(t, ws, withdrawRelayed(id)...)
			if len(out) != 1 {
				t.Fatalf("withdraw --json keys = %v, want only challenge", out)
			}
			c := out["challenge"].(map[string]any)
			if c["status"] != "withdrawn" || c["version"] != float64(2) {
				t.Errorf("challenge = %v, want status withdrawn and version 2", c)
			}

			acts := activitiesOf(t, ws, id)
			last := acts[len(acts)-1]
			if last["entity"] != "challenge" || last["action"] != "withdraw" || last["channel"] != "cli" || last["verification"] != "relayed" {
				t.Errorf("last activity = %v, want challenge/withdraw via cli/relayed", last)
			}
			requireJSONEqual(t, "before", last["before"], map[string]any{"status": from})
			requireJSONEqual(t, "after", last["after"], map[string]any{
				"status": "withdrawn", "reason": "採らない", "last_occurrence_id": 0.0, "version": 2.0,
				"relay_session": "s1", "relay_quote": "q1"})

			if n := len(runJSON(t, ws, "show", id)["approvals"].([]any)); n != 0 {
				t.Errorf("approvals = %d, want none", n)
			}
		})
	}
}

// AC-375: --reason が無い・空は usage_error で、何も書かない。
func TestWithdraw_MissingOrEmptyReason_IsUsageErrorAndWritesNothing(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	requireErrorCode(t, []string{"withdraw", id, "--relayed", "--session", "s", "--quote", "q", "--workspace", ws}, 2, CodeUsageError)
	requireErrorCode(t, []string{"withdraw", id, "--reason", "  ", "--relayed", "--session", "s", "--quote", "q", "--workspace", ws}, 2, CodeUsageError)
	if statusOf(t, ws, id) != "unclassified" || len(activitiesOf(t, ws, id)) != 1 {
		t.Error("state or log changed despite usage_error")
	}
}

// AC-372: 端末が無く --relayed も無いときは tty_required で、何も書かない。
func TestWithdraw_NoTerminalAndNoRelayed_IsTTYRequiredAndWritesNothing(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	cmd, stdout, stderr := newChildCmd([]string{"withdraw", "--workspace", ws, "--json", "--reason", "r", id})
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
	if statusOf(t, ws, id) != "unclassified" {
		t.Error("status changed despite tty_required")
	}
}

// AC-374・381: 完了・取り下げの課題の取り下げ、取り下げた課題への各操作は terminal_state で、何も書かない。
func TestWithdraw_TerminalChallenges_AreRejectedWithTerminalStateAndWriteNothing(t *testing.T) {
	ws := initializedWorkspace(t)
	other := createForCase(t, ws)
	done := createWithStatusForCase(t, ws, "done")
	withdrawn := createWithStatusForCase(t, ws, "in_progress")
	runJSON(t, ws, withdrawRelayed(withdrawn)...)

	for _, id := range []string{done, withdrawn} {
		before := len(activitiesOf(t, ws, id))
		requireErrorCode(t, append(withdrawRelayed(id), "--workspace", ws), 1, CodeTerminalState)
		rel := []string{"--relayed", "--session", "s", "--quote", "q", "--workspace", ws}
		for _, args := range [][]string{
			{"edit", id, "--title", "x", "--workspace", ws},
			{"dep", "add", id, other, "--workspace", ws},
			{"dep", "rm", id, other, "--workspace", ws},
			append([]string{"approve", id}, rel...),
			append([]string{"reject", id, "--reason", "r"}, rel...),
			append([]string{"answer", id, "--answer", "a"}, rel...),
			{"hold", id, "--question", "q", "--workspace", ws},
		} {
			requireErrorCode(t, args, 1, CodeTerminalState)
		}
		if got := len(activitiesOf(t, ws, id)); got != before {
			t.Errorf("%s: activities %d -> %d despite rejections", id, before, got)
		}
	}
	if statusOf(t, ws, withdrawn) != "withdrawn" || statusOf(t, ws, done) != "done" {
		t.Error("terminal status changed")
	}
}

// AC-376: 着手中で終わっていない委譲の run がある課題は conflict で、何も書かない。
func TestWithdraw_InProgressWithOpenDelegationRun_IsConflict(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createWithStatusForCase(t, ws, "in_progress")
	host, _ := os.Hostname()
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	coretest.InsertRun(t, ws, coretest.InsertRunInput{
		ChallengeID: challengeIDToInternalID(t, id), Kind: "delegate", ChallengeVersion: 1,
		SessionID: "11111111-1111-4111-8111-111111111111", PID: int64(os.Getpid()), Host: host,
		HeartbeatAt: now, StartedAt: now, MaxBudgetUSD: 1_000_000, BudgetBucket: "delegate",
	})
	requireErrorCode(t, append(withdrawRelayed(id), "--workspace", ws), 1, CodeConflict)
	if statusOf(t, ws, id) != "in_progress" || len(activitiesOf(t, ws, id)) != 1 {
		t.Error("state or log changed despite conflict")
	}
}

// AC-379: pending・approved の不可逆操作は rejected になり、操作ごとに作業ログが残る。
func TestWithdraw_RejectsPendingAndApprovedOperationsAndLogsEach(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	runJSON(t, ws, "op", "add", id, "--kind", "other", "--summary", "s1")
	runJSON(t, ws, "op", "add", id, "--kind", "other", "--summary", "s2")
	runJSON(t, ws, "op", "add", id, "--kind", "other", "--summary", "s3")
	coretest.Exec(t, ws, `UPDATE operation SET state = 'approved' WHERE id = 2`)
	coretest.Exec(t, ws, `UPDATE operation SET state = 'executed', version = 5 WHERE id = 3`)

	runJSON(t, ws, withdrawRelayed(id)...)

	ops := runJSON(t, ws, "show", id)["operations"].([]any)
	if len(ops) != 3 {
		t.Fatalf("operations = %v, want 3", ops)
	}
	for _, o := range ops {
		m := o.(map[string]any)
		want := "rejected"
		if m["id"] == "OP-3" {
			want = "executed" // 実行済みは触らない
			if m["version"] != float64(5) {
				t.Errorf("executed operation version = %v, want 5", m["version"])
			}
		}
		if m["state"] != want {
			t.Errorf("operation = %v, want %s", o, want)
		}
	}
	rejects := 0
	for _, a := range activitiesOf(t, ws, id) {
		if a["entity"] == "operation" && a["action"] == "reject" {
			rejects++
			if a["verification"] != "relayed" {
				t.Errorf("operation activity verification = %v, want relayed", a["verification"])
			}
			if after, _ := a["after"].(map[string]any); after["relay_session"] != "s1" || after["relay_quote"] != "q1" {
				t.Errorf("operation activity after = %v, want the relay evidence", a["after"])
			}
		}
	}
	if rejects != 2 {
		t.Errorf("operation reject activities = %d, want 2", rejects)
	}
	if n := len(runJSON(t, ws, "show", id)["approvals"].([]any)); n != 0 {
		t.Errorf("approvals = %d, want none", n)
	}
}

// AC-380: 人間対応待ちの課題の開いている保留は、回答なしで閉じる（closed_at が入り answer は NULL）。
func TestWithdraw_ClosesOpenHoldWithoutAnswer(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createWithStatusForCase(t, ws, "verifying")
	runJSON(t, ws, "verify", id, "--result", "uncertain", "--question", "why?")

	runJSON(t, ws, withdrawRelayed(id)...)

	holds := runJSON(t, ws, "show", id)["holds"].([]any)
	if len(holds) != 1 {
		t.Fatalf("holds = %v, want 1", holds)
	}
	h := holds[0].(map[string]any)
	if h["answer"] != nil || h["closed_at"] == nil {
		t.Errorf("hold = %v, want answer null and closed_at set", h)
	}
	// 閉じた保留が未回答の質問として数えられないことは core の
	// TestWithdraw_ClosedHold_IsNotOpenInAnyReadPath（ListUnansweredHolds）が確かめる。
}

// AC-384 相当: list --status は withdrawn と 取り下げ のどちらでも取り下げた課題を返す。
func TestWithdraw_ListStatusFindsWithdrawnByCodeAndLabel(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	createForCase(t, ws)
	runJSON(t, ws, withdrawRelayed(id)...)
	for _, s := range []string{"withdrawn", "取り下げ"} {
		list := runJSON(t, ws, "list", "--status", s)["challenges"].([]any)
		if len(list) != 1 || list[0].(map[string]any)["id"] != id {
			t.Errorf("list --status %s = %v, want only %s", s, list, id)
		}
	}
}
