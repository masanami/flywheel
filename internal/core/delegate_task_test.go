package core

// このファイルは、委譲の段の対象を「着手中の課題の起動できるタスク」にする変更（区切り S2a。
// docs/features/core-redesign.md の受入基準 AC-104〜AC-125）を、実ストア・偽の claude・偽の契約のコマンドで検証する。
// 受入基準 1 件ごとに、基準の文が述べる条件・結果・値を確かめるテストを置く。

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// delegateRunTaskIDs は kind = delegate の run の task_id（表示形。NULL は ""）を ID の昇順で返す。
func delegateRunTaskIDs(t *testing.T, f *delegateFixture) []string {
	t.Helper()
	var out []string
	for _, r := range f.delegateRuns(t) {
		if r.TaskID == nil {
			out = append(out, "")
		} else {
			out = append(out, *r.TaskID)
		}
	}
	return out
}

// taskStatusOf は課題 id のタスクのうち key のものの状態を返す。
func taskStatusOf(t *testing.T, s *Store, id, key string) TaskStatus {
	t.Helper()
	cid, _ := parseChallengeID(id)
	for _, tk := range taskRows(t, s, cid) {
		if tk.Key == key {
			return tk.Status
		}
	}
	t.Fatalf("task %q not found in %s", key, id)
	return ""
}

// taskStatusActivities は、課題 id のタスクの作業ログのうち action = task_status で after の状態が status のものを返す
// （entity_id・run_id つき）。
type taskStatusActivity struct {
	Entity   string
	EntityID int64
	RunID    sql.NullInt64
}

func taskStatusActivities(t *testing.T, s *Store, status TaskStatus) []taskStatusActivity {
	t.Helper()
	var out []taskStatusActivity
	err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT entity, entity_id, run_id FROM activity
			WHERE entity = 'task' AND action = 'task_status' AND json_extract(after, '$.status') = ? ORDER BY id`, string(status))
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var a taskStatusActivity
			if err := rows.Scan(&a.Entity, &a.EntityID, &a.RunID); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("read task activities: %v", err)
	}
	return out
}

func countRows(t *testing.T, s *Store, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(q, args...).Scan(&n) }); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// contractRunInput は契約の形の委譲の段（RunDelegation）の入力。
func (f *contractFixture) runInput(t *testing.T, id *string) DelegateInput {
	t.Helper()
	in := f.input(f.cycle(t, 300), id)
	in.Contract = f.client
	return in
}

func (f *contractFixture) runDelegationStage(t *testing.T, id *string) *DelegateResult {
	t.Helper()
	res, err := f.s.RunDelegation(context.Background(), f.runInput(t, id))
	if err != nil {
		t.Fatalf("RunDelegation: %v", err)
	}
	return res
}

func firstTaskID(t *testing.T, s *Store, id string) string {
	t.Helper()
	cid, _ := parseChallengeID(id)
	tasks := taskRows(t, s, cid)
	if len(tasks) == 0 {
		t.Fatalf("%s has no tasks", id)
	}
	return tasks[0].ID
}

// AC-104: タスクが 1 個の着手中の課題に委譲の段を実行すると（form: plugin）、委譲の run の task_id がそのタスクの ID である。
func TestDelegateTask_AC104_PluginRunHasTheTaskID(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	taskID := firstTaskID(t, f.s, id)
	f.mustRun(t, nil)
	runs := f.delegateRuns(t)
	if len(runs) != 1 || runs[0].TaskID == nil || *runs[0].TaskID != taskID {
		t.Fatalf("delegate runs = %+v, want one run with task_id %s", runs, taskID)
	}
	// ID を指定した委譲の段でも同じ。
	f2 := newDelegateFixture(t)
	id2 := f2.newInProgress(t, "t", "P1", planSpec(nil))
	taskID2 := firstTaskID(t, f2.s, id2)
	f2.mustRun(t, &id2)
	if got := delegateRunTaskIDs(t, f2); len(got) != 1 || got[0] != taskID2 {
		t.Fatalf("task_ids = %v, want [%s]", got, taskID2)
	}
}

// AC-105: 契約の形（form: cli）の委譲の段で記録される contract_run の task_id が、そのタスクの ID である。
func TestDelegateTask_AC105_ContractRunHasTheTaskID(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	taskID := firstTaskID(t, f.s, f.cid)
	f.runDelegationStage(t, nil)
	var got sql.NullString
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT 'T-' || task_id FROM contract_run`).Scan(&got)
	}); err != nil {
		t.Fatalf("read contract_run: %v", err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM contract_run`); n != 1 {
		t.Fatalf("contract_run rows = %d, want 1", n)
	}
	if !got.Valid || got.String != taskID {
		t.Fatalf("contract_run.task_id = %v, want %s", got, taskID)
	}
}

// AC-106: 委譲を起動すると（form: plugin）、pending だったタスクの status が in_progress になる。
func TestDelegateTask_AC106_PluginLaunchMakesAPendingTaskInProgress(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	f.deleg.result = JudgmentLaunchOutput{Result: RunResultErrored, ErrorSummary: "errored"} // 検証中へ進まない結果
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	if got := taskStatusOf(t, f.s, id, "t1"); got != TaskStatusPending {
		t.Fatalf("status before = %s, want pending", got)
	}
	f.mustRun(t, nil)
	if got := taskStatusOf(t, f.s, id, "t1"); got != TaskStatusInProgress {
		t.Fatalf("status after the launch = %s, want in_progress", got)
	}
}

// AC-107: 委譲を起動すると（form: cli）、pending だったタスクの status が in_progress になる。
func TestDelegateTask_AC107_ContractLaunchMakesAPendingTaskInProgress(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	if got := taskStatusOf(t, f.s, f.cid, "t1"); got != TaskStatusPending {
		t.Fatalf("status before = %s, want pending", got)
	}
	f.runDelegationStage(t, nil)
	if got := taskStatusOf(t, f.s, f.cid, "t1"); got != TaskStatusInProgress {
		t.Fatalf("status after the launch = %s, want in_progress", got)
	}
}

// AC-108: 委譲の起動でタスクの状態が変わると、作業ログに entity=task・action=task_status・after の状態が in_progress の記録が
// 1 件増える（形態 plugin・cli の両方。原因の run が記録される）。すでに in_progress のタスクの再開では増えない。
func TestDelegateTask_AC108_TheStatusChangeIsLoggedOnce(t *testing.T) {
	t.Parallel()
	t.Run("plugin", func(t *testing.T) {
		f := newResumeFixture(t)
		f.deleg.queue = []JudgmentLaunchOutput{failOut(RunResultErrored), failOut(RunResultErrored)}
		id := f.newInProgress(t, "t", "P1", planSpec(nil))
		taskID := firstTaskID(t, f.s, id)
		before := len(taskStatusActivities(t, f.s, TaskStatusInProgress))
		f.mustRun(t, nil)
		got := taskStatusActivities(t, f.s, TaskStatusInProgress)
		if len(got) != before+1 {
			t.Fatalf("in_progress task_status activities %d → %d, want +1", before, len(got))
		}
		last := got[len(got)-1]
		runID, _ := parseRunID(f.delegateRuns(t)[0].ID)
		if last.Entity != "task" || formatTaskID(last.EntityID) != taskID || !last.RunID.Valid || last.RunID.Int64 != runID {
			t.Fatalf("activity = %+v, want task %s caused by run %d", last, taskID, runID)
		}
		// すでに in_progress のタスクの再開・やり直しでは、タスクの状態も作業ログも変えない。
		f.mustRun(t, &id)
		if n := len(f.deleg.launched()); n != 2 {
			t.Fatalf("launches = %d, want 2 (the retry must launch)", n)
		}
		if n := len(taskStatusActivities(t, f.s, TaskStatusInProgress)); n != before+1 {
			t.Fatalf("in_progress activities after the retry = %d, want %d (unchanged)", n, before+1)
		}
	})
	t.Run("cli", func(t *testing.T) {
		f := newContractFixture(t)
		taskID := firstTaskID(t, f.s, f.cid)
		before := len(taskStatusActivities(t, f.s, TaskStatusInProgress))
		f.runDelegationStage(t, nil)
		got := taskStatusActivities(t, f.s, TaskStatusInProgress)
		if len(got) != before+1 || formatTaskID(got[len(got)-1].EntityID) != taskID {
			t.Fatalf("in_progress task_status activities = %+v, want exactly one more, for %s", got, taskID)
		}
	})
}

// AC-108（同じトランザクション）: タスクの状態の作業ログを書けなければ、委譲の run も記録されず、タスクは pending のまま。
func TestDelegateTask_AC108_TaskStatusAndRunAreWrittenInTheSameTransaction(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.s.insertActivity = func(tx *sql.Tx, row activityRow) error {
		if row.Entity == "task" {
			return errors.New("injected activity failure")
		}
		return defaultInsertActivity(tx, row)
	}
	if _, err := f.run(t, nil); err == nil && len(f.delegateRuns(t)) != 0 {
		t.Fatal("a delegate run was recorded although the task status log failed")
	}
	f.s.insertActivity = nil
	if got := f.delegateRuns(t); len(got) != 0 {
		t.Fatalf("delegate runs = %+v, want none (rolled back with the task status log)", got)
	}
	if got := taskStatusOf(t, f.s, id, "t1"); got != TaskStatusPending {
		t.Fatalf("task status = %s, want pending (rolled back)", got)
	}
}

// AC-109: 起動した委譲の run の repo は、タスクの repo である（計画の構造化した出力の repo ではなく、タスクの行のもの）。
func TestDelegateTask_AC109_RunRepoIsTheTaskRepo(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil)) // 構造化した出力のタスクは main-repo
	cid, _ := parseChallengeID(id)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task SET repo = 'sibling' WHERE challenge_id = ?`, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.mustRun(t, nil)
	runs := f.delegateRuns(t)
	if len(runs) != 1 || runs[0].Repo != "sibling" {
		t.Fatalf("delegate runs = %+v, want one run with repo sibling (the task's repo)", runs)
	}
	if got := f.deleg.launched(); len(got) != 1 || !strings.HasSuffix(got[0].WorkDir, "slot-s") {
		t.Fatalf("launch work dir = %+v, want the sibling repo's slot", got)
	}
}

// AC-110: 前の版の計画の done のタスクが残っていて、新しい版のタスクが pending で 1 個ある着手中の課題（統合先を使わない）に
// 委譲の段を実行すると、新しい版のタスクの委譲が起動する。
func TestDelegateTask_AC110_AdoneTaskOfAnEarlierPlanVersionDoesNotBlockTheNewVersionsTask(t *testing.T) {
	t.Parallel()
	for _, explicit := range []bool{false, true} {
		f := newDelegateFixture(t)
		id := f.newInProgress(t, "t", "P1", planSpec(nil))
		cid, _ := parseChallengeID(id)
		oldTask := firstTaskID(t, f.s, id)
		setAllTasksStatus(t, f.s, cid, TaskStatusPending, TaskStatusDone)
		if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
			if _, err := tx.Exec(`INSERT INTO task_plan (challenge_id, version, body, created_at, spec) SELECT challenge_id, 2, body, created_at, spec FROM task_plan WHERE challenge_id = ? AND version = 1`, cid); err != nil {
				return err
			}
			_, err := tx.Exec(`INSERT INTO approval (challenge_id, kind, decision, target_version, plan_version, actor, channel, verification, decided_at)
				VALUES (?, 'plan', 'approved', 2, 2, 'alice', 'cli', 'tty_confirm', '2026-09-26T00:00:00.000Z')`, cid)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		seedPlanTasks(t, f.s, cid, 2)
		var target *string
		if explicit {
			target = &id
		}
		res, err := f.run(t, target)
		if err != nil || len(res.NotStarted) != 0 {
			t.Fatalf("explicit=%v: res = %+v err = %v, want a launch", explicit, res, err)
		}
		ids := delegateRunTaskIDs(t, f)
		if len(ids) != 1 || ids[0] == oldTask || ids[0] == "" {
			t.Fatalf("explicit=%v: delegate run task_ids = %v, want the new version's task (not %s)", explicit, ids, oldTask)
		}
		if got := taskRows(t, f.s, cid); got[0].Status != TaskStatusDone || got[0].ID != oldTask {
			t.Fatalf("the earlier version's task = %+v, want it left done", got[0])
		}
	}
}

// AC-111: 委譲の指示文の判断点（J3）の run の task_id が、対象のタスクの ID である。
func TestDelegateTask_AC111_J3RunHasTheTaskID(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	taskID := firstTaskID(t, f.s, id)
	f.mustRun(t, nil)
	var got sql.NullString
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT 'T-' || task_id FROM run WHERE judgment = 'J3'`).Scan(&got)
	}); err != nil {
		t.Fatalf("read the J3 run: %v", err)
	}
	if !got.Valid || got.String != taskID {
		t.Fatalf("J3 run task_id = %v, want %s", got, taskID)
	}
	if n := countRows(t, f.s, `SELECT COUNT(*) FROM run WHERE judgment = 'J3'`); n != 1 {
		t.Fatalf("J3 runs = %d, want 1", n)
	}
}

// AC-112・AC-113: J3 の入力に区画「タスク」があり、その区画にタスクの完了条件（と題名・何をするか・やらないこと）が含まれる。
func TestDelegateTask_AC112_AC113_J3InputHasTheTaskSectionWithTheDoneCriteria(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	f.newInProgress(t, "t", "P1", planSpec(func(m map[string]any) {
		task := j2TestTask("t1", "main-repo", "impl")
		task["non_goals"] = []string{"NON-GOAL-X"}
		m["tasks"] = []any{task}
	}))
	f.mustRun(t, nil)
	if len(f.j3Inputs) != 1 {
		t.Fatalf("J3 calls = %d, want 1", len(f.j3Inputs))
	}
	sec, ok := sectionByLabel(f.j3Inputs[0].Sections, "タスク")
	if !ok {
		t.Fatalf("J3 input has no 「タスク」 section:\n%s", sectionsText(f.j3Inputs[0].Sections))
	}
	for _, want := range []string{"タスク t1", "TASK-BODY-t1", "完了条件 t1", "NON-GOAL-X"} {
		if !strings.Contains(sec.Content, want) {
			t.Errorf("「タスク」 section lacks %q:\n%s", want, sec.Content)
		}
	}
}

// AC-114: 委譲の結果で課題が検証中へ進むと、タスクの status が verifying になる（形態 plugin・cli の両方）。
func TestDelegateTask_AC114_TaskBecomesVerifyingWhenTheChallengeDoes(t *testing.T) {
	t.Parallel()
	t.Run("plugin", func(t *testing.T) {
		f := newDelegateFixture(t)
		f.branches.branches["feat/x"] = true
		f.branches.prs["feat/x"] = []UpstreamPullRequest{mkPR(1, "open", "develop")}
		id := f.newInProgress(t, "t", "P1", planSpec(nil))
		f.mustRun(t, nil)
		if got := f.detail(t, id).Status; got != StatusVerifying {
			t.Fatalf("challenge status = %s, want verifying", got)
		}
		if got := taskStatusOf(t, f.s, id, "t1"); got != TaskStatusVerifying {
			t.Fatalf("task status = %s, want verifying", got)
		}
		if n := len(taskStatusActivities(t, f.s, TaskStatusVerifying)); n != 1 {
			t.Fatalf("verifying task_status activities = %d, want 1", n)
		}
	})
	t.Run("cli", func(t *testing.T) {
		f := newContractFixture(t)
		f.runDelegationStage(t, nil)
		cost := 1.0
		f.client.queue = []ContractLaunchOutput{contractOut(ContractStateSucceeded, &cost, func(st *ContractStatus) {
			st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: "https://github.com/o/r/pull/9"}}
		})}
		f.poll(t)
		if got := f.status(t, f.cid); got != StatusVerifying {
			t.Fatalf("challenge status = %s, want verifying", got)
		}
		if got := taskStatusOf(t, f.s, f.cid, "t1"); got != TaskStatusVerifying {
			t.Fatalf("task status = %s, want verifying", got)
		}
	})
}

// 検証中へ進まない結果（保留・失敗）では、タスクの状態を in_progress のまま変えない。
func TestDelegateTask_TaskStaysInProgressWhenTheChallengeDoesNotReachVerifying(t *testing.T) {
	t.Parallel()
	f := newResumeFixture(t)
	f.deleg.result = questionsOut("feat/x")
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.mustRun(t, nil)
	if got := f.detail(t, id).Status; got != StatusAwaitingHuman {
		t.Fatalf("challenge status = %s, want awaiting_human", got)
	}
	if got := taskStatusOf(t, f.s, id, "t1"); got != TaskStatusInProgress {
		t.Fatalf("task status = %s, want in_progress (a hold does not change the task)", got)
	}
}

// AC-115: 委譲の子の問いで作られた保留の task_id が、そのタスクの ID である。
func TestDelegateTask_AC115_HoldFromTheChildsQuestionsHasTheTaskID(t *testing.T) {
	t.Parallel()
	f := newResumeFixture(t)
	f.deleg.result = questionsOut("feat/x")
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	taskID := firstTaskID(t, f.s, id)
	f.mustRun(t, nil)
	cid, _ := parseChallengeID(id)
	var got sql.NullString
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT 'T-' || task_id FROM hold WHERE challenge_id = ?`, cid).Scan(&got)
	}); err != nil {
		t.Fatalf("read the hold: %v", err)
	}
	if n := countRows(t, f.s, `SELECT COUNT(*) FROM hold WHERE challenge_id = ?`, cid); n != 1 {
		t.Fatalf("holds = %d, want 1", n)
	}
	if !got.Valid || got.String != taskID {
		t.Fatalf("hold.task_id = %v, want %s", got, taskID)
	}
}

// 契約の形でオーナーの対応待ち（契約の run が waiting・decider が human）に写した保留も、契約の run が結び付くタスクに結び付く。
func TestDelegateTask_ContractWaitHoldHasTheTaskID(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	taskID := firstTaskID(t, f.s, f.cid)
	f.runDelegationStage(t, nil)
	f.client.queue = []ContractLaunchOutput{waitingOut(&ContractRequestedAction{Kind: "answer", Decider: "human", Text: "どうする"}, nil)}
	if reps := f.poll(t); len(reps) != 1 || !reps[0].Held {
		t.Fatalf("report = %+v, want held", reps)
	}
	var got sql.NullString
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT 'T-' || task_id FROM hold`).Scan(&got)
	}); err != nil {
		t.Fatalf("read the hold: %v", err)
	}
	if !got.Valid || got.String != taskID {
		t.Fatalf("hold.task_id = %v, want %s", got, taskID)
	}
	if got := taskStatusOf(t, f.s, f.cid, "t1"); got != TaskStatusInProgress {
		t.Fatalf("task status = %s, want in_progress (a hold does not change the task)", got)
	}
}

// launchedByStage は、課題が 2 個のタスクを持つ（統合先を使う）か統合先を使う課題で、委譲の run も契約の run も J3 も
// 増えないことを確かめる。
func assertNothingLaunched(t *testing.T, f *delegateFixture, wantJ3 int) {
	t.Helper()
	if n := len(f.delegateRuns(t)); n != 0 {
		t.Errorf("delegate runs = %d, want 0", n)
	}
	if n := countRows(t, f.s, `SELECT COUNT(*) FROM run`); n != 0 {
		t.Errorf("runs = %d, want 0 (not even a J3 run)", n)
	}
	if n := countRows(t, f.s, `SELECT COUNT(*) FROM contract_run`); n != 0 {
		t.Errorf("contract runs = %d, want 0", n)
	}
	if len(f.j3Inputs) != wantJ3 || len(f.deleg.launched()) != 0 {
		t.Errorf("J3 %d, launched %d; want %d and 0", len(f.j3Inputs), len(f.deleg.launched()), wantJ3)
	}
}

// AC-116: タスクが 2 個の着手中の課題（統合先を使う）に委譲の段を実行しても、委譲の run が増えない。
func TestDelegateTask_AC116_TwoTasksLaunchNothing(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P1", twoTaskPlanSpec())
	cid, _ := parseChallengeID(id)
	if n := len(taskRows(t, f.s, cid)); n != 2 {
		t.Fatalf("tasks = %d, want 2", n)
	}
	f.mustRun(t, nil)
	assertNothingLaunched(t, f, 0)
	f.mustRun(t, &id)
	assertNothingLaunched(t, f, 0)
	for _, tk := range taskRows(t, f.s, cid) {
		if tk.Status != TaskStatusPending {
			t.Errorf("task %s = %s, want it left pending", tk.ID, tk.Status)
		}
	}
}

// AC-117: タスクが 2 個の着手中の課題（統合先を使う）に委譲の段を実行すると、起動しなかった理由が integration_unavailable で返る。
func TestDelegateTask_AC117_TwoTasksReasonIsIntegrationUnavailable(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P1", twoTaskPlanSpec())
	res := mustRunResult(t, f, nil)
	assertOnlyNotStarted(t, res, id, NotStartedIntegrationUnavailable)
}

// AC-118: タスクが 1 個で、統合先を使う（uses_integration が 1）着手中の課題に委譲の段を実行すると、integration_unavailable で返る。
func TestDelegateTask_AC118_OneTaskOfAnIntegrationChallengeIsNotLaunched(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "one", "P1", planSpec(nil))
	cid, _ := parseChallengeID(id)
	if n := len(taskRows(t, f.s, cid)); n != 1 {
		t.Fatalf("tasks = %d, want 1", n)
	}
	yes := true
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error { return setChallengeUsesIntegration(context.Background(), tx, cid, &yes) }); err != nil {
		t.Fatal(err)
	}
	res := mustRunResult(t, f, nil)
	assertOnlyNotStarted(t, res, id, NotStartedIntegrationUnavailable)
	assertNothingLaunched(t, f, 0)
	res = mustRunResult(t, f, &id)
	assertOnlyNotStarted(t, res, id, NotStartedIntegrationUnavailable)
}

// AC-119: タスクが 2 個の着手中の課題（統合先を使う）に `flywheel run <課題の ID>` を実行すると、エラーではなく、
// 起動しなかった理由 integration_unavailable が返る。
func TestDelegateTask_AC119_ExplicitIDReturnsTheReasonNotAnError(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P1", twoTaskPlanSpec())
	res, err := f.run(t, &id)
	if err != nil {
		t.Fatalf("RunDelegation(%s) err = %v, want no error", id, err)
	}
	assertOnlyNotStarted(t, res, id, NotStartedIntegrationUnavailable)
}

// AC-120: 記録した課題の前提が終わっておらず、タスクが 2 個ある着手中の課題に委譲の段を実行すると、起動しなかった理由は
// 見直し前と同じ前提の理由（dependency）で返る。
func TestDelegateTask_AC120_DependencyComesBeforeTheTaskCount(t *testing.T) {
	t.Parallel()
	for _, explicit := range []bool{false, true} {
		f := newDelegateFixture(t)
		id := f.newInProgress(t, "two", "P1", twoTaskPlanSpec())
		dep := f.newDepTarget(t)
		f.dependOn(t, id, dep)
		var target *string
		if explicit {
			target = &id
		}
		res := mustRunResult(t, f, target)
		assertDependencyNotStarted(t, res, id, formatChallengeID(dep))
	}
}

// 過去の形の計画（tasks のキーが無い構造化した出力）で task の行が無い課題。
const oldShapePlanSpec = `{"verdict":"plan","summary":"s","steps":["a"],"repo":"main-repo","operation":"impl","size":"S","done_criteria":"d","cross_repo":false,"related_repos":[]}`

// AC-121: 承認済みの計画が、タスクを持たない過去の形の構造化した出力で、task の行が無い着手中の課題に委譲の段を実行すると、
// 起動しなかった理由が no_tasks で返る（計画の検査は掛けない）。
func TestDelegateTask_AC121_NoTasksForAnOldShapePlan(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "old", "P1", oldShapePlanSpec)
	cid, _ := parseChallengeID(id)
	if n := len(taskRows(t, f.s, cid)); n != 0 {
		t.Fatalf("tasks = %d, want none", n)
	}
	res := mustRunResult(t, f, nil)
	assertOnlyNotStarted(t, res, id, NotStartedNoTasks)
	assertNothingLaunched(t, f, 0)
}

// AC-122: 同じ課題に `flywheel run <課題の ID>` を実行すると、エラーではなく起動しなかった理由 no_tasks が返る。
func TestDelegateTask_AC122_ExplicitIDNoTasks(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "old", "P1", oldShapePlanSpec)
	res, err := f.run(t, &id)
	if err != nil {
		t.Fatalf("RunDelegation(%s) err = %v, want no error", id, err)
	}
	assertOnlyNotStarted(t, res, id, NotStartedNoTasks)
	assertNothingLaunched(t, f, 0)
}

// AC-123: 承認済みの計画が構造化した出力を持たない（task_plan.spec が NULL）着手中の課題に委譲の段を実行すると、起動しなかった理由は
// 見直し前と同じ plan_unavailable で返る（ID を指定した `flywheel run <課題の ID>` は validation_failed に当たる ErrValidation）。
func TestDelegateTask_AC123_PlanWithoutSpecIsStillPlanUnavailable(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "manual", "P1", "")
	res := mustRunResult(t, f, nil)
	assertOnlyNotStarted(t, res, id, NotStartedPlanUnavailable)
	if _, err := f.run(t, &id); !errors.Is(err, ErrValidation) {
		t.Fatalf("explicit run err = %v, want ErrValidation (validation_failed)", err)
	}
	// タスクの行があっても（人が登録した計画の判定がタスクの数の判定より先）同じ。
	cid, _ := parseChallengeID(id)
	addTaskRowForTest(t, f.s, cid, 1, "t1", "main-repo", "impl")
	res = mustRunResult(t, f, nil)
	assertOnlyNotStarted(t, res, id, NotStartedPlanUnavailable)
	if _, err := f.run(t, &id); !errors.Is(err, ErrValidation) {
		t.Fatalf("explicit run err = %v, want ErrValidation (validation_failed)", err)
	}
	assertNothingLaunched(t, f, 0)
}

// AC-124・AC-125: core が定義する起動しなかった理由の値の一覧に、integration_unavailable と no_tasks がある。
func TestDelegateTask_AC124_AC125_NotStartedReasonValuesHaveTheNewReasons(t *testing.T) {
	t.Parallel()
	have := map[NotStartedReason]bool{}
	for _, r := range NotStartedReasonValues() {
		have[r] = true
	}
	if !have["integration_unavailable"] {
		t.Error("NotStartedReasonValues() lacks integration_unavailable (AC-124)")
	}
	if !have["no_tasks"] {
		t.Error("NotStartedReasonValues() lacks no_tasks (AC-125)")
	}
}

// 起動できるタスクは、構造化した出力ではなく task の行で数える: done・cancelled・verifying のタスクだけの課題は no_tasks、
// in_progress のタスク 1 個なら起動する（再開・やり直し）。
func TestDelegateTask_LaunchableTasksAreCountedFromRowsByStatus(t *testing.T) {
	t.Parallel()
	for _, st := range []TaskStatus{TaskStatusDone, TaskStatusCancelled, TaskStatusVerifying} {
		f := newDelegateFixture(t)
		id := f.newInProgress(t, "t", "P1", planSpec(nil))
		cid, _ := parseChallengeID(id)
		setAllTasksStatus(t, f.s, cid, TaskStatusPending, st)
		res := mustRunResult(t, f, nil)
		assertOnlyNotStarted(t, res, id, NotStartedNoTasks)
	}
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	cid, _ := parseChallengeID(id)
	setAllTasksStatus(t, f.s, cid, TaskStatusPending, TaskStatusInProgress)
	res := mustRunResult(t, f, nil)
	if len(res.NotStarted) != 0 || len(res.Items) != 1 {
		t.Fatalf("res = %+v, want a launch for an in_progress task", res)
	}
}

func mustRunResult(t *testing.T, f *delegateFixture, id *string) *DelegateResult {
	t.Helper()
	res, err := f.run(t, id)
	if err != nil {
		t.Fatalf("RunDelegation: %v", err)
	}
	return res
}

func assertOnlyNotStarted(t *testing.T, res *DelegateResult, id string, reason NotStartedReason) {
	t.Helper()
	if len(res.Items) != 0 || len(res.NotStarted) != 1 || res.NotStarted[0].ChallengeID != id || res.NotStarted[0].Reason != reason {
		t.Fatalf("items = %+v not_started = %+v, want only %s %s", res.Items, res.NotStarted, id, reason)
	}
}
