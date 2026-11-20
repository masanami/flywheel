package core

// このファイルは、統合先を使う課題の「上げる依頼」（統合先から反映先へ上げる反映の依頼。契約の実行の kind が reflect で、
// 完了でない課題のもの）を、委譲の段から始める・状態の確認で読む処理を持つ（docs/features/core-redesign.md §課題の単位の確かめと、反映先へ上げる）:
//
//   - 始める（startIntegrationReflects）: 統合先を使う、着手中の課題で、未着手・着手中・検証中のタスクが無く、済みのタスクが 1 個以上あり、
//     終わっていない契約の実行が無いとき、commands.reflect を、{artifact_ref} に課題に記録した統合先の識別を入れて呼ぶ。
//     契約の実行は kind が reflect、task_id と operation_id が NULL。
//   - レビュー待ちを受け取る（checkReflectPRs・submitReflectReview）: 反映先宛ての PR がちょうど 1 つなら、その PR の反映の操作を作って
//     契約の実行へ結び付け、課題を検証中へ進める（タスクは済みのまま）。
//   - 終わりの状態を読んだ（holdReflectEnded）: 課題を着手中へ戻して、理由 reflect_failed で保留にする。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// reflectRunKind は、課題 cid の契約の実行（外部の実行 ID が externalRunID）の種類（kind）。無ければ空。
func (s *Store) reflectRunKind(ctx context.Context, cid int64, externalRunID string) (string, error) {
	var kind string
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		qerr := tx.QueryRowContext(ctx, `SELECT kind FROM contract_run WHERE challenge_id = ? AND external_run_id = ?`, cid, externalRunID).Scan(&kind)
		if errors.Is(qerr, sql.ErrNoRows) {
			return nil
		}
		return qerr
	})
	return kind, classifyReadWriteErr(err)
}

// isReflectRun は rep が指す契約の実行が、反映の依頼（kind が reflect）か。
func (s *Store) isReflectRun(ctx context.Context, rep *ContractRunReport) (bool, error) {
	cid, ok := parseChallengeID(rep.ChallengeID)
	if !ok {
		return false, nil
	}
	kind, err := s.reflectRunKind(ctx, cid, rep.ExternalRunID)
	return kind == contractRunKindReflect, err
}

func reflectEndedQuestion(runID string, state ContractState, summary string) string {
	return fmt.Sprintf("接続ツールの run（%s）の、統合先から反映先へ上げる依頼が、反映先へ入れる前に終わった（状態: %s）。委譲先の要約: %s\n", runID, state, summary) +
		"課題を着手中へ戻して保留にした。委譲先を確かめてから回答するか、課題を取り下げる。回答すると、次の委譲の段が上げる依頼を始め直す。"
}

func reflectNoLandablePRQuestion(runID string) string {
	return fmt.Sprintf("接続ツールの run（%s）のレビュー待ちに、反映先（既定ブランチ）宛ての成果物（PR）が見つからない。統合先から反映先へ上げる依頼は、反映先宛ての成果物がちょうど 1 つ要るので、検証中へ進めなかった。", runID) +
		"委譲先の出力を直してから回答するか、課題を取り下げる。回答しても、回答の文は委譲先へ返さない。次に読んだレビュー待ちも同じなら、もう一度保留にする。"
}

func reflectUnavailableQuestion(challengeRepo string) string {
	return fmt.Sprintf("統合先から反映先へ上げる依頼を始められない。リポジトリ（%s）の接続ツールに、反映の依頼の口（commands.reflect）が宣言されていない。", challengeRepo) +
		"口を宣言に足してから回答するか、課題を取り下げる。回答すると、次の委譲の段が上げる依頼を始め直す。"
}

func reflectFailedQuestion(detail string) string {
	return "統合先から反映先へ上げる依頼を始められなかった。" + detail + "\n委譲先を確かめてから回答するか、課題を取り下げる。回答すると、次の委譲の段が上げる依頼を始め直す。"
}

func integrationRefMissingQuestion() string {
	return "統合先から反映先へ上げる依頼を始められない。この課題に統合先の識別が記録されていない。" +
		"統合先を確かめてから回答するか、課題を取り下げる。回答すると、次の委譲の段が上げる依頼を始め直す（記録が無いままなら、もう一度保留にする）。"
}

// closeReflectOperation は、上げる依頼（課題 cid の、外部の実行 ID が externalRunID の契約の実行）が結び付いた、承認待ち・承認済みの
// 反映の操作を、遷移 tc と同じトランザクションで閉じる（無ければ何もしない）。
func closeReflectOperation(ctx context.Context, tc *transitionCtx, cid int64, externalRunID string) error {
	rows, err := tc.tx.QueryContext(ctx,
		`SELECT o.id, o.version, o.state FROM operation o JOIN contract_run k ON k.operation_id = o.id
		 WHERE k.challenge_id = ? AND k.external_run_id = ? AND o.kind = ? AND o.state IN (?, ?) ORDER BY o.id`,
		cid, externalRunID, string(OperationKindRelease), string(OperationStatePending), string(OperationStateApproved))
	if err != nil {
		return err
	}
	type target struct {
		id      int64
		version int
		state   string
	}
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.version, &t.state); err != nil {
			_ = rows.Close()
			return err
		}
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, t := range targets {
		res, err := tc.tx.ExecContext(ctx,
			`UPDATE operation SET state = ?, version = ?, closed_reason = ?, closed_at = ? WHERE id = ? AND version = ? AND state = ?`,
			string(OperationStateClosed), t.version+1, string(ClosedReasonSuperseded), tc.nowStr, t.id, t.version, t.state)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return fmt.Errorf("core: reflect ended: expected to close operation %s, updated %d rows", formatOperationID(t.id), n)
		}
		if err := tc.rec.record("operation", t.id, ActivityActionOpClose,
			map[string]any{"state": t.state},
			map[string]any{"state": string(OperationStateClosed), "version": t.version + 1, "closed_reason": string(ClosedReasonSuperseded),
				"reason": "統合先から反映先へ上げる依頼が、反映先へ入れる前に終わったため、反映の操作を閉じた"}); err != nil {
			return err
		}
	}
	return nil
}

// holdReflectEnded は、完了でない課題の上げる依頼が終わりの状態を返したとき（契約の実行は、読んだ記録と同じ書き込みで終わった記録になっている）、
// 作っていた反映の操作を閉じ、課題を着手中へ戻して、理由 reflect_failed で保留にする。課題が着手中・検証中のときだけ行う（保留中などは何もしない）。
func (s *Store) holdReflectEnded(ctx context.Context, rep *ContractRunReport) error {
	cid, ok := parseChallengeID(rep.ChallengeID)
	if !ok {
		return nil
	}
	var status Status
	if err := classifyReadWriteErr(s.db.Read(ctx, func(tx *sql.Tx) error {
		ch, err := loadChallenge(ctx, tx, cid)
		if err != nil || ch == nil {
			return err
		}
		status = ch.Status
		return nil
	})); err != nil {
		return err
	}
	ctx = context.WithoutCancel(ctx)
	tolerated := func(err error) bool {
		return errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) || errors.Is(err, errReconcileNotEligible)
	}
	switch status {
	case StatusVerifying:
		_, err := s.transitionAsInvoker(ctx, rep.RunID, rep.ChallengeID, OpVerifyNotMet,
			func(ctx context.Context, tc *transitionCtx) error {
				return closeReflectOperation(ctx, tc, cid, rep.ExternalRunID)
			})
		if err != nil {
			if tolerated(err) {
				return nil
			}
			return err
		}
	case StatusInProgress:
	default:
		return nil
	}
	question := reflectEndedQuestion(rep.ExternalRunID, rep.State, rep.Summary)
	_, err := s.transitionAsInvoker(ctx, rep.RunID, rep.ChallengeID, OpHold,
		func(ctx context.Context, tc *transitionCtx) error {
			if tc.current.Status != StatusInProgress {
				return errReconcileNotEligible
			}
			if err := closeReflectOperation(ctx, tc, cid, rep.ExternalRunID); err != nil {
				return err
			}
			return s.insertHoldWithOccurrence(ctx, tc, question, holdReasonReflectFailed, "")
		})
	if err != nil {
		if tolerated(err) {
			return nil
		}
		return err
	}
	rep.Held = true
	return nil
}

// checkReflectPRs は、上げる依頼のレビュー待ちの成果物の、反映先宛ての PR がちょうど 1 つであることを確かめる。当たれば PR を返す。
// 当たらなければ課題を保留にして（理由 content_ref_invalid）nil を返す。
func (s *Store) checkReflectPRs(ctx context.Context, rep *ContractRunReport, rs *contractReleaseSource, hold func(string, holdReason) error) (*UpstreamPullRequest, error) {
	var prs []UpstreamPullRequest
	if rs != nil && rs.Reconcile != nil {
		var err error
		if prs, err = rs.defaultBranchPRs(context.WithoutCancel(ctx), rep.Artifacts); err != nil {
			if rerr := s.restoreFactsCount(ctx, rep); rerr != nil {
				return nil, rerr
			}
			return nil, err
		}
	}
	if len(prs) == 1 {
		return &prs[0], nil
	}
	if err := s.restoreFactsCount(ctx, rep); err != nil {
		return nil, err
	}
	if len(prs) == 0 {
		return nil, hold(reflectNoLandablePRQuestion(rep.ExternalRunID), holdReasonContentRefInvalid)
	}
	return nil, hold(tooManyLandablePRsQuestion(rep.ExternalRunID, len(prs)), holdReasonContentRefInvalid)
}

// submitReflectReview は課題が着手中なら、上げる依頼のレビュー待ちの PR の反映の操作を作り（作り方はタスクの実行のレビュー待ちと同じ）、
// 契約の実行の operation_id へ記録し、成果物を取り込んで課題を検証中へ進める（タスクは済みのまま）。
func (s *Store) submitReflectReview(ctx context.Context, rep *ContractRunReport, rs *contractReleaseSource, pr UpstreamPullRequest) error {
	if err := s.registerContractReleases(ctx, rs, rep); err != nil {
		return err
	}
	runInt, ok := parseRunID(rep.RunID)
	cid, cok := parseChallengeID(rep.ChallengeID)
	if !ok || !cok {
		return nil
	}
	_, err := s.transitionAsInvoker(context.WithoutCancel(ctx), rep.RunID, rep.ChallengeID, OpSubmit,
		func(ctx context.Context, tc *transitionCtx) error {
			if tc.current.Status != StatusInProgress {
				return errReconcileNotEligible
			}
			prs, err := importContractArtifacts(ctx, tc.tx, runInt, rep.Artifacts, s.currentTime())
			rep.PRs = append(rep.PRs, prs...)
			if err != nil {
				return err
			}
			_, err = tc.tx.ExecContext(ctx,
				`UPDATE contract_run SET operation_id = (SELECT id FROM operation WHERE challenge_id = ? AND kind = ? AND ref = ? AND state IN (?, ?) ORDER BY id DESC LIMIT 1)
				 WHERE challenge_id = ? AND external_run_id = ?`,
				cid, string(OperationKindRelease), pr.URL, string(OperationStatePending), string(OperationStateApproved), cid, rep.ExternalRunID)
			return err
		})
	if err != nil && !errors.Is(err, ErrInvalidTransition) && !errors.Is(err, ErrTerminalState) && !errors.Is(err, errReconcileNotEligible) {
		return err
	}
	return nil
}

// holdChallengeForReflect は課題が着手中なら、上げる依頼を始められない保留を理由 reason で立てる（保留はどのタスクにも結び付けない）。
func (s *Store) holdChallengeForReflect(ctx context.Context, challengeID, question string, reason holdReason) error {
	actor, err := s.resolveActor()
	if err != nil {
		return err
	}
	_, err = s.runTransition(context.WithoutCancel(ctx), runTransitionParams{
		actor: actor, channel: ChannelInvoker, verification: VerificationNone, id: challengeID, op: OpHold, preceding: NoStatus,
	}, func(ctx context.Context, tc *transitionCtx) error {
		if tc.current.Status != StatusInProgress {
			return errReconcileNotEligible
		}
		return s.insertHoldWithOccurrence(ctx, tc, question, reason, "")
	})
	if err != nil && !errors.Is(err, ErrInvalidTransition) && !errors.Is(err, ErrTerminalState) && !errors.Is(err, errReconcileNotEligible) {
		return err
	}
	return nil
}

// reflectCandidates は、上げる依頼を始める条件に当たる課題の内部整数 ID（優先度→ID の昇順）。only が非 nil ならその課題だけ。
// 条件: 統合先を使う・着手中で、未着手・着手中・検証中のタスクが 0 個、済みのタスクが 1 個以上（取りやめは数えない）、終わっていない契約の実行が無い。
// 保留中の課題は着手中でないので、対象にならない。
func (s *Store) reflectCandidates(ctx context.Context, only *int64) ([]int64, error) {
	var out []int64
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		chs, err := loadChallengesByStatusSorted(ctx, tx, StatusInProgress)
		if err != nil {
			return err
		}
		for _, ch := range chs {
			cid, ok := parseChallengeID(ch.ID)
			if !ok || (only != nil && *only != cid) {
				continue
			}
			integ, err := challengeUsesIntegration(ctx, tx, cid)
			if err != nil {
				return err
			}
			if integ == nil || !*integ {
				continue
			}
			tasks, err := listTasks(ctx, tx, cid)
			if err != nil {
				return err
			}
			unfinished, done := 0, 0
			for _, t := range tasks {
				switch t.Status {
				case TaskStatusPending, TaskStatusInProgress, TaskStatusVerifying:
					unfinished++
				case TaskStatusDone:
					done++
				}
			}
			if unfinished > 0 || done == 0 {
				continue
			}
			var open int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM contract_run WHERE challenge_id = ? AND ended_at IS NULL`, cid).Scan(&open); err != nil {
				return err
			}
			if open == 0 {
				out = append(out, cid)
			}
		}
		return nil
	})
	return out, classifyReadWriteErr(err)
}

// startIntegrationReflects は委譲の段で、上げる依頼を始める条件に当たる課題ごとに、上げる依頼を始める。対象の課題が 1 つでもあれば
// handled を真で返す（ID を指定した呼び出しは、その課題をタスクの起動へ進めない）。
func (s *Store) startIntegrationReflects(ctx context.Context, in DelegateInput) (res *DelegateResult, handled bool, err error) {
	res = &DelegateResult{}
	if in.Contract == nil || in.ConnDecl == nil || in.AgentDecl == nil {
		return res, false, nil
	}
	var only *int64
	if in.ChallengeID != nil {
		n, ok := parseChallengeID(*in.ChallengeID)
		if !ok {
			return res, false, nil
		}
		only = &n
	}
	ids, err := s.reflectCandidates(ctx, only)
	if err != nil {
		return nil, false, err
	}
	for _, cid := range ids {
		handled = true
		item, ns, err := s.startIntegrationReflect(ctx, in, cid)
		if err != nil {
			return nil, false, err
		}
		appendDelegateOutcome(res, item, ns)
	}
	return res, handled, nil
}

// startIntegrationReflect は課題 cid の上げる依頼を始める。始めたら item、始めなかったら（枠・予算・別の実行）理由つきの ns を返す。
// 保留にしたときは、どちらも nil。
func (s *Store) startIntegrationReflect(ctx context.Context, in DelegateInput, cid int64) (*JudgmentAutoItem, *NotStarted, error) {
	challengeID := formatChallengeID(cid)
	notStarted := func(r NotStartedReason, detail string) *NotStarted {
		return &NotStarted{ChallengeID: challengeID, Reason: r, Detail: detail}
	}
	var ref string
	var repos []string
	var planVersion *int64
	if err := classifyReadWriteErr(s.db.Read(ctx, func(tx *sql.Tx) error {
		var r sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT integration_ref FROM challenge WHERE id = ?`, cid).Scan(&r); err != nil {
			return err
		}
		ref = r.String
		tasks, err := listTasks(ctx, tx, cid)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, t := range tasks {
			if t.Status == TaskStatusDone && !seen[t.Repo] {
				seen[t.Repo] = true
				repos = append(repos, t.Repo)
			}
		}
		if p, found, err := loadApprovedPlanAnySpec(ctx, tx, cid); err != nil {
			return err
		} else if found {
			v := int64(p.Version)
			planVersion = &v
		}
		return nil
	})); err != nil {
		return nil, nil, err
	}
	if ref == "" {
		return nil, nil, s.holdChallengeForReflect(ctx, challengeID, integrationRefMissingQuestion(), holdReasonIntegrationRefInvalid)
	}
	if len(repos) != 1 {
		return nil, notStarted(NotStartedIntegrationUnavailable, "統合先を使う課題は、済みのタスクのリポジトリが 1 つのときだけ、上げる依頼を始める"), nil
	}
	decl, _ := in.ConnDecl.findConnectorRepo(repos[0])
	var connector *Connector
	if decl != nil {
		connector = in.ConnDecl.ReflectConnectorFor(*decl)
	}
	if connector == nil {
		return nil, nil, s.holdChallengeForReflect(ctx, challengeID, reflectUnavailableQuestion(repos[0]), holdReasonReflectUnavailable)
	}
	holdFailed := func(detail string) error {
		return s.holdChallengeForReflect(ctx, challengeID, reflectFailedQuestion(detail), holdReasonReflectFailed)
	}
	command, err := fillContractArgsWith(connector.Commands.Reflect, contractFill{ChallengeID: challengeID, EmptySource: true, ArtifactRef: ref})
	if err != nil {
		return nil, nil, holdFailed("宣言の commands.reflect の引数を埋められなかった（" + err.Error() + "）。")
	}
	// 同時に走らせる数（走っている反映の依頼も数える）。作業ディレクトリを決める処理より先に確かめる。
	agent := in.AgentDecl
	var active int
	if err := classifyReadWriteErr(s.db.Read(ctx, func(tx *sql.Tx) (err error) {
		active, err = activeDelegationCount(ctx, tx)
		return err
	})); err != nil {
		return nil, nil, err
	}
	if active >= agent.MaxParallelRuns {
		return nil, notStarted(NotStartedSlotUnavailable, ""), nil
	}
	workDir, ok, err := s.repoIdleWorkDir(ctx, *decl)
	if err != nil && errors.Is(err, ErrStoreError) {
		return nil, nil, err
	}
	if err != nil {
		return nil, nil, holdFailed("上げる依頼の作業ディレクトリを決められなかった（" + err.Error() + "）。")
	}
	if !ok {
		if slotProvider(decl.Slots.Provider) == slotProviderClone && len(decl.Slots.Paths) > 0 {
			return nil, notStarted(NotStartedSlotUnavailable, ""), nil
		}
		return nil, nil, holdFailed("上げる依頼の作業ディレクトリが宣言されていない（リポジトリ " + decl.Name + "）。")
	}
	sessionID, err := newLowercaseUUIDv4()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: generate session id: %w", ErrStoreError, err)
	}
	var cycleIDInt *int64
	if n, ok := parseCycleID(in.CycleID); ok {
		cycleIDInt = &n
	}
	maxMicros := usdToMicros(agent.MaxRunBudgetUSD)
	runIDInt, err := s.beginContractRunMarked(ctx, cid, cycleIDInt, planVersion, sessionID, maxMicros, true, nil, nil, true)
	switch {
	case errors.Is(err, ErrBudgetExceeded):
		return nil, notStarted(NotStartedCycleBudget, ""), nil
	case errors.Is(err, ErrRunInProgress):
		return nil, notStarted(NotStartedInProgress, "課題に終わっていない実行がある"), nil
	case errors.Is(err, errDelegationNotEligible):
		return nil, nil, nil
	case err != nil:
		return nil, nil, err
	}
	rep, err := s.finishContractCall(ctx, contractCall{
		ChallengeID: challengeID, ChallengeIDInt: cid, RunIDInt: runIDInt, Connector: connector.ID,
		Launch: ContractLaunchInput{
			Workspace: s.workspace, WorkDir: s.contractWorkDir(workDir), RunDir: runDirPath(s.workspace, formatRunID(runIDInt)),
			Command: command, TimeoutSec: agent.TimeoutSec.Delegate,
		},
		Invoke: in.Contract.Start, MaxMicros: maxMicros,
		// 始められなかったときの保留は、理由 reflect_failed で下の処理が立てる（契約の実行の記録の失敗の保留にしない）。
		Kind: contractRunKindReflect, ReleaseStage: true,
	})
	if err != nil {
		return nil, nil, err
	}
	// 契約の実行の終わっていない記録が作られていなければ、始められなかった（失敗・時間切れ・不正な出力・start の最中の回収・終わった状態の応答）。
	var created int
	if err := classifyReadWriteErr(s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM contract_run WHERE start_run_id = ? AND ended_at IS NULL`, runIDInt).Scan(&created)
	})); err != nil {
		return nil, nil, err
	}
	if created == 0 {
		detail := fmt.Sprintf("commands.reflect が、終わっていない契約の実行を始めなかった（結果: %s。状態: %s）。%s", rep.Result, rep.State, rep.Error)
		if rep.Result == RunResultSucceeded {
			detail = fmt.Sprintf("commands.reflect が、終わった状態（%s）を返した。委譲先の要約: %s", rep.State, rep.Summary)
		}
		_, err := s.holdContractWithReason(ctx, rep.RunID, challengeID, reflectFailedQuestion(detail), holdReasonReflectFailed)
		return nil, nil, err
	}
	rs := &contractReleaseSource{Reconcile: in.Reconcile, Decl: in.ConnDecl, J4: in.Judgment != nil, Checks: in.Checks, Compare: in.Compare}
	if err := s.afterContractRead(ctx, rep, connector, rs); err != nil {
		return nil, nil, err
	}
	return &JudgmentAutoItem{ChallengeID: rep.ChallengeID, RunID: rep.RunID, Result: rep.Result}, nil, nil
}

// challengeStatusIs は課題 cid の状態が want か。
func (s *Store) challengeStatusIs(ctx context.Context, cid int64, want Status) (bool, error) {
	var ok bool
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		ch, err := loadChallenge(ctx, tx, cid)
		ok = err == nil && ch != nil && ch.Status == want
		return err
	})
	return ok, classifyReadWriteErr(err)
}
