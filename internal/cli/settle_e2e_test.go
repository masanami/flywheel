package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// 要求を作ってから解決するまでを、CLI の口だけで通して確かめる（課題を終端にする所だけは、本人確認を通さずに
// ストアへ直接書く）。片付けの段の対象があるので、ほかに進むものが無くてもサイクルは始まる。
func TestCycle_SettlesARequirementEndToEnd_AC176_AC177_AC178_AC187_AC188_AC221(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	runJSON(t, ws, "requirement", "create", "--title", "通知")
	evidence := filepath.Join(ws, ".flywheel", "requirements", "RQ-1.md")
	if err := os.MkdirAll(filepath.Dir(evidence), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidence, []byte("根拠"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJSON(t, ws, "create", "--title", "区切り", "--requirement", "RQ-1")
	coretest.Exec(t, ws, `UPDATE challenge SET status = 'done' WHERE id = 1`)

	stages := runJSON(t, ws, "status")["cycle_work"].(map[string]any)["stages"].(map[string]any)
	if stages["settle"] != float64(1) {
		t.Fatalf("cycle_work.stages = %v, want settle 1", stages)
	}

	doc := cycleDoc(t, ws)
	phases := doc["phases"].([]any)
	if len(phases) < 3 || phases[1].(map[string]any)["phase"] != "settle" {
		t.Fatalf("phases = %v, want settle second", phases)
	}
	if _, err := os.Lstat(evidence); !os.IsNotExist(err) {
		t.Errorf("the evidence remains (err = %v)", err)
	}
	req := runJSON(t, ws, "show", "C-1")["requirement"].(map[string]any)
	if req["resolved_at"] == nil {
		t.Errorf("requirement = %v, want resolved_at", req)
	}
	assertContains(t, runText(t, ws, "show", "C-1"),
		"要求:          RQ-1（出どころ: 手で作った）\n同じ要求の課題:\n  C-1  区切り  完了 (done)\n")
	resolved := 0
	for _, e := range runJSON(t, ws, "log")["activities"].([]any) {
		if e.(map[string]any)["action"] == "requirement_resolved" {
			resolved++
		}
	}
	if resolved != 1 {
		t.Errorf("requirement_resolved = %d, want 1", resolved)
	}
}
