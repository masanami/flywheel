package core

import (
	"context"
	"database/sql"
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

// 再開でない新しい委譲（計画の版が変わった後）でも、前の委譲が SL-2 でブランチ B を報告し、
// SL-2 が B を checkout したまま idle なら、新しい委譲は B のまま起動される（#526）。
func TestAcquireDelegationSlot_NewDelegationFollowsPreviousRunBranch(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	g := newFakeSlotGit()
	repo := worktreeRepoForTest(2)
	p1 := filepath.Join(s.Workspace(), worktreeSlotsDir, "r", "SL-1")
	p2 := filepath.Join(s.Workspace(), worktreeSlotsDir, "r", "SL-2")
	switchStateAt(g, p1, "", false)
	switchStateAt(g, p2, "fix/issue-457-pr", false)

	sl2 := "SL-2"
	prev := []runRow{
		{ID: "R-1", Result: RunResultSucceeded, Output: string(reportJSON(func(m map[string]any) { m["branch"] = "fix/issue-457-pr" })), SlotID: &sl2},
		{ID: "R-2", Result: RunResultLaunchFailed},
	}
	var follow *followBranch
	if err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		follow, err = loadFollowBranch(ctx, tx, prev, nil, repo.DefaultBranch)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if follow == nil || *follow != (followBranch{Branch: "fix/issue-457-pr", SlotID: "SL-2"}) {
		t.Fatalf("follow = %+v", follow)
	}
	dc := &delegationContext{Repo: repo, Follow: follow}
	a, err := s.acquireDelegationSlot(ctx, DelegateInput{Git: g}, dc, bindRunForTest(ctx))
	if err != nil {
		t.Fatal(err)
	}
	if a.SlotID != "SL-2" || a.BaseBranch != "fix/issue-457-pr" || len(g.switched) != 0 {
		t.Errorf("assignment = %+v switched = %+v, want SL-2 on fix/issue-457-pr without switching", a, g.switched)
	}
}

// 前の run のスロットが分からず別のスロットに割り当てるときも、B を checkout している
// idle・変更なしのスロットから B を外して、割り当てたスロットで checkout する。
func TestAcquireDelegationSlot_NewDelegationReleasesBranchFromOtherIdleSlot(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	g := newFakeSlotGit()
	repo := worktreeRepoForTest(2)
	p1 := filepath.Join(s.Workspace(), worktreeSlotsDir, "r", "SL-1")
	p2 := filepath.Join(s.Workspace(), worktreeSlotsDir, "r", "SL-2")
	switchStateAt(g, p1, "test/issue-9-other", false)
	switchStateAt(g, p2, "fix/issue-457-pr", false)

	// 前の run のスロットの記録が無い（SlotID が空）: 候補の先頭の SL-1 で B を checkout する。
	dc := &delegationContext{Repo: repo, Follow: &followBranch{Branch: "fix/issue-457-pr"}}
	a, err := s.acquireDelegationSlot(ctx, DelegateInput{Git: g}, dc, bindRunForTest(ctx))
	if err != nil {
		t.Fatal(err)
	}
	want := []SlotSwitchCall{
		{Path: p2, Req: SlotSwitch{Ref: "refs/heads/main"}},
		{Path: p1, Req: SlotSwitch{Branch: "fix/issue-457-pr", Ref: "refs/heads/main"}},
	}
	if a.SlotID != "SL-1" || a.BaseBranch != "fix/issue-457-pr" || len(g.switched) != 2 || g.switched[0] != want[0] || g.switched[1] != want[1] {
		t.Errorf("assignment = %+v switched = %+v, want %+v", a, g.switched, want)
	}
}

// 前の run が無い・ブランチが既定ブランチ・報告が読めないときは追従しない（base へ揃える）。
func TestLoadFollowBranch_NoneWhenNoUsableBranch(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	for name, runs := range map[string][]runRow{
		"no runs":        nil,
		"default branch": {{ID: "R-1", Result: RunResultSucceeded, Output: string(reportJSON(func(m map[string]any) { m["branch"] = "main" }))}},
		"unreadable":     {{ID: "R-1", Result: RunResultSucceeded, Output: "not json"}},
		"failed run":     {{ID: "R-1", Result: RunResultLaunchFailed}},
	} {
		var follow *followBranch
		if err := s.db.Read(ctx, func(tx *sql.Tx) error {
			var err error
			follow, err = loadFollowBranch(ctx, tx, runs, nil, "main")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if follow != nil {
			t.Errorf("%s: follow = %+v, want nil", name, follow)
		}
	}
	// 追従しない新しい委譲は、前の課題のブランチに残るスロットを base へ揃える。
	g := newFakeSlotGit()
	p1 := filepath.Join(s.Workspace(), worktreeSlotsDir, "r", "SL-1")
	switchStateAt(g, p1, "test/issue-1-other", false)
	if _, err := s.acquireDelegationSlot(ctx, DelegateInput{Git: g}, &delegationContext{Repo: worktreeRepoForTest(1)}, bindRunForTest(ctx)); err != nil {
		t.Fatal(err)
	}
	if len(g.switched) != 1 || g.switched[0] != (SlotSwitchCall{Path: p1, Req: SlotSwitch{Ref: "refs/heads/main"}}) {
		t.Errorf("switched = %+v, want one detach to base", g.switched)
	}
}
