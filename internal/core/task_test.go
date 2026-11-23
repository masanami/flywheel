package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

// このファイルのテスト名の AC-<n> は、core の見直しの区切り S2a の課題（masanami/flywheel#750）の受入基準の番号。

func taskTestChallenge(t *testing.T, s *Store) int64 {
	t.Helper()
	c, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "c"})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := parseChallengeID(c.ID)
	return id
}

// taskTx は fn を、作業ログの記録つきの書き込みトランザクションの中で実行する。
func taskTx(t *testing.T, s *Store, fn func(tx *sql.Tx, rec *activityRecorder) error) error {
	t.Helper()
	return s.mutate(context.Background(), ChannelCLI, fn)
}

func mustCreateTasks(t *testing.T, s *Store, cid, pv int64, in []TaskInput) []Task {
	t.Helper()
	var out []Task
	if err := taskTx(t, s, func(tx *sql.Tx, rec *activityRecorder) error {
		var err error
		out, err = createTasks(context.Background(), tx, rec, cid, pv, in)
		return err
	}); err != nil {
		t.Fatalf("createTasks: %v", err)
	}
	return out
}

func readTasks(t *testing.T, s *Store, cid int64) []Task {
	t.Helper()
	var out []Task
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		out, err = listTasks(context.Background(), tx, cid)
		return err
	}); err != nil {
		t.Fatalf("listTasks: %v", err)
	}
	return out
}

func sampleTask(key string, deps ...string) TaskInput {
	return TaskInput{Key: key, Title: "題 " + key, Body: "何をするか", DoneCriteria: "完了条件", NonGoals: []string{"やらないこと"}, Repo: "o/r", Operation: "pull_request", DependsOn: deps}
}

func taskActivities(t *testing.T, s *Store) []Activity {
	t.Helper()
	all, err := s.ListActivities(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []Activity
	for _, a := range all {
		if a.Entity == "task" {
			out = append(out, a)
		}
	}
	return out
}

func TestCreateTasks_CreatesPendingTasksWithDependenciesAndReadsThemBack(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	cid := taskTestChallenge(t, s)
	created := mustCreateTasks(t, s, cid, 1, []TaskInput{sampleTask("a"), sampleTask("b", "a"), sampleTask("c", "a", "b")})
	if len(created) != 3 {
		t.Fatalf("created = %d tasks, want 3", len(created))
	}
	for i, tk := range created {
		if tk.Status != TaskStatusPending || tk.PlanVersion != 1 || tk.ChallengeID != formatChallengeID(cid) {
			t.Errorf("task %d = %+v, want pending in plan 1 of the challenge", i, tk)
		}
	}
	got := readTasks(t, s, cid)
	if len(got) != 3 {
		t.Fatalf("listTasks = %d tasks, want 3", len(got))
	}
	if !reflect.DeepEqual(got[0].NonGoals, []string{"やらないこと"}) || got[0].Key != "a" || got[0].Title != "題 a" || got[0].Body != "何をするか" ||
		got[0].DoneCriteria != "完了条件" || got[0].Repo != "o/r" || got[0].Operation != "pull_request" {
		t.Errorf("task a = %+v", got[0])
	}
	if got[0].DependsOn != nil {
		t.Errorf("a.DependsOn = %v, want none", got[0].DependsOn)
	}
	if want := []string{got[0].ID}; !reflect.DeepEqual(got[1].DependsOn, want) {
		t.Errorf("b.DependsOn = %v, want %v", got[1].DependsOn, want)
	}
	if want := []string{got[0].ID, got[1].ID}; !reflect.DeepEqual(got[2].DependsOn, want) {
		t.Errorf("c.DependsOn = %v, want %v", got[2].DependsOn, want)
	}
	if got[0].ID != "T-1" {
		t.Errorf("first task ID = %s, want T-1", got[0].ID)
	}
}

func TestCreateTasks_AllowsTasksWithoutKeyAndWithoutNonGoals(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	cid := taskTestChallenge(t, s)
	in := sampleTask("")
	in.NonGoals = nil
	created := mustCreateTasks(t, s, cid, 1, []TaskInput{in, in})
	if len(created) != 2 || created[0].Key != "" || created[1].Key != "" || created[0].NonGoals != nil {
		t.Fatalf("created = %+v, want 2 tasks with no key and no non-goals", created)
	}
	var keys, nonGoals int
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT SUM(key IS NULL), SUM(non_goals IS NULL) FROM task`).Scan(&keys, &nonGoals)
	}); err != nil {
		t.Fatal(err)
	}
	if keys != 2 || nonGoals != 2 {
		t.Errorf("NULL key rows = %d, NULL non_goals rows = %d; want 2, 2", keys, nonGoals)
	}
}

func TestCreateTasks_RejectsDuplicateKeyInTheSamePlanVersionButNotAnotherVersion(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	cid := taskTestChallenge(t, s)
	mustCreateTasks(t, s, cid, 1, []TaskInput{sampleTask("a")})
	err := taskTx(t, s, func(tx *sql.Tx, rec *activityRecorder) error {
		_, err := createTasks(context.Background(), tx, rec, cid, 1, []TaskInput{sampleTask("a")})
		return err
	})
	if !errors.Is(err, errDuplicateTaskKey) || !errors.Is(err, ErrValidation) {
		t.Fatalf("duplicate key error = %v, want errDuplicateTaskKey (ErrValidation)", err)
	}
	// 同じ呼び出しの中での重なりも拒む。
	err = taskTx(t, s, func(tx *sql.Tx, rec *activityRecorder) error {
		_, err := createTasks(context.Background(), tx, rec, cid, 2, []TaskInput{sampleTask("x"), sampleTask("x")})
		return err
	})
	if !errors.Is(err, errDuplicateTaskKey) {
		t.Errorf("duplicate key within one call error = %v, want errDuplicateTaskKey", err)
	}
	mustCreateTasks(t, s, cid, 2, []TaskInput{sampleTask("a")}) // 別の計画の版なら同じ key を作れる
	if n := len(readTasks(t, s, cid)); n != 2 {
		t.Errorf("tasks = %d, want 2 (failed calls leave nothing)", n)
	}
}

func TestCreateTasks_RejectsInvalidInputAndUnknownChallengeWithoutWriting(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	cid := taskTestChallenge(t, s)
	bad := func(mod func(*TaskInput)) TaskInput { in := sampleTask("a"); mod(&in); return in }
	for name, in := range map[string][]TaskInput{
		"empty title":        {bad(func(i *TaskInput) { i.Title = "" })},
		"empty body":         {bad(func(i *TaskInput) { i.Body = " " })},
		"empty criteria":     {bad(func(i *TaskInput) { i.DoneCriteria = "" })},
		"empty repo":         {bad(func(i *TaskInput) { i.Repo = "" })},
		"empty operation":    {bad(func(i *TaskInput) { i.Operation = "" })},
		"unknown dependency": {sampleTask("a", "zzz")},
		"self dependency":    {sampleTask("a", "a")},
	} {
		err := taskTx(t, s, func(tx *sql.Tx, rec *activityRecorder) error {
			_, err := createTasks(context.Background(), tx, rec, cid, 1, in)
			return err
		})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("%s: error = %v, want ErrValidation", name, err)
		}
	}
	err := taskTx(t, s, func(tx *sql.Tx, rec *activityRecorder) error {
		_, err := createTasks(context.Background(), tx, rec, 999, 1, []TaskInput{sampleTask("a")})
		return err
	})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown challenge error = %v, want ErrNotFound", err)
	}
	if n := len(readTasks(t, s, cid)); n != 0 || len(taskActivities(t, s)) != 0 {
		t.Errorf("rejected calls left %d tasks and %d task activities", n, len(taskActivities(t, s)))
	}
}

func TestCreateTasks_RollsBackWithTheCallersTransaction(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	cid := taskTestChallenge(t, s)
	boom := errors.New("boom")
	err := taskTx(t, s, func(tx *sql.Tx, rec *activityRecorder) error {
		if _, err := createTasks(context.Background(), tx, rec, cid, 1, []TaskInput{sampleTask("a")}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want boom", err)
	}
	if n := len(readTasks(t, s, cid)); n != 0 || len(taskActivities(t, s)) != 0 {
		t.Errorf("rollback left %d tasks and %d task activities", n, len(taskActivities(t, s)))
	}
}

func TestListTasks_ReturnsEmptyForChallengeWithoutTasksAndOnlyThatChallengesTasks(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	c1, c2 := taskTestChallenge(t, s), taskTestChallenge(t, s)
	if got := readTasks(t, s, c1); got == nil || len(got) != 0 {
		t.Errorf("tasks of a challenge without tasks = %#v, want empty non-nil", got)
	}
	mustCreateTasks(t, s, c2, 1, []TaskInput{sampleTask("a")})
	if n := len(readTasks(t, s, c1)); n != 0 {
		t.Errorf("c1 tasks = %d, want 0", n)
	}
}

func TestCreateTasks_WritesTaskActivityWithStatusOnlyAndNoVersion(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	cid := taskTestChallenge(t, s)
	created := mustCreateTasks(t, s, cid, 1, []TaskInput{sampleTask("a"), sampleTask("b")})
	acts := taskActivities(t, s)
	if len(acts) != 2 {
		t.Fatalf("task activities = %d, want 2", len(acts))
	}
	for i, a := range acts {
		if a.EntityID != created[i].ID || a.Action != "create" || a.Before != nil {
			t.Errorf("activity %d = entity_id %s action %s before %s; want %s create no-before", i, a.EntityID, a.Action, a.Before, created[i].ID)
		}
		var after map[string]any
		if err := json.Unmarshal(a.After, &after); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(after, map[string]any{"status": "pending"}) {
			t.Errorf("activity %d after = %v, want only status=pending (no version)", i, after)
		}
	}
}

func TestSetTaskStatus_ChangesStatusLogsOnlyRealChangesAndValidates(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	cid := taskTestChallenge(t, s)
	created := mustCreateTasks(t, s, cid, 1, []TaskInput{sampleTask("a")})
	id, _ := parseTaskID(created[0].ID)
	set := func(to TaskStatus) (bool, error) {
		var changed bool
		err := taskTx(t, s, func(tx *sql.Tx, rec *activityRecorder) error {
			var err error
			changed, err = setTaskStatus(context.Background(), tx, rec, id, to)
			return err
		})
		return changed, err
	}
	if changed, err := set(TaskStatusInProgress); err != nil || !changed {
		t.Fatalf("set in_progress = %v, %v; want true, nil", changed, err)
	}
	if got := readTasks(t, s, cid)[0]; got.Status != TaskStatusInProgress || !got.UpdatedAt.After(got.CreatedAt) && !got.UpdatedAt.Equal(got.CreatedAt) {
		t.Errorf("task after set = %+v", got)
	}
	if changed, err := set(TaskStatusInProgress); err != nil || changed {
		t.Errorf("set the same status = %v, %v; want false, nil (no-op)", changed, err)
	}
	if changed, err := set(" done "); err != nil || !changed {
		t.Fatalf("set \" done \" = %v, %v; want true, nil", changed, err)
	}
	if got := readTasks(t, s, cid)[0].Status; got != TaskStatusDone {
		t.Errorf("status after set \" done \" = %q, want the trimmed %q", got, TaskStatusDone)
	}
	if changed, err := set(TaskStatusInProgress); err != nil || !changed {
		t.Fatalf("set back to in_progress = %v, %v", changed, err)
	}
	if _, err := set("bogus"); !errors.Is(err, ErrValidation) {
		t.Errorf("set bogus error = %v, want ErrValidation", err)
	}
	acts := taskActivities(t, s)
	if len(acts) != 4 || acts[1].Action != "task_status" || acts[1].EntityID != created[0].ID {
		t.Fatalf("task activities = %+v, want create then task_status of %s", acts, created[0].ID)
	}
	var after, before map[string]any
	_ = json.Unmarshal(acts[1].After, &after)
	_ = json.Unmarshal(acts[1].Before, &before)
	if !reflect.DeepEqual(after, map[string]any{"status": "in_progress"}) || !reflect.DeepEqual(before, map[string]any{"status": "pending"}) {
		t.Errorf("task_status activity before = %v after = %v", before, after)
	}
	err := taskTx(t, s, func(tx *sql.Tx, rec *activityRecorder) error {
		_, err := setTaskStatus(context.Background(), tx, rec, 999, TaskStatusDone)
		return err
	})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown task error = %v, want ErrNotFound", err)
	}
}

func TestSetTasksStatus_ChangesOnlyTasksMatchingTheFilter(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	cid := taskTestChallenge(t, s)
	mustCreateTasks(t, s, cid, 1, []TaskInput{sampleTask("a"), sampleTask("b")})
	mustCreateTasks(t, s, cid, 2, []TaskInput{sampleTask("a"), sampleTask("b")})
	run := func(f taskFilter, to TaskStatus) int {
		var n int
		if err := taskTx(t, s, func(tx *sql.Tx, rec *activityRecorder) error {
			var err error
			n, err = setTasksStatus(context.Background(), tx, rec, cid, f, to)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	pv1 := int64(1)
	if n := run(taskFilter{PlanVersion: &pv1, Statuses: []TaskStatus{TaskStatusPending}}, TaskStatusCancelled); n != 2 {
		t.Fatalf("cancelled %d tasks, want 2", n)
	}
	statuses := func() []TaskStatus {
		var out []TaskStatus
		for _, tk := range readTasks(t, s, cid) {
			out = append(out, tk.Status)
		}
		return out
	}
	want := []TaskStatus{TaskStatusCancelled, TaskStatusCancelled, TaskStatusPending, TaskStatusPending}
	if got := statuses(); !reflect.DeepEqual(got, want) {
		t.Errorf("statuses = %v, want %v", got, want)
	}
	// 状態の条件に合わないものは変えない。
	if n := run(taskFilter{Statuses: []TaskStatus{TaskStatusDone}}, TaskStatusVerifying); n != 0 {
		t.Errorf("changed %d tasks with no matching status, want 0", n)
	}
	// 条件なしなら課題のすべて（すでに同じ状態のものは数えない）。
	if n := run(taskFilter{}, TaskStatusCancelled); n != 2 {
		t.Errorf("changed %d tasks, want 2 (the 2 not yet cancelled)", n)
	}
	// create 4 件 + task_status 4 件（最初の 2 件 + 残りの 2 件）。
	if acts := taskActivities(t, s); len(acts) != 8 {
		t.Errorf("task activities = %d, want 8", len(acts))
	}
}

func TestParseTaskStatus_AcceptsOnlyTheFiveValues(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"pending", "in_progress", "verifying", "done", "cancelled", " done "} {
		if _, ok := ParseTaskStatus(v); !ok {
			t.Errorf("ParseTaskStatus(%q) rejected", v)
		}
	}
	for _, v := range []string{"", "Done", "complete", "in progress"} {
		if _, ok := ParseTaskStatus(v); ok {
			t.Errorf("ParseTaskStatus(%q) accepted", v)
		}
	}
}

func TestParseTaskID_RoundTripsAndRejectsOtherForms(t *testing.T) {
	t.Parallel()
	if n, ok := parseTaskID(formatTaskID(12)); !ok || n != 12 {
		t.Errorf("parseTaskID(T-12) = %d, %v", n, ok)
	}
	for _, v := range []string{"", "T-0", "T-", "t-1", "C-1", "T-01", "T-1x"} {
		if _, ok := parseTaskID(v); ok {
			t.Errorf("parseTaskID(%q) accepted", v)
		}
	}
}

func recordAnswer(t *testing.T, s *Store, in PlanAnswerInput, at time.Time) (*PlanAnswer, error) {
	t.Helper()
	var out *PlanAnswer
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		out, err = recordPlanAnswer(context.Background(), tx, in, at)
		return err
	})
	return out, classifyReadWriteErr(err)
}

func choiceAnswer(cid, pv, q, choice int64) PlanAnswerInput {
	return PlanAnswerInput{ChallengeID: cid, PlanVersion: pv, QuestionNumber: q, Choice: &choice, Recommended: true,
		Actor: "owner", Channel: ChannelCLI, Verification: VerificationTTYConfirm}
}

func TestRecordPlanAnswer_StoresChoiceAndTextAnswersWithAttestation(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	cid := taskTestChallenge(t, s)
	at := time.Date(2026, 11, 18, 1, 2, 3, 0, time.UTC)
	got, err := recordAnswer(t, s, choiceAnswer(cid, 1, 1, 2), at)
	if err != nil {
		t.Fatal(err)
	}
	if got.Choice == nil || *got.Choice != 2 || got.Text != nil || !got.Recommended || got.Actor != "owner" ||
		got.Channel != ChannelCLI || got.Verification != VerificationTTYConfirm || got.RelaySession != "" || !got.AnsweredAt.Equal(at) {
		t.Errorf("choice answer = %+v", got)
	}
	text := "文の答え"
	got, err = recordAnswer(t, s, PlanAnswerInput{ChallengeID: cid, PlanVersion: 1, QuestionNumber: 2, Text: &text,
		Actor: "owner", Channel: ChannelCLI, Verification: VerificationRelayed, RelaySession: "sess", RelayQuote: "引用"}, at)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text == nil || *got.Text != text || got.Choice != nil || got.Recommended || got.RelaySession != "sess" || got.RelayQuote != "引用" {
		t.Errorf("text answer = %+v", got)
	}
}

func TestRecordPlanAnswer_RejectsInvalidInputAndUnknownChallenge(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	cid := taskTestChallenge(t, s)
	txt := "x"
	both := choiceAnswer(cid, 1, 1, 1)
	both.Text = &txt
	neither := choiceAnswer(cid, 1, 1, 1)
	neither.Choice = nil
	zeroQ := choiceAnswer(cid, 1, 0, 1)
	zeroChoice := choiceAnswer(cid, 1, 1, 0)
	zeroPlan := choiceAnswer(cid, 0, 1, 1)
	noActor := choiceAnswer(cid, 1, 1, 1)
	noActor.Actor = ""
	for name, in := range map[string]PlanAnswerInput{"both": both, "neither": neither, "question 0": zeroQ, "choice 0": zeroChoice, "plan 0": zeroPlan, "no actor": noActor} {
		if _, err := recordAnswer(t, s, in, time.Now()); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: error = %v, want ErrValidation", name, err)
		}
	}
	if _, err := recordAnswer(t, s, choiceAnswer(999, 1, 1, 1), time.Now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown challenge error = %v, want ErrNotFound", err)
	}
	var n int
	_ = s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(`SELECT COUNT(*) FROM plan_answer`).Scan(&n) })
	if n != 0 {
		t.Errorf("plan_answer rows = %d, want 0", n)
	}
}

func TestListPlanAnswers_UsesTheLaterRowForTheSameQuestionAndFiltersByPlanVersion(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	cid := taskTestChallenge(t, s)
	other := taskTestChallenge(t, s)
	at := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	for _, in := range []PlanAnswerInput{
		choiceAnswer(cid, 1, 2, 1), choiceAnswer(cid, 1, 1, 1), choiceAnswer(cid, 1, 2, 3), // 問い 2 に 2 回
		choiceAnswer(cid, 2, 1, 1), choiceAnswer(other, 1, 1, 1),
	} {
		if _, err := recordAnswer(t, s, in, at); err != nil {
			t.Fatal(err)
		}
	}
	var got []PlanAnswer
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		got, err = listPlanAnswers(context.Background(), tx, cid, 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].QuestionNumber != 1 || got[1].QuestionNumber != 2 || *got[1].Choice != 3 {
		t.Fatalf("answers = %+v, want question 1 then question 2 (the later row, choice 3)", got)
	}
	var none []PlanAnswer
	_ = s.db.Read(context.Background(), func(tx *sql.Tx) error {
		none, _ = listPlanAnswers(context.Background(), tx, cid, 9)
		return nil
	})
	if none == nil || len(none) != 0 {
		t.Errorf("answers of an unanswered plan version = %#v, want empty non-nil", none)
	}
}

func TestChallengeUsesIntegration_ReadsNullAsUnsetAndRoundTripsTrueAndFalse(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	cid := taskTestChallenge(t, s)
	read := func() *bool {
		var v *bool
		if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
			var err error
			v, err = challengeUsesIntegration(context.Background(), tx, cid)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return v
	}
	write := func(v *bool) error {
		return s.db.Write(context.Background(), func(tx *sql.Tx) error {
			return setChallengeUsesIntegration(context.Background(), tx, cid, v)
		})
	}
	if read() != nil {
		t.Error("new challenge uses_integration is set, want NULL (unset)")
	}
	yes, no := true, false
	for _, v := range []*bool{&yes, &no, nil} {
		if err := write(v); err != nil {
			t.Fatal(err)
		}
		got := read()
		if (v == nil) != (got == nil) || (v != nil && *v != *got) {
			t.Errorf("after writing %v, read %v", v, got)
		}
	}
	if err := write(&yes); err != nil {
		t.Fatal(err)
	}
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		return setChallengeUsesIntegration(context.Background(), tx, 999, &yes)
	})
	if !errors.Is(classifyReadWriteErr(err), ErrNotFound) {
		t.Errorf("unknown challenge error = %v, want ErrNotFound", err)
	}
}

func TestInsertRun_WritesAndReadsTaskIDAndDefaultsToNull(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	cid := taskTestChallenge(t, s)
	created := mustCreateTasks(t, s, cid, 1, []TaskInput{sampleTask("a")})
	taskID, _ := parseTaskID(created[0].ID)
	hb := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	in := insertRunInput{Kind: runKindDelegate, ChallengeVersion: 1, SessionID: "55555555-5555-4555-8555-555555555555", PID: 1, Host: "h",
		HeartbeatAt: hb, StartedAt: hb, MaxBudgetUSD: 1_000_000, BudgetBucket: budgetBucketImpl, TaskID: &taskID}
	r, err := insertRunForTest(t, s, cid, in)
	if err != nil {
		t.Fatal(err)
	}
	if r.TaskID == nil || *r.TaskID != created[0].ID {
		t.Errorf("inserted run TaskID = %v, want %s", r.TaskID, created[0].ID)
	}
	runID, _ := parseRunID(r.ID)
	if loaded, err := loadRunForTest(t, s, runID); err != nil || loaded.TaskID == nil || *loaded.TaskID != created[0].ID {
		t.Errorf("loaded run = %+v, %v; want TaskID %s", loaded, err, created[0].ID)
	}
	// 課題の単位の実行（task_id なし）は NULL のまま。
	end := updateRunEndInput{EndedAt: hb, Result: runResultSucceeded}
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error { _, err := updateRunEnd(context.Background(), tx, runID, end); return err }); err != nil {
		t.Fatal(err)
	}
	in.TaskID = nil
	r2, err := insertRunForTest(t, s, cid, in)
	if err != nil || r2.TaskID != nil {
		t.Errorf("run without task: %+v, %v; want nil TaskID", r2, err)
	}
	// 0 以下の task_id は受け付けない。
	zero := int64(0)
	in.TaskID = &zero
	if _, err := insertRunForTest(t, s, cid, in); !errors.Is(err, ErrValidation) {
		t.Errorf("task_id 0 error = %v, want ErrValidation", err)
	}
}

func TestRecordContractStatus_WritesTaskIDOnCreateAndReadsItBack(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	hb := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	// 終わっていない contract_run は課題ごとに 1 本なので、課題を分ける。
	record := func(tid *int64, cid int64, ext string) {
		t.Helper()
		run, err := insertRunForTest(t, s, cid, insertRunInput{Kind: runKindContract, ChallengeVersion: 1, SessionID: "55555555-5555-4555-8555-555555555555", PID: 1, Host: "h",
			HeartbeatAt: hb, StartedAt: hb, MaxBudgetUSD: 1_000_000, BudgetBucket: budgetBucketImpl})
		if err != nil {
			t.Fatal(err)
		}
		runInt, _ := parseRunID(run.ID)
		call := contractCall{ChallengeID: formatChallengeID(cid), ChallengeIDInt: cid, RunIDInt: runInt, Connector: "tool", MaxMicros: 1_000_000, TaskIDInt: tid}
		if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
			return s.recordContractStatus(context.Background(), tx, call, &ContractStatus{RunID: ext, State: ContractStateRunning, Summary: "実行中"}, 0, &ContractRunReport{})
		}); err != nil {
			t.Fatal(err)
		}
	}
	c1, c2 := taskTestChallenge(t, s), taskTestChallenge(t, s)
	created := mustCreateTasks(t, s, c1, 1, []TaskInput{sampleTask("a")})
	taskID, _ := parseTaskID(created[0].ID)
	record(&taskID, c1, "ext-1")
	record(nil, c2, "ext-2")
	read := func(ext string) *contractRunRow {
		var r *contractRunRow
		if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
			var err error
			r, err = scanContractRun(tx.QueryRowContext(context.Background(), contractRunSelect+` WHERE external_run_id = ?`, ext))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := read("ext-1"); r.TaskID == nil || *r.TaskID != taskID {
		t.Errorf("contract_run ext-1 TaskID = %v, want %d", r.TaskID, taskID)
	}
	if r := read("ext-2"); r.TaskID != nil {
		t.Errorf("contract_run ext-2 TaskID = %v, want nil", r.TaskID)
	}
}

func TestInsertHoldKindFromTask_WritesTaskIDAndTheExistingInsertLeavesItNull(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	cid := taskTestChallenge(t, s)
	// 開いている保留は課題ごとに 1 つまで。task_id なしの保留は別の課題に立てる。
	cidWithout := taskTestChallenge(t, s)
	created := mustCreateTasks(t, s, cid, 1, []TaskInput{sampleTask("a")})
	taskID, _ := parseTaskID(created[0].ID)
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	var withTask, without int64
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		tc := &transitionCtx{tx: tx, id: cid, current: &Challenge{Status: StatusInProgress}, after: map[string]any{}, nowStr: formatTimestamp(now)}
		var err error
		if withTask, err = insertHoldKindFromTask(context.Background(), tc, "q1", HoldKindHuman, StatusInProgress, &taskID); err != nil {
			return err
		}
		tcWithout := &transitionCtx{tx: tx, id: cidWithout, current: &Challenge{Status: StatusInProgress}, after: map[string]any{}, nowStr: formatTimestamp(now)}
		without, err = insertHoldKindFrom(context.Background(), tcWithout, "q2", HoldKindHuman, StatusInProgress)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var got1, got2 *int64
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		if got1, err = holdTaskID(context.Background(), tx, withTask); err != nil {
			return err
		}
		got2, err = holdTaskID(context.Background(), tx, without)
		if err != nil {
			return err
		}
		if _, err := holdTaskID(context.Background(), tx, 999); !errors.Is(err, ErrNotFound) {
			t.Errorf("holdTaskID(unknown) error = %v, want ErrNotFound", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got1 == nil || *got1 != taskID || got2 != nil {
		t.Errorf("hold task_ids = %v, %v; want %d, nil", got1, got2, taskID)
	}
}
