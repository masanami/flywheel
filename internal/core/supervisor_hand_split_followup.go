package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// このファイルは手 split_followup（残った条件をフォローアップへ回して、ほかの条件で判定する）と、その手が使う
// 「回せる条件」の引き方・「フォローアップへ回した条件（判定から外す）」の引き方を持つ。
// 回せるのは、保留の原因の、タスクの検証の判断点の実行の記録済みの出力にある、区分つきの条件（deferred_conditions）だけ。
// 判断点は番号（SVOutput.Conditions）で返し、条件の文は core が番号から引く（判断点が条件を書き換える・作ることを防ぐ）。

// splittableConditions は保留 t の「回せる条件」を、原因の実行の出力の順に、番号（1 から）つきで返す。読み取りだけ。
// 回せるのは、止まり方が verify_uncertain でタスクを指す保留の、原因の（保留を作った）判断点 J5 の実行の出力にある区分つきの条件。
// 原因の実行が無い・読めない・区分つきの条件が無いときは空（nil）。
func splittableConditions(ctx context.Context, tx *sql.Tx, t supervisorTarget) ([]SupervisorCondition, error) {
	if t.StopKind != StopKindVerifyUncertain || t.TaskID == nil {
		return nil, nil
	}
	var output sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT r.output FROM hold h JOIN run r ON r.id = h.run_id
		 WHERE h.id = ? AND r.judgment = ? AND r.challenge_id = ? AND r.task_id = ? AND r.result = ?`,
		t.HoldID, string(judgmentJ5), t.ChallengeID, *t.TaskID, string(RunResultSucceeded)).Scan(&output)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !output.Valid) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v, ok := validateJ5Output([]byte(output.String))
	if !ok {
		return nil, nil
	}
	var out []SupervisorCondition
	for i, d := range v.Deferred {
		out = append(out, SupervisorCondition{Number: i + 1, Text: d.Condition})
	}
	return out, nil
}

// formatSplittableConditions は「回せる条件」の区画の本文（番号つき）。条件が無ければ空。
func formatSplittableConditions(conds []SupervisorCondition) string {
	var b strings.Builder
	for _, c := range conds {
		fmt.Fprintf(&b, "%d. %s\n", c.Number, c.Text)
	}
	return strings.TrimRight(b.String(), "\n")
}

// splitFollowupTitle は条件の文からフォローアップの題名（1 行）を作る。1 行ならそのまま、改行を含むときは空白 1 つにまとめる。
func splitFollowupTitle(text string) string {
	if followupTitleValid(text) {
		return text
	}
	return strings.Join(strings.Fields(text), " ")
}

// supervisorHandSplitFollowup は手 split_followup の実行。conditions の番号を、入力の「回せる条件」から引き（番号が空・回せる条件に無い・
// 重複のときは何も書かずに ErrValidation。呼び出し側が対処の記録を failed にし、保留をオーナーへ上げる）、番号 1 件ごとに、そのタスクに
// ぶら下がるフォローアップ（出どころ supervisor・題名は条件の文）を作り、対処の記録の conditions に回した番号と文を残し、保留を閉じる。
func (s *Store) supervisorHandSplitFollowup(ctx context.Context, in supervisorHandInput) (supervisorHandOutcome, error) {
	if in.TaskID == nil {
		return supervisorHandOutcome{}, fmt.Errorf("%w: split_followup needs a hold that points at a task", ErrValidation)
	}
	if len(in.Output.Conditions) == 0 {
		return supervisorHandOutcome{}, fmt.Errorf("%w: split_followup needs at least one condition number", ErrValidation)
	}
	avail, err := splittableConditions(ctx, in.Tx, supervisorTarget{HoldID: in.HoldID, ChallengeID: in.ChallengeID, TaskID: in.TaskID, StopKind: in.StopKind})
	if err != nil {
		return supervisorHandOutcome{}, err
	}
	picked := make([]SupervisorCondition, 0, len(in.Output.Conditions))
	seen := map[int]bool{}
	for _, n := range in.Output.Conditions {
		if n < 1 || n > len(avail) || seen[n] {
			return supervisorHandOutcome{}, fmt.Errorf("%w: condition number %d is not among the %d splittable conditions", ErrValidation, n, len(avail))
		}
		seen[n] = true
		picked = append(picked, avail[n-1])
	}

	// 保留を閉じる（待ちでなくなっていれば、何も書かずに断る）。
	if err := s.resolveSupervisorHold(ctx, in, false); err != nil {
		if errors.Is(err, errSupervisorHoldNotPending) {
			return supervisorHandOutcome{Result: ActionRejected, Reject: RejectStateChanged}, nil
		}
		return supervisorHandOutcome{}, err
	}
	actor, err := s.resolveActor()
	if err != nil {
		return supervisorHandOutcome{}, err
	}
	rec := &activityRecorder{tx: in.Tx, at: s.currentTime(), actor: actor, channel: ChannelInvoker, verification: VerificationNone, insert: s.insertActivity, runID: in.RunID}
	for _, c := range picked {
		fin := FollowupInput{
			ChallengeID: formatChallengeID(in.ChallengeID), TaskID: formatTaskID(*in.TaskID),
			Title: splitFollowupTitle(c.Text), Source: FollowupSourceSupervisor,
		}
		if fin.Title != c.Text {
			fin.Detail = c.Text
		}
		if _, err := createFollowup(ctx, in.Tx, rec, fin); err != nil {
			return supervisorHandOutcome{}, err
		}
	}
	if err := setSupervisorActionConditions(ctx, in.Tx, in.ActionID, picked); err != nil {
		return supervisorHandOutcome{}, err
	}
	return supervisorHandOutcome{Result: ActionExecuted}, nil
}

// setSupervisorActionConditions は先に書いた対処の記録 actionID の conditions に、回した番号と条件の文を書く。
func setSupervisorActionConditions(ctx context.Context, tx *sql.Tx, actionID int64, conds []SupervisorCondition) error {
	v, err := marshalNullable(conds, len(conds) == 0)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE supervisor_action SET conditions = ? WHERE id = ?`, v, actionID)
	return err
}

// splitConditionsForTask は、課題 cid のタスク tid の、最後のオーナーの回答より後の、結果が executed の split_followup の対処の記録から、
// フォローアップへ回した条件の文を、記録の順（同じ文は 1 回）で返す。次のタスクの検証の入力の「判定から外す」区画の材料。読み取りだけ。
func splitConditionsForTask(ctx context.Context, tx *sql.Tx, cid, tid int64) ([]string, error) {
	where := `WHERE challenge_id = ? AND task_id = ? AND hand = ? AND result = ?`
	args := []any{cid, tid, string(HandSplitFollowup), string(ActionExecuted)}
	if at, err := lastOwnerAnswerAt(ctx, tx, cid); err != nil {
		return nil, err
	} else if at != "" {
		where += ` AND created_at > ?`
		args = append(args, at)
	}
	actions, err := querySupervisorActions(ctx, tx, where, args...)
	if err != nil {
		return nil, err
	}
	var out []string
	seen := map[string]bool{}
	for _, a := range actions {
		for _, c := range a.Conditions {
			if !seen[c.Text] {
				seen[c.Text] = true
				out = append(out, c.Text)
			}
		}
	}
	return out, nil
}
