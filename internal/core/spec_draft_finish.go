package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// このファイルは、下書きづくりの実行が終わった後の処理（docs/features/request-to-plan-s2b.md「実行が終わった後」）を持つ:
// 状態の確認と、待ちで返った実行の取り消し、元のクローンが書き換えられていないかの確かめ、返った問いと下書きの検査、
// 記録を済みにする書き込みと下書きづくりの保留の理由の選び分け。
//
// 実行の記録（contract_run）は、終わりを読んだだけでは終わった記録にしない。記録を済みにするか保留にする書き込みが、同じ
// 書き込みの中で終わった記録にする（途中で止まっても、次の状態の確認が同じ終わりを扱い直せる。実行中の記録だけが残って
// 行き止まりの失敗にならない）。

const (
	// specQuestionsFact は、残った問いの一覧（JSON の配列の文字列）が載る facts の名前。
	specQuestionsFact = "spec_questions"
	// specDraftDoneByFlow は、フローで済んだ（spec_draft_done の detail）。
	specDraftDoneByFlow = "flow"
	// flowOutcomeBudgetExhausted は、上限額に当たったステップの結果。
	flowOutcomeBudgetExhausted = "budget_exhausted"
	// flowOutcomeFailure は、ステップが自分で失敗と返した結果。
	flowOutcomeFailure = "failure"
	// specStepSummaryFact は、最後に出力を返したステップの要約が載る facts の名前。
	specStepSummaryFact = "spec_step_summary"

	maxSpecQuestions     = 10
	minSpecOptions       = 2
	maxSpecOptions       = 5
	maxSpecQuestionRunes = 2000
	// maxRewrittenPathsShown は、保留の問いの文に出す、増えたパスの数の上限。
	maxRewrittenPathsShown = 20
)

// pollSpecDraftRun は下書きづくりの実行 row の状態を 1 回読み、終わっていれば終わった後の処理をする。
// 待ちで返った実行は、取り消して失敗として扱う（答えの返し・レビュー待ちの取り込み・予算の上限での中止はしない）。
func (s *Store) pollSpecDraftRun(ctx context.Context, in ContractPollInput, row *contractRunRow, connector *Connector, binding *sourceBinding, planVersion, cycleID *int64) (*ContractRunReport, error) {
	challengeID := formatChallengeID(row.ChallengeID)
	call := func(row *contractRunRow, cmd contractCmd, name string) (*ContractRunReport, error) {
		command, err := s.fillRowCommandArgs(connector, row, cmd, challengeID, binding, contractAnswer{})
		if err != nil {
			return nil, err
		}
		return s.runExistingContractCommandWith(ctx, in.Client, in.AgentDecl, row.WorkDir, row, name, command, planVersion, cycleID, "", nil, true)
	}
	rep, err := call(row, contractCmdStatus, "status")
	if err != nil || rep.Result != RunResultSucceeded {
		return rep, err
	}
	waitFailure := ""
	switch {
	case rep.State == ContractStateWaiting:
		latest := *row
		latest.State, latest.Summary, latest.CostMicros = rep.State, rep.Summary, usdToMicros(rep.CostTotalUSD)
		crep, err := call(&latest, contractCmdCancel, "cancel")
		if err != nil {
			return nil, err
		}
		waitFailure = "下書きづくりの実行が待ち（waiting）で返ったため、取り消しました。"
		if crep.Result != RunResultSucceeded || !crep.State.Terminal() {
			waitFailure = fmt.Sprintf("下書きづくりの実行が待ち（waiting）で返りました。実行（%s）は取り消せませんでした（失敗の種類: %s）。次の状態の確認で取り消し直します。",
				row.ExternalRunID, crep.Result)
		}
		rep = crep
	case !rep.State.Terminal():
		return rep, nil
	}
	held, requeued, err := s.finishSpecDraftRun(ctx, in, rep.RunID, row.ID, waitFailure)
	if err != nil {
		return nil, err
	}
	rep.Held = held
	if requeued {
		rep.requeuedSpecDraft = row.ChallengeID
	}
	return rep, nil
}

// specDraftEnd は、終わった後の処理が読んだ、実行と記録の値。
type specDraftEnd struct {
	run    *contractRunRow
	draft  *SpecDraft
	status Status
	facts  map[string]string
	// hasStep はステップの記録が 1 件以上あるか。lastStep は最後のステップの名前、lastOutcome はその結果（無ければ空）。
	hasStep               bool
	lastStep, lastOutcome string
}

// loadSpecDraftEnd は終わっていない記録の実行 rowID の値を読む（終わった記録になっていれば nil）。
func loadSpecDraftEnd(ctx context.Context, tx *sql.Tx, rowID int64) (*specDraftEnd, error) {
	run, err := scanContractRun(tx.QueryRowContext(ctx, contractRunSelect+` WHERE id = ? AND ended_at IS NULL`, rowID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := &specDraftEnd{run: run}
	ch, err := loadChallenge(ctx, tx, run.ChallengeID)
	if err != nil {
		return nil, err
	}
	out.status = ch.Status
	if out.draft, err = loadSpecDraft(ctx, tx, run.ChallengeID); err != nil {
		return nil, err
	}
	var facts sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT facts FROM contract_run WHERE id = ?`, rowID).Scan(&facts); err != nil {
		return nil, err
	}
	if facts.Valid {
		if err := json.Unmarshal([]byte(facts.String), &out.facts); err != nil {
			return nil, fmt.Errorf("contract_run facts: %w", err)
		}
	}
	var outcome sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT step, outcome FROM contract_run_step WHERE contract_run_id = ? ORDER BY seq DESC LIMIT 1`, rowID).
		Scan(&out.lastStep, &outcome)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	out.hasStep = err == nil
	out.lastOutcome = outcome.String
	return out, nil
}

// endSpecContractRun は、状態が終わりの契約の実行を終わった記録にする。
func (s *Store) endSpecContractRun(ctx context.Context, tx *sql.Tx, run *contractRunRow) error {
	if !run.State.Terminal() {
		return nil
	}
	return requireOneRow(tx.ExecContext(ctx, `UPDATE contract_run SET ended_at = ? WHERE id = ? AND ended_at IS NULL`,
		formatTimestamp(s.currentTime()), run.ID))
}

// requireRunningSpecDraft は、書き込みの中で、記録がまだ実行中であることを確かめて返す。
func requireRunningSpecDraft(ctx context.Context, tx *sql.Tx, cid int64) (*SpecDraft, error) {
	d, err := loadSpecDraft(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	if d == nil || d.State != SpecDraftStateRunning {
		return nil, errSpecDraftHoldNotEligible
	}
	return d, nil
}

// finishSpecDraftRun は、読んだ下書きづくりの実行 rowID の終わりを、記録と課題へ写す。causeRunID は終わりを読んだ実行。
// 問いが残って問いの保留にする所では、呼べる条件（specSupervisorMayRun）を満たせば、先にスーパーバイザーを呼ぶ。requeued は、
// その答えで全部の問いが解けて、記録を実行待ちへ戻したか。
// waitFailure が空でなければ、待ちで返った実行（取り消せなかった実行は、状態が終わりでないまま開けておく）。
//   - 記録が実行中でなければ、記録も保留も変えない（状態が終わりの実行を、終わった記録にするだけ）。
//   - 課題が分類済でなければ（別の理由で人間対応待ち）、何も書かず、実行も開けたままにする（分類済へ戻った後の状態の確認が扱う）。
//
// held は、この呼び出しで下書きづくりの保留を作ったか。
func (s *Store) finishSpecDraftRun(ctx context.Context, in ContractPollInput, causeRunID string, rowID int64, waitFailure string) (held, requeued bool, err error) {
	var end *specDraftEnd
	if err := classifyReadWriteErr(s.db.Read(ctx, func(tx *sql.Tx) (err error) {
		end, err = loadSpecDraftEnd(ctx, tx, rowID)
		return err
	})); err != nil || end == nil {
		return false, false, err
	}
	if end.draft == nil || end.draft.State != SpecDraftStateRunning {
		err := s.db.Write(context.WithoutCancel(ctx), func(tx *sql.Tx) error { return s.endSpecContractRun(ctx, tx, end.run) })
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
		return false, false, classifyReadWriteErr(err)
	}
	if end.status != StatusClassified {
		return false, false, nil
	}
	hold, err := s.judgeSpecDraftEnd(ctx, in, end, waitFailure)
	if err != nil {
		return false, false, err
	}
	// 回数の上限の後と失敗には呼ばない（理由が spec_question の保留になる所だけ）。
	if hold != nil && hold.Reason == SpecDraftHoldQuestion && specSupervisorMayRun(in) {
		sv, err := s.superviseSpecQuestions(ctx, in, end, hold.Questions)
		if errors.Is(err, errSpecSupervisionInterrupted) || errors.Is(err, ErrRunInProgress) {
			return false, false, nil // 何も書かず、次の状態の確認が同じ終わりを扱い直す
		}
		if err != nil {
			return false, false, err
		}
		if sv != nil {
			return s.recordSpecSupervision(context.WithoutCancel(ctx), causeRunID, end.run, sv)
		}
	}
	// 判定した後は、呼び出し元の ctx の取り消しに関わらず書く。
	ctx = context.WithoutCancel(ctx)
	cid := end.run.ChallengeID
	if hold == nil {
		return false, false, s.completeSpecDraft(ctx, causeRunID, end.run)
	}
	if hold.Reason == SpecDraftHoldFailed {
		hold.DraftExists = s.specDraftFileExists(end.draft.Path)
	}
	_, err = s.transitionAsInvoker(ctx, causeRunID, formatChallengeID(cid), OpHold,
		func(ctx context.Context, tc *transitionCtx) error {
			if _, err := requireRunningSpecDraft(ctx, tc.tx, tc.id); err != nil {
				return err
			}
			if err := s.endSpecContractRun(ctx, tc.tx, end.run); err != nil {
				return err
			}
			return raiseSpecDraftHold(ctx, tc, *hold)
		})
	if errors.Is(err, errSpecDraftHoldNotEligible) || errors.Is(err, sql.ErrNoRows) ||
		errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) {
		return false, false, nil
	}
	return err == nil, false, err
}

// completeSpecDraft は、確かめを通った下書きづくりの記録を済みにする。書き込みの中で読み直し、課題が分類済でないか記録が
// 実行中でなければ、何も書かない。
func (s *Store) completeSpecDraft(ctx context.Context, causeRunID string, run *contractRunRow) error {
	runID, ok := parseRunID(causeRunID)
	if !ok {
		return ErrValidation
	}
	actor, err := s.resolveActor()
	if err != nil {
		return err
	}
	err = s.mutateAsRun(ctx, actor, ChannelInvoker, VerificationNone, &runID, func(tx *sql.Tx, rec *activityRecorder) error {
		ch, err := loadChallenge(ctx, tx, run.ChallengeID)
		if err != nil {
			return err
		}
		if ch.Status != StatusClassified {
			return errSpecDraftHoldNotEligible
		}
		d, err := requireRunningSpecDraft(ctx, tx, run.ChallengeID)
		if err != nil {
			return err
		}
		if err := s.endSpecContractRun(ctx, tx, run); err != nil {
			return err
		}
		if err := updateSpecDraft(ctx, tx, run.ChallengeID, specDraftUpdate{State: SpecDraftStateDone, Attempts: d.Attempts, Answers: d.Answers}, rec.at); err != nil {
			return err
		}
		// 課題の中身が変わるので、版を進める（前の版を読んだ判断の出力を写さない）。
		if err := requireOneRow(tx.ExecContext(ctx, `UPDATE challenge SET version = version + 1, updated_at = ? WHERE id = ? AND version = ?`,
			formatTimestamp(rec.at), run.ChallengeID, ch.Version)); err != nil {
			return err
		}
		return writeSpecDraftDone(ctx, tx, rec, run.ChallengeID, d, specDraftDoneByFlow)
	})
	if errors.Is(err, errSpecDraftHoldNotEligible) || errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

// judgeSpecDraftEnd は、実行中の記録の実行の終わりを確かめ、作る保留を返す（nil は、記録を済みにしてよい）。
// 何も書かない。誤りは、ctx の取り消しで確かめられなかったときだけ。
func (s *Store) judgeSpecDraftEnd(ctx context.Context, in ContractPollInput, end *specDraftEnd, waitFailure string) (*specDraftHold, error) {
	var failures []string
	// 元のクローンの確かめは、実行の結果によらず行う。
	added, checkErr := s.rewrittenRepoPaths(ctx, in, end)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch {
	case checkErr != nil:
		failures = append(failures, "対象のリポジトリの元のクローンが変わっていないかを確かめられませんでした: "+checkErr.Error())
	case len(added) > 0:
		failures = append(failures, rewrittenRepoDetail(added))
	}

	var questions []SpecDraftQuestion
	switch {
	case waitFailure != "":
		failures = append(failures, waitFailure)
	case end.run.State != ContractStateSucceeded:
		failures = append(failures, fmt.Sprintf("下書きづくりの実行が終わりました（%s）。%s", contractStateWord(end.run.State), end.run.Summary))
	default:
		var err error
		if questions, err = parseSpecQuestions(end.facts); err != nil {
			failures = append(failures, "下書きづくりの実行が返した問いを読めません: "+err.Error())
		} else if _, _, err := readSpecDocument(s.workspace, end.draft.Path); err != nil &&
			(len(questions) == 0 || !errors.Is(err, errSpecNoCriteria)) {
			// 問いが残るときは、受入基準がまだ無い下書きを失敗にしない。
			failures = append(failures, "下書きを使えません: "+err.Error())
		}
	}

	var failedStep *specDraftFailedStep
	if end.run.State == ContractStateFailed || end.run.State == ContractStateCancelled {
		if end.hasStep {
			failedStep = &specDraftFailedStep{Step: end.lastStep, Outcome: end.lastOutcome, Summary: end.facts[specStepSummaryFact]}
		}
		if end.lastOutcome == flowOutcomeBudgetExhausted || end.run.CostMicros >= end.run.MaxBudget {
			failures = append(failures, fmt.Sprintf("1 回の上限額（%s USD）に当たりました。費用: %s USD",
				formatUSDAmount(microsToUSD(end.run.MaxBudget)), formatUSDAmount(microsToUSD(end.run.CostMicros))))
		}
	}

	switch {
	case len(failures) > 0:
		return &specDraftHold{Reason: SpecDraftHoldFailed, Detail: strings.Join(failures, "\n\n"), FailedStep: failedStep}, nil
	case len(questions) == 0:
		return nil, nil
	case end.draft.Attempts >= SpecDraftMaxAttempts:
		return &specDraftHold{Reason: SpecDraftHoldAttemptLimit, Questions: questions}, nil
	}
	return &specDraftHold{Reason: SpecDraftHoldQuestion, Questions: questions}, nil
}

// rewrittenRepoPaths は、対象のリポジトリの元のクローンの変わっているパスのうち、始める前の一覧に無いものを返す
// （始める前の一覧と同じ書き方。formatRepoStatus）。比べるのはパスの集合で、始める前から変わっていたファイルの書き換えは見つけない。
func (s *Store) rewrittenRepoPaths(ctx context.Context, in ContractPollInput, end *specDraftEnd) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(runDirPath(s.workspace, formatRunID(end.run.StartRunID)), repoStatusBeforeFileName))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", repoStatusBeforeFileName, err)
	}
	var repo *ConnectorRepo
	if in.ConnDecl != nil {
		repo, _ = in.ConnDecl.findConnectorRepo(end.draft.Repo)
	}
	if repo == nil || in.Git == nil {
		return nil, fmt.Errorf("the repository %q cannot be inspected", end.draft.Repo)
	}
	changed, err := in.Git.ChangedPaths(ctx, SlotTree{Path: filepath.Join(s.workspace, repo.Slots.Base)})
	if err != nil {
		return nil, fmt.Errorf("read the changed paths of the repository %q: %w", repo.Name, err)
	}
	before := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		before[line] = true
	}
	var added []string
	for _, line := range strings.Split(formatRepoStatus(changed), "\n") {
		if line != "" && !before[line] {
			added = append(added, line)
		}
	}
	return added, nil
}

// rewrittenRepoDetail は、元のクローンに増えた変わっているパス added を伝える、保留の問いの文の一部。
func rewrittenRepoDetail(added []string) string {
	var b strings.Builder
	b.WriteString("リポジトリが書き換えられた: 対象のリポジトリの元のクローンに、始める前に無かった変わっているパスがあります。flywheel は戻しません。確かめて戻してください。")
	for _, p := range added[:min(len(added), maxRewrittenPathsShown)] {
		b.WriteString("\n- " + EscapeControl(p, false))
	}
	if rest := len(added) - maxRewrittenPathsShown; rest > 0 {
		fmt.Fprintf(&b, "\n（残り %d 件）", rest)
	}
	return b.String()
}

// parseSpecQuestions は facts の spec_questions（JSON の配列の文字列）を読み、形を検査する。問いが残っていなければ空。
func parseSpecQuestions(facts map[string]string) ([]SpecDraftQuestion, error) {
	raw, ok := facts[specQuestionsFact]
	if !ok {
		return nil, fmt.Errorf("facts has no %s", specQuestionsFact)
	}
	// null は誤りなく空の一覧に読めてしまうので、先に配列の形を確かめる。
	if !strings.HasPrefix(strings.TrimSpace(raw), "[") {
		return nil, fmt.Errorf("%s is not a JSON array", specQuestionsFact)
	}
	var out []SpecDraftQuestion
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("%s is not a JSON array of questions: %w", specQuestionsFact, err)
	}
	if len(out) > maxSpecQuestions {
		return nil, fmt.Errorf("%d questions, more than %d", len(out), maxSpecQuestions)
	}
	text := func(v string) bool {
		return strings.TrimSpace(v) != "" && utf8.RuneCountInString(v) <= maxSpecQuestionRunes
	}
	for i, q := range out {
		bad := !text(q.Question) || !text(q.Reason) || len(q.Options) < minSpecOptions || len(q.Options) > maxSpecOptions ||
			q.Recommended < 1 || q.Recommended > len(q.Options)
		for _, o := range q.Options {
			bad = bad || !text(o.Label)
		}
		if bad {
			return nil, fmt.Errorf("question %d is malformed", i+1)
		}
	}
	return out, nil
}
