// このファイルは show・status が返す不可逆操作の要素に足す、実行の表示用の値
// （approved_head・last_execution_result・consecutive_failures・waiting_since）を
// 読み取る（docs/features/m4.5-learning-and-self-improvement.md S1）。
// executed_at・merge_commit は operation の列なので scanOperationRow が読む。

package core

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// fillOperationExecutionInfo は ops の各要素に、実行の表示用の値を埋める。
// 連続の回数は consecutiveFailures（導出の唯一の関数）を使い、列としては持たない。
func fillOperationExecutionInfo(ctx context.Context, tx *sql.Tx, ops []IrreversibleOperation) error {
	for i := range ops {
		opID, ok := parseOperationID(ops[i].ID)
		if !ok {
			continue
		}
		head, err := latestReleaseApprovedHead(ctx, tx, opID)
		if err != nil {
			return err
		}
		if head != "" {
			ops[i].ApprovedHead = &head
		}
		last, err := latestOperationExecution(ctx, tx, opID)
		if err != nil {
			return err
		}
		if last != nil && last.Result != "" {
			r := last.Result
			ops[i].LastExecutionResult = &r
		}
		if ops[i].ConsecutiveFailures, err = consecutiveFailures(ctx, tx, opID); err != nil {
			return err
		}
		if ops[i].State == OperationStateApproved {
			if ops[i].WaitingSince, err = firstAttemptSinceApproval(ctx, tx, opID); err != nil {
				return err
			}
		}
	}
	return nil
}

// firstAttemptSinceApproval は opID の最新の承認の後の最初の試行の開始日時を返す
// （試行が無ければ nil）。「最新の承認の後」は consecutiveFailures と同じく、最後の
// 取り消しの試行より後の試行（id で決める）とする。
func firstAttemptSinceApproval(ctx context.Context, tx *sql.Tx, opID int64) (*time.Time, error) {
	cancelPH, cancelArgs := executionResultsWhere(func(r ExecutionResult) bool {
		c, _ := r.Class()
		return c == ExecutionClassCancel
	})
	args := append([]any{opID, opID}, cancelArgs...)
	var started sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT started_at FROM operation_execution
		 WHERE operation_id = ?
		   AND id > COALESCE((SELECT MAX(id) FROM operation_execution WHERE operation_id = ? AND result IN (`+cancelPH+`)), 0)
		 ORDER BY id ASC LIMIT 1`, args...).Scan(&started)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t, err := parseTimestamp(started.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
