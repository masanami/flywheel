package core

// このファイルは委譲の段が使うタスクの口を持つ: 起動できるタスクの判定、委譲の起動・結果によるタスクの
// 状態の変化、J3 の入力の区画「タスク」。タスクの行の読み書きは task.go の口を使う。
// 課題の状態と遷移は変えない。委譲の履歴・再開・やり直しの数え・予算の枠は、課題と計画の版の単位のまま。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// labelTaskSection は J3 の入力の区画「タスク」の見出し。
const labelTaskSection = "タスク"

// isLaunchableTaskStatus は、状態が起動できるタスクのもの（pending・in_progress）か。
func isLaunchableTaskStatus(s TaskStatus) bool {
	return s == TaskStatusPending || s == TaskStatusInProgress
}

// loadLaunchableTasks は課題 cid の起動できるタスク（task の行のうち状態が pending か in_progress のもの。
// 計画の版は問わない）を ID の昇順で返す。構造化した出力ではなく行で数える。
func loadLaunchableTasks(ctx context.Context, tx *sql.Tx, cid int64) ([]Task, error) {
	all, err := listTasks(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(all))
	for _, t := range all {
		if isLaunchableTaskStatus(t.Status) {
			out = append(out, t)
		}
	}
	return out, nil
}

// launchGate は、起動できるタスクの数と課題の統合先の使用から、委譲を起動できるかを決める。
//
//   - 起動できるタスクが 0 個 → no_tasks
//   - 課題が統合先を使う（uses_integration が 1）、または起動できるタスクが 2 個以上 → integration_unavailable
//   - ちょうど 1 個で、そのタスクが対応する子が起動を妨げる（taskUpstreamExclusion）→ upstream_closed・upstream_missing
//   - ちょうど 1 個 → 起動できる（task を返す。NotStarted は nil）
//
// 計画の検査（validateJ2Output）はここで掛けない（呼び出し側が、起動できるときだけ後で行う）。
func launchGate(ctx context.Context, tx *sql.Tx, challengeID string, cid int64) (*Task, *NotStarted, error) {
	tasks, err := loadLaunchableTasks(ctx, tx, cid)
	if err != nil {
		return nil, nil, err
	}
	if len(tasks) == 0 {
		return nil, &NotStarted{ChallengeID: challengeID, Reason: NotStartedNoTasks, Detail: "起動できるタスクが無い"}, nil
	}
	integ, err := challengeUsesIntegration(ctx, tx, cid)
	if err != nil {
		return nil, nil, err
	}
	if len(tasks) >= 2 || (integ != nil && *integ) {
		return nil, &NotStarted{ChallengeID: challengeID, Reason: NotStartedIntegrationUnavailable,
			Detail: "統合先を使う課題と、起動できるタスクが 2 個以上の課題は、後の区切りまで起動しない"}, nil
	}
	// タスクが対応する子の上流の状態（記録が無い・閉じた・無い）で起動しない。
	if ns, err := taskUpstreamExclusion(ctx, tx, challengeID, cid, &tasks[0]); err != nil || ns != nil {
		return nil, ns, err
	}
	return &tasks[0], nil, nil
}

// taskIntID はタスクの内部整数 ID。
func taskIntID(t *Task) int64 {
	n, _ := parseTaskID(t.ID)
	return n
}

// taskIDPtr はタスクの内部整数 ID のポインタ（ID を持たないタスクのゼロ値は nil＝NULL）。
func taskIDPtr(t *Task) *int64 {
	if n := taskIntID(t); n > 0 {
		return &n
	}
	return nil
}

// newInvokerRecorder は、委譲の run を原因とする作業ログ（経路 invoker）の記録口を tx に作る。
func (s *Store) newInvokerRecorder(tx *sql.Tx, runID *int64) (*activityRecorder, error) {
	actor, err := s.resolveActor()
	if err != nil {
		return nil, err
	}
	return &activityRecorder{
		tx: tx, at: s.currentTime(), actor: actor, channel: ChannelInvoker, verification: VerificationNone,
		insert: s.insertActivity, runID: runID,
	}, nil
}

// startTaskTx は委譲の起動で、pending のタスクを in_progress にする（作業ログは同じトランザクション）。
// すでに in_progress など pending でないタスクの再開・やり直しでは何も変えない。
func (s *Store) startTaskTx(ctx context.Context, tx *sql.Tx, taskID int64, runID *int64) error {
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM task WHERE id = ?`, taskID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if TaskStatus(status) != TaskStatusPending {
		return nil
	}
	rec, err := s.newInvokerRecorder(tx, runID)
	if err != nil {
		return err
	}
	_, err = setTaskStatus(ctx, tx, rec, taskID, TaskStatusInProgress)
	return err
}

// verifyTaskInTransition は、課題が検証中へ進む遷移（tc）と同じトランザクションで、タスクを verifying にする
// （作業ログも同じトランザクション）。taskID が nil（過去の行など）なら何もしない。
func verifyTaskInTransition(ctx context.Context, tc *transitionCtx, taskID *int64) error {
	if taskID == nil {
		return nil
	}
	// 作業中のタスクだけを検証中にする（計画の作り直しで取りやめになったタスクなどを動かさない）。
	var status string
	if err := tc.tx.QueryRowContext(ctx, `SELECT status FROM task WHERE id = ?`, *taskID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if TaskStatus(status) != TaskStatusInProgress {
		return nil
	}
	_, err := setTaskStatus(ctx, tc.tx, tc.rec, *taskID, TaskStatusVerifying)
	return err
}

// verifyContractTask は、契約の run（externalRunID）が結び付くタスクを検証中にする（課題が検証中へ進む遷移 tc と同じ
// トランザクション）。タスクに結び付かない契約の run（過去の行）では何もしない。
func verifyContractTask(ctx context.Context, tc *transitionCtx, externalRunID string) error {
	taskID, err := contractRunTaskID(ctx, tc.tx, tc.id, externalRunID)
	if err != nil {
		return err
	}
	return verifyTaskInTransition(ctx, tc, taskID)
}

// contractRunTaskID は課題の、契約の実行 ID が externalRunID の契約の run の task_id を読む（無ければ nil）。
func contractRunTaskID(ctx context.Context, tx *sql.Tx, challengeID int64, externalRunID string) (*int64, error) {
	var v sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT task_id FROM contract_run WHERE challenge_id = ? AND external_run_id = ?`,
		challengeID, externalRunID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !v.Valid) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v.Int64, nil
}

// formatTaskSection は J3 の入力の区画「タスク」の本文（題名・何をするか・完了条件・やらないこと）。
func formatTaskSection(t Task) string {
	nonGoals := "（なし）"
	if len(t.NonGoals) > 0 {
		lines := make([]string, len(t.NonGoals))
		for i, g := range t.NonGoals {
			lines[i] = "- " + g
		}
		nonGoals = "\n" + strings.Join(lines, "\n")
	}
	return fmt.Sprintf("題名: %s\n\n何をするか:\n%s\n\n完了条件:\n%s\n\nやらないこと: %s", t.Title, t.Body, t.DoneCriteria, nonGoals)
}
