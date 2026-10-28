package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは M4.5 S3（#363）の `flywheel learn [<C-ID>] [--json]` と `cycle` の `learn` の段
// （AC-222・AC-227〜AC-230・AC-310）を、実ストア・偽の claude で検証する。本物の claude・gh は起動しない。

// fakeClaudeJ6Match は J6 の起動の引数列を見分ける部分文字列（J6 の出力スキーマだけが持つ名前）。
const fakeClaudeJ6Match = `"classifications"`

// j6RouteClassify は未分類の発生 occIDs をすべて context／prompt に分類する J6 の応答。
func j6RouteClassify(occIDs ...string) fakeClaudeRoute {
	cls := []any{}
	for _, id := range occIDs {
		cls = append(cls, map[string]any{"occurrence_id": id, "cause": "context", "layer": "prompt", "target": "internal/invoker/prompts/j1.md", "signal": "SIGNAL-CLI"})
	}
	out, _ := json.Marshal(map[string]any{"classifications": cls, "added": []any{}, "memory": []any{}, "followups": []any{}})
	return fakeClaudeRoute{Match: fakeClaudeJ6Match, Tag: "J6",
		Stdout: `{"session_id":"s","is_error":false,"total_cost_usd":0.5,"structured_output":` + string(out) + `}`}
}

// newLearnableForCase は status の課題に未分類の発生を 1 件持たせ、課題の ID と発生の ID を返す。
// 発生の ID は、そのワークスペースで n 番目に足したものを n として組み立てる。
func newLearnableForCase(t *testing.T, ws, status string, n int) (id, occ string) {
	t.Helper()
	id = createForCase(t, ws)
	cid := challengeIDToInternalID(t, id)
	coretest.SetChallengeStatus(t, ws, cid, status)
	coretest.Exec(t, ws, `INSERT INTO occurrence (challenge_id, kind, activity_id, outcome, source, created_at)
		VALUES (?, 'completion_rejected', (SELECT MAX(id) FROM activity), 'bad', 'core', '2026-03-01T00:00:00.000Z')`, cid)
	return id, fmt.Sprintf("O-%d", n)
}

// requireNoLearnTargets は、未分類の発生を持つ対象がもう無い（learn が何も処理しない）ことを確かめる。
func requireNoLearnTargets(t *testing.T, ws string) {
	t.Helper()
	pm := runJSON(t, ws, "learn")["phase"].(map[string]any)
	if len(phaseItems(t, pm)) != 0 || len(phaseNotStarted(t, pm)) != 0 {
		t.Errorf("learn still has targets: %v", pm)
	}
}

func readFileForTest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestLearnCommand_MatchesM45IFAPITable は、`learn` の名前と書式（位置引数は任意の 1 つ・必須のフラグなし）が
// m4.5 の §IF / API の表のとおりであることを確かめる。
func TestLearnCommand_MatchesM45IFAPITable(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "features", "m4.5-learning-and-self-improvement.md"))
	if err != nil {
		t.Fatalf("read m4.5 spec: %v", err)
	}
	sigs, ok := parseIFAPISignatures(t, string(data))["learn"]
	if !ok || len(sigs) != 1 {
		t.Fatalf("m4.5 spec's IF/API table has %d `learn` rows, want 1", len(sigs))
	}
	want := sigs[0]
	cmd, ok := registeredCommands()["learn"]
	if !ok {
		t.Fatal("learn is not registered in defaultCommands()")
	}
	if cmd.MinPositional != want.minPositional || cmd.MaxPositional != 1 {
		t.Errorf("learn: Min/MaxPositional = %d/%d, IF/API table says min %d and one optional <C-ID>", cmd.MinPositional, cmd.MaxPositional, want.minPositional)
	}
	for _, f := range cmd.Flags {
		if f.Required {
			t.Errorf("learn has required flag %q, IF/API table says none", f.Name)
		}
	}
}

// AC-227: ID を指定した learn は、対象の条件を満たす課題なら J6 を 1 回呼び、満たさなければ J6 を呼ばず
// not_started に理由 not_learnable で示す。
func TestLearn_WithID_RunsJ6ForLearnableAndNotLearnableOtherwise(t *testing.T) {
	m45SpecLine(t, "`flywheel learn <C-ID>` は、その課題が対象の条件を満たせば J6 を 1 回呼び")
	ws := setupWorkspaceWithPosition(t)
	learnable, occ := newLearnableForCase(t, ws, "done", 1)
	order := filepath.Join(t.TempDir(), "order.log")
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j6RouteClassify(occ)}, order)

	out := runJSON(t, ws, "learn", learnable)
	pm := out["phase"].(map[string]any)
	items := phaseItems(t, pm)
	if len(items) != 1 || items[0]["challenge_id"] != learnable || items[0]["result"] != "succeeded" {
		t.Fatalf("items = %v, want one succeeded item for %s", items, learnable)
	}
	requireNoLearnTargets(t, ws)
	if n := strings.Count(readFileForTest(t, order), "claude J6"); n != 1 {
		t.Errorf("J6 was called %d times, want 1", n)
	}

	// 対象外（状態が着手中）の課題は J6 を呼ばない。
	other, _ := newLearnableForCase(t, ws, "in_progress", 2)
	out = runJSON(t, ws, "learn", other)
	pm = out["phase"].(map[string]any)
	ns := phaseNotStarted(t, pm)
	if len(phaseItems(t, pm)) != 0 || len(ns) != 1 || ns[0]["challenge_id"] != other || ns[0]["reason"] != "not_learnable" {
		t.Errorf("phase = %v, want not_started not_learnable for %s", pm, other)
	}
	if n := strings.Count(readFileForTest(t, order), "claude J6"); n != 1 {
		t.Errorf("J6 was called %d times in total, want still 1", n)
	}
}

// AC-228: ID を省略した learn は、対象を課題の ID の昇順に処理する（J6 は課題 1 件につき 1 回）。
func TestLearn_WithoutID_ProcessesTargetsInAscendingIDOrder(t *testing.T) {
	m45SpecLine(t, "`flywheel learn`（ID なし）は、`learn` の段と同じ対象を課題の ID の昇順に処理する")
	ws := setupWorkspaceWithPosition(t)
	first, o1 := newLearnableForCase(t, ws, "awaiting_human", 1)
	_, _ = newLearnableForCase(t, ws, "in_progress", 2) // 対象外（状態）
	third, o3 := newLearnableForCase(t, ws, "done", 3)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j6RouteClassify(o1, o3)}, "")

	pm := runJSON(t, ws, "learn")["phase"].(map[string]any)
	var got []string
	for _, it := range phaseItems(t, pm) {
		got = append(got, it["challenge_id"].(string))
	}
	if !reflect.DeepEqual(got, []string{first, third}) {
		t.Errorf("processed = %v, want ascending [%s %s]", got, first, third)
	}
	if len(phaseNotStarted(t, pm)) != 0 {
		t.Errorf("not_started = %v, want none (an ineligible challenge is skipped silently)", pm["not_started"])
	}
}

// AC-229: learn --json は {"phase": {…}} を返し、phase は cycle の phases の 1 要素と同じ形（phase は "learn"・skipped は false）。
func TestLearn_JSONIsOnePhaseObject(t *testing.T) {
	m45SpecLine(t, "`learn --json` は、`cycle` の `phases` の 1 要素と同じ形を")
	doc := loadDocumentedJSON(t)
	ws := setupWorkspaceWithPosition(t)
	_, occ := newLearnableForCase(t, ws, "done", 1)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j6RouteClassify(occ)}, "")

	out := runJSON(t, ws, "learn")
	if got := keysOf(out); !reflect.DeepEqual(got, doc.topLevel["learn"][0]) {
		t.Fatalf("learn --json top-level keys = %v, want %v", sortedKeys(got), sortedKeys(doc.topLevel["learn"][0]))
	}
	pm := out["phase"].(map[string]any)
	if pm["phase"] != "learn" || pm["skipped"] != false {
		t.Errorf("phase = %v skipped = %v, want %q and false", pm["phase"], pm["skipped"], "learn")
	}
	if len(phaseItems(t, pm)) != 1 {
		t.Fatalf("items = %v, want 1 item so that the element shape is compared", pm["items"])
	}
	assertDocumentedPhase(t, doc, "learn", pm)
}

// AC-230: connectors.json が無くても learn は動く（J6 は接続ツールの宣言を使わない）。
func TestLearn_WorksWithoutConnectorsJSON(t *testing.T) {
	m45SpecLine(t, "`.flywheel/connectors.json` が無くても、`learn` の段と `flywheel learn` は動く")
	ws := setupWorkspaceWithPosition(t)
	if _, err := os.Stat(filepath.Join(ws, ".flywheel", "connectors.json")); err == nil {
		t.Fatal("fixture unexpectedly has connectors.json")
	}
	_, occ := newLearnableForCase(t, ws, "done", 1)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j6RouteClassify(occ)}, "")

	pm := runJSON(t, ws, "learn")["phase"].(map[string]any)
	if items := phaseItems(t, pm); len(items) != 1 || items[0]["result"] != "succeeded" {
		t.Errorf("items = %v, want one succeeded item", items)
	}
}

// 対象が無い learn は空の phase を返し、J6 を起動しない。claude が無ければ invoker_unavailable で、ストアは変わらない。
func TestLearn_NoTargets_EmptyPhase_AndClaudeNotOnPATH_InvokerUnavailable(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	argvLog, _ := putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: `{"is_error":false}`})
	pm := runJSON(t, ws, "learn")["phase"].(map[string]any)
	if len(phaseItems(t, pm)) != 0 || len(phaseNotStarted(t, pm)) != 0 {
		t.Errorf("phase = %v, want empty", pm)
	}
	if _, err := os.Stat(argvLog); err == nil {
		t.Error("claude was invoked although there is no target")
	}

	newLearnableForCase(t, ws, "done", 1)
	emptyPATH(t)
	before := storeBytes(t, ws)
	requireJSONErrorEnvelope(t, []string{"learn", "--workspace", ws}, 2, CodeInvokerUnavailable)
	if string(storeBytes(t, ws)) != string(before) {
		t.Error("invoker_unavailable must not change the store")
	}
	requireJSONErrorEnvelope(t, []string{"learn", "C-1", "C-2", "--workspace", ws}, 2, CodeUsageError)
}

// AC-222: cycle の phases の最後は learn で、verify の後に実行される。learn の段は connectors.json が無くても
// skipped にならず、完了の課題の発生を分類する。
func TestCycle_LearnPhaseIsBeforeImproveAndClassifiesWithoutConnectors(t *testing.T) {
	m45SpecLine(t, "`cycle --json` の `phases` の最後の要素は `phase: \"learn\"` である")
	ws := setupWorkspaceWithPosition(t)
	id, occ := newLearnableForCase(t, ws, "done", 1)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1"), j6RouteClassify(occ)}, "")

	doc := cycleDoc(t, ws)
	phases := doc["phases"].([]any)
	if tail := phases[len(phases)-1].(map[string]any); tail["phase"] != "improve" {
		t.Fatalf("last phase = %v, want improve (S4 puts improve after learn)", tail)
	}
	last := phases[len(phases)-2].(map[string]any)
	if last["phase"] != "learn" || last["skipped"] != false {
		t.Fatalf("phase before improve = %v, want learn (not skipped)", last)
	}
	prev := phases[len(phases)-3].(map[string]any)
	if prev["phase"] != "verify" {
		t.Errorf("the phase before learn = %v, want verify", prev["phase"])
	}
	items := phaseItems(t, last)
	if len(items) != 1 || items[0]["challenge_id"] != id {
		t.Errorf("learn items = %v, want one for %s", items, id)
	}
	assertDocumentedCycle(t, loadDocumentedJSON(t), doc)
	requireNoLearnTargets(t, ws)
}

// AC-285: 1 周目の learn の段で J6 が起票したフォローアップは、2 周目の classify の段（J1）の対象に入る。
func TestCycle_FollowupFiledByLearnIsClassifiedInTheNextCycle(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	first, occ := newLearnableForCase(t, ws, "done", 1)
	out, _ := json.Marshal(map[string]any{
		"classifications": []any{map[string]any{"occurrence_id": occ, "cause": "context", "layer": "prompt", "target": "internal/invoker/prompts/j1.md", "signal": "s"}},
		"added":           []any{}, "memory": []any{},
		"followups": []any{map[string]any{"title": "FOLLOWUP-FROM-J6", "description": "d", "done_criteria": "c"}},
	})
	j6 := fakeClaudeRoute{Match: fakeClaudeJ6Match, Tag: "J6",
		Stdout: `{"session_id":"s","is_error":false,"total_cost_usd":0.5,"structured_output":` + string(out) + `}`}
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1"), j6}, "")

	// 1 周目: 分類の段は新しい課題を持たず、learn の段がフォローアップを起票する。
	doc1 := cycleDoc(t, ws)
	if its := phaseItems(t, cyclePhase(t, doc1, "classify")); len(its) != 0 {
		t.Errorf("1st cycle classify items = %v, want none", its)
	}
	items := phaseItems(t, cyclePhase(t, doc1, "learn"))
	if len(items) != 1 || items[0]["challenge_id"] != first {
		t.Fatalf("learn items = %v", items)
	}
	var followupID string
	for _, c := range runJSON(t, ws, "list")["challenges"].([]any) {
		m := c.(map[string]any)
		if m["title"] == "FOLLOWUP-FROM-J6" {
			followupID, _ = m["id"].(string)
		}
	}
	if followupID == "" {
		t.Fatal("the follow-up challenge was not filed")
	}

	// 2 周目: 起票したフォローアップが classify の items に入る。
	doc2 := cycleDoc(t, ws)
	var found bool
	for _, it := range phaseItems(t, cyclePhase(t, doc2, "classify")) {
		found = found || it["challenge_id"] == followupID
	}
	if !found {
		t.Errorf("2nd cycle classify items = %v, want to include %s", cyclePhase(t, doc2, "classify")["items"], followupID)
	}
}
