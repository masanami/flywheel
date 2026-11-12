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
func contractWaitingQuestion(runID, summary string, ra *ContractRequestedAction) string {
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
	b.WriteString("回答すると、その文を接続ツールへ返して続ける。")
	return b.String()
}

// afterContractRead は読めた状態に応じた課題への写しを行う: waiting は求められた操作に従って保留へ写し、
// succeeded は課題を着手中から検証中へ進める。読みが成功していない（Result が succeeded でない）・既に保留にした
// ときは何もしない。
func (s *Store) afterContractRead(ctx context.Context, rep *ContractRunReport) error {
	if rep.Result != RunResultSucceeded || rep.Held {
		return nil
	}
	switch rep.State {
	case ContractStateWaiting:
		if contractWaitDestination(rep.RequestedAction) != contractWaitOwnerHold {
			return nil
		}
		held, err := s.holdReclaimedContractStart(ctx, rep.RunID, rep.ChallengeID,
			contractWaitingQuestion(rep.ExternalRunID, rep.Summary, rep.RequestedAction))
		rep.Held = held
		return err
	case ContractStateSucceeded:
		_, err := s.transitionAsInvoker(context.WithoutCancel(ctx), rep.RunID, rep.ChallengeID, OpSubmit,
			func(_ context.Context, tc *transitionCtx) error {
				if tc.current.Status != StatusInProgress {
					return errReconcileNotEligible
				}
				return nil
			})
		if err != nil && !errors.Is(err, ErrInvalidTransition) && !errors.Is(err, ErrTerminalState) && !errors.Is(err, errReconcileNotEligible) {
			return err
		}
	}
	return nil
}

// pollOne は終わっていない契約の run 1 件を進める。保留への答えが返っていれば resume で返し、無ければ status で読む。
// 読んだ後に状態の写しと予算の確認を行う。
func (s *Store) pollOne(ctx context.Context, in ContractPollInput, row *contractRunRow, connector *Connector, binding *sourceBinding, planVersion, cycleID *int64) (*ContractRunReport, error) {
	challengeID := formatChallengeID(row.ChallengeID)
	answer, resume, err := s.loadContractAnswer(ctx, row)
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
	if err := s.afterContractRead(ctx, rep); err != nil {
		return nil, err
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

// loadContractAnswer は、契約の run が waiting のまま、読んだ後に出された保留へオーナーが答え、課題が着手中へ
// 戻っているとき、その答えを返す（無ければ resume=false）。resume の後に state が waiting でなくなる（または新しい保留が
// 出る）ので、同じ答えを二重に返さない。
func (s *Store) loadContractAnswer(ctx context.Context, row *contractRunRow) (answer string, resume bool, err error) {
	err = s.db.Read(ctx, func(tx *sql.Tx) error {
		var a sql.NullString
		qerr := tx.QueryRowContext(ctx,
			`SELECT h.answer FROM hold h JOIN challenge c ON c.id = h.challenge_id JOIN contract_run k ON k.challenge_id = h.challenge_id
			 WHERE k.id = ? AND k.state = 'waiting' AND c.status = 'in_progress' AND h.answered_at IS NOT NULL AND h.answered_at >= k.observed_at
			 ORDER BY h.id DESC LIMIT 1`, row.ID).Scan(&a)
		if errors.Is(qerr, sql.ErrNoRows) {
			return nil
		}
		if qerr != nil {
			return qerr
		}
		answer, resume = a.String, true
		return nil
	})
	return answer, resume, classifyReadWriteErr(err)
}

// cancelOverBudget は契約の累計の費用が、課題の実装の枠とレビュー対応の枠の合計（run の上限額）を上回ったとき
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
	note := fmt.Sprintf("接続ツールの run（%s）の費用の累計（%s USD）が、課題の実装の枠とレビュー対応の枠の合計（%s USD）を上回ったため、中止を依頼した。",
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
		return nil
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
func (s *Store) delegateViaContract(ctx context.Context, in DelegateInput, jc *JudgmentCycle, dc *delegationContext) (*JudgmentAutoItem, *NotStarted, error) {
	if in.Contract == nil {
		return nil, nil, ErrValidation
	}
	rep, err := s.StartContractDelegation(ctx, ContractStartInput{
		ChallengeID: dc.Challenge.ID, ConnectorID: dc.Connector.ID, AgentDecl: in.AgentDecl, ConnDecl: in.ConnDecl,
		Client: in.Contract, CycleID: in.CycleID, MaxBudgetUSD: microsToUSD(dc.ImplBudgetMicros + dc.ReviewBudgetMicros),
	})
	if err != nil {
		if jc != nil && errors.Is(err, ErrBudgetExceeded) {
			return nil, &NotStarted{ChallengeID: dc.Challenge.ID, Reason: NotStartedCycleBudget}, nil
		}
		return nil, nil, err
	}
	return &JudgmentAutoItem{ChallengeID: rep.ChallengeID, RunID: rep.RunID, Result: rep.Result}, nil, nil
}
