package core

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// 作業ツリーの先頭を揃える規則（前の課題のブランチに残さない・既存の PR のブランチを
// 他のスロットから外して checkout する）を、実ストアと SlotGit の偽の実装で検証する。

func switchStateAt(g *fakeSlotGit, path, branch string, dirty bool) {
	st := okState()
	st.Branch, st.Dirty = branch, dirty
	g.states[path] = st
}

// 前の課題のブランチに残っているスロットは、起動の前に base へ detach される。
func TestAcquireSlot_WorktreeLeftOnAnotherIssueBranchIsDetachedToBase(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	g := newFakeSlotGit()
	p1 := filepath.Join(s.Workspace(), worktreeSlotsDir, "r", "SL-1")
	switchStateAt(g, p1, "test/issue-1-other", false)

	a, err := s.AcquireSlot(ctx, g, worktreeRepoForTest(1), bindRunForTest(ctx))
	if err != nil {
		t.Fatal(err)
	}
	if len(g.switched) != 1 || g.switched[0].Path != p1 || g.switched[0].Req != (SlotSwitch{Ref: "refs/heads/main"}) {
		t.Fatalf("switched = %+v, want one detach to refs/heads/main at %s", g.switched, p1)
	}
	if a.BaseBranch != "" {
		t.Errorf("BaseBranch = %q, want \"\" (detached at base)", a.BaseBranch)
	}
}

// detached のスロット・clone のスロットは切り替えない。
func TestAcquireSlot_DetachedWorktreeAndCloneAreNotSwitched(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	g := newFakeSlotGit()
	switchStateAt(g, filepath.Join(s.Workspace(), worktreeSlotsDir, "r", "SL-1"), "", false)
	if _, err := s.AcquireSlot(ctx, g, worktreeRepoForTest(1), bindRunForTest(ctx)); err != nil {
		t.Fatal(err)
	}
	cl := filepath.Join(s.Workspace(), "c")
	switchStateAt(g, cl, "feat/x", false)
	a, err := s.AcquireSlot(ctx, g, cloneRepoForTest("c"), bindRunForTest(ctx))
	if err != nil {
		t.Fatal(err)
	}
	if len(g.switched) != 0 || a.BaseBranch != "feat/x" {
		t.Errorf("switched = %+v BaseBranch = %q, want no switch and feat/x", g.switched, a.BaseBranch)
	}
}

// 追従するブランチを他の idle のスロットが checkout していれば、そちらを base へ外してから
// このスロットへ checkout する。
func TestAcquireSlot_FollowUpBranchHeldByIdleSlotIsReleasedThenCheckedOut(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	g := newFakeSlotGit()
	repo := worktreeRepoForTest(2)
	p1 := filepath.Join(s.Workspace(), worktreeSlotsDir, "r", "SL-1")
	p2 := filepath.Join(s.Workspace(), worktreeSlotsDir, "r", "SL-2")
	switchStateAt(g, p1, "test/issue-9-other", false)
	switchStateAt(g, p2, "fix/issue-457-pr", false)

	a, err := s.acquireSlot(ctx, g, repo, bindRunForTest(ctx), slotChoice{Only: "SL-1", Branch: "fix/issue-457-pr"})
	if err != nil {
		t.Fatal(err)
	}
	want := []SlotSwitchCall{
		{Path: p2, Req: SlotSwitch{Ref: "refs/heads/main"}},
		{Path: p1, Req: SlotSwitch{Branch: "fix/issue-457-pr", Ref: "refs/heads/main"}},
	}
	if len(g.switched) != 2 || g.switched[0] != want[0] || g.switched[1] != want[1] {
		t.Fatalf("switched = %+v, want %+v", g.switched, want)
	}
	if a.SlotID != "SL-1" || a.BaseBranch != "fix/issue-457-pr" {
		t.Errorf("assignment = %+v", a)
	}
}

// すでにそのブランチにいるスロットは何も切り替えない。
func TestAcquireSlot_FollowUpOnAlreadyCheckedOutBranchDoesNotSwitch(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	g := newFakeSlotGit()
	switchStateAt(g, filepath.Join(s.Workspace(), worktreeSlotsDir, "r", "SL-1"), "fix/issue-457-pr", false)
	a, err := s.acquireSlot(ctx, g, worktreeRepoForTest(1), bindRunForTest(ctx), slotChoice{Branch: "fix/issue-457-pr"})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.switched) != 0 || a.BaseBranch != "fix/issue-457-pr" {
		t.Errorf("switched = %+v BaseBranch = %q", g.switched, a.BaseBranch)
	}
}

// 他のスロットが使用中（または変更あり）でそのブランチを外せなければ、割り当てない
// （無関係なブランチで起動しない）。スロットは needs_attention にもしない。
func TestAcquireSlot_FollowUpBranchHeldByBusyOrDirtySlotIsUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dirty bool
		busy  bool
	}{{"busy", false, true}, {"dirty", true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStoreForTest(t)
			ctx := context.Background()
			g := newFakeSlotGit()
			repo := worktreeRepoForTest(2)
			p1 := filepath.Join(s.Workspace(), worktreeSlotsDir, "r", "SL-1")
			p2 := filepath.Join(s.Workspace(), worktreeSlotsDir, "r", "SL-2")
			switchStateAt(g, p1, "", false)
			switchStateAt(g, p2, "fix/issue-457-pr", tc.dirty)
			if tc.busy {
				if _, err := s.acquireSlot(ctx, g, repo, bindRunForTest(ctx), slotChoice{Only: "SL-2", Branch: "fix/issue-457-pr"}); err != nil {
					t.Fatal(err)
				}
				g.switched = nil
			}
			_, err := s.acquireSlot(ctx, g, repo, bindRunForTest(ctx), slotChoice{Only: "SL-1", Branch: "fix/issue-457-pr"})
			if !errors.Is(err, ErrSlotUnavailable) {
				t.Fatalf("error = %v, want ErrSlotUnavailable", err)
			}
			if len(g.switched) != 0 {
				t.Errorf("switched = %+v, want none", g.switched)
			}
			if sl := slotByID(t, s, "SL-1"); sl.State != "idle" {
				t.Errorf("SL-1 = %+v, want idle", sl)
			}
		})
	}
}

// 切り替えに失敗したスロットは割り当てずに needs_attention にする。
func TestAcquireSlot_SwitchFailureMarksNeedsAttention(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	g := newFakeSlotGit()
	p1 := filepath.Join(s.Workspace(), worktreeSlotsDir, "r", "SL-1")
	switchStateAt(g, p1, "test/issue-1-other", false)
	g.switchErr = map[string]error{p1: errors.New("boom")}
	if _, err := s.AcquireSlot(ctx, g, worktreeRepoForTest(1), bindRunForTest(ctx)); !errors.Is(err, ErrSlotUnavailable) {
		t.Fatalf("error = %v, want ErrSlotUnavailable", err)
	}
	if sl := slotByID(t, s, "SL-1"); sl.State != "needs_attention" {
		t.Errorf("SL-1 = %+v, want needs_attention", sl)
	}
}

// ブランチが分からない再開（KeepTree）は、作業ツリーの先頭を動かさない。
func TestAcquireSlot_KeepTreeDoesNotSwitch(t *testing.T) {
	s := newStoreForTest(t)
	g := newFakeSlotGit()
	switchStateAt(g, filepath.Join(s.Workspace(), worktreeSlotsDir, "r", "SL-1"), "fix/own", false)
	a, err := s.acquireSlot(context.Background(), g, worktreeRepoForTest(1), bindRunForTest(context.Background()), slotChoice{KeepTree: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.switched) != 0 || a.BaseBranch != "fix/own" {
		t.Errorf("switched = %+v BaseBranch = %q", g.switched, a.BaseBranch)
	}
}
