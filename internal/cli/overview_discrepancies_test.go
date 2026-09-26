package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは #58 の「食い違いの表示」（AC-80〜AC-86）のうち、CLI 側
// （`status --json` の `needs_human.discrepancies`）を検証する。core 側
// （GetOverview の判定）は internal/core/overview_discrepancies_test.go が
// 担当する。

func bindSourceForDiscrepancyCase(t *testing.T, ws, challengeID, externalKey, upstreamState, policyState string) {
	t.Helper()
	coretest.InsertSourceBinding(t, ws, challengeIDToInternalID(t, challengeID),
		"s", externalKey, "https://example.invalid/"+externalKey, "2:abc", upstreamState, policyState, "2026-09-26T00:00:00.000Z")
}

func discrepancyEntries(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()
	needsHuman, ok := doc["needs_human"].(map[string]any)
	if !ok {
		t.Fatalf(`doc["needs_human"] = %#v, want an object`, doc["needs_human"])
	}
	arr, ok := needsHuman["discrepancies"].([]any)
	if !ok {
		t.Fatalf(`needs_human["discrepancies"] = %#v, want an array`, needsHuman["discrepancies"])
	}
	out := make([]map[string]any, 0, len(arr))
	for _, v := range arr {
		e, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("discrepancies element = %#v, want an object", v)
		}
		out = append(out, e)
	}
	return out
}

func findDiscrepancyEntry(entries []map[string]any, challengeID string) map[string]any {
	for _, e := range entries {
		if e["challenge_id"] == challengeID {
			return e
		}
	}
	return nil
}

func kindsOf(t *testing.T, e map[string]any) []string {
	t.Helper()
	arr, ok := e["kinds"].([]any)
	if !ok {
		t.Fatalf("kinds = %#v, want an array", e["kinds"])
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		out = append(out, v.(string))
	}
	return out
}

// --- AC-80〜83: kinds の内容 ---

func TestRunStatus_Discrepancies_ReflectsUpstreamAndPolicyState(t *testing.T) {
	ws := initializedWorkspace(t)
	closed := createForCase(t, ws)
	bindSourceForDiscrepancyCase(t, ws, closed, "o/r#1", "closed", "in_policy")
	missing := createForCase(t, ws)
	bindSourceForDiscrepancyCase(t, ws, missing, "o/r#2", "missing", "in_policy")
	outOfPolicy := createForCase(t, ws)
	bindSourceForDiscrepancyCase(t, ws, outOfPolicy, "o/r#3", "open", "out_of_policy")
	both := createForCase(t, ws)
	bindSourceForDiscrepancyCase(t, ws, both, "o/r#4", "closed", "out_of_policy")
	clean := createForCase(t, ws)
	bindSourceForDiscrepancyCase(t, ws, clean, "o/r#5", "open", "in_policy")

	doc := runJSON(t, ws, "status")
	entries := discrepancyEntries(t, doc)

	if e := findDiscrepancyEntry(entries, closed); e == nil || !equalStrings(kindsOf(t, e), []string{"upstream_closed"}) {
		t.Errorf("closed entry = %+v, want kinds=[upstream_closed] (AC-80)", e)
	}
	if e := findDiscrepancyEntry(entries, missing); e == nil || !equalStrings(kindsOf(t, e), []string{"upstream_missing"}) {
		t.Errorf("missing entry = %+v, want kinds=[upstream_missing] (AC-81)", e)
	}
	if e := findDiscrepancyEntry(entries, outOfPolicy); e == nil || !equalStrings(kindsOf(t, e), []string{"out_of_policy"}) {
		t.Errorf("out_of_policy entry = %+v, want kinds=[out_of_policy] (AC-82)", e)
	}
	if e := findDiscrepancyEntry(entries, both); e == nil || !equalStrings(kindsOf(t, e), []string{"upstream_closed", "out_of_policy"}) {
		t.Errorf("both entry = %+v, want kinds=[upstream_closed, out_of_policy] (AC-83)", e)
	}
	if e := findDiscrepancyEntry(entries, clean); e != nil {
		t.Errorf("clean entry = %+v, want none (in_policy/open は食い違いではない)", e)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- AC-85: 食い違いのある未分類の課題は actionable.challenges にも出る
// （M1 の区分の規則が変わらないことの検証） ---

func TestRunStatus_Discrepancies_UnclassifiedChallengeAlsoAppearsInActionable(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	bindSourceForDiscrepancyCase(t, ws, id, "o/r#1", "closed", "in_policy")

	doc := runJSON(t, ws, "status")
	actionable := statusIDs(t, doc, "actionable.challenges")
	if !containsString(actionable, id) {
		t.Errorf("actionable.challenges = %v, want to contain %s (AC-85)", actionable, id)
	}
	entries := discrepancyEntries(t, doc)
	if findDiscrepancyEntry(entries, id) == nil {
		t.Errorf("discrepancies = %+v, want to contain %s", entries, id)
	}
}

// --- AC-86相当: 食い違いの無いワークスペースは [] ---

func TestRunStatus_Discrepancies_EmptyWorkspaceIsEmptyArray(t *testing.T) {
	ws := initializedWorkspace(t)
	doc := runJSON(t, ws, "status")
	entries := discrepancyEntries(t, doc)
	if len(entries) != 0 {
		t.Errorf("discrepancies = %+v, want empty", entries)
	}
}

// --- status は読み取り専用（discrepancies を読んでも状態を変えない） ---

func TestRunStatus_Discrepancies_DoesNotMutateSourceBinding(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	bindSourceForDiscrepancyCase(t, ws, id, "o/r#1", "closed", "out_of_policy")

	before := runJSON(t, ws, "show", id)["source_binding"]
	runJSON(t, ws, "status")
	after := runJSON(t, ws, "show", id)["source_binding"]
	beforeMap, ok1 := before.(map[string]any)
	afterMap, ok2 := after.(map[string]any)
	if !ok1 || !ok2 || beforeMap["upstream_state"] != afterMap["upstream_state"] || beforeMap["policy_state"] != afterMap["policy_state"] {
		t.Errorf("source_binding changed: before=%+v after=%+v", before, after)
	}
}

// --- kinds の閉集合: status --json が出す kinds と core.DiscrepancyKindValues を
// 双方向に照合する（出力の値はすべて閉集合に含まれ、閉集合の値はすべて出力に
// 現れうる）。 ---

func TestRunStatus_Discrepancies_KindsMatchCoreClosedSetBothWays(t *testing.T) {
	ws := initializedWorkspace(t)
	closedID := createForCase(t, ws)
	bindSourceForDiscrepancyCase(t, ws, closedID, "o/r#1", "closed", "out_of_policy")
	missingID := createForCase(t, ws)
	bindSourceForDiscrepancyCase(t, ws, missingID, "o/r#2", "missing", "in_policy")

	seen := map[string]bool{}
	for _, e := range discrepancyEntries(t, runJSON(t, ws, "status")) {
		kinds, ok := e["kinds"].([]any)
		if !ok {
			t.Fatalf("kinds = %#v, want an array", e["kinds"])
		}
		for _, k := range kinds {
			seen[k.(string)] = true
		}
	}
	want := map[string]bool{}
	for _, k := range core.DiscrepancyKindValues() {
		want[string(k)] = true
		if !seen[string(k)] {
			t.Errorf("kind %q of core.DiscrepancyKindValues never appeared in status --json (seen=%v)", k, seen)
		}
	}
	for k := range seen {
		if !want[k] {
			t.Errorf("status --json emitted kind %q, which is not in core.DiscrepancyKindValues %v", k, core.DiscrepancyKindValues())
		}
	}
}

// --- テキスト出力: 食い違いは「人間の操作を待っているもの」の中に出る（JSON の
// needs_human.discrepancies と同じ区分。§食い違いの表示）。食い違いしか無くても
// その見出しは (なし) にならない。 ---

func TestRunStatus_Discrepancies_TextOutputListsThemUnderNeedsHuman(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	bindSourceForDiscrepancyCase(t, ws, id, "o/r#1", "closed", "in_policy")

	got := runText(t, ws, "status")
	humanIdx := strings.Index(got, "人間の操作を待っているもの")
	actionableIdx := strings.Index(got, "システムが次に進められるもの")
	if humanIdx < 0 || actionableIdx < 0 || humanIdx >= actionableIdx {
		t.Fatalf("status text output = %q, want the needs-human heading before the actionable heading", got)
	}
	humanSection := got[humanIdx:actionableIdx]
	if !strings.Contains(humanSection, id) || !strings.Contains(humanSection, "upstream_closed") {
		t.Errorf("needs-human section = %q, want %s with upstream_closed", humanSection, id)
	}
	if strings.Contains(humanSection, "(なし)") {
		t.Errorf("needs-human section = %q, must not say (なし) when a discrepancy exists", humanSection)
	}
}
