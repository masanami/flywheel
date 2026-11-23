package core

// このファイルは、区切り S4c-3 の「課題の単位の段の止め方・課題の最後の保留の読み・サイクルと枠の数え」を実ストアで検証する。
// タスクを止める保留は、保留の行を直接作って（課題は人間対応待ち）用意する。受入基準の番号は、親 Issue の「## 受入基準」の並びの順。

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// rawTaskHold は、課題の task（内部整数 ID）を止める保留の行を直接作り、課題を人間対応待ちにする（戻り先は着手中）。
func rawTaskHold(t *testing.T, s *Store, cid, task int64, raisedAt string) {
	t.Helper()
	writeForTest(t, s, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at, task_id, scope) VALUES (?, 'q', 'in_progress', ?, ?, 'task')`,
			cid, raisedAt, task); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE challenge SET status = 'awaiting_human' WHERE id = ?`, cid)
		return err
	})
}

// closeRawHolds は、課題の開いている保留を回答なしで閉じ、課題を着手中へ戻す。
func closeRawHolds(t *testing.T, s *Store, cid int64) {
	t.Helper()
	writeForTest(t, s, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE hold SET closed_at = '2026-11-23T00:00:00.000Z' WHERE challenge_id = ? AND answer IS NULL AND closed_at IS NULL`, cid); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE challenge SET status = 'in_progress' WHERE id = ?`, cid)
		return err
	})
}

// AC-33: タスク a を止める保留が開いている間、その課題の、仕様待ちの足す依頼について、判断点は呼ばれない（保留が閉じて着手中へ戻れば呼ばれる）。
func TestHoldScopeStages_AC33_TaskHoldKeepsTheExpandJudgmentFromRunning(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	f.setTaskStatus(t, "a", TaskStatusDone)
	ids := map[string]string{}
	for _, tk := range taskRows(t, f.s, f.cid()) {
		ids[tk.Key] = tk.ID
	}
	if _, err := f.s.CreateTaskAddition(context.Background(), ChannelInvoker, f.id, TaskAdditionInput{Origin: TaskAdditionOriginExpand, OriginTaskID: ids["a"]}); err != nil {
		t.Fatal(err)
	}
	rawTaskHold(t, f.s, f.cid(), mustTaskID(t, ids["b"]), "2026-11-23T00:00:00.000Z")
	inv := &expandRecorder{out: []byte(expandAskOutput)}
	f.expand(t, inv)
	if inv.count() != 0 {
		t.Fatalf("J2 calls while a task hold is open = %d, want 0", inv.count())
	}
	readForTest(t, f.s, func(tx *sql.Tx) error {
		if n, err := countExpandWork(context.Background(), tx, f.agent); err != nil || n != 0 {
			t.Errorf("expand work = %d, %v, want 0 while a task hold is open", n, err)
		}
		return nil
	})
	closeRawHolds(t, f.s, f.cid())
	f.expand(t, inv)
	if inv.count() != 1 {
		t.Errorf("J2 calls after the hold closed = %d, want 1", inv.count())
	}
}

// AC-34: タスク a を止める保留が開いている間、ほかのタスクが全部統合先に入っても、上げる依頼は始まらない。
func TestHoldScopeStages_AC34_TaskHoldKeepsTheUpRequestFromStarting(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, upConnectorsJSON(), multiTask{key: "a"}, multiTask{key: "b"})
	cid, _ := parseChallengeID(id)
	taskA := mustTaskID(t, f.taskID(t, id, "a"))
	rawTaskHold(t, f.s, cid, taskA, "2026-11-23T00:00:00.000Z")
	if ids, err := f.s.reflectCandidates(context.Background(), &cid, nil); err != nil || len(ids) != 0 {
		t.Fatalf("reflect candidates = %v, %v, want none while a task hold is open", ids, err)
	}
	f.runDelegationStage(t, nil)
	if got := f.reflectStarts(); len(got) != 0 {
		t.Fatalf("reflect starts = %v, want none", got)
	}
	closeRawHolds(t, f.s, cid)
	if ids, err := f.s.reflectCandidates(context.Background(), &cid, nil); err != nil || len(ids) != 1 {
		t.Errorf("reflect candidates after the hold closed = %v, %v, want 1", ids, err)
	}
}

// AC-35: タスクを止める保留が開いている間、課題の単位の確かめは始まらない。
func TestHoldScopeStages_AC35_TaskHoldKeepsTheChallengeCheckFromStarting(t *testing.T) {
	t.Parallel()
	f := newChkFixture(t)
	cid, _ := parseChallengeID(f.id)
	rawTaskHold(t, f.s, cid, mustTaskID(t, f.taskID(t, f.id, "a")), "2026-11-23T00:00:00.000Z")
	res, err := f.j5.verify(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 0 || len(f.j5.j5Inputs) != 0 {
		t.Fatalf("items = %+v, J5 calls = %d, want the check not started", res.Items, len(f.j5.j5Inputs))
	}
	f.sqlExec(t, `UPDATE hold SET closed_at = '2026-11-23T00:00:01.000Z' WHERE challenge_id = ?`, cid)
	f.sqlExec(t, `UPDATE challenge SET status = 'verifying' WHERE id = ?`, cid)
	f.verify(t)
	if len(f.j5.j5Inputs) != 1 {
		t.Errorf("J5 calls after the hold closed = %d, want 1", len(f.j5.j5Inputs))
	}
}

// AC-37: タスク a を止める保留が開いている間、a 以外のタスクが全部済みになっても、課題の状態は人間対応待ちのまま。
func TestHoldScopeStages_AC37_ChallengeStaysAwaitingHumanWhenTheOtherTasksAreDone(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	f.waitHold(t, f.runA)
	setTaskStatusRaw(t, f.s, f.cid, "b", TaskStatusDone)
	f.runDelegationStage(t, nil)
	if got := f.challengeStatus(t); got != string(StatusAwaitingHuman) {
		t.Errorf("status = %s, want awaiting_human", got)
	}
	if got := f.q(t, `SELECT status FROM task WHERE id = ?`, f.taskA); got == string(TaskStatusDone) {
		t.Errorf("task a is %s, want it not done", got)
	}
}

// AC-38: タスクを止める保留だけが開いている課題の、枠が尽きたタスク b は、「枠が尽きた」に数えられる（保留のタスク a は数えない）。
func TestHoldScopeStages_AC38_BudgetExhaustedCountsTheProceedingTaskOfAHeldChallenge(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 3, 2))
	cid, _ := parseChallengeID(id)
	tasks := taskRows(t, f.s, cid)
	a, b := mustTaskID(t, tasks[0].ID), mustTaskID(t, tasks[1].ID)
	costRun(t, f.s, cid, a, "delegate", "", "impl", 3, 0)
	costRun(t, f.s, cid, b, "delegate", "", "impl", 3, 0)
	if got := f.overview(t).NeedsHumanBudgetExhausted; len(got) != 2 {
		t.Fatalf("budget_exhausted before the hold = %+v, want both tasks", got)
	}
	rawTaskHold(t, f.s, cid, a, "2026-11-23T00:00:00.000Z")
	got := f.overview(t).NeedsHumanBudgetExhausted
	if len(got) != 1 || got[0].ChallengeID != id || got[0].TaskID != tasks[1].ID {
		t.Fatalf("budget_exhausted = %+v, want only task %s (the task a is stopped by its hold)", got, tasks[1].ID)
	}
	// 課題を止める保留が開いていれば、課題の全部が止まる（どのタスクも数えない）。
	closeRawHolds(t, f.s, cid)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at) VALUES (?, 'q', 'in_progress', '2026-11-23T00:00:02.000Z')`, cid); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE challenge SET status = 'awaiting_human' WHERE id = ?`, cid)
		return err
	})
	if got := f.overview(t).NeedsHumanBudgetExhausted; len(got) != 0 {
		t.Errorf("budget_exhausted under a challenge hold = %+v, want none", got)
	}
}

// AC-96: タスク b を止める保留が開いている間、判断点の連続失敗の数えの境は、b の開いている保留では決まらない（その前の、閉じた保留で決まる）。
func TestHoldScopeStages_AC96_FailureCountBoundaryIgnoresTheOpenTaskHold(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 3, 2))
	cid, _ := parseChallengeID(id)
	tasks := taskRows(t, f.s, cid)
	version := int64(0)
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT version FROM challenge WHERE id = ?`, cid).Scan(&version)
	})
	failedRun := func(startedAt string) {
		writeForTest(t, f.s, func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO run (challenge_id, kind, judgment, challenge_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket)
				VALUES (?, 'judgment', 'J5', ?, 's', 1, 'h', ?, ?, ?, 'errored', 1000000, 'judgment')`, cid, version, startedAt, startedAt, startedAt)
			return err
		})
	}
	failedRun("2026-11-01T00:00:01.000Z")
	failedRun("2026-11-01T00:00:02.000Z")
	writeForTest(t, f.s, func(tx *sql.Tx) error { // 閉じた（回答済みの）保留。ここが境
		_, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at, answer, answered_at, answered_by, closed_at)
			VALUES (?, 'q', 'in_progress', '2026-11-01T00:00:03.000Z', 'a', '2026-11-01T00:00:04.000Z', 'alice', '2026-11-01T00:00:04.000Z')`, cid)
		return err
	})
	failedRun("2026-11-01T00:00:05.000Z")
	rawTaskHold(t, f.s, cid, mustTaskID(t, tasks[1].ID), "2026-11-01T00:00:06.000Z")
	var hit *judgmentLimitHit
	readForTest(t, f.s, func(tx *sql.Tx) (err error) {
		hit, err = loadJudgmentLimitHit(context.Background(), tx, cid, int(version), JudgmentPoint("J5"), 2)
		return err
	})
	if hit != nil {
		t.Errorf("limit hit = %+v, want none (only 1 failure after the closed hold; the open task hold is not the boundary)", hit)
	}
}

// AC-97: タスク b を止める保留が開いている間、課題の単位の確かめの入力の「直前の保留」は、b の開いている保留にならない。
func TestHoldScopeStages_AC97_ChallengeCheckInputIgnoresTheOpenTaskHold(t *testing.T) {
	t.Parallel()
	f := newChkFixture(t)
	cid, _ := parseChallengeID(f.id)
	f.sqlExec(t, `INSERT INTO hold (challenge_id, question, from_status, raised_at, answer, answered_at, answered_by, reason, replan, closed_at)
		VALUES (?, 'Q', 'in_progress', '2026-11-01T00:00:00.000Z', 'ANSWER-BEFORE-TASK-HOLD', '2026-11-01T00:00:01.000Z', 'alice', 'challenge_check_not_met', 0, '2026-11-01T00:00:01.000Z')`, cid)
	f.sqlExec(t, `INSERT INTO hold (challenge_id, question, from_status, raised_at, task_id, scope) VALUES (?, 'q', 'in_progress', '2026-11-02T00:00:00.000Z', ?, 'task')`,
		cid, mustTaskID(t, f.taskID(t, f.id, "a")))
	var sc *j5ChallengeScope
	readForTest(t, f.s, func(tx *sql.Tx) (err error) {
		sc, err = loadJ5ChallengeScope(context.Background(), tx, cid)
		return err
	})
	if sc == nil || sc.Answer != "ANSWER-BEFORE-TASK-HOLD" {
		t.Errorf("scope = %+v, want the answer of the closed hold before the open task hold", sc)
	}
}

// AC-98: タスク b を止める保留が開いている間、実行の終わりの時刻に使う「課題の最後の保留」は、b の開いている保留にならない。
func TestHoldScopeStages_AC98_RunEndTimeIgnoresTheOpenTaskHoldOfAnotherTask(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	f.waitHold(t, f.runB)
	f.sqlExec(t, `UPDATE hold SET raised_at = '2026-01-01T00:00:00.000Z' WHERE task_id = ?`, f.taskB)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		return f.s.endRunAtHold(context.Background(), tx, f.cid, f.runA.ExternalRunID, true)
	}); err != nil {
		t.Fatal(err)
	}
	if got := f.q(t, `SELECT ended_at FROM contract_run WHERE task_id = ?`, f.taskA); got == "2026-01-01T00:00:00.000Z" || got == "" {
		t.Errorf("task a's run ended_at = %q, want the current time (not the open hold of task b)", got)
	}
	// 同じ実行のタスクを止める保留は、終わりの時刻の上限になる（保留の時刻より遅くならない）。
	f2 := newHoldScopeFixture(t)
	f2.waitHold(t, f2.runA)
	f2.sqlExec(t, `UPDATE hold SET raised_at = '2026-01-01T00:00:00.000Z' WHERE task_id = ?`, f2.taskA)
	if err := f2.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		return f2.s.endRunAtHold(context.Background(), tx, f2.cid, f2.runA.ExternalRunID, true)
	}); err != nil {
		t.Fatal(err)
	}
	if got := f2.q(t, `SELECT ended_at FROM contract_run WHERE task_id = ?`, f2.taskA); got != "2026-01-01T00:00:00.000Z" {
		t.Errorf("task a's run ended_at = %q, want the time of its own hold", got)
	}
}

// AC-103: タスクを止める保留だけが開いている課題に、進められるタスクがあるとき、サイクルの「いま始められるもの」にそのタスクが数えられる。
// AC-104: 進められるタスクが無く、ほかに進められるものも無いとき、サイクルは始まらない（いまと同じ）。
func TestHoldScopeStages_AC103_AC104_CycleWorkCountsTheProceedingTasksOfAHeldChallenge(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixtureStarted(t, false)
	count := func() int {
		w, err := f.s.CountCycleWork(context.Background(), f.agent, f.conn)
		if err != nil {
			t.Fatal(err)
		}
		return w.Stages.Run
	}
	both := count()
	if both < 2 {
		t.Fatalf("run work before the hold = %d, want at least the two tasks", both)
	}
	rawTaskHold(t, f.s, f.cid, f.taskA, "2026-11-23T00:00:00.000Z")
	if got := count(); got != both-1 {
		t.Errorf("run work with a task hold on a = %d, want %d (task b only)", got, both-1)
	}
	// AC-104: b が済みなら、進められるものが無い。
	setTaskStatusRaw(t, f.s, f.cid, "b", TaskStatusDone)
	w, err := f.s.CountCycleWork(context.Background(), f.agent, f.conn)
	if err != nil {
		t.Fatal(err)
	}
	if w.Stages.Run != 0 || w.Total != 0 {
		t.Errorf("run = %d total = %d, want 0 (nothing proceeds while the hold of the last task is open)", w.Stages.Run, w.Total)
	}
	// 課題を止める保留が開いていれば、課題の全部が止まる。
	closeRawHolds(t, f.s, f.cid)
	setTaskStatusRaw(t, f.s, f.cid, "b", TaskStatusPending)
	f.sqlExec(t, `INSERT INTO hold (challenge_id, question, from_status, raised_at) VALUES (?, 'q', 'in_progress', '2026-11-23T00:00:03.000Z')`, f.cid)
	f.sqlExec(t, `UPDATE challenge SET status = 'awaiting_human' WHERE id = ?`, f.cid)
	if got := count(); got != 0 {
		t.Errorf("run work under a challenge hold = %d, want 0", got)
	}
}

// 上げる依頼の保留は、タスクが 1 個に決まる場面でも、課題を止める保留として立つ（scope は NULL）。
func TestHoldScopeStages_ChallengeWideHoldIsNotTaskScopedEvenWhenOneTaskIsBound(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	held, err := f.s.holdContractChallengeWide(context.Background(), f.runA.RunID, f.id, "q", holdReasonReflectFailed)
	if err != nil || !held {
		t.Fatalf("holdContractChallengeWide = %v, %v, want held", held, err)
	}
	if got := f.q(t, `SELECT COALESCE(scope, 'NULL') FROM hold WHERE challenge_id = ? ORDER BY id DESC LIMIT 1`, f.cid); got != "NULL" {
		t.Errorf("hold.scope = %q, want NULL", got)
	}
}

// 課題を止める保留に絞って読む所は、タスクを止める保留が別に開いていても、課題を止める保留だけを相手にする。
func TestHoldScopeStages_ChallengeScopedReadsIgnoreTheTaskHold(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixtureStarted(t, false)
	rawTaskHold(t, f.s, f.cid, f.taskA, "2026-11-23T00:00:00.000Z")
	readForTest(t, f.s, func(tx *sql.Tx) error {
		if _, _, err := loadOpenHoldKind(context.Background(), tx, f.cid); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("loadOpenHoldKind err = %v, want sql.ErrNoRows (the only open hold stops a task)", err)
		}
		ok, reason, err := expandAddableReason(context.Background(), tx, f.cid, f.agent)
		if err != nil || ok || !strings.Contains(reason, "着手中から置かれたものではない") {
			t.Errorf("expandAddableReason = %v, %q, %v, want not addable under a task hold", ok, reason, err)
		}
		return nil
	})
}
