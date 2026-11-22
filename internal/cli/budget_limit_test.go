package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
	"github.com/masanami/flywheel/internal/server"
)

// `flywheel budget <課題> --limit <USD>`（区切り S4c-1。AC-25〜29・217）。見込みは 14 USD（createTwoTaskTargetForCase）。

func budgetLimitOf(t *testing.T, ws, id string) string {
	t.Helper()
	r := coretest.QueryRows(t, ws, `SELECT budget_limit_micros FROM challenge WHERE id = `+fmt.Sprint(challengeIDToInternalID(t, id)))
	if len(r) != 1 {
		t.Fatalf("rows = %v", r)
	}
	return fmt.Sprint(r[0][0])
}

func budgetApprovalCount(t *testing.T, ws string) int {
	t.Helper()
	return len(coretest.QueryRows(t, ws, `SELECT id FROM approval WHERE kind = 'budget'`))
}

// AC-26・217: --relayed で上限がその額になり、承認の記録が 1 行でき、サイクルを始める処理が 1 回呼ばれる（きっかけは budget）。
func TestBudgetLimit_RelayedSetsTheLimitAndStartsTheCycle(t *testing.T) {
	parallelTools(t, false)
	ws := initializedWorkspace(t)
	id := createTwoTaskTargetForCase(t, ws)
	createForCase(t, ws) // いま始められるもの
	st := &fakeStarter{launch: server.CycleLaunch{Started: true}}

	code, stdout, stderr := runWithStarter(t, st, "budget", id, "--limit", "25", "--relayed", "--session", "S", "--quote", "上げて", "--workspace", ws, "--json")

	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if got := budgetLimitOf(t, ws, id); got != "25000000" {
		t.Errorf("budget_limit_micros = %s, want 25000000", got)
	}
	if n := budgetApprovalCount(t, ws); n != 1 {
		t.Errorf("budget approvals = %d, want 1", n)
	}
	if len(st.calls) != 1 || st.calls[0] != "budget" {
		t.Errorf("starter calls = %v, want [budget]", st.calls)
	}
}

// AC-25: ターミナルでの本人確認を通すと、上限がその額になる。
func TestBudgetLimit_ConfirmedFromATerminalSetsTheLimit(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createTwoTaskTargetForCase(t, ws)
	code, pty, stdout, stderr := runConfirmedChild(t, []string{"budget", id, "--limit", "25", "--workspace", ws, "--json"}, id)
	if code != 0 {
		t.Fatalf("exit=%d (stdout=%q stderr=%q pty=%q)", code, stdout, stderr, pty)
	}
	for _, want := range []string{"変更後の上限", "25 USD", "見込み"} {
		if !strings.Contains(pty, want) {
			t.Errorf("summary lacks %q:\n%s", want, pty)
		}
	}
	if got := budgetLimitOf(t, ws, id); got != "25000000" {
		t.Errorf("budget_limit_micros = %s, want 25000000", got)
	}
}

// AC-27: 見込みより小さい額は検証の誤りで断り、上限も承認の記録も変わらず、サイクルも始めない。
func TestBudgetLimit_SmallerThanTheEstimateIsAValidationError(t *testing.T) {
	parallelTools(t, false)
	ws := initializedWorkspace(t)
	id := createTwoTaskTargetForCase(t, ws)
	st := &fakeStarter{launch: server.CycleLaunch{Started: true}}
	code, stdout, stderr := runWithStarter(t, st, "budget", id, "--limit", "13.9", "--relayed", "--session", "S", "--quote", "q", "--workspace", ws, "--json")
	if code != 1 || !strings.Contains(stdout+stderr, string(CodeValidationFailed)) {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want exit 1 with %s", code, stdout, stderr, CodeValidationFailed)
	}
	if got := budgetLimitOf(t, ws, id); got != "<nil>" || budgetApprovalCount(t, ws) != 0 || len(st.calls) != 0 {
		t.Errorf("state changed: limit=%s approvals=%d calls=%v", got, budgetApprovalCount(t, ws), st.calls)
	}
}

// AC-28: 本人確認なしは断り、上限は変わらない。
func TestBudgetLimit_WithoutIdentityIsRejected(t *testing.T) {
	parallelTools(t, false)
	ws := initializedWorkspace(t)
	id := createTwoTaskTargetForCase(t, ws)
	st := &fakeStarter{launch: server.CycleLaunch{Started: true}}
	code, stdout, stderr := runWithStarter(t, st, "budget", id, "--limit", "25", "--workspace", ws, "--json")
	if code != 1 || !strings.Contains(stdout+stderr, string(CodeTTYRequired)) {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want exit 1 with %s", code, stdout, stderr, CodeTTYRequired)
	}
	if got := budgetLimitOf(t, ws, id); got != "<nil>" || budgetApprovalCount(t, ws) != 0 || len(st.calls) != 0 {
		t.Errorf("state changed: limit=%s approvals=%d calls=%v", got, budgetApprovalCount(t, ws), st.calls)
	}
}

// AC-29: --limit とタスクの枠の指定の同時指定は検証の誤り。
func TestBudgetLimit_WithATaskFrameIsAValidationError(t *testing.T) {
	parallelTools(t, false)
	ws := initializedWorkspace(t)
	id := createTwoTaskTargetForCase(t, ws)
	for _, extra := range [][]string{{"--impl-usd", "5"}, {"--review-usd", "5"}, {"--task", "T-1"}} {
		st := &fakeStarter{launch: server.CycleLaunch{Started: true}}
		args := append([]string{"budget", id, "--limit", "25"}, extra...)
		args = append(args, "--relayed", "--session", "S", "--quote", "q", "--workspace", ws, "--json")
		code, stdout, stderr := runWithStarter(t, st, args...)
		if code != 1 || !strings.Contains(stdout+stderr, string(CodeValidationFailed)) {
			t.Errorf("%v: exit=%d stdout=%q stderr=%q, want exit 1 with %s", extra, code, stdout, stderr, CodeValidationFailed)
		}
		if len(st.calls) != 0 {
			t.Errorf("%v: starter calls = %v", extra, st.calls)
		}
	}
	if got := budgetLimitOf(t, ws, id); got != "<nil>" || budgetApprovalCount(t, ws) != 0 {
		t.Errorf("state changed: limit=%s", got)
	}
}
