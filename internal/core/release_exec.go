package core

// このファイルは承認済みの release の実行（docs/features/m4.5-learning-and-self-improvement.md
// §承認済みの release の実行・§実行できないときの扱い・§IF / API「実行の判定の順（S1）」）の
// 公開 API と判定の順を持つ。実行は決定的で、LLM を呼ばない（claude を起動しない）。
//
// 1 件の流れ:
//
//	① 操作を読む（release で approved でなければ not_executable）
//	② 止まった試行を interrupted で閉じ、新しい試行を始める（終了していない試行が生きていれば in_progress）
//	③ ストアの外で、判定の順 1〜11 を最初に当たった条件まで評価する（GitHub の読み取りとマージの依頼）
//	④ 結果に応じて試行を閉じる（実行済み・取り消しは release_exec_write.go の 1 トランザクションの関数）

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// ReleaseExecTrigger は実行の契機（作業ログの after の trigger）。
const (
	ReleaseExecTriggerOpExec = "op exec"
	ReleaseExecTriggerCycle  = "cycle"
)

// NotStartedInProgress は、同じ操作の実行の試行が終わっておらず飛ばしたことを表す
// （詳細は終わっていない試行の ID）。
const NotStartedInProgress NotStartedReason = "in_progress"

// NotStartedNotExecutable は、release 以外、または状態が approved でない操作を外部を呼ばずに
// 飛ばしたことを表す（詳細は `kind=<種類> state=<状態>`）。
const NotStartedNotExecutable NotStartedReason = "not_executable"

// NotStartedDependency は、依存先に満たされていないものがあり委譲を起動しなかったことを表す
// （S2。Detail は満たされていない依存先の ID を昇順に "," で連ねた文字列）。
const NotStartedDependency NotStartedReason = "dependency"

// releaseAttemptStaleAfter は、終了していない試行を、プロセスの有無によらず止まったとみなす経過時間。
// ちょうどこの時間は止まったに含めない。
const releaseAttemptStaleAfter = 15 * time.Minute

// ReleaseExecOptions は ExecuteReleases の入力。Checks・Merger・Connectors は必須
// （どれかが nil なら ErrValidation）。
type ReleaseExecOptions struct {
	// Checks は PR の状態・チェックの読み取り（GET だけ）。
	Checks UpstreamCheckSource
	// Merger はマージの依頼（唯一の書き込み）。
	Merger UpstreamPullRequestMerger
	// Connectors は PR のリポジトリ・既定ブランチ・マージの方式を引く宣言。
	Connectors *ConnectorsDeclaration
	// Agent は failure_limit の出どころ。nil なら既定値。
	Agent *AgentDeclaration
	// CycleID は発生に持たせる周の ID（"Y-<n>"）。空なら周を持たない（NULL）。形式不正は ErrValidation。
	CycleID string
	// cycleID は ExecuteReleases が CycleID を解釈した内部整数 ID。
	cycleID *int64
	// Trigger は作業ログの契機（ReleaseExecTrigger*）。空なら ReleaseExecTriggerOpExec。
	Trigger string
	// Now は現在時刻。nil ならストアの時計（テストから与えられる）。
	Now func() time.Time
	// PID・Host は試行に記録する値。0・空ならこのプロセスの値。
	PID  int64
	Host string
}

// ReleaseExecItem は実行を試みた release 1 件の結果。
type ReleaseExecItem struct {
	OperationID         string
	ChallengeID         string
	Result              ExecutionResult
	ApprovedHead        string
	ObservedHead        string
	MergeCommit         string
	ConsecutiveFailures int
	// Detail は結果が merge_unconfirmed で、マージの依頼の後に試行の記録を書けなかったときだけ入る。
	Detail string
}

// PartialReleaseError は release の段が途中でエラーになったとき、それまでに実行済みの結果（マージ済みの
// release 等）を運ぶ。Unwrap で元のエラーを返すので errors.Is の判定は変わらない。
type PartialReleaseError struct {
	Err   error
	Items []ReleaseExecItem
}

func (e *PartialReleaseError) Error() string { return e.Err.Error() }
func (e *PartialReleaseError) Unwrap() error { return e.Err }

// ReleaseExecResult は ExecuteReleases の結果。
type ReleaseExecResult struct {
	Items      []ReleaseExecItem
	NotStarted []NotStarted
}

// ListExecutableReleases は状態が approved の release の ID を、ID の昇順に返す。
func (s *Store) ListExecutableReleases(ctx context.Context) ([]string, error) {
	var out []string
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT id FROM operation WHERE kind = 'release' AND state = ? ORDER BY id ASC`, string(OperationStateApproved))
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, formatOperationID(id))
		}
		return rows.Err()
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return out, nil
}

// ExecuteReleases は ids の操作を ID の昇順に実行する。ids が空なら承認済みで未実行の release
// すべて。存在しない ID（形式不正を含む）は何も実行せず ErrNotFound。実行は課題の状態を変えない。
func (s *Store) ExecuteReleases(ctx context.Context, ids []string, opts ReleaseExecOptions) (*ReleaseExecResult, error) {
	if opts.Checks == nil || opts.Merger == nil || opts.Connectors == nil {
		return nil, ErrValidation
	}
	if opts.Trigger == "" {
		opts.Trigger = ReleaseExecTriggerOpExec
	}
	if opts.Trigger != ReleaseExecTriggerOpExec && opts.Trigger != ReleaseExecTriggerCycle {
		return nil, ErrValidation
	}

	if opts.CycleID != "" {
		n, ok := parseCycleID(opts.CycleID)
		if !ok {
			return nil, ErrValidation
		}
		opts.cycleID = &n
	}

	var opIDs []int64
	if len(ids) == 0 {
		listed, err := s.ListExecutableReleases(ctx)
		if err != nil {
			return nil, err
		}
		ids = listed
	}
	for _, id := range ids {
		n, ok := parseOperationID(id)
		if !ok {
			return nil, ErrNotFound
		}
		opIDs = append(opIDs, n)
	}
	sort.Slice(opIDs, func(i, j int) bool { return opIDs[i] < opIDs[j] })
	// 存在の確認は、何も実行する前に済ませる（周の ID も。無いと発生の挿入が外部キーで失敗し、
	// マージの依頼の後に取り消しの記録だけが残らなくなる）。
	if err := s.db.Read(ctx, func(tx *sql.Tx) error {
		if opts.cycleID != nil {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM cycle WHERE id = ?`, *opts.cycleID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return ErrValidation
			}
		}
		for _, id := range opIDs {
			if _, err := loadOperationRow(ctx, tx, id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, classifyReadWriteErr(err)
	}

	res := &ReleaseExecResult{Items: []ReleaseExecItem{}, NotStarted: []NotStarted{}}
	seen := map[int64]bool{}
	for _, id := range opIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		item, ns, err := s.executeRelease(ctx, id, opts)
		if errors.Is(err, ErrConflict) || errors.Is(err, errExecutionNotOpen) {
			// 試行を始める前に別の呼び出しが同じ操作・試行を変えた。この release は飛ばして続ける。
			// detail には、その時点で開いている試行の ID を入れる（無ければ出さない）。
			cid, detail := "", ""
			_ = s.db.Read(ctx, func(tx *sql.Tx) error {
				if r, e := loadOperationRow(ctx, tx, id); e == nil {
					cid = formatChallengeID(r.ChallengeID)
				}
				if cur, e := latestOperationExecution(ctx, tx, id); e == nil && cur != nil && cur.open() {
					detail = fmt.Sprintf("%d", cur.ID)
				}
				return nil
			})
			res.NotStarted = append(res.NotStarted, NotStarted{ChallengeID: cid, OperationID: formatOperationID(id), Reason: NotStartedInProgress, Detail: detail})
			continue
		}
		if err != nil {
			return res, err
		}
		if ns != nil {
			res.NotStarted = append(res.NotStarted, *ns)
		}
		if item != nil {
			res.Items = append(res.Items, *item)
		}
	}
	return res, nil
}

// releaseJudgment は判定の順の評価の結果。
type releaseJudgment struct {
	result       ExecutionResult
	observedHead string
	mergeCommit  string
	detail       string
	// mergeRequested はマージの依頼（gh pr merge）が成功した後の判定であること。
	mergeRequested bool
}

func (s *Store) executeRelease(ctx context.Context, opID int64, opts ReleaseExecOptions) (*ReleaseExecItem, *NotStarted, error) {
	now := opts.Now
	if now == nil {
		now = s.currentTime
	}
	host := opts.Host
	if host == "" {
		h, err := os.Hostname()
		if err != nil || h == "" {
			h = "unknown"
		}
		host = h
	}
	pid := opts.PID
	if pid <= 0 {
		pid = int64(os.Getpid())
	}

	// 外部への書き込み（マージの依頼）より前に、作業ログの actor を解決しておく。
	actor, err := resolveActor()
	if err != nil {
		return nil, nil, err
	}

	var (
		row         *operationRow
		attempt     *operationExecutionRow
		notStarted  *NotStarted
		approvedRow string
	)
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		r, err := loadOperationRow(ctx, tx, opID)
		if err != nil {
			return err
		}
		row = r
		cid := formatChallengeID(r.ChallengeID)
		if r.Kind != string(OperationKindRelease) || r.State != OperationStateApproved {
			notStarted = &NotStarted{ChallengeID: cid, OperationID: formatOperationID(opID), Reason: NotStartedNotExecutable,
				Detail: fmt.Sprintf("kind=%s state=%s", r.Kind, r.State)}
			return nil
		}
		at := now()
		if open, err := latestOperationExecution(ctx, tx, opID); err != nil {
			return err
		} else if open != nil && open.open() {
			if !releaseAttemptStale(open, at) {
				notStarted = &NotStarted{ChallengeID: cid, OperationID: formatOperationID(opID), Reason: NotStartedInProgress,
					Detail: fmt.Sprintf("%d", open.ID)}
				return nil
			}
			if err := closeOperationExecution(ctx, tx, open.ID, closeOperationExecutionInput{EndedAt: at, Result: ExecutionResultInterrupted}); err != nil {
				return err
			}
		}
		head, err := latestReleaseApprovedHead(ctx, tx, opID)
		if err != nil {
			return err
		}
		approvedRow = head
		attempt, err = beginOperationExecution(ctx, tx, opID, beginOperationExecutionInput{StartedAt: at, ApprovedHead: head, PID: pid, Host: host})
		if errors.Is(err, errActiveExecutionExists) {
			// 別のプロセスが先に試行を始めた。
			attempt = nil
			detail := ""
			if cur, lerr := latestOperationExecution(ctx, tx, opID); lerr == nil && cur != nil && cur.open() {
				detail = fmt.Sprintf("%d", cur.ID)
			}
			notStarted = &NotStarted{ChallengeID: cid, OperationID: formatOperationID(opID), Reason: NotStartedInProgress, Detail: detail}
			return nil
		}
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, nil, err
	}
	if notStarted != nil || attempt == nil {
		return nil, notStarted, nil
	}

	j := s.judgeRelease(ctx, row, approvedRow, opts)

	cycleID := opts.cycleID
	item, err := s.finishReleaseAttempt(ctx, actor, opID, row, attempt, approvedRow, j, opts, cycleID, now)
	if err != nil {
		conflict := errors.Is(err, ErrConflict) || errors.Is(err, errExecutionNotOpen)
		// 記録が衝突した原因が、release が別の経路で pending へ取り消されたことで、PR がマージ済みなら、
		// 依頼した試行そのものを merged_unapproved で閉じる（承認の取り消し後にマージされた事象）。
		var recordErr error
		if conflict && j.mergeRequested {
			var it *ReleaseExecItem
			it, recordErr = s.recordMergeAfterWithdrawal(ctx, actor, opID, row, attempt, approvedRow, j, opts, cycleID, now, host, pid)
			if it != nil {
				return it, nil, nil
			}
		}
		// 試行を終えられなかった。終了していない行が残ると次の口が 15 分待つことになるので、
		// 可能なら interrupted で閉じる（失敗しても元のエラーを返す）。
		_ = s.db.Write(ctx, func(tx *sql.Tx) error {
			return closeOperationExecution(ctx, tx, attempt.ID, closeOperationExecutionInput{EndedAt: now(), Result: ExecutionResultInterrupted, Detail: err.Error()})
		})
		if conflict {
			if j.mergeRequested {
				// マージは依頼済みで、結果は未確認として扱う。release が approved のまま残っていれば、
				// 次の実行の口の先頭の読み取りで already_merged かを確かめる。
				n := 0
				_ = s.db.Read(ctx, func(tx *sql.Tx) error {
					c, e := consecutiveFailures(ctx, tx, opID)
					if e == nil {
						n = c
					}
					return nil
				})
				return &ReleaseExecItem{
					ConsecutiveFailures: n,
					OperationID:         formatOperationID(opID),
					ChallengeID:         formatChallengeID(row.ChallengeID),
					Result:              ExecutionResultMergeUnconfirmed,
					ApprovedHead:        approvedRow,
					ObservedHead:        j.observedHead,
					Detail:              mergeUnconfirmedDetail(err, recordErr),
				}, nil, nil
			}
			// 実行の途中で別の呼び出しが同じ操作・試行を変えた。自分の試行の ID を詳細に入れて飛ばす。
			return nil, &NotStarted{ChallengeID: formatChallengeID(row.ChallengeID), OperationID: formatOperationID(opID),
				Reason: NotStartedInProgress, Detail: fmt.Sprintf("%d", attempt.ID)}, nil
		}
		return nil, nil, err
	}
	return item, nil, nil
}

// releaseAttemptStale は終了していない試行が止まっているかを返す: 同じホストで pid のプロセスが
// 無い、または開始から 15 分を過ぎた（ホストを問わない。ちょうど 15 分は過ぎたに含めない）。
func releaseAttemptStale(a *operationExecutionRow, now time.Time) bool {
	if !isRunProcessAlive(a.Host, a.PID) {
		return true
	}
	return now.Sub(a.StartedAt) > releaseAttemptStaleAfter
}

// latestReleaseApprovedHead は opID の最新の承認（approved）の記録の head_sha を返す（無ければ空）。
func latestReleaseApprovedHead(ctx context.Context, tx *sql.Tx, opID int64) (string, error) {
	var head sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT head_sha FROM approval WHERE operation_id = ? AND kind = ? AND decision = ? ORDER BY id DESC LIMIT 1`,
		opID, string(ApprovalKindRelease), string(ApprovalDecisionApproved)).Scan(&head)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return head.String, nil
}

// checkOutcome はチェック 1 件の分類（チェックの分類の表）。
type checkOutcome int

const (
	checkPass checkOutcome = iota
	checkFail
	checkPending
)

func classifyCheck(c UpstreamCheck) checkOutcome {
	if !checkCompleted(c) {
		return checkPending
	}
	if c.Status == UpstreamCheckStatusLegacy {
		if c.Conclusion == "success" {
			return checkPass
		}
		return checkFail
	}
	switch c.Conclusion {
	case "success", "neutral", "skipped":
		return checkPass
	}
	return checkFail
}

// judgeRelease は判定の順 1〜11 を評価し、最初に当たった条件の結果を返す。
func (s *Store) judgeRelease(ctx context.Context, row *operationRow, approvedHead string, opts ReleaseExecOptions) releaseJudgment {
	// 1
	if approvedHead == "" {
		return releaseJudgment{result: ExecutionResultNoApprovedHead}
	}
	// 2
	ref := row.Ref.String
	repo, number, ok := parseReleasePullRequestRef(ref)
	var decl *ConnectorRepo
	if ok {
		for i := range opts.Connectors.Repos {
			if strings.EqualFold(opts.Connectors.Repos[i].Remote, repo) {
				decl = &opts.Connectors.Repos[i]
				break
			}
		}
	}
	if !row.Ref.Valid || !ok || decl == nil {
		return releaseJudgment{result: ExecutionResultUnsupported}
	}
	// 3
	pr, err := opts.Checks.GetPullRequestChecks(ctx, repo, number)
	if err != nil {
		return releaseJudgment{result: ExecutionResultUpstreamFailed, detail: err.Error()}
	}
	observed := pr.HeadSHA
	// 4
	if pr.State == string(artifactStateMerged) {
		if pr.HeadSHA == approvedHead {
			return releaseJudgment{result: ExecutionResultAlreadyMerged, observedHead: observed, mergeCommit: pr.MergeCommitSHA}
		}
		return releaseJudgment{result: ExecutionResultMergedUnapproved, observedHead: observed}
	}
	// 5
	if pr.State == string(artifactStateClosed) {
		return releaseJudgment{result: ExecutionResultClosed, observedHead: observed}
	}
	// 6
	if pr.Base != decl.DefaultBranch {
		return releaseJudgment{result: ExecutionResultBaseChanged, observedHead: observed, detail: pr.Base}
	}
	// 7
	if pr.HeadSHA != approvedHead {
		return releaseJudgment{result: ExecutionResultHeadChanged, observedHead: observed}
	}
	// 8・9
	failed, pending := false, false
	for _, c := range pr.Checks {
		switch classifyCheck(c) {
		case checkFail:
			failed = true
		case checkPending:
			pending = true
		}
	}
	if failed {
		return releaseJudgment{result: ExecutionResultChecksFailed, observedHead: observed}
	}
	if pending {
		return releaseJudgment{result: ExecutionResultWaitingChecks, observedHead: observed}
	}
	// 10
	if pr.Mergeable == nil {
		return releaseJudgment{result: ExecutionResultWaitingChecks, observedHead: observed, detail: "mergeable is null"}
	}
	if !*pr.Mergeable {
		return releaseJudgment{result: ExecutionResultNotMergeable, observedHead: observed}
	}
	// 11
	method := decl.MergeMethod
	if method == "" {
		method = MergeMethodSquash
	}
	prURL := fmt.Sprintf("https://github.com/%s/pull/%d", repo, number)
	if err := opts.Merger.MergePullRequest(ctx, prURL, method, approvedHead); err != nil {
		return releaseJudgment{result: ExecutionResultMergeFailed, observedHead: observed, detail: err.Error()}
	}
	after, err := opts.Checks.GetPullRequestChecks(ctx, repo, number)
	if err != nil {
		// 依頼は成功したが結果を読めない。マージされたかは次の試行の先頭の読み取りで確かめる。
		return releaseJudgment{mergeRequested: true, result: ExecutionResultMergeUnconfirmed, observedHead: observed, detail: err.Error()}
	}
	if after.State != string(artifactStateMerged) {
		return releaseJudgment{mergeRequested: true, result: ExecutionResultMergeUnconfirmed, observedHead: after.HeadSHA}
	}
	if after.HeadSHA != approvedHead {
		return releaseJudgment{mergeRequested: true, result: ExecutionResultMergedUnapproved, observedHead: after.HeadSHA}
	}
	return releaseJudgment{mergeRequested: true, result: ExecutionResultMerged, observedHead: after.HeadSHA, mergeCommit: after.MergeCommitSHA}
}

func mergeUnconfirmedDetail(err, recordErr error) string {
	d := "merge requested; recording the attempt failed: " + err.Error()
	if recordErr != nil {
		d += "; recording the merge after withdrawal failed: " + recordErr.Error()
	}
	return d
}

// recordMergeAfterWithdrawal は、マージを依頼した後に試行の記録が衝突した release が pending に
// 取り消されていて、PR がマージ済みだったときに、依頼した試行を結果 merged_unapproved で閉じて返す
// （別の呼び出しが先に閉じていれば新しい試行を足す。直前の試行がすでに merged_unapproved なら足さない）。
// status の食い違い・承認し直しの禁止は、直前の試行のこの結果で働く。
// 取り消されていない・マージ済みと確かめられないときは (nil, nil)、確かめた後に書けなかったときは
// (nil, エラー) を返し、呼び出し側が merge_unconfirmed を返す。確かめるのは j の結果、なければ PR の読み取り 1 回。
func (s *Store) recordMergeAfterWithdrawal(ctx context.Context, actor string, opID int64, row *operationRow, attempt *operationExecutionRow, approvedHead string, j releaseJudgment, opts ReleaseExecOptions, cycleID *int64, now func() time.Time, host string, pid int64) (*ReleaseExecItem, error) {
	merged := j.result == ExecutionResultMerged || j.result == ExecutionResultMergedUnapproved
	observed := j.observedHead
	if !merged {
		repo, number, ok := parseReleasePullRequestRef(row.Ref.String)
		if !ok {
			return nil, nil
		}
		pr, err := opts.Checks.GetPullRequestChecks(ctx, repo, number)
		if err != nil || pr.State != string(artifactStateMerged) {
			return nil, nil
		}
		observed = pr.HeadSHA
	}
	const detail = "merged after the approval was withdrawn (release is pending)"
	var outcome mergedAfterWithdrawalOutcome
	err := s.mutateAs(ctx, actor, ChannelCLI, VerificationNone, func(tx *sql.Tx, rec *activityRecorder) error {
		rec.at = now()
		var err error
		outcome, err = s.recordMergedAfterWithdrawalInTx(ctx, tx, rec, releaseOutcomeWrite{
			opID: opID, challengeID: row.ChallengeID, attemptID: attempt.ID,
			result: ExecutionResultMergedUnapproved, approvedHead: approvedHead, observedHead: observed,
			mergeCommit: j.mergeCommit, detail: detail, trigger: opts.Trigger, at: rec.at, cycleID: cycleID,
		}, pid, host)
		return err
	})
	if err != nil {
		return nil, err
	}
	if outcome == mergedAfterWithdrawalNotPending {
		return nil, nil
	}
	return &ReleaseExecItem{
		OperationID:  formatOperationID(opID),
		ChallengeID:  formatChallengeID(row.ChallengeID),
		Result:       ExecutionResultMergedUnapproved,
		ApprovedHead: approvedHead,
		ObservedHead: observed,
	}, nil
}
