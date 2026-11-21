package core

// このファイルは、起動の履歴（連続失敗・差し戻し・再開の対象）をタスクの単位に絞る変更（区切り S3b の S3b-5。
// docs/features/core-redesign.md の受入基準 AC-19・AC-36）を、実ストア・偽の契約のコマンドで、起動の経路を通して検証する。
// 絞り込み（launchHistory.forTask）そのものの単体試験は delegate_resume_test.go にある。ここは、その絞り込みが
// 委譲の起動（delegate.go）・レビュー待ちへの差し戻し（contract_delegate_review.go）・状況の予算切れ（budget.go）の
// 各経路で効いていることを固定する（絞り込みを外すと落ちる）。

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// insertTaskRun は、タスク taskID（0 ならタスクに結び付けない）の終わった run を、履歴の試験のために直に記録する。
func insertTaskRun(t *testing.T, s *Store, cid, taskID int64, kind, judgment, result, sessionID, costSource, output string, costMicros, maxBudgetMicros int64) {
	t.Helper()
	var task, j, sess, src, out any
	if taskID != 0 {
		task = taskID
	}
	if judgment != "" {
		j = judgment
	}
	if sessionID != "" {
		sess = sessionID
	}
	if costSource != "" {
		src = costSource
	}
	if output != "" {
		out = output
	}
	writeForTest(t, s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO run (challenge_id, kind, judgment, challenge_version, plan_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket, cost_usd, cost_source, output, task_id)
			VALUES (?, ?, ?, 1, 1, ?, 1, 'h', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:01.000Z', ?, ?, 'impl', ?, ?, ?, ?)`,
			cid, kind, j, sess, result, maxBudgetMicros, costMicros, src, out, task)
		return err
	})
}

// failDelegation は、タスクの委譲の run を失敗（errored）で n 本記録する。
func failDelegation(t *testing.T, s *Store, cid, taskID int64, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		insertTaskRun(t, s, cid, taskID, "delegate", "", "errored", fmt.Sprintf("sess-%d-%d", taskID, i), "", "", 0, 1000000)
	}
}

// notMetOf は、タスクへの J5 の not_met（差し戻し。指摘は feedback）の run を記録する。
func notMetRun(t *testing.T, s *Store, cid, taskID int64, feedback string) {
	t.Helper()
	insertTaskRun(t, s, cid, taskID, "judgment", "J5", "succeeded", "", "",
		fmt.Sprintf(`{"verdict":"not_met","reason":"r","feedback":%q}`, feedback), 0, 1000000)
}

// AC-19: 統合先を使う 2 個のタスクの課題で、タスク a の委譲の失敗が連続の失敗の上限に達していても、
// そのことは別のタスク b の起動に混ざらない（b は起動される。上限の保留にならない）。
// a の失敗は a だけの上限に数える（a が起動できるタスクなら、上限の保留になる）。
func TestLaunchHistoryTask_AC19_FailuresOfAnotherTaskDoNotStopTheLaunch(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) (*contractFixture, string, int64) {
		f := newMultiFixture(t, 4)
		id := f.newMulti(t, "two", "P1", true, multiTask{key: "a"}, multiTask{key: "b"})
		cid, _ := parseChallengeID(id)
		return f, id, cid
	}
	t.Run("a's failures at the limit do not hold b", func(t *testing.T) {
		f, id, cid := setup(t)
		taskA, taskB := f.taskID(t, id, "a"), f.taskID(t, id, "b")
		failDelegation(t, f.s, cid, mustTaskID(t, taskA), f.agent.FailureLimit) // a は上限まで失敗した
		setTaskStatusRaw(t, f.s, cid, "a", TaskStatusCancelled)                 // 起動の候補は b だけ
		res := f.runDelegationStage(t, nil)
		if got := notStartedByReason(res, NotStartedFailureLimit); len(got) != 0 {
			t.Fatalf("not_started = %+v, want no failure_limit for task b", res.NotStarted)
		}
		if got := f.startedTasks(t); !equalStrings(got, []string{taskB}) {
			t.Fatalf("started tasks = %v, want only %s", got, taskB)
		}
		if got := f.holdReasons(t, id); len(got) != 0 {
			t.Errorf("holds = %v, want none", got)
		}
	})
	t.Run("a's own failures at the limit hold a", func(t *testing.T) {
		f, id, cid := setup(t)
		taskA := f.taskID(t, id, "a")
		failDelegation(t, f.s, cid, mustTaskID(t, taskA), f.agent.FailureLimit)
		setTaskStatusRaw(t, f.s, cid, "b", TaskStatusCancelled) // 起動の候補は a だけ
		res := f.runDelegationStage(t, nil)
		if got := notStartedByReason(res, NotStartedFailureLimit); len(got) != 1 || got[0].ChallengeID != id {
			t.Fatalf("not_started = %+v, want one failure_limit for %s", res.NotStarted, id)
		}
		if len(f.client.starts) != 0 {
			t.Errorf("starts = %v, want none at the failure limit", f.client.starts)
		}
		if got := f.holdReasons(t, id); len(got) != 1 {
			t.Fatalf("holds = %v, want one hold", got)
		}
		if got := f.delegateFixture.queryString(t, `SELECT task_id FROM hold`); got != fmt.Sprint(mustTaskID(t, taskA)) {
			t.Errorf("hold.task_id = %q, want task %s", got, taskA)
		}
	})
}

// multiReviewConnectorsJSON は multiConnectorsJSON の resume に、選択肢と添え書きの差し込みを足した宣言。
func multiReviewConnectorsJSON() string {
	return strings.Replace(multiConnectorsJSON(), `"--answer", "{answer}"`, `"--choice", "{choice}", "--note", "{note}"`, 1)
}

// AC-36: タスク a への差し戻しが差し戻しの上限に達していても、別のタスク b のレビュー待ちへの差し戻し
// （pendingReviewReply の経路）は、上限の保留にならず、b への指摘を同じ契約の run へ返す。
// a の差し戻しは a だけの上限に数える（a のレビュー待ちなら、上限の保留になる）。
func TestLaunchHistoryTask_AC36_ReworksOfAnotherTaskDoNotStopTheReviewReply(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) (*contractFixture, string, int64, string, string) {
		f := newMultiFixture(t, 4)
		conn, err := parseConnectorsDeclaration([]byte(multiReviewConnectorsJSON()))
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.validate(); err != nil {
			t.Fatal(err)
		}
		f.conn = conn
		f.agent.ReworkLimit = 2
		id := f.newMulti(t, "two", "P1", true, multiTask{key: "a"}, multiTask{key: "b"})
		cid, _ := parseChallengeID(id)
		f.runDelegationStage(t, nil) // a・b の契約の run を始める
		if len(f.client.starts) != 2 {
			t.Fatalf("starts = %d, want 2", len(f.client.starts))
		}
		return f, id, cid, f.taskID(t, id, "a"), f.taskID(t, id, "b")
	}
	externalRun := func(t *testing.T, f *contractFixture, task string) string {
		return f.delegateFixture.queryString(t, fmt.Sprintf(`SELECT external_run_id FROM contract_run WHERE task_id = %d`, mustTaskInt(task)))
	}
	waitForReview := func(t *testing.T, f *contractFixture, task string) {
		if err := f.delegateFixture.exec(`UPDATE contract_run SET state = 'waiting', requested_kind = 'review' WHERE task_id = ?`, mustTaskInt(task)); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("a's reworks at the limit do not hold b", func(t *testing.T) {
		f, id, cid, taskA, taskB := setup(t)
		a, b := mustTaskID(t, taskA), mustTaskID(t, taskB)
		notMetRun(t, f.s, cid, a, "FEEDBACK-A1")
		notMetRun(t, f.s, cid, a, "FEEDBACK-A2") // a は上限（2）まで差し戻された
		notMetRun(t, f.s, cid, b, "FEEDBACK-B")  // b は 1 回
		// a のその後の契約の run（別のタスクの run を挟んでも、b の指摘は渡す）。
		insertTaskRun(t, f.s, cid, a, "contract", "", "succeeded", "sess-a-later", "", "", 0, 1000000)
		waitForReview(t, f, taskB)
		bExt := externalRun(t, f, taskB)
		f.client.resumeOut = contractOut(ContractStateRunning, nil, func(st *ContractStatus) { st.RunID = bExt })
		f.poll(t)
		if got := f.holdReasons(t, id); len(got) != 0 {
			t.Fatalf("holds = %v, want none (b has one rework of its own)", got)
		}
		if len(f.client.resumes) != 1 {
			t.Fatalf("resumes = %v, want one respond to b's run", f.client.resumes)
		}
		resume := f.client.resumes[0]
		if got := argAfter(t, resume, "--choice"); got != "respond" {
			t.Errorf("--choice = %q, want respond", got)
		}
		if got := argAfter(t, resume, "--note"); got != "FEEDBACK-B" {
			t.Errorf("--note = %q, want b's own feedback FEEDBACK-B", got)
		}
		if got := f.status(t, id); got != StatusInProgress {
			t.Errorf("status = %s, want in_progress", got)
		}
	})
	t.Run("a's own reworks at the limit hold a", func(t *testing.T) {
		f, id, cid, taskA, _ := setup(t)
		a := mustTaskID(t, taskA)
		notMetRun(t, f.s, cid, a, "FEEDBACK-A1")
		notMetRun(t, f.s, cid, a, "FEEDBACK-A2")
		waitForReview(t, f, taskA)
		f.poll(t)
		if got := f.holdReasons(t, id); len(got) != 1 {
			t.Fatalf("holds = %v, want one hold", got)
		}
		if got := f.status(t, id); got != StatusAwaitingHuman {
			t.Errorf("status = %s, want awaiting_human", got)
		}
		if len(f.client.resumes) != 0 {
			t.Errorf("resumes = %v, want none at the rework limit", f.client.resumes)
		}
	})
}

// 再開の許可（effectiveImplBudget）: 費用が取れない失敗（出所が unknown）の後の再開の許可は、その失敗をしたタスク a にだけ効く。
// 実装の枠が尽きている 2 個のタスクのうち、a は人間の増額を待たず（状況の予算切れに出ない）、b は予算切れに出る
// （a の失敗の run を再開の対象にして、b の枠に許可が及ぶことはない）。
func TestLaunchHistoryTask_ResumeGrantOfOneTaskDoesNotCoverAnotherTask(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	f.agent.FailureLimit = 5
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 3, 2))
	cid, _ := parseChallengeID(id)
	tasks := taskRows(t, f.s, cid)
	a, b := mustTaskID(t, tasks[0].ID), mustTaskID(t, tasks[1].ID)
	// a: 実装の枠を 2.5 USD 使って、費用の取れない失敗で止まった（残り 0.5 USD。失敗前の上限は 3 USD）。
	insertTaskRun(t, f.s, cid, a, "delegate", "", "timed_out", "sess-a", "unknown", "", usdToMicros(2.5), usdToMicros(3))
	// b: 実装の枠を 2.5 USD 使った（契約の run。委譲の run は無い。残り 0.5 USD）。
	insertTaskRun(t, f.s, cid, b, "contract", "", "succeeded", "sess-b", "reported", "", usdToMicros(2.5), usdToMicros(3))
	got := f.overview(t).NeedsHumanBudgetExhausted
	if len(got) != 1 || got[0].ChallengeID != id || got[0].TaskID != tasks[1].ID {
		t.Fatalf("budget_exhausted = %+v, want exactly task %s (task %s is allowed one resume)", got, tasks[1].ID, tasks[0].ID)
	}
}
