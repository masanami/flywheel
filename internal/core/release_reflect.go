package core

// このファイルは反映の段（release_exec.go）が、反映の依頼（契約の実行の kind が reflect）を始める・取り消す処理を持つ
// （docs/features/core-redesign.md §反映の段「反映の依頼の起動」）:
//
//   - 道「反映の依頼を始める」（startReflectRequest）: 課題に終わっていない契約の実行が無く、成果物のリポジトリに口があるとき、
//     口の委譲先の commands.reflect を呼ぶ。始める前に、同時に走らせる数とサイクルの費用の上限を確かめる。
//   - 対応する反映の依頼の取り消し（cancelReleaseRunNote）と、完了から戻す前の取り消し（cancelOpenReflectRunBeforeReopen）。
//
// 反映の依頼の状態の読み・「入れてよい」の返し方は release_contract.go（タスクの実行と同じ読み方）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
)

// releaseNotStartedError は、反映の段が試行を始めた後に、始めなかった理由（reason）が分かったことを表す。
// 開いた試行の行を残さずに終え、その理由を返す（slot_unavailable・cycle_budget）。
type releaseNotStartedError struct{ reason NotStartedReason }

func (e *releaseNotStartedError) Error() string {
	return fmt.Sprintf("core: release: the reflect request was not started (%s)", e.reason)
}

// releaseRepoFor は反映の操作 row の成果物（PR）のリポジトリの宣言（無ければ nil）。
func releaseRepoFor(opts ReleaseExecOptions, row *operationRow) *ConnectorRepo {
	if !row.Ref.Valid || opts.Connectors == nil {
		return nil
	}
	repo, _, ok := parseReleasePullRequestRef(row.Ref.String)
	if !ok {
		return nil
	}
	for i := range opts.Connectors.Repos {
		if strings.EqualFold(opts.Connectors.Repos[i].Remote, repo) {
			return &opts.Connectors.Repos[i]
		}
	}
	return nil
}

// startReflectRequest は、課題に終わっていない契約の実行が無い反映の操作の道を選ぶ。成果物のリポジトリに反映の依頼の口があれば
// 反映の依頼を始め（commands.reflect）、無ければ何も呼ばず flywheel の外での反映を待つ。
// 返すエラーは、ストアを読めなかったとき・始めなかった理由（*releaseNotStartedError）・実行の記録を作れないとき（errReleaseContractBusy）。
func (s *Store) startReflectRequest(ctx context.Context, row *operationRow, opts ReleaseExecOptions, observed string) (releaseJudgment, error) {
	outside := releaseJudgment{result: ExecutionResultWaitingOutside, observedHead: observed, route: ReleaseRouteOutside}
	decl := releaseRepoFor(opts, row)
	if decl == nil {
		return outside, nil
	}
	connector := opts.Connectors.ReflectConnectorFor(*decl)
	if connector == nil {
		return outside, nil
	}
	// 起動の前に分かる結果（契約の実行が無い）の route は null。route は始めた結果・始められなかった結果にだけ付ける。
	j := releaseJudgment{observedHead: observed}
	if opts.Contract == nil {
		j.result, j.detail = ExecutionResultUpstreamFailed, "could not start the reflect request: the contract client is not available"
		return j, nil
	}
	cid := row.ChallengeID
	challengeID := formatChallengeID(cid)
	// {artifact_ref} は、反映の操作の参照（PR）。統合先を使う課題では、課題に記録した統合先の識別。
	artifactRef := row.Ref.String
	var integ bool
	var integRef sql.NullString
	if err := s.db.Read(ctx, func(tx *sql.Tx) error {
		u, err := challengeUsesIntegration(ctx, tx, cid)
		if err != nil {
			return err
		}
		if integ = u != nil && *u; integ {
			return tx.QueryRowContext(ctx, `SELECT integration_ref FROM challenge WHERE id = ?`, cid).Scan(&integRef)
		}
		return nil
	}); classifyReadWriteErr(err) != nil {
		return releaseJudgment{}, classifyReadWriteErr(err)
	}
	if integ {
		if integRef.String == "" {
			j.result, j.detail = ExecutionResultUnsupported, "could not start the reflect request: the challenge has no integration ref"
			return j, nil
		}
		artifactRef = integRef.String
	}
	command, err := s.contractCommandArgs(connector, contractCmdReflect,
		contractFill{ChallengeID: challengeID, EmptySource: true, ArtifactRef: artifactRef})
	if err != nil {
		j.result, j.detail = ExecutionResultUnsupported, "could not fill the reflect command: "+err.Error()
		return j, nil
	}
	agent := opts.Agent
	if agent == nil {
		agent = defaultAgentDeclaration()
	}
	// 同時に走らせる数（走っている反映の依頼も数える。数え方は委譲の段と同じ）。作業ディレクトリを決める処理より先に確かめる。
	var active int
	if err := s.db.Read(ctx, func(tx *sql.Tx) (err error) {
		active, err = activeDelegationCount(ctx, tx)
		return err
	}); classifyReadWriteErr(err) != nil {
		return releaseJudgment{}, classifyReadWriteErr(err)
	}
	if active >= agent.MaxParallelRuns {
		return releaseJudgment{}, &releaseNotStartedError{reason: NotStartedSlotUnavailable}
	}
	workDir, ok, err := s.repoIdleWorkDir(ctx, *decl)
	if err != nil && errors.Is(err, ErrStoreError) {
		return releaseJudgment{}, err
	}
	if err != nil {
		j.result, j.detail = ExecutionResultUnsupported, "could not decide the working directory of the reflect request: "+err.Error()
		return j, nil
	}
	if !ok {
		if slotProvider(decl.Slots.Provider) == slotProviderClone && len(decl.Slots.Paths) > 0 {
			// 作業用のクローンが使用中で空きが無いだけ。承認は保ち、次の反映の段でやり直す。
			return releaseJudgment{}, &releaseNotStartedError{reason: NotStartedSlotUnavailable}
		}
		j.result, j.detail = ExecutionResultUnsupported, "could not decide the working directory of the reflect request: no working directory is declared for repository "+decl.Name
		return j, nil
	}
	// サイクルの費用の上限は、flywheel の run を記録するのと同じ書き込みで評価する（超えるなら何も残らない）。
	maxMicros := usdToMicros(agent.MaxRunBudgetUSD)
	runIDInt, err := s.beginReflectRun(ctx, row, opts.cycleID, maxMicros, agent.MaxParallelRuns)
	switch {
	case errors.Is(err, ErrBudgetExceeded):
		return releaseJudgment{}, &releaseNotStartedError{reason: NotStartedCycleBudget}
	case errors.Is(err, errReflectSlotFull):
		return releaseJudgment{}, &releaseNotStartedError{reason: NotStartedSlotUnavailable}
	case errors.Is(err, ErrRunInProgress):
		return releaseJudgment{}, errReleaseContractBusy
	case err != nil:
		return releaseJudgment{}, err
	}
	opID := row.ID
	rep, err := s.finishContractCall(ctx, contractCall{
		ChallengeID: challengeID, ChallengeIDInt: cid, RunIDInt: runIDInt, Connector: connector.ID,
		Launch: ContractLaunchInput{
			Workspace: s.workspace, WorkDir: s.contractWorkDir(workDir), RunDir: runDirPath(s.workspace, formatRunID(runIDInt)),
			Command: command, TimeoutSec: agent.TimeoutSec.Delegate,
		},
		Invoke: opts.Contract.Start, MaxMicros: maxMicros,
		Kind: contractRunKindReflect, OperationIDInt: &opID, ReleaseStage: true,
	})
	if err != nil {
		return releaseJudgment{}, err
	}
	// 契約の実行の記録が作られていなければ、始められなかった（失敗・時間切れ・不正な出力・start の最中の回収）。
	// 起動の応答がすでに終わった状態（failed・cancelled・succeeded）なら、終わっていない実行が残らず、次の段が始め直してしまうので始められなかった扱いにする。
	var created int
	if err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM contract_run WHERE start_run_id = ? AND ended_at IS NULL`, runIDInt).Scan(&created)
	}); classifyReadWriteErr(err) != nil {
		return releaseJudgment{}, classifyReadWriteErr(err)
	}
	if created == 0 {
		j.result, j.route = ExecutionResultReflectFailed, ReleaseRouteReflectRequest
		j.detail = fmt.Sprintf("commands.reflect did not start a contract run (%s): %s", rep.Result, rep.Error)
		return j, nil
	}
	j.result, j.route = ExecutionResultReflectStarted, ReleaseRouteReflectRequest
	return j, nil
}

// errReflectSlotFull は、反映の依頼を始める書き込みの中で、同時に走らせる数の上限に達していたことを表す（何も書かない）。
var errReflectSlotFull = errors.New("core: release: the maximum number of parallel runs is reached")

// beginReflectRun は反映の依頼の start の flywheel の run を記録する（タスクには結び付けない。課題の状態は問わない）。
// 課題に終わっていない run があれば ErrRunInProgress、サイクルの費用の上限を超えるなら ErrBudgetExceeded（何も書かない）。
// 読みと起動の間に承認が取り消されたときも「反映の依頼を始めるのは承認済みの反映の操作だけ」を守るため、書き込みの中で反映の操作が
// 承認済み（approved）のままで、試行を開いた時の版（row.Version。試行を開く書き込みは反映の操作の版を進めない）から変わっていないことを
// 確かめ直す。変わっていれば何も書かず ErrConflict（commands.reflect は呼ばれない）。
func (s *Store) beginReflectRun(ctx context.Context, row *operationRow, cycleID *int64, maxMicros int64, maxParallel int) (int64, error) {
	cid := row.ChallengeID
	sessionID, err := newLowercaseUUIDv4()
	if err != nil {
		return 0, fmt.Errorf("%w: generate session id: %w", ErrStoreError, err)
	}
	host, _ := os.Hostname()
	now := s.currentTime()
	var runIDInt int64
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		op, err := loadOperationRow(ctx, tx, row.ID)
		if err != nil {
			return err
		}
		if op.Version != row.Version || op.State != OperationStateApproved {
			return ErrConflict
		}
		cur, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		if cur == nil {
			return ErrNotFound
		}
		active, err := challengeHasActiveRun(ctx, tx, cid)
		if err != nil {
			return err
		}
		// レビュー待ちの実行は実行中に数えられないので、終わっていない契約の実行が無いことも、この書き込みの中で確かめ直す。
		var open int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM contract_run WHERE challenge_id = ? AND ended_at IS NULL`, cid).Scan(&open); err != nil {
			return err
		}
		if active || open > 0 {
			return errActiveRunExists
		}
		// 同時に走らせる数も、run を作る同じ書き込みの中で確かめ直す（読みから書き込みまでの間に埋まっていたら何も書かない）。
		running, err := activeDelegationCount(ctx, tx)
		if err != nil {
			return err
		}
		if running >= maxParallel {
			return errReflectSlotFull
		}
		if cycleID != nil {
			if err := evalCycleBudgetTx(ctx, tx, *cycleID, maxMicros); err != nil {
				return err
			}
		}
		var planVersion *int64
		if p, found, err := loadApprovedPlanAnySpec(ctx, tx, cid); err != nil {
			return err
		} else if found {
			v := int64(p.Version)
			planVersion = &v
		}
		runRow, err := insertRun(ctx, tx, cid, insertRunInput{
			CycleID: cycleID, Kind: runKindContract, ChallengeVersion: int64(cur.Version), PlanVersion: planVersion,
			SessionID: sessionID, PID: int64(os.Getpid()), Host: host, HeartbeatAt: now, StartedAt: now,
			MaxBudgetUSD: maxMicros, BudgetBucket: budgetBucketImpl, OperationID: &row.ID,
		})
		if err != nil {
			return err
		}
		n, ok := parseRunID(runRow.ID)
		if !ok {
			return fmt.Errorf("core: malformed run id %q", runRow.ID)
		}
		runIDInt = n
		return nil
	})
	if errors.Is(err, errActiveRunExists) {
		return 0, ErrRunInProgress
	}
	if err = classifyReadWriteErr(err); err != nil {
		return 0, err
	}
	return runIDInt, nil
}

// cancelsCorrespondingRun は、反映の操作が結果 result で終わるとき、対応する契約の実行を commands.cancel で取り消すか。
// 取り消すのは、委譲先の succeeded を読む以外の道で実行済み・閉じになる結果（already_merged・merged_unapproved・closed）と、
// 対応する実行が反映の依頼のときの取り消す結果すべて。対応するタスクの実行は、取り消す結果では取り消さない（レビュー待ちのまま残す）。
func cancelsCorrespondingRun(run *releaseRun, result ExecutionResult) bool {
	if run == nil || !run.corresponds {
		return false
	}
	switch result {
	case ExecutionResultAlreadyMerged, ExecutionResultMergedUnapproved, ExecutionResultClosed:
		return true
	}
	if run.row.Kind != contractRunKindReflect {
		return false
	}
	class, _ := result.Class()
	return class == ExecutionClassCancel
}

// cancelReflectRunOnLimit は、数える結果 j が連続の回数の上限に達して limit_reached で取り消すことになるとき、対応する反映の依頼を
// 取り消した結果の文を j.cancelNote へ入れて返す（上限に達するかは、結果を書く前の連続の回数で見る。書き込みで確かめ直す）。
func (s *Store) cancelReflectRunOnLimit(ctx context.Context, opts ReleaseExecOptions, opID int64, j releaseJudgment) (releaseJudgment, error) {
	run := j.run
	if run == nil || !run.corresponds || run.row.Kind != contractRunKindReflect || !j.result.Counts() {
		return j, nil
	}
	var prior int
	if err := s.db.Read(ctx, func(tx *sql.Tx) (err error) {
		prior, err = consecutiveFailures(ctx, tx, opID)
		return err
	}); classifyReadWriteErr(err) != nil {
		return j, classifyReadWriteErr(err)
	}
	if prior+1 >= judgmentFailureLimit(opts.Agent) {
		j.cancelNote = s.cancelReleaseRunNote(ctx, opts, run, nil)
	}
	return j, nil
}

// cancelOpenReflectRunBeforeReopen は、課題を完了から戻す前に、終わっていない反映の依頼を commands.cancel で取り消す。
// 取り消せなければ（コマンドの失敗・終わらない状態・契約のコマンドを呼べない）errReopenBlockedByRun。
func (s *Store) cancelOpenReflectRunBeforeReopen(ctx context.Context, run *releaseRun, cs ReopenContractSources) error {
	if cs.Contract == nil || cs.Connectors == nil {
		return errReopenBlockedByRun
	}
	opts := ReleaseExecOptions{Contract: cs.Contract, Agent: cs.Agent, Connectors: cs.Connectors}
	out, why, err := s.releaseContractCommand(ctx, opts, run, "cancel", contractFill{}, false)
	if errors.Is(err, errReleaseContractBusy) {
		return errReopenBlockedByRun
	}
	if err != nil {
		return err
	}
	if why != "" || out.Result != RunResultSucceeded || !out.State.Terminal() {
		return errReopenBlockedByRun
	}
	return nil
}
