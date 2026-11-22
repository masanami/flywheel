package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// このファイルは、スーパーバイザーの手が、スーパーバイザー待ち（supervisor_state が pending）の保留を閉じる共通の関数（遷移 T20。supervisor_resolve）。
// 保留を閉じる 5 つの手（reverify・send_back・retry・split_followup・replan）が使う。本人確認の登録簿は通さない
// （人の回答 T12 の行・登録簿は変えない）。回答の中身は、人の回答と同じ writeHoldAnswer。

// supervisorAnswerText は、スーパーバイザーが閉じた保留の回答の文を、手の名前・findings・note から組み立てる（「スーパーバイザーが調べた結果」として書く）。
func supervisorAnswerText(hand SupervisorHand, findings, note string) string {
	var b strings.Builder
	b.WriteString("スーパーバイザーが調べた結果（手: ")
	b.WriteString(string(hand))
	b.WriteString("）:\n")
	b.WriteString(strings.TrimSpace(findings))
	if n := strings.TrimSpace(note); n != "" {
		b.WriteString("\n添え書き: ")
		b.WriteString(n)
	}
	return b.String()
}

// isSupervisorAnswered は、保留がスーパーバイザーの手で閉じられた回答か（オーナーの回答ではないか）を返す。
func isSupervisorAnswered(h Hold) bool {
	return h.Answer != nil && h.AnsweredBy != nil && h.AnsweredChannel != nil && h.AnsweredVerification != nil &&
		isSupervisorAnswerColumns(*h.AnsweredBy, *h.AnsweredChannel, *h.AnsweredVerification)
}

// isSupervisorAnswerColumns は、回答の列（回答した者・経路・本人確認）が、スーパーバイザーが閉じた保留のものか（判定の正本）。
func isSupervisorAnswerColumns(by, channel, verification string) bool {
	return by == supervisorActorName && channel == string(ChannelInvoker) && verification == string(VerificationNone)
}

// resolveSupervisorHold は in.HoldID の保留を、in.Tx の中で閉じる（手の書き込みは、対処の記録・作業ログと同じトランザクション）。
// 課題を保留の直前の状態（replan のときは分類済）へ戻し、回答の列（answered_by=supervisor・経路 invoker・本人確認 none）と supervisor_state=handled を書き、
// 作業ログは操作 supervisor_resolve で残す。保留がスーパーバイザー待ちでなくなっていた・課題が人間対応待ちでなくなっていたときは、
// 何も書かず errSupervisorHoldNotPending を返す。
func (s *Store) resolveSupervisorHold(ctx context.Context, in supervisorHandInput, replan bool) error {
	actor, err := s.resolveActor()
	if err != nil {
		return err
	}
	rec := &activityRecorder{tx: in.Tx, at: s.currentTime(), actor: actor, channel: ChannelInvoker, verification: VerificationNone, insert: s.insertActivity, runID: in.RunID}
	actionID := in.ActionID
	w := holdAnswerWrite{
		HoldID: in.HoldID, Answer: supervisorAnswerText(in.Output.Hand, in.Output.Findings, in.Output.Note),
		By: supervisorActorName, Channel: ChannelInvoker, Verification: VerificationNone, Replan: replan, SupervisorActionID: &actionID,
	}
	resolvePreceding := func(ctx context.Context, tx *sql.Tx, cid int64, _ *Challenge) (Status, error) {
		var from string
		var reason sql.NullString
		err := tx.QueryRowContext(ctx,
			`SELECT from_status, reason, task_id FROM hold WHERE id = ? AND challenge_id = ? AND supervisor_state = ? AND answer IS NULL AND closed_at IS NULL`,
			in.HoldID, cid, string(SupervisorStatePending)).Scan(&from, &reason, &w.TaskID)
		if errors.Is(err, sql.ErrNoRows) {
			return "", errSupervisorHoldNotPending
		}
		if err != nil {
			return "", err
		}
		w.Reason = holdReason(reason.String)
		if replan {
			if err := checkReplanAllowed(ctx, tx, cid); err != nil {
				return "", err
			}
		}
		return Status(from), nil
	}
	_, err = s.runTransitionInTx(ctx, in.Tx, rec, in.ChallengeID, runTransitionParams{
		actor: actor, channel: ChannelInvoker, verification: VerificationNone, id: formatChallengeID(in.ChallengeID),
		op: OpSupervisorResolve, preceding: NoStatus, resolvePreceding: resolvePreceding, runID: in.RunID, replan: replan, restricted: true,
	}, func(ctx context.Context, tc *transitionCtx) error {
		if err := writeHoldAnswer(ctx, tc, w); err != nil {
			return err
		}
		tc.before["answer"] = nil
		tc.after["answer"] = w.Answer
		tc.after["hold_id"] = in.HoldID
		tc.after["hand"] = string(in.Output.Hand)
		tc.after["supervisor_action_id"] = in.ActionID
		if replan {
			tc.after["replan"] = true
		}
		return nil
	})
	if errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) {
		return errSupervisorHoldNotPending
	}
	return err
}
