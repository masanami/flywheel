package core

import (
	"context"
	"database/sql"
)

// このファイルは、スーパーバイザーの判断点へ渡す「選べる手の一覧」を作る（区切り S4b-2）。
// 止まり方ごとの表（supervisorHandsByStopKind。閉じた一覧は 1 か所）から、いまの状態で実行できない手を外して返す。
// 回せる条件が無いときに split_followup を外す決まりは、条件をフォローアップへ回すタスクが足す（ここでは外さない）。

// offeredSupervisorHands は保留 t について、判断点へ渡す選べる手の一覧を表の順で返す。読み取りだけ。
func (s *Store) offeredSupervisorHands(ctx context.Context, t supervisorTarget) ([]SupervisorHand, error) {
	var out []SupervisorHand
	err := classifyReadWriteErr(s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = offeredSupervisorHandsTx(ctx, tx, t)
		return err
	}))
	return out, err
}

// offeredSupervisorHandsTx は offeredSupervisorHands の、呼び出し側のトランザクションの中で使える版。
func offeredSupervisorHandsTx(ctx context.Context, tx *sql.Tx, t supervisorTarget) ([]SupervisorHand, error) {
	var from Status
	if err := tx.QueryRowContext(ctx, `SELECT from_status FROM hold WHERE id = ?`, t.HoldID).Scan(&from); err != nil {
		return nil, err
	}
	var out []SupervisorHand
	for _, h := range SupervisorHandsFor(t.StopKind) {
		ok, err := supervisorHandOfferable(ctx, tx, t, from, h)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, h)
		}
	}
	return out, nil
}

// supervisorHandOfferable は、表にある手 h を、いまの状態で選べる手として渡してよいかを返す。
func supervisorHandOfferable(ctx context.Context, tx *sql.Tx, t supervisorTarget, from Status, h SupervisorHand) (bool, error) {
	switch h {
	case HandRetry:
		// 同じタスク（無ければ同じ課題）の同じ止まり方に、結果が executed の retry が 1 件あれば外す（最後のオーナーの回答より後）。
		n, err := countSupervisorActions(ctx, tx, supervisorOwnHandCount(t, HandRetry))
		return n == 0, err
	case HandReplan:
		if !ReplanAllowedFrom(from) {
			return false, nil
		}
		working, err := contractRunWorkingTx(ctx, tx, t.ChallengeID)
		return !working, err
	case HandSendBack:
		return sendBackOfferable(ctx, tx, t, from)
	case HandSplitFollowup:
		return t.TaskID != nil, nil // タスクを指さない保留は、どの条件も回せない
	}
	return true, nil
}

// sendBackOfferable は、send_back を選べる組かを返す。「止まり方 × 保留が指すタスクの最後の契約の実行の形」の表で「できる」のもの
// （開いている、契約のレビュー待ちの実行）だけを残す。委譲の報告の形の実行・終わった実行・実行なしは、いずれもできない。
// 加えて、保留の直前の状態が着手中のときだけ残し、rework_limit は同じタスクに 1 回までにする。checks_failed はいつも外す。
func sendBackOfferable(ctx context.Context, tx *sql.Tx, t supervisorTarget, from Status) (bool, error) {
	switch t.StopKind {
	case StopKindVerifyUncertain, StopKindFactsUnmet, StopKindReworkLimit:
	default:
		return false, nil
	}
	if from != StatusInProgress || t.TaskID == nil {
		return false, nil
	}
	waiting, err := contractReviewWaitingOpenTx(ctx, tx, t.ChallengeID, *t.TaskID)
	if err != nil || !waiting {
		return false, err
	}
	if t.StopKind == StopKindReworkLimit {
		n, err := countSupervisorActions(ctx, tx, supervisorOwnHandCount(t, HandSendBack))
		return n == 0, err
	}
	return true, nil
}

// supervisorOwnHandCount は、保留 t と同じタスク（無ければ同じ課題）・同じ止まり方の、最後のオーナーの回答より後の、
// 結果が executed の手 h の対処の記録を数える条件。
func supervisorOwnHandCount(t supervisorTarget, h SupervisorHand) SupervisorActionCountQuery {
	q := SupervisorActionCountQuery{
		ChallengeID: formatChallengeID(t.ChallengeID), StopKind: t.StopKind, Hands: []SupervisorHand{h},
		ExecutedOnly: true, AfterLastOwnerAnswer: true,
	}
	if t.TaskID != nil {
		q.TaskID = formatTaskID(*t.TaskID)
	}
	return q
}

// supervisorHandDescription は選べる手の一覧に添える、手ごとの 1 行の説明。
func supervisorHandDescription(h SupervisorHand) string {
	switch h {
	case HandReverify:
		return "調べた結果を添えて、検証（受け取り）をやり直す"
	case HandSendBack:
		return "添え書きを付けて、委譲先へ差し戻す"
	case HandRetry:
		return "同じ入力でやり直す"
	case HandSplitFollowup:
		return "残った条件をフォローアップへ回して、ほかの条件で判定する"
	case HandReplan:
		return "計画を作り直す（作り直した計画の承認はオーナー）"
	case HandDefer:
		return "このサイクルでは見送る"
	case HandEscalate:
		return "調べた内容と推奨を付けて、オーナーへ上げる"
	}
	return ""
}
