package core

// このファイルは、区切りを分ける記録（slice_split）の作成と、途中の記録を持つかの読み取りを持つ
// （docs/features/request-to-plan-s3.md）。

import (
	"context"
	"database/sql"
	"errors"
)

// requestSliceSplit は、分類の判断点が区切り 1 つに収まらないと見た課題を分類済へ進める書き込みの中で呼び、対象の課題なら
// 区切りを分ける記録を状態 pending で作って作業ログに残す。対象は、取り込みで作り（出どころ ingest）、取り込み元の子の
// 記録を持たず、要求の記録を指さない課題だけ。対象でない課題・記録をすでに持つ課題では何もしない。
// classifyReason は分類の判断点のその実行の理由。
func requestSliceSplit(ctx context.Context, tc *transitionCtx, classifyReason string) error {
	var target bool
	if err := tc.tx.QueryRowContext(ctx,
		`SELECT COUNT(*) > 0 FROM challenge c WHERE c.id = ? AND c.origin = ? AND c.requirement_id IS NULL
			AND NOT EXISTS (SELECT 1 FROM source_child s WHERE s.challenge_id = c.id)
			AND NOT EXISTS (SELECT 1 FROM slice_split p WHERE p.challenge_id = c.id)`,
		tc.id, string(ChallengeOriginIngest)).Scan(&target); err != nil {
		return err
	}
	if !target {
		return nil
	}
	if _, err := tc.tx.ExecContext(ctx,
		`INSERT INTO slice_split (challenge_id, state, classify_reason, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		tc.id, string(SliceSplitStatePending), classifyReason, tc.nowStr, tc.nowStr); err != nil {
		return err
	}
	return tc.rec.record("challenge", tc.id, ActivityActionSliceSplitRequested, nil,
		map[string]any{"state": string(SliceSplitStatePending)})
}

// sliceSplitState は課題 cid の区切りを分ける記録の状態と classify_reason を返す。記録が無ければ ok=false。
func sliceSplitState(ctx context.Context, tx *sql.Tx, cid int64) (state SliceSplitState, classifyReason string, ok bool, err error) {
	var st string
	err = tx.QueryRowContext(ctx, `SELECT state, classify_reason FROM slice_split WHERE challenge_id = ?`, cid).Scan(&st, &classifyReason)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return SliceSplitState(st), classifyReason, true, nil
}

// sliceSplitBlocksPlanning は課題 cid が途中の状態（pending・awaiting_owner）の区切りを分ける記録を持つか。
func sliceSplitBlocksPlanning(ctx context.Context, tx *sql.Tx, cid int64) (bool, error) {
	state, _, ok, err := sliceSplitState(ctx, tx, cid)
	if err != nil || !ok {
		return false, err
	}
	return state == SliceSplitStatePending || state == SliceSplitStateAwaitingOwner, nil
}

// sliceSplitIsPending は課題 id（"C-<n>"）の区切りを分ける記録が pending かを読む。
func (s *Store) sliceSplitIsPending(ctx context.Context, id string) (bool, error) {
	cid, ok := parseChallengeID(id)
	if !ok {
		return false, ErrNotFound
	}
	var pending bool
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		state, _, found, err := sliceSplitState(ctx, tx, cid)
		pending = found && state == SliceSplitStatePending
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return false, err
	}
	return pending, nil
}
