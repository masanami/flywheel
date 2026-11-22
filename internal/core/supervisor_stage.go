package core

// このファイルはスーパーバイザーの段（区切り S4b-2）の「対象を選ぶ」部分と、段の進め方（1 件ずつ回す）を持つ。
// 段の 4 つの仕事は別のファイルに分けてある: 対象を選ぶ（ここ）・判断点を呼ぶ（judgment_sv.go）・結果を確かめる
// （supervisor_verify.go）・手を実行する（supervisor_exec.go と supervisor_hand_*.go）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// supervisorTarget はスーパーバイザー待ちの保留 1 件。
type supervisorTarget struct {
	HoldID      int64
	ChallengeID int64
	TaskID      *int64
	StopKind    StopKind
	Question    string
	// DeferredCount は手 defer を続けて打った回数（見送ったサイクルごとに 1 増える）。
	DeferredCount int
}

// supervisorPendingWhere は「スーパーバイザー待ちで、まだ回答も取り下げも無い保留」の条件（hold h と challenge c を結合した上で使う）。
const supervisorPendingWhere = `h.supervisor_state = 'pending' AND h.answer IS NULL AND h.closed_at IS NULL AND c.status = 'awaiting_human'`

// supervisorNotDeferredIn は、cycleDBID（0 は条件なし）のサイクルで見送っていない、の条件（? を 2 つ使う）。
const supervisorNotDeferredIn = ` AND (? = 0 OR h.deferred_cycle_id IS NULL OR h.deferred_cycle_id <> ?)`

// supervisorDeferLimit は、手 defer が続いたら、次のサイクルで判断点を呼ばずにオーナーへ上げる連続の数。
const supervisorDeferLimit = 3

// cycleDBIDOf は周の ID（"Y-<n>"）の数値。解釈できなければ 0。
func cycleDBIDOf(cycleID string) int64 {
	n, _ := parseCycleID(cycleID)
	return n
}

// selectSupervisorTargets は、処理するスーパーバイザー待ちの保留を古い順に返す。cycleDBID のサイクルで見送った保留は除く（0 なら除かない）。
func selectSupervisorTargets(ctx context.Context, tx *sql.Tx, cycleDBID int64) ([]supervisorTarget, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT h.id, h.challenge_id, h.task_id, h.stop_kind, h.question, h.deferred_count FROM hold h JOIN challenge c ON c.id = h.challenge_id
		 WHERE `+supervisorPendingWhere+supervisorNotDeferredIn+` ORDER BY h.id ASC`, cycleDBID, cycleDBID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []supervisorTarget
	for rows.Next() {
		t, err := scanSupervisorTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func scanSupervisorTarget(rows interface{ Scan(...any) error }) (*supervisorTarget, error) {
	var t supervisorTarget
	var taskID sql.NullInt64
	var kind sql.NullString
	if err := rows.Scan(&t.HoldID, &t.ChallengeID, &taskID, &kind, &t.Question, &t.DeferredCount); err != nil {
		return nil, err
	}
	if taskID.Valid {
		v := taskID.Int64
		t.TaskID = &v
	}
	t.StopKind = StopKind(kind.String)
	return &t, nil
}

// reloadSupervisorTarget は保留 1 件がまだスーパーバイザー待ちか（オーナーが回答していないか）を読み直す。待ちでなければ nil。
func (s *Store) reloadSupervisorTarget(ctx context.Context, holdID int64) (*supervisorTarget, error) {
	var t *supervisorTarget
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		r := tx.QueryRowContext(ctx,
			`SELECT h.id, h.challenge_id, h.task_id, h.stop_kind, h.question, h.deferred_count FROM hold h JOIN challenge c ON c.id = h.challenge_id
			 WHERE h.id = ? AND `+supervisorPendingWhere, holdID)
		v, err := scanSupervisorTarget(r)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		t = v
		return err
	})
	return t, classifyReadWriteErr(err)
}

// superviseStage はスーパーバイザーの段。スーパーバイザー待ちの保留を古い順に 1 件ずつ、判断点 SV へ渡して手を実行する。
// 待ちが無ければ判断点を呼ばない。起動できなかった保留（枠超過・サイクルの費用の上限）は待ちのまま残す。
func (s *Store) superviseStage(ctx context.Context, in CycleRunInput, cycleID string, jc *JudgmentCycle) (CyclePhaseResult, error) {
	res := CyclePhaseResult{Phase: CyclePhaseSupervise, Skipped: advancePhaseSkipped(in, CyclePhaseSupervise), SuperviseHands: map[SupervisorHand]int{}}
	if res.Skipped || in.Invoker == nil {
		return res, nil
	}
	var targets []supervisorTarget
	if err := classifyReadWriteErr(s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		targets, err = selectSupervisorTargets(ctx, tx, cycleDBIDOf(cycleID))
		return err
	})); err != nil {
		return res, err
	}
	for _, t := range targets {
		cur, err := s.reloadSupervisorTarget(ctx, t.HoldID)
		if err != nil {
			return res, err
		}
		if cur == nil {
			continue // オーナーが先に回答した・取り下げた
		}
		if cur.DeferredCount >= supervisorDeferLimit {
			// 手 defer が続いた。判断点を呼ばず、オーナーへ上げる（処理した件数には入れない）。
			raised, err := s.escalateDeferExhausted(ctx, cycleID, *cur)
			if err != nil {
				return res, fmt.Errorf("escalate hold %d: %w", cur.HoldID, err)
			}
			if raised {
				res.SuperviseHands[HandEscalate]++ // 結果が executed の対処の記録は、手ごとの件数に入る
			}
			continue
		}
		if jc != nil && jc.RateLimited() {
			res.NotStarted = append(res.NotStarted, NotStarted{ChallengeID: formatChallengeID(cur.ChallengeID), Reason: NotStartedRateLimited})
			continue
		}
		r, err := s.superviseOne(ctx, in, cycleID, jc, *cur)
		if err != nil {
			if errors.Is(err, ErrRunInProgress) {
				continue // 課題に終わっていない run がある。待ちのまま次の回りへ残す
			}
			return res, fmt.Errorf("supervise hold %d: %w", cur.HoldID, err)
		}
		if r.NotStarted != nil {
			res.NotStarted = append(res.NotStarted, *r.NotStarted)
			continue
		}
		res.SuperviseProcessed++
		if r.ExecutedHand != "" {
			res.SuperviseHands[r.ExecutedHand]++
		}
	}
	return res, nil
}

// superviseOneResult は 1 件の処理の結果。NotStarted は判断点を起動しなかったとき。ExecutedHand は手を実行したとき（結果が executed）の手。
type superviseOneResult struct {
	NotStarted   *NotStarted
	ExecutedHand SupervisorHand
}

// superviseOne は保留 1 件について、判断点を呼び・結果を確かめ・手を実行する。
func (s *Store) superviseOne(ctx context.Context, in CycleRunInput, cycleID string, jc *JudgmentCycle, t supervisorTarget) (superviseOneResult, error) {
	call, err := s.callSupervisorJudgment(ctx, in, cycleID, jc, t)
	if err != nil || call.NotStarted != nil {
		return superviseOneResult{NotStarted: call.NotStarted}, err
	}
	if call.Run.RateLimited {
		// 利用上限に当たった。保留はスーパーバイザー待ちのまま残し、対処の記録は残さない（次のサイクルで呼び直す）。
		return superviseOneResult{NotStarted: &NotStarted{ChallengeID: formatChallengeID(t.ChallengeID), Reason: NotStartedRateLimited}}, nil
	}
	out, bad := s.verifySupervisorResult(ctx, call)
	req := supervisorHandRequest{CycleID: cycleID, Target: t, RunID: call.Run.RunID, Decl: in.AgentDecl}
	if bad != "" {
		// 判断の間にオーナーが回答していたら、失敗として上げずに、結果を捨てる（owner_answered）。
		var owner bool
		if err := s.db.Read(ctx, func(tx *sql.Tx) error {
			var e error
			owner, e = supervisorHoldAnsweredByOwner(ctx, tx, t.HoldID)
			return e
		}); err != nil {
			return superviseOneResult{}, classifyReadWriteErr(err)
		}
		if owner {
			return superviseOneResult{}, s.recordSupervisorRejected(ctx, req, "", RejectOwnerAnswered)
		}
		return superviseOneResult{}, s.recordSupervisorFailure(ctx, req, nil, bad)
	}
	req.Output = *out
	outcome, err := s.applySupervisorHand(ctx, req)
	if err != nil {
		return superviseOneResult{}, err
	}
	if outcome.Result == ActionExecuted {
		return superviseOneResult{ExecutedHand: out.Hand}, nil
	}
	return superviseOneResult{}, nil
}
