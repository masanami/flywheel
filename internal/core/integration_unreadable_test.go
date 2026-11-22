package core

// 「入れてよい」を返した実行について、上流を読めない回りの数え（区切り S4b-2。AC-215〜AC-218）を、実ストアで確かめる。

import (
	"errors"
	"testing"
)

func (f *igLandFixture) unreadableRounds(t *testing.T) int64 {
	t.Helper()
	return f.count(t, `SELECT upstream_unreadable_rounds FROM contract_run`)
}

// AC-215・AC-217: 読めない回りが 3 回続くと、止まり方 upstream_unreadable の保留（スーパーバイザー待ち）ができ、実行は取り消さない。
func TestUpstreamUnreadable_AC215_AC217_ThreeRoundsHoldAndKeepTheRun(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.landed(t)
	f.checks.err = errors.New("boom")
	for want := int64(1); want <= 2; want++ {
		f.succeed(t)
		if f.count(t, `SELECT COUNT(*) FROM hold`) != 0 || f.unreadableRounds(t) != want {
			t.Fatalf("round %d: holds = %d, rounds = %d", want, f.count(t, `SELECT COUNT(*) FROM hold`), f.unreadableRounds(t))
		}
	}
	f.succeed(t)
	if got := f.text(t, `SELECT stop_kind || '/' || supervisor_state || '/' || task_id FROM hold`); got != "upstream_unreadable/pending/1" {
		t.Fatalf("hold = %q, want upstream_unreadable/pending/1", got)
	}
	if f.status(t, f.cid) != StatusAwaitingHuman {
		t.Errorf("status = %s, want awaiting_human", f.status(t, f.cid))
	}
	// AC-217: 契約の実行の状態は取り消しではなく、行は開いたまま。
	if st := f.text(t, `SELECT state FROM contract_run`); st == string(ContractStateCancelled) {
		t.Errorf("contract run state = %q, want not cancelled", st)
	}
	if f.text(t, `SELECT COALESCE(ended_at, '') FROM contract_run`) != "" {
		t.Error("the contract run must stay open")
	}
	if f.unreadableRounds(t) != 0 {
		t.Errorf("rounds after the hold = %d, want 0 (counted again after the hold)", f.unreadableRounds(t))
	}
}

// AC-216・AC-218: 2 回読めず、3 回目に読めたとき、保留はできず、数えは 0 に戻る。
func TestUpstreamUnreadable_AC216_AC218_ReadableRoundResetsTheCount(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.landed(t)
	f.checks.err = errors.New("boom")
	f.succeed(t)
	f.succeed(t)
	if f.unreadableRounds(t) != 2 {
		t.Fatalf("rounds = %d, want 2", f.unreadableRounds(t))
	}
	f.checks.err = nil
	f.succeed(t)
	if f.count(t, `SELECT COUNT(*) FROM hold`) != 0 {
		t.Fatal("a hold was made although the third round could be read") // AC-216
	}
	if f.unreadableRounds(t) != 0 { // AC-218
		t.Errorf("rounds = %d, want 0", f.unreadableRounds(t))
	}
}

// 宣言の supervisor が偽なら、止まり方は残し、スーパーバイザー待ちにしない。
func TestUpstreamUnreadable_SupervisorDisabledLeavesTheHoldForTheOwner(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.agent.Supervisor = false
	f.landed(t)
	f.checks.err = errors.New("boom")
	f.succeed(t)
	f.succeed(t)
	f.succeed(t)
	if got := f.text(t, `SELECT stop_kind || '/' || COALESCE(supervisor_state, 'NULL') FROM hold`); got != "upstream_unreadable/NULL" {
		t.Fatalf("hold = %q, want upstream_unreadable/NULL", got)
	}
}

// 課題が保留の間の読めない回りは数えない（回答の後の最初の回りで、すぐ同じ保留が立たない）。
func TestUpstreamUnreadable_RoundsWhileHeldAreNotCounted(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.landed(t)
	f.checks.err = errors.New("boom")
	for i := 0; i < 3; i++ {
		f.succeed(t)
	}
	for i := 0; i < 3; i++ {
		f.succeed(t) // 保留の間
	}
	if f.unreadableRounds(t) != 0 {
		t.Fatalf("rounds while held = %d, want 0", f.unreadableRounds(t))
	}
	f.sql(t, `UPDATE hold SET answer = 'a', answered_at = ?, answered_by = 'owner'`, formatTimestamp(f.s.currentTime()))
	f.sql(t, `UPDATE challenge SET status = 'in_progress' WHERE id = 1`)
	f.succeed(t)
	if f.count(t, `SELECT COUNT(*) FROM hold`) != 1 || f.unreadableRounds(t) != 1 {
		t.Fatalf("holds = %d, rounds = %d, want 1, 1", f.count(t, `SELECT COUNT(*) FROM hold`), f.unreadableRounds(t))
	}
}
