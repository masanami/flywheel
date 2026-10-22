// このファイルは release の実行の試行（マイグレーション 0009 の表
// operation_execution）に対する、非公開の行レベルの読み書きと、連続の回数の導出を
// 持つ（docs/features/m4.5-learning-and-self-improvement.md S1）。
//
// 実行の判定・取り消し・実行済みの書き込みは別のチケットが、ここの関数を使って作る。
// 試行の書き込みは作業ログに載せない（実行済みか取り消しの結果のときの op_execute は、
// 実行済み・取り消しを書く関数が同じトランザクションで残す）。

package core

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/masanami/flywheel/internal/core/internal/store"
)

var (
	// errActiveExecutionExists は、操作に終了していない試行があることを表す
	// （部分一意索引 idx_operation_execution_active の違反を翻訳する）。
	errActiveExecutionExists = errors.New("core: operation already has an active execution attempt")

	// errExecutionNotOpen は、閉じようとした試行が存在しない、または既に閉じていることを表す。
	errExecutionNotOpen = errors.New("core: execution attempt is not open")
)

// operationExecutionRow は operation_execution の 1 行。
type operationExecutionRow struct {
	ID          int64
	OperationID int64
	StartedAt   time.Time
	// EndedAt は終了していない試行ではゼロ値。
	EndedAt time.Time
	// Result は終了していない試行では空。
	Result       ExecutionResult
	ApprovedHead string
	ObservedHead string
	Detail       string
	PID          int64
	Host         string
}

// open は試行が終了していないかを返す。
func (r *operationExecutionRow) open() bool { return r.EndedAt.IsZero() }

// beginOperationExecutionInput は試行を始める入力。
type beginOperationExecutionInput struct {
	StartedAt time.Time
	// ApprovedHead は承認の記録の head_sha（NULL なら空）。
	ApprovedHead string
	PID          int64
	Host         string
}

// beginOperationExecution は opID の操作に新しい試行（終了していない行）を作る。
// 呼び出し側は操作が同じトランザクションの中に存在することを確かめておく。
//
//   - PID が 0 以下・Host が空・StartedAt がゼロ値なら ErrValidation
//   - 操作に既に終了していない試行があれば errActiveExecutionExists
func beginOperationExecution(ctx context.Context, tx *sql.Tx, opID int64, in beginOperationExecutionInput) (*operationExecutionRow, error) {
	if opID <= 0 || in.PID <= 0 || in.Host == "" || in.StartedAt.IsZero() {
		return nil, ErrValidation
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO operation_execution (operation_id, started_at, approved_head, pid, host)
		 VALUES (?, ?, ?, ?, ?)`,
		opID, formatTimestamp(in.StartedAt), nullableTextColumn(in.ApprovedHead), in.PID, in.Host)
	if store.IsUniqueViolation(err) {
		return nil, errActiveExecutionExists
	}
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return getOperationExecution(ctx, tx, id)
}

// closeOperationExecutionInput は試行を閉じる入力。
type closeOperationExecutionInput struct {
	EndedAt      time.Time
	Result       ExecutionResult
	ObservedHead string
	Detail       string
}

// closeOperationExecution は id の終了していない試行を閉じる。
//
//   - Result が閉集合の外・EndedAt がゼロ値なら ErrValidation
//   - 試行が無い、または既に閉じていれば errExecutionNotOpen
func closeOperationExecution(ctx context.Context, tx *sql.Tx, id int64, in closeOperationExecutionInput) error {
	if _, ok := in.Result.Class(); !ok || in.EndedAt.IsZero() {
		return ErrValidation
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE operation_execution SET ended_at = ?, result = ?, observed_head = ?, detail = ?
		 WHERE id = ? AND ended_at IS NULL`,
		formatTimestamp(in.EndedAt), string(in.Result), nullableTextColumn(in.ObservedHead), nullableTextColumn(in.Detail), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errExecutionNotOpen
	}
	return nil
}

const operationExecutionSelect = `SELECT id, operation_id, started_at, ended_at, result, approved_head, observed_head, detail, pid, host
	FROM operation_execution`

func scanOperationExecution(sc interface{ Scan(...any) error }) (*operationExecutionRow, error) {
	var (
		r                                         operationExecutionRow
		started                                   string
		ended, result, approved, observed, detail sql.NullString
	)
	if err := sc.Scan(&r.ID, &r.OperationID, &started, &ended, &result, &approved, &observed, &detail, &r.PID, &r.Host); err != nil {
		return nil, err
	}
	var err error
	if r.StartedAt, err = parseTimestamp(started); err != nil {
		return nil, err
	}
	if ended.Valid {
		if r.EndedAt, err = parseTimestamp(ended.String); err != nil {
			return nil, err
		}
	}
	r.Result = ExecutionResult(result.String)
	r.ApprovedHead = approved.String
	r.ObservedHead = observed.String
	r.Detail = detail.String
	return &r, nil
}

// getOperationExecution は id の試行を返す（無ければ sql.ErrNoRows）。
func getOperationExecution(ctx context.Context, tx *sql.Tx, id int64) (*operationExecutionRow, error) {
	return scanOperationExecution(tx.QueryRowContext(ctx, operationExecutionSelect+` WHERE id = ?`, id))
}

// listOperationExecutions は opID の操作の試行を id の昇順で返す。
func listOperationExecutions(ctx context.Context, tx *sql.Tx, opID int64) ([]*operationExecutionRow, error) {
	rows, err := tx.QueryContext(ctx, operationExecutionSelect+` WHERE operation_id = ? ORDER BY id ASC`, opID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*operationExecutionRow
	for rows.Next() {
		r, err := scanOperationExecution(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// latestOperationExecution は opID の操作の直前の試行（id が最大の行）を返す。
// 試行が無ければ nil。
func latestOperationExecution(ctx context.Context, tx *sql.Tx, opID int64) (*operationExecutionRow, error) {
	r, err := scanOperationExecution(tx.QueryRowContext(ctx,
		operationExecutionSelect+` WHERE operation_id = ? ORDER BY id DESC LIMIT 1`, opID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// executionResultsWhere は match を満たす結果の SQL 断片（`?, ?`）と引数を返す。
// match が true のものを集める（正本は executionResultSpecs の 1 か所）。
func executionResultsWhere(match func(ExecutionResult) bool) (string, []any) {
	var args []any
	ph := ""
	for _, r := range ExecutionResultValues() {
		if !match(r) {
			continue
		}
		if ph != "" {
			ph += ", "
		}
		ph += "?"
		args = append(args, string(r))
	}
	return ph, args
}

// consecutiveFailures は opID の release の連続の回数を、試行の行から導く
// （列は持たない。実行・表示が共有する唯一の関数）。
//
// その release の最新の承認の記録より後の試行について、数える結果
// （ExecutionResult.Counts。checks_failed・not_mergeable・merge_failed）を足す。
// 数えない結果と interrupted、終了していない試行は足さず、連続も切らない。
// 数える結果どうしは種類が違っても連続とみなす。
//
// 「最新の承認の記録より後」は、時計ではなく試行の行の順（id）で決める。承認が
// 取り消されて pending へ戻るのは取り消しの結果（ExecutionClassCancel）で試行が
// 閉じたときだけで、承認し直しはその後にしか起きない。したがって最新の承認より
// 後の試行 = 最後の取り消しの試行より後の試行である。承認の日時と試行の日時を
// 比べると、時計が固定・逆行したときに境界が崩れるため比べない。
func consecutiveFailures(ctx context.Context, tx *sql.Tx, opID int64) (int, error) {
	cancelPH, cancelArgs := executionResultsWhere(func(r ExecutionResult) bool {
		c, _ := r.Class()
		return c == ExecutionClassCancel
	})
	countPH, countArgs := executionResultsWhere(ExecutionResult.Counts)
	args := []any{opID}
	args = append(args, countArgs...)
	args = append(args, opID)
	args = append(args, cancelArgs...)
	var n int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM operation_execution
		 WHERE operation_id = ? AND result IN (`+countPH+`)
		   AND id > COALESCE((SELECT MAX(id) FROM operation_execution WHERE operation_id = ? AND result IN (`+cancelPH+`)), 0)`,
		args...).Scan(&n)
	return n, err
}
