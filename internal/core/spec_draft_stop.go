package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// このファイルは、下書きづくりを途中でやめる 2 つの道を持つ: オーナーが課題に仕様の文書の場所を付けたとき（EditChallenge が呼ぶ）と、
// 課題が取り下げになったとき。

// specDraftSkippedByEdit は作業ログ spec_draft_skipped の細目のうち、仕様の文書の場所を付けたことを表す値。
const specDraftSkippedByEdit = "edit"

// cancelSpecDraftRunsBeforeEdit は、課題 cid の記録が途中の状態なら、終わっていない下書きづくりの実行を取り消す。仕様の文書の場所を
// 付ける編集が、何かを書く前に呼ぶ。取り消せなければ ErrConflict。課題が無い・終わっているときは何もしない（書き込みの側が断る）。
func (s *Store) cancelSpecDraftRunsBeforeEdit(ctx context.Context, cid int64, open func() (ContractCancelInput, error)) error {
	blocked := false
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		c, err := loadChallenge(ctx, tx, cid)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil || IsTerminal(Table, StatusVocabulary, c.Status) {
			return err
		}
		blocked, err = specDraftBlocksPlanning(ctx, tx, cid)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil || !blocked {
		return err
	}
	// 異常終了した実行の記録を先に回収し、残骸が編集を止めないようにする（取り下げと同じ）。
	if err := s.ReapInterruptedRuns(ctx); err != nil {
		return err
	}
	rows, err := s.countOpenContractRuns(ctx, cid, contractRunKindSpec)
	if err != nil || rows == 0 {
		return err
	}
	if open == nil {
		return fmt.Errorf("%w: the spec draft run of %s cannot be cancelled here", ErrConflict, formatChallengeID(cid))
	}
	in, err := open()
	if err != nil {
		return err
	}
	// 作業ディレクトリは、開始のときに記録した値を使う。
	in.ChallengeID, in.WorkDir = formatChallengeID(cid), ""
	_, err = s.CancelOpenSpecDraftRuns(ctx, in)
	if err == nil || errors.Is(err, ErrConflict) || errors.Is(err, ErrStoreError) {
		return err
	}
	// 元のエラーは種類を引き継がない（取り消せなかったことは、どれもコンフリクトで断る）。
	return fmt.Errorf("%w: the spec draft run of %s could not be cancelled: %s", ErrConflict, formatChallengeID(cid), err.Error())
}

// requireNoOpenSpecDraftRun は、課題 cid に終わっていない下書きづくりの実行（接続ツール側の実行か、開始などのコマンドを呼んでいる
// 最中の実行の記録）があれば ErrConflict を返す。取り消した後に始まった実行を残して、記録を省かないようにする。
func requireNoOpenSpecDraftRun(ctx context.Context, tx *sql.Tx, cid int64) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM contract_run WHERE challenge_id = ? AND kind = ? AND ended_at IS NULL)
		+ (SELECT COUNT(*) FROM run WHERE challenge_id = ? AND budget_bucket = ? AND result IS NULL)`,
		cid, contractRunKindSpec, cid, string(budgetBucketSpec)).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: %s has a spec draft run that is not finished", ErrConflict, formatChallengeID(cid))
	}
	return nil
}

// skipSpecDraftByEdit は、仕様の文書の場所を付ける編集の書き込みの中で、途中の状態の記録 d を skipped にする。開いている
// 下書きづくりの保留があれば、回答なしで閉じて、課題を分類済へ戻す。
func (s *Store) skipSpecDraftByEdit(ctx context.Context, tx *sql.Tx, rec *activityRecorder, ch Channel, cid int64, d *SpecDraft) error {
	if d.HoldID != nil {
		holdID, ok := parseHoldID(*d.HoldID)
		if !ok {
			return fmt.Errorf("core: malformed hold id %q", *d.HoldID)
		}
		p := runTransitionParams{actor: rec.actor, channel: ch, verification: VerificationNone, id: formatChallengeID(cid),
			op: OpSpecDraftUnhold, preceding: NoStatus, restricted: true}
		if _, err := s.transitionInTx(ctx, tx, rec, p, cid, func(ctx context.Context, tc *transitionCtx) error {
			tc.after["closed_holds"] = []string{*d.HoldID}
			if err := requireOneRow(tc.tx.ExecContext(ctx,
				`UPDATE hold SET closed_at = ? WHERE id = ? AND answer IS NULL AND closed_at IS NULL`, tc.nowStr, holdID)); err != nil {
				return err
			}
			// 下書きづくりの保留には、ほかの保留は重ならない。残っていれば、開いた保留を持つ分類済の課題を作らずに断る。
			open, err := countOpenHolds(ctx, tc.tx, cid)
			if err == nil && open > 0 {
				err = fmt.Errorf("%w: %s has another open hold", ErrConflict, formatChallengeID(cid))
			}
			return err
		}); err != nil {
			return err
		}
	}
	if err := updateSpecDraft(ctx, tx, cid, specDraftUpdate{State: SpecDraftStateSkipped, Attempts: d.Attempts,
		Questions: d.Questions, Answers: d.Answers}, rec.at); err != nil {
		return err
	}
	return rec.record("challenge", cid, ActivityActionSpecDraftSkipped, nil, map[string]any{"detail": specDraftSkippedByEdit})
}

// discardWithdrawnSpecDraft は、取り下げた課題 cid の記録が途中の状態なら、記録の下書きのファイルを消す（記録は変えない）。
// 途中の状態の課題は仕様の文書の場所を持たないので、終わった課題の後始末（cleanupSpecDraft）はこのファイルを消さない。
// 取り下げの書き込みが成立した後に呼ぶ。
func (s *Store) discardWithdrawnSpecDraft(ctx context.Context, cid int64, actor string) {
	ctx = context.WithoutCancel(ctx)
	blocked := false
	err := s.db.Read(ctx, func(tx *sql.Tx) (err error) {
		blocked, err = specDraftBlocksPlanning(ctx, tx, cid)
		return err
	})
	// 読めないのはストアの障害で、作業ログも書けないので、何もしない。
	if err == nil && blocked {
		s.discardSpecDraft(ctx, cid, actor)
	}
}
