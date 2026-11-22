package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// このファイルは手 escalate と、保留をオーナーへ上げる共通の関数（失敗・断りで上げるときにも使う）。

// supervisorEscalation は保留をオーナーへ上げるときに残す値。
type supervisorEscalation struct {
	Findings       string
	Recommendation string
	// ActionID は対処の記録の ID（保留の supervisor_action_id に入れる）。
	ActionID int64
	// FailedQuestion が真なら、問いの先頭へ固定の 1 行（supervisorFailedQuestionHead）を付ける（判断できなかったとき）。
	FailedQuestion bool
	// ReplaceOptions が真なら、保留の選択肢を Options で置き換える。偽なら選択肢に触れない（保留がもともと持つ選択肢を残す）。
	// 識別は core が並びの順に o1・o2… と振る。
	ReplaceOptions bool
	Options        []HoldOption
}

// errSupervisorHoldNotPending は、上げようとした保留がスーパーバイザー待ちでなくなっていたことを表す。
var errSupervisorHoldNotPending = errors.New("core: hold is no longer waiting for the supervisor")

// escalateSupervisorHold は保留 holdID を tx の中でオーナーへ上げる（supervisor_state を escalated にし、調べたことと推奨と対処の記録の ID を残す）。
// 保留は閉じず、課題の状態も変えない（遷移を通らない）。スーパーバイザー待ちでなければ errSupervisorHoldNotPending。
func escalateSupervisorHold(ctx context.Context, tx *sql.Tx, holdID int64, e supervisorEscalation) error {
	var optionsJSON *string
	if e.ReplaceOptions {
		_, raw, err := resolveHoldOptions(e.Options)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrValidation, err)
		}
		optionsJSON = raw
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE hold SET supervisor_state = ?, supervisor_findings = ?, supervisor_recommendation = ?, supervisor_action_id = ?
		 WHERE id = ? AND supervisor_state = ? AND answer IS NULL AND closed_at IS NULL`,
		string(SupervisorStateEscalated), e.Findings, e.Recommendation, e.ActionID, holdID, string(SupervisorStatePending))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return errSupervisorHoldNotPending
	}
	if e.FailedQuestion {
		var q string
		if err := tx.QueryRowContext(ctx, `SELECT question FROM hold WHERE id = ?`, holdID).Scan(&q); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE hold SET question = ? WHERE id = ?`, prefixSupervisorFailedQuestion(q), holdID); err != nil {
			return err
		}
	}
	if e.ReplaceOptions {
		// 選択肢を置き換えた保留は、識別の無い回答を断らない（スーパーバイザーの選択肢は選ぶための見出しで、必須にしない）。
		if _, err := tx.ExecContext(ctx, `UPDATE hold SET options = ?, option_required = 0 WHERE id = ?`, optionsJSON, holdID); err != nil {
			return err
		}
	}
	return nil
}

// supervisorHandEscalate は手 escalate の実行。保留をオーナーへ上げる（findings・recommendation・選択肢を保留に残す）。
func (s *Store) supervisorHandEscalate(ctx context.Context, in supervisorHandInput) (supervisorHandOutcome, error) {
	err := escalateSupervisorHold(ctx, in.Tx, in.HoldID, supervisorEscalation{
		Findings: in.Output.Findings, Recommendation: in.Output.Recommendation, ActionID: in.ActionID,
		ReplaceOptions: len(in.Output.Options) > 0, Options: in.Output.HoldOptions(),
	})
	if errors.Is(err, errSupervisorHoldNotPending) {
		return supervisorHandOutcome{Result: ActionRejected, Reject: RejectStateChanged}, nil
	}
	if err != nil {
		return supervisorHandOutcome{}, err
	}
	return supervisorHandOutcome{Result: ActionExecuted}, nil
}

// recordSupervisorChildEscalate は、子の問い（保留を作る前）にスーパーバイザーが escalate を選んだときの対処の記録（結果 executed）と作業ログを書き、
// 対処の記録の ID を返す。保留はまだ無いので、hold_id は NULL（呼び出し元が、この ID を保留に結び付ける）。
// 判断の間に課題が着手中でなくなっていたら（取り下げなど）、保留を作れないので、同じ書き込みで断りの記録（rejected・state_changed）を残して
// executed を偽で返す（呼び出し元は、保留を作らない）。
func (s *Store) recordSupervisorChildEscalate(ctx context.Context, req supervisorHandRequest) (id int64, executed bool, err error) {
	o := req.Output
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		ch, err := loadChallenge(ctx, tx, req.Target.ChallengeID)
		if err != nil {
			return err
		}
		executed = ch.Status == StatusInProgress
		in := req.actionInput(HandEscalate, o.Findings, o.Note, o.Basis, ActionExecuted, "")
		if !executed {
			in = req.actionInput(HandEscalate, o.Findings, o.Note, o.Basis, ActionRejected, RejectStateChanged)
		}
		in.HoldID = nil
		id, err = s.writeSupervisorAction(ctx, tx, req, in)
		return err
	})
	if err != nil {
		return 0, false, classifyReadWriteErr(err)
	}
	return id, executed, nil
}
