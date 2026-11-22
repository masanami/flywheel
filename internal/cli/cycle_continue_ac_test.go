package cli

import (
	"encoding/json"
	"testing"

	"github.com/masanami/flywheel/internal/server"
)

// #988 AC-121（Issue #1037。番号は #988 本文の「## 受入基準」の並びの順）: `flywheel cycle` が settled で終えて続きを始めるとき、
// 始める処理（CycleStart）が trigger = continue で 1 回呼ばれる。続きを始める元の「いま始められるもの」は、終わりの段（改修の提案の段）が
// 起票した未分類の課題で、回りの終わりの数えには入らない（settled で終わる）が、排他を放した後の数えには入る。
func TestCycle_AC121_SettledCycleStartsTheNextOneWithTheContinueTrigger(t *testing.T) {
	parallelTools(t, false)
	ws := setupWorkspaceWithPosition(t)
	writeImproveThreshold(t, ws, 1)
	occ := addJ8Occurrences(t, ws, 1)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1"), j8Route("T", occ...)}, "")
	st := &fakeStarter{launch: server.CycleLaunch{Started: true}}

	code, stdout, stderr := runWithStarter(t, st, "cycle", "--workspace", ws, "--json")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout)
	}
	if got := doc["cycle"].(map[string]any)["end_reason"]; got != "settled" {
		t.Fatalf("end_reason = %v, want settled (the premise of AC-121)", got)
	}
	if imp := phaseItems(t, cyclePhase(t, doc, "improve")); len(imp) != 1 {
		t.Fatalf("improve items = %v, want the J8 run that filed the new challenge", imp)
	}
	if len(st.calls) != 1 || st.calls[0] != "continue" {
		t.Fatalf("starter calls = %v, want exactly [continue]", st.calls)
	}
}

// #988 AC-121（記録の側）: きっかけ continue で始まったサイクルの記録の trigger は continue になる。
func TestCycle_AC121_TheContinueTriggerIsRecordedOnTheCycle(t *testing.T) {
	parallelTools(t, false)
	ws := setupWorkspaceWithPosition(t)
	createTitled(t, ws, "work")
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1")}, "")

	doc := cycleDoc(t, ws, "--trigger", "continue")

	if c := doc["cycle"].(map[string]any); c["trigger"] != "continue" {
		t.Errorf("cycle.trigger = %v, want continue", c["trigger"])
	}
}
