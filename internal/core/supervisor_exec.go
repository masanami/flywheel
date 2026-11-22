package core

import (
	"context"
	"errors"
	"fmt"
)

// このファイルは手の実行の入口の雛形（区切り S4b-2）。手ごとの関数を手ごとの別のファイル
// （supervisor_hand_<手の名前>.go の 7 つ）に置き、振り分けはここの 1 か所の switch にする。後のタスクは自分の手のファイルだけを書き換える
// （並行のタスクが同じ行を書き換えないための形）。いまは、どの手も ErrSupervisorHandNotImplemented を返すだけ。

// ErrSupervisorHandNotImplemented は、手の実行がまだ無いことを表す（雛形）。
var ErrSupervisorHandNotImplemented = errors.New("core: supervisor hand is not implemented yet")

// supervisorHandInput は手の実行への入力。
type supervisorHandInput struct {
	CycleID     string
	ChallengeID int64
	// TaskID は保留が指すタスク（無ければ nil）。HoldID は対処する保留。
	TaskID *int64
	HoldID int64
	// StopKind は保留の止まり方。RunID はスーパーバイザーの判断の実行（起動できたとき）。
	StopKind StopKind
	RunID    *int64
	// Output は判断点の出力（検査済み）。Decl は宣言。
	Output SVOutput
	Decl   *AgentDeclaration
}

// supervisorHandOutcome は手の実行の結果。Result は対処の記録の結果、Reject は Result が rejected のときの断りの理由。
type supervisorHandOutcome struct {
	Result SupervisorActionResult
	Reject SupervisorRejectReason
}

// executeSupervisorHand は in.Output.Hand に応じた手の関数を呼ぶ（振り分けは、ここの 1 か所の switch）。
func (s *Store) executeSupervisorHand(ctx context.Context, in supervisorHandInput) (supervisorHandOutcome, error) {
	switch in.Output.Hand {
	case HandReverify:
		return s.supervisorHandReverify(ctx, in)
	case HandSendBack:
		return s.supervisorHandSendBack(ctx, in)
	case HandRetry:
		return s.supervisorHandRetry(ctx, in)
	case HandSplitFollowup:
		return s.supervisorHandSplitFollowup(ctx, in)
	case HandReplan:
		return s.supervisorHandReplan(ctx, in)
	case HandDefer:
		return s.supervisorHandDefer(ctx, in)
	case HandEscalate:
		return s.supervisorHandEscalate(ctx, in)
	}
	return supervisorHandOutcome{}, fmt.Errorf("%w: unknown hand %q", ErrValidation, in.Output.Hand)
}
