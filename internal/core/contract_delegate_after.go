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
	// contractWaitReview は成果物のレビュー待ち。保留にせず、成果物を取り込んで課題を検証中へ進める。
	contractWaitReview
	// contractWaitParent は親が答える問い（decider が parent・kind が answer）。J4 が答えるか人へ上げる。
	contractWaitParent
)

// contractWaitDestination は求められた操作の写し先を決める 1 か所。
// kind が observe なら読み直すだけ。decider が parent で kind が answer の待ちは、子の問いを振り分ける判断点（J4）へ回す
// （contractWaitParent）。それ以外の answer・approve は decider に依らずオーナーの保留へ写す（human はもともとオーナーの承認、
// any は仕様に写し方が無いので安全側の人の承認、parent でも approve は親が代わりに承認しない）。
// review は、成果物（pr・branch・commit）があるときだけ検証中へ進める（decider は読まない）。成果物が無ければ、
// 検証できないので他の求めと同じくオーナーの保留へ写す。
func contractWaitDestination(ra *ContractRequestedAction, artifacts []ContractArtifact) contractWaitDest {
	if ra == nil || ra.Kind == contractRequestedKindObserve {
		return contractWaitObserve
	}
	if ra.Kind == contractRequestedKindReview && hasReviewableArtifact(artifacts) {
		return contractWaitReview
	}
	// resolvable_by が terminal の待ちは resume では解けないので、J4 の答え（resume で返す）へ回さずオーナーの保留へ写す。
	if ra.Kind == contractRequestedKindAnswer && ra.Decider == contractDeciderParent && !resolvableByTerminal(ra.ResolvableBy) {
		return contractWaitParent
	}
	return contractWaitOwnerHold
}

// contractWaitingQuestion は保留の問い。求められた操作の文と、選択肢があれば選択肢として並べる。
func contractWaitingQuestion(runID, summary string, ra *ContractRequestedAction, carriesAnswer bool) string {
	what := "回答"
	switch ra.Kind {
	case contractRequestedKindApprove:
		what = "承認"
	case contractRequestedKindReview:
		what = "レビュー"
	}
	var b strings.Builder
	terminal := resolvableByTerminal(ra.ResolvableBy)
	if terminal {
		// 接続ツールの外で解く必要がある。何をするかを書いた text を逐語で先に案内する。
		if t := strings.TrimSpace(ra.Text); t != "" {
			b.WriteString("接続ツールの出力（text）: " + t + "\n")
		}
	}
	fmt.Fprintf(&b, "接続ツールの run（%s）が%sを待っている（求めている相手: %s）。接続ツールの要約: %s\n", runID, what, ra.Decider, summary)
	if t := strings.TrimSpace(ra.Text); t != "" && !terminal {
		b.WriteString("求められた操作: " + t + "\n")
	}
	if len(ra.Options) > 0 {
		b.WriteString("選択肢:\n")
		for i, o := range ra.Options {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, o)
		}
	}
	if terminal {
		b.WriteString("この求めは接続ツールの外のターミナルで人が解く必要があり、resume では解けない。解いてから回答する。回答しても resume は送らず、次の周で接続ツールの run の状態を読み直す。")
	} else if carriesAnswer {
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
		if strings.Contains(a, "{answer}") || strings.Contains(a, "{choice}") {
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
	return row.RequestedKind == ra.Kind && row.RequestedDecider == ra.Decider && row.RequestedText == ra.Text &&
		row.RequestedResolvableBy == ra.ResolvableBy
}

// afterContractRead は読めた状態に応じた課題への写しを行う: waiting は求められた操作に従って保留へ写し、
// succeeded は課題を着手中から検証中へ進める（統合先を使う課題は進めず、holdIntegrationSucceeded が保留にする）。読みが成功していない（Result が succeeded でない）・既に保留にした
// ときは何もしない。
func (s *Store) afterContractRead(ctx context.Context, rep *ContractRunReport, connector *Connector, rs *contractReleaseSource) error {
	if rep.Result != RunResultSucceeded || rep.Held {
		return nil
	}
	switch rep.State {
	case ContractStateWaiting:
		dest := contractWaitDestination(rep.RequestedAction, rep.Artifacts)
		if dest == contractWaitParent {
			if rs != nil && rs.J4 {
				return nil // J4 へ回す（pollOne の answerParentWait）。保留にしない
			}
			dest = contractWaitOwnerHold
		}
		switch dest {
		case contractWaitReview:
			return s.receiveContractReview(ctx, rep, connector, rs)
		case contractWaitOwnerHold:
		default:
			return nil
		}
		held, err := s.holdContractWait(ctx, rep, contractWaitingQuestion(rep.ExternalRunID, rep.Summary, rep.RequestedAction, resumeCarriesAnswer(connector)))
		rep.Held = held
		return err
	}
	if isReflect, err := s.isReflectRun(ctx, rep); err != nil || isReflect {
		if err != nil || !rep.State.Terminal() {
			return err
		}
		// 完了でない課題の上げる依頼が、終わりの状態を返した（flywheel が取り消した応答は、この関数へ来ない）。
		return s.holdReflectEnded(ctx, rep)
	}
	switch rep.State {
	case ContractStateSucceeded:
		if handled, err := s.holdIntegrationSucceeded(ctx, rep, rs); err != nil || handled {
			return err
		}
		// succeeded の run は読んだ時点で終わりとして記録済みで、次の周に読み直されない。release を作れなくても検証中へ進める
		// （進めないと課題が着手中のまま取り残される）。release が無いときは人が op add で補う。
		_ = s.registerContractReleases(ctx, rs, rep)
		_, err := s.transitionAsInvoker(context.WithoutCancel(ctx), rep.RunID, rep.ChallengeID, OpSubmit,
			func(ctx context.Context, tc *transitionCtx) error {
				if tc.current.Status != StatusInProgress {
					return errReconcileNotEligible
				}
				// 読んだ後で答えが付いて着手中へ戻っていたときは、ここで進めるので印は不要。
				if _, err := tc.tx.ExecContext(ctx, `UPDATE contract_run SET held_success = 0 WHERE challenge_id = ? AND external_run_id = ?`, tc.id, rep.ExternalRunID); err != nil {
					return err
				}
				return verifyContractTask(ctx, tc, rep.ExternalRunID)
			})
		if err != nil && !errors.Is(err, ErrInvalidTransition) && !errors.Is(err, ErrTerminalState) && !errors.Is(err, errReconcileNotEligible) {
			return err
		}
		// 課題が保留の間に読んだ成功の印（held_success）は、読んだ記録と同じトランザクションで付けてある（recordContractStatus）。
	case ContractStateFailed, ContractStateCancelled:
		// 「入れてよい」を返し済みの実行の失敗・取り消しは、統合先へ入ったかを確かめて扱う（返し済みでなければ、読んだ記録の書き込みが扱い済み）。
		return s.finishStoppedLanding(ctx, rep, rs)
	}
	return nil
}

// finishStoppedLanding は、統合先を使う課題のタスクの実行が、「入れてよい」を返し済み（land_ref がある）のまま失敗・取り消しで終わったとき、
// 成果物が統合先へ入ったかを確かめて扱う（finishIntegrationLanding）。返し済みでなければ何もしない。
func (s *Store) finishStoppedLanding(ctx context.Context, rep *ContractRunReport, rs *contractReleaseSource) error {
	cid, ok := parseChallengeID(rep.ChallengeID)
	if !ok {
		return nil
	}
	integ, landRef, recorded, err := s.loadIntegrationTaskEnd(ctx, cid, rep.ExternalRunID)
	if err != nil || !integ || landRef == "" {
		return err
	}
	return s.finishIntegrationLanding(ctx, rep, rs, cid, landRef, recorded)
}

// loadIntegrationTaskEnd は、統合先を使う課題のタスクの契約の実行（externalRunID）の終わりを扱うための読み。integ は、統合先を使う課題の
// タスクの実行か（上げる依頼・統合先を使わない課題は偽）。landRef は通す中身（返し済みでなければ空）、recorded は課題の統合先の記録。
func (s *Store) loadIntegrationTaskEnd(ctx context.Context, cid int64, externalRunID string) (integ bool, landRef, recorded string, err error) {
	err = s.db.Read(ctx, func(tx *sql.Tx) error {
		u, err := challengeUsesIntegration(ctx, tx, cid)
		if err != nil {
			return err
		}
		integ = u != nil && *u
		if !integ {
			return nil
		}
		var kind, lr, ir sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT kind, land_ref FROM contract_run WHERE challenge_id = ? AND external_run_id = ?`, cid, externalRunID).Scan(&kind, &lr); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if kind.String == contractRunKindReflect {
			integ = false
			return nil
		}
		if err := tx.QueryRowContext(ctx, `SELECT integration_ref FROM challenge WHERE id = ?`, cid).Scan(&ir); err != nil {
			return err
		}
		landRef, recorded = lr.String, ir.String
		return nil
	})
	return integ, landRef, recorded, classifyReadWriteErr(err)
}

// holdIntegrationSucceeded は、統合先を使う課題のタスクの実行が、レビュー待ちを経ずに succeeded になったとき（land_ref が無い）、
// 検証へ進めず、課題が着手中なら保留にする（上流の成果物が統合先へ入っていれば merged_unverified、入っていなければ delegate_ended）。
// 統合先を使う課題では、succeeded を課題の検証中への移り変わりに使わないので、処理した（真）。land_ref があるときの succeeded は、
// 「入れてよい」を返し済みの実行の終わりの確かめ（finishIntegrationLanding）が扱う。統合先を使わない課題では何もしない（偽）。
func (s *Store) holdIntegrationSucceeded(ctx context.Context, rep *ContractRunReport, rs *contractReleaseSource) (bool, error) {
	cid, ok := parseChallengeID(rep.ChallengeID)
	if !ok {
		return false, nil
	}
	integ, landRef, recorded, err := s.loadIntegrationTaskEnd(ctx, cid, rep.ExternalRunID)
	if err != nil || !integ {
		return false, err
	}
	if landRef != "" {
		// 「入れてよい」を返し済み。成果物が統合先へ入ったかを上流で確かめ、入っていればタスクを済みにする（課題の状態によらない）。
		return true, s.finishIntegrationLanding(ctx, rep, rs, cid, landRef, recorded)
	}
	// 保留にしない場合（課題が着手中でない・取りやめのタスク）は、契約の実行を終わった記録にするだけ。保留にする場合は、保留と終わりの記録を
	// 1 つの書き込みにする（間でプロセスが止まっても、保留の無い終わった記録だけが残らない。開いたままなら次の状態の確認が読み直す）。
	if inProgress, err := s.challengeInProgress(ctx, cid); err != nil || !inProgress {
		if err != nil {
			return true, err
		}
		// 保留中の課題は、契約の実行を開けたまま残す（上げる依頼の終わりと同じ形。回答で着手中へ戻った後の状態の確認が、同じ終わりを読み直して、
		// 統合先へ入ったかを確かめて保留を立て、終わった記録にする。ここで終わった記録にすると、タスクが検証中・着手中のまま、保留が立たない）。
		// 開けたままの実行は、タスクの二重の起動を止め、state が succeeded で land_ref が空なので、計画を作り直す回答の「作業している実行」に数えない。
		if held, err := s.challengeStatusIs(ctx, cid, StatusAwaitingHuman); err != nil || held {
			return true, err
		}
		return true, s.closeContractRunRow(ctx, cid, rep.ExternalRunID)
	}
	if sc, err := s.loadReviewIntakeScope(ctx, cid, rep.ExternalRunID); err != nil || (sc.TaskID != nil && sc.TaskStatus == TaskStatusCancelled) {
		if err != nil {
			return true, err
		}
		return true, s.closeContractRunRow(ctx, cid, rep.ExternalRunID) // 取りやめのタスクの実行の終わりでは保留にしない
	}
	// 記録済みの状態が終わりのとき（保留の書き込みのやり直し）、読み直した status は成果物を持たない。取り込み済みの成果物で補う。
	arts, err := s.landingArtifacts(ctx, rep, cid)
	if err != nil {
		return true, err
	}
	if recorded == "" {
		if branches, _ := integrationArtifactRefs(arts); len(branches) == 1 {
			recorded = branches[0]
		}
	}
	merged, unreadable := false, true // 確かめられなかったときは、入っているとは言えず、入っていないとも言い切れない
	if rs != nil && rs.Reconcile != nil && recorded != "" {
		unreadable = false
		m, err := rs.mergedIntoBranch(context.WithoutCancel(ctx), arts, recorded)
		if err != nil {
			unreadable = true
		}
		merged = m
	}
	reason := holdReasonDelegateEnded
	if merged {
		reason = holdReasonMergedUnverified
	}
	return true, s.holdLandedEnd(ctx, rep, cid, integrationSucceededQuestion(rep.ExternalRunID, merged, unreadable), reason)
}

// SubmitHeldContractSuccesses は、課題が保留の間に契約の run が succeeded まで進み（held_success）、オーナーが答えて課題が
// 着手中へ戻ったものを、start も resume も呼ばずに検証中へ進める。成果物は succeeded を読んだときに取り込み済み。
// 答えは保留に残り、完了の検証（J5）の材料に入る。オーナーが答える前（課題がまだ保留）は何も変えない。
// 検証中へ進めた課題の数を返す。only が非 nil ならその課題だけを対象にする。統合先を使う課題は対象にしない。
func (s *Store) SubmitHeldContractSuccesses(ctx context.Context, only *int64) (int, error) {
	type target struct {
		id          int64
		startRunID  int64
		externalRun string
	}
	var targets []target
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		q := `SELECT k.challenge_id, k.start_run_id, k.external_run_id FROM contract_run k JOIN challenge c ON c.id = k.challenge_id
			WHERE k.held_success = 1 AND c.status = 'in_progress' AND COALESCE(c.uses_integration, 0) = 0
			AND NOT EXISTS (SELECT 1 FROM task t WHERE t.id = k.task_id AND t.status = 'cancelled')`
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
				if _, err := tc.tx.ExecContext(ctx, `UPDATE contract_run SET held_success = 0 WHERE challenge_id = ? AND external_run_id = ?`, tc.id, t.externalRun); err != nil {
					return err
				}
				return verifyContractTask(ctx, tc, t.externalRun)
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
			// 契約の run が結び付くタスクに、保留も結び付ける。
			taskID, err := contractRunTaskID(ctx, tc.tx, tc.id, rep.ExternalRunID)
			if err != nil {
				return err
			}
			tc.taskID = taskID
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
	row.reqFacts = connector.RequiredFacts
	if row.Kind == contractRunKindReflect {
		row.reqFacts = nil // 上げる依頼のレビュー待ちには required_facts を掛けない（満たさなかった回数も数えない）
	}
	var answer contractAnswer
	var resume bool
	// 上げる依頼（kind が reflect）のレビュー待ちには、差し戻しの返しをしない。課題が検証中の間は、レビュー待ちを読み直さない
	// （読むたびに作る run が、課題の単位の確かめが読む最後の委譲の実行になり、取り込んだ成果物を隠すため）。
	if row.Kind == contractRunKindReflect && row.State == ContractStateWaiting && row.RequestedKind == contractRequestedKindReview {
		verifying, err := s.challengeStatusIs(ctx, row.ChallengeID, StatusVerifying)
		if err != nil || verifying {
			return nil, err
		}
	}
	if row.Kind != contractRunKindReflect && row.State == ContractStateWaiting && row.RequestedKind == contractRequestedKindReview {
		rr, err := s.pendingReviewReply(ctx, row, planVersion, in.AgentDecl)
		if err != nil {
			return nil, err
		}
		switch {
		case rr == nil:
		case rr.Skip:
			return nil, nil
		case rr.HoldQuestion != "":
			held, err := s.holdContractWithReason(ctx, rr.HoldRunID, challengeID, rr.HoldQuestion, holdReasonReworkLimit)
			return &ContractRunReport{ChallengeID: challengeID, ExternalRunID: row.ExternalRunID, Result: RunResultSucceeded,
				State: row.State, Summary: row.Summary, Held: held}, err
		default:
			// 引数を埋められないまま答えを失わないよう、返すと決める前に確かめる。
			if _, err := fillRowCommandArgs(connector, row, connector.Commands.Resume, challengeID, binding, *rr.Answer); err != nil {
				return nil, err
			}
			answer, resume = *rr.Answer, true
		}
	}
	if !resume {
		a, ok, err := s.loadContractAnswer(ctx, row, func(answer string) error {
			// 答えを返すと決める（結び付きを外す）前に、resume の引数を埋められることを確かめる（埋められないまま答えを失わない）。
			// 答えが無い状態の確認（status）では resume の引数を確かめない（取り込み元を要するのが resume だけの宣言でも状態を読める）。
			_, err := fillRowCommandArgs(connector, row, connector.Commands.Resume, challengeID, binding, contractAnswer{Choice: answer})
			return err
		})
		if err != nil {
			return nil, err
		}
		answer, resume = contractAnswer{Choice: a}, ok
	}
	integ, err := s.integrationTaskRun(ctx, row)
	if err != nil {
		return nil, err
	}
	// 統合先を使う課題のタスクの実行の succeeded は、統合先へ入ったかの確かめと、保留・タスクを済みにする書き込みが済むまで、終わった記録にしない
	// （その書き込みが、同じトランザクションで終わった記録にする）。
	row.holdSuccessEnd = integ
	args, name := connector.Commands.Status, "status"
	if resume {
		args, name = connector.Commands.Resume, "resume"
	}
	command, err := fillRowCommandArgs(connector, row, args, challengeID, binding, answer)
	if err != nil {
		return nil, err
	}
	workDir := existingContractWorkDir(row, in.WorkDir)
	// 上げる依頼（kind が reflect）は、終わりを読んでも終わった記録にしない（保留を立てる書き込み〔holdReflectEnded〕が終わった記録にする。
	// 書き込みが途中で失敗したら、次の状態の確認が同じ終わりを読み直してやり直す）。
	rep, err := s.runExistingContractCommandWith(ctx, in.Client, in.AgentDecl, workDir, row, name, command, planVersion, cycleID, "", nil, row.Kind == contractRunKindReflect)
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
		if err := s.afterContractRead(ctx, rep, connector, in.releaseSource()); err != nil {
			return nil, err
		}
	}
	// 上げる依頼は、予算の上限での中止をしない。
	if row.Kind != contractRunKindReflect && rep.Result == RunResultSucceeded && !rep.State.Terminal() && !rep.Held {
		if err := s.cancelOverBudget(ctx, in, row, connector, binding, planVersion, cycleID, rep); err != nil {
			return nil, err
		}
	}
	if !resume && integ && rep.Result == RunResultSucceeded && !rep.Held && rep.PendingRespond == nil && rep.State == ContractStateWaiting &&
		rep.RequestedAction != nil && rep.RequestedAction.Kind == contractRequestedKindReview {
		return s.landVerifiedReview(ctx, in, row, connector, binding, planVersion, cycleID, rep)
	}
	if !resume && rep.PendingRespond != nil && !rep.Held && !rep.State.Terminal() {
		return s.respondToReview(ctx, in, row, connector, binding, planVersion, cycleID, rep)
	}
	if !resume && in.Judgment != nil && rep.Result == RunResultSucceeded && !rep.Held && rep.State == ContractStateWaiting &&
		contractWaitDestination(rep.RequestedAction, rep.Artifacts) == contractWaitParent {
		return s.answerParentWait(ctx, in, row, connector, binding, planVersion, cycleID, rep)
	}
	if !resume && row.State == ContractStateWaiting && row.RequestedKind == contractRequestedKindObserve && stillObserving(rep) {
		// オーナーの保留（人間対応待ち）の間は、接続ツールの run を進める resume を呼ばない。
		if ok, err := s.challengeInProgress(ctx, row.ChallengeID); err != nil || !ok {
			return rep, err
		}
		return s.resumeObservation(ctx, in, row, connector, binding, planVersion, cycleID, rep)
	}
	return rep, nil
}

// stillObserving は、状態の確認の読み（rep）が成功し、課題を保留にせず、観測の待ち（waiting・observe）のままか。
func stillObserving(rep *ContractRunReport) bool {
	return rep.Result == RunResultSucceeded && !rep.Held && rep.State == ContractStateWaiting &&
		rep.RequestedAction != nil && rep.RequestedAction.Kind == contractRequestedKindObserve
}

// resumeObservation は、観測の待ちを読み直した後に、答え無しの resume（{answer}・{choice}・{note} を空で差し込む）を
// 1 回呼ぶ（観測の待ちは、接続ツール側で resume を呼ばれて初めて進むことがある）。時間の上限は他の契約のコマンドと同じ。
// 引数を埋められない・resume が失敗したときは課題を保留にせず、状態の確認の読み（statusRep）を返す
// （次の周の読み直しで再び呼ぶだけで、追加のやり直しはしない）。成功したら、返った状態を status と同じに読んで写す。
func (s *Store) resumeObservation(ctx context.Context, in ContractPollInput, row *contractRunRow, connector *Connector, binding *sourceBinding, planVersion, cycleID *int64, statusRep *ContractRunReport) (*ContractRunReport, error) {
	command, err := fillRowCommandArgs(connector, row, connector.Commands.Resume, statusRep.ChallengeID, binding, contractAnswer{})
	if err != nil {
		return statusRep, nil
	}
	latest := *row
	latest.State, latest.Summary, latest.CostMicros = statusRep.State, statusRep.Summary, usdToMicros(statusRep.CostTotalUSD)
	latest.RequestedKind = contractRequestedKindObserve
	workDir := existingContractWorkDir(row, in.WorkDir)
	rep, err := s.runExistingContractCommand(ctx, in.Client, in.AgentDecl, workDir, &latest, "resume", command, planVersion, cycleID, "")
	if err != nil {
		return nil, err
	}
	if rep.Result != RunResultSucceeded {
		return statusRep, nil
	}
	if err := s.afterContractRead(ctx, rep, connector, in.releaseSource()); err != nil {
		return nil, err
	}
	if !rep.State.Terminal() && !rep.Held {
		if err := s.cancelOverBudget(ctx, in, &latest, connector, binding, planVersion, cycleID, rep); err != nil {
			return nil, err
		}
	}
	return rep, nil
}

// challengeInProgress は課題 cid が着手中か。
func (s *Store) challengeInProgress(ctx context.Context, cid int64) (bool, error) {
	var inProgress bool
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		ch, err := loadChallenge(ctx, tx, cid)
		inProgress = err == nil && ch != nil && ch.Status == StatusInProgress
		return err
	})
	return inProgress, classifyReadWriteErr(err)
}

// recordedTerminalStatus は、記録した状態が終わりの契約の実行（row）の状態の確認を、記録から作った読みにしたもの。
// 費用は持たない。成果物は、取り込み済みのもの（arts）を持つ。
func recordedTerminalStatus(row *contractRunRow, arts []ContractArtifact) ContractLaunchOutput {
	return ContractLaunchOutput{Result: RunResultSucceeded, Status: &ContractStatus{
		ContractVersion: contractVersion, RunID: row.ExternalRunID, State: row.State, Summary: row.Summary, Artifacts: arts,
	}}
}

// runExistingContractCommand は既存の契約の run に対するコマンド（status・resume・cancel）を 1 回呼んで記録する。
func (s *Store) runExistingContractCommand(ctx context.Context, client ContractClient, agent *AgentDeclaration, workDir string, row *contractRunRow, name string, command []string, planVersion, cycleID *int64, stopNote string) (*ContractRunReport, error) {
	return s.runExistingContractCommandWith(ctx, client, agent, workDir, row, name, command, planVersion, cycleID, stopNote, nil, false)
}

// runExistingContractCommandWith は runExistingContractCommand に、反映の操作のために呼ぶとき（releaseOp が非 nil。反映の操作の
// 内部整数 ID）の違い（成果物を取り込まない・保留にしない・facts の回数を数えない・run の行に反映の操作の印を付ける）を足したもの。
// holdEnd が真なら、終わり（succeeded・failed・cancelled）を読んでも契約の実行（contract_run）を終わった記録（ended_at）にしない
// （反映の試行の結果を書く書き込みが終わった記録にする。それまでは次の反映の段が同じ終わりを読み直せる）。
func (s *Store) runExistingContractCommandWith(ctx context.Context, client ContractClient, agent *AgentDeclaration, workDir string, row *contractRunRow, name string, command []string, planVersion, cycleID *int64, stopNote string, releaseOp *int64, holdEnd bool) (*ContractRunReport, error) {
	sessionID, err := newLowercaseUUIDv4()
	if err != nil {
		return nil, fmt.Errorf("%w: generate session id: %w", ErrStoreError, err)
	}
	var recordedArts []ContractArtifact
	if name == "status" && releaseOp == nil && row.State.Terminal() {
		var err error
		if recordedArts, err = s.landingArtifacts(ctx, &ContractRunReport{ExternalRunID: row.ExternalRunID}, row.ChallengeID); err != nil {
			return nil, err
		}
	}
	// コマンド 1 回ごとの run の行も、契約の run が結び付くタスクに結び付ける（過去の行は nil のまま）。
	runIDInt, err := s.beginContractRunMarked(ctx, row.ChallengeID, cycleID, planVersion, sessionID, row.MaxBudget, false, row.TaskID, releaseOp, row.Kind == contractRunKindReflect)
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
			if name == "status" && releaseOp == nil && row.State.Terminal() {
				// 記録した状態が終わりの行は、委譲先を呼ばず、記録から読んだことにする（委譲先の失敗で、後の処理が止まらない）。
				return recordedTerminalStatus(row, recordedArts), nil
			}
			return client.Status(ctx, li)
		},
		MaxMicros: row.MaxBudget, Existing: row, StopNote: stopNote, ReleaseStage: releaseOp != nil || row.Kind == contractRunKindReflect || row.discard, HoldEnd: holdEnd, HoldSuccessEnd: row.holdSuccessEnd,
		FactsRequired: row.reqFacts, CountFacts: name == "status" && releaseOp == nil,
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
	// resume では解けない求め（terminal）への答えは接続ツールへ返さない。結び付きだけを外し（同じ答えで周ごとに
	// 読み直さない）、状態の確認（status）で run の進みを読み直す。
	terminal := resolvableByTerminal(row.RequestedResolvableBy)
	if check != nil && !terminal {
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
		if n, _ := res.RowsAffected(); n == 1 && !terminal {
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
	if inProgress, err := s.challengeInProgress(ctx, row.ChallengeID); err != nil || !inProgress {
		return err
	}
	command, err := fillRowCommandArgs(connector, row, connector.Commands.Cancel, rep.ChallengeID, binding, contractAnswer{})
	if err != nil {
		return err
	}
	note := fmt.Sprintf("接続ツールの run（%s）の費用の累計（%s USD）が、課題の実装の枠とレビュー対応の枠の残りの合計（%s USD）を上回ったため、中止を依頼した。",
		row.ExternalRunID, formatUSDAmount(rep.CostTotalUSD), formatUSDAmount(microsToUSD(row.MaxBudget)))
	latest := *row
	latest.State, latest.Summary, latest.CostMicros = rep.State, rep.Summary, usdToMicros(rep.CostTotalUSD)
	workDir := existingContractWorkDir(row, in.WorkDir)
	crep, err := s.runExistingContractCommand(ctx, in.Client, in.AgentDecl, workDir, &latest, "cancel", command, planVersion, cycleID, note)
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
		return s.afterContractRead(ctx, crep, connector, in.releaseSource())
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
		Client: in.Contract, CycleID: in.CycleID, Reconcile: in.Reconcile, MaxBudgetUSD: microsToUSD(dc.ImplBudgetMicros + dc.ReviewBudgetMicros), Judgment: in.Judgment, InsertRun: adm.insertRun,
		TaskID: taskIntID(&dc.Task),
	})
	if err != nil {
		if jc != nil && errors.Is(err, ErrBudgetExceeded) {
			return nil, &NotStarted{ChallengeID: dc.Challenge.ID, Reason: NotStartedCycleBudget}, nil
		}
		// clone に idle の作業用クローンが無い（契約のコマンドを実行する作業ディレクトリが決まらない）ときは、周を止めず見送る。
		if jc != nil && errors.Is(err, ErrSlotUnavailable) {
			return nil, &NotStarted{ChallengeID: dc.Challenge.ID, Reason: NotStartedSlotUnavailable}, nil
		}
		// 宣言の commands.start の差し込みを埋められない等（ErrValidation）は、黙って飛ばさず理由つきで見えるようにする。
		if jc != nil && errors.Is(err, ErrValidation) {
			return nil, &NotStarted{ChallengeID: dc.Challenge.ID, Reason: NotStartedPlanUnavailable, Detail: err.Error()}, nil
		}
		return nil, nil, err
	}
	return &JudgmentAutoItem{ChallengeID: rep.ChallengeID, RunID: rep.RunID, Result: rep.Result}, nil, nil
}

// answerParentWait は、契約の run が親の答えを待っている（decider が parent・kind が answer）とき、J4 を呼ぶ。
// 全問に答えたら宣言の resume で答えを返し（選択肢があれば選んだ選択肢、無ければ文章）、保留は作らない。
// 人へ上げる判定・J4 の失敗・再開の上限は、オーナーの保留（保留の問いの冒頭に理由）にする。課題が着手中でなければ何もしない。
func (s *Store) answerParentWait(ctx context.Context, in ContractPollInput, row *contractRunRow, connector *Connector, binding *sourceBinding, planVersion, cycleID *int64, rep *ContractRunReport) (*ContractRunReport, error) {
	if inProgress, err := s.challengeInProgress(ctx, row.ChallengeID); err != nil || !inProgress {
		return rep, err
	}
	ra := rep.RequestedAction
	text := strings.TrimSpace(ra.Text)
	if text == "" {
		text = strings.TrimSpace(rep.Summary)
	}
	req := j4Request{
		ChallengeID: rep.ChallengeID, Questions: []j4Question{{Text: text, Options: ra.Options}},
		AgentDecl: in.AgentDecl, CycleID: in.CycleID, Invoker: in.Judgment, Cycle: in.Cycle,
	}
	if sm := strings.TrimSpace(rep.Summary); sm != "" {
		req.Extra = []JudgmentDataSection{{Label: "接続ツールの実行の要約", Content: sm}}
	}
	res, err := s.runJ4(ctx, req)
	if err != nil {
		return nil, err
	}
	holdOwner := func(reason string) (*ContractRunReport, error) {
		q := contractWaitingQuestion(rep.ExternalRunID, rep.Summary, ra, resumeCarriesAnswer(connector))
		if reason != "" {
			q = reason + "\n\n" + q
		}
		held, err := s.holdContractWait(ctx, rep, q)
		rep.Held = held
		return rep, err
	}
	if !res.Answered {
		return holdOwner(res.Reason)
	}
	d := res.Decisions[0]
	answer := contractAnswer{Choice: j4AnswerText(d)}
	if d.Option != nil && len(ra.Options) > 0 {
		answer = contractAnswer{Choice: *d.Option}
		if !blankStr(d.Answer) {
			answer.Note = strings.TrimSpace(*d.Answer)
		}
	}
	command, err := fillRowCommandArgs(connector, row, connector.Commands.Resume, rep.ChallengeID, binding, answer)
	if err != nil {
		return holdOwner("親が答えたが、接続ツールへ答えを返す宣言の resume の引数を埋められなかった（" + err.Error() + "）。")
	}
	latest := *row
	latest.State, latest.Summary, latest.CostMicros = rep.State, rep.Summary, usdToMicros(rep.CostTotalUSD)
	latest.RequestedKind, latest.RequestedDecider, latest.RequestedText = ra.Kind, ra.Decider, ra.Text
	workDir := existingContractWorkDir(row, in.WorkDir)
	rrep, err := s.runExistingContractCommand(ctx, in.Client, in.AgentDecl, workDir, &latest, "resume", command, planVersion, cycleID, "")
	if err != nil {
		return nil, err
	}
	if rrep.Result != RunResultSucceeded {
		held, err := s.holdReclaimedContractStart(ctx, rrep.RunID, rrep.ChallengeID, contractResumeFailedQuestion(row.ExternalRunID, rrep.Result, rrep.Error))
		rrep.Held = held
		return rrep, err
	}
	// 答えを返した直後に同じ求めの waiting が返っても、その周では J4 を呼び直さない（次の周の読みで改めて扱う）。
	if rrep.State == ContractStateWaiting && sameRequestedAction(&latest, rrep.RequestedAction) {
		return rrep, nil
	}
	if err := s.afterContractRead(ctx, rrep, connector, in.releaseSource()); err != nil {
		return nil, err
	}
	return rrep, nil
}
