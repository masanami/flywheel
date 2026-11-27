package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// このファイルは、下書きづくりを始める処理（docs/features/request-to-plan-s2b.md「下書きづくりを始める」「1 回の上限額」）を持つ。
// 始めるのはサイクルの委譲の段で、契約の実行の種類 spec として、タスクにも計画にも結び付けずに起動する。
// 起動できなかったときと、実行中の記録なのに終わっていない実行が無いときは、下書きづくりの保留（spec_failed）にする。
// 実行が終わった後の扱いは spec_draft_finish.go が持つ。

// SpecDraftDefaultBudgetUSD は下書きづくり 1 回の上限額の既定の値（USD）。埋め込んだフロー spec-draft の limits.budget_usd と同じ値にする。
const SpecDraftDefaultBudgetUSD = 15.0

const (
	specDraftFlowName = "spec-draft"
	// specBriefFileName は下書きづくりの文面のファイルの名前（実行の記録の置き場の下）。
	specBriefFileName = "spec-brief.md"
	// repoStatusBeforeFileName は、始める前の元のクローンの変わっているパスの一覧のファイルの名前（実行の記録の置き場の下）。
	repoStatusBeforeFileName = "repo-status-before.txt"
)

// specContractFill は、下書きづくりの開始のコマンドの引数に差し込む値。
type specContractFill struct {
	DraftFile, BriefFile, BriefDir, RepoDir string
	// BasePath は置き換える元の文書の場所（リポジトリの中の相対のパス）。持たない記録では空で、引数に入れない。
	BasePath     string
	BudgetMicros int64
}

// specDraftBudgetMicros は下書きづくり 1 回の上限額。委譲先の spec_draft_budget_usd（無ければ既定の値）と、
// エージェントの max_run_budget_usd の小さいほう。c は nil でよい。
func specDraftBudgetMicros(agent *AgentDeclaration, c *Connector) int64 {
	usd := SpecDraftDefaultBudgetUSD
	if c != nil && c.SpecDraftBudgetUSD != nil {
		usd = *c.SpecDraftBudgetUSD
	}
	return usdToMicros(min(usd, agent.MaxRunBudgetUSD))
}

// formatSpecBrief は下書きづくりの文面のファイル（Markdown）を組み立てる。中身の無い節は見出しごと書かない。
// binding は課題の取り込み元（無ければ nil）。
func formatSpecBrief(title, description string, binding *sourceBinding, d SpecDraft) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", oneLine(title))
	section := func(heading, body string) {
		if body = strings.Trim(body, "\n"); strings.TrimSpace(body) != "" {
			fmt.Fprintf(&b, "\n## %s\n%s\n", heading, body)
		}
	}
	section("説明", description)
	if binding != nil {
		section("取り込み元", fmt.Sprintf("- 指定: %s\n- URL: %s", binding.ExternalKey, binding.URL))
	}
	section("回す理由", d.Reason)
	if d.BasePath != nil {
		section("置き換える元", *d.BasePath)
	}
	var answers strings.Builder
	seq := map[int]int{}
	// 複数の行の値は、2 行目から字下げして、項目の中に収める（見出しや次の項目と読み違えないようにする）。
	item := func(v string) string { return strings.ReplaceAll(strings.TrimRight(v, "\n"), "\n", "\n  ") }
	var notes strings.Builder
	for _, a := range d.Answers {
		if a.Kind == SpecDraftAnswerKindNote {
			fmt.Fprintf(&notes, "### %d\n%s\n", a.Attempt, strings.Trim(a.Answer, "\n"))
			continue
		}
		seq[a.Attempt]++
		fmt.Fprintf(&answers, "### %d-%d\n- 問い: %s\n- 答え: %s\n- 答えた者: %s\n", a.Attempt, seq[a.Attempt], item(a.Question), item(a.Answer), item(a.By))
		for _, basis := range a.Basis {
			fmt.Fprintf(&answers, "- 根拠: %s（%s）\n", oneLine(basis.Path), oneLine(basis.Locator))
		}
	}
	section("これまでの問いと答え", answers.String())
	section("オーナーの添え書き", notes.String())
	return b.String()
}

// formatRepoStatus は、変わっているパスの一覧のファイルの中身（1 行に 1 つ。改行を含むパスと " で始まるパスは引用して書く）。
func formatRepoStatus(paths []string) string {
	var b strings.Builder
	for _, p := range paths {
		if strings.ContainsAny(p, "\r\n") || strings.HasPrefix(p, `"`) {
			p = strconv.Quote(p)
		}
		b.WriteString(p + "\n")
	}
	return b.String()
}

// specDraftOpenRunCount は課題の、終わっていない実行（実行の記録と契約の実行の記録）の数。
func specDraftOpenRunCount(ctx context.Context, tx *sql.Tx, cid int64) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM run WHERE challenge_id = ? AND result IS NULL)
		+ (SELECT COUNT(*) FROM contract_run WHERE challenge_id = ? AND ended_at IS NULL)`, cid, cid).Scan(&n)
	return n, err
}

// specDraftStall は、下書きづくりが行き止まりになっている課題（失敗の保留にする）。
type specDraftStall struct {
	ChallengeID, RunID int64
	// CountAttempt は、記録が pending のまま（開始の失敗を回数に数えていない）とき真。
	CountAttempt bool
	Detail       string
	// DraftPath は記録の下書きの場所。
	DraftPath string
}

// specDraftWork は、分類済の課題 cid の下書きづくりについて、委譲の段が行うことを返す。
// startable は開始できる（記録が pending で、終わっていない実行が無い）。stall は失敗の保留にするもの（無ければ nil）で、
// 次の 2 つ。①記録が running なのに終わっていない実行が無い。②記録が pending で、最後の下書きづくりの実行が、契約の実行の記録も
// 保留も残さずに終わった（開始の失敗を記録へ写す前に止まった）。開始の実行は、その開始の時点で、先に始まった契約の実行が
// 開いていないもの（開いている実行への状態の確認・取り消しの実行は、開始の実行と数えない）。
func specDraftWork(ctx context.Context, tx *sql.Tx, cid int64) (startable bool, stall *specDraftStall, err error) {
	d, err := loadSpecDraft(ctx, tx, cid)
	if err != nil || d == nil || (d.State != SpecDraftStatePending && d.State != SpecDraftStateRunning) {
		return false, nil, err
	}
	open, err := specDraftOpenRunCount(ctx, tx, cid)
	if err != nil || open > 0 {
		return false, nil, err
	}
	var (
		runID             int64
		result, errText   string
		recorded, hasHold bool
	)
	err = tx.QueryRowContext(ctx, `SELECT r.id, r.result, COALESCE(r.error, ''),
			EXISTS (SELECT 1 FROM contract_run k WHERE k.start_run_id = r.id), EXISTS (SELECT 1 FROM hold h WHERE h.run_id = r.id)
		FROM run r WHERE r.challenge_id = ? AND r.budget_bucket = ?
			AND NOT EXISTS (SELECT 1 FROM contract_run k WHERE k.challenge_id = r.challenge_id AND k.start_run_id < r.id
				AND (k.ended_at IS NULL OR k.ended_at > r.started_at))
		ORDER BY r.id DESC LIMIT 1`, cid, string(budgetBucketSpec)).
		Scan(&runID, &result, &errText, &recorded, &hasHold)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, nil, err
	}
	if d.State == SpecDraftStatePending {
		if !found || recorded || hasHold {
			return true, nil, nil
		}
		return false, &specDraftStall{ChallengeID: cid, RunID: runID, CountAttempt: true, DraftPath: d.Path,
			Detail: specDraftNotStartedDetail(RunResult(result), errText)}, nil
	}
	if !found {
		// 下書きづくりの実行の記録が無いときは、課題の最後の実行を保留の原因にする（回した判断の実行がある）。
		var last sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT MAX(id) FROM run WHERE challenge_id = ?`, cid).Scan(&last); err != nil || !last.Valid {
			return false, nil, err
		}
		runID = last.Int64
	}
	return false, &specDraftStall{ChallengeID: cid, RunID: runID, DraftPath: d.Path,
		Detail: "下書きづくりの記録は実行中ですが、終わっていない実行がありません。"}, nil
}

func specDraftNotStartedDetail(result RunResult, errSummary string) string {
	s := fmt.Sprintf("下書きづくりを開始できませんでした（失敗の種類: %s）。", result)
	if errSummary != "" {
		s += "詳細: " + errSummary
	}
	return s
}

// selectSpecDraftWork は、分類済の課題（優先度・ID の昇順）のうち、下書きづくりを始められるものと、行き止まりのものを返す。
// only が非 nil ならその課題だけを見る。取り下げた課題と人間対応待ちの課題は、分類済でないので選ばない。
func selectSpecDraftWork(ctx context.Context, tx *sql.Tx, only *int64) (startable []int64, stalls []specDraftStall, err error) {
	challenges, err := loadChallengesByStatusSorted(ctx, tx, StatusClassified)
	if err != nil {
		return nil, nil, err
	}
	for _, ch := range challenges {
		cid, ok := parseChallengeID(ch.ID)
		if !ok || (only != nil && cid != *only) {
			continue
		}
		start, stall, err := specDraftWork(ctx, tx, cid)
		if err != nil {
			return nil, nil, err
		}
		if start {
			startable = append(startable, cid)
		}
		if stall != nil {
			stalls = append(stalls, *stall)
		}
	}
	return startable, stalls, nil
}

// holdStalledSpecDraft は行き止まりの下書きづくりを、失敗の保留にする。保留を作る書き込みの中で、まだ行き止まりかを確かめ直す。
func (s *Store) holdStalledSpecDraft(ctx context.Context, st specDraftStall) error {
	// ファイルの確かめは書き込みの前に行う（保留の選択肢は、確かめた時点のまま）。
	draftExists := s.specDraftFileExists(st.DraftPath)
	_, err := s.transitionAsInvoker(context.WithoutCancel(ctx), formatRunID(st.RunID), formatChallengeID(st.ChallengeID), OpHold,
		func(ctx context.Context, tc *transitionCtx) error {
			_, cur, err := specDraftWork(ctx, tc.tx, tc.id)
			if err != nil {
				return err
			}
			if cur == nil {
				return errSpecDraftHoldNotEligible
			}
			return raiseSpecDraftHold(ctx, tc, specDraftHold{Reason: SpecDraftHoldFailed, Detail: cur.Detail, CountAttempt: cur.CountAttempt,
				DraftExists: draftExists})
		})
	if errors.Is(err, errSpecDraftHoldNotEligible) || errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) {
		return nil
	}
	return err
}

// startSpecDrafts は委譲の段の、下書きづくりの部分。行き止まりの課題を失敗の保留にし、始められる課題の下書きづくりを始める。
// handled は、対象の課題があったか（ID を指定した呼び出しは、あればここで終える）。同時に走らせる数・サイクルの費用の上限で
// 始められなかった課題は、結果のどこにも載せない（記録は pending のまま残り、次の機会に始める）。
func (s *Store) startSpecDrafts(ctx context.Context, in DelegateInput) (res *DelegateResult, handled bool, err error) {
	res = &DelegateResult{}
	if in.Contract == nil {
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
	var startable []int64
	var stalls []specDraftStall
	if err := classifyReadWriteErr(s.db.Read(ctx, func(tx *sql.Tx) (err error) {
		startable, stalls, err = selectSpecDraftWork(ctx, tx, only)
		return err
	})); err != nil {
		return nil, false, err
	}
	for _, st := range stalls {
		if err := s.holdStalledSpecDraft(ctx, st); err != nil {
			return nil, false, err
		}
	}
	for _, cid := range startable {
		if in.requeuedSpecDrafts[cid] {
			continue
		}
		item, err := s.startSpecDraft(ctx, in, cid)
		if err != nil {
			return nil, false, err
		}
		appendDelegateOutcome(res, item, nil)
	}
	// ID を指定した呼び出しの対象が、いま実行待ちへ戻した下書きづくりなら、始めないが、対象はあったことにする。
	return res, len(startable)+len(stalls) > 0 || (only != nil && in.requeuedSpecDrafts[*only]), nil
}

// specDraftStartContext は、下書きづくりの実行の行を作った書き込みの中で読んだ値。
type specDraftStartContext struct {
	runID       int64
	title       string
	description string
	binding     *sourceBinding
	draft       SpecDraft
	repo        *ConnectorRepo
	connector   *Connector
	maxMicros   int64
}

var errSpecDraftNotStartable = errors.New("core: the spec draft cannot be started now")

// beginSpecDraftRun は下書きづくりの実行の行（予算の枠 spec・タスクなし）を作る。同じ書き込みの中で、対象であること・同時に走らせる数・
// サイクルの費用の上限を確かめ、始められなければ何も書かず nil を返す。
func (s *Store) beginSpecDraftRun(ctx context.Context, in DelegateInput, cid int64) (*specDraftStartContext, error) {
	sessionID, err := newLowercaseUUIDv4()
	if err != nil {
		return nil, fmt.Errorf("%w: generate session id: %w", ErrStoreError, err)
	}
	var cycleIDInt *int64
	if n, ok := parseCycleID(in.CycleID); ok {
		cycleIDInt = &n
	}
	host, _ := os.Hostname()
	now := s.currentTime()
	var out *specDraftStartContext
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		ch, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		if ch == nil || ch.Status != StatusClassified {
			return errSpecDraftNotStartable
		}
		startable, _, err := specDraftWork(ctx, tx, cid)
		if err != nil {
			return err
		}
		if !startable {
			return errSpecDraftNotStartable
		}
		d, err := loadSpecDraft(ctx, tx, cid)
		if err != nil {
			return err
		}
		binding, err := loadSourceBindingByChallengeID(ctx, tx, cid)
		if err != nil {
			return err
		}
		active, err := activeDelegationCount(ctx, tx)
		if err != nil {
			return err
		}
		if active >= in.AgentDecl.MaxParallelRuns {
			return errSpecDraftNotStartable
		}
		repo, connector := in.ConnDecl.findConnectorRepo(d.Repo)
		maxMicros := specDraftBudgetMicros(in.AgentDecl, connector)
		if cycleIDInt != nil {
			if err := evalCycleBudgetTx(ctx, tx, *cycleIDInt, maxMicros); err != nil {
				return err
			}
		}
		row, err := insertRun(ctx, tx, cid, insertRunInput{
			CycleID: cycleIDInt, Kind: runKindContract, ChallengeVersion: int64(ch.Version),
			SessionID: sessionID, PID: int64(os.Getpid()), Host: host, HeartbeatAt: now, StartedAt: now,
			MaxBudgetUSD: maxMicros, BudgetBucket: budgetBucketSpec,
		})
		if err != nil {
			return err
		}
		runID, ok := parseRunID(row.ID)
		if !ok {
			return fmt.Errorf("core: malformed run id %q", row.ID)
		}
		out = &specDraftStartContext{runID: runID, title: ch.Title, description: ch.Description, binding: binding, draft: *d,
			repo: repo, connector: connector, maxMicros: maxMicros}
		return nil
	})
	if errors.Is(err, errSpecDraftNotStartable) || errors.Is(err, ErrBudgetExceeded) || errors.Is(err, errActiveRunExists) {
		return nil, nil
	}
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return out, nil
}

// prepareSpecDraftStart は、文面のファイルと始める前の元のクローンの一覧を実行の記録の置き場に書き、開始のコマンドの引数と
// 作業ディレクトリを組み立てる。誤りは開始の失敗として扱う（開始のコマンドは呼ばない）。
func (s *Store) prepareSpecDraftStart(ctx context.Context, git SlotGit, sc *specDraftStartContext, challengeID, workDir string) ([]string, error) {
	if sc.repo == nil || sc.connector == nil || !slices.Contains(specDraftTargetRepos(&ConnectorsDeclaration{
		Repos: []ConnectorRepo{*sc.repo}, Connectors: []Connector{*sc.connector}}), sc.repo.Name) {
		return nil, fmt.Errorf("the repository %q cannot be used for spec drafting in the connectors declaration", sc.draft.Repo)
	}
	runDir, err := filepath.Abs(runDirPath(s.workspace, formatRunID(sc.runID)))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, fmt.Errorf("write the spec brief: %w", err)
	}
	briefFile := filepath.Join(runDir, specBriefFileName)
	if err := os.WriteFile(briefFile, []byte(formatSpecBrief(sc.title, sc.description, sc.binding, sc.draft)), 0o644); err != nil {
		return nil, fmt.Errorf("write the spec brief: %w", err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return nil, fmt.Errorf("create the spec draft directory: %w", err)
	}
	repoDir := filepath.Join(s.workspace, sc.repo.Slots.Base)
	changed, err := git.ChangedPaths(ctx, SlotTree{Path: repoDir})
	if err != nil {
		return nil, fmt.Errorf("read the changed paths of the repository %q: %w", sc.repo.Name, err)
	}
	if err := os.WriteFile(filepath.Join(runDir, repoStatusBeforeFileName), []byte(formatRepoStatus(changed)), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", repoStatusBeforeFileName, err)
	}
	basePath := ""
	if sc.draft.BasePath != nil {
		basePath = *sc.draft.BasePath
	}
	return s.contractCommandArgs(sc.connector, contractCmdSpec, contractFill{ChallengeID: challengeID, Spec: &specContractFill{
		DraftFile: filepath.Base(sc.draft.Path), BriefFile: briefFile, BriefDir: runDir, RepoDir: repoDir,
		BasePath: basePath, BudgetMicros: sc.maxMicros,
	}})
}

// startSpecDraft は課題 cid の下書きづくりを 1 回始める。始まらなかった（上限・対象外・開始の失敗）ときは item は nil。
func (s *Store) startSpecDraft(ctx context.Context, in DelegateInput, cid int64) (*JudgmentAutoItem, error) {
	sc, err := s.beginSpecDraftRun(ctx, in, cid)
	if err != nil || sc == nil {
		return nil, err
	}
	challengeID := formatChallengeID(cid)
	workDir := filepath.Join(s.workspace, filepath.FromSlash(specDraftDir))
	command, prepErr := s.prepareSpecDraftStart(ctx, in.Git, sc, challengeID, workDir)
	connectorID := ""
	if sc.connector != nil {
		connectorID = sc.connector.ID
	}
	rep, err := s.finishContractCall(ctx, contractCall{
		ChallengeID: challengeID, ChallengeIDInt: cid, RunIDInt: sc.runID, Connector: connectorID,
		Launch: ContractLaunchInput{
			Workspace: s.workspace, WorkDir: workDir, RunDir: runDirPath(s.workspace, formatRunID(sc.runID)),
			Command: command, TimeoutSec: in.AgentDecl.TimeoutSec.Delegate,
		},
		Invoke: func(ctx context.Context, li ContractLaunchInput) (ContractLaunchOutput, error) {
			if prepErr != nil {
				return ContractLaunchOutput{Result: RunResultLaunchFailed, ErrorSummary: prepErr.Error()}, nil
			}
			return in.Contract.Start(ctx, li)
		},
		MaxMicros: sc.maxMicros, Kind: contractRunKindSpec,
	})
	if err != nil || rep.Held {
		return nil, err
	}
	return &JudgmentAutoItem{ChallengeID: rep.ChallengeID, RunID: rep.RunID, Result: rep.Result}, nil
}

// markSpecDraftRunning は、始まった下書きづくりを記録へ写す（実行中・回数を 1 増やす・問いの一覧を空にする。答えは残す）。
// 起動の間に記録が実行待ちでなくなっていれば、何も変えない。
func (s *Store) markSpecDraftRunning(ctx context.Context, tx *sql.Tx, cid int64) error {
	d, err := loadSpecDraft(ctx, tx, cid)
	if err != nil || d == nil || d.State != SpecDraftStatePending {
		return err
	}
	return updateSpecDraft(ctx, tx, cid, specDraftUpdate{State: SpecDraftStateRunning, Attempts: d.Attempts + 1, Answers: d.Answers}, s.currentTime())
}

// holdFailedSpecDraftStart は、下書きづくりの開始のコマンドの結果が失敗なら、失敗の保留にする。recorded は記録に使った状態
// （記録しなかったときは nil）。開始の失敗は、起動できない・出力が不正・開始の最中の回収・終わった状態の応答。
func (s *Store) holdFailedSpecDraftStart(ctx context.Context, c contractCall, recorded *ContractStatus, reclaimed bool, result RunResult, errSummary string) (bool, error) {
	h := specDraftHold{Reason: SpecDraftHoldFailed}
	switch {
	case reclaimed:
		h.Detail, h.CountAttempt = specDraftNotStartedDetail(RunResultInterrupted, "開始の最中に実行の記録が回収されました。接続ツールの側に実行が残っているかもしれません。"), true
	case recorded == nil:
		h.Detail, h.CountAttempt = specDraftNotStartedDetail(result, errSummary), true
	case recorded.State.Terminal():
		// 始まったものとして回数は数えてある。
		h.Detail = fmt.Sprintf("下書きづくりの開始のコマンドが、終わった状態（%s）を返しました。%s", recorded.State, recorded.Summary)
	default:
		return false, nil
	}
	return s.holdSpecDraft(ctx, formatRunID(c.RunIDInt), c.ChallengeID, h)
}
