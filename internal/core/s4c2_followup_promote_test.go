package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// 区切り S4c-2 の 2: 上げた先を読む所（タスクの列があればタスク）・タスクの済みでフォローアップを済みにする・上げ直せる条件。

// s4c2PromotedFollowup は、課題 id のタスク task へ上げたフォローアップを 1 件置き、その ID（F-n）を返す。
func s4c2PromotedFollowup(t *testing.T, s *Store, id string, task Task, taskStatus TaskStatus) string {
	t.Helper()
	cid, _ := parseChallengeID(id)
	tid := mustTaskID(t, task.ID)
	var fid int64
	writeForTest(t, s, func(tx *sql.Tx) error {
		now := formatTimestamp(time.Now())
		res, err := tx.Exec(`INSERT INTO followup (challenge_id, title, source, status, promoted_task_id, created_at, updated_at) VALUES (?, 'up', 'learn', 'open', ?, ?, ?)`, cid, tid, now, now)
		if err != nil {
			return err
		}
		fid, _ = res.LastInsertId()
		_, err = tx.Exec(`UPDATE task SET status = ? WHERE id = ?`, string(taskStatus), tid)
		return err
	})
	return formatFollowupID(fid)
}

func s4c2AddAddition(t *testing.T, s *Store, id, followupID string, state TaskAdditionState) {
	t.Helper()
	cid, _ := parseChallengeID(id)
	fid, _ := parseFollowupID(followupID)
	writeForTest(t, s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO task_addition (challenge_id, origin, state, followup_id, created_at) VALUES (?, 'followup', ?, ?, ?)`,
			cid, string(state), fid, formatTimestamp(time.Now()))
		return err
	})
}

func mustFollowupNum(t *testing.T, id string) int64 {
	t.Helper()
	n, ok := parseFollowupID(id)
	if !ok {
		t.Fatalf("malformed followup id %q", id)
	}
	return n
}

func s4c2OpenFollowup(t *testing.T, s *Store, id string) string {
	t.Helper()
	cid, _ := parseChallengeID(id)
	var fid int64
	writeForTest(t, s, func(tx *sql.Tx) error {
		now := formatTimestamp(time.Now())
		res, err := tx.Exec(`INSERT INTO followup (challenge_id, title, source, status, created_at, updated_at) VALUES (?, 'f', 'learn', 'open', ?, ?)`, cid, now, now)
		if err != nil {
			return err
		}
		fid, _ = res.LastInsertId()
		return nil
	})
	return formatFollowupID(fid)
}

func s4c2Triage(t *testing.T, s *Store) int {
	t.Helper()
	n, err := s.CountOpenFollowupsToTriage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func s4c2SetTask(t *testing.T, s *Store, task Task, to TaskStatus) {
	t.Helper()
	if err := s.mutate(context.Background(), ChannelCLI, func(tx *sql.Tx, rec *activityRecorder) error {
		_, err := setTaskStatus(context.Background(), tx, rec, mustTaskID(t, task.ID), to)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func s4c2Followup(t *testing.T, s *Store, id string) Followup {
	t.Helper()
	fs, err := s.ListFollowups(context.Background(), FollowupFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("followup %s not found", id)
	return Followup{}
}

// AC-60
func TestFollowupTask_PendingPromotedTaskIsNotToTriage_AC60(t *testing.T) {
	t.Parallel()
	s, id, tasks := additionFixture(t)
	s4c2PromotedFollowup(t, s, id, tasks[0], TaskStatusPending)
	if n := s4c2Triage(t, s); n != 0 {
		t.Errorf("triage count = %d, want 0 (上げた先のタスクが未着手)", n)
	}
	// 取りやめになれば、また対象に入る。
	s4c2SetTask(t, s, tasks[0], TaskStatusCancelled)
	if n := s4c2Triage(t, s); n != 1 {
		t.Errorf("triage count after cancel = %d, want 1", n)
	}
}

func TestFollowupTask_OpenAdditionIsNotToTriage(t *testing.T) {
	t.Parallel()
	s, id, _ := additionFixture(t)
	f := s4c2OpenFollowup(t, s, id)
	if n := s4c2Triage(t, s); n != 1 {
		t.Fatalf("triage count = %d, want 1", n)
	}
	s4c2AddAddition(t, s, id, f, TaskAdditionPending)
	if n := s4c2Triage(t, s); n != 0 {
		t.Errorf("triage count with pending addition = %d, want 0", n)
	}
}

// AC-66・AC-67
func TestFollowupTask_TaskDoneCompletesFollowupWithTaskID_AC66_AC67(t *testing.T) {
	t.Parallel()
	s, id, tasks := additionFixture(t)
	f := s4c2PromotedFollowup(t, s, id, tasks[0], TaskStatusVerifying)
	s4c2SetTask(t, s, tasks[0], TaskStatusDone)
	got := s4c2Followup(t, s, f)
	if got.Status != FollowupStatusDone {
		t.Fatalf("status = %s, want done (AC-66)", got.Status)
	}
	if got.Note == nil || !strings.Contains(*got.Note, tasks[0].ID) {
		t.Errorf("note = %v, want it to contain %s (AC-67)", got.Note, tasks[0].ID)
	}
	if len(fuActs(t, s, "followup", ActivityActionFollowupDone)) != 1 {
		t.Errorf("followup_done activities != 1")
	}
}

func TestFollowupTask_TaskDoneLeavesDroppedAndOtherFollowups(t *testing.T) {
	t.Parallel()
	s, id, tasks := additionFixture(t)
	other := s4c2PromotedFollowup(t, s, id, tasks[1], TaskStatusVerifying)
	dropped := s4c2PromotedFollowup(t, s, id, tasks[0], TaskStatusVerifying)
	writeForTest(t, s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE followup SET status = 'dropped', reason = 'r' WHERE id = ?`, mustFollowupNum(t, dropped))
		return err
	})
	s4c2SetTask(t, s, tasks[0], TaskStatusDone) // 済み・捨てたものがあっても、タスクの済みは失敗しない
	if got := s4c2Followup(t, s, other); got.Status != FollowupStatusOpen {
		t.Errorf("other task's followup status = %s, want open", got.Status)
	}
	if got := s4c2Followup(t, s, dropped); got.Status != FollowupStatusDropped {
		t.Errorf("dropped followup status = %s, want dropped", got.Status)
	}
}

// 課題の完了では、タスクの列に値があるフォローアップを済みにしない。
func TestFollowupTask_ChallengeDoneSkipsTaskPromoted(t *testing.T) {
	t.Parallel()
	s, id, tasks := additionFixture(t)
	f := s4c2PromotedFollowup(t, s, id, tasks[0], TaskStatusPending)
	cid, _ := parseChallengeID(id)
	fid, _ := parseFollowupID(f)
	writeForTest(t, s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE followup SET promoted_challenge_id = ? WHERE id = ?`, cid, fid)
		return err
	})
	if err := s.mutate(context.Background(), ChannelCLI, func(tx *sql.Tx, rec *activityRecorder) error {
		return completeFollowupsPromotedTo(context.Background(), tx, rec, cid)
	}); err != nil {
		t.Fatal(err)
	}
	if got := s4c2Followup(t, s, f); got.Status != FollowupStatusOpen {
		t.Errorf("status = %s, want open", got.Status)
	}
}

// AC-59・AC-68
func TestFollowupTask_CancelledTaskCanBeRepromotedAndClearsTaskColumn_AC59_AC68(t *testing.T) {
	t.Parallel()
	s, id, tasks := additionFixture(t)
	f := s4c2PromotedFollowup(t, s, id, tasks[0], TaskStatusCancelled)
	ctx := context.Background()
	if _, err := s.PrepareFollowupPromote(ctx, f, FollowupPromoteOptions{}, nil); err != nil {
		t.Fatalf("prepare (AC-68): %v", err)
	}
	c, got, err := promoteViaPlan(ctx, s, f, fuAtt(t, f))
	if err != nil {
		t.Fatalf("re-promote (AC-68): %v", err)
	}
	if got.PromotedTaskID != nil || got.PromotedChallengeID == nil || *got.PromotedChallengeID != c.ID {
		t.Errorf("followup = %+v, want promoted_task_id nil and challenge %s (AC-59)", got, c.ID)
	}
	if reread := s4c2Followup(t, s, f); reread.PromotedTaskID != nil {
		t.Errorf("stored promoted_task_id = %v, want nil", *reread.PromotedTaskID)
	}
	acts := fuActs(t, s, "followup", ActivityActionFollowupPromote)
	if len(acts) != 1 || !strings.Contains(string(acts[0].Before), tasks[0].ID) {
		t.Errorf("promote activity should keep the previous task: %+v", acts)
	}
}

// AC-69・AC-70
func TestFollowupTask_DeclinedOrSupersededAdditionCanBeRepromoted_AC69_AC70(t *testing.T) {
	t.Parallel()
	for _, st := range []TaskAdditionState{TaskAdditionDeclined, TaskAdditionSuperseded} {
		t.Run(string(st), func(t *testing.T) {
			t.Parallel()
			s, id, _ := additionFixture(t)
			f := s4c2OpenFollowup(t, s, id)
			s4c2AddAddition(t, s, id, f, st)
			if _, _, err := promoteViaPlan(context.Background(), s, f, fuAtt(t, f)); err != nil {
				t.Errorf("promote after %s: %v", st, err)
			}
		})
	}
}

// AC-71
func TestFollowupTask_PendingAdditionRejectsPromote_AC71(t *testing.T) {
	t.Parallel()
	for _, st := range []TaskAdditionState{TaskAdditionPending, TaskAdditionAwaitingOwner} {
		s, id, _ := additionFixture(t)
		f := s4c2OpenFollowup(t, s, id)
		s4c2AddAddition(t, s, id, f, st)
		if _, _, err := promoteViaPlan(context.Background(), s, f, fuAtt(t, f)); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: err = %v, want ErrValidation", st, err)
		}
		if _, err := s.PrepareFollowupPromote(context.Background(), f, FollowupPromoteOptions{}, nil); !errors.Is(err, ErrValidation) {
			t.Errorf("%s prepare: err = %v, want ErrValidation", st, err)
		}
	}
}

// AC-72・AC-73・AC-74
func TestFollowupTask_LiveTaskRejectsPromote_AC72_AC73_AC74(t *testing.T) {
	t.Parallel()
	for _, st := range []TaskStatus{TaskStatusPending, TaskStatusInProgress, TaskStatusVerifying} {
		s, id, tasks := additionFixture(t)
		f := s4c2PromotedFollowup(t, s, id, tasks[0], st)
		if _, _, err := promoteViaPlan(context.Background(), s, f, fuAtt(t, f)); !errors.Is(err, ErrValidation) {
			t.Errorf("task %s: err = %v, want ErrValidation", st, err)
		}
		if got := s4c2Followup(t, s, f); got.PromotedTaskID == nil {
			t.Errorf("task %s: promoted_task_id was cleared by a rejected promote", st)
		}
	}
}

// AC-75
func TestFollowupTask_DoneTaskFollowupCannotBePromoted_AC75(t *testing.T) {
	t.Parallel()
	s, id, tasks := additionFixture(t)
	f := s4c2PromotedFollowup(t, s, id, tasks[0], TaskStatusVerifying)
	s4c2SetTask(t, s, tasks[0], TaskStatusDone)
	if _, _, err := promoteViaPlan(context.Background(), s, f, fuAtt(t, f)); !errors.Is(err, ErrValidation) {
		t.Errorf("err = %v, want ErrValidation", err)
	}
}

func TestFollowupTask_ListCarriesPromotedTaskTitle(t *testing.T) {
	t.Parallel()
	s, id, tasks := additionFixture(t)
	f := s4c2PromotedFollowup(t, s, id, tasks[0], TaskStatusPending)
	got := s4c2Followup(t, s, f)
	if got.PromotedTaskTitle == nil || *got.PromotedTaskTitle != tasks[0].Title {
		t.Errorf("PromotedTaskTitle = %v, want %q", got.PromotedTaskTitle, tasks[0].Title)
	}
}

// 未のままタスクが済みの行（済みにする処理が走っていない行）も、上げ直せない（タスクの状態の検査が効いている）。
func TestFollowupTask_OpenFollowupWithDoneTaskRejectsPromote_AC75(t *testing.T) {
	t.Parallel()
	s, id, tasks := additionFixture(t)
	f := s4c2PromotedFollowup(t, s, id, tasks[0], TaskStatusDone)
	if _, _, err := promoteViaPlan(context.Background(), s, f, fuAtt(t, f)); !errors.Is(err, ErrValidation) {
		t.Errorf("err = %v, want ErrValidation", err)
	}
}

// 両方の列に値があるときは、タスクの列で読む（上げた先の課題が取り下げでも、タスクが生きていれば整理の対象にも上げ直しにもならない）。
func TestFollowupTask_BothColumnsReadTheTask(t *testing.T) {
	t.Parallel()
	s, id, tasks := additionFixture(t)
	f := s4c2PromotedFollowup(t, s, id, tasks[0], TaskStatusPending)
	cid, _ := parseChallengeID(id)
	writeForTest(t, s, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE challenge SET status = 'withdrawn' WHERE id = ?`, cid); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE followup SET promoted_challenge_id = ? WHERE id = ?`, cid, mustFollowupNum(t, f))
		return err
	})
	if n := s4c2Triage(t, s); n != 0 {
		t.Errorf("triage count = %d, want 0", n)
	}
	if _, _, err := promoteViaPlan(context.Background(), s, f, fuAtt(t, f)); !errors.Is(err, ErrValidation) {
		t.Errorf("err = %v, want ErrValidation", err)
	}
}
