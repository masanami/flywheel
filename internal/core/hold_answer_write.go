package core

import (
	"context"
	"database/sql"
	"fmt"
)

// このファイルは、保留への回答の中身（本人確認を取らない内部の関数）。人の回答（ExecuteAnswer。T12。本人確認の検査の後）と、
// スーパーバイザーの手が保留を閉じる遷移（supervisor_resolve。T20）の両方が呼ぶ。

// checkReplanAllowed は、作り直しの回答（印 replan）を成立させてよいか、書き込みの中で確かめる。
// 作業している契約の実行があるとき（判定は contractRunWorkingTx）は、成立させない（ErrValidation）。
func checkReplanAllowed(ctx context.Context, tx *sql.Tx, cid int64) error {
	working, err := contractRunWorkingTx(ctx, tx, cid)
	if err != nil {
		return err
	}
	if working {
		return fmt.Errorf("%w: replan is not allowed while a contract run is working", ErrValidation)
	}
	return nil
}

// holdAnswerWrite は writeHoldAnswer へ渡す、回答の列の値と、保留の理由・結び付くタスク。
type holdAnswerWrite struct {
	HoldID       int64
	Answer       string
	By           string
	Channel      Channel
	Verification Verification
	// RelaySession・RelayQuote は対話で受けた回答の手がかり（無ければ nil）。
	RelaySession, RelayQuote any
	Replan                   bool
	Option                   *string
	// Reason は保留の理由、TaskID は保留が結び付くタスク（無ければ Valid=false）。
	Reason holdReason
	TaskID sql.NullInt64
	// SupervisorActionID が非 nil のとき、スーパーバイザーが閉じる保留として書く（supervisor_state を handled にし、対処の記録の ID を残す。
	// 対象の保留がスーパーバイザー待ちであることを、書き込みの条件にする）。nil は、人の回答（supervisor_state を書き換えない）。
	SupervisorActionID *int64
}

// writeHoldAnswer は保留への回答の書き込み（回答の列・理由 facts_unmet の数えの戻し・統合先のタスクの受け取り直し）を tc のトランザクションで行う。
// 本人確認・遷移の可否・作業ログは、呼び出し元が行う。
func writeHoldAnswer(ctx context.Context, tc *transitionCtx, w holdAnswerWrite) error {
	query := `UPDATE hold SET answer = ?, answered_at = ?, answered_by = ?, answered_channel = ?, answered_verification = ?, relay_session = ?, relay_quote = ?, replan = ?, answer_option = ?`
	args := []any{w.Answer, tc.nowStr, w.By, string(w.Channel), string(w.Verification), w.RelaySession, w.RelayQuote, boolToInt(w.Replan), stringPtrColumn(w.Option)}
	where := ` WHERE id = ? AND answer IS NULL AND closed_at IS NULL`
	if w.SupervisorActionID != nil {
		query += `, supervisor_state = ?, supervisor_action_id = ?`
		args = append(args, string(SupervisorStateHandled), *w.SupervisorActionID)
		where += ` AND supervisor_state = ?`
	}
	args = append(args, w.HoldID)
	if w.SupervisorActionID != nil {
		args = append(args, string(SupervisorStatePending))
	}
	res, err := tc.tx.ExecContext(ctx, query+where, args...)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("core: answer hold %d: expected to update 1 row, updated %d", w.HoldID, affected)
	}
	// 理由 facts_unmet の保留への回答は、満たさなかった回数を 0 に戻す（次に読んだレビュー待ちから数え直す）。
	// 戻す対象は、保留が結び付くタスクの契約の実行。保留がタスクに結び付かないときは、その課題の終わっていないタスクの契約の実行。
	if w.Reason == holdReasonFactsUnmet {
		var err error
		if w.TaskID.Valid {
			_, err = tc.tx.ExecContext(ctx, `UPDATE contract_run SET facts_unmet_count = 0 WHERE challenge_id = ? AND task_id = ?`, tc.id, w.TaskID.Int64)
		} else {
			_, err = tc.tx.ExecContext(ctx, `UPDATE contract_run SET facts_unmet_count = 0 WHERE challenge_id = ?
				AND task_id IN (SELECT id FROM task WHERE challenge_id = ? AND status NOT IN ('done', 'cancelled'))`, tc.id, tc.id)
		}
		if err != nil {
			return err
		}
	}
	if w.TaskID.Valid && !w.Replan {
		return resumeIntegrationTaskAfterAnswer(ctx, tc, w.Reason, w.TaskID.Int64)
	}
	return nil
}
