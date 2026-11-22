package core

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// 区切り S4c-1: 計画の後に足されたタスクの値（周の出力・完了の承認の要約）。足す依頼は、テストの中で直接置く。

// markAddedForTest は課題 cid の足す依頼を 1 件置き、タスク tid をその依頼で足したタスク（作成日時 createdAt）にする。
func markAddedForTest(t *testing.T, s *Store, cid, tid int64, createdAt time.Time) {
	t.Helper()
	writeForTest(t, s, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO task_addition (challenge_id, origin, origin_task_id, state, total_after_micros, created_at) VALUES (?, 'expand', ?, 'applied', 0, ?)`,
			cid, tid, formatTimestamp(createdAt))
		if err != nil {
			return err
		}
		aid, err := res.LastInsertId()
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE task SET addition_id = ?, created_at = ? WHERE id = ?`, aid, formatTimestamp(createdAt), tid)
		return err
	})
}

// AC-207: cycle の出力の足したタスクは、その周で足したもの（課題・題名・出どころ）だけ。前の周までに足したものは入らない。
func TestRunCycle_AddedTasksAreTheOnesAddedInThisCycle_AC207(t *testing.T) {
	t.Parallel()
	f := newJ5Fixture(t, "brief")
	f.branches.branches["feat/x"] = true
	f.branches.prs["feat/x"] = []UpstreamPullRequest{mkPR(1, "open", "develop")}
	f.checks.prs["o/r#1"] = prChecks("open", checkDone)
	var calls []JudgmentPoint
	tasks := taskRows(t, f.s, 1)
	if len(tasks) == 0 {
		t.Fatal("the fixture has no task")
	}
	in := f.cycleInput(cycleS2Invoker("met", &calls))
	in.OnBegin = func(string) { markAddedForTest(t, f.s, 1, 1, time.Now()) }

	res, err := f.s.RunCycle(context.Background(), in)
	if err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if len(res.AddedTasks) != 1 {
		t.Fatalf("AddedTasks = %+v, want the 1 task added during the cycle", res.AddedTasks)
	}
	if a := res.AddedTasks[0]; a.ChallengeID != "C-1" || a.TaskID != "T-1" || a.Title != tasks[0].Title || a.Origin != TaskAdditionOriginExpand {
		t.Errorf("AddedTasks[0] = %+v, want C-1 / T-1 / %q / expand", a, tasks[0].Title)
	}

	time.Sleep(10 * time.Millisecond)
	in2 := f.cycleInput(cycleS2Invoker("met", &calls))
	res2, err := f.s.RunCycle(context.Background(), in2)
	if err != nil {
		t.Fatalf("second RunCycle: %v", err)
	}
	if res2.AddedTasks == nil || len(res2.AddedTasks) != 0 {
		t.Errorf("second cycle AddedTasks = %#v, want an empty list (the task was added in the earlier cycle)", res2.AddedTasks)
	}
}

// AC-209: 完了の承認の要約に、計画の後に足したタスクが入る。計画の承認で作ったタスクは入らない。
func TestPrepareApproval_CompletionSummaryListsTheAddedTasks_AC209(t *testing.T) {
	t.Parallel()
	s, id, tasks := additionFixture(t)
	cid, _ := parseChallengeID(id)
	tid := mustTaskID(t, tasks[1].ID)
	markAddedForTest(t, s, cid, mustTaskID(t, tasks[0].ID), time.Now())
	writeForTest(t, s, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE task SET addition_id = NULL WHERE id = ?`, mustTaskID(t, tasks[0].ID)); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE task SET addition_id = 1 WHERE id = ?`, tid)
		return err
	})
	writeForTest(t, s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET status = 'awaiting_completion_approval' WHERE id = ?`, cid)
		return err
	})
	for name, prepare := range map[string]func() (*ApprovalPreview, error){
		"PrepareApproval":    func() (*ApprovalPreview, error) { return s.PrepareApproval(context.Background(), id) },
		"PrepareApprovalFor": func() (*ApprovalPreview, error) { return s.PrepareApprovalFor(context.Background(), id, nil) },
	} {
		p, err := prepare()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(p.AddedTasks) != 1 || p.AddedTasks[0].TaskID != tasks[1].ID || p.AddedTasks[0].Title != tasks[1].Title || p.AddedTasks[0].Origin != TaskAdditionOriginExpand {
			t.Errorf("%s: AddedTasks = %+v, want only %s (%q, expand)", name, p.AddedTasks, tasks[1].ID, tasks[1].Title)
		}
	}
}
