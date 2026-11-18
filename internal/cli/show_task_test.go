package cli

import (
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// AC-13（core の見直しの区切り S2a・masanami/flywheel#750）: タスクが 0 個の完了の課題で、
// flywheel show <課題の ID> --json が終了コード 0 で終わる。出力の形は、区切り S2a の #755 で根の tasks と challenge.uses_integration を足した形（タスクが 0 個なら tasks は空の配列）。
func TestRunShow_DoneChallengeWithoutTasksSucceedsAndKeepsOutputShape(t *testing.T) {
	t.Parallel()
	ws := initializedWorkspace(t)
	created := runJSON(t, ws, "create", "--title", "t", "--done-criteria", "d")
	id := created["challenge"].(map[string]any)["id"].(string)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), "done")

	code, stdout, stderr := runCLIRaw(t, "show", id, "--workspace", ws, "--json")
	if code != 0 {
		t.Fatalf("show exit = %d, want 0 (stderr=%s)", code, stderr)
	}
	doc := decodeJSON(t, stdout)
	ch, ok := doc["challenge"].(map[string]any)
	if !ok || ch["id"] != id || ch["status"] != "done" {
		t.Fatalf("show output challenge = %v, want id %s status done", doc["challenge"], id)
	}
	// 区切り S2a の課題（#755）で、タスクを足した形になった: 根に tasks（0 個なら空の配列）、challenge に uses_integration。
	// 単数の task は足していない。
	if tasks, ok := doc["tasks"].([]any); !ok || len(tasks) != 0 {
		t.Errorf("show output tasks = %#v, want an empty array", doc["tasks"])
	}
	if _, has := doc["task"]; has {
		t.Errorf("show output has key %q; only the plural key tasks is part of the output shape", "task")
	}
	if v, has := ch["uses_integration"]; !has || v != nil {
		t.Errorf("show output challenge.uses_integration = %v (present=%v), want present and null", v, has)
	}
}
