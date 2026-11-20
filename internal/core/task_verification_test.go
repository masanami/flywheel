package core

// このファイルは、完了の検証（J5）・完了の差し戻し・反映のやり直し・取り下げ・完了の承認を、タスクの単位に合わせる変更
// （区切り S2a。docs/features/core-redesign.md の受入基準 AC-126〜AC-143）を、実ストアで検証する。
// 受入基準 1 件ごとに、基準の文が述べる条件・結果・値を確かめるテストを置く。
// AC-129（指示文 j5.md の記述）は internal/invoker の TestJ5Prompt_TaskSection... が確かめる。

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// --- 足場 ---

// verifyingTaskFixture は、タスク t1 が検証中で、課題 C-1 が検証中の j5Fixture。
func verifyingTaskFixture(t *testing.T) *j5Fixture {
	t.Helper()
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	if got := taskStatusOf(t, f.s, "C-1", "t1"); got != TaskStatusVerifying {
		t.Fatalf("task t1 after the delegation = %s, want verifying", got)
	}
	return f
}

// setTaskStatusRaw はタスク key の状態を直に書く（足場。作業ログなし）。
func setTaskStatusRaw(t *testing.T, s *Store, cid int64, key string, to TaskStatus) {
	t.Helper()
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task SET status = ? WHERE challenge_id = ? AND key = ?`, string(to), cid, key)
		return err
	}); err != nil {
		t.Fatalf("setTaskStatusRaw: %v", err)
	}
}

// taskStatusMap は課題 cid のタスクの key → 状態。
func taskStatusMap(t *testing.T, s *Store, cid int64) map[string]TaskStatus {
	t.Helper()
	out := map[string]TaskStatus{}
	for _, tk := range taskRows(t, s, cid) {
		out[tk.Key] = tk.Status
	}
	return out
}

func assertTaskStatuses(t *testing.T, s *Store, cid int64, want map[string]TaskStatus) {
	t.Helper()
	got := taskStatusMap(t, s, cid)
	for k, w := range want {
		if got[k] != w {
			t.Errorf("task %s status = %q, want %q (all: %v)", k, got[k], w, got)
		}
	}
}

// addTaskWithStatus は課題 cid の計画の版 version にタスク key を足し、状態を status にする。
func addTaskWithStatus(t *testing.T, s *Store, cid, version int64, key string, status TaskStatus) {
	t.Helper()
	addTaskRowForTest(t, s, cid, version, key, "main-repo", "impl")
	setTaskStatusRaw(t, s, cid, key, status)
}

// approvePlanVersionForTest は課題 cid の計画の版 version の計画と承認を足す（計画の作り直しの後の承認の足場）。
func approvePlanVersionForTest(t *testing.T, s *Store, cid, version int64) {
	t.Helper()
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO task_plan (challenge_id, version, body, created_at, spec) VALUES (?, ?, 'PLAN-V', '2026-09-26T00:00:00.000Z', NULL)`, cid, version); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO approval (challenge_id, kind, decision, target_version, plan_version, actor, channel, verification, decided_at)
			VALUES (?, 'plan', 'approved', (SELECT version FROM challenge WHERE id = ?), ?, 'alice', 'cli', 'tty_confirm', '2026-09-26T00:00:00.000Z')`, cid, cid, version)
		return err
	}); err != nil {
		t.Fatalf("approvePlanVersionForTest: %v", err)
	}
}

func rejectCompletionNow(t *testing.T, s *Store, id, reason string) {
	t.Helper()
	prev, err := s.PrepareRejection(context.Background(), id, reason)
	if err != nil {
		t.Fatalf("PrepareRejection: %v", err)
	}
	if _, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{ChallengeID: id, ExpectedVersion: prev.Version,
		Decision: ApprovalDecisionRejected, Reason: &reason}, ttyAtt(t, id)); err != nil {
		t.Fatalf("ExecuteApproval(reject): %v", err)
	}
}

func approveCompletionNow(t *testing.T, s *Store, id string, holdRelease bool) {
	t.Helper()
	prev, err := s.PrepareApproval(context.Background(), id)
	if err != nil {
		t.Fatalf("PrepareApproval: %v", err)
	}
	if _, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{ChallengeID: id, ExpectedVersion: prev.Version,
		Decision: ApprovalDecisionApproved, HoldRelease: holdRelease}, ttyAtt(t, id)); err != nil {
		t.Fatalf("ExecuteApproval(approve): %v", err)
	}
}

func j5TaskSection(f *j5Fixture) (string, bool) {
	if len(f.j5Inputs) == 0 {
		return "", false
	}
	for _, s := range f.j5Inputs[len(f.j5Inputs)-1].Sections {
		if s.Label == "タスク" {
			return s.Content, true
		}
	}
	return "", false
}

// --- 完了の検証の判断点（J5）: run・入力 ---

func TestJ5Task_AC126_RunRecordsTheSingleVerifyingTask(t *testing.T) {
	t.Parallel()
	f := verifyingTaskFixture(t)
	tasks := taskRows(t, f.s, 1)
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(tasks))
	}
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	runs := f.j5Runs(t)
	if len(runs) != 1 {
		t.Fatalf("J5 runs = %d, want 1", len(runs))
	}
	if runs[0].TaskID == nil || *runs[0].TaskID != tasks[0].ID {
		t.Errorf("J5 run task_id = %v, want %s", runs[0].TaskID, tasks[0].ID)
	}
}

func TestJ5Task_AC126_NoTaskIDWhenNotExactlyOneVerifyingTask(t *testing.T) {
	t.Parallel()
	for name, mut := range map[string]func(t *testing.T, f *j5Fixture){
		"zero verifying tasks": func(t *testing.T, f *j5Fixture) { setTaskStatusRaw(t, f.s, 1, "t1", TaskStatusInProgress) },
		"two verifying tasks":  func(t *testing.T, f *j5Fixture) { addTaskWithStatus(t, f.s, 1, 1, "t2", TaskStatusVerifying) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := verifyingTaskFixture(t)
			mut(t, f)
			if _, err := f.verify(t, nil); err != nil {
				t.Fatal(err)
			}
			runs := f.j5Runs(t)
			if len(runs) != 1 || runs[0].TaskID != nil {
				t.Fatalf("J5 runs = %+v, want one run with task_id NULL", runs)
			}
			if _, ok := j5TaskSection(f); ok {
				t.Error("J5 input has a タスク section, want none")
			}
			// タスクの状態は動かさない（対象のタスクが決まらない）。
			want := map[string]TaskStatus{"t1": TaskStatusVerifying}
			if name == "zero verifying tasks" {
				want["t1"] = TaskStatusInProgress
			}
			assertTaskStatuses(t, f.s, 1, want)
		})
	}
}

func TestJ5Task_AC127_InputHasTheTaskSection(t *testing.T) {
	t.Parallel()
	f := verifyingTaskFixture(t)
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	content, ok := j5TaskSection(f)
	if !ok {
		t.Fatalf("J5 input has no タスク section: %s", f.stdin(t))
	}
	// 題名・何をするか・完了条件・やらないこと（j2TestTask の t1）。
	for _, want := range []string{"タスク t1", "TASK-BODY-t1", "完了条件 t1", "やらないこと"} {
		if !strings.Contains(content, want) {
			t.Errorf("タスク section lacks %q:\n%s", want, content)
		}
	}
}

func TestJ5Task_AC128_TaskSectionCarriesTheTasksDoneCriteria(t *testing.T) {
	t.Parallel()
	f := verifyingTaskFixture(t)
	// タスクの完了条件を課題の完了条件と区別できる値にする。
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task SET done_criteria = 'TASK-DONE-CRITERIA-MARKER' WHERE challenge_id = 1 AND key = 't1'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	content, ok := j5TaskSection(f)
	if !ok || !strings.Contains(content, "TASK-DONE-CRITERIA-MARKER") {
		t.Errorf("タスク section = %q (ok=%v), want it to carry the task's done criteria", content, ok)
	}
	// 課題の達成条件の区画は、これまでどおり別に入る。
	if !strings.Contains(f.stdin(t), "## 達成条件\nDC-t") {
		t.Errorf("達成条件 section is missing or changed:\n%s", f.stdin(t))
	}
}

// --- 完了の検証の結果とタスクの状態 ---

func TestJ5Task_AC130_MetMovesTheTaskToDoneWhenTheChallengeAwaitsCompletionApproval(t *testing.T) {
	t.Parallel()
	f := verifyingTaskFixture(t)
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingCompletionApproval {
		t.Fatalf("challenge status = %s, want awaiting_completion_approval", got)
	}
	if got := taskStatusOf(t, f.s, "C-1", "t1"); got != TaskStatusDone {
		t.Errorf("task status = %s, want done", got)
	}
	// 状態の変化の作業ログは、J5 の run を原因として同じトランザクションで残る。
	acts := taskStatusActivities(t, f.s, TaskStatusDone)
	runs := f.j5Runs(t)
	if len(acts) != 1 || acts[0].EntityID != 1 || !acts[0].RunID.Valid || formatRunID(acts[0].RunID.Int64) != runs[0].ID {
		t.Errorf("done activities = %+v, want one for T-1 caused by %s", acts, runs[0].ID)
	}
}

func TestJ5Task_AC131_NotMetMovesTheTaskToInProgress(t *testing.T) {
	t.Parallel()
	f := verifyingTaskFixture(t)
	f.j5.result = j5Out("not_met", "FEEDBACK", nil)
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.detail(t, "C-1").Status; got != StatusInProgress {
		t.Fatalf("challenge status = %s, want in_progress", got)
	}
	if got := taskStatusOf(t, f.s, "C-1", "t1"); got != TaskStatusInProgress {
		t.Errorf("task status = %s, want in_progress", got)
	}
	acts := taskStatusActivities(t, f.s, TaskStatusInProgress) // 委譲の起動と、この差し戻し
	runs := f.j5Runs(t)
	if len(acts) < 2 || !acts[len(acts)-1].RunID.Valid || formatRunID(acts[len(acts)-1].RunID.Int64) != runs[0].ID {
		t.Errorf("in_progress activities = %+v, want the not_met change logged with the J5 run %s as the cause", acts, runs[0].ID)
	}
}

func TestJ5Task_AC132_UncertainHoldRecordsTheTaskID(t *testing.T) {
	t.Parallel()
	f := verifyingTaskFixture(t)
	f.j5.result = j5Out("uncertain", nil, "QUESTION")
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingHuman {
		t.Fatalf("challenge status = %s, want awaiting_human", got)
	}
	if got := f.queryString(t, `SELECT COALESCE(task_id, '') FROM hold WHERE challenge_id = 1`); got != "1" {
		t.Errorf("hold.task_id = %q, want 1 (T-1)", got)
	}
}

func TestJ5Task_AC133_UncertainKeepsTheTaskVerifying(t *testing.T) {
	t.Parallel()
	f := verifyingTaskFixture(t)
	f.j5.result = j5Out("uncertain", nil, "QUESTION")
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := taskStatusOf(t, f.s, "C-1", "t1"); got != TaskStatusVerifying {
		t.Errorf("task status = %s, want verifying", got)
	}
}

func TestJ5Task_AC134_MetHeldForActualMismatchKeepsTheTaskVerifying(t *testing.T) {
	t.Parallel()
	f := verifyingTaskFixture(t)
	f.setExpected(t, "C-1", 2) // 予想 2 個に対し、実物 1 個 → 完了確認へ進めず保留へ
	f.j5.result = j5OutWithActual("met", []any{map[string]any{"scene": "s", "after": "a"}})
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingHuman {
		t.Fatalf("challenge status = %s, want awaiting_human (held)", got)
	}
	if got := taskStatusOf(t, f.s, "C-1", "t1"); got != TaskStatusVerifying {
		t.Errorf("task status = %s, want verifying", got)
	}
	if got := f.queryString(t, `SELECT COALESCE(task_id, '') FROM hold WHERE challenge_id = 1`); got != "1" {
		t.Errorf("hold.task_id = %q, want 1", got)
	}
}

// --- 完了の差し戻し・反映のやり直し ---

func TestJ5Task_AC135_CompletionRejectionReopensTheDoneTaskOfTheApprovedPlan(t *testing.T) {
	t.Parallel()
	f := verifyingTaskFixture(t)
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := taskStatusOf(t, f.s, "C-1", "t1"); got != TaskStatusDone {
		t.Fatalf("task status before the rejection = %s, want done", got)
	}
	rejectCompletionNow(t, f.s, "C-1", "直して")
	if got := f.detail(t, "C-1").Status; got != StatusInProgress {
		t.Fatalf("challenge status = %s, want in_progress", got)
	}
	if got := taskStatusOf(t, f.s, "C-1", "t1"); got != TaskStatusInProgress {
		t.Errorf("task status = %s, want in_progress", got)
	}
	// 差し戻しの後は、そのタスクの委譲を続けられる。
	if res := f.runDelegation(t, "C-1"); len(res.Items) != 1 {
		t.Errorf("delegation after the rejection = %+v, want the task launched", res)
	}
}

func TestJ5Task_AC136_CompletionRejectionLeavesDoneTasksOfAnEarlierPlanVersion(t *testing.T) {
	t.Parallel()
	f := verifyingTaskFixture(t)
	// 版 1 のタスク t1 は済み。計画を作り直して承認し直し（版 2）、版 2 のタスク t2 も済みにする。
	setTaskStatusRaw(t, f.s, 1, "t1", TaskStatusDone)
	approvePlanVersionForTest(t, f.s, 1, 2)
	addTaskWithStatus(t, f.s, 1, 2, "t2", TaskStatusDone)
	f.setStatus(t, "C-1", string(StatusAwaitingCompletionApproval))

	rejectCompletionNow(t, f.s, "C-1", "直して")
	assertTaskStatuses(t, f.s, 1, map[string]TaskStatus{"t1": TaskStatusDone, "t2": TaskStatusInProgress})
	if got := f.detail(t, "C-1").Status; got != StatusInProgress {
		t.Errorf("challenge status = %s, want in_progress", got)
	}
}

func TestJ5Task_AC137_ReopenForReleaseReopensTheDoneTaskOfTheApprovedPlan(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	f.setVerifiedHead(t, "v")
	f.fake.pr.HeadSHA = "hc"
	// 承認済みの計画は版 2。版 2 に済み・取りやめのタスクがあり、前の版（版 1）の済みのタスクは残る。
	approvePlanVersionForTest(t, f.s, f.cid, 1)
	approvePlanVersionForTest(t, f.s, f.cid, 2)
	addTaskWithStatus(t, f.s, f.cid, 1, "old", TaskStatusDone)
	addTaskWithStatus(t, f.s, f.cid, 2, "done", TaskStatusDone)
	addTaskWithStatus(t, f.s, f.cid, 2, "cancelled", TaskStatusCancelled)

	res, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, ReopenForReleaseInput{
		OperationID: formatOperationID(f.opID), Reason: ReopenReasonContentChanged,
		Sources: ReleaseHeadSources{Checks: f.fake, Compare: compareContentChanged()},
	})
	if err != nil || !res.Reopened {
		t.Fatalf("res = %+v, err = %v, want reopened", res, err)
	}
	assertTaskStatuses(t, f.s, f.cid, map[string]TaskStatus{"done": TaskStatusVerifying, "old": TaskStatusDone, "cancelled": TaskStatusCancelled})
	if got := f.state(t).chStatus; got != string(StatusVerifying) {
		t.Errorf("challenge status = %s, want verifying", got)
	}
}

// --- 取り下げ ---

func withdrawFixtureWithAllStatuses(t *testing.T) *delegateFixture {
	t.Helper()
	f := newDelegateFixture(t)
	f.newInProgress(t, "t", "P1", planSpec(nil)) // t1: pending
	addTaskWithStatus(t, f.s, 1, 1, "ip", TaskStatusInProgress)
	addTaskWithStatus(t, f.s, 1, 1, "vf", TaskStatusVerifying)
	addTaskWithStatus(t, f.s, 1, 1, "dn", TaskStatusDone)
	addTaskWithStatus(t, f.s, 1, 1, "cc", TaskStatusCancelled)
	if _, err := withdrawForTest(t, f.s, "C-1", "要らない"); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if got := f.detail(t, "C-1").Status; got != StatusWithdrawn {
		t.Fatalf("challenge status = %s, want withdrawn", got)
	}
	return f
}

func TestJ5Task_AC138_WithdrawCancelsPendingInProgressAndVerifyingTasks(t *testing.T) {
	t.Parallel()
	f := withdrawFixtureWithAllStatuses(t)
	assertTaskStatuses(t, f.s, 1, map[string]TaskStatus{
		"t1": TaskStatusCancelled, "ip": TaskStatusCancelled, "vf": TaskStatusCancelled,
	})
}

func TestJ5Task_AC139_WithdrawLeavesDoneTasks(t *testing.T) {
	t.Parallel()
	f := withdrawFixtureWithAllStatuses(t)
	assertTaskStatuses(t, f.s, 1, map[string]TaskStatus{"dn": TaskStatusDone, "cc": TaskStatusCancelled})
}

// --- オーケストレーターの直接の完了 ---

// directCompletionFixture は、タスク t1（in_progress）と済みのタスク dn を持つ着手中の C-1 を、親が直接やる保留へ置いたもの。
func directCompletionFixture(t *testing.T) *delegateFixture {
	t.Helper()
	f := newDelegateFixture(t)
	f.newInProgress(t, "t", "P1", planSpec(nil))
	setTaskStatusRaw(t, f.s, 1, "t1", TaskStatusInProgress)
	addTaskWithStatus(t, f.s, 1, 1, "dn", TaskStatusDone)
	if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, "C-1", HoldInput{Question: "親が直接行う", Kind: string(HoldKindParentDirect)}); err != nil {
		t.Fatalf("HoldChallenge: %v", err)
	}
	return f
}

func completeDirectNow(t *testing.T, f *delegateFixture) {
	t.Helper()
	if _, _, err := f.s.CompleteDirect(context.Background(), ChannelCLI, "C-1", completeDirectInput("o/r#12")); err != nil {
		t.Fatalf("CompleteDirect: %v", err)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingCompletionApproval {
		t.Fatalf("status after complete-direct = %s, want awaiting_completion_approval", got)
	}
}

func TestJ5Task_AC140_CompleteDirectDoesNotChangeTaskStatuses(t *testing.T) {
	t.Parallel()
	f := directCompletionFixture(t)
	before := taskStatusMap(t, f.s, 1)
	completeDirectNow(t, f)
	after := taskStatusMap(t, f.s, 1)
	for k, w := range before {
		if after[k] != w {
			t.Errorf("task %s: %s → %s, want unchanged", k, w, after[k])
		}
	}
	if after["t1"] != TaskStatusInProgress || after["dn"] != TaskStatusDone {
		t.Errorf("tasks after = %v", after)
	}
}

func TestJ5Task_AC141_RejectingADirectCompletionDoesNotChangeTaskStatuses(t *testing.T) {
	t.Parallel()
	f := directCompletionFixture(t)
	completeDirectNow(t, f)
	before := taskStatusMap(t, f.s, 1)
	rejectCompletionNow(t, f.s, "C-1", "やり直して")
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingHuman {
		t.Fatalf("challenge status = %s, want awaiting_human", got)
	}
	after := taskStatusMap(t, f.s, 1)
	for k, w := range before {
		if after[k] != w {
			t.Errorf("task %s: %s → %s, want unchanged", k, w, after[k])
		}
	}
	if after["dn"] != TaskStatusDone {
		t.Errorf("done task = %s, want done (not reopened)", after["dn"])
	}
}

func TestJ5Task_AC142_ApprovingADirectCompletionCancelsUnfinishedTasks(t *testing.T) {
	t.Parallel()
	for name, holdRelease := range map[string]bool{"approve": false, "approve and hold release": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := directCompletionFixture(t)
			addTaskWithStatus(t, f.s, 1, 1, "vf", TaskStatusVerifying)
			addTaskWithStatus(t, f.s, 1, 1, "cc", TaskStatusCancelled)
			completeDirectNow(t, f)
			approveCompletionNow(t, f.s, "C-1", holdRelease)
			if got := f.detail(t, "C-1").Status; got != StatusDone {
				t.Fatalf("challenge status = %s, want done", got)
			}
			// pending（t1 は in_progress にしてあるので pending は別に足す）も含め、終わっていないものは取りやめ。
			assertTaskStatuses(t, f.s, 1, map[string]TaskStatus{
				"t1": TaskStatusCancelled, "vf": TaskStatusCancelled, "dn": TaskStatusDone, "cc": TaskStatusCancelled,
			})
		})
	}
}

func TestJ5Task_AC142_ApprovingADirectCompletionCancelsPendingTasks(t *testing.T) {
	t.Parallel()
	f := directCompletionFixture(t)
	addTaskWithStatus(t, f.s, 1, 1, "pd", TaskStatusPending)
	completeDirectNow(t, f)
	approveCompletionNow(t, f.s, "C-1", false)
	assertTaskStatuses(t, f.s, 1, map[string]TaskStatus{"pd": TaskStatusCancelled, "dn": TaskStatusDone})
}

func TestJ5Task_AC143_AfterADirectCompletionIsRejectedAndAnsweredTheRemainingTaskIsDelegated(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	f.newInProgress(t, "t", "P1", planSpec(nil))
	setTaskStatusRaw(t, f.s, 1, "t1", TaskStatusInProgress)
	if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, "C-1", HoldInput{Question: "親が直接行う", Kind: string(HoldKindParentDirect)}); err != nil {
		t.Fatal(err)
	}
	completeDirectNow(t, f)
	rejectCompletionNow(t, f.s, "C-1", "やり直して")

	// 保留への回答で着手中へ戻る。
	prev, err := f.s.PrepareAnswer(context.Background(), "C-1", "続けて")
	if err != nil {
		t.Fatalf("PrepareAnswer: %v", err)
	}
	if _, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: "C-1", ExpectedVersion: prev.Version, Answer: "続けて"}, ttyAtt(t, "C-1")); err != nil {
		t.Fatalf("ExecuteAnswer: %v", err)
	}
	if got := f.detail(t, "C-1").Status; got != StatusInProgress {
		t.Fatalf("challenge status = %s, want in_progress", got)
	}
	tasks := taskRows(t, f.s, 1)
	if len(tasks) != 1 || tasks[0].Status != TaskStatusInProgress {
		t.Fatalf("tasks = %+v, want one in_progress task", tasks)
	}

	res := f.runDelegation(t, "C-1")
	if len(res.Items) != 1 || len(res.NotStarted) != 0 {
		t.Fatalf("delegation = items %+v, not_started %+v, want the task launched", res.Items, res.NotStarted)
	}
	if ids := delegateRunTaskIDs(t, f); len(ids) != 1 || ids[0] != tasks[0].ID {
		t.Errorf("delegate run task_ids = %v, want [%s]", ids, tasks[0].ID)
	}
}
