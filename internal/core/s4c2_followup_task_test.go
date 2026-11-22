package core

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// 区切り S4c-2 の 1: 出どころが followup で足されたタスクの印（added.followup_id・additions[].followup_id）と、
// フォローアップの上げた先のタスク（promoted_task_id）を読む。出どころ followup の足す依頼は、テストの中で直接置く。

func TestFollowupTask_AddedAndAdditionsCarryFollowupID_AC84(t *testing.T) {
	t.Parallel()
	s, id, tasks := additionFixture(t)
	ctx := context.Background()
	cid, _ := parseChallengeID(id)
	tid := mustTaskID(t, tasks[1].ID)
	writeForTest(t, s, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO followup (challenge_id, title, source, status, created_at, updated_at) VALUES (?, 'f', 'learn', 'open', ?, ?)`,
			cid, formatTimestamp(time.Now()), formatTimestamp(time.Now())); err != nil {
			return err
		}
		res, err := tx.Exec(`INSERT INTO task_addition (challenge_id, origin, state, followup_id, budget_impl_micros, budget_review_micros, created_at) VALUES (?, 'followup', 'applied', 1, 2000000, 1000000, ?)`,
			cid, formatTimestamp(time.Now()))
		if err != nil {
			return err
		}
		aid, _ := res.LastInsertId()
		_, err = tx.Exec(`UPDATE task SET addition_id = ? WHERE id = ?`, aid, tid)
		return err
	})
	d, err := s.GetChallenge(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tk := range d.Tasks {
		if tk.ID == tasks[0].ID || tk.ID == tasks[1].ID {
			seen++
		}
		switch tk.ID {
		case tasks[1].ID:
			if tk.Added == nil || tk.Added.Origin != "followup" || tk.Added.FollowupID == nil || *tk.Added.FollowupID != "F-1" {
				t.Errorf("%s.Added = %+v, want origin followup and followup_id F-1", tk.ID, tk.Added)
			}
		case tasks[0].ID:
			if tk.Added != nil {
				t.Errorf("%s.Added = %+v, want nil (made by the plan approval)", tk.ID, tk.Added)
			}
		}
	}
	if seen != 2 {
		t.Errorf("saw %d of the 2 tasks in the detail, want both", seen)
	}
	if len(d.Additions) != 1 || d.Additions[0].FollowupID == nil || *d.Additions[0].FollowupID != "F-1" {
		t.Errorf("Additions = %+v, want 1 with followup_id F-1", d.Additions)
	}
}

func TestFollowupTask_AdditionFromExpandHasNoFollowupID(t *testing.T) {
	t.Parallel()
	s, id, tasks := additionFixture(t)
	ctx := context.Background()
	if _, err := s.CreateTaskAddition(ctx, ChannelCLI, id, TaskAdditionInput{Origin: TaskAdditionOriginExpand, OriginTaskID: tasks[0].ID}); err != nil {
		t.Fatal(err)
	}
	as, err := s.ListTaskAdditions(ctx, id)
	if err != nil || len(as) != 1 || as[0].FollowupID != nil {
		t.Errorf("ListTaskAdditions = %+v, err %v; want 1 with nil followup_id", as, err)
	}
}

func TestFollowupTask_PromotedTaskIDIsRead_AC85(t *testing.T) {
	t.Parallel()
	s, id, tasks := additionFixture(t)
	ctx := context.Background()
	cid, _ := parseChallengeID(id)
	tid := mustTaskID(t, tasks[0].ID)
	writeForTest(t, s, func(tx *sql.Tx) error {
		now := formatTimestamp(time.Now())
		if _, err := tx.Exec(`INSERT INTO followup (challenge_id, title, source, status, promoted_task_id, created_at, updated_at) VALUES (?, 'up', 'learn', 'open', ?, ?, ?)`, cid, tid, now, now); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO followup (challenge_id, title, source, status, created_at, updated_at) VALUES (?, 'no', 'learn', 'open', ?, ?)`, cid, now, now); err != nil {
			return err
		}
		return nil
	})
	fs, err := s.ListFollowups(ctx, FollowupFilter{})
	if err != nil || len(fs) != 2 {
		t.Fatalf("ListFollowups = %+v, err %v", fs, err)
	}
	if fs[0].PromotedTaskID == nil || *fs[0].PromotedTaskID != tasks[0].ID || fs[0].PromotedChallengeID != nil {
		t.Errorf("promoted followup = %+v, want promoted_task_id %s and no challenge", fs[0], tasks[0].ID)
	}
	if fs[1].PromotedTaskID != nil {
		t.Errorf("unpromoted followup PromotedTaskID = %v, want nil (AC-88)", *fs[1].PromotedTaskID)
	}
}
