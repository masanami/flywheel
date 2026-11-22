package core

import (
	"context"
	"database/sql"
	"errors"
)

// このファイルは手 escalate と、保留をオーナーへ上げる共通の関数（失敗・断りで上げるときにも使う）。

// supervisorEscalation は保留をオーナーへ上げるときに残す値。
type supervisorEscalation struct {
	Findings       string
	Recommendation string
	// ActionID は対処の記録の ID（保留の supervisor_action_id に入れる）。
	ActionID int64
}

// errSupervisorHoldNotPending は、上げようとした保留がスーパーバイザー待ちでなくなっていたことを表す。
var errSupervisorHoldNotPending = errors.New("core: hold is no longer waiting for the supervisor")

// escalateSupervisorHold は保留 holdID を tx の中でオーナーへ上げる（supervisor_state を escalated にし、調べたことと推奨と対処の記録の ID を残す）。
// 保留は閉じず、課題の状態も変えない（遷移を通らない）。スーパーバイザー待ちでなければ errSupervisorHoldNotPending。
func escalateSupervisorHold(ctx context.Context, tx *sql.Tx, holdID int64, e supervisorEscalation) error {
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
	return nil
}

// supervisorHandEscalate は手 escalate の実行。保留をオーナーへ上げる（findings・recommendation を保留に残す。選択肢の付与は別のタスク）。
func (s *Store) supervisorHandEscalate(ctx context.Context, in supervisorHandInput) (supervisorHandOutcome, error) {
	err := escalateSupervisorHold(ctx, in.Tx, in.HoldID, supervisorEscalation{
		Findings: in.Output.Findings, Recommendation: in.Output.Recommendation, ActionID: in.ActionID,
	})
	if errors.Is(err, errSupervisorHoldNotPending) {
		return supervisorHandOutcome{Result: ActionRejected, Reject: RejectStateChanged}, nil
	}
	if err != nil {
		return supervisorHandOutcome{}, err
	}
	return supervisorHandOutcome{Result: ActionExecuted}, nil
}
