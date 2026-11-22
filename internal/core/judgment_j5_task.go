package core

// このファイルは、タスクの検証（統合先を使う着手中の課題の、検証中のタスク 1 個を対象にする J5）を持つ。
//
//   - 対象: 課題が着手中・統合先を使う・タスクが検証中で、タスクの契約の実行（最後の 1 件）の中身の識別（content_ref）が、
//     検証した中身（verified_ref）と違う（か、verified_ref が無い）もの。課題とタスクの組で選ぶ。保留中の課題のタスクは選ばない。
//   - 入力: そのタスクの委譲の実行のうち、成果物を持つ最後の実行の成果物（ほかのタスクの成果物は入れない）と、子に対応するタスクの「対応する取り込み元」
//     （取り込みのときに記録した子の題名・URL・本文。上流は読み直さない）。
//   - 結果: 課題の遷移（T8〜T10）を使わずに書く。書く直前に、課題が着手中・タスクが検証中・契約の実行の中身の識別が検証を
//     始めたときと同じことを確かめ、違えば捨てる（何も書かない。次の J5 がやり直す）。
//     満たした→契約の実行の verified_ref に中身の識別を記録するだけ（タスクは検証中のまま・課題は着手中のまま。承認は求めず、
//     反映の操作・課題の実物の記録は作らない）。満たしていない→タスクを着手中へ戻す（次の状態の確認が、契約の実行へ respond を
//     返す）。判断できない→課題を保留にし、保留をそのタスクに結び付ける。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// labelTaskSourceSection は、タスクが対応する取り込み元（子）の区画のラベル（指示文はこの名前で区画を指す）。
const labelTaskSourceSection = "対応する取り込み元"

// labelSplitConditionsSection は、スーパーバイザーがフォローアップへ回した条件の区画のラベル（指示文はこの名前で区画を指す）。
const labelSplitConditionsSection = "フォローアップへ回した条件（判定から外す）"

// formatSplitConditionsSection は区画「フォローアップへ回した条件（判定から外す）」の本文（条件の文を 1 行ずつ）。
func formatSplitConditionsSection(conds []string) string {
	var b strings.Builder
	for _, c := range conds {
		b.WriteString("- " + c + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// j5TaskTarget はタスクの検証の対象（課題とタスクの組）。
type j5TaskTarget struct {
	ChallengeID int64
	TaskID      int64
}

// j5TaskScope は 1 回のタスクの検証が、始めたときに読んだ対象（書く直前に照合する）。
type j5TaskScope struct {
	Task Task
	// ContractRunRowID はタスクの契約の実行（最後の 1 件）の行の ID。ContentRef は、検証を始めたときのその中身の識別。
	ContractRunRowID int64
	ContentRef       string
	// Source はタスクが対応する取り込み元の子（記録した値。対応しなければ nil）。
	Source *SourceChild
}

// latestTaskContract はタスクの契約の実行（kind が task の最後の 1 件）の、行の ID・中身の識別・検証した中身。無ければ ok=false。
// 確かめ待ちの中身（fix_pending_ref）を持つ実行は、タスクの検証の対象にしない（コンフリクトの解消の確かめが扱う）ので、ok を偽にする。
func latestTaskContract(ctx context.Context, tx *sql.Tx, cid, taskID int64) (rowID int64, contentRef, verifiedRef sql.NullString, ok bool, err error) {
	var pending string
	err = tx.QueryRowContext(ctx,
		`SELECT id, content_ref, verified_ref, COALESCE(fix_pending_ref, '') FROM contract_run WHERE challenge_id = ? AND task_id = ? AND kind = 'task' ORDER BY id DESC LIMIT 1`,
		cid, taskID).Scan(&rowID, &contentRef, &verifiedRef, &pending)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, contentRef, verifiedRef, false, nil
	}
	return rowID, contentRef, verifiedRef, err == nil && pending == "", err
}

// needsTaskVerification は、タスクの契約の実行の中身の識別が、検証した中身と違う（か、検証した中身が無い）か。
// 中身の識別が無い（空）ときは、検証できる中身が無いので偽。
func needsTaskVerification(contentRef, verifiedRef sql.NullString) bool {
	return contentRef.Valid && contentRef.String != "" && (!verifiedRef.Valid || verifiedRef.String != contentRef.String)
}

// selectJ5TaskTargets は、検証するタスクの組を、課題の ID の昇順（課題の中ではタスクの ID の昇順）で返す。
func selectJ5TaskTargets(ctx context.Context, tx *sql.Tx) ([]j5TaskTarget, error) {
	challenges, err := loadChallengesByStatusSorted(ctx, tx, StatusInProgress)
	if err != nil {
		return nil, err
	}
	var out []j5TaskTarget
	for _, ch := range challenges {
		ts, err := selectJ5TaskTargetsOf(ctx, tx, ch)
		if err != nil {
			return nil, err
		}
		out = append(out, ts...)
	}
	return out, nil
}

// selectJ5TaskTargetsOf は課題 ch（着手中でなければ対象なし）の、検証するタスクの組を返す。統合先を使わない課題は対象にしない。
// 終わっていない実行を持つタスク（タスクの検証・契約のコマンドの実行中）は、今は選ばない。
func selectJ5TaskTargetsOf(ctx context.Context, tx *sql.Tx, ch Challenge) ([]j5TaskTarget, error) {
	cid, ok := parseChallengeID(ch.ID)
	if !ok || ch.Status != StatusInProgress {
		return nil, nil
	}
	integ, err := challengeUsesIntegration(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	if integ == nil || !*integ {
		return nil, nil
	}
	tasks, err := listTasks(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	var out []j5TaskTarget
	for i := range tasks {
		if tasks[i].Status != TaskStatusVerifying {
			continue
		}
		tid := taskIntID(&tasks[i])
		_, contentRef, verifiedRef, found, err := latestTaskContract(ctx, tx, cid, tid)
		if err != nil {
			return nil, err
		}
		if !found || !needsTaskVerification(contentRef, verifiedRef) {
			continue
		}
		active, err := taskHasActiveRun(ctx, tx, cid, tid)
		if err != nil {
			return nil, err
		}
		if !active {
			if active, err = taskLessHasActiveRun(ctx, tx, cid); err != nil {
				return nil, err
			}
		}
		if active {
			continue
		}
		out = append(out, j5TaskTarget{ChallengeID: cid, TaskID: tid})
	}
	return out, nil
}

// loadJ5TaskScope はタスク taskID の検証の対象を読む。課題が着手中・統合先を使う・タスクが検証中・中身の識別が検証した中身と
// 違う、のどれかを満たさなければ nil。
func loadJ5TaskScope(ctx context.Context, tx *sql.Tx, cid, taskID int64) (*j5TaskScope, error) {
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM challenge WHERE id = ?`, cid).Scan(&status); err != nil {
		return nil, err
	}
	integ, err := challengeUsesIntegration(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	if Status(status) != StatusInProgress || integ == nil || !*integ {
		return nil, nil
	}
	task, err := loadTask(ctx, tx, taskID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if task == nil || task.ChallengeID != formatChallengeID(cid) || task.Status != TaskStatusVerifying {
		return nil, nil
	}
	rowID, contentRef, verifiedRef, found, err := latestTaskContract(ctx, tx, cid, taskID)
	if err != nil {
		return nil, err
	}
	if !found || !needsTaskVerification(contentRef, verifiedRef) {
		return nil, nil
	}
	scope := &j5TaskScope{Task: *task, ContractRunRowID: rowID, ContentRef: contentRef.String}
	if task.SourceKey != "" {
		children, err := loadSourceChildren(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		for i := range children {
			if children[i].ExternalKey == task.SourceKey {
				scope.Source = &children[i]
			}
		}
	}
	return scope, nil
}

// formatTaskSourceSection は区画「対応する取り込み元」の本文（取り込みのときに記録した子の題名・URL・本文）。
func formatTaskSourceSection(c SourceChild) string {
	return fmt.Sprintf("題名: %s\n\nURL: %s\n\n本文:\n%s", c.Title, c.URL, c.Body)
}

// j5TaskStillCurrent は、タスクの検証の結果を書く直前の読み直し（書き込みのトランザクションの中）。課題が着手中・タスクが
// 検証中・タスクの契約の実行（最後の 1 件）の行と中身の識別が検証を始めたときと同じなら真。
func j5TaskStillCurrent(ctx context.Context, tx *sql.Tx, cid int64, scope *j5TaskScope) (bool, error) {
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM challenge WHERE id = ?`, cid).Scan(&status); err != nil {
		return false, err
	}
	if Status(status) != StatusInProgress {
		return false, nil
	}
	tid := taskIntID(&scope.Task)
	var tstatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM task WHERE id = ?`, tid).Scan(&tstatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if TaskStatus(tstatus) != TaskStatusVerifying {
		return false, nil
	}
	rowID, contentRef, _, found, err := latestTaskContract(ctx, tx, cid, tid)
	if err != nil {
		return false, err
	}
	return found && rowID == scope.ContractRunRowID && contentRef.Valid && contentRef.String == scope.ContentRef, nil
}

// finalizeJ5TaskOutput は run が succeeded のときに、タスクの検証の出力を検査し直して書く。検査に落ちたら run の結果を
// invalid_output にし、何も書かない。書かなかった（直前の読み直しで対象が変わっていた）ときは status が nil。
// status は書いた後の課題の状態（満たした・満たしていないは着手中のまま。判断できない・差し戻しの上限に達したは保留）。
func (s *Store) finalizeJ5TaskOutput(ctx context.Context, runIDDisplay, challengeIDDisplay string, structured []byte, scope *j5TaskScope, vh *j5VerifiedHeads, agent *AgentDeclaration) (outcome string, status *string, result RunResult, err error) {
	v, ok := validateJ5Output(structured)
	if !ok {
		if err := s.invalidateRunOutput(ctx, runIDDisplay); err != nil {
			return "", nil, "", err
		}
		return "", nil, RunResultInvalidOutput, nil
	}
	mapped, held, err := s.applyJ5Task(ctx, runIDDisplay, challengeIDDisplay, v, scope, vh, agent)
	if err != nil {
		return "", nil, "", err
	}
	outcome = string(v.Verdict)
	if !mapped {
		return outcome, nil, RunResultSucceeded, nil
	}
	st := string(StatusInProgress)
	if v.Verdict == J5VerdictUncertain || held {
		st = string(StatusAwaitingHuman)
	}
	return outcome, &st, RunResultSucceeded, nil
}

// applyJ5Task はタスクの検証の判定を、課題の遷移（T8〜T10）を使わずに書く。書く直前に j5TaskStillCurrent で読み直し、
// 違えば何も書かずに mapped=false を返す。
func (s *Store) applyJ5Task(ctx context.Context, runIDDisplay, challengeIDDisplay string, v *j5ValidatedOutput, scope *j5TaskScope, vh *j5VerifiedHeads, agent *AgentDeclaration) (mapped, held bool, err error) {
	runInt, ok := parseRunID(runIDDisplay)
	cid, cok := parseChallengeID(challengeIDDisplay)
	if !ok || !cok {
		return false, false, ErrValidation
	}
	tid := taskIntID(&scope.Task)
	after := map[string]any{"verdict": string(v.Verdict), "reason": v.Reason, "challenge": challengeIDDisplay}
	if scope.ContentRef != "" {
		after["content_ref"] = scope.ContentRef
	}
	if heads := vh.logEntries(); len(heads) > 0 {
		after[activityKeyVerifiedHeads] = heads
	}

	if v.Verdict == J5VerdictUncertain {
		_, err := s.transitionAsInvoker(ctx, runIDDisplay, challengeIDDisplay, OpHold, func(ctx context.Context, tc *transitionCtx) error {
			cur, err := j5TaskStillCurrent(ctx, tc.tx, cid, scope)
			if err != nil {
				return err
			}
			if !cur {
				return errReconcileNotEligible
			}
			mapped = true
			tc.taskID = &tid
			if err := insertHoldStop(ctx, tc, *v.Question, stopHoldOptions(StopKindVerifyUncertain, agent)); err != nil {
				return err
			}
			if err := tc.rec.record("task", tid, ActivityActionTaskVerify, nil, after); err != nil {
				return err
			}
			return s.insertJ5Occurrence(ctx, tc, OccurrenceKindVerificationUncertain)
		})
		if err != nil {
			if errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) || errors.Is(err, errReconcileNotEligible) {
				return false, false, nil
			}
			return false, false, err
		}
		return mapped, false, nil
	}

	if v.Verdict == J5VerdictNotMet && v.ConflictOverriddenFrom == "" && agent != nil {
		// 連続の「満たしていない」がこの判定で差し戻しの上限に達するなら、委譲先へ差し戻さず、同じ書き込みで保留にする。
		var streak int
		if err := s.db.Read(ctx, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `SELECT verify_not_met_streak FROM task WHERE id = ?`, tid).Scan(&streak)
		}); err != nil {
			return false, false, classifyReadWriteErr(err)
		}
		if streak+1 >= agent.ReworkLimit {
			return s.holdForTaskVerifyLimit(ctx, runIDDisplay, challengeIDDisplay, runInt, cid, tid, streak+1, agent, scope, after)
		}
	}

	actor, err := s.resolveActor()
	if err != nil {
		return false, false, err
	}
	err = s.mutateAsRun(context.WithoutCancel(ctx), actor, ChannelInvoker, VerificationNone, &runInt, func(tx *sql.Tx, rec *activityRecorder) error {
		cur, err := j5TaskStillCurrent(ctx, tx, cid, scope)
		if err != nil || !cur {
			return err
		}
		mapped = true
		switch v.Verdict {
		case J5VerdictMet:
			// 検証した中身を記録するだけ。タスクは検証中のまま（済みにするのは統合先へ入った後）、課題も動かさない。
			if _, err := tx.ExecContext(ctx, `UPDATE contract_run SET verified_ref = ? WHERE id = ?`, scope.ContentRef, scope.ContractRunRowID); err != nil {
				return err
			}
			after["verified_ref"] = scope.ContentRef
			// 「満たした」で、連続の「満たしていない」の数えを 0 に戻す。
			if _, err := tx.ExecContext(ctx, `UPDATE task SET verify_not_met_streak = 0 WHERE id = ?`, tid); err != nil {
				return err
			}
			return rec.record("task", tid, ActivityActionTaskVerify, nil, after)
		default: // not_met
			if _, err := setTaskStatus(ctx, tx, rec, tid, TaskStatusInProgress); err != nil {
				return err
			}
			if err := rec.record("task", tid, ActivityActionTaskVerify, nil, after); err != nil {
				return err
			}
			if v.ConflictOverriddenFrom != "" {
				return nil // コンフリクトの PR のための置き換えは、達成条件の未達ではないので、連続の数えにも発生にも数えない
			}
			if _, err := tx.ExecContext(ctx, `UPDATE task SET verify_not_met_streak = verify_not_met_streak + 1 WHERE id = ?`, tid); err != nil {
				return err
			}
			return s.insertRunOccurrence(ctx, tx, runInt, OccurrenceKindVerificationNotMet, OccurrenceOutcomeBad, rec.at)
		}
	})
	if err != nil {
		return false, false, err
	}
	return mapped, false, nil
}

// holdForTaskVerifyLimit は、タスクの検証の連続の「満たしていない」が差し戻しの上限（宣言の rework_limit）に達した判定を書く。
// 委譲先へ差し戻さず、止まり方 rework_limit の保留（宣言の supervisor が真ならスーパーバイザー待ち）を、判定の記録と同じ書き込みで作る。
// 保留ができた後に数え直すため、連続の数えは 0 へ戻す（そうしないと、保留への回答の後の次の判定がすぐ上限になる）。
// いまある委譲の差し戻しの回数（decideRework）とは別の数えで、そちらには数えない。
func (s *Store) holdForTaskVerifyLimit(ctx context.Context, runIDDisplay, challengeIDDisplay string, runInt, cid, tid int64, streak int, agent *AgentDeclaration, scope *j5TaskScope, after map[string]any) (mapped, held bool, err error) {
	_, err = s.transitionAsInvoker(context.WithoutCancel(ctx), runIDDisplay, challengeIDDisplay, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		cur, err := j5TaskStillCurrent(ctx, tc.tx, cid, scope)
		if err != nil {
			return err
		}
		if !cur {
			return errReconcileNotEligible
		}
		mapped = true
		tc.taskID = &tid
		if _, err := setTaskStatus(ctx, tc.tx, tc.rec, tid, TaskStatusInProgress); err != nil {
			return err
		}
		if _, err := tc.tx.ExecContext(ctx, `UPDATE task SET verify_not_met_streak = 0 WHERE id = ?`, tid); err != nil {
			return err
		}
		if err := tc.rec.record("task", tid, ActivityActionTaskVerify, nil, after); err != nil {
			return err
		}
		if err := s.insertRunOccurrence(ctx, tc.tx, runInt, OccurrenceKindVerificationNotMet, OccurrenceOutcomeBad, tc.now); err != nil {
			return err
		}
		question := fmt.Sprintf("タスクの検証が連続して「満たしていない」と判定され、差し戻しの上限に達したため、委譲先へ差し戻さず止めた（上限の種類: 差し戻し、回数: %d、上限: %d）。\n"+
			"直近の検証: %s。回答すると、連続の数えを 0 から数え直して続ける。", streak, agent.ReworkLimit, runIDDisplay)
		_, err = s.insertHoldWithOccurrenceOptions(ctx, tc, question, holdReasonReworkLimit, "", HoldOptionsInput{StopKind: StopKindReworkLimit, SupervisorEnabled: agent.Supervisor})
		return err
	})
	if err != nil {
		if errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) || errors.Is(err, errReconcileNotEligible) {
			return false, false, nil
		}
		return false, false, err
	}
	return mapped, mapped, nil
}
