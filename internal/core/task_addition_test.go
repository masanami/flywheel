package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// 足す依頼（表 task_addition）の core の API。

func additionFixture(t *testing.T) (*Store, string, []Task) {
	t.Helper()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 4, 5))
	cid, _ := parseChallengeID(id)
	return f.s, id, taskRows(t, f.s, cid)
}

func TestTaskAddition_CreateReadAndList(t *testing.T) {
	t.Parallel()
	s, id, tasks := additionFixture(t)
	ctx := context.Background()
	spec := `{"tasks":[]}`
	ta, err := s.CreateTaskAddition(ctx, ChannelCLI, id, TaskAdditionInput{Origin: TaskAdditionOriginExpand, OriginTaskID: tasks[0].ID, Spec: &spec})
	if err != nil {
		t.Fatal(err)
	}
	if ta.ID != "TA-1" || ta.ChallengeID != id || ta.Origin != TaskAdditionOriginExpand || ta.State != TaskAdditionPending ||
		ta.OriginTaskID == nil || *ta.OriginTaskID != tasks[0].ID || ta.Spec == nil || *ta.Spec != spec ||
		ta.LimitUSD != nil || ta.HoldID != nil || ta.DecidedAt != nil || len(ta.TaskIDs) != 0 {
		t.Errorf("created = %+v", ta)
	}
	got, err := s.GetTaskAddition(ctx, "TA-1")
	if err != nil || got.ID != "TA-1" {
		t.Errorf("GetTaskAddition = %+v, err %v", got, err)
	}
	list, err := s.ListTaskAdditions(ctx, id)
	if err != nil || len(list) != 1 || list[0].ID != "TA-1" {
		t.Errorf("ListTaskAdditions = %+v, err %v", list, err)
	}
	if _, err := s.GetTaskAddition(ctx, "TA-9"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: err = %v, want ErrNotFound", err)
	}
	if _, err := s.ListTaskAdditions(ctx, "C-9999"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing challenge: err = %v, want ErrNotFound", err)
	}
	acts, _ := s.ListActivities(ctx, &id)
	var created int
	for _, a := range acts {
		if a.Entity == "task_addition" && a.Action == "create" && a.EntityID == "TA-1" {
			created++
		}
	}
	if created != 1 {
		t.Errorf("activities = %+v, want one task_addition create entry in the challenge's log", acts)
	}
}

// AC-222 の core の口: 同じ出どころのタスクに expand の足す依頼は 1 つ。2 つ目は ErrValidation。
func TestTaskAddition_SecondExpandOfTheSameOriginIsRejected(t *testing.T) {
	t.Parallel()
	s, id, tasks := additionFixture(t)
	ctx := context.Background()
	in := TaskAdditionInput{Origin: TaskAdditionOriginExpand, OriginTaskID: tasks[0].ID}
	if _, err := s.CreateTaskAddition(ctx, ChannelCLI, id, in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTaskAddition(ctx, ChannelCLI, id, in); !errors.Is(err, ErrValidation) {
		t.Errorf("second: err = %v, want ErrValidation", err)
	}
	in.OriginTaskID = tasks[1].ID
	if _, err := s.CreateTaskAddition(ctx, ChannelCLI, id, in); err != nil {
		t.Errorf("another origin task: err = %v, want success", err)
	}
}

func TestTaskAddition_CreateValidation(t *testing.T) {
	t.Parallel()
	s, id, tasks := additionFixture(t)
	ctx := context.Background()
	other, err := s.CreateChallenge(ctx, ChannelCLI, CreateInput{Title: "other", Description: "d", DoneCriteria: "c"})
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		challenge string
		in        TaskAdditionInput
		want      error
	}{
		"unknown origin":      {id, TaskAdditionInput{Origin: "followup", OriginTaskID: tasks[0].ID}, ErrValidation},
		"malformed task id":   {id, TaskAdditionInput{Origin: TaskAdditionOriginExpand, OriginTaskID: "x"}, ErrValidation},
		"missing task":        {id, TaskAdditionInput{Origin: TaskAdditionOriginExpand, OriginTaskID: "T-999"}, ErrValidation},
		"task of another one": {other.ID, TaskAdditionInput{Origin: TaskAdditionOriginExpand, OriginTaskID: tasks[0].ID}, ErrValidation},
		"missing challenge":   {"C-9999", TaskAdditionInput{Origin: TaskAdditionOriginExpand}, ErrNotFound},
		"no origin task":      {id, TaskAdditionInput{Origin: TaskAdditionOriginExpand}, ErrValidation},
	} {
		if _, err := s.CreateTaskAddition(ctx, ChannelCLI, tc.challenge, tc.in); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
}

func TestTaskAddition_StateTransitions(t *testing.T) {
	t.Parallel()
	s, id, tasks := additionFixture(t)
	ctx := context.Background()
	ta, err := s.CreateTaskAddition(ctx, ChannelCLI, id, TaskAdditionInput{Origin: TaskAdditionOriginExpand, OriginTaskID: tasks[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	cid, _ := parseChallengeID(id)
	writeForTest(t, s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at) VALUES (?, 'q', 'in_progress', '2026-11-01T00:00:00.000Z')`, cid)
		return err
	})
	total, limit, hold := 30.0, 20.0, int64(1)
	got, err := s.SetTaskAdditionState(ctx, ChannelCLI, ta.ID, TaskAdditionAwaitingOwner, TaskAdditionUpdate{TotalAfterUSD: &total, LimitUSD: &limit, HoldID: &hold})
	if err != nil {
		t.Fatal(err)
	}
	if got.State != TaskAdditionAwaitingOwner || got.TotalAfterUSD != 30 || got.LimitUSD == nil || *got.LimitUSD != 20 || got.HoldID == nil || *got.HoldID != 1 || got.DecidedAt != nil {
		t.Errorf("awaiting_owner = %+v, want the judged values recorded and no decided_at", got)
	}
	// 開いている間は、値の更新だけの書き込みもできる（仕様待ちへ戻すことも）。
	spec := `{"tasks":[1]}`
	if got, err = s.SetTaskAdditionState(ctx, ChannelCLI, ta.ID, TaskAdditionPending, TaskAdditionUpdate{Spec: &spec}); err != nil || got.Spec == nil || *got.Spec != spec {
		t.Errorf("back to pending: %+v, err %v", got, err)
	}
	got, err = s.SetTaskAdditionState(ctx, ChannelCLI, ta.ID, TaskAdditionApplied, TaskAdditionUpdate{})
	if err != nil || got.State != TaskAdditionApplied || got.DecidedAt == nil {
		t.Fatalf("applied = %+v, err %v; want decided_at recorded", got, err)
	}
	// 閉じた足す依頼は動かせない。
	if _, err := s.SetTaskAdditionState(ctx, ChannelCLI, ta.ID, TaskAdditionPending, TaskAdditionUpdate{}); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("reopen: err = %v, want ErrInvalidTransition", err)
	}
	if _, err := s.SetTaskAdditionState(ctx, ChannelCLI, ta.ID, "bogus", TaskAdditionUpdate{}); !errors.Is(err, ErrValidation) {
		t.Errorf("bogus state: err = %v, want ErrValidation", err)
	}
	if _, err := s.SetTaskAdditionState(ctx, ChannelCLI, "TA-99", TaskAdditionNone, TaskAdditionUpdate{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: err = %v, want ErrNotFound", err)
	}
}

// 足したタスクの印（task.addition_id）から、足す依頼の task_ids を引く。
func TestTaskAddition_TaskIDsComeFromTheTaskMark(t *testing.T) {
	t.Parallel()
	s, id, tasks := additionFixture(t)
	ctx := context.Background()
	ta, err := s.CreateTaskAddition(ctx, ChannelCLI, id, TaskAdditionInput{Origin: TaskAdditionOriginExpand, OriginTaskID: tasks[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	writeForTest(t, s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task SET addition_id = 1 WHERE id = ?`, mustTaskID(t, tasks[1].ID))
		return err
	})
	got, err := s.GetTaskAddition(ctx, ta.ID)
	if err != nil || len(got.TaskIDs) != 1 || got.TaskIDs[0] != tasks[1].ID {
		t.Errorf("TaskIDs = %+v, err %v; want [%s]", got, err, tasks[1].ID)
	}
}

func TestTaskAdditionClosedSets(t *testing.T) {
	t.Parallel()
	if got := len(TaskAdditionStateValues()); got != 6 {
		t.Errorf("states = %d, want 6", got)
	}
	for _, s := range TaskAdditionStateValues() {
		if p, ok := ParseTaskAdditionState(string(s)); !ok || p != s {
			t.Errorf("ParseTaskAdditionState(%q) = %q, %v", s, p, ok)
		}
	}
	for s, open := range map[TaskAdditionState]bool{TaskAdditionPending: true, TaskAdditionAwaitingOwner: true, TaskAdditionApplied: false, TaskAdditionNone: false, TaskAdditionDeclined: false, TaskAdditionSuperseded: false} {
		if s.IsOpen() != open {
			t.Errorf("%s.IsOpen() = %v, want %v", s, s.IsOpen(), open)
		}
	}
	if got := TaskAdditionOriginValues(); len(got) != 1 || got[0] != TaskAdditionOriginExpand {
		t.Errorf("origins = %v, want [expand] only in S4c-1", got)
	}
}
