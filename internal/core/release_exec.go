package core

// このファイルは承認済みの release の実行（docs/features/m4.5-learning-and-self-improvement.md
// §承認済みの release の実行・§実行できないときの扱い・§IF / API「実行の判定の順（S1）」）の
// 公開 API と判定の順を持つ。実行は決定的で、LLM を呼ばない（claude を起動しない）。
//
// 1 件の流れ:
//
//	① 操作を読む（release で approved でなければ not_executable）
//	② 止まった試行を interrupted で閉じ、新しい試行を始める（終了していない試行が生きていれば in_progress）
//	③ 成果物を上流から読んで確かめ、道を選ぶ（GitHub の読み取りだけ。flywheel は反映先へ入れない）
//	④ 結果に応じて試行を閉じる（実行済み・取り消しは release_exec_write.go の 1 トランザクションの関数）。
//	   2 回目からの waiting_outside は、開いた試行の行を消して始めなかった理由で返す

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

// NotStartedWaitingOutside は、直前の試行の結果が waiting_outside の反映の操作が、もう一度
// flywheel の外での反映を待つことになり、試行の行を増やさず飛ばしたことを表す。
const NotStartedWaitingOutside NotStartedReason = "waiting_outside"

// NotStartedDependency は、依存先に満たされていないものがあり委譲を起動しなかったことを表す
// （S2。Detail は満たされていない依存先の ID を昇順に "," で連ねた文字列）。
const NotStartedDependency NotStartedReason = "dependency"

// releaseAttemptStaleAfter は、終了していない試行を、プロセスの有無によらず止まったとみなす経過時間。
// ちょうどこの時間は止まったに含めない。
const releaseAttemptStaleAfter = 15 * time.Minute

// ReleaseExecOptions は ExecuteReleases の入力。Checks・Connectors は必須
// （どれかが nil なら ErrValidation）。
type ReleaseExecOptions struct {
	// Checks は PR の状態・チェックの読み取り（GET だけ）。UpstreamCommitChecksSource も実装していれば、承認した head のチェックの有無の確認に使う（ラップする値はこれも通すこと）。
	Checks UpstreamCheckSource
	// Compare は、PR の head が承認の head から動いたとき、動いた中身が既定ブランチの取り込みだけかを判定するための比較の取得
	// （GET だけ）。nil なら、その判定をしない（承認待ちの release の片付けのうち、マージ済みを閉じる処理は動く）。
	Compare UpstreamCompareSource
	// Connectors は PR のリポジトリ・既定ブランチを引く宣言。
	Connectors *ConnectorsDeclaration
	// Agent は failure_limit と、契約のコマンドの時間の上限の出どころ。nil なら既定値。
	Agent *AgentDeclaration
	// Contract は契約のコマンド（status・resume）の起動。課題に対応する契約の実行があるときに使う（nil なら読めなかったものとして扱う）。
	Contract ContractClient
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
	// Route は反映の依頼の経路（ReleaseRouteWaitingRun・ReleaseRouteReflectRequest・ReleaseRouteOutside）。対応する契約の実行が無い・
	// 対応しない実行を待つときは空。
	Route string
}

// ReleaseRouteOutside は、flywheel の外で反映先へ入るのを待っている反映の経路（route の値）。
const ReleaseRouteOutside = "outside"

// ReleaseTidyAction は承認待ちの release の片付けで行ったこと。
type ReleaseTidyAction string

const (
	// ReleaseTidyClosed は、PR がマージ済みの承認待ちの release を closed にしたこと。
	ReleaseTidyClosed ReleaseTidyAction = "closed"
	// ReleaseTidyReopened は、検証の後に中身が変わった PR の release を置き換え、完了の課題を検証中へ戻したこと。
	ReleaseTidyReopened ReleaseTidyAction = "reopened"
	// ReleaseTidyNeedsHuman は、完了の課題の release の PR の head が検証の後に動いたが、取り込みだけとも中身の変更とも
	// 確かめられず（比較の打ち切り・head 無し等）、人間待ちにしたこと。何も書かない。人が承認するか検証中へ戻すかを選ぶ。
	ReleaseTidyNeedsHuman ReleaseTidyAction = "needs_human"
	// ReleaseTidySkipped は、完了の課題を検証中へ戻す前の読みで、終わっていないタスクの契約の実行が running か読めなかったため、
	// その課題を飛ばしたこと（課題は完了のまま。次の反映の段でやり直す）。
	ReleaseTidySkipped ReleaseTidyAction = "skipped"
)

// ReleaseTidyItem は承認待ちの release の片付け 1 件の結果。
type ReleaseTidyItem struct {
	OperationID string
	ChallengeID string
	Action      ReleaseTidyAction
	// ClosedReason は Action が closed のときの閉じた理由。
	ClosedReason ClosedReason
	// ReopenReason・NewOperationID は Action が reopened のときの戻した理由と新しい release。
	ReopenReason   ReopenReason
	NewOperationID string
	// WaitReason は Action が needs_human のとき・skipped のときの、判定できなかった理由。
	WaitReason string
	// CancelNote は Action が closed のとき、対応する契約の実行を取り消せなかった（読めなかった）理由（取り消せたか不要なら空）。
	CancelNote string
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
	// Tidied は承認待ちの release の片付け（ids を指定しない呼び出しだけ）の結果。
	Tidied []ReleaseTidyItem
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
	if opts.Checks == nil || opts.Connectors == nil {
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

	// ids を指定しない呼び出し（周の release の段・`op exec` の ID なし）だけ、承認待ちの release の片付けも行う。
	sweepPending := len(ids) == 0
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

	res := &ReleaseExecResult{Items: []ReleaseExecItem{}, NotStarted: []NotStarted{}, Tidied: []ReleaseTidyItem{}}
	// 承認待ちの release の片付けは、実行より先に行う（実行で取り消した・置き換えた release を同じ呼び出しで読み直さない）。
	if sweepPending {
		tidied, err := s.tidyPendingReleases(ctx, opts)
		res.Tidied = append(res.Tidied, tidied...)
		if err != nil {
			return res, err
		}
	}
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

// releaseJudgment は成果物の確認と道の選択の結果。
type releaseJudgment struct {
	result       ExecutionResult
	observedHead string
	mergeCommit  string
	detail       string
	// route は試行の route（空は null）。
	route string
	// landDeliveredRunID が 0 でないとき、結果を書く書き込みで、その契約の実行の「入れてよい」が届いた記録（land_delivered）を真にする。
	landDeliveredRunID int64
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

	actor, err := s.resolveActor()
	if err != nil {
		return nil, nil, err
	}

	var (
		row         *operationRow
		attempt     *operationExecutionRow
		notStarted  *NotStarted
		approvedRow string
		prevResult  ExecutionResult
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
		prevResult, err = lastDecidedExecutionResult(ctx, tx, opID)
		if err != nil {
			return err
		}
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

	j, err := s.judgeRelease(ctx, row, approvedRow, opts)
	if errors.Is(err, errReleaseContractBusy) {
		// 契約のコマンドを呼ぶための実行の記録を作れない。開いた試行の行を消して（試行の行を増やさない）、次の反映の段でやり直す。
		detail := ""
		_ = s.db.Read(ctx, func(tx *sql.Tx) error {
			if a, e := loadActiveRunByChallengeID(ctx, tx, row.ChallengeID); e == nil && a != nil {
				detail = a.ID
			}
			return nil
		})
		if derr := s.db.Write(ctx, func(tx *sql.Tx) error { return deleteOpenOperationExecution(ctx, tx, attempt.ID) }); classifyReadWriteErr(derr) != nil {
			s.abandonReleaseAttempt(ctx, attempt.ID, now(), err)
		}
		return nil, &NotStarted{ChallengeID: formatChallengeID(row.ChallengeID), OperationID: formatOperationID(opID), Reason: NotStartedInProgress, Detail: detail}, nil
	}
	if errors.Is(err, ErrConflict) {
		s.abandonReleaseAttempt(ctx, attempt.ID, now(), err)
		return nil, &NotStarted{ChallengeID: formatChallengeID(row.ChallengeID), OperationID: formatOperationID(opID),
			Reason: NotStartedInProgress, Detail: fmt.Sprintf("%d", attempt.ID)}, nil
	}
	if err != nil {
		s.abandonReleaseAttempt(ctx, attempt.ID, now(), err)
		return nil, nil, err
	}

	// 待っている間は試行の行を増やさない: 直前の試行も flywheel の外での反映を待っていたなら、開いた行を消して返す。
	if j.result == ExecutionResultWaitingOutside && prevResult == ExecutionResultWaitingOutside {
		derr := s.db.Write(ctx, func(tx *sql.Tx) error {
			// 判定の間に別の経路が操作を変えていたら、待っているとは返さない（試行の終わり方と同じ楽観ロック）。
			cur, err := loadOperationRow(ctx, tx, opID)
			if err != nil {
				return err
			}
			if cur.Version != row.Version || cur.State != OperationStateApproved {
				return ErrConflict
			}
			return deleteOpenOperationExecution(ctx, tx, attempt.ID)
		})
		if derr = classifyReadWriteErr(derr); derr != nil {
			s.abandonReleaseAttempt(ctx, attempt.ID, now(), derr)
			if errors.Is(derr, ErrConflict) {
				return nil, &NotStarted{ChallengeID: formatChallengeID(row.ChallengeID), OperationID: formatOperationID(opID),
					Reason: NotStartedInProgress, Detail: fmt.Sprintf("%d", attempt.ID)}, nil
			}
			return nil, nil, derr
		}
		return nil, &NotStarted{ChallengeID: formatChallengeID(row.ChallengeID), OperationID: formatOperationID(opID), Reason: NotStartedWaitingOutside}, nil
	}

	item, err := s.finishReleaseAttempt(ctx, actor, opID, row, attempt, approvedRow, j, opts, opts.cycleID, now)
	if err != nil {
		// 試行を終えられなかった。終了していない行が残ると次の口が 15 分待つことになるので、
		// 可能なら interrupted で閉じる（失敗しても元のエラーを返す）。
		s.abandonReleaseAttempt(ctx, attempt.ID, now(), err)
		if errors.Is(err, ErrConflict) || errors.Is(err, errExecutionNotOpen) {
			// 実行の途中で別の呼び出しが同じ操作・試行を変えた。自分の試行の ID を詳細に入れて飛ばす。
			return nil, &NotStarted{ChallengeID: formatChallengeID(row.ChallengeID), OperationID: formatOperationID(opID),
				Reason: NotStartedInProgress, Detail: fmt.Sprintf("%d", attempt.ID)}, nil
		}
		return nil, nil, err
	}
	return item, nil, nil
}

// abandonReleaseAttempt は、終えられなかった試行を interrupted で閉じる（閉じられなくても何もしない）。
func (s *Store) abandonReleaseAttempt(ctx context.Context, attemptID int64, at time.Time, cause error) {
	_ = s.db.Write(ctx, func(tx *sql.Tx) error {
		return closeOperationExecution(ctx, tx, attemptID, closeOperationExecutionInput{EndedAt: at, Result: ExecutionResultInterrupted, Detail: cause.Error()})
	})
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

// movedByMergeInOnly は、head の動きが既定ブランチの取り込みだけで、取り込みに紛れた変更（衝突の解消の差分）が
// 無いかを返す。差分を人が見ない道なので、直したファイルがある取り込みは含めない（GitHub の update-branch は
// 衝突があれば失敗するので、取り込みの依頼の結果はこれに当たる）。
func movedByMergeInOnly(ctx context.Context, src UpstreamCompareSource, repo, prBase, approvedHead, head string) bool {
	ch := ClassifyHeadChange(ctx, src, repo, prBase, approvedHead, head)
	if ch.Kind != HeadChangeMergeInOnly {
		return false
	}
	for _, f := range ch.ConflictFixes {
		if len(f.Files) > 0 {
			return false
		}
	}
	return true
}

// 承認した head にもチェックが無かった反映の detail と、取り込み後の head にチェックが無くて待つ 3 つの理由の detail。
const (
	noChecksMergeDetail                 = "merged with no checks on the updated head (the approved head had none either)"
	waitDetailNoChecksYet               = "no checks on the updated head yet"
	waitDetailApprovedChecksUnsupported = "cannot tell whether the approved head had checks: the connector does not support reading commit checks"
	waitDetailApprovedChecksUnreadable  = "cannot read the checks of the approved head: "
)

// approvedHeadChecks は、承認した head のチェックの取得結果を 1 回の判定の中で使い回す入れ物。
type approvedHeadChecks struct {
	done        bool
	none        bool
	unsupported bool
	err         error
}

// noChecks は、承認した head にチェックが 1 件も付いていなかったと確かめられたときだけ none=true を返す。
// 取得の手段が無い場合は none=false・err=nil、取得に失敗した場合は none=false・err（確かめられないので待つ側に倒す）。
func (a *approvedHeadChecks) noChecks(ctx context.Context, src UpstreamCheckSource, repo, approvedHead string) (bool, error) {
	if a.done {
		return a.none, a.err
	}
	a.done = true
	cs, ok := src.(UpstreamCommitChecksSource)
	if !ok {
		a.unsupported = true
		return false, nil
	}
	checks, err := cs.GetCommitChecks(ctx, repo, approvedHead)
	if err != nil {
		a.err = err
		return false, err
	}
	a.none = len(checks) == 0
	return a.none, nil
}

// judgeRelease は成果物を上流から読んで確かめ（承認した中身・宛先・入っているか・閉じているか・取り込みだけの変わり方・
// チェック）、道を選んで結果を返す。読み取りだけで、反映先へは何も書かない。
//
// head が承認の head から動いていても、動いた中身が既定ブランチの取り込みだけ（ClassifyHeadChange）なら外さず、
// 取り込んだ後の head のすべてのチェック（必須でないものを含む。1 件以上）が成功していることを確かめてから進む
// （承認し直さない）。ただし承認した head にもチェックが 0 件だったと確かめられたときは、0 件のままでも進む。
//
// 道は chooseReleasePath が選ぶ（タスクの実行へ「入れてよい」を返す・対応しない実行を待つ・flywheel の外での反映を待つ）。
// 返すエラーは、ストアを読めなかったときだけ。
func (s *Store) judgeRelease(ctx context.Context, row *operationRow, approvedHead string, opts ReleaseExecOptions) (releaseJudgment, error) {
	run, err := s.loadReleaseRun(ctx, row)
	if err != nil {
		return releaseJudgment{}, err
	}
	// 対応する契約の実行があれば、先に commands.status で読む（「入れてよい」を返したか・最後に読んだ状態が何かによらない）。
	var rep *ContractRunReport
	if run != nil && run.corresponds {
		var early *releaseJudgment
		if rep, early, err = s.readReleaseRun(ctx, opts, run); err != nil {
			return releaseJudgment{}, err
		}
		if early != nil {
			early.route = run.route()
			return *early, nil
		}
		// 「入れてよい」が届いた後に、同じ中身のレビュー待ちが戻った: 委譲先が入れられなかった。
		if refused := landRefusal(run, rep); refused != nil {
			return *refused, nil
		}
	}
	j, err := s.judgeReleaseUpstream(ctx, row, approvedHead, opts, run, rep)
	if err != nil {
		return releaseJudgment{}, err
	}
	if j.route == "" && j.result != ExecutionResultWaitingOutside {
		j.route = run.route()
	}
	// 委譲先の succeeded を読む以外の道で終わるとき、終わっていない対応する実行を取り消す（開いたまま残さない）。
	switch j.result {
	case ExecutionResultAlreadyMerged, ExecutionResultMergedUnapproved, ExecutionResultClosed:
		j.detail = appendDetail(j.detail, s.cancelReleaseRunNote(ctx, opts, run, rep))
	}
	return j, nil
}

// judgeReleaseUpstream は judgeRelease の、成果物を上流から読んで確かめ、道を選ぶ部分。
func (s *Store) judgeReleaseUpstream(ctx context.Context, row *operationRow, approvedHead string, opts ReleaseExecOptions, run *releaseRun, rep *ContractRunReport) (releaseJudgment, error) {
	approved := &approvedHeadChecks{}
	// 1
	if approvedHead == "" {
		return releaseJudgment{result: ExecutionResultNoApprovedHead}, nil
	}
	// 2
	repo, number, ok := parseReleasePullRequestRef(row.Ref.String)
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
		return releaseJudgment{result: ExecutionResultUnsupported}, nil
	}
	// 3
	pr, err := opts.Checks.GetPullRequestChecks(ctx, repo, number)
	if err != nil {
		return releaseJudgment{result: ExecutionResultUpstreamFailed, detail: err.Error()}, nil
	}
	observed := pr.HeadSHA
	// 4
	if pr.State == string(artifactStateMerged) {
		if pr.HeadSHA == approvedHead || movedByMergeInOnly(ctx, opts.Compare, repo, decl.DefaultBranch, approvedHead, pr.HeadSHA) {
			detail := ""
			if pr.HeadSHA != approvedHead && len(pr.Checks) == 0 {
				if none, _ := approved.noChecks(ctx, opts.Checks, repo, approvedHead); none {
					detail = noChecksMergeDetail
				}
			}
			// 委譲先が succeeded で終わって入っていれば landed、failed・cancelled で終わった（または終わりを読んでいない）なら already_merged。
			result := ExecutionResultAlreadyMerged
			if rep != nil && rep.State == ContractStateSucceeded {
				result = ExecutionResultLanded
			}
			return releaseJudgment{result: result, observedHead: observed, mergeCommit: pr.MergeCommitSHA, detail: detail}, nil
		}
		return releaseJudgment{result: ExecutionResultMergedUnapproved, observedHead: observed}, nil
	}
	// 委譲先が終わりの状態で、成果物が反映先へ入っていない（succeeded を返したときを含む）。取り消して、承認し直しを待つ。
	if rep != nil && rep.State.Terminal() {
		return releaseJudgment{result: ExecutionResultDelegateEnded, observedHead: observed,
			detail: fmt.Sprintf("the contract run %s ended as %s without landing the artifact", run.row.ExternalRunID, rep.State)}, nil
	}
	// 5
	if pr.State == string(artifactStateClosed) {
		return releaseJudgment{result: ExecutionResultClosed, observedHead: observed}, nil
	}
	// 6
	if pr.Base != decl.DefaultBranch {
		return releaseJudgment{result: ExecutionResultBaseChanged, observedHead: observed, detail: pr.Base}, nil
	}
	// 7
	moved := pr.HeadSHA != approvedHead
	if moved && !movedByMergeInOnly(ctx, opts.Compare, repo, decl.DefaultBranch, approvedHead, pr.HeadSHA) {
		return releaseJudgment{result: ExecutionResultHeadChanged, observedHead: observed}, nil
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
		return releaseJudgment{result: ExecutionResultChecksFailed, observedHead: observed}, nil
	}
	if pending {
		return releaseJudgment{result: ExecutionResultWaitingChecks, observedHead: observed}, nil
	}
	// 取り込んだ後の head は、チェックが 1 件も付いていない間は何も見ていないので待つ。
	// 承認した head にもチェックが 0 件だった（読めない場合は 0 件と見なさない）なら、何も見ていないのは承認の時点から同じなので待たない。
	if moved && len(pr.Checks) == 0 {
		none, _ := approved.noChecks(ctx, opts.Checks, repo, approvedHead)
		switch {
		case approved.err != nil:
			return releaseJudgment{result: ExecutionResultWaitingChecks, observedHead: observed, detail: waitDetailApprovedChecksUnreadable + approved.err.Error()}, nil
		case approved.unsupported:
			return releaseJudgment{result: ExecutionResultWaitingChecks, observedHead: observed, detail: waitDetailApprovedChecksUnsupported}, nil
		case !none:
			return releaseJudgment{result: ExecutionResultWaitingChecks, observedHead: observed, detail: waitDetailNoChecksYet}, nil
		}
	}
	// 道（通す中身は、成果物のいまの中身。承認した中身と同じか、反映先の最新の取り込みだけを足したもの）。
	return s.chooseReleasePath(ctx, row, opts, run, rep, pr.HeadSHA, observed)
}

// lastDecidedExecutionResult は opID の、閉じた試行のうち最後のもの（interrupted を除く）の結果を返す（無ければ空）。
func lastDecidedExecutionResult(ctx context.Context, tx *sql.Tx, opID int64) (ExecutionResult, error) {
	var r sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT result FROM operation_execution WHERE operation_id = ? AND result IS NOT NULL AND result != ? ORDER BY id DESC LIMIT 1`,
		opID, string(ExecutionResultInterrupted)).Scan(&r)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return ExecutionResult(r.String), err
}
