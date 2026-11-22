package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは #620（前の委譲のブランチをスロットの外の作業ツリーが持つときの再委譲）を、実ストア・
// 実物の git（元のクローンと `git worktree add` で作った作業ツリー）・実際の adapter（internal/adapters/git）
// を core へ渡す CLI の結線で検証する。git が報告する持ち主のパス（macOS の /var → /private/var の
// シンボリックリンクを含む）で、置き場の中か外かの判定が成り立つことを固定する。偽の claude・gh だけを置く。

// setupHeldBranchWorkspace は direct-repo を worktree（元のクローン slot-d・1 本）にし、前の委譲の run が
// ブランチ feat/held を報告した、着手中の課題を作る。ブランチは元のクローンにあり、まだ誰も checkout しない。
func setupHeldBranchWorkspace(t *testing.T) (ws, id string) {
	t.Helper()
	ws = setupRunWorkspace(t)
	conn := strings.Replace(runConnectorsFixture, `"slots": {"provider": "clone", "paths": ["slot-d"]}`,
		`"slots": {"provider": "worktree", "base": "slot-d", "count": 1}`, 1)
	writeConnectorsJSONForTest(t, ws, conn)
	slotGit(t, filepath.Join(ws, "slot-d"), "branch", "feat/held")
	id = newInProgressForRun(t, ws, nil)
	cid := challengeIDToInternalID(t, id)
	prev := coretest.InsertRun(t, ws, coretest.InsertRunInput{ChallengeID: cid, Kind: "delegate", ChallengeVersion: 1,
		SessionID: "prev", PID: 1, Host: "h", HeartbeatAt: "2026-11-15T00:00:00.000Z", StartedAt: "2026-11-15T00:00:00.000Z",
		EndedAt: "2026-11-15T00:10:00.000Z", Result: "succeeded", MaxBudgetUSD: 1_000_000, BudgetBucket: "impl"})
	coretest.InsertRunArtifact(t, ws, prev, "branch", "feat/held", "", "")
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j3Route("b"), delegateRoute("completed")}, "")
	return ws, id
}

// ① 置き場の中の作業ツリー（前の委譲の子の一時の作業ツリー）が持つ: 委譲は起動し、スロットは既定の
// ブランチの detached HEAD で、持ち主のパスとブランチが run の保存先の記録に残る。
func TestCycle_PreviousBranchHeldInsidePlacement_StartsDetachedAndRecordsHolder_RealGit(t *testing.T) {
	parallelTools(t, false)
	ws, id := setupHeldBranchWorkspace(t)
	base := filepath.Join(ws, "slot-d")
	holder := filepath.Join(ws, ".flywheel", "worktrees", "direct-repo", "SL-1-tmp-1")
	slotGit(t, base, "worktree", "add", "-q", holder, "feat/held")

	doc := cycleDoc(t, ws)
	pm := cyclePhase(t, doc, "run")
	if ns, _ := pm["not_started"].([]any); len(ns) != 0 {
		t.Fatalf("not_started = %v, want none", ns)
	}
	items, _ := pm["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["challenge_id"] != id {
		t.Fatalf("run items = %v, want one delegation of %s", items, id)
	}
	runID, _ := items[0].(map[string]any)["run_id"].(string)

	slot := filepath.Join(ws, ".flywheel", "worktrees", "direct-repo", "SL-1")
	if head, main := slotGit(t, slot, "rev-parse", "HEAD"), slotGit(t, base, "rev-parse", "refs/heads/main"); head != main {
		t.Errorf("slot HEAD = %s, want refs/heads/main (%s)", head, main)
	}
	if b := slotGit(t, slot, "rev-parse", "--abbrev-ref", "HEAD"); b != "HEAD" {
		t.Errorf("slot branch = %q, want a detached HEAD", b)
	}
	if b := slotGit(t, holder, "rev-parse", "--abbrev-ref", "HEAD"); b != "feat/held" {
		t.Errorf("holder branch = %q, want feat/held (left as is)", b)
	}
	data, err := os.ReadFile(filepath.Join(ws, ".flywheel", "runs", runID, "slot-branch-held.txt"))
	if err != nil {
		t.Fatalf("run record: %v", err)
	}
	// git が報告するパスはシンボリックリンクを解決した形（macOS の /private/var）でもよい。
	realHolder, err := filepath.EvalSymlinks(holder)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(data); !strings.Contains(s, "feat/held") || (!strings.Contains(s, holder) && !strings.Contains(s, realHolder)) {
		t.Errorf("run record = %q, want feat/held and %s", s, holder)
	}
}

// ② 置き場の外（元のクローン）が持つ: 委譲は起動せず、cycle --json の not_started[] は理由
// slot_unavailable で、detail にブランチ名と持ち主のパスが入る。
func TestCycle_PreviousBranchHeldOutsidePlacement_NotStartedWithDetail_RealGit(t *testing.T) {
	parallelTools(t, false)
	ws, id := setupHeldBranchWorkspace(t)
	base := filepath.Join(ws, "slot-d")
	slotGit(t, base, "checkout", "-q", "feat/held")

	doc := cycleDoc(t, ws)
	pm := cyclePhase(t, doc, "run")
	ns, _ := pm["not_started"].([]any)
	// 進みの無い回りは、回りごとに同じ理由の項目を足す（作業ツリーが空かないので、回りを重ねても同じ）。
	if len(ns) < 1 {
		t.Fatalf("not_started = %v, want at least one", ns)
	}
	for _, e := range ns {
		n := e.(map[string]any)
		if n["challenge_id"] != id || n["reason"] != "slot_unavailable" {
			t.Fatalf("not_started entry = %v, want %s slot_unavailable", n, id)
		}
	}
	n := ns[0].(map[string]any)
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := n["detail"].(string)
	if !strings.Contains(d, "feat/held") || (!strings.Contains(d, base) && !strings.Contains(d, realBase)) {
		t.Errorf("detail = %q, want feat/held and %s", d, base)
	}
	if n := len(delegateRunsOf(t, ws)); n != 1 {
		t.Errorf("delegate runs = %d, want 1 (only the previous one)", n)
	}
}
