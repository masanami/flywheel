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
	// loadErr は、宣言を読めなかった理由（どちらも読めたら nil）。検査に通らないのではなく、宣言を読めないことを案内するために持つ。
	loadErr error
}

// answerDecls は req の宣言を返す。req に無ければ core が作業ディレクトリの宣言を読む（読めなければ nil で、理由を loadErr に持つ。足せない）。
func (s *Store) answerDecls(req AnswerRequest) answerDecls {
	d := answerDecls{agent: req.Agent, conn: req.Conn}
	if d.agent == nil {
		var err error
		if d.agent, err = LoadAgentDeclaration(s.Workspace()); err != nil {
			d.agent, d.loadErr = nil, fmt.Errorf("agent declaration: %w", err)
		}
	}
	if d.conn == nil {
		var err error
		if d.conn, err = LoadConnectorsDeclaration(s.Workspace()); err != nil {
			d.conn, d.loadErr = nil, errors.Join(d.loadErr, fmt.Errorf("connectors declaration: %w", err))
		}
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

// taskAdditionEval は、足す依頼を「足す」と答えたときの見込みの計算の結果（回答を受けたときに計算し直した値）。
type taskAdditionEval struct {
	st     j2ExpandState
	v      *j2ExpandValidated
	inputs []TaskInput
	limit  int64
	total  int64
}

// evaluateTaskAddition は足す依頼 additionID の保存した出力を検査し直し、足した後の見込み・いまの上限を計算し直す。
// 検査・見込みの数え方は、上限以下で足す処理（applyJ2ExpandAdd）と同じものを使う。
func evaluateTaskAddition(ctx context.Context, tx *sql.Tx, cid, additionID int64, decls answerDecls) (taskAdditionEval, error) {
	if decls.agent == nil || decls.conn == nil {
		if decls.loadErr != nil {
			return taskAdditionEval{}, fmt.Errorf("%w: the declaration cannot be read; cannot add tasks: %w", ErrValidation, decls.loadErr)
		}
		return taskAdditionEval{}, fmt.Errorf("%w: the agent or connectors declaration is missing; cannot add tasks", ErrValidation)
	}
	add, err := loadTaskAddition(ctx, tx, additionID)
	if err != nil {
		return taskAdditionEval{}, err
	}
	if add.Spec == nil {
		return taskAdditionEval{}, fmt.Errorf("%w: the task addition has no saved output", ErrValidation)
	}
	st, ready, err := loadJ2ExpandValidationContext(ctx, tx, cid, J2AutoInput{AgentDecl: decls.agent, ConnDecl: decls.conn})
	if err != nil {
		return taskAdditionEval{}, err
	}
	if !ready {
		return taskAdditionEval{}, fmt.Errorf("%w: the challenge has no approved plan to add tasks to", ErrValidation)
	}
	if err := st.useAdditionOrigin(ctx, tx, additionID); err != nil {
		return taskAdditionEval{}, err
	}
	v, ok := validateJ2ExpandOutput([]byte(`{"verdict":"add","tasks":`+*add.Spec+`}`), st.j2ExpandValidationContext)
	if !ok {
		return taskAdditionEval{}, fmt.Errorf("%w: the saved output no longer passes validation; choose skip or replan", ErrValidation)
	}
	before, known := sumTaskBudgets(st.Budgets)
	limit, limitOK := effectiveBudgetLimit(before, known, st.LimitRec)
	if !known || !limitOK {
		return taskAdditionEval{}, fmt.Errorf("%w: the estimate is unknown; cannot add tasks", ErrValidation)
	}
	inputs := j2ExpandTaskInputs(v.Tasks)
	bs := append([]taskBudget(nil), st.Budgets...)
	for _, in := range inputs {
		bs = append(bs, taskBudget{PlanVersion: st.Version, Status: TaskStatusPending, Impl: *in.BudgetImplMicros, Review: *in.BudgetReviewMicros, Known: true})
	}
	after, _ := sumTaskBudgets(bs)
	return taskAdditionEval{st: st, v: v, inputs: inputs, limit: limit, total: after.Impl + after.Review}, nil
}

// addOptionDescription は、保留の選択肢から add の説明を返す（無ければ空）。
func addOptionDescription(opts []HoldOption) string {
	for _, o := range opts {
		if o.ID == taskAddOptionAdd {
			return o.Description
		}
	}
	return ""
}

// overLimitReissueEdit は、足す依頼の保留で「足す」の回答を受けたとき、足した後の見込みを計算し直した額が、選択肢 add の説明に書いた額と違うかを調べ、
// 違えば、問いの文と add の説明を計算し直した額に書き換える内容を返す（同じなら nil）。判定は、いまの説明の文と、計算し直した額で作った説明の文の一致で行う
// （額の文を読み取らない）。足す依頼の記録の額・上限は判定に使わない。検査に通らないなど見込みを作れないときは nil（足す処理が、同じ誤りで断る）。
func overLimitReissueEdit(ctx context.Context, tx *sql.Tx, cid, holdID, additionID int64, decls answerDecls) (*HoldTextEdit, error) {
	ev, err := evaluateTaskAddition(ctx, tx, cid, additionID, decls)
	if err != nil {
		if errors.Is(err, ErrValidation) {
			return nil, nil
		}
		return nil, err
	}
	opts, _, err := loadHoldOptionsOf(ctx, tx, holdID)
	if err != nil {
		return nil, err
	}
	totalUSD := microsToUSD(ev.total)
	want := addOptionDescription(overLimitHoldOptions(totalUSD).Options)
	if addOptionDescription(opts) == want {
		return nil, nil
	}
	over := ev.total - ev.limit
	if over < 0 {
		over = 0
	}
	q := overLimitQuestion(ev.v.Tasks, totalUSD, microsToUSD(ev.limit), microsToUSD(over))
	return &HoldTextEdit{Question: &q, Descriptions: map[string]string{taskAddOptionAdd: want}}, nil
}

// reissueOverLimitQuestion は、足す依頼の保留に「足す」で答える回答の前に、同じ書き込みの中で呼ぶ。計算し直した額が選択肢 add の説明の額と違えば、
// 問いの文と add の説明を書き換え、課題の版を 1 つ進めて その保留の ID を返す（保留・足す依頼・上限・タスクは変えない。エラーにしない）。
// 保留が足す依頼の「足す」への回答でない、または額が同じなら何もせず 0 を返す。版の不一致・状態・本人確認の検査は、遷移（transitionInTx）と同じ順。
func (s *Store) reissueOverLimitQuestion(ctx context.Context, tx *sql.Tx, rec *activityRecorder, p runTransitionParams, cid int64, req AnswerRequest) (reissuedHoldID int64, err error) {
	current, _, err := s.transitionGateInTx(ctx, tx, p, cid)
	if err != nil {
		return 0, err
	}
	// 保留を --hold で指した回答は、指した保留を相手にする（ほかの開いている保留を相手にしない）。指さない回答は、足す依頼の保留が課題を止める保留なので、
	// 課題を止める保留に絞って読む（タスクを止める保留が別に開いていても相手にしない）。
	var holdID int64
	var q string
	var args []any
	if n, ok := parseHoldID(req.HoldID); ok {
		q, args = `SELECT id FROM hold WHERE id = ? AND challenge_id = ? AND answer IS NULL AND closed_at IS NULL`, []any{n, cid}
	} else {
		q, args = `SELECT id FROM hold WHERE challenge_id = ? AND answer IS NULL AND closed_at IS NULL AND `+challengeScopedHoldSQL+` ORDER BY id DESC LIMIT 1`, []any{cid}
	}
	if err := tx.QueryRowContext(ctx, q, args...).Scan(&holdID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	var reason sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT reason FROM hold WHERE id = ?`, holdID).Scan(&reason); err != nil {
		return 0, err
	}
	if holdReason(reason.String) != holdReasonTaskAddOverLimit {
		return 0, nil
	}
	additionID, ok, err := awaitingAdditionOfHold(ctx, tx, holdID)
	if err != nil || !ok {
		return 0, err
	}
	edit, err := overLimitReissueEdit(ctx, tx, cid, holdID, additionID, s.answerDecls(req))
	if err != nil || edit == nil {
		return 0, err
	}
	extra := map[string]any{"version": current.Version + 1, "reissued_on_answer": true}
	p.att.addRelayTo(extra)
	changed, err := editHoldText(ctx, tx, rec, cid, holdID, *edit, extra)
	if err != nil || !changed {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE challenge SET version = ?, updated_at = ? WHERE id = ? AND version = ?`,
		current.Version+1, formatTimestamp(rec.at), cid, current.Version)
	if err != nil {
		return 0, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return 0, err
	} else if n != 1 {
		return 0, fmt.Errorf("core: reissue the question of %s: expected to update 1 row, updated %d", current.ID, n)
	}
	return holdID, nil
}

// applyTaskAdditionAdd は applyTaskAdditionAnswer の add。検査・タスクの作り方は、上限以下で足す処理（applyJ2ExpandAdd）と同じものを使う。
// 回答の前の出し直し（reissueOverLimitQuestion）と同じ書き込みの中なので、ここで額が変わっていることはない。
func applyTaskAdditionAdd(ctx context.Context, tc *transitionCtx, att Attestation, decls answerDecls, additionID int64) error {
	tx, rec, cid := tc.tx, tc.rec, tc.id
	ev, err := evaluateTaskAddition(ctx, tx, cid, additionID, decls)
	if err != nil {
		return err
	}
	st, v, inputs, limit, total := ev.st, ev.v, ev.inputs, ev.limit, ev.total
	newLimit := limit
	if total > limit {
		newLimit = total
		if _, err := raiseBudgetLimit(ctx, tx, rec, att, cid, int(st.Version), limit, newLimit); err != nil {
			return err
		}
	}
	_, err = createExpandedTasks(ctx, tx, rec, cid, additionID, st, v, inputs, microsToUSD(total), microsToUSD(newLimit))
	return err
}

// loadReissuedHold は、問いを出し直した課題 cid と、書き換えた後の保留 holdID を返す（出し直したことを表す Reissued つき）。
func loadReissuedHold(ctx context.Context, tx *sql.Tx, cid, holdID int64) (*Challenge, Hold, error) {
	c, err := loadChallenge(ctx, tx, cid)
	if err != nil {
		return nil, Hold{}, err
	}
	var question, fs, raisedAtStr string
	if err := tx.QueryRowContext(ctx, `SELECT question, from_status, raised_at FROM hold WHERE id = ?`, holdID).Scan(&question, &fs, &raisedAtStr); err != nil {
		return nil, Hold{}, err
	}
	raisedAt, err := parseTimestamp(raisedAtStr)
	if err != nil {
		return nil, Hold{}, err
	}
	from := Status(fs)
	opts, required, err := loadHoldOptionsOf(ctx, tx, holdID)
	if err != nil {
		return nil, Hold{}, err
	}
	var kind string
	var runID sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT kind, run_id FROM hold WHERE id = ?`, holdID).Scan(&kind, &runID); err != nil {
		return nil, Hold{}, err
	}
	h := Hold{Kind: HoldKind(kind), Question: question, FromStatus: from, RaisedAt: raisedAt, Options: opts, OptionRequired: required, Reissued: true}
	if runID.Valid {
		v := formatRunID(runID.Int64)
		h.RunID = &v
	}
	return c, h, nil
}
