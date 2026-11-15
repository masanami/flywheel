package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 追従するブランチを、スロットの外の作業ツリーが持つときの割り当て。置き場
// （.flywheel/worktrees/<リポジトリ名>/）の中の作業ツリーが持つなら、スロットを既定の
// ブランチへそろえて起動し、置き場の外が持つなら起動せず理由の詳細を出す。

func heldBranchSetup(t *testing.T, s *Store, g *fakeSlotGit, holder string) {
	t.Helper()
	g.branchSwitchErr = map[string]error{}
	for _, id := range []string{"SL-1", "SL-2"} {
		p := filepath.Join(s.Workspace(), worktreeSlotsDir, "r", id)
		switchStateAt(g, p, "", false)
		g.branchSwitchErr[p] = &heldTestError{path: holder}
	}
}

// heldTestError は adapters/git が返す形（ErrBranchCheckedOutElsewhere を包んだ、パスつきの失敗）。
type heldTestError struct{ path string }

func (e *heldTestError) Error() string { return "git checkout: already used by worktree at " + e.path }
func (e *heldTestError) Unwrap() error {
	return &BranchCheckedOutElsewhereError{Path: e.path}
}

func TestAcquireSlot_BranchHeldInsidePlacementStartsAlignedToDefaultBranch(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	g := newFakeSlotGit()
	holder := filepath.Join(s.Workspace(), worktreeSlotsDir, "r", "SL-1-tmp-493")
	heldBranchSetup(t, s, g, holder)

	a, err := s.acquireSlot(ctx, g, worktreeRepoForTest(2), bindRunForTest(ctx), slotChoice{Branch: "feat/b"})
	if err != nil {
		t.Fatalf("acquireSlot: %v", err)
	}
	if a.HeldBy != holder || a.HeldBranch != "feat/b" {
		t.Errorf("HeldBy/HeldBranch = %q/%q, want %q/feat/b", a.HeldBy, a.HeldBranch, holder)
	}
	if a.BaseBranch != "" {
		t.Errorf("BaseBranch = %q, want \"\" (aligned to the default branch)", a.BaseBranch)
	}
	last := g.switched[len(g.switched)-1]
	if last.Req != (SlotSwitch{Ref: "refs/heads/main"}) {
		t.Errorf("last switch = %+v, want a detach to refs/heads/main", last)
	}
	if sl := slotByID(t, s, a.SlotID); sl.State != "busy" {
		t.Errorf("slot = %+v, want busy", sl)
	}
}

func TestAcquireSlot_BranchHeldOutsidePlacementIsUnavailableWithDetail(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	g := newFakeSlotGit()
	holder := filepath.Join(s.Workspace(), "base")
	heldBranchSetup(t, s, g, holder)

	_, err := s.acquireSlot(ctx, g, worktreeRepoForTest(2), bindRunForTest(ctx), slotChoice{Branch: "feat/b"})
	if !errors.Is(err, ErrSlotUnavailable) {
		t.Fatalf("err = %v, want ErrSlotUnavailable", err)
	}
	d := slotUnavailableDetail(err)
	if !strings.Contains(d, "feat/b") || !strings.Contains(d, holder) {
		t.Errorf("detail = %q, want the branch and %s", d, holder)
	}
	for _, id := range []string{"SL-1", "SL-2"} {
		if sl := slotByID(t, s, id); sl.State != "idle" {
			t.Errorf("%s = %+v, want idle", id, sl)
		}
	}
}

// 他のスロットが理由なく使えないとき（全スロット使用中）は詳細を付けない。
func TestAcquireSlot_NoHeldBranchHasNoDetail(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	g := newFakeSlotGit()
	_, err := s.acquireSlot(ctx, g, worktreeRepoForTest(0), bindRunForTest(ctx), slotChoice{})
	if !errors.Is(err, ErrSlotUnavailable) || slotUnavailableDetail(err) != "" {
		t.Errorf("err = %v, detail %q", err, slotUnavailableDetail(err))
	}
}

func TestInWorktreePlacement(t *testing.T) {
	s := newStoreForTest(t)
	repo := worktreeRepoForTest(1)
	area := filepath.Join(s.Workspace(), worktreeSlotsDir, "r")
	for p, want := range map[string]bool{
		filepath.Join(area, "SL-1-tmp-9"):    true,
		area:                                 false,
		filepath.Join(s.Workspace(), "base"): false,
		filepath.Join(s.Workspace(), worktreeSlotsDir, "other", "x"): false,
		filepath.Join(area, "..", "x"):                               false,
	} {
		if got := s.inWorktreePlacement(repo, p); got != want {
			t.Errorf("inWorktreePlacement(%s) = %v, want %v", p, got, want)
		}
	}
}

// 再委譲（差し戻し）のフロー: 前の委譲のブランチを置き場の中の作業ツリーが持つとき、委譲は起動し、
// どの作業ツリーが持っていたかが結果の補足と run の保存先に残る。
func TestRunDelegation_RedelegationStartsWhenPreviousBranchIsHeldInsidePlacement(t *testing.T) {
	f := newResumeFixture(t)
	id := f.worktreeChallenge(t)
	f.deleg.queue = []JudgmentLaunchOutput{{Result: RunResultSucceeded, ReportedTotalCostUSD: float64Ptr(1), StructuredOutput: reportJSON(func(m map[string]any) {
		m["branch"] = "feat/w"
	})}}
	f.runDelegation(t, id)
	f.setStatus(t, id, "in_progress")

	holder := pathOf(f, ".flywheel/worktrees/wt-repo/SL-1-tmp-1")
	f.git.branchSwitchErr = map[string]error{}
	for _, sl := range []string{"SL-1", "SL-2"} {
		f.git.branchSwitchErr[pathOf(f, ".flywheel/worktrees/wt-repo/"+sl)] = &heldTestError{path: holder}
	}
	launches := len(f.deleg.launched())
	res := f.runDelegation(t, id)
	if len(res.NotStarted) != 0 || len(f.deleg.launched()) != launches+1 {
		t.Fatalf("notStarted = %+v, launches %d→%d, want a launch", res.NotStarted, launches, len(f.deleg.launched()))
	}
	if len(res.Items) != 1 || !strings.Contains(res.Items[0].Note, holder) {
		t.Fatalf("items = %+v, want a note naming %s", res.Items, holder)
	}
	runID := f.delegateRuns(t)[len(f.delegateRuns(t))-1].ID
	data, err := os.ReadFile(filepath.Join(runDirPath(f.s.Workspace(), runID), slotBranchHeldNoteFile))
	if err != nil || !strings.Contains(string(data), holder) {
		t.Errorf("run record = %q, %v, want the holder path", data, err)
	}
}

// 置き場の外が持つときは起動せず、not_started の slot_unavailable に、ブランチと作業ツリーのパスが付く。
func TestRunDelegation_RedelegationNotStartedWithDetailWhenPreviousBranchIsHeldOutsidePlacement(t *testing.T) {
	f := newResumeFixture(t)
	id := f.worktreeChallenge(t)
	f.deleg.queue = []JudgmentLaunchOutput{{Result: RunResultSucceeded, ReportedTotalCostUSD: float64Ptr(1), StructuredOutput: reportJSON(func(m map[string]any) {
		m["branch"] = "feat/w"
	})}}
	f.runDelegation(t, id)
	f.setStatus(t, id, "in_progress")

	holder := pathOf(f, "wt-base")
	f.git.branchSwitchErr = map[string]error{}
	for _, sl := range []string{"SL-1", "SL-2"} {
		f.git.branchSwitchErr[pathOf(f, ".flywheel/worktrees/wt-repo/"+sl)] = &heldTestError{path: holder}
	}
	launches := len(f.deleg.launched())
	res, err := f.run(t, nil) // 周の中の委譲（理由を not_started で返す）
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedSlotUnavailable || len(f.deleg.launched()) != launches {
		t.Fatalf("notStarted = %+v, launches %d→%d", res.NotStarted, launches, len(f.deleg.launched()))
	}
	if d := res.NotStarted[0].Detail; !strings.Contains(d, "feat/w") || !strings.Contains(d, holder) {
		t.Errorf("detail = %q, want the branch and %s", d, holder)
	}
}

// ブランチを持つのがスロット自身の作業ツリー（走査で外せなかったもの）なら、置き場の中でも
// 既定のブランチへそろえず、従来どおりそのスロットは使わない（#526 の扱いを変えない）。
func TestAcquireSlot_BranchHeldBySlotItselfIsNotAlignedToDefaultBranch(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	g := newFakeSlotGit()
	holder := filepath.Join(s.Workspace(), worktreeSlotsDir, "r", "SL-2")
	heldBranchSetup(t, s, g, holder)

	_, err := s.acquireSlot(ctx, g, worktreeRepoForTest(2), bindRunForTest(ctx), slotChoice{Branch: "feat/b"})
	if !errors.Is(err, ErrSlotUnavailable) {
		t.Fatalf("err = %v, want ErrSlotUnavailable", err)
	}
}

// run の保存先へ書けなくても委譲は止めず、書けなかったことと理由を補足に出す（失敗を捨てない）。
func TestRecordSlotBranchHeld_WriteFailureIsInTheNote(t *testing.T) {
	s := newStoreForTest(t)
	runs := filepath.Join(s.Workspace(), flywheelDirName, "runs")
	if err := os.MkdirAll(runs, 0o755); err != nil {
		t.Fatal(err)
	}
	// run の保存先のディレクトリと同じ名前のファイルを置き、ディレクトリを作れなくする。
	if err := os.WriteFile(filepath.Join(runs, "R-99"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	holder := filepath.Join(s.Workspace(), worktreeSlotsDir, "r", "SL-1-tmp-1")
	note := s.recordSlotBranchHeld(&SlotAssignment{SlotID: "SL-1", RunID: "R-99", HeldBy: holder, HeldBranch: "feat/b"})
	if !strings.Contains(note, holder) || !strings.Contains(note, "writing "+slotBranchHeldNoteFile+" failed") {
		t.Errorf("note = %q, want the holder and the write failure", note)
	}
}
