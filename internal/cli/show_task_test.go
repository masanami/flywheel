package cli

import (
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// AC-13（core の見直しの区切り S2a・masanami/flywheel#750）: タスクが 0 個の完了の課題で、
// flywheel show <課題の ID> --json が終了コード 0 で終わる。出力の形（キー）はタスクを足す前から変わらない。
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
	for _, k := range []string{"task", "tasks", "uses_integration"} {
		if _, has := doc[k]; has {
			t.Errorf("show output has key %q; the output shape must not change", k)
		}
		if _, has := ch[k]; has {
			t.Errorf("show output challenge has key %q; the output shape must not change", k)
		}
	}
}
