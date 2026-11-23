package core

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
)

// 区切り S4c-3 の見せ方を、実ストアで検証する（AC-100・AC-105・AC-106・AC-107・AC-117）。
// タスク a・b を止める保留（H-1・H-2）が開いている、人間対応待ちの課題を使う。

func twoTaskHoldsFixture(t *testing.T) *holdScopeFixture {
	t.Helper()
	f := newHoldScopeFixture(t)
	f.waitHold(t, f.runA)
	if err := f.overlay(f.taskB, HoldOptionsInput{Scope: HoldScopeTask}); err != nil {
		t.Fatalf("overlay: %v", err)
	}
	return f
}

func TestHoldView_ChallengeDetailHoldsCarryHoldIDAndScope(t *testing.T) { // AC-106, AC-107
	t.Parallel()
	f := twoTaskHoldsFixture(t)
	d, err := f.s.GetChallenge(context.Background(), f.id)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Holds) != 2 {
		t.Fatalf("holds = %d, want 2", len(d.Holds))
	}
	for i, want := range []string{"H-1", "H-2"} {
		if d.Holds[i].HoldID != want || d.Holds[i].ScopeLabel() != "task" {
			t.Errorf("holds[%d] = %s/%s, want %s/task", i, d.Holds[i].HoldID, d.Holds[i].ScopeLabel(), want)
		}
	}
}

func TestHoldView_OverviewListsOpenHoldsPerHoldAndCountsTheChallengeOnce(t *testing.T) { // AC-105, AC-117
	t.Parallel()
	f := twoTaskHoldsFixture(t)
	ov, err := f.s.GetOverview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range ov.NeedsHumanChallenges {
		if c.ID == f.id {
			n++
		}
	}
	if n != 1 {
		t.Errorf("needs_human に課題 %s が %d 件。1 件のはず", f.id, n)
	}
	hs := ov.OpenHolds[f.id]
	if len(hs) != 2 || hs[0].HoldID != "H-1" || hs[1].HoldID != "H-2" {
		t.Fatalf("OpenHolds = %+v, want H-1・H-2 の 2 件", hs)
	}
	if hs[0].TaskTitle != "タスク a" || hs[1].TaskTitle != "タスク b" {
		t.Errorf("OpenHolds = %+v, want タスクの題名 ", hs)
	}
}

func TestHoldView_TaskHoldOnlyChallengeIsAJ6AutoTargetLikeAnyAwaitingHumanChallenge(t *testing.T) { // AC-100
	t.Parallel()
	f := twoTaskHoldsFixture(t)
	// 動いている実行は課題の単位の判断点の対象から外す（いまの規則）ので、実行を終えた状態にして、保留の重なりだけを見る。
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		for _, q := range []string{
			`UPDATE run SET result = 'succeeded' WHERE challenge_id = ` + strconv.FormatInt(f.cid, 10) + ` AND result IS NULL`,
			`UPDATE contract_run SET ended_at = '2026-11-22T00:00:00.000Z' WHERE challenge_id = ` + strconv.FormatInt(f.cid, 10) + ` AND ended_at IS NULL`,
			`INSERT INTO occurrence (challenge_id, kind, activity_id, outcome, source, created_at) VALUES (` + strconv.FormatInt(f.cid, 10) + `, 'delegation_failed', (SELECT MAX(id) FROM activity), 'bad', 'core', '2026-11-22T00:00:00.000Z')`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var targets []int64
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		targets, err = selectJ6AutoTargets(context.Background(), tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0] != f.cid {
		t.Errorf("selectJ6AutoTargets = %v, want [%d]", targets, f.cid)
	}
}
