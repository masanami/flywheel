package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// このファイルは手 defer（保留をスーパーバイザー待ちのまま残して見送る）と、見送りが続いたときの上げ方と、
// 手の実行の直前のオーナーの回答の確かめを持つ。

// supervisorHandDefer は手 defer の実行。保留をスーパーバイザー待ちのまま残し、見送ったサイクルと連続の数を書く
// （同じサイクルでは 1 回しか数えない）。保留がスーパーバイザー待ちでなくなっていれば、断る（state_changed）。
func (s *Store) supervisorHandDefer(ctx context.Context, in supervisorHandInput) (supervisorHandOutcome, error) {
	cid, ok := parseCycleID(in.CycleID)
	if !ok {
		return supervisorHandOutcome{}, fmt.Errorf("%w: cycle id %q", ErrValidation, in.CycleID)
	}
	res, err := in.Tx.ExecContext(ctx,
		`UPDATE hold SET deferred_cycle_id = ?, deferred_count = deferred_count + 1
		 WHERE id = ? AND supervisor_state = ? AND answer IS NULL AND closed_at IS NULL
		   AND (deferred_cycle_id IS NULL OR deferred_cycle_id <> ?)`,
		cid, in.HoldID, string(SupervisorStatePending), cid)
	if err != nil {
		return supervisorHandOutcome{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return supervisorHandOutcome{}, err
	}
	if n == 0 {
		return supervisorHandOutcome{Result: ActionRejected, Reject: RejectStateChanged}, nil
	}
	return supervisorHandOutcome{Result: ActionExecuted}, nil
}

// escalateDeferExhausted は、手 defer が supervisorDeferLimit サイクル続いた保留を、判断点を呼ばずにオーナーへ上げる
// （対処の記録 executed・escalate と作業ログを、上げる書き込みと同じトランザクションで残す）。
// すでにスーパーバイザー待ちでなければ何もしない（false を返す）。上げたら true。
func (s *Store) escalateDeferExhausted(ctx context.Context, cycleID string, t supervisorTarget) (bool, error) {
	req := supervisorHandRequest{CycleID: cycleID, Target: t}
	findings := fmt.Sprintf("手 defer が %d サイクル続いたため、判断点を呼ばずにオーナーへ上げた。", supervisorDeferLimit)
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		id, err := s.writeSupervisorAction(ctx, tx, req, req.actionInput(HandEscalate, findings, "", nil, ActionExecuted, ""))
		if err != nil {
			return err
		}
		err = escalateSupervisorHold(ctx, tx, t.HoldID, supervisorEscalation{
			Findings: findings, Recommendation: "保留の問いを確認し、回答してください。", ActionID: id,
		})
		if errors.Is(err, errSupervisorHoldNotPending) {
			return errSupervisorNotExecuted // 書いた対処の記録ごと巻き戻す
		}
		return err
	})
	if errors.Is(err, errSupervisorNotExecuted) {
		return false, nil
	}
	if err = classifyReadWriteErr(err); err != nil {
		return false, err
	}
	return true, nil
}

// supervisorHoldAnsweredByOwner は、保留がオーナー（スーパーバイザー以外）に回答済みか。
func supervisorHoldAnsweredByOwner(ctx context.Context, tx *sql.Tx, holdID int64) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM hold WHERE id = ? AND answer IS NOT NULL AND COALESCE(answered_by, '') <> ?`,
		holdID, supervisorActorName).Scan(&n)
	return n > 0, err
}
