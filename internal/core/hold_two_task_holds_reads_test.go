package core

// このファイルは、区切り S4c-3 の AC-85 のために、「課題の最新の保留」を 1 件読んでいた所を、
// タスクを止める保留が 2 つ（タスク a・b）開いている課題で確かめる。どの試験も、読む相手を取り違えないこと
// （ほかのタスクの保留・課題を止める保留でない保留を相手にしないこと、保留ごとに返すこと）を確かめる。
// ほかの所の試験の名前は、PR の説明の表に並べる。

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// 親が直接行う保留の判定は、課題を止める保留だけを読む。タスクを止める保留の種類が親が直接行うでも、課題は親のエージェントが動く番に入らない。
func TestHoldTwoTaskHolds_ParentDirectReadsOnlyTheChallengeScopedHold(t *testing.T) {
	t.Parallel()
	f := twoTaskHoldsFixture(t)
	f.sqlExec(t, `UPDATE hold SET kind = ? WHERE challenge_id = ? AND task_id = ?`, string(HoldKindParentDirect), f.cid, f.taskB)
	readForTest(t, f.s, func(tx *sql.Tx) error {
		if _, _, err := loadOpenHoldKind(context.Background(), tx, f.cid); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("loadOpenHoldKind err = %v, want sql.ErrNoRows (both open holds stop a task)", err)
		}
		got, err := listParentAgentChallenges(context.Background(), tx)
		if err != nil {
			return err
		}
		if len(got) != 0 {
			t.Errorf("listParentAgentChallenges = %d challenges, want 0 (only task holds are open)", len(got))
		}
		return nil
	})
}

// 学びの判断点へ渡す保留は、課題の全部の保留を、保留ごとに（古い順に）返す。最新の 1 件に丸めない。
func TestHoldTwoTaskHolds_J6ReadsEveryHoldInOrder(t *testing.T) {
	t.Parallel()
	f := twoTaskHoldsFixture(t)
	readForTest(t, f.s, func(tx *sql.Tx) error {
		holds, err := loadJ6Holds(context.Background(), tx, f.cid)
		if err != nil {
			return err
		}
		if len(holds) != 2 || holds[0].ID >= holds[1].ID {
			t.Errorf("loadJ6Holds = %+v, want 2 holds in ascending id order", holds)
		}
		return nil
	})
}

// 委譲の履歴をタスクの単位に絞ると、タスク a の履歴にはタスク a の保留だけが残り、タスク b の保留は入らない（再開の対象と数えの境の取り違えを防ぐ）。
func TestHoldTwoTaskHolds_LaunchHistoryForTaskKeepsOnlyTheTasksOwnHold(t *testing.T) {
	t.Parallel()
	f := twoTaskHoldsFixture(t)
	taskA, taskB := f.taskID(t, f.id, "a"), f.taskID(t, f.id, "b")
	readForTest(t, f.s, func(tx *sql.Tx) error {
		h, err := loadLaunchHistory(context.Background(), tx, f.cid)
		if err != nil {
			return err
		}
		for _, tc := range []struct{ task, other string }{{taskA, taskB}, {taskB, taskA}} {
			got := h.forTask(tc.task).Holds
			if len(got) != 1 || got[0].TaskID == nil || *got[0].TaskID != tc.task {
				t.Errorf("forTask(%s).Holds = %+v, want only the hold of %s (not %s)", tc.task, got, tc.task, tc.other)
			}
		}
		return nil
	})
}
