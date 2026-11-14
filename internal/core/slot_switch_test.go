package core

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
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
// （無関係なブランチで起動しない）。スロットは needs_attention にもしない。使用中のときに
// 待つのは委譲の割り当て（waitForBranchHolder）で、acquireSlot 自体は待たない。
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

// endAndRelease は a の run を終了させてスロットを解放する（別の goroutine から呼べるよう、
// t を使わずにエラーを返す）。
func endAndRelease(s *Store, a *SlotAssignment) error {
	ctx := context.Background()
	runID, _ := parseRunID(a.RunID)
	if err := s.mutateSlots(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE run SET result = 'succeeded', ended_at = ? WHERE id = ?`, formatTimestamp(time.Now()), runID)
		return err
	}); err != nil {
		return err
	}
	return s.ReleaseSlot(ctx, a.SlotID)
}

func shortSlotWait(t *testing.T) {
	old := slotWaitInterval
	slotWaitInterval = 10 * time.Millisecond
	t.Cleanup(func() { slotWaitInterval = old })
}

// 追従するブランチを使用中（busy）の他のスロットが checkout していれば、その委譲が終わるまで
// 待ち、空いた後にそのブランチで起動する（オーナーの決定。#526）。待つ間は割り当てない
// （SL-1 を busy で押さえない）。
func TestAcquireDelegationSlot_WaitsForBusySlotHoldingTheBranch(t *testing.T) {
	shortSlotWait(t)
	s := newStoreForTest(t)
	ctx := context.Background()
	g := newFakeSlotGit()
	repo := worktreeRepoForTest(2)
	p1 := filepath.Join(s.Workspace(), worktreeSlotsDir, "r", "SL-1")
	p2 := filepath.Join(s.Workspace(), worktreeSlotsDir, "r", "SL-2")
	switchStateAt(g, p1, "", false)
	switchStateAt(g, p2, "fix/issue-457-pr", false)
	busy, err := s.acquireSlot(ctx, g, repo, bindRunForTest(ctx), slotChoice{Only: "SL-2", Branch: "fix/issue-457-pr"})
	if err != nil {
		t.Fatal(err)
	}

	released := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		if sl := slotByIDNoT(s, "SL-1"); sl != "idle" {
			released <- errors.New("SL-1 was " + sl + " while waiting, want idle")
			return
		}
		released <- endAndRelease(s, busy)
	}()
	dc := &delegationContext{Repo: repo, Follow: &followBranch{Branch: "fix/issue-457-pr", SlotID: "SL-2"}}
	in := DelegateInput{Git: g, AgentDecl: &AgentDeclaration{TimeoutSec: AgentTimeoutSec{Delegate: 30}}}
	a, err := s.acquireDelegationSlot(ctx, in, dc, bindRunForTest(ctx))
	if rerr := <-released; rerr != nil {
		t.Fatal(rerr)
	}
	if err != nil {
		t.Fatal(err)
	}
	// 解放が取り直しの途中に起きると、SL-1 が SL-2 からブランチを外して checkout することもある。
	// どちらでも、追従するブランチで起動する。
	if a.BaseBranch != "fix/issue-457-pr" {
		t.Errorf("assignment = %+v, want fix/issue-457-pr", a)
	}
}

// 使用中のまま待つ上限（委譲の時間の上限）を超えたら ErrSlotUnavailable（全部使用中と同じ扱い）。
// 再開（Resume.Branch の経路）も同じく待ってから諦める。
func TestAcquireDelegationSlot_BusyBranchHolderPastDeadlineIsUnavailable(t *testing.T) {
	shortSlotWait(t)
	for _, resume := range []bool{false, true} {
		s := newStoreForTest(t)
		ctx := context.Background()
		g := newFakeSlotGit()
		repo := worktreeRepoForTest(2)
		p1 := filepath.Join(s.Workspace(), worktreeSlotsDir, "r", "SL-1")
		p2 := filepath.Join(s.Workspace(), worktreeSlotsDir, "r", "SL-2")
		switchStateAt(g, p1, "", false)
		switchStateAt(g, p2, "fix/issue-457-pr", false)
		if _, err := s.acquireSlot(ctx, g, repo, bindRunForTest(ctx), slotChoice{Only: "SL-2", Branch: "fix/issue-457-pr"}); err != nil {
			t.Fatal(err)
		}
		g.switched = nil
		dc := &delegationContext{Repo: repo, Follow: &followBranch{Branch: "fix/issue-457-pr"}}
		if resume {
			sl1 := "SL-1"
			dc = &delegationContext{Repo: repo, Resume: &resumePlan{Target: runRow{SlotID: &sl1}, Kind: ResumeKindAnswer, Branch: "fix/issue-457-pr"}}
		}
		in := DelegateInput{Git: g, AgentDecl: &AgentDeclaration{TimeoutSec: AgentTimeoutSec{Delegate: 1}}}
		start := time.Now()
		_, err := s.acquireDelegationSlot(ctx, in, dc, bindRunForTest(ctx))
		if !errors.Is(err, ErrSlotUnavailable) {
			t.Fatalf("resume=%v: error = %v, want ErrSlotUnavailable", resume, err)
		}
		if el := time.Since(start); el < 900*time.Millisecond {
			t.Errorf("resume=%v: returned after %v, want to wait until the deadline", resume, el)
		}
		if len(g.switched) != 0 {
			t.Errorf("resume=%v: switched = %+v, want none", resume, g.switched)
		}
		if sl := slotByID(t, s, "SL-1"); sl.State != "idle" {
			t.Errorf("resume=%v: SL-1 = %+v, want idle", resume, sl)
		}
	}
}

// ブランチを持つ他のスロットが idle で未コミットの変更があるときは待たない（待っても解けない）。
func TestAcquireDelegationSlot_DirtyBranchHolderDoesNotWait(t *testing.T) {
	s := newStoreForTest(t)
	ctx := context.Background()
	g := newFakeSlotGit()
	repo := worktreeRepoForTest(2)
	p1 := filepath.Join(s.Workspace(), worktreeSlotsDir, "r", "SL-1")
	p2 := filepath.Join(s.Workspace(), worktreeSlotsDir, "r", "SL-2")
	switchStateAt(g, p1, "", false)
	switchStateAt(g, p2, "fix/issue-457-pr", true)
	dc := &delegationContext{Repo: repo, Follow: &followBranch{Branch: "fix/issue-457-pr"}}
	in := DelegateInput{Git: g, AgentDecl: &AgentDeclaration{TimeoutSec: AgentTimeoutSec{Delegate: 30}}}
	start := time.Now()
	if _, err := s.acquireDelegationSlot(ctx, in, dc, bindRunForTest(ctx)); !errors.Is(err, ErrSlotUnavailable) {
		t.Fatalf("error = %v, want ErrSlotUnavailable", err)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Errorf("waited %v, want no wait", el)
	}
}

func slotByIDNoT(s *Store, id string) string {
	all, err := s.ListSlots(context.Background())
	if err != nil {
		return "error: " + err.Error()
	}
	for _, sl := range all {
		if sl.ID == id {
			return sl.State
		}
	}
	return "missing"
}
