package core

// このファイルは、課題の遷移に合わせてタスクの状態を進める口を持つ（完了の検証・完了の差し戻し・反映のやり直し・
// 取り下げ・完了の承認）。タスクの行の読み書きは task.go の口を使い、状態の変化と作業ログは遷移と同じトランザクションで書く。
// 課題の状態と遷移はここでは変えない。

import (
	"context"
	"database/sql"
	"errors"
)

// loadVerifyingTask は課題 cid のタスクのうち状態が verifying のものを返す。ちょうど 1 個のときだけ返し、
// それ以外（0 個・2 個以上）は nil（完了の検証の対象のタスクを決めない）。
func loadVerifyingTask(ctx context.Context, tx *sql.Tx, cid int64) (*Task, error) {
	all, err := listTasks(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	var found *Task
	for i := range all {
		if all[i].Status != TaskStatusVerifying {
			continue
		}
		if found != nil {
			return nil, nil
		}
		found = &all[i]
	}
	return found, nil
}

// runTaskID は run（内部整数 ID）の task_id を読む（NULL は nil）。run が無ければ ErrNotFound。
func runTaskID(ctx context.Context, tx *sql.Tx, runID int64) (*int64, error) {
	var v sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT task_id FROM run WHERE id = ?`, runID).Scan(&v); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if !v.Valid {
		return nil, nil
	}
	return &v.Int64, nil
}

// moveVerifyingTask は、完了の検証の結果に合わせて、検証中（verifying）のタスクを to にする。
// taskID が nil（検証の対象のタスクが無い）か、タスクが verifying でなければ何もしない。
func moveVerifyingTask(ctx context.Context, tc *transitionCtx, taskID *int64, to TaskStatus) error {
	if taskID == nil {
		return nil
	}
	var status string
	if err := tc.tx.QueryRowContext(ctx, `SELECT status FROM task WHERE id = ?`, *taskID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if TaskStatus(status) != TaskStatusVerifying {
		return nil
	}
	_, err := setTaskStatus(ctx, tc.tx, tc.rec, *taskID, to)
	return err
}

// approvedPlanVersionOf は課題 cid の承認済みの計画の版（最後の計画の承認の approval.plan_version）を返す。
// 計画の承認が無い・版が無いときは ok=false。
func approvedPlanVersionOf(ctx context.Context, tx *sql.Tx, cid int64) (version int64, ok bool, err error) {
	var v sql.NullInt64
	err = tx.QueryRowContext(ctx,
		`SELECT plan_version FROM approval WHERE challenge_id = ? AND kind = ? AND decision = ? ORDER BY id DESC LIMIT 1`,
		cid, string(ApprovalKindPlan), string(ApprovalDecisionApproved)).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if !v.Valid || v.Int64 <= 0 {
		return 0, false, nil
	}
	return v.Int64, true, nil
}

// reopenDoneTasks は課題 cid の、承認済みの計画の版のタスクのうち done のものを in_progress にする
// （完了の差し戻し・反映のやり直しで課題が着手中へ戻るとき）。前の版の計画の done のタスクは動かさない。
func reopenDoneTasks(ctx context.Context, tx *sql.Tx, rec *activityRecorder, cid int64) error {
	v, ok, err := approvedPlanVersionOf(ctx, tx, cid)
	if err != nil || !ok {
		return err
	}
	_, err = setTasksStatus(ctx, tx, rec, cid,
		taskFilter{PlanVersion: &v, Statuses: []TaskStatus{TaskStatusDone}}, TaskStatusInProgress)
	return err
}

// cancelUnfinishedTasks は課題 cid の、done でも cancelled でもないタスク（pending・in_progress・verifying）を
// cancelled にする（取り下げ・完了の承認で課題が終わるとき）。done のタスクは残す。
func cancelUnfinishedTasks(ctx context.Context, tx *sql.Tx, rec *activityRecorder, cid int64) error {
	_, err := setTasksStatus(ctx, tx, rec, cid,
		taskFilter{Statuses: []TaskStatus{TaskStatusPending, TaskStatusInProgress, TaskStatusVerifying}}, TaskStatusCancelled)
	return err
}
