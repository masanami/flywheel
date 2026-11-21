package core

// このファイルは、統合先を使う課題の「上げる依頼」（統合先から反映先へ上げる反映の依頼。契約の実行の kind が reflect で、
// 完了でない課題のもの）を、委譲の段から始める・状態の確認で読む処理を持つ（docs/features/core-redesign.md §課題の単位の確かめと、反映先へ上げる）:
//
//   - 始める（startIntegrationReflects）: 統合先を使う、着手中の課題で、未着手・着手中・検証中のタスクが無く、済みのタスクが 1 個以上あり、
//     終わっていない契約の実行が無いとき、commands.reflect を、{artifact_ref} に課題に記録した統合先の識別を入れて呼ぶ。
//     契約の実行は kind が reflect、task_id と operation_id が NULL。
//   - レビュー待ちを受け取る（checkReflectPRs・submitReflectReview）: 反映先宛ての PR がちょうど 1 つなら、その PR の反映の操作を作って
//     契約の実行へ結び付け、課題を検証中へ進める（タスクは済みのまま）。
//   - 終わりの状態を読んだ（holdReflectEnded）: 反映の操作を閉じ、契約の実行を終わった記録にし、理由 reflect_failed で保留にする（1 つの遷移）。

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
	return fmt.Sprintf("委譲先の実行（%s）の、統合先から反映先へ上げる依頼が、反映先へ入れる前に終わった（%s）。委譲先の要約: %s\n", runID, contractStateWord(state), summary) +
		"課題を着手中へ戻して保留にした。委譲先を確かめてから回答するか、課題を取り下げる。回答すると、次の委譲の段が上げる依頼を始め直す。"
}

func reflectNoLandablePRQuestion(runID string) string {
	return fmt.Sprintf("委譲先の実行（%s）のレビュー待ちに、反映先宛ての成果物が見つからない。統合先から反映先へ上げる依頼は、反映先宛ての成果物がちょうど 1 つ要るので、検証中へ進めなかった。", runID) +
		"委譲先の出力を直してから回答するか、課題を取り下げる。回答しても、回答の文は委譲先へ返さない。次に読んだレビュー待ちも同じなら、もう一度保留にする。"
}

func reflectUnavailableQuestion(challengeRepo string) string {
	return fmt.Sprintf("統合先から反映先へ上げる依頼を始められない。リポジトリ（%s）の委譲先に、反映の依頼の口（commands.reflect）が宣言されていない。", challengeRepo) +
		"口を宣言に足してから回答するか、課題を取り下げる。回答すると、次の委譲の段が上げる依頼を始め直す。"
}

func reflectFailedQuestion(detail string) string {
	return "統合先から反映先へ上げる依頼を始められなかった。" + detail + "\n委譲先を確かめてから回答するか、課題を取り下げる。回答すると、次の委譲の段が上げる依頼を始め直す。"
}

// reflectNoReleaseOperationQuestion は、上げる依頼の成果物（ref）の、承認できる反映の操作を作れなかったときの問い。
// 回答の後に取れる道（取り下げる・手で反映の操作を足す）を書く。何もせずに回答しても、同じ成果物で作れないのでもう一度保留になる。
func reflectNoReleaseOperationQuestion(challengeID, ref string) string {
	return fmt.Sprintf("統合先から反映先へ上げる依頼の成果物（%s）について、承認して反映先へ入れる操作（反映の操作）を作れなかった。"+
		"同じ成果物の反映の操作が、置き換え・差し戻し以外の理由で閉じているため、自動では作り直さない。\n", ref) +
		fmt.Sprintf("次のどちらかで進める。①課題を取り下げる（`flywheel withdraw %s --reason <理由>`）。"+
			"②手で反映の操作を足す（`flywheel op add %s --kind release --ref %s --summary <要約>`）。", challengeID, challengeID, ref) +
		"足してから回答すると、次の委譲の段が、足した反映の操作を成果物に結び付けて、課題を検証中へ進める。足さずに回答すると、同じ理由でもう一度保留にする。"
}

func integrationRefMissingQuestion() string {
	return "統合先から反映先へ上げる依頼を始められない。この課題に統合先の識別が記録されていない。" +
		"統合先を確かめてから回答するか、課題を取り下げる。回答すると、次の委譲の段が上げる依頼を始め直す（記録が無いままなら、もう一度保留にする）。"
}

// closeReflectOperation は、上げる依頼（課題 cid の、外部の実行 ID が externalRunID の契約の実行）が結び付いた、承認待ち・承認済みの
// 反映の操作を、遷移 tc と同じトランザクションで閉じる（無ければ何もしない）。
func closeReflectOperation(ctx context.Context, tc *transitionCtx, cid int64, externalRunID string) error {
	return closeReflectOperations(ctx, tc, cid, externalRunID, "統合先から反映先へ上げる依頼が、反映先へ入れる前に終わったため、反映の操作を閉じた")
}

// closeReflectOperations は closeReflectOperation の、対象の上げる依頼（externalRunID が空なら、課題の上げる依頼のすべて）と、作業ログへ書く理由 why を
// 指定する版。
func closeReflectOperations(ctx context.Context, tc *transitionCtx, cid int64, externalRunID, why string) error {
	// externalRunID が空のときは、上げる依頼との結び付きによらず、課題の待っている反映の操作のすべて（完了から戻した後に置き換えて作った操作を含む）。
	rows, err := tc.tx.QueryContext(ctx,
		`SELECT o.id, o.version, o.state FROM operation o
		 WHERE o.challenge_id = ? AND o.kind = ? AND o.state IN (?, ?)
		   AND (? = '' OR o.id IN (SELECT k.operation_id FROM contract_run k WHERE k.challenge_id = ? AND k.external_run_id = ? AND k.kind = ?))
		 ORDER BY o.id`,
		cid, string(OperationKindRelease), string(OperationStatePending), string(OperationStateApproved),
		externalRunID, cid, externalRunID, contractRunKindReflect)
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
				"reason": why}); err != nil {
			return err
		}
	}
	return nil
}

// holdReflectEnded は、完了でない課題の上げる依頼が終わりの状態を返したとき（契約の実行は、読んだ記録と同じ書き込みで終わった記録になっている）、
// 作っていた反映の操作を閉じ、理由 reflect_failed で保留にする（保留への遷移 1 つ・トランザクション 1 つ。回答で戻る先は着手中）。
// 課題が着手中・検証中のときだけ行う。保留中の課題は、契約の実行を開けたまま残し（回答の後の状態の確認が同じ終わりを読み直す）、取り下げなどは終わった記録にするだけ。
func (s *Store) holdReflectEnded(ctx context.Context, rep *ContractRunReport) error {
	cid, ok := parseChallengeID(rep.ChallengeID)
	if !ok {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	question := reflectEndedQuestion(rep.ExternalRunID, rep.State, rep.Summary)
	// 1 つの遷移（保留 T11。検証中からも着手中からも入れる）・1 つのトランザクションで、反映の操作を閉じて保留を立てる。
	// 検証中から入るときも、保留に入る前の状態（回答で戻る先）は着手中にする（上げる依頼は終わったので、
	// 検証中へ戻すと待つものが無くなる。着手中へ戻れば、回答の後に次の委譲の段が上げる依頼を始め直す）。
	_, err := s.transitionAsInvoker(ctx, rep.RunID, rep.ChallengeID, OpHold,
		func(ctx context.Context, tc *transitionCtx) error {
			if tc.current.Status != StatusInProgress && tc.current.Status != StatusVerifying {
				return errReconcileNotEligible
			}
			if err := closeReflectOperation(ctx, tc, cid, rep.ExternalRunID); err != nil {
				return err
			}
			if err := endReflectRunRecord(ctx, tc.tx, cid, rep.ExternalRunID, tc.nowStr); err != nil {
				return err
			}
			return s.insertHoldWithOccurrenceFrom(ctx, tc, question, holdReasonReflectFailed, "", StatusInProgress)
		})
	if err != nil {
		if errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) || errors.Is(err, errReconcileNotEligible) {
			// 保留中の課題は、終わりを記録するだけで、行を開けたまま残す（回答で着手中・検証中へ戻った後の状態の確認が、同じ終わりを読み直して、
			// 反映の操作を閉じ、保留を立てる）。
			if held, herr := s.challengeStatusIs(ctx, cid, StatusAwaitingHuman); herr != nil || held {
				return herr
			}
			// 保留を立てられない課題（取り下げなど）は、読み直しても同じなので、契約の実行を終わった記録にする。
			return classifyReadWriteErr(s.db.Write(ctx, func(tx *sql.Tx) error {
				return endReflectRunRecord(ctx, tx, cid, rep.ExternalRunID, formatTimestamp(s.currentTime()))
			}))
		}
		return err
	}
	rep.Held = true
	return nil
}

// endReflectRunRecord は上げる依頼の契約の実行を、終わった記録にする（終わっていなければ）。
func endReflectRunRecord(ctx context.Context, tx *sql.Tx, cid int64, externalRunID, at string) error {
	_, err := tx.ExecContext(ctx, `UPDATE contract_run SET ended_at = ?, fix_pending_ref = NULL, fix_verified_ref = NULL, fix_rejected_ref = NULL WHERE challenge_id = ? AND external_run_id = ? AND ended_at IS NULL`, at, cid, externalRunID)
	return err
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
	noRelease := false
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
			res, err := tc.tx.ExecContext(ctx,
				`UPDATE contract_run SET operation_id = (SELECT id FROM operation WHERE challenge_id = ? AND kind = ? AND ref = ? AND state IN (?, ?) ORDER BY id DESC LIMIT 1)
				 WHERE challenge_id = ? AND external_run_id = ?
				 AND EXISTS (SELECT 1 FROM operation WHERE challenge_id = ? AND kind = ? AND ref = ? AND state IN (?, ?))`,
				cid, string(OperationKindRelease), pr.URL, string(OperationStatePending), string(OperationStateApproved), cid, rep.ExternalRunID,
				cid, string(OperationKindRelease), pr.URL, string(OperationStatePending), string(OperationStateApproved))
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n != 1 {
				// 承認できる反映の操作が無い。反映の操作の無い検証中を作らない（遷移ごと取りやめ、下で保留にする）。
				noRelease = true
				rep.PRs = rep.PRs[:len(rep.PRs)-len(prs)] // 遷移ごと巻き戻る取り込みは、報告にも載せない
				return errReconcileNotEligible
			}
			return nil
		})
	if err != nil && !errors.Is(err, ErrInvalidTransition) && !errors.Is(err, ErrTerminalState) && !errors.Is(err, errReconcileNotEligible) {
		return err
	}
	if noRelease {
		// 毎回の状態の確認で黙って取りやめ続けないよう、オーナーへ見せる。
		return s.holdChallengeForReflect(ctx, rep.ChallengeID, reflectNoReleaseOperationQuestion(rep.ChallengeID, pr.URL), holdReasonReflectFailed)
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
		detail := reflectNotStartedDetail(rep)
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

// reflectRunOperationClosed は、上げる依頼（kind が reflect）の契約の実行 row が結び付いた反映の操作が、閉じているか
// （課題の単位の確かめが満たしていない・完了の差し戻しで閉じた。閉じた操作の上げる依頼は、取り消すと決まっている）。
func (s *Store) reflectRunOperationClosed(ctx context.Context, row *contractRunRow) (bool, error) {
	if row.Kind != contractRunKindReflect {
		return false, nil
	}
	var n int
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM contract_run k JOIN operation o ON o.id = k.operation_id WHERE k.id = ? AND o.state = ? AND o.closed_reason IN (?, ?)`,
			row.ID, string(OperationStateClosed), string(ClosedReasonSuperseded), string(ClosedReasonChallengeRejected)).Scan(&n)
	})
	return n > 0, classifyReadWriteErr(err)
}

// obsoleteContractRun は、契約の実行 row がもう要らず、取り消すと決まっているとき、その理由（取り消しの依頼の注記）を返す（要るなら空）。
//   - 上げる依頼で、結び付いた反映の操作を閉じた（課題の単位の確かめが満たしていない・完了の差し戻し）
//   - タスクの実行で、結び付いたタスクが取りやめになった（作り直した計画の承認・課題の取り下げ）
//   - 上げる依頼で、始めた後に計画が承認し直された、または終わっていないタスクがある（作り直した計画の新しい版のタスクが、これから集める）
func (s *Store) obsoleteContractRun(ctx context.Context, row *contractRunRow) (string, error) {
	if closed, err := s.reflectRunOperationClosed(ctx, row); err != nil || closed {
		if err != nil {
			return "", err
		}
		return "反映の操作を閉じたため（課題の単位の確かめが満たしていない、または完了の差し戻し）、統合先から反映先へ上げる依頼の中止を依頼した。", nil
	}
	switch row.Kind {
	case contractRunKindReflect:
		var stale bool
		err := s.db.Read(ctx, func(tx *sql.Tx) error {
			var unfinished int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task WHERE challenge_id = ? AND status IN (?, ?, ?)`, row.ChallengeID,
				string(TaskStatusPending), string(TaskStatusInProgress), string(TaskStatusVerifying)).Scan(&unfinished); err != nil {
				return err
			}
			if unfinished > 0 {
				stale = true
				return nil
			}
			var startPlan sql.NullInt64
			if err := tx.QueryRowContext(ctx, `SELECT plan_version FROM run WHERE id = ?`, row.StartRunID).Scan(&startPlan); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if !startPlan.Valid {
				return nil
			}
			v, ok, err := approvedPlanVersionOf(ctx, tx, row.ChallengeID)
			stale = err == nil && ok && v > startPlan.Int64
			return err
		})
		if err = classifyReadWriteErr(err); err != nil || !stale {
			return "", err
		}
		return "計画を作り直して承認し直したため、統合先から反映先へ上げる依頼の中止を依頼した（新しい計画のタスクが済んだら、上げる依頼を始め直す）。", nil
	case contractRunKindTask:
		if row.TaskID == nil {
			return "", nil
		}
		var n int
		err := s.db.Read(ctx, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task WHERE id = ? AND status = ?`, *row.TaskID, string(TaskStatusCancelled)).Scan(&n)
		})
		if err = classifyReadWriteErr(err); err != nil || n == 0 {
			return "", err
		}
		return "タスクを取りやめにしたため（計画の作り直し・課題の取り下げ）、タスクの実行の中止を依頼した。", nil
	}
	return "", nil
}

// cancelObsoleteContractRun は、要らなくなった契約の実行を、宣言の commands.cancel で取り消す。終わりの状態が返れば、契約の実行を終わった記録にする
// （課題の状態は変えず、保留にもしない。成果物も取り込まない）。取り消せなかった（コマンドの失敗・終わりの状態でない）ときは、終わっていない記録のまま残し、
// 保留にもしない（次の状態の確認が取り消し直す。残っている間、新しい上げる依頼は始まらない）。
func (s *Store) cancelObsoleteContractRun(ctx context.Context, in ContractPollInput, row *contractRunRow, connector *Connector, binding *sourceBinding, planVersion, cycleID *int64, note string) (*ContractRunReport, error) {
	challengeID := formatChallengeID(row.ChallengeID)
	if row.State.Terminal() {
		// 記録した状態が終わりの行は、委譲先では終わっている。中止を呼ばず、終わった記録にする（課題の状態は変えず、保留にもしない）。
		if err := s.endRecordedTerminalRun(ctx, row); err != nil {
			return nil, err
		}
		return &ContractRunReport{ChallengeID: challengeID, ExternalRunID: row.ExternalRunID, Result: RunResultSucceeded, State: row.State, Summary: row.Summary}, nil
	}
	command, err := fillRowCommandArgs(connector, row, connector.Commands.Cancel, challengeID, binding, contractAnswer{})
	if err != nil {
		return nil, err
	}
	row.discard = true
	return s.runExistingContractCommand(ctx, in.Client, in.AgentDecl, existingContractWorkDir(row, in.WorkDir), row, "cancel", command, planVersion, cycleID, note)
}

// contractStateWord は契約の状態を、問いの文に出す言葉にする（内部の値をそのまま出さない）。
func contractStateWord(s ContractState) string {
	switch s {
	case ContractStateRunning:
		return "実行中"
	case ContractStateWaiting:
		return "待ち"
	case ContractStateSucceeded:
		return "成功で終了"
	case ContractStateFailed:
		return "失敗で終了"
	case ContractStateCancelled:
		return "中止で終了"
	}
	return "状態が不明"
}

// runResultWord は実行の結果を、問いの文に出す言葉にする（内部の値をそのまま出さない）。
func runResultWord(r RunResult) string {
	switch r {
	case RunResultLaunchFailed:
		return "起動に失敗"
	case RunResultTimedOut:
		return "時間切れ"
	case RunResultMalformed:
		return "応答の形が不正"
	case RunResultBudgetExhausted:
		return "予算切れ"
	case RunResultErrored:
		return "失敗"
	case RunResultInvalidOutput:
		return "出力が不正"
	case RunResultSucceeded:
		return "成功"
	case RunResultInterrupted:
		return "中断"
	}
	return "結果が不明"
}

// reflectNotStartedDetail は、上げる依頼の commands.reflect が、終わっていない契約の実行を始めなかったときの理由の文。
// 結果・状態は内部の値ではなく言葉で書く。委譲先が返した文字列（要約・エラー）はそのまま添える。
func reflectNotStartedDetail(rep *ContractRunReport) string {
	if rep.Result == RunResultSucceeded {
		return fmt.Sprintf("commands.reflect が、終わった状態（%s）を返した。委譲先の要約: %s", contractStateWord(rep.State), rep.Summary)
	}
	return fmt.Sprintf("commands.reflect が、終わっていない契約の実行を始めなかった（%s・%s）。%s", runResultWord(rep.Result), contractStateWord(rep.State), rep.Error)
}
