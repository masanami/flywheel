package cli

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは #108（親要件チケット #98 §一括の操作（サイクル）の S2。AC-353〜358・362）の
// `flywheel cycle` の委譲（run）と検証（verify）の段を、実ストア・実物の git スロット・偽の
// claude・偽の gh で検証する。本物の claude・gh・harness は起動しない。

// ghPlainGET は偽の gh のログの 1 行が、書き込みのフラグを持たない api の呼び出し（GET）か。
// フラグは先頭の語で判定する（-fkey=value・--field・--raw-field・--input・-X・--method を含む）。
func ghPlainGET(call string) bool {
	words := strings.Fields(strings.TrimPrefix(call, "CALL "))
	if len(words) == 0 || words[0] != "api" {
		return false
	}
	for _, w := range words[1:] {
		switch {
		case strings.HasPrefix(w, "-f"), strings.HasPrefix(w, "-F"), strings.HasPrefix(w, "-X"),
			strings.HasPrefix(w, "--field"), strings.HasPrefix(w, "--raw-field"),
			strings.HasPrefix(w, "--method"), strings.HasPrefix(w, "--input"):
			return false
		}
	}
	return true
}

// 取り込み・分類・計画・委譲・検証の順に段を実行し、同じ周の委譲の段で検証中になった課題を同じ周の
// 検証の段で J5 の対象にする。委譲の run が終わってから J5 を起動する。flywheel が呼んだ gh は
// すべて GET である（AC-353・354・355・357・362）。
func TestCycle_AllFivePhases_DelegatesAndVerifiesInTheSameCycleWithOnlyGETs(t *testing.T) {
	ws := setupRunWorkspace(t)
	writeSourcesDeclaration(t, ws, cycleSourcesJSON)
	id := newInProgressForRun(t, ws, nil)
	orderLog := filepath.Join(t.TempDir(), "order.log")

	issue := marshalFakeGHIssue(t, fakeGHIssue{Number: 1, Title: "ingested-issue", Body: "body", Repo: "o/r", UpdatedAt: "2026-09-25T09:00:00Z"})
	calls := withFakeGHRoutesOrderedOnPATH(t, []fakeGHRoute{
		fakeGHListRoute("o/r", 1, "["+issue+"]", 0),
		fakeGHGetIssueStatusLineRoute("o/r", 1, "HTTP/2.0 200 OK", issue, 0),
		{match: "repos/o/r/issues/1/comments?per_page=100&page=1", stdout: "[]"},
		// 委譲の後の照合（子がスロットで切ったブランチ）。
		{match: "repos/o/direct/branches/feat/child", stdout: "HTTP/2.0 200 OK\r\n\r\n{\"name\":\"feat/child\"}"},
		{match: "repos/o/direct/pulls?head=o%3Afeat%2Fchild&page=1&per_page=100&state=all", stdout: `[{"html_url":"https://github.com/o/direct/pull/9","title":"t","state":"open","merged_at":null,"base":{"ref":"develop"}}]`},
		// 検証の段の PR のチェック（完了済み）。
		{match: "repos/o/direct/pulls/9", stdout: `{"html_url":"https://github.com/o/direct/pull/9","title":"t","state":"open","merged_at":null,"base":{"ref":"develop"},"head":{"sha":"abc123"}}`},
		{match: "repos/o/direct/commits/abc123/check-runs?page=1&per_page=100", stdout: checkRunsComplete},
		{match: "repos/o/direct/commits/abc123/status?page=1&per_page=100", stdout: `{"state":"success","total_count":0,"statuses":[]}`},
	}, orderLog)
	child := delegateRoute("completed")
	child.ShellBefore = "git -C " + shellSingleQuote(filepath.Join(ws, "slot-d")) + " checkout -q -b feat/child"
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1"), j2RoutePlan(t), j3Route("BRIEF-CYCLE"), child, j5Route("met", nil, nil)}, orderLog)

	doc := cycleDoc(t, ws)

	assertDocumentedCycle(t, loadDocumentedJSON(t), doc)
	var names []string
	for _, p := range doc["phases"].([]any) {
		names = append(names, p.(map[string]any)["phase"].(string))
	}
	if !reflect.DeepEqual(names, []string{"ingest", "classify", "plan", "run", "verify"}) {
		t.Fatalf("phases = %v", names)
	}
	got := collapseConsecutive(orderLogTags(readOrderLog(t, orderLog)))
	want := []string{"gh", "claude J1", "gh", "claude J2", "claude J3", "claude DELEGATE", "gh", "claude J5"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("call order = %v, want %v", got, want)
	}

	run := cyclePhase(t, doc, "run")
	items := phaseItems(t, run)
	if run["skipped"] != false || len(items) != 1 || items[0]["challenge_id"] != id || items[0]["outcome"] != "completed" || items[0]["status"] != "verifying" {
		t.Errorf("run phase = %v, want the delegation's outcome (completed) and the mapped status (verifying)", run)
	}
	verify := cyclePhase(t, doc, "verify")
	vitems := phaseItems(t, verify)
	if verify["skipped"] != false || len(vitems) != 1 || vitems[0]["challenge_id"] != id || vitems[0]["outcome"] != "met" || vitems[0]["status"] != "awaiting_completion_approval" {
		t.Errorf("verify phase = %v, want J5 on the challenge that became verifying in this cycle", verify)
	}
	if got := challengeStatusOf(t, ws, id); got != "awaiting_completion_approval" {
		t.Errorf("status = %s", got)
	}
	if got := challengeStatusOf(t, ws, "C-2"); got != "awaiting_plan_approval" {
		t.Errorf("ingested challenge status = %s, want awaiting_plan_approval (planned in this cycle; the cycle never approves)", got)
	}

	// J5 は、その周の委譲の run が終わってから起動される。
	var delegateEnd, j5Start string
	for _, r := range runJSON(t, ws, "runs")["runs"].([]any) {
		m := r.(map[string]any)
		switch {
		case m["kind"] == "delegate":
			delegateEnd, _ = m["ended_at"].(string)
		case m["judgment"] == "J5":
			j5Start, _ = m["started_at"].(string)
		}
	}
	if delegateEnd == "" || j5Start == "" || j5Start < delegateEnd {
		t.Errorf("J5 started_at = %q, delegation ended_at = %q: J5 must start after the delegation ended", j5Start, delegateEnd)
	}

	ghCalls := calls()
	if len(ghCalls) == 0 {
		t.Fatal("no gh calls were recorded")
	}
	for _, c := range ghCalls {
		if !ghPlainGET(c) {
			t.Errorf("gh call is not a plain GET: %q", c)
		}
	}
}

// connectors.json の無いワークスペースの cycle は、run と verify の段を skipped にし（対象になりうる課題が
// あっても）J3・J5 を起動せず、終了コード 0 で終わる（AC-356）。
func TestCycle_WithoutConnectors_RunAndVerifyPhasesAreSkipped(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	// connectors.json が実在すれば J3（委譲）・J5（検証）の対象になる課題を置く。
	newInProgressForRun(t, ws, nil)
	newVerifyingForCase(t, ws, false)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1")}, "")

	doc := cycleDoc(t, ws)
	for _, r := range runJSON(t, ws, "runs")["runs"].([]any) {
		m := r.(map[string]any)
		// 周の外で記録された run（フィクスチャが置いた直前の委譲）は数えない。
		if m["cycle_id"] != nil && (m["kind"] == "delegate" || m["judgment"] == "J3" || m["judgment"] == "J5") {
			t.Errorf("run %v was recorded, want no delegation, J3 or J5 without connectors.json", m)
		}
	}

	assertDocumentedCycle(t, loadDocumentedJSON(t), doc)
	for _, name := range []string{"run", "verify"} {
		p := cyclePhase(t, doc, name)
		if p["skipped"] != true || len(phaseItems(t, p)) != 0 || len(p["not_started"].([]any)) != 0 {
			t.Errorf("%s phase = %v, want skipped with no items and no not_started", name, p)
		}
	}
	if groups := cyclePhase(t, doc, "run")["serial_groups"].([]any); len(groups) != 0 {
		t.Errorf("run.serial_groups = %v, want []", groups)
	}
}

// 計画承認待ち・完了確認待ち・人間対応待ちの課題は、委譲・検証のどちらの段の対象にもならず、
// cycle は承認を行わない（AC-358）。
func TestCycle_WaitingChallengesAreNotTargetsOfRunOrVerify(t *testing.T) {
	ws := setupRunWorkspace(t)
	ids := map[string]string{}
	for status, c := range map[string]string{"awaiting_plan_approval": "a", "awaiting_completion_approval": "b", "awaiting_human": "c"} {
		id := createTitled(t, ws, c)
		runJSON(t, ws, "classify", id, "--priority", "P1")
		setChallengeStatusForTest(t, ws, id, status)
		ids[status] = id
	}
	putRoutedFakeClaudeOnPATH(t, nil, "")

	doc := cycleDoc(t, ws)

	for _, name := range []string{"run", "verify"} {
		p := cyclePhase(t, doc, name)
		if p["skipped"] != false || len(phaseItems(t, p)) != 0 || len(p["not_started"].([]any)) != 0 {
			t.Errorf("%s phase = %v, want an executed phase with no target", name, p)
		}
	}
	for status, id := range ids {
		if got := challengeStatusOf(t, ws, id); got != status {
			t.Errorf("%s status = %s, want unchanged %s", id, got, status)
		}
	}
}

func setChallengeStatusForTest(t *testing.T, ws, id, status string) {
	t.Helper()
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), status)
}
