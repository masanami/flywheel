package core

// このファイルは、衝突の解消の確かめ（J5）の結果を、統合先宛て（着手中の課題の、検証中のタスクの実行）へ書く口を持つ
// （docs/features/core-redesign.md §衝突の解消の確かめ）。書く直前の読み直し（j5FixStillCurrent）を通った後の、同じトランザクションの中で呼ばれる。
//
//   - 満たしたまま: 検証した中身（verified_ref）を確かめ待ちだった中身に改め、確かめ待ちの中身を消す。統合先宛てでは fix_verified_ref を使わない。
//     作業ログに conflict_fix_passed を 1 件残す。次の状態の確認で、チェックが通っていれば、確かめを通した中身を付けた「入れてよい」が返る。
//   - 問題あり: タスクを着手中へ戻し（J5 の指摘は、次の状態の確認が respond で委譲先へ返す）、検証した中身と確かめ待ちの中身を消す。
//   - 判断できない・連続の失敗の上限: 両方を消す。タスクは検証中のままで、課題は保留にしない。次の J5 でタスクの検証の対象になる。
//
// 満たしたまま以外では conflict_fix_passed を残さない。

import (
	"context"
	"database/sql"
	"fmt"
)

// writeIntegrationFix は統合先宛ての書き込みの口（j5FixWriter）。
func (s *Store) writeIntegrationFix(ctx context.Context, tx *sql.Tx, rec *activityRecorder, r *j5FixResult) error {
	sc := r.Scope
	rowID := sc.Target.RunRowID
	switch r.Verdict {
	case J5VerdictMet:
		res, err := tx.ExecContext(ctx, `UPDATE contract_run SET verified_ref = ?, fix_pending_ref = NULL WHERE id = ? AND fix_pending_ref = ?`,
			sc.PendingRef, rowID, sc.PendingRef)
		if err := requireOneRow(res, err); err != nil {
			return err
		}
		return rec.record("task", *sc.Target.TaskID, ActivityActionConflictFixPassed,
			map[string]any{"ref": sc.PassedRef},
			map[string]any{"ref": sc.PendingRef, "files": conflictFixFileNames(sc.Change.ConflictFixes), "run_id": r.RunID})
	case J5VerdictNotMet:
		if err := clearIntegrationFixRefs(ctx, tx, rowID); err != nil {
			return err
		}
		tid := *sc.Target.TaskID
		if _, err := setTaskStatus(ctx, tx, rec, tid, TaskStatusInProgress); err != nil {
			return err
		}
		// S3b のタスクの検証の「満たしていない」と同じ記録（作業ログ task_verify と発生 verification_not_met）。
		// J5 の指摘（r.Feedback）は、確かめの run の出力に残り、次の状態の確認が読んで respond で委譲先へ返す（contract_delegate_review.go）。
		if err := rec.record("task", tid, ActivityActionTaskVerify, nil, map[string]any{
			"verdict": string(r.Verdict), "reason": r.Reason, "challenge": formatChallengeID(sc.Target.ChallengeID), "content_ref": sc.PendingRef,
		}); err != nil {
			return err
		}
		runInt, ok := parseRunID(r.RunID)
		if !ok {
			return ErrValidation
		}
		return s.insertRunOccurrence(ctx, tx, runInt, OccurrenceKindVerificationNotMet, OccurrenceOutcomeBad, rec.at)
	case J5VerdictUncertain:
		return clearIntegrationFixRefs(ctx, tx, rowID)
	}
	return fmt.Errorf("%w: unknown verdict %q", ErrValidation, r.Verdict)
}

// clearIntegrationFixRefs は検証した中身・確かめ待ちの中身・通す中身を消す（検証した中身が無いのに通す中身を残さない）。
func clearIntegrationFixRefs(ctx context.Context, tx *sql.Tx, rowID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE contract_run SET verified_ref = NULL, fix_pending_ref = NULL, land_ref = NULL, land_delivered = 0 WHERE id = ?`, rowID)
	return err
}

// conflictFixFileNames は衝突を解消するために直したファイルの名前を、現れた順に重複なく返す（無ければ空のスライス。nil でない）。
func conflictFixFileNames(fixes []ConflictFix) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, fx := range fixes {
		for _, f := range fx.Files {
			if !seen[f.Filename] {
				seen[f.Filename] = true
				out = append(out, f.Filename)
			}
		}
	}
	return out
}

// defaultFixWriters は本番の Store が持つ、衝突の解消の確かめ（J5）の書き込みの口。ここだけで組み立てる（開く道が複数でも口がそろう）。
// 反映先宛ての口は release_fix_check.go の writeReflectFix。
func (s *Store) defaultFixWriters() j5FixWriters {
	return j5FixWriters{Integration: s.writeIntegrationFix, Reflect: s.writeReflectFix}
}
