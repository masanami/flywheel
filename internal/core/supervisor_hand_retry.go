package core

import (
	"context"
	"errors"
)

// supervisorHandRetry は手 retry の実行。保留を閉じ、課題を保留の直前の状態へ戻す（オーナーが回答したときと同じ処理。共通の resolveSupervisorHold）。
// 委譲先の実行が終わっていれば、次の回りの委譲の段が同じタスクを起動する。artifact_missing は「照合をやり直す」印を付けない
// （新しい委譲の実行を 1 件起動する）。連続失敗の上限の保留（delegate_stopped・judgment_failed）の「上限 − 1」からの数え直しは、
// 保留が retry で閉じられたことを見る連続失敗の数え（decideLaunch・loadJudgmentLimitHit）が行う。
func (s *Store) supervisorHandRetry(ctx context.Context, in supervisorHandInput) (supervisorHandOutcome, error) {
	err := s.resolveSupervisorHold(ctx, in, false)
	if errors.Is(err, errSupervisorHoldNotPending) {
		return supervisorHandOutcome{Result: ActionRejected, Reject: RejectStateChanged}, nil
	}
	if err != nil {
		return supervisorHandOutcome{}, err
	}
	return supervisorHandOutcome{Result: ActionExecuted}, nil
}
