package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// このファイルは課題の取り下げ（T16。docs/features/m4.5-learning-and-self-improvement.md S4。
// M45H13・M45H28・M45H29）を持つ。形は answer と同じく ① 読み取り（PrepareWithdraw）→
// core.Verify → ② 書き込み（ExecuteWithdraw）。遷移の可否は遷移表（T16）だけが決め、
// 取り下げに伴う不可逆操作の差し戻しと保留の閉じ方はここに持つ。

// WithdrawPreview は PrepareWithdraw が返す、取り下げの ① の結果。
type WithdrawPreview struct {
	ChallengeID string
	Title       string
	Status      Status // 取り下げる前の状態
	Reason      string // 入力された理由をそのまま返す（要約の組み立てに使う）
	Version     int    // 表示時点の課題の版（ExecuteWithdraw の ExpectedVersion にそのまま渡す）
	// RejectedOperations は取り下げと同時に差し戻される不可逆操作（pending・approved。ID 昇順）。
	RejectedOperations []IrreversibleOperation
	// ClosedHold は取り下げと同時に回答なしで閉じる開いている保留の問い。無ければ nil。
	ClosedHold *string
}

// WithdrawRequest は ExecuteWithdraw の入力（②）。
type WithdrawRequest struct {
	ChallengeID     string
	ExpectedVersion int
	Reason          string
}

// errWithdrawOpenDelegation は、着手中で終わっていない委譲の run がある課題を取り下げようとしたこと。
// ErrConflict を包み、CLI では conflict で終わる（M45H29）。
func errWithdrawOpenDelegation(challengeID string) error {
	return fmt.Errorf("%w: challenge %s has an unfinished delegation run", ErrConflict, challengeID)
}

// hasOpenDelegationRun は課題 cid に終わっていない委譲の run があるかを返す。
func hasOpenDelegationRun(ctx context.Context, tx *sql.Tx, cid int64) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run WHERE challenge_id = ? AND kind = ? AND ended_at IS NULL`,
		cid, string(RunKindDelegate)).Scan(&n)
	return n > 0, err
}

// hasOpenReleaseAttempt は課題の不可逆操作に、終わっていない実行の試行（`op exec` がマージを依頼している最中）が
// あるかを返す。取り下げで release を rejected にすると、依頼中のマージの結果を記録する経路が働かないため止める。
func hasOpenReleaseAttempt(ctx context.Context, tx *sql.Tx, cid int64) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM operation_execution e JOIN operation o ON o.id = e.operation_id
		 WHERE o.challenge_id = ? AND e.ended_at IS NULL`, cid).Scan(&n)
	return n > 0, err
}

// checkWithdrawable は取り下げの前提（完了・取り下げでない、着手中なら終わっていない委譲の run が無い、
// 終わっていない release の実行の試行が無い）を調べる。
func checkWithdrawable(ctx context.Context, tx *sql.Tx, cid int64, current *Challenge) error {
	if IsTerminal(Table, StatusVocabulary, current.Status) {
		return ErrTerminalState
	}
	if current.Status == StatusInProgress {
		open, err := hasOpenDelegationRun(ctx, tx, cid)
		if err != nil {
			return err
		}
		if open {
			return errWithdrawOpenDelegation(current.ID)
		}
	}
	attempt, err := hasOpenReleaseAttempt(ctx, tx, cid)
	if err != nil {
		return err
	}
	if attempt {
		return fmt.Errorf("%w: challenge %s has a release execution in progress", ErrConflict, current.ID)
	}
	return nil
}

// PrepareWithdraw は withdraw の ①（読み取り）。理由が空なら ErrValidation、課題が無ければ ErrNotFound、
// 完了か取り下げなら ErrTerminalState、着手中で終わっていない委譲の run があれば ErrConflict。
func (s *Store) PrepareWithdraw(ctx context.Context, id string, reason string) (*WithdrawPreview, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, ErrValidation
	}
	cid, ok := parseChallengeID(id)
	if !ok {
		return nil, ErrNotFound
	}
	// 異常終了した委譲の run を先に回収し、残骸が取り下げを止めないようにする（runs と同じ）。
	if err := s.ReapInterruptedRuns(ctx); err != nil {
		return nil, err
	}

	var preview WithdrawPreview
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		current, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		if err := checkWithdrawable(ctx, tx, cid, current); err != nil {
			return err
		}
		ops, err := loadOperations(ctx, tx, cid)
		if err != nil {
			return err
		}
		var rejected []IrreversibleOperation
		for _, op := range ops {
			if op.State == OperationStatePending || op.State == OperationStateApproved {
				rejected = append(rejected, op)
			}
		}
		var closed *string
		if current.Status == StatusAwaitingHuman {
			_, question, _, _, err := loadUnansweredHold(ctx, tx, cid)
			switch {
			case err == nil:
				closed = &question
			case !errors.Is(err, sql.ErrNoRows):
				return err
			}
		}
		preview = WithdrawPreview{
			ChallengeID:        current.ID,
			Title:              current.Title,
			Status:             current.Status,
			Reason:             reason,
			Version:            current.Version,
			RejectedOperations: rejected,
			ClosedHold:         closed,
		}
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return &preview, nil
}

// ExecuteWithdraw は withdraw の ②（書き込み）。確認が成立した（att が有効な）ことと、表示時点の版
// （ExpectedVersion）がまだ最新であることを条件に、遷移（T16）・不可逆操作の差し戻し・開いている保留の
// クローズ・作業ログを 1 つのトランザクションで行う。承認の行（approval）は作らない。
//
// 作業ログ（entity=challenge・action=withdraw）の before は元の状態、after は status: withdrawn・理由・
// last_occurrence_id（取り下げた時点の発生の id の最大。無ければ 0）。--relayed の証跡は after の
// relay_session・relay_quote に入れる。
func (s *Store) ExecuteWithdraw(ctx context.Context, req WithdrawRequest, att Attestation) (*Challenge, error) {
	if strings.TrimSpace(req.Reason) == "" {
		return nil, ErrValidation
	}
	if !attestationPermits(att, req.ChallengeID, actionWithdraw) {
		return nil, ErrVerificationRejected
	}

	apply := func(ctx context.Context, tc *transitionCtx) error {
		if err := checkWithdrawable(ctx, tc.tx, tc.id, tc.current); err != nil {
			return err
		}
		if err := rejectOperationsForWithdrawal(ctx, tc, req.Reason, att); err != nil {
			return err
		}
		// 開いている保留は人間対応待ちの課題にだけある。要約（①）が見せる範囲と揃える。
		if tc.current.Status == StatusAwaitingHuman {
			if _, err := tc.tx.ExecContext(ctx,
				`UPDATE hold SET closed_at = ? WHERE challenge_id = ? AND answer IS NULL AND closed_at IS NULL`,
				tc.nowStr, tc.id); err != nil {
				return err
			}
		}
		var lastOccurrence int64
		if err := tc.tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM occurrence`).Scan(&lastOccurrence); err != nil {
			return err
		}
		tc.after["reason"] = req.Reason
		tc.after["last_occurrence_id"] = lastOccurrence
		att.addRelayTo(tc.after)
		return nil
	}
	return s.verifiedTransition(ctx, att, req.ChallengeID, req.ExpectedVersion, OpWithdraw, NoStatus, nil, apply)
}

// rejectOperationsForWithdrawal は課題の pending・approved の不可逆操作を rejected にし、操作ごとに
// 作業ログ（entity=operation・action=reject）を残す（M45H29。取り下げた後に release が実行される経路を断つ）。
// 承認の行は作らない。
func rejectOperationsForWithdrawal(ctx context.Context, tc *transitionCtx, reason string, att Attestation) error {
	rows, err := tc.tx.QueryContext(ctx,
		`SELECT id, state, version FROM operation WHERE challenge_id = ? AND state IN (?, ?) ORDER BY id ASC`,
		tc.id, string(OperationStatePending), string(OperationStateApproved))
	if err != nil {
		return err
	}
	type opRow struct {
		id      int64
		state   string
		version int
	}
	var ops []opRow
	for rows.Next() {
		var r opRow
		if err := rows.Scan(&r.id, &r.state, &r.version); err != nil {
			_ = rows.Close()
			return err
		}
		ops = append(ops, r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, r := range ops {
		res, err := tc.tx.ExecContext(ctx,
			`UPDATE operation SET state = ?, version = ? WHERE id = ? AND version = ?`,
			string(OperationStateRejected), r.version+1, r.id, r.version)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return fmt.Errorf("core: withdraw reject operation %s: expected to update 1 row, updated %d", formatOperationID(r.id), n)
		}
		after := map[string]any{"state": string(OperationStateRejected), "version": r.version + 1, "reason": reason}
		att.addRelayTo(after)
		if err := tc.rec.record("operation", r.id, string(OpReject), map[string]any{"state": r.state}, after); err != nil {
			return err
		}
	}
	return nil
}
