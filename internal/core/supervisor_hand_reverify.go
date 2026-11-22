package core

import (
	"context"
	"errors"
)

// supervisorHandReverify は手 reverify の実行。保留を閉じ、課題を保留の直前の状態へ戻す（共通の resolveSupervisorHold）。
// verify_uncertain は検証中へ戻り、次の回りの検証の入力（保留の記録の区画）に、回答の文（findings と note）が渡る。
// intake_unverified は着手中へ戻り、オーナーの回答のときと同じに、そのタスクの統合先の受け取りがやり直される。
// artifact_missing の照合のやり直しは、別の手続き（このファイルの範囲外）なので、まだ実行しない。
func (s *Store) supervisorHandReverify(ctx context.Context, in supervisorHandInput) (supervisorHandOutcome, error) {
	if in.StopKind == StopKindArtifactMissing {
		return supervisorHandOutcome{}, ErrSupervisorHandNotImplemented
	}
	err := s.resolveSupervisorHold(ctx, in, false)
	if errors.Is(err, errSupervisorHoldNotPending) {
		return supervisorHandOutcome{Result: ActionRejected, Reject: RejectStateChanged}, nil
	}
	if err != nil {
		return supervisorHandOutcome{}, err
	}
	return supervisorHandOutcome{Result: ActionExecuted}, nil
}
