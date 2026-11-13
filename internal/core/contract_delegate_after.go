package core

// このファイルは `form: cli` の接続ツールの委譲の後半（docs/features/m3-invoker-delegation.md
// §委譲の流れ（`form: cli`）後半）を持つ: 判断待ちの写し・答えの返し（resume）・予算の上限での中止（cancel）・
// 成功した契約の run の検証中への移り変わり。接続ツール固有の名前は持たず、起動の形は宣言から取る。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// contractWaitDest は、契約が waiting で求めた操作の写し先。
type contractWaitDest int

const (
	// contractWaitObserve は保留にせず、次の周で読み直す。
	contractWaitObserve contractWaitDest = iota
	// contractWaitOwnerHold はオーナーの保留（人間対応待ち）へ写す。
	contractWaitOwnerHold
)

// contractWaitDestination は求められた操作の写し先を決める 1 か所。
// kind が observe なら読み直すだけ。answer・approve は decider に依らずオーナーの保留へ写す:
// human はもともとオーナーの承認、any は仕様に写し方が無いので安全側の人の承認、parent は子の問いを
// 振り分ける判断点（J4）の宛先だが J4 が未実装の間は安全側でオーナーへ上げる（J4 ができたら parent だけをここで分ける）。
func contractWaitDestination(ra *ContractRequestedAction) contractWaitDest {
	if ra == nil || ra.Kind == "observe" {
		return contractWaitObserve
	}
	return contractWaitOwnerHold
}

// contractWaitingQuestion は保留の問い。求められた操作の文と、選択肢があれば選択肢として並べる。
func contractWaitingQuestion(runID, summary string, ra *ContractRequestedAction, carriesAnswer bool) string {
	what := "回答"
	if ra.Kind == "approve" {
		what = "承認"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "接続ツールの run（%s）が%sを待っている（求めている相手: %s）。接続ツールの要約: %s\n", runID, what, ra.Decider, summary)
	if t := strings.TrimSpace(ra.Text); t != "" {
		b.WriteString("求められた操作: " + t + "\n")
	}
	if len(ra.Options) > 0 {
		b.WriteString("選択肢:\n")
		for i, o := range ra.Options {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, o)
		}
	}
	if carriesAnswer {
		b.WriteString("回答すると、その文を接続ツールへ返して続ける。")
	} else {
		b.WriteString("宣言の resume は答えの差し込みを含まないので、回答の文は接続ツールへ渡らない。回答すると、再開の指示だけを接続ツールへ送る。")
	}
	return b.String()
}

// resumeCarriesAnswer は宣言の resume が、保留への答えを接続ツールへ渡す差し込みを含むか。
func resumeCarriesAnswer(c *Connector) bool {
	if c == nil || c.Commands == nil {
		return false
	}
	for _, a := range c.Commands.Resume {
		if strings.Contains(a, "{answer}") {
			return true
		}
	}
	return false
}

// contractStatusMalformedQuestion は状態の確認（status）の出力が契約の型に合わなかった（malformed）ときの問い。
func contractStatusMalformedQuestion(externalRunID, connector, errSummary string) string {
	q := fmt.Sprintf("接続ツールの run（%s。接続先: %s）の状態の確認の出力が契約の形に合わなかった（失敗の種類: %s", externalRunID, connector, RunResultMalformed)
	if errSummary != "" {
		q += "。詳細: " + errSummary
	}
	return q + "）。契約の状態は更新していない。接続ツールの出力（requested_action.options は文字列の配列など）を確かめて直してから回答する。回答すると次の周で状態を読み直す。"
}

// contractResumeFailedQuestion は resume（答えの返し）が失敗したときの問い。
func contractResumeFailedQuestion(externalRunID string, result RunResult, detail string) string {
	q := fmt.Sprintf("接続ツールの run（%s）へ保留への答えを返す resume が失敗した（失敗の種類: %s", externalRunID, result)
	if detail != "" {
		q += "。詳細: " + detail
	}
	return q + "）。答えが接続ツールへ届いたかは分からない。自動では答えを返し直さない。" +
		"接続ツールの run の状態を確かめ、必要なら接続ツールの側で答えを返してから回答する。"
}

// sameRequestedAction は、契約の run が最後に読んだ求められた操作と ra が同じか。
func sameRequestedAction(row *contractRunRow, ra *ContractRequestedAction) bool {
	if ra == nil {
		return row.RequestedKind == ""
	}
	return row.RequestedKind == ra.Kind && row.RequestedDecider == ra.Decider && row.RequestedText == ra.Text
}

// afterContractRead は読めた状態に応じた課題への写しを行う: waiting は求められた操作に従って保留へ写し、
// succeeded は課題を着手中から検証中へ進める。読みが成功していない（Result が succeeded でない）・既に保留にした
// ときは何もしない。
func (s *Store) afterContractRead(ctx context.Context, rep *ContractRunReport, connector *Connector) error {
	if rep.Result != RunResultSucceeded || rep.Held {
		return nil
	}
	switch rep.State {
	case ContractStateWaiting:
		if contractWaitDestination(rep.RequestedAction) != contractWaitOwnerHold {
			return nil
		}
		held, err := s.holdContractWait(ctx, rep, contractWaitingQuestion(rep.ExternalRunID, rep.Summary, rep.RequestedAction, resumeCarriesAnswer(connector)))
		rep.Held = held
		return err
	case ContractStateSucceeded:
		_, err := s.transitionAsInvoker(context.WithoutCancel(ctx), rep.RunID, rep.ChallengeID, OpSubmit,
			func(ctx context.Context, tc *transitionCtx) error {
				if tc.current.Status != StatusInProgress {
					return errReconcileNotEligible
				}
				// 読んだ後で答えが付いて着手中へ戻っていたときは、ここで進めるので印は不要。
				_, err := tc.tx.ExecContext(ctx, `UPDATE contract_run SET held_success = 0 WHERE challenge_id = ? AND external_run_id = ?`, tc.id, rep.ExternalRunID)
				return err
			})
		if err != nil && !errors.Is(err, ErrInvalidTransition) && !errors.Is(err, ErrTerminalState) && !errors.Is(err, errReconcileNotEligible) {
			return err
		}
		// 課題が保留の間に読んだ成功の印（held_success）は、読んだ記録と同じトランザクションで付けてある（recordContractStatus）。
	}
	return nil
}

// SubmitHeldContractSuccesses は、課題が保留の間に契約の run が succeeded まで進み（held_success）、オーナーが答えて課題が
// 着手中へ戻ったものを、start も resume も呼ばずに検証中へ進める。成果物は succeeded を読んだときに取り込み済み。
// 答えは保留に残り、完了の検証（J5）の材料に入る。オーナーが答える前（課題がまだ保留）は何も変えない。
// 検証中へ進めた課題の数を返す。only が非 nil ならその課題だけを対象にする。
func (s *Store) SubmitHeldContractSuccesses(ctx context.Context, only *int64) (int, error) {
	type target struct {
		id          int64
		startRunID  int64
		externalRun string
	}
	var targets []target
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		q := `SELECT k.challenge_id, k.start_run_id, k.external_run_id FROM contract_run k JOIN challenge c ON c.id = k.challenge_id
			WHERE k.held_success = 1 AND c.status = 'in_progress'`
		args := []any{}
		if only != nil {
			q += ` AND k.challenge_id = ?`
			args = append(args, *only)
		}
		rows, err := tx.QueryContext(ctx, q+` ORDER BY k.id`, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var t target
			if err := rows.Scan(&t.id, &t.startRunID, &t.externalRun); err != nil {
				return err
			}
			targets = append(targets, t)
		}
		return rows.Err()
	})
	if err = classifyReadWriteErr(err); err != nil {
		return 0, err
	}
	n := 0
	var firstErr error
	for _, t := range targets {
		_, err := s.transitionAsInvoker(context.WithoutCancel(ctx), formatRunID(t.startRunID), formatChallengeID(t.id), OpSubmit,
			func(ctx context.Context, tc *transitionCtx) error {
				if tc.current.Status != StatusInProgress {
					return errReconcileNotEligible
				}
				_, err := tc.tx.ExecContext(ctx, `UPDATE contract_run SET held_success = 0 WHERE challenge_id = ? AND external_run_id = ?`, tc.id, t.externalRun)
				return err
			})
		switch {
		case err == nil:
			n++
		case errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) || errors.Is(err, errReconcileNotEligible):
		default:
			firstErr = firstErrOr(firstErr, err)
		}
	}
	return n, firstErr
}

// holdContractWait は課題が着手中なら、契約の waiting を写した保留を作り、その保留の ID を契約の run に結び付ける
// （保留への答えを resume で返すとき、この保留の答えだけを返すため）。着手中でなければ何もしない（偽）。
func (s *Store) holdContractWait(ctx context.Context, rep *ContractRunReport, question string) (bool, error) {
	_, err := s.transitionAsInvoker(context.WithoutCancel(ctx), rep.RunID, rep.ChallengeID, OpHold,
		func(ctx context.Context, tc *transitionCtx) error {
			if tc.current.Status != StatusInProgress {
				return errReconcileNotEligible
			}
			holdID, err := s.insertHoldWithOccurrenceID(ctx, tc, question, holdReasonContractStopped, "")
			if err != nil {
				return err
			}
			_, err = tc.tx.ExecContext(ctx, `UPDATE contract_run SET wait_hold_id = ? WHERE challenge_id = ? AND external_run_id = ?`,
				holdID, tc.id, rep.ExternalRunID)
			return err
		})
	if err != nil {
		if errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) || errors.Is(err, errReconcileNotEligible) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// pollOne は終わっていない契約の run 1 件を進める。保留への答えが返っていれば resume で返し、無ければ status で読む。
// 読んだ後に状態の写しと予算の確認を行う。
func (s *Store) pollOne(ctx context.Context, in ContractPollInput, row *contractRunRow, connector *Connector, binding *sourceBinding, planVersion, cycleID *int64) (*ContractRunReport, error) {
	challengeID := formatChallengeID(row.ChallengeID)
	answer, resume, err := s.loadContractAnswer(ctx, row, func(answer string) error {
		// 答えを返すと決める（結び付きを外す）前に、resume の引数を埋められることを確かめる（埋められないまま答えを失わない）。
		// 答えが無い状態の確認（status）では resume の引数を確かめない（取り込み元を要するのが resume だけの宣言でも状態を読める）。
		_, err := fillContractArgs(connector.Commands.Resume, challengeID, binding, row.ExternalRunID, answer)
		return err
	})
	if err != nil {
		return nil, err
	}
	args, name := connector.Commands.Status, "status"
	if resume {
		args, name = connector.Commands.Resume, "resume"
	}
	command, err := fillContractArgs(args, challengeID, binding, row.ExternalRunID, answer)
	if err != nil {
		return nil, err
	}
	rep, err := s.runExistingContractCommand(ctx, in.Client, in.AgentDecl, in.WorkDir, row, name, command, planVersion, cycleID, "")
	if err != nil {
		return nil, err
	}
	skipRead := false
	if resume {
		if rep.Result != RunResultSucceeded {
			// 答えを返したか分からない。自動ではやり直さず、保留にする（答えは返し済みとして記録してある）。
			held, err := s.holdReclaimedContractStart(ctx, rep.RunID, rep.ChallengeID, contractResumeFailedQuestion(row.ExternalRunID, rep.Result, rep.Error))
			rep.Held = held
			return rep, err
		}
		// resume はすぐ戻り、その時点の状態を返す。答えを返した直後に同じ求められた操作の waiting が返っても、
		// 同じ問いの保留を立て直さない（次の周の状態の読みで、まだ waiting なら改めて問う）。
		skipRead = rep.State == ContractStateWaiting && sameRequestedAction(row, rep.RequestedAction)
	} else if rep.Result == RunResultMalformed && !rep.Held {
		// 状態の確認の出力の型が合わない（例: requested_action.options に文字列でない要素がある）。契約の状態は更新しておらず、
		// 読み直しても同じ出力が返りうるので、自動では読み直し続けず保留にする。
		held, err := s.holdReclaimedContractStart(ctx, rep.RunID, rep.ChallengeID, contractStatusMalformedQuestion(row.ExternalRunID, connector.ID, rep.Error))
		rep.Held = held
		return rep, err
	}
	if !skipRead {
		if err := s.afterContractRead(ctx, rep, connector); err != nil {
			return nil, err
		}
	}
	if rep.Result == RunResultSucceeded && !rep.State.Terminal() && !rep.Held {
		if err := s.cancelOverBudget(ctx, in, row, connector, binding, planVersion, cycleID, rep); err != nil {
			return nil, err
		}
	}
	return rep, nil
}

// runExistingContractCommand は既存の契約の run に対するコマンド（status・resume・cancel）を 1 回呼んで記録する。
func (s *Store) runExistingContractCommand(ctx context.Context, client ContractClient, agent *AgentDeclaration, workDir string, row *contractRunRow, name string, command []string, planVersion, cycleID *int64, stopNote string) (*ContractRunReport, error) {
	sessionID, err := newLowercaseUUIDv4()
	if err != nil {
		return nil, fmt.Errorf("%w: generate session id: %w", ErrStoreError, err)
	}
	runIDInt, err := s.beginContractRun(ctx, row.ChallengeID, cycleID, planVersion, sessionID, row.MaxBudget, false)
	if err != nil {
		return nil, err
	}
	return s.finishContractCall(ctx, contractCall{
		ChallengeID: formatChallengeID(row.ChallengeID), ChallengeIDInt: row.ChallengeID, RunIDInt: runIDInt, Connector: row.ConnectorID,
		Launch: ContractLaunchInput{
			Workspace: s.workspace, WorkDir: s.contractWorkDir(workDir), RunDir: runDirPath(s.workspace, formatRunID(runIDInt)),
			Command: command, TimeoutSec: agent.TimeoutSec.Delegate,
		},
		Invoke: func(ctx context.Context, li ContractLaunchInput) (ContractLaunchOutput, error) {
			switch name {
			case "resume":
				return client.Resume(ctx, li)
			case "cancel":
				return client.Cancel(ctx, li)
			}
			return client.Status(ctx, li)
		},
		MaxMicros: row.MaxBudget, Existing: row, StopNote: stopNote,
	})
}

// loadContractAnswer は、契約の run が waiting のまま、その waiting を写した保留（wait_hold_id）へオーナーが答え、
// 課題が着手中へ戻っているとき、その答えを返す（無ければ resume=false）。答えが無い確認は読み取りだけで済ませ、
// 書き込みのロックを取らない。答えがあれば、check（resume の引数を埋められるかの確認。失敗したら答えを失わないよう何も変えずに
// エラーを返す）を通した後、保留との結び付きを外す（resume が失敗しても同じ答えを二重に返さない）。別の理由で立った保留・
// 古い保留の答えは返さない。
func (s *Store) loadContractAnswer(ctx context.Context, row *contractRunRow, check func(answer string) error) (answer string, resume bool, err error) {
	var holdID int64
	var a sql.NullString
	found := false
	err = s.db.Read(ctx, func(tx *sql.Tx) error {
		qerr := tx.QueryRowContext(ctx,
			`SELECT h.id, h.answer FROM contract_run k JOIN hold h ON h.id = k.wait_hold_id JOIN challenge c ON c.id = k.challenge_id
			 WHERE k.id = ? AND k.state = 'waiting' AND c.status = 'in_progress' AND h.answered_at IS NOT NULL`, row.ID).Scan(&holdID, &a)
		if errors.Is(qerr, sql.ErrNoRows) {
			return nil
		}
		found = qerr == nil
		return qerr
	})
	if err = classifyReadWriteErr(err); err != nil || !found {
		return "", false, err
	}
	if check != nil {
		if err := check(a.String); err != nil {
			return "", false, err
		}
	}
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		// 読んだ後に別の周が先に結び付きを外した・課題が着手中でなくなったときは 0 件になり、答えは返さない。
		res, qerr := tx.ExecContext(ctx, `UPDATE contract_run SET wait_hold_id = NULL WHERE id = ? AND wait_hold_id = ? AND state = 'waiting'
			 AND EXISTS (SELECT 1 FROM challenge WHERE id = challenge_id AND status = 'in_progress')`, row.ID, holdID)
		if qerr != nil {
			return qerr
		}
		if n, _ := res.RowsAffected(); n == 1 {
			answer, resume = a.String, true
		}
		return nil
	})
	return answer, resume, classifyReadWriteErr(err)
}

// cancelOverBudget は契約の累計の費用が、課題の実装の枠とレビュー対応の枠の残りの合計（run の上限額）を上回ったとき
// （同額では中止しない）、宣言の cancel で中止して課題を保留にする。中止が失敗した（コマンドの失敗・中止されない状態）
// ときも保留にし、費用が増え続けうることを問いに書く。自動ではやり直さない（課題が着手中のときだけ中止する）。
func (s *Store) cancelOverBudget(ctx context.Context, in ContractPollInput, row *contractRunRow, connector *Connector, binding *sourceBinding, planVersion, cycleID *int64, rep *ContractRunReport) error {
	if usdToMicros(rep.CostTotalUSD) <= row.MaxBudget {
		return nil
	}
	var inProgress bool
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		ch, err := loadChallenge(ctx, tx, row.ChallengeID)
		inProgress = err == nil && ch != nil && ch.Status == StatusInProgress
		return err
	})
	if err = classifyReadWriteErr(err); err != nil || !inProgress {
		return err
	}
	command, err := fillContractArgs(connector.Commands.Cancel, rep.ChallengeID, binding, row.ExternalRunID, "")
	if err != nil {
		return err
	}
	note := fmt.Sprintf("接続ツールの run（%s）の費用の累計（%s USD）が、課題の実装の枠とレビュー対応の枠の残りの合計（%s USD）を上回ったため、中止を依頼した。",
		row.ExternalRunID, formatUSDAmount(rep.CostTotalUSD), formatUSDAmount(microsToUSD(row.MaxBudget)))
	latest := *row
	latest.State, latest.Summary, latest.CostMicros = rep.State, rep.Summary, usdToMicros(rep.CostTotalUSD)
	crep, err := s.runExistingContractCommand(ctx, in.Client, in.AgentDecl, in.WorkDir, &latest, "cancel", command, planVersion, cycleID, note)
	if err != nil {
		return err
	}
	if crep.Held {
		rep.Held, rep.State = true, crep.State
		return nil
	}
	if crep.Result == RunResultSucceeded && crep.State.Terminal() {
		rep.State = crep.State
		// 中止の依頼の応答が succeeded（成果物つき）なら、読んだときと同じく検証中へ進める。
		return s.afterContractRead(ctx, crep, connector)
	}
	detail := "中止のコマンドが失敗した"
	if crep.Result != RunResultSucceeded {
		detail += fmt.Sprintf("（失敗の種類: %s。詳細: %s）", crep.Result, crep.Error)
	} else {
		detail = fmt.Sprintf("中止を依頼したが、接続ツールの run の状態が %s のまま終わらなかった", crep.State)
	}
	q := note + "\n" + detail + "。接続ツールの側で run が動き続け、費用が増え続けうる。接続ツール側の run を確かめて止めてから回答する。自動では中止をやり直さない。"
	held, err := s.holdReclaimedContractStart(ctx, crep.RunID, rep.ChallengeID, q)
	rep.Held = held
	return err
}

// delegateViaContract は form: cli の接続ツールの操作の計画を、契約の start で委譲する。run の上限額は、課題の実装の枠と
// レビュー対応の枠の残りの合計（契約の費用の累計と比べる額）。
func (s *Store) delegateViaContract(ctx context.Context, in DelegateInput, jc *JudgmentCycle, dc *delegationContext, adm *admission) (*JudgmentAutoItem, *NotStarted, error) {
	if in.Contract == nil {
		return nil, nil, ErrValidation
	}
	rep, err := s.StartContractDelegation(ctx, ContractStartInput{
		ChallengeID: dc.Challenge.ID, ConnectorID: dc.Connector.ID, AgentDecl: in.AgentDecl, ConnDecl: in.ConnDecl,
		Client: in.Contract, CycleID: in.CycleID, MaxBudgetUSD: microsToUSD(dc.ImplBudgetMicros + dc.ReviewBudgetMicros), OnRunInserted: adm.markInserted,
	})
	if err != nil {
		if jc != nil && errors.Is(err, ErrBudgetExceeded) {
			return nil, &NotStarted{ChallengeID: dc.Challenge.ID, Reason: NotStartedCycleBudget}, nil
		}
		// 宣言の commands.start の差し込みを埋められない等（ErrValidation）は、黙って飛ばさず理由つきで見えるようにする。
		if jc != nil && errors.Is(err, ErrValidation) {
			return nil, &NotStarted{ChallengeID: dc.Challenge.ID, Reason: NotStartedPlanUnavailable, Detail: err.Error()}, nil
		}
		return nil, nil, err
	}
	return &JudgmentAutoItem{ChallengeID: rep.ChallengeID, RunID: rep.RunID, Result: rep.Result}, nil, nil
}
