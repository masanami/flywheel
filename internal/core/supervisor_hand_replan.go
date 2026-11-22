package core

import (
	"context"
	"errors"
	"fmt"
)

// supervisorHandReplan は手 replan の実行。計画を作り直す回答と同じ印（replan）で保留を閉じ（共通の resolveSupervisorHold）、課題は分類済へ戻る。
// 次の回りで計画の判断点（J2）が呼ばれ、入力の「保留の記録」の区画に、回答の文（supervisorAnswerText。findings と note）が渡る。
// 作り直した計画は計画の承認待ちになる。承認はオーナーで、この手は承認の記録を作らない。
//
// 判断の間に、作業している契約の実行（contractRunWorkingTx）ができていたときは、手を実行せず、断り（rejected・state_changed）にして保留をオーナーへ上げる。
func (s *Store) supervisorHandReplan(ctx context.Context, in supervisorHandInput) (supervisorHandOutcome, error) {
	if !supervisorHandAllowed(in.StopKind, HandReplan) {
		return supervisorHandOutcome{}, fmt.Errorf("%w: %w: replan is not offered for stop kind %q", ErrValidation, ErrSupervisorHandNotImplemented, in.StopKind)
	}
	working, err := contractRunWorkingTx(ctx, in.Tx, in.ChallengeID)
	if err != nil {
		return supervisorHandOutcome{}, err
	}
	if working {
		return supervisorHandOutcome{Result: ActionRejected, Reject: RejectStateChanged, EscalateOnReject: true}, nil
	}
	err = s.resolveSupervisorHold(ctx, in, true)
	if errors.Is(err, errSupervisorHoldNotPending) {
		return supervisorHandOutcome{Result: ActionRejected, Reject: RejectStateChanged}, nil
	}
	if err != nil {
		return supervisorHandOutcome{}, err
	}
	return supervisorHandOutcome{Result: ActionExecuted}, nil
}
