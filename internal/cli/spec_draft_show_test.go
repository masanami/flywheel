package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// 下書きづくりの記録の show・status の表示（docs/features/request-to-plan-s2b.md「記録と表示」）。

func insertSpecDraftRow(t *testing.T, ws, id string) {
	t.Helper()
	coretest.Exec(t, ws, `INSERT INTO spec_draft (challenge_id, path, repo, reason, state, attempts, created_at, updated_at)
		VALUES (?, '.flywheel/specs/c-1.md', 'o/r', 'why', 'pending', 1, '2026-11-26T00:00:00.000Z', '2026-11-26T00:00:00.000Z')`, challengeIDToInternalID(t, id))
}

func TestRunShow_SpecDraftLineAndJSON(t *testing.T) { // AC-357・AC-367・AC-368
	t.Parallel()
	ws := initializedWorkspace(t)
	with, without := createForCase(t, ws), createForCase(t, ws)
	insertSpecDraftRow(t, ws, with)

	if text := runText(t, ws, "show", with); !strings.Contains(text, "下書きづくり:  実行待ち (pending)・実行 1 回（上限 3 回）\n") {
		t.Errorf("show text lacks the spec draft line:\n%s", text)
	}
	if text := runText(t, ws, "show", without); strings.Contains(text, "下書きづくり") {
		t.Errorf("show text of a challenge without a record has the line:\n%s", text)
	}
	ch := runJSON(t, ws, "show", without)["challenge"].(map[string]any)
	if v, ok := ch["spec_draft"]; !ok || v != nil {
		t.Errorf("spec_draft = %#v, want null", v)
	}
	sd, ok := runJSON(t, ws, "show", with)["challenge"].(map[string]any)["spec_draft"].(map[string]any)
	if !ok {
		t.Fatal("spec_draft is not an object")
	}
	want := map[string]any{"state": "pending", "path": ".flywheel/specs/c-1.md", "base_path": nil, "repo": "o/r", "reason": "why",
		"attempts": 1.0, "max_attempts": 3.0, "hold_id": nil, "hold_reason": nil}
	for k, w := range want {
		if v, ok := sd[k]; !ok || v != w {
			t.Errorf("spec_draft[%q] = %#v (present %v), want %#v", k, v, ok, w)
		}
	}
	for _, k := range []string{"questions", "answers"} {
		if v, ok := sd[k].([]any); !ok || len(v) != 0 {
			t.Errorf("spec_draft[%q] = %#v, want []", k, sd[k])
		}
	}
	if len(sd) != 11 {
		t.Errorf("spec_draft has %d keys, want 11: %v", len(sd), sd)
	}
}

// 記録が pending の課題は、計画の段の対象から外れても（cycle_work の計画の数え）、「いま進められること」には出続ける。
func TestRunStatus_PendingSpecDraftIsActionable(t *testing.T) { // AC-369
	t.Parallel()
	ws := setupWorkspaceWithPosition(t)
	writeConnectorsJSONForTest(t, ws, j2ConnectorsFixture)
	id := createForCase(t, ws)
	runText(t, ws, "classify", id, "--priority", "P2")

	check := func(wantPlan float64) {
		t.Helper()
		doc := runJSON(t, ws, "status")
		if got := doc["cycle_work"].(map[string]any)["stages"].(map[string]any)["plan"]; got != wantPlan {
			t.Errorf("cycle_work.stages.plan = %v, want %v", got, wantPlan)
		}
		if !containsString(statusIDs(t, doc, "actionable.challenges"), id) {
			t.Errorf("%s is not in actionable.challenges: %v", id, doc["actionable"])
		}
	}
	check(1)
	insertSpecDraftRow(t, ws, id)
	check(0)
}
