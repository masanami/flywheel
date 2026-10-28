package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは M4.5 S4（#393）の `flywheel improve [--json]` と `cycle` の `improve` の段
// （AC-319・AC-326〜AC-329・AC-354・AC-355・AC-409）を、実ストア・偽の claude で検証する。
// 本物の claude・gh は起動しない。

// fakeClaudeJ8Match は J8 の起動の引数列を見分ける部分文字列（J8 の出力スキーマだけが持つ名前）。
const fakeClaudeJ8Match = `"proposals"`

// j8Route は occIDs を根拠にした 1 件の提案（層 prompt・対象 target）を返す J8 の応答。
func j8Route(target string, occIDs ...string) fakeClaudeRoute {
	out, _ := json.Marshal(map[string]any{"proposals": []any{map[string]any{
		"layer": "prompt", "target": target, "cause": "context", "occurrence_ids": occIDs, "metric": "rejection_rate",
		"expected": "却下が減る", "action": "add", "title": "IMPROVE-" + target, "done_criteria": "完了条件 " + target,
	}}})
	return fakeClaudeRoute{Match: fakeClaudeJ8Match, Tag: "J8",
		Stdout: `{"session_id":"s","is_error":false,"total_cost_usd":0.5,"structured_output":` + string(out) + `}`}
}

// j8InvalidRoute は出力検査に通らない J8 の応答（run は invalid_output になり、失敗に数える）。
func j8InvalidRoute() fakeClaudeRoute {
	return fakeClaudeRoute{Match: fakeClaudeJ8Match, Tag: "J8",
		Stdout: `{"session_id":"s","is_error":false,"total_cost_usd":0.5,"structured_output":{"nothing":1}}`}
}

// addJ8Occurrences は起動の件数に入る発生（bad・原因 context・層 prompt・出どころ j6）を n 件足し、
// 発生の ID（O-n）を返す。発生の ID は、そのワークスペースで足した順の序数として組み立てる。
func addJ8Occurrences(t *testing.T, ws string, n int) []string {
	t.Helper()
	return addJ8OccurrencesFor(t, ws, challengeIDToInternalID(t, createForCase(t, ws)), 1, n)
}

// addJ8OccurrencesFor は課題 cid（内部 ID）に発生を n 件足す。first は最初に足す発生の序数（O-<first>）。
func addJ8OccurrencesFor(t *testing.T, ws string, cid, first, n int) []string {
	t.Helper()
	var ids []string
	for i := 0; i < n; i++ {
		coretest.Exec(t, ws, `INSERT INTO occurrence (challenge_id, kind, activity_id, outcome, source, cause, layer, target, signal, created_at)
			VALUES (?, 'plan_rejected', (SELECT MAX(id) FROM activity), 'bad', 'j6', 'context', 'prompt', 'T', 'sig', '2026-03-01T00:00:00.000Z')`, cid)
		ids = append(ids, fmt.Sprintf("O-%d", first+i))
	}
	return ids
}

func writeImproveThreshold(t *testing.T, ws string, n int) {
	t.Helper()
	writeAgentJSONForTest(t, ws, fmt.Sprintf(`{"position_file": "position.md", "improve_threshold": %d}`, n))
}

// `improve` の名前と書式（位置引数なし・必須のフラグなし）が m4.5 の §IF / API の表のとおりであること。
func TestImproveCommand_MatchesM45IFAPITable(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "features", "m4.5-learning-and-self-improvement.md"))
	if err != nil {
		t.Fatalf("read m4.5 spec: %v", err)
	}
	sigs, ok := parseIFAPISignatures(t, string(data))["improve"]
	if !ok || len(sigs) != 1 {
		t.Fatalf("m4.5 spec's IF/API table has %d `improve` rows, want 1", len(sigs))
	}
	cmd, ok := registeredCommands()["improve"]
	if !ok {
		t.Fatal("improve is not registered in defaultCommands()")
	}
	if cmd.MinPositional != sigs[0].minPositional || cmd.MaxPositional != 0 {
		t.Errorf("improve: Min/MaxPositional = %d/%d, IF/API table says min %d and none", cmd.MinPositional, cmd.MaxPositional, sigs[0].minPositional)
	}
	for _, f := range cmd.Flags {
		if f.Required {
			t.Errorf("improve has required flag %q, IF/API table says none", f.Name)
		}
	}
}

// AC-326・AC-328・AC-331 相当: connectors.json が無くても improve は J8 を呼び、{"phase": …} の形で
// run ごとの {created, linked, result, run_id} を返す。経路はどの層も Issue。起票した課題は未分類。
func TestImprove_WithoutConnectors_CallsJ8AndFilesAnUnclassifiedImprovementChallenge(t *testing.T) {
	m45SpecLine(t, "`.flywheel/connectors.json` が無くても、`improve` の段と `flywheel improve` は J8 を呼ぶ")
	ws := setupWorkspaceWithPosition(t)
	if _, err := os.Stat(filepath.Join(ws, ".flywheel", "connectors.json")); err == nil {
		t.Fatal("fixture unexpectedly has connectors.json")
	}
	occ := addJ8Occurrences(t, ws, 1) // 既定のしきい値 10 未満でも、手動は 1 件以上で呼ぶ
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j8Route("T", occ...)}, "")

	doc := runJSON(t, ws, "improve")
	assertDocumentedJSON(t, loadDocumentedJSON(t), "improve", doc)
	pm := doc["phase"].(map[string]any)
	if pm["phase"] != "improve" || pm["skipped"] != false {
		t.Fatalf("phase = %v, want improve and not skipped", pm)
	}
	items := phaseItems(t, pm)
	if len(items) != 1 || items[0]["result"] != "succeeded" || items[0]["run_id"] == "" {
		t.Fatalf("items = %v, want one succeeded run", items)
	}
	created, _ := items[0]["created"].([]any)
	linked, _ := items[0]["linked"].([]any)
	if len(created) != 1 || linked == nil || len(linked) != 0 {
		t.Fatalf("created/linked = %v/%v, want one created and an empty (non-null) linked", items[0]["created"], items[0]["linked"])
	}

	show := runJSON(t, ws, "show", created[0].(string))
	ch := show["challenge"].(map[string]any)
	if ch["status"] != "unclassified" || ch["title"] != "IMPROVE-T" {
		t.Errorf("challenge = %v, want an unclassified improvement challenge", ch)
	}
	if ch["improvement"] == nil {
		t.Errorf("challenge.improvement = nil, want the improvement key")
	}
	desc, _ := ch["description"].(string)
	if !strings.Contains(desc, "Issue") {
		t.Errorf("description = %q, want the route to say Issue (no connectors.json)", desc)
	}
}

// AC-327・M45P26: 起動の件数が 0 なら J8 を呼ばず、skipped は偽で items は空。
func TestImprove_NoTriggeringOccurrences_DoesNotCallJ8(t *testing.T) {
	m45SpecLine(t, "J8 を呼ばなかった `improve` の段は、`skipped` が偽で、`items` が空である")
	ws := setupWorkspaceWithPosition(t)
	argvLog, _ := putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: `{"is_error":false}`})

	doc := runJSON(t, ws, "improve")
	assertDocumentedJSON(t, loadDocumentedJSON(t), "improve", doc)
	pm := doc["phase"].(map[string]any)
	if pm["skipped"] != false || len(phaseItems(t, pm)) != 0 || len(phaseNotStarted(t, pm)) != 0 {
		t.Errorf("phase = %v, want skipped=false with no items", pm)
	}
	if _, err := os.Stat(argvLog); err == nil {
		t.Error("claude was invoked although the triggering count is 0")
	}
}

// claude が PATH に無ければ invoker_unavailable で、ストアは変わらない。位置引数は usage_error。
func TestImprove_ClaudeNotOnPATH_InvokerUnavailable_AndRejectsPositional(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	addJ8Occurrences(t, ws, 1)
	emptyPATH(t)
	before := storeBytes(t, ws)
	requireJSONErrorEnvelope(t, []string{"improve", "--workspace", ws}, 2, CodeInvokerUnavailable)
	if string(storeBytes(t, ws)) != string(before) {
		t.Error("invoker_unavailable must not change the store")
	}
	requireJSONErrorEnvelope(t, []string{"improve", "C-1", "--workspace", ws}, 2, CodeUsageError)
}

// AC-329: improve は端末を求めない（標準入力が端末でなくても動く）。委譲の子の環境（M3H13 の目印）では、
// ほかのコマンドと同じく拒否される。
func TestImprove_NeedsNoTerminal_AndIsRejectedInDelegatedChildEnvironment(t *testing.T) {
	m45SpecLine(t, "`flywheel improve` は端末を求めない")
	ws := setupWorkspaceWithPosition(t)
	putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: `{"is_error":false}`})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"improve", "--workspace", ws, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands()); code != 0 {
		t.Fatalf("exit = %d, want 0 without a terminal (stderr=%s)", code, stderr.String())
	}

	t.Setenv(core.DelegatedRunEnvVar, "R-7")
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"improve", "--workspace", ws, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands()); code != 1 ||
		!strings.Contains(stderr.String(), `"code":"verification_rejected"`) || stdout.Len() != 0 {
		t.Errorf("exit=%d stdout=%q stderr=%s, want the common rejection", code, stdout.String(), stderr.String())
	}
}

// AC-319・AC-320・AC-321: cycle の最終の段は improve。起動の件数が improve_threshold 未満なら J8 を呼ばず、
// 以上なら 1 回呼ぶ。connectors.json が無くても skipped にならない。
func TestCycle_ImprovePhaseIsLast_CallsJ8OnlyAtThreshold(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	writeImproveThreshold(t, ws, 2)
	occ := addJ8Occurrences(t, ws, 1)
	argvLog := filepath.Join(t.TempDir(), "j8.argv")
	j8 := j8Route("T", occ...)
	j8.ArgvLogPath = argvLog
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1"), j8}, "")

	doc := cycleDoc(t, ws)
	phases := doc["phases"].([]any)
	last := phases[len(phases)-1].(map[string]any)
	if last["phase"] != "improve" || last["skipped"] != false || len(phaseItems(t, last)) != 0 {
		t.Fatalf("last phase = %v, want improve (not skipped) with no items below the threshold", last)
	}
	if prev := phases[len(phases)-2].(map[string]any); prev["phase"] != "learn" {
		t.Errorf("the phase before improve = %v, want learn", prev["phase"])
	}
	if _, err := os.Stat(argvLog); err == nil {
		t.Error("J8 was called below the threshold")
	}
	assertDocumentedCycle(t, loadDocumentedJSON(t), doc)

	// 2 件目の発生でしきい値に達すると、次の周の improve の段は J8 を 1 回呼ぶ。
	occ = append(occ, addJ8OccurrencesFor(t, ws, challengeIDToInternalID(t, "C-1"), 2, 1)...)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1"), j8Route("T", occ...)}, "")
	doc = cycleDoc(t, ws)
	phases = doc["phases"].([]any)
	last = phases[len(phases)-1].(map[string]any)
	items := phaseItems(t, last)
	if last["phase"] != "improve" || len(items) != 1 || items[0]["result"] != "succeeded" {
		t.Fatalf("last phase = %v, want one succeeded J8 run at the threshold", last)
	}
	assertDocumentedCycle(t, loadDocumentedJSON(t), doc)
}

// AC-354・AC-355: 起票した改修課題は次の周の分類の段（J1）の対象になる。計画と完了の承認は、ほかの課題と同じ
// 種類（plan・completion）で記録される。
func TestCycle_FiledImprovementChallengeIsClassifiedNextCycle_AndUsesPlanAndCompletionApprovals(t *testing.T) {
	m45SpecLine(t, "起票した改修課題は、次の `cycle` の分類の段で J1 の対象になる")
	ws := setupWorkspaceWithPosition(t)
	writeImproveThreshold(t, ws, 1)
	occ := addJ8Occurrences(t, ws, 1)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1"), j8Route("T", occ...)}, "")

	doc1 := cycleDoc(t, ws)
	imp := cyclePhase(t, doc1, "improve")
	items := phaseItems(t, imp)
	if len(items) != 1 {
		t.Fatalf("improve items = %v, want one", items)
	}
	created := items[0]["created"].([]any)
	if len(created) != 1 {
		t.Fatalf("created = %v, want one", created)
	}
	id := created[0].(string)
	if got := statusOfChallenge(t, ws, id); got != "unclassified" {
		t.Fatalf("status after the cycle that filed it = %q, want unclassified", got)
	}
	for _, it := range phaseItems(t, cyclePhase(t, doc1, "classify")) {
		if it["challenge_id"] == id {
			t.Fatalf("%s was classified in the cycle that filed it", id)
		}
	}

	// 2 周目: 分類の段（J1）の対象に入る。
	doc2 := cycleDoc(t, ws)
	var classified bool
	for _, it := range phaseItems(t, cyclePhase(t, doc2, "classify")) {
		classified = classified || it["challenge_id"] == id
	}
	if !classified {
		t.Fatalf("2nd cycle classify items = %v, want to include %s", cyclePhase(t, doc2, "classify")["items"], id)
	}

	// 計画と完了の承認は plan・completion で記録される。
	runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, "PLAN"))
	runJSON(t, ws, "approve", id, "--relayed", "--session", "s", "--quote", "q")
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "awaiting_completion_approval")
	runJSON(t, ws, "approve", id, "--relayed", "--session", "s", "--quote", "q")
	var kinds []string
	for _, a := range runJSON(t, ws, "show", id)["approvals"].([]any) {
		kinds = append(kinds, a.(map[string]any)["kind"].(string))
	}
	if !reflect.DeepEqual(kinds, []string{"plan", "completion"}) {
		t.Errorf("approval kinds = %v, want [plan completion]", kinds)
	}
}

// M45P26・AC-342 相当: J8 の失敗が failure_limit 回続くと cycle の improve の段は J8 を呼ばず、課題の ID を持たない
// not_started（failure_limit）で示す。手動の improve には上限を当てない。
func TestImprove_FailureLimit_StopsOnlyTheCyclePhase(t *testing.T) {
	m45SpecLine(t, "この `not_started` の要素は `challenge_id` を持たない")
	ws := setupWorkspaceWithPosition(t)
	writeImproveThreshold(t, ws, 1)
	addJ8Occurrences(t, ws, 1)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1"), j8InvalidRoute()}, "")

	for i := 0; i < 2; i++ { // 既定の failure_limit は 2
		items := phaseItems(t, cyclePhase(t, cycleDoc(t, ws), "improve"))
		if len(items) != 1 || items[0]["result"] != "invalid_output" {
			t.Fatalf("cycle %d improve items = %v, want one invalid_output run", i+1, items)
		}
	}
	doc := cycleDoc(t, ws)
	imp := cyclePhase(t, doc, "improve")
	ns := phaseNotStarted(t, imp)
	if len(phaseItems(t, imp)) != 0 || len(ns) != 1 || ns[0]["reason"] != "failure_limit" {
		t.Fatalf("improve phase = %v, want no items and one failure_limit not_started", imp)
	}
	if _, has := ns[0]["challenge_id"]; has {
		t.Errorf("failure_limit not_started has challenge_id: %v", ns[0])
	}
	assertDocumentedCycle(t, loadDocumentedJSON(t), doc)

	// 手動の improve には上限を当てない（J8 を呼ぶ）。
	pm := runJSON(t, ws, "improve")["phase"].(map[string]any)
	if items := phaseItems(t, pm); len(items) != 1 || len(phaseNotStarted(t, pm)) != 0 {
		t.Errorf("manual improve phase = %v, want a J8 call without the failure limit", pm)
	}
}
