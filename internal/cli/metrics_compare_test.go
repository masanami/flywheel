package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// M4.5 S4 の `flywheel metrics --compare <C-ID>`（AC-390〜AC-400）を CLI の入口から検査する。
// 組の作り方・指標の数え方は core の metrics_compare_test.go が実ストアで検査する。

// compareWorkspace は C-1・C-2（前）、C-3（比べる課題）、C-4（後）を完了の承認の順に持つワークスペース。
func compareWorkspace(t *testing.T) string {
	t.Helper()
	ws := initializedWorkspace(t)
	for i := 0; i < 5; i++ {
		createForCase(t, ws) // C-5 は完了していない課題
	}
	for cid := 1; cid <= 4; cid++ {
		coretest.SetChallengeStatus(t, ws, cid, "done")
		coretest.Exec(t, ws, `INSERT INTO approval (challenge_id, kind, decision, target_version, actor, channel, verification, decided_at)
			VALUES (?, 'completion', 'approved', 1, 'a', 'cli', 'tty_confirm', ?)`, cid, localStamp(t, "2026-03-0"+string(rune('0'+cid)), 12))
	}
	return ws
}

func TestMetricsCompare_JSONShapeMatchesTheSpecAndGroupsAreSplit(t *testing.T) {
	ws := compareWorkspace(t)
	out := runJSON(t, ws, "metrics", "--compare", "C-3")
	doc := loadDocumentedJSON(t)
	if got := keysOf(out); !reflect.DeepEqual(got, doc.entity["metrics_compare_top"]) {
		t.Fatalf("top-level keys = %v", sortedKeys(got))
	}
	c := asMap(t, out["compare"])
	if got := keysOf(c); !reflect.DeepEqual(got, doc.entity["metrics_compare"]) {
		t.Fatalf("compare keys = %v, documented %v", sortedKeys(got), sortedKeys(doc.entity["metrics_compare"]))
	}
	if c["challenge_id"] != "C-3" || c["applied_at"] != localStamp(t, "2026-03-03", 12) {
		t.Errorf("compare = %v", c)
	}
	for _, side := range []string{"before", "after"} {
		g := asMap(t, c[side])
		if got := keysOf(g); !reflect.DeepEqual(got, doc.entity["metrics_compare_group"]) {
			t.Fatalf("%s keys = %v", side, sortedKeys(got))
		}
		assertDocumentedMetrics(t, doc, map[string]any{
			"period": map[string]any{"since": nil, "until": nil}, "metrics": g["metrics"],
			"occurrences": runJSON(t, ws, "metrics")["occurrences"], "definitions": runJSON(t, ws, "metrics")["definitions"],
		})
	}
	if asMap(t, c["before"])["target_challenges"] != 2.0 || asMap(t, c["after"])["target_challenges"] != 1.0 {
		t.Errorf("target_challenges before/after = %v / %v, want 2 / 1", asMap(t, c["before"])["target_challenges"], asMap(t, c["after"])["target_challenges"])
	}
}

func TestMetricsCompare_ErrorsLeaveNothingOnStdout(t *testing.T) {
	ws := compareWorkspace(t)
	for _, tc := range []struct {
		args []string
		exit int
		code ErrorCode
	}{
		{[]string{"metrics", "--compare", "C-5"}, 1, CodeValidationFailed},
		{[]string{"metrics", "--compare", "C-99"}, 1, CodeNotFound},
		{[]string{"metrics", "--compare", "C-1", "--until", "2026-03-09"}, 2, CodeUsageError},
	} {
		requireErrorCode(t, append(tc.args, "--workspace", ws), tc.exit, tc.code)
	}
	coretest.InsertReleaseOperation(t, ws, 1, "https://example.test/pr/1", "approved")
	requireErrorCode(t, []string{"metrics", "--compare", "C-1", "--workspace", ws}, 1, CodeValidationFailed)
}

func TestMetricsCompare_DeterministicReadOnlyAndPlainMetricsUnchanged(t *testing.T) {
	ws := compareWorkspace(t)
	dbPath := filepath.Join(ws, ".flywheel", "flywheel.db")
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"metrics", "--compare", "C-3", "--workspace", ws, "--json"}
	c1, o1, _ := runPiped(t, args)
	c2, o2, _ := runPiped(t, args)
	if c1 != 0 || c2 != 0 || o1 != o2 {
		t.Fatalf("exit %d/%d; outputs differ:\n%s\n%s", c1, c2, o1, o2)
	}
	if _, o, _ := runPiped(t, []string{"metrics", "--compare", "C-3", "--workspace", ws}); o == "" {
		t.Error("text output is empty")
	}
	after, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("store changed by --compare")
	}
	if plain := runJSON(t, ws, "metrics"); keysOf(plain)["compare"] || plain["target_challenges"] != 4.0 {
		t.Errorf("plain metrics changed: %v", plain)
	}
}
