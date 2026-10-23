package core

// このファイルは release の実行の試行の終わり方ごとの書き込みを持つ。実行済みと取り消しは
// それぞれ 1 つのトランザクションの関数（executeReleaseInTx・cancelReleaseInTx）にまとめる。
// S3 が発生の記録を足すときは、この 2 つの関数の中へ足す（同じトランザクションに乗せるため）。
//
// release が approved から pending へ戻る経路は cancelReleaseInTx だけである。連続の回数の導出
// （consecutiveFailures）は、この前提で「最後の取り消しの試行より後」を最新の承認より後とみなす。

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// releaseOutcomeWrite は executeReleaseInTx・cancelReleaseInTx の入力。
type releaseOutcomeWrite struct {
	opID        int64
	challengeID int64
	attemptID   int64
	// version は更新前の不可逆操作の版（楽観ロックの条件）。
	version      int
	result       ExecutionResult
	approvedHead string
	observedHead string
	mergeCommit  string
	detail       string
	trigger      string
	at           time.Time
}

func (w releaseOutcomeWrite) after(extra map[string]any) map[string]any {
	m := map[string]any{
		"result":        string(w.result),
		"approved_head": nullableTextColumn(w.approvedHead),
		"observed_head": nullableTextColumn(w.observedHead),
		"merge_commit":  nullableTextColumn(w.mergeCommit),
		"trigger":       w.trigger,
	}
	if w.detail != "" {
		m["detail"] = w.detail
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// bumpChallengeVersionQuietly は課題の版を 1 上げる（作業ログは残さない。呼び出し側が残す）。
func bumpChallengeVersionQuietly(ctx context.Context, tx *sql.Tx, challengeID int64, nowStr string) (int, error) {
	var cur int
	if err := tx.QueryRowContext(ctx, `SELECT version FROM challenge WHERE id = ?`, challengeID).Scan(&cur); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE challenge SET version = ?, updated_at = ? WHERE id = ? AND version = ?`, cur+1, nowStr, challengeID, cur)
	if err != nil {
		return 0, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return 0, err
	} else if n != 1 {
		return 0, fmt.Errorf("core: op_execute: expected to bump challenge %s version, updated %d rows", formatChallengeID(challengeID), n)
	}
	return cur + 1, nil
}

// updateOperationForExecution は approved の操作を newState へ変え、版を 1 上げる。
func updateOperationForExecution(ctx context.Context, tx *sql.Tx, w releaseOutcomeWrite, newState OperationState, executedAt, mergeCommit any) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE operation SET state = ?, version = ?, executed_at = ?, merge_commit = ? WHERE id = ? AND version = ? AND state = ?`,
		string(newState), w.version+1, executedAt, mergeCommit, w.opID, w.version, string(OperationStateApproved))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return fmt.Errorf("core: op_execute operation %s: expected to update 1 row, updated %d", formatOperationID(w.opID), n)
	}
	return nil
}

// executeReleaseInTx は実行済み（merged・already_merged）の書き込み: 試行を閉じ、状態を executed にし、
// executed_at と merge_commit を記録し、不可逆操作と課題の版をそれぞれ 1 上げ、
// entity=operation の op_execute を残す。課題の状態は変えない。
func executeReleaseInTx(ctx context.Context, tx *sql.Tx, rec *activityRecorder, w releaseOutcomeWrite) error {
	nowStr := formatTimestamp(w.at)
	if err := closeOperationExecution(ctx, tx, w.attemptID, closeOperationExecutionInput{
		EndedAt: w.at, Result: w.result, ObservedHead: w.observedHead, Detail: w.detail}); err != nil {
		return err
	}
	if err := updateOperationForExecution(ctx, tx, w, OperationStateExecuted, nowStr, nullableTextColumn(w.mergeCommit)); err != nil {
		return err
	}
	if _, err := bumpChallengeVersionQuietly(ctx, tx, w.challengeID, nowStr); err != nil {
		return err
	}
	before := map[string]any{"state": string(OperationStateApproved)}
	return rec.record("operation", w.opID, ActivityActionOpExecute, before,
		w.after(map[string]any{"state": string(OperationStateExecuted), "version": w.version + 1}))
}

// cancelReleaseInTx は取り消しの書き込み: 試行を閉じ、approved から pending へ戻し、不可逆操作と課題の版を
// それぞれ 1 上げ、entity=operation と entity=challenge の op_execute を同じトランザクションで残す。
// 承認の行（approval）は消さない。課題の状態は変えない。
func cancelReleaseInTx(ctx context.Context, tx *sql.Tx, rec *activityRecorder, w releaseOutcomeWrite) error {
	nowStr := formatTimestamp(w.at)
	if err := closeOperationExecution(ctx, tx, w.attemptID, closeOperationExecutionInput{
		EndedAt: w.at, Result: w.result, ObservedHead: w.observedHead, Detail: w.detail}); err != nil {
		return err
	}
	if err := updateOperationForExecution(ctx, tx, w, OperationStatePending, nil, nil); err != nil {
		return err
	}
	newChallengeVersion, err := bumpChallengeVersionQuietly(ctx, tx, w.challengeID, nowStr)
	if err != nil {
		return err
	}
	before := map[string]any{"state": string(OperationStateApproved)}
	if err := rec.record("operation", w.opID, ActivityActionOpExecute, before,
		w.after(map[string]any{"state": string(OperationStatePending), "version": w.version + 1})); err != nil {
		return err
	}
	return rec.record("challenge", w.challengeID, ActivityActionOpExecute, nil, map[string]any{
		"version":      newChallengeVersion,
		"operation_id": formatOperationID(w.opID),
		"result":       string(w.result),
	})
}

// finishReleaseAttempt は判定 j の結果で試行を閉じる。待つ結果は試行の行だけを書き（作業ログなし）、
// 数える結果で連続の回数が failure_limit に達したときは limit_reached として取り消す。
func (s *Store) finishReleaseAttempt(ctx context.Context, actor string, opID int64, row *operationRow, attempt *operationExecutionRow, approvedHead string, j releaseJudgment, opts ReleaseExecOptions, now func() time.Time) (*ReleaseExecItem, error) {
	class, ok := j.result.Class()
	if !ok {
		return nil, ErrValidation
	}
	limit := judgmentFailureLimit(opts.Agent)
	item := &ReleaseExecItem{
		OperationID:  formatOperationID(opID),
		ChallengeID:  formatChallengeID(row.ChallengeID),
		ApprovedHead: approvedHead,
	}
	err := s.mutateAs(ctx, actor, ChannelCLI, VerificationNone, func(tx *sql.Tx, rec *activityRecorder) error {
		rec.at = now()
		w := releaseOutcomeWrite{
			opID: opID, challengeID: row.ChallengeID, attemptID: attempt.ID, version: row.Version,
			result: j.result, approvedHead: approvedHead, observedHead: j.observedHead,
			mergeCommit: j.mergeCommit, detail: j.detail, trigger: opts.Trigger, at: rec.at,
		}
		// 状態が変わっていれば（別の経路で取り消された等）、楽観ロックで失敗させる。
		cur, err := loadOperationRow(ctx, tx, opID)
		if err != nil {
			return err
		}
		if cur.Version != row.Version || cur.State != OperationStateApproved {
			return ErrConflict
		}
		if j.result.Counts() {
			prior, err := consecutiveFailures(ctx, tx, opID)
			if err != nil {
				return err
			}
			if prior+1 >= limit {
				w.result = ExecutionResultLimitReached
				w.detail = string(j.result)
				class = ExecutionClassCancel
			}
		}
		switch class {
		case ExecutionClassExecuted:
			if err := executeReleaseInTx(ctx, tx, rec, w); err != nil {
				return err
			}
		case ExecutionClassCancel:
			if err := cancelReleaseInTx(ctx, tx, rec, w); err != nil {
				return err
			}
		default:
			if err := closeOperationExecution(ctx, tx, attempt.ID, closeOperationExecutionInput{
				EndedAt: w.at, Result: w.result, ObservedHead: w.observedHead, Detail: w.detail}); err != nil {
				return err
			}
		}
		item.Result = w.result
		item.ObservedHead = w.observedHead
		if class == ExecutionClassExecuted {
			item.MergeCommit = w.mergeCommit
		}
		n, err := consecutiveFailures(ctx, tx, opID)
		if err != nil {
			return err
		}
		item.ConsecutiveFailures = n
		return nil
	})
	if err != nil {
		return nil, err
	}
	return item, nil
}
