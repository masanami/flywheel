package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは #107（親要件チケット #98 §J5 検証。AC-268〜277・359・363 の CLI 側）の
// `flywheel verify --auto` と `status.waiting_external` を、実ストア・偽の claude・偽の gh で検証する。
// 本物の claude・gh・harness は起動しない。

// fakeClaudeJ5Match は J5 の起動の引数列を見分ける部分文字列（J5 の出力スキーマだけが持つ値）。
const fakeClaudeJ5Match = `"not_met"`

func j5Route(verdict string, feedback, question any) fakeClaudeRoute {
	out, _ := json.Marshal(map[string]any{"verdict": verdict, "reason": "REASON-CLI", "feedback": feedback, "question": question})
	return fakeClaudeRoute{Match: fakeClaudeJ5Match, Tag: "J5",
		Stdout: `{"session_id":"s","is_error":false,"total_cost_usd":0.4,"structured_output":` + string(out) + `}`}
}

// ghChecksRoutes は o/r の PR #1（head abc123）のチェックを返す偽の gh の規則。checkRunsBody は
// check-runs の応答。コミットステータスは空。
func ghChecksRoutes(prState, checkRunsBody string) []fakeGHRoute {
	return []fakeGHRoute{
		{match: "repos/o/r/pulls/1", stdout: `{"html_url":"https://github.com/o/r/pull/1","title":"PR-CLI","state":"` + prState + `","merged_at":null,"base":{"ref":"develop"},"head":{"sha":"abc123"}}`},
		{match: "repos/o/r/commits/abc123/check-runs?page=1&per_page=100", stdout: checkRunsBody},
		{match: "repos/o/r/commits/abc123/status?page=1&per_page=100", stdout: `{"state":"success","total_count":0,"statuses":[]}`},
	}
}

const (
	checkRunsPending  = `{"total_count":2,"check_runs":[{"name":"build","status":"completed","conclusion":"success"},{"name":"lint","status":"in_progress","conclusion":null}]}`
	checkRunsComplete = `{"total_count":2,"check_runs":[{"name":"build","status":"completed","conclusion":"success"},{"name":"test-CLI","status":"completed","conclusion":"failure"}]}`
)

// newVerifyingForCase は検証中の課題を作る。直前の委譲の run（終了済み）の成果物に withPR なら
// o/r の PR #1 を、そうでなければブランチだけを持たせる。
func newVerifyingForCase(t *testing.T, ws string, withPR bool) string {
	t.Helper()
	id := createForCase(t, ws)
	_, output := j2PlanFixture(t, nil)
	spec, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	cid := challengeIDToInternalID(t, id)
	coretest.InsertApprovedPlan(t, ws, cid, "PLAN-BODY-CLI", string(spec))
	coretest.SetChallengeStatus(t, ws, cid, "verifying")
	runID := coretest.InsertRun(t, ws, coretest.InsertRunInput{
		ChallengeID: cid, Kind: "delegate", ChallengeVersion: 1, SessionID: "22222222-2222-4222-8222-222222222222",
		PID: 1, Host: "h", HeartbeatAt: "2026-10-02T00:00:00.000Z", StartedAt: "2026-10-02T00:00:00.000Z",
		EndedAt: "2026-10-02T00:10:00.000Z", Result: "succeeded", MaxBudgetUSD: 5_000_000, BudgetBucket: "impl",
	})
	if withPR {
		coretest.InsertRunArtifact(t, ws, runID, "pr", "https://github.com/o/r/pull/1", "open", "develop")
	} else {
		coretest.InsertRunArtifact(t, ws, runID, "branch", "feat/x", "", "")
	}
	return id
}

func statusOfChallenge(t *testing.T, ws, id string) string {
	t.Helper()
	return runJSON(t, ws, "show", id)["challenge"].(map[string]any)["status"].(string)
}

func TestVerifyAuto_WithResult_IsUsageErrorAndNothingRuns(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	id := newVerifyingForCase(t, ws, true)
	argvLog, _ := putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: `{"is_error":false}`})
	before := storeBytes(t, ws)

	for _, args := range [][]string{
		{"verify", "--auto", "--result", "met"},
		{"verify", id, "--auto", "--result", "met"},
		{"verify", id, "--auto", "--result", "uncertain", "--question", "q"},
		{"verify", "--auto", "--question", "q"},
		{"verify"}, // どちらも指定しない
	} {
		requireJSONErrorEnvelope(t, append(append([]string{}, args...), "--workspace", ws), 2, CodeUsageError)
	}
	if string(storeBytes(t, ws)) != string(before) {
		t.Error("usage errors must not change the store")
	}
	if _, err := os.Stat(argvLog); err == nil {
		t.Error("claude was invoked on a usage error")
	}
}

func TestVerifyAuto_ClaudeNotOnPATH_InvokerUnavailableAndStoreUnchanged(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	newVerifyingForCase(t, ws, true)
	emptyPATH(t)
	before := storeBytes(t, ws)
	requireJSONErrorEnvelope(t, []string{"verify", "--auto", "--workspace", ws}, 2, CodeInvokerUnavailable)
	if string(storeBytes(t, ws)) != string(before) {
		t.Error("the store changed")
	}
}

// agent.json が無くても（全キーが省略可能）既定の宣言で動き、周の上限額は既定の 300 USD で評価される。
func TestVerifyAuto_WithoutAgentJSON_UsesTheDefaultCycleBudget(t *testing.T) {
	ws := initializedWorkspace(t)
	id := newVerifyingForCase(t, ws, false)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j5Route("met", nil, nil)}, "")

	runJSON(t, ws, "verify", "--auto", id)

	var j5 map[string]any
	for _, r := range runJSON(t, ws, "runs", id)["runs"].([]any) {
		if r.(map[string]any)["judgment"] == "J5" {
			j5 = r.(map[string]any)
		}
	}
	if j5 == nil || j5["cycle_budget_usd"] != 300.0 || j5["max_budget_usd"] != 5.0 {
		t.Errorf("J5 run = %v, want cycle_budget_usd 300 and the default J5 budget 5", j5)
	}
}

// agent.json の judgment_budget_usd.J5 が、J5 の `--max-budget-usd` になる（判断点ごとの上限額）。
func TestVerifyAuto_J5BudgetComesFromAgentJSON(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	writeAgentJSONForTest(t, ws, `{"position_file": "position.md", "judgment_budget_usd": {"J5": 2.5}}`)
	id := newVerifyingForCase(t, ws, false)
	argvLog := t.TempDir() + "/j5-argv.log"
	route := j5Route("met", nil, nil)
	route.ArgvLogPath = argvLog
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{route}, "")

	runJSON(t, ws, "verify", "--auto", id)

	argv, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argv), "--max-budget-usd 2.5") {
		t.Errorf("claude argv = %q, want --max-budget-usd 2.5", argv)
	}
}

func TestVerifyAuto_PendingChecks_WaitingExternalInPhaseAndStatusAndJ5NotStarted(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	id := newVerifyingForCase(t, ws, true)
	calls := withFakeGHRoutesOnPATH(t, ghChecksRoutes("open", checkRunsPending))
	argvLog, _ := putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: `{"is_error":false}`})
	before := runJSON(t, ws, "show", id)["challenge"].(map[string]any)["version"]

	doc := runJSON(t, ws, "verify", "--auto")
	phase := phaseOf(t, doc)
	if phase["phase"] != "verify" || phase["skipped"] != false || len(phase["items"].([]any)) != 0 {
		t.Fatalf("phase = %v", phase)
	}
	ns := phase["not_started"].([]any)
	if len(ns) != 1 || ns[0].(map[string]any)["challenge_id"] != id || ns[0].(map[string]any)["reason"] != "waiting_external" {
		t.Fatalf("not_started = %v, want %s waiting_external", ns, id)
	}
	if _, err := os.Stat(argvLog); err == nil {
		t.Error("J5 must not be invoked while a check is pending")
	}
	show := runJSON(t, ws, "show", id)["challenge"].(map[string]any)
	if show["status"] != "verifying" || show["version"] != before {
		t.Errorf("challenge = %v, want unchanged (verifying v%v)", show, before)
	}

	status := runJSON(t, ws, "status")
	we, ok := status["waiting_external"].(map[string]any)
	if !ok {
		t.Fatalf("status has no waiting_external object: %v", status)
	}
	got := we["challenges"].([]any)
	if len(got) != 1 {
		t.Fatalf("waiting_external.challenges = %v", got)
	}
	e := got[0].(map[string]any)
	if e["challenge_id"] != id || e["pr_url"] != "https://github.com/o/r/pull/1" || e["checks"] != "pending" {
		t.Errorf("entry = %v", e)
	}
	// 呼んだ gh はすべて GET（api の呼び出しで、書き込みのフラグを持たない）。
	for _, c := range calls() {
		if !strings.HasPrefix(c, "CALL api ") || strings.Contains(c, " -X") || strings.Contains(c, "--method") || strings.Contains(c, " -f ") || strings.Contains(c, " -F ") {
			t.Errorf("gh call is not a plain GET: %q", c)
		}
	}
}

func TestVerifyAuto_CompletedChecks_J5RunsWithTheChecksAndMapsMet(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	id := newVerifyingForCase(t, ws, true)
	withFakeGHRoutesOnPATH(t, ghChecksRoutes("open", checkRunsComplete))
	stdinLog := t.TempDir() + "/j5-stdin.log"
	route := j5Route("met", nil, nil)
	route.StdinLogPath = stdinLog
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{route}, "")

	doc := runJSON(t, ws, "verify", "--auto", id)
	phase := phaseOf(t, doc)
	items := phase["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", items)
	}
	item := items[0].(map[string]any)
	if item["challenge_id"] != id || item["result"] != "succeeded" || item["outcome"] != "met" || item["status"] != "awaiting_completion_approval" {
		t.Errorf("item = %v", item)
	}
	if got := statusOfChallenge(t, ws, id); got != "awaiting_completion_approval" {
		t.Errorf("status = %s", got)
	}
	in, err := os.ReadFile(stdinLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"test-CLI", "failure", "https://github.com/o/r/pull/1", "DONE-CLI-MARKER"} {
		if !bytes.Contains(in, []byte(want)) {
			t.Errorf("J5 stdin lacks %q", want)
		}
	}
	// 検証の run は J5 として記録される。
	runs := runJSON(t, ws, "runs", id)["runs"].([]any)
	found := false
	for _, r := range runs {
		if r.(map[string]any)["judgment"] == "J5" {
			found = true
		}
	}
	if !found {
		t.Errorf("runs = %v, want a J5 run", runs)
	}
}

func TestVerifyAuto_NoPR_NeverCallsGHAndStdinSaysSo(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	id := newVerifyingForCase(t, ws, false)
	installedGuardCalls := withFakeGHRoutesOnPATH(t, nil) // どの gh の呼び出しも失敗する偽の gh
	stdinLog := t.TempDir() + "/j5-stdin.log"
	route := j5Route("not_met", "FIX-THIS-CLI", nil)
	route.StdinLogPath = stdinLog
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{route}, "")

	doc := runJSON(t, ws, "verify", "--auto", id)
	item := phaseOf(t, doc)["items"].([]any)[0].(map[string]any)
	if item["outcome"] != "not_met" || item["status"] != "in_progress" {
		t.Errorf("item = %v", item)
	}
	if calls := installedGuardCalls(); len(calls) != 0 {
		t.Errorf("gh was called for a challenge without a PR: %v", calls)
	}
	in, _ := os.ReadFile(stdinLog)
	if !bytes.Contains(in, []byte("PR は無い")) {
		t.Errorf("J5 stdin must say there is no PR:\n%s", in)
	}
}

func TestVerifyAuto_InvalidOutput_LeavesTheChallengeVerifying(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	id := newVerifyingForCase(t, ws, false)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j5Route("not_met", nil, nil)}, "")
	item := phaseOf(t, runJSON(t, ws, "verify", "--auto", id))["items"].([]any)[0].(map[string]any)
	if item["result"] != "invalid_output" || item["outcome"] != nil || item["status"] != nil {
		t.Errorf("item = %v", item)
	}
	if got := statusOfChallenge(t, ws, id); got != "verifying" {
		t.Errorf("status = %s", got)
	}
}

func TestVerifyAuto_Uncertain_HoldsWithTheQuestion(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	id := newVerifyingForCase(t, ws, false)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j5Route("uncertain", nil, "HUMAN-QUESTION-CLI")}, "")
	item := phaseOf(t, runJSON(t, ws, "verify", "--auto", id))["items"].([]any)[0].(map[string]any)
	if item["outcome"] != "uncertain" || item["status"] != "awaiting_human" {
		t.Errorf("item = %v", item)
	}
	holds := runJSON(t, ws, "show", id)["holds"].([]any)
	if len(holds) != 1 || holds[0].(map[string]any)["question"] != "HUMAN-QUESTION-CLI" {
		t.Errorf("holds = %v", holds)
	}
}

func TestVerifyAuto_OmittedID_OnlyVerifyingChallenges(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	verifying := newVerifyingForCase(t, ws, false)
	other := newClassifiedForCase(t, ws)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j5Route("met", nil, nil)}, "")
	items := phaseOf(t, runJSON(t, ws, "verify", "--auto"))["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["challenge_id"] != verifying {
		t.Fatalf("items = %v, want only %s", items, verifying)
	}
	if got := statusOfChallenge(t, ws, other); got != "classified" {
		t.Errorf("other status = %s", got)
	}
}

func TestVerifyAuto_ByID_NotVerifyingIsInvalidTransition(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	id := newClassifiedForCase(t, ws)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j5Route("met", nil, nil)}, "")
	requireJSONErrorEnvelope(t, []string{"verify", "--auto", id, "--workspace", ws}, 1, CodeInvalidTransition)
}

func TestStatus_WaitingExternalIsEmptyWithoutAVerifyingChallengeAndNeverCallsGH(t *testing.T) {
	ws := initializedWorkspace(t)
	newClassifiedForCase(t, ws)
	calls := withFakeGHRoutesOnPATH(t, nil)
	status := runJSON(t, ws, "status")
	we := status["waiting_external"].(map[string]any)
	if ch, ok := we["challenges"].([]any); !ok || len(ch) != 0 {
		t.Errorf("waiting_external = %v, want an empty challenges array", we)
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("status called gh without a verifying challenge: %v", got)
	}
}
