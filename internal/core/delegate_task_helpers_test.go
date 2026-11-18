package core

import (
	"context"
	"database/sql"
	"testing"
)

// seedPlanTasks は、計画の承認（onPlanApproved）が行うタスクの記録を、計画の版 version について直に行う
// （既存のテストの足場が承認を通さずに着手中の課題を作るため）。同じ課題の済みでも取りやめでもないタスクを取りやめにし、
// その版の構造化した出力の tasks から task の行（pending）を作り、統合先を使うかを課題へ記録する。
// 構造化した出力を持たない計画（spec が NULL）・タスクを持たない過去の形では、タスクを作らない。
func seedPlanTasks(t *testing.T, s *Store, cid, version int64) []Task {
	t.Helper()
	var out []Task
	err := s.mutate(context.Background(), ChannelCLI, func(tx *sql.Tx, rec *activityRecorder) error {
		view, err := loadPlanSpec(context.Background(), tx, cid, version)
		if err != nil {
			return err
		}
		if _, err := setTasksStatus(context.Background(), tx, rec, cid,
			taskFilter{Statuses: []TaskStatus{TaskStatusPending, TaskStatusInProgress, TaskStatusVerifying}}, TaskStatusCancelled); err != nil {
			return err
		}
		if len(view.Tasks) > 0 {
			if out, err = createTasks(context.Background(), tx, rec, cid, version, view.Tasks); err != nil {
				return err
			}
		}
		if view.UsesIntegration != nil {
			return setChallengeUsesIntegration(context.Background(), tx, cid, view.UsesIntegration)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seedPlanTasks: %v", err)
	}
	return out
}

// taskRows は課題 cid のタスクを ID の昇順で返す。
func taskRows(t *testing.T, s *Store, cid int64) []Task {
	t.Helper()
	var out []Task
	err := s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
		out, err = listTasks(context.Background(), tx, cid)
		return err
	})
	if err != nil {
		t.Fatalf("listTasks: %v", err)
	}
	return out
}

// setAllTasksStatus は課題 cid のタスクのうち、状態が from のものを to にする（作業ログつき）。
func setAllTasksStatus(t *testing.T, s *Store, cid int64, from, to TaskStatus) {
	t.Helper()
	err := s.mutate(context.Background(), ChannelCLI, func(tx *sql.Tx, rec *activityRecorder) error {
		_, err := setTasksStatus(context.Background(), tx, rec, cid, taskFilter{Statuses: []TaskStatus{from}}, to)
		return err
	})
	if err != nil {
		t.Fatalf("setAllTasksStatus: %v", err)
	}
}

// moveVerifyingTasksRaw は、検証中（verifying）のタスクを to へ直に書き換える（作業ログを残さない）。
// テストの足場が core の遷移を通さずに課題の状態を直に書き換える箇所（完了の検証を通さずに完了の承認待ちへ置く・
// 保留への回答を通さずに着手中へ置く）で、本物の遷移なら core が進めていたタスクの状態を合わせるためだけに使う。
// core の遷移を通すテストでは使わない（遷移がタスクを進める）。
func moveVerifyingTasksRaw(t *testing.T, s *Store, to TaskStatus) {
	t.Helper()
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task SET status = ? WHERE status = 'verifying'`, string(to))
		return err
	})
	if err != nil {
		t.Fatalf("moveVerifyingTasksRaw: %v", err)
	}
}

// addTaskRowForTest は課題 cid の計画の版 version に、状態が pending のタスクの行を 1 個足す。
func addTaskRowForTest(t *testing.T, s *Store, cid, version int64, key, repo, operation string) Task {
	t.Helper()
	var out []Task
	err := s.mutate(context.Background(), ChannelCLI, func(tx *sql.Tx, rec *activityRecorder) error {
		var err error
		out, err = createTasks(context.Background(), tx, rec, cid, version, []TaskInput{
			{Key: key, Title: "題 " + key, Body: "何をするか " + key, DoneCriteria: "完了条件 " + key, NonGoals: []string{"やらないこと " + key}, Repo: repo, Operation: operation},
		})
		return err
	})
	if err != nil {
		t.Fatalf("addTaskRowForTest: %v", err)
	}
	return out[0]
}
