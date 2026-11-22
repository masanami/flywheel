package core

import (
	"context"
	"errors"
	"fmt"
)

// supervisorHandSendBack は手 send_back の実行。保留を閉じ（共通の resolveSupervisorHold）、課題を着手中に、保留が指すタスクも着手中にする。
// 添え書き（note）は、対処の記録に残る。次の回りの状態の確認で、レビュー待ちへの返しを決める所（pendingReviewReply）が、
// 最後の契約のコマンドより新しい send_back の note を、指摘として委譲先へ 1 回だけ返す。
// 差し戻しの回数には数える（decideRework が、結果が executed の send_back を数える）が、タスクの検証の連続の「満たしていない」の数えは変えない。
//
// 判断の間に、保留が指すタスクの契約のレビュー待ちの実行が終わっていた（開いたレビュー待ちが無い）ときは、手を実行せず、
// 断り（rejected・state_changed）にして保留をオーナーへ上げる。
func (s *Store) supervisorHandSendBack(ctx context.Context, in supervisorHandInput) (supervisorHandOutcome, error) {
	if !supervisorHandAllowed(in.StopKind, HandSendBack) {
		return supervisorHandOutcome{}, fmt.Errorf("%w: %w: send_back is not offered for stop kind %q", ErrValidation, ErrSupervisorHandNotImplemented, in.StopKind)
	}
	if in.TaskID == nil {
		return supervisorHandOutcome{}, fmt.Errorf("%w: send_back needs a task", ErrValidation)
	}
	waiting, err := contractReviewWaitingOpenTx(ctx, in.Tx, in.ChallengeID, *in.TaskID)
	if err != nil {
		return supervisorHandOutcome{}, err
	}
	if !waiting {
		return supervisorHandOutcome{Result: ActionRejected, Reject: RejectStateChanged, EscalateOnReject: true}, nil
	}
	err = s.resolveSupervisorHold(ctx, in, false)
	if errors.Is(err, errSupervisorHoldNotPending) {
		return supervisorHandOutcome{Result: ActionRejected, Reject: RejectStateChanged}, nil
	}
	if err != nil {
		return supervisorHandOutcome{}, err
	}
	actor, err := s.resolveActor()
	if err != nil {
		return supervisorHandOutcome{}, err
	}
	rec := &activityRecorder{tx: in.Tx, at: s.currentTime(), actor: actor, channel: ChannelInvoker, verification: VerificationNone, insert: s.insertActivity, runID: in.RunID}
	if _, err := setTaskStatus(ctx, in.Tx, rec, *in.TaskID, TaskStatusInProgress); err != nil {
		return supervisorHandOutcome{}, err
	}
	return supervisorHandOutcome{Result: ActionExecuted}, nil
}
