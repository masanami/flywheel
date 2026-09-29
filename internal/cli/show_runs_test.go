package cli

import (
	"fmt"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは #86（docs/features/m3-invoker-delegation.md §観測「show は、課題の run の
// 一覧（新しい順・最大 20 件）…を示す」・§IF / API「show: 最上位に runs」）の `show --json` の
// runs を検証する。

// insertEndedRuns は課題 id に、終了した run を n 件入れる（終了した run は課題ごとの
// 「終了していない run は高々 1 つ」の一意制約に触れない）。
func insertEndedRuns(t *testing.T, ws, id string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		insertRunFixture(t, ws, id, func(in *coretest.InsertRunInput) {
			in.SessionID = fmt.Sprintf("11111111-1111-1111-1111-%012d", i)
			in.EndedAt = "2026-09-28T00:01:00.000Z"
			in.Result = "succeeded"
			cost := int64(500_000)
			in.CostUSD = &cost
			in.CostSource = "reported"
		})
	}
}

// AC-154: show --json の runs は、その課題の run を新しい順に最大 20 件出力する。他の課題の
// run は含まない。要素の形は runs コマンドと同じ（文書の形と照合する）。
func TestShow_RunsAreNewestFirstAtMost20AndOnlyThisChallenges(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	other := createForCase(t, ws)
	insertEndedRuns(t, ws, id, 22) // R-1〜R-22
	insertEndedRuns(t, ws, other, 1)

	doc := runJSON(t, ws, "show", id)

	runs, ok := doc["runs"].([]any)
	if !ok || len(runs) != 20 {
		t.Fatalf("runs = %#v, want an array of exactly 20 (capped)", doc["runs"])
	}
	if first := runs[0].(map[string]any); first["id"] != "R-22" {
		t.Errorf("runs[0].id = %v, want R-22 (newest first)", first["id"])
	}
	if last := runs[19].(map[string]any); last["id"] != "R-3" {
		t.Errorf("runs[19].id = %v, want R-3 (R-1 and R-2 are beyond the 20 newest)", last["id"])
	}
	for i, r := range runs {
		if got := r.(map[string]any)["challenge_id"]; got != id {
			t.Errorf("runs[%d].challenge_id = %v, want %s only", i, got, id)
		}
	}
	assertDocumentedJSON(t, loadDocumentedJSON(t), "show", doc)
}

// 走った run が無い課題の show --json の runs は空の配列（null でない）。
func TestShow_RunsIsAnEmptyArrayWhenTheChallengeHasNoRuns(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)

	runs, ok := runJSON(t, ws, "show", id)["runs"].([]any)
	if !ok || len(runs) != 0 {
		t.Fatalf("runs = %#v, want an empty array", runs)
	}
}
