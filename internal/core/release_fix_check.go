package core

// このファイルは反映の段（release_exec.go）の、衝突の解消の確かめ（S3c）の道を持つ
// （docs/features/core-redesign.md §反映の段・§衝突の解消の確かめ）。
//
//   - 見分け（classifyFixMove）: 見分けの起点（通した中身 fix_verified_ref があればそれ、無ければ承認した中身）からいまの中身への変わり方を、
//     上流の比較の口で core が自分で読んで見分ける（委譲先の申告は使わない。取り込みのほかにコミットが 1 つでも足されていれば衝突の解消の道に入らない）。
//   - 衝突の解消あり: 承認は取り消さず、いまの中身を確かめ待ちの中身（fix_pending_ref）に記録し、反映の試行の結果 waiting_fix_check を残す。
//     課題は完了のまま、commands.resume は呼ばない。確かめ（J5）は別の口が行う。
//   - 確かめが通らなかった中身（fix_rejected_ref）と同じ中身なら、結果 head_changed の道（承認を取り消す）。
//   - J5 の結果の書き込みの口（反映先宛て。writeReflectFix）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// fixMoveOutcome は見分けの結果。
type fixMoveOutcome int

const (
	// fixMoveProceed は、起点と同じ・取り込みだけ（S3a のまま。チェックを確かめて進む）。
	fixMoveProceed fixMoveOutcome = iota
	// fixMoveConflictFix は、衝突の解消あり。
	fixMoveConflictFix
	// fixMoveContentChanged は、関係のない変更あり・判定できない（S3a のまま。承認を取り消す）。
	fixMoveContentChanged
	// fixMoveRejected は、いまの中身が確かめの通らなかった中身と同じ（承認を取り消す）。
	fixMoveRejected
)

type releaseFixMove struct {
	outcome fixMoveOutcome
}

// fixRejectedDetail は、確かめが通らなかった中身で head_changed にした試行の記録の詳細。
func fixRejectedDetail(ref string) string {
	return fmt.Sprintf("the conflict-fix check did not pass for %s", ref)
}

// classifyFixMove は、対応する契約の実行 run があるときの、いまの中身 head の見分け。ストアは書かない。
func (s *Store) classifyFixMove(ctx context.Context, opts ReleaseExecOptions, run *releaseRun, repo, base, approvedHead, head string) releaseFixMove {
	if r := run.row.FixRejectedRef; r != "" && head == r {
		return releaseFixMove{outcome: fixMoveRejected}
	}
	if head == approvedHead {
		return releaseFixMove{outcome: fixMoveProceed}
	}
	origin := approvedHead
	if v := run.row.FixVerifiedRef; v != "" {
		origin = v
	}
	if head == origin {
		return releaseFixMove{outcome: fixMoveProceed}
	}
	switch classifyContentMove(ClassifyHeadChange(ctx, opts.Compare, repo, base, origin, head)) {
	case contentMoveMergeInOnly:
		return releaseFixMove{outcome: fixMoveProceed}
	case contentMoveConflictFix:
		return releaseFixMove{outcome: fixMoveConflictFix}
	}
	return releaseFixMove{outcome: fixMoveContentChanged}
}

// applyReleaseFixWrites は結果を書く書き込み（finishReleaseAttempt）の中で、確かめ待ちの中身の記録（すでにあれば、いまの中身に改める）・
// 衝突の解消ありでなくなったときの確かめ待ちの中身の消去・通らなかった中身の消去を行う。
func applyReleaseFixWrites(ctx context.Context, tx *sql.Tx, j releaseJudgment, written ExecutionResult) error {
	if j.run == nil || !j.run.fixEligible {
		return nil
	}
	id := j.run.row.ID
	if j.clearFixRejected {
		if _, err := tx.ExecContext(ctx, `UPDATE contract_run SET fix_rejected_ref = NULL WHERE id = ? AND ended_at IS NULL`, id); err != nil {
			return err
		}
	}
	if j.clearFixPending {
		// 見分けの結果が衝突の解消ありでない（起点と同じ・取り込みだけ）のに残っている確かめ待ちの中身は、もう確かめる中身ではないので消す。
		// 消すのは判定のときに読んだ値のままのときだけ。判定の間に J5 がその中身を通した中身に書いていたら、判定のときの通した中身へ戻す
		// （この判定は、その起点で見分けた）。
		stale, verified := j.run.row.FixPendingRef, j.run.row.FixVerifiedRef
		if _, err := tx.ExecContext(ctx, `UPDATE contract_run SET fix_pending_ref = NULL WHERE id = ? AND ended_at IS NULL AND fix_pending_ref = ?`, id, stale); err != nil {
			return err
		}
		if stale != verified {
			if _, err := tx.ExecContext(ctx, `UPDATE contract_run SET fix_verified_ref = NULLIF(?, '') WHERE id = ? AND ended_at IS NULL AND fix_verified_ref = ?`, verified, id, stale); err != nil {
				return err
			}
		}
	}
	if j.fixPendingRef != "" && written == ExecutionResultWaitingFixCheck {
		res, err := tx.ExecContext(ctx, `UPDATE contract_run SET fix_pending_ref = ? WHERE id = ? AND ended_at IS NULL`, j.fixPendingRef, id)
		if err := requireOneRow(res, err); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrConflict // 判定の間に契約の実行が終わった
			}
			return err
		}
	}
	return nil
}

// writeReflectFix は反映先宛て（完了の課題の、終わっていない反映の操作に対応する契約の実行）の J5 の確かめの結果の書き込みの口（j5FixWriter）。
// 書く直前の読み直し（j5FixStillCurrent）を通った後の、同じトランザクションの中で呼ばれる。反映の承認・反映の操作・課題の状態・
// 反映の試行の記録は変えない（反映の段だけが書く）。
//   - 満たしたまま: 確かめ待ちだった中身を通した中身（fix_verified_ref）に記録し、確かめ待ちの中身を消す。作業ログ conflict_fix_passed
//     （entity は反映の操作）を 1 件残す。
//   - 問題あり・判断できない・連続の失敗の上限: 確かめ待ちだった中身を通らなかった中身（fix_rejected_ref）に記録し、確かめ待ちの中身を消す。
func (s *Store) writeReflectFix(ctx context.Context, tx *sql.Tx, rec *activityRecorder, r *j5FixResult) error {
	sc := r.Scope
	rowID := sc.Target.RunRowID
	switch r.Verdict {
	case J5VerdictMet:
		res, err := tx.ExecContext(ctx, `UPDATE contract_run SET fix_verified_ref = ?, fix_pending_ref = NULL WHERE id = ? AND fix_pending_ref = ?`,
			sc.PendingRef, rowID, sc.PendingRef)
		if err := requireOneRow(res, err); err != nil {
			return err
		}
		return rec.record("operation", *sc.Target.OperationID, ActivityActionConflictFixPassed,
			map[string]any{"ref": sc.PassedRef},
			map[string]any{"ref": sc.PendingRef, "files": conflictFixFileNames(sc.Change.ConflictFixes), "run_id": r.RunID})
	case J5VerdictNotMet, J5VerdictUncertain:
		res, err := tx.ExecContext(ctx, `UPDATE contract_run SET fix_rejected_ref = ?, fix_pending_ref = NULL WHERE id = ? AND fix_pending_ref = ?`,
			sc.PendingRef, rowID, sc.PendingRef)
		return requireOneRow(res, err)
	}
	return fmt.Errorf("%w: unknown verdict %q", ErrValidation, r.Verdict)
}

// returnedLandRef は、成果物がすでに入っているとき（手順 4）の、いまの中身 head が、対応する契約の実行へ「入れてよい」を付けて返した中身
// （land_ref。届いた記録 land_delivered が真）と同じか。flywheel がチェックと見分けを確かめて許した中身で入ったものは、承認していない中身が
// 入ったとは言わない。委譲先が取り込み後の中身をマージのコミットで入れると、その中身は反映先の祖先になり、比較の口は取り込みだけと言えなくなるため、
// 比べる相手は land_ref にする。land_ref は 1 本の実行に 1 つで、どの操作のために返したかを持たないので、衝突の解消の確かめの道に入ってよい実行
// （fixEligible。反映の依頼か、対応する操作がちょうど 1 つのタスクの実行）に限る。
// 通した中身（fix_verified_ref）は比べる相手にしない。判断点の確かめだけで通したもので、オーナーが承認した中身でも、flywheel がチェックを確かめて
// 「入れてよい」を返した中身でもないため、「入れてよい」を返す前に入っていれば、通した中身（やそこから取り込みだけで動いた中身）でも merged_unapproved になる。
func returnedLandRef(run *releaseRun, head string) bool {
	return run != nil && run.fixEligible && run.row.LandDelivered && run.row.LandRef != "" && head == run.row.LandRef
}
