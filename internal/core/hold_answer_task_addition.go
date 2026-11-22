package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// このファイルは、足す依頼から立った保留（task_add_over_limit）への回答の動き（選んだ選択肢の識別ごと）を持つ。回答の処理（ExecuteAnswer）の
// 書き込みの中から呼ぶ。足す・足さない・計画を作り直すのどれも、保留を閉じる書き込みと同じトランザクションで足す依頼を閉じる。

// answerDecls は、保存した出力の検査に使う宣言。
type answerDecls struct {
	agent *AgentDeclaration
	conn  *ConnectorsDeclaration
}

// answerDecls は req の宣言を返す。req に無ければ core が作業ディレクトリの宣言を読む（読めなければ nil。検査に通らず、足せない）。
func (s *Store) answerDecls(req AnswerRequest) answerDecls {
	d := answerDecls{agent: req.Agent, conn: req.Conn}
	if d.agent == nil {
		d.agent, _ = LoadAgentDeclaration(s.Workspace())
	}
	if d.conn == nil {
		d.conn, _ = LoadConnectorsDeclaration(s.Workspace())
	}
	return d
}

// awaitingAdditionOfHold は保留 holdID（理由 task_add_over_limit）を指す、オーナー待ちの足す依頼の内部整数 ID を返す。無ければ ok=false。
func awaitingAdditionOfHold(ctx context.Context, tx *sql.Tx, holdID int64) (int64, bool, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT a.id FROM task_addition a JOIN hold h ON h.id = a.hold_id
		WHERE a.hold_id = ? AND a.state = ? AND h.reason = ?`, holdID, string(TaskAdditionAwaitingOwner), string(holdReasonTaskAddOverLimit)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// isTaskAdditionReplanChoice は、選んだ識別が足す依頼の保留への「計画を作り直す」か。読めなければ偽（回答の処理が、そのまま別の誤りとして扱う）。
func isTaskAdditionReplanChoice(ctx context.Context, tx *sql.Tx, holdID int64, chosen *string) bool {
	if chosen == nil || *chosen != taskAddOptionReplan {
		return false
	}
	_, ok, err := awaitingAdditionOfHold(ctx, tx, holdID)
	return err == nil && ok
}

// workingTaskNames は、課題 cid の作業している契約の実行のタスクの題名と ID（`題名 (T-n)` を `, ` 区切り）。
func workingTaskNames(ctx context.Context, tx *sql.Tx, cid int64) string {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT t.id, t.title FROM contract_run r JOIN task t ON t.id = r.task_id
		WHERE r.challenge_id = ? AND r.kind = ? AND ((r.ended_at IS NULL AND r.state = ?)
		OR (r.ended_at IS NULL AND COALESCE(r.land_ref, '') <> '' AND r.land_delivered = 0 AND r.state NOT IN (?, ?, ?))
		OR r.held_success = 1) ORDER BY t.id`,
		cid, contractRunKindTask, string(ContractStateRunning),
		string(ContractStateSucceeded), string(ContractStateFailed), string(ContractStateCancelled))
	if err != nil {
		return ""
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var id int64
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			return ""
		}
		names = append(names, fmt.Sprintf("%s (%s)", EscapeControl(title, false), formatTaskID(id)))
	}
	return strings.Join(names, ", ")
}

// applyTaskAdditionAnswer は、足す依頼の保留への回答で選んだ識別 option の動きを、回答を記録する前の同じ書き込みの中で行う。
// 保留を指すオーナー待ちの足す依頼が無ければ何もしない。
//   - add: 上限を足した後の見込みへ上げ（いまの上限のほうが大きければ変えない）、保存した出力からタスクの行を作り、足す依頼を applied で閉じる。
//     上限が上がったときだけ、承認の記録（種類 budget）と作業ログを残す。保存した出力が検査に通らなければ ErrValidation で断る（何も書かない）。
//   - skip: 足す依頼を declined で閉じる（タスクも上限も変えない）。
//   - replan: 足す依頼を superseded で閉じる（計画の作り直しは回答の処理が行う）。
func applyTaskAdditionAnswer(ctx context.Context, tc *transitionCtx, att Attestation, decls answerDecls, holdID int64, option string) error {
	additionID, ok, err := awaitingAdditionOfHold(ctx, tc.tx, holdID)
	if err != nil || !ok {
		return err
	}
	switch option {
	case taskAddOptionSkip:
		_, err = setTaskAdditionStateTx(ctx, tc.tx, tc.rec, additionID, TaskAdditionDeclined, TaskAdditionUpdate{})
		return err
	case taskAddOptionReplan:
		_, err = setTaskAdditionStateTx(ctx, tc.tx, tc.rec, additionID, TaskAdditionSuperseded, TaskAdditionUpdate{})
		return err
	case taskAddOptionAdd:
		return applyTaskAdditionAdd(ctx, tc, att, decls, additionID)
	}
	return nil
}

// applyTaskAdditionAdd は applyTaskAdditionAnswer の add。検査・タスクの作り方は、上限以下で足す処理（applyJ2ExpandAdd）と同じものを使う。
func applyTaskAdditionAdd(ctx context.Context, tc *transitionCtx, att Attestation, decls answerDecls, additionID int64) error {
	tx, rec, cid := tc.tx, tc.rec, tc.id
	if decls.agent == nil || decls.conn == nil {
		return fmt.Errorf("%w: the agent or connectors declaration cannot be read; cannot add tasks", ErrValidation)
	}
	add, err := loadTaskAddition(ctx, tx, additionID)
	if err != nil {
		return err
	}
	if add.Spec == nil {
		return fmt.Errorf("%w: the task addition has no saved output", ErrValidation)
	}
	st, ready, err := loadJ2ExpandValidationContext(ctx, tx, cid, J2AutoInput{AgentDecl: decls.agent, ConnDecl: decls.conn})
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("%w: the challenge has no approved plan to add tasks to", ErrValidation)
	}
	v, ok := validateJ2ExpandOutput([]byte(`{"verdict":"add","tasks":`+*add.Spec+`}`), st.j2ExpandValidationContext)
	if !ok {
		return fmt.Errorf("%w: the saved output no longer passes validation; choose skip or replan", ErrValidation)
	}
	before, known := sumTaskBudgets(st.Budgets)
	limit, limitOK := effectiveBudgetLimit(before, known, st.LimitRec)
	if !known || !limitOK {
		return fmt.Errorf("%w: the estimate is unknown; cannot add tasks", ErrValidation)
	}
	inputs := j2ExpandTaskInputs(v.Tasks)
	bs := append([]taskBudget(nil), st.Budgets...)
	for _, in := range inputs {
		bs = append(bs, taskBudget{PlanVersion: st.Version, Status: TaskStatusPending, Impl: *in.BudgetImplMicros, Review: *in.BudgetReviewMicros, Known: true})
	}
	after, _ := sumTaskBudgets(bs)
	total := after.Impl + after.Review
	newLimit := limit
	if total > limit {
		newLimit = total
		_, reason, err := challengeBudgetLimit(ctx, tx, cid)
		if err != nil {
			return err
		}
		if err := setChallengeBudgetLimit(ctx, tx, cid, &newLimit, reason); err != nil {
			return err
		}
		if _, err := insertBudgetApproval(ctx, tx, rec, att, cid, int(st.Version), nil); err != nil {
			return err
		}
		approveAfter := map[string]any{
			"approval_kind": string(ApprovalKindBudget), "decision": string(ApprovalDecisionApproved),
			"target_version": st.Version, "limit_usd": microsToUSD(newLimit),
		}
		att.addRelayTo(approveAfter)
		if err := rec.record("challenge", cid, string(OpApprove), map[string]any{"limit_usd": microsToUSD(limit)}, approveAfter); err != nil {
			return err
		}
		if err := rec.record("challenge", cid, ActivityActionBudgetLimit, map[string]any{"limit_usd": microsToUSD(limit)},
			map[string]any{"limit_usd": microsToUSD(newLimit), "verification": string(att.Verification())}); err != nil {
			return err
		}
	}
	_, err = createExpandedTasks(ctx, tx, rec, cid, additionID, st, v, inputs, microsToUSD(total), microsToUSD(newLimit))
	return err
}
