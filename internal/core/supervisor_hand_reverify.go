package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// supervisorHandReverify は手 reverify の実行。保留を閉じ、課題を保留の直前の状態へ戻す（共通の resolveSupervisorHold）。
// verify_uncertain は検証中へ戻り、次の回りの検証の入力（保留の記録の区画）に、回答の文（findings と note）が渡る。
// intake_unverified は着手中へ戻り、オーナーの回答のときと同じに、そのタスクの統合先の受け取りがやり直される。
// artifact_missing は着手中へ戻り、課題に「照合をやり直す」印を付ける（次の回りの委譲の段が、新しい委譲を起動せず、その実行の成果物の照合だけを行う）。
func (s *Store) supervisorHandReverify(ctx context.Context, in supervisorHandInput) (supervisorHandOutcome, error) {
	var causeRun sql.NullInt64
	if in.StopKind == StopKindArtifactMissing {
		// 照合をやり直す実行は、保留の原因の実行。閉じる前に読む。
		err := in.Tx.QueryRowContext(ctx, `SELECT run_id FROM hold WHERE id = ? AND challenge_id = ?`, in.HoldID, in.ChallengeID).Scan(&causeRun)
		if errors.Is(err, sql.ErrNoRows) {
			return supervisorHandOutcome{Result: ActionRejected, Reject: RejectStateChanged}, nil
		}
		if err != nil {
			return supervisorHandOutcome{}, err
		}
		if !causeRun.Valid {
			return supervisorHandOutcome{}, fmt.Errorf("%w: the hold has no cause run to reconcile again", ErrValidation)
		}
	}
	err := s.resolveSupervisorHold(ctx, in, false)
	if errors.Is(err, errSupervisorHoldNotPending) {
		return supervisorHandOutcome{Result: ActionRejected, Reject: RejectStateChanged}, nil
	}
	if err != nil {
		return supervisorHandOutcome{}, err
	}
	if in.StopKind == StopKindArtifactMissing {
		// 前の照合と別の実行なら、前の照合の取得の失敗の数えとブランチは引き継がない。
		if _, err := in.Tx.ExecContext(ctx,
			`UPDATE challenge SET reconcile_fetch_branch = CASE WHEN reconcile_fetch_run_id = ? THEN reconcile_fetch_branch END,
				reconcile_fetch_failures = CASE WHEN reconcile_fetch_run_id = ? THEN reconcile_fetch_failures ELSE 0 END,
				reconcile_fetch_run_id = ?, reconcile_supervisor_action_id = ? WHERE id = ?`,
			causeRun.Int64, causeRun.Int64, causeRun.Int64, in.ActionID, in.ChallengeID); err != nil {
			return supervisorHandOutcome{}, err
		}
	}
	return supervisorHandOutcome{Result: ActionExecuted}, nil
}
