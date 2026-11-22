package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// このファイルは手の実行の入口の雛形（区切り S4b-2）。手ごとの関数を手ごとの別のファイル
// （supervisor_hand_<手の名前>.go の 7 つ）に置き、振り分けはここの 1 か所の switch にする。後のタスクは自分の手のファイルだけを書き換える
// （並行のタスクが同じ行を書き換えないための形）。いまは、escalate 以外の手が ErrSupervisorHandNotImplemented を返すだけ。

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
	// Tx は手の実行の書き込みのトランザクション。手の書き込みは、対処の記録・作業ログと同じこのトランザクションの中で行う。
	// ActionID は、同じトランザクションで先に書いた対処の記録の ID（手が保留などへ残す）。
	Tx       *sql.Tx
	ActionID int64
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

// ActivityActionSupervise は作業ログ（entity=challenge）の action。スーパーバイザーの対処 1 回につき 1 行（経路は invoker）。
const ActivityActionSupervise = "supervise"

// errSupervisorNotExecuted は、手を実行せずに書き込みを巻き戻すための内部の印（理由は supervisorHandOutcome か、手の返したエラー）。
var errSupervisorNotExecuted = errors.New("core: supervisor hand not executed")

// supervisorHandRequest は 1 件の対処（判断点の出力 1 つと、それを実行する保留）。
type supervisorHandRequest struct {
	CycleID string
	Target  supervisorTarget
	// RunID は判断の実行（"R-<n>"）。
	RunID  string
	Output SVOutput
	Decl   *AgentDeclaration
}

func (r supervisorHandRequest) actionInput(hand SupervisorHand, findings, note string, basis []SupervisorBasis, result SupervisorActionResult, reject SupervisorRejectReason) SupervisorActionInput {
	in := SupervisorActionInput{
		CycleID: r.CycleID, ChallengeID: formatChallengeID(r.Target.ChallengeID), HoldID: &r.Target.HoldID, RunID: r.RunID,
		StopKind: r.Target.StopKind, Hand: hand, Findings: findings, Note: note, Basis: basis, Result: result, RejectReason: reject,
	}
	if r.Target.TaskID != nil {
		in.TaskID = formatTaskID(*r.Target.TaskID)
	}
	return in
}

// writeSupervisorAction は対処の記録 1 行と、作業ログ 1 行（action supervise、経路 invoker）を、tx の中で書き、対処の記録の ID を返す。
func (s *Store) writeSupervisorAction(ctx context.Context, tx *sql.Tx, req supervisorHandRequest, in SupervisorActionInput) (int64, error) {
	actor, err := s.resolveActor()
	if err != nil {
		return 0, err
	}
	now := s.currentTime()
	id, err := insertSupervisorAction(ctx, tx, now, in)
	if err != nil {
		return 0, err
	}
	var runID *int64
	if in.RunID != "" {
		if n, ok := parseRunID(in.RunID); ok {
			runID = &n
		}
	}
	rec := &activityRecorder{tx: tx, at: now, actor: actor, channel: ChannelInvoker, verification: VerificationNone, insert: s.insertActivity, runID: runID}
	after := map[string]any{"action_id": id, "stop_kind": string(in.StopKind), "result": string(in.Result)}
	if in.Hand != "" {
		after["hand"] = string(in.Hand)
	}
	if in.HoldID != nil {
		after["hold_id"] = *in.HoldID
	}
	if in.RejectReason != "" {
		after["reject_reason"] = string(in.RejectReason)
	}
	return id, rec.record("challenge", req.Target.ChallengeID, ActivityActionSupervise, nil, after)
}

// applySupervisorHand は手を実行する。対処の記録（executed）・作業ログ・手の書き込みを、1 つのトランザクションで行う。
// 手が断った（rejected）とき・手がまだ無い／入力が不正で実行できなかった（failed）ときは、手の書き込みを巻き戻し、
// その結果の対処の記録を別の書き込みで残す（failed は、保留をオーナーへ上げる）。ストアのエラーは、記録せずに返す（保留は待ちのまま）。
// オーナーの回答の直前の確かめ（owner_answered）は別のタスクが足す。この関数の手の書き込みは、待ちでない保留を書き換えない
// （手の書き込みが、対象の保留がスーパーバイザー待ちであることを条件にする）。
func (s *Store) applySupervisorHand(ctx context.Context, req supervisorHandRequest) (supervisorHandOutcome, error) {
	var outcome supervisorHandOutcome
	var handErr error
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		o := req.Output
		actionID, err := s.writeSupervisorAction(ctx, tx, req, req.actionInput(o.Hand, o.Findings, o.Note, o.Basis, ActionExecuted, ""))
		if err != nil {
			return err
		}
		hin := supervisorHandInput{
			CycleID: req.CycleID, ChallengeID: req.Target.ChallengeID, TaskID: req.Target.TaskID, HoldID: req.Target.HoldID,
			StopKind: req.Target.StopKind, Output: o, Decl: req.Decl, Tx: tx, ActionID: actionID,
		}
		if n, ok := parseRunID(req.RunID); ok {
			hin.RunID = &n
		}
		outcome, handErr = s.executeSupervisorHand(ctx, hin)
		if handErr != nil {
			if errors.Is(handErr, ErrSupervisorHandNotImplemented) || errors.Is(handErr, ErrValidation) {
				return errSupervisorNotExecuted
			}
			return handErr // ストアのエラーなど。巻き戻して返す
		}
		if outcome.Result != ActionExecuted {
			return errSupervisorNotExecuted // 書いた対処の記録ごと巻き戻し、別の書き込みで、その結果を残す
		}
		return nil
	})
	if err == nil {
		return outcome, nil
	}
	if !errors.Is(err, errSupervisorNotExecuted) {
		return supervisorHandOutcome{}, classifyReadWriteErr(err)
	}
	if handErr != nil {
		if err := s.recordSupervisorFailure(ctx, req, &req.Output.Hand, fmt.Sprintf("手を実行できなかった: %v", handErr)); err != nil {
			return supervisorHandOutcome{}, err
		}
		return supervisorHandOutcome{Result: ActionFailed}, nil
	}
	switch outcome.Result {
	case ActionRejected:
		o := req.Output
		return outcome, s.recordSupervisorRejected(ctx, req, o.Hand, outcome.Reject)
	case ActionFailed:
		if err := s.recordSupervisorFailure(ctx, req, &req.Output.Hand, "手が失敗とした"); err != nil {
			return supervisorHandOutcome{}, err
		}
	}
	return outcome, nil
}

// recordSupervisorRejected は、手を実行しなかった対処の記録（rejected）と作業ログを書く。
func (s *Store) recordSupervisorRejected(ctx context.Context, req supervisorHandRequest, hand SupervisorHand, reason SupervisorRejectReason) error {
	o := req.Output
	return classifyReadWriteErr(s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := s.writeSupervisorAction(ctx, tx, req, req.actionInput(hand, o.Findings, o.Note, o.Basis, ActionRejected, reason))
		return err
	}))
}

// recordSupervisorFailure は、判断できなかった・出力に不備があった・手を実行できなかったときの対処の記録（failed）と作業ログを書き、
// 保留を、その理由つきでオーナーへ上げる（共通の「上げる」関数を使う）。hand は選ばれた手（判断できなかったときは nil）。
// 保留がすでにスーパーバイザー待ちでなくなっていれば、上げずに、断り（rejected・state_changed）を残す。
func (s *Store) recordSupervisorFailure(ctx context.Context, req supervisorHandRequest, hand *SupervisorHand, reason string) error {
	var h SupervisorHand
	if hand != nil {
		h = *hand
	}
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		id, err := s.writeSupervisorAction(ctx, tx, req, req.actionInput(h, reason, "", nil, ActionFailed, ""))
		if err != nil {
			return err
		}
		err = escalateSupervisorHold(ctx, tx, req.Target.HoldID, supervisorEscalation{
			Findings: reason, Recommendation: "スーパーバイザーでは対処できなかった。オーナーが判断してください。", ActionID: id, FailedQuestion: true,
		})
		if errors.Is(err, errSupervisorHoldNotPending) {
			return errSupervisorNotExecuted
		}
		return err
	})
	if errors.Is(err, errSupervisorNotExecuted) {
		return s.recordSupervisorRejected(ctx, req, h, RejectStateChanged)
	}
	return classifyReadWriteErr(err)
}
