package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// judgmentLimitHit は判断点（J1・J2・J5）の連続失敗の上限の検出。
type judgmentLimitHit struct {
	Judgment JudgmentPoint
	Count    int
	Limit    int
	// Last は最後に数えた失敗の run（保留の原因の run になる）。
	Last runRow
}

// judgmentFailureLimit は判断点の連続失敗の上限。J3 の failure_limit と同じ値を使う。
func judgmentFailureLimit(decl *AgentDeclaration) int {
	if decl == nil || decl.FailureLimit <= 0 {
		return defaultFailureLimit
	}
	return decl.FailureLimit
}

// loadJudgmentLimitHit は、課題の判断点 j の run を、課題の現在の版（version）に限り、最後の
// 回答済みの保留より後のものだけ数えて、連続失敗の数が limit に達していれば検出を返す。
// 枠超過の run は数えず連続も切らない（J3 の decideLaunch と同じ規則）。
func loadJudgmentLimitHit(ctx context.Context, tx *sql.Tx, challengeID int64, version int, j JudgmentPoint, limit int) (*judgmentLimitHit, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM run WHERE challenge_id = ? AND kind = 'judgment' AND judgment = ? AND challenge_version = ? ORDER BY id ASC`,
		challengeID, string(j), int64(version))
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	if len(ids) == 0 {
		return nil, nil
	}

	var lastHold *holdRef
	var (
		ref                    holdRef
		runID                  sql.NullInt64
		answer, raisedAtString sql.NullString
	)
	err = tx.QueryRowContext(ctx,
		`SELECT id, run_id, answer, raised_at FROM hold WHERE challenge_id = ? ORDER BY id DESC LIMIT 1`, challengeID).
		Scan(&ref.ID, &runID, &answer, &raisedAtString)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, err
	case answer.Valid:
		if runID.Valid {
			v := runID.Int64
			ref.RunID = &v
		}
		if ref.RaisedAt, err = parseTimestamp(raisedAtString.String); err != nil {
			return nil, err
		}
		lastHold = &ref
	}

	var since []runRow
	for _, id := range ids {
		r, err := loadRunByID(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if r == nil {
			continue
		}
		if lastHold != nil && holdIsAfterRun(*lastHold, *r) {
			continue
		}
		since = append(since, *r)
	}

	count := 0
	var last *runRow
	for i := len(since) - 1; i >= 0; i-- {
		r := since[i]
		if r.RateLimited {
			continue
		}
		if !isCountedFailure(r.Result) {
			break
		}
		count++
		if last == nil {
			rr := r
			last = &rr
		}
	}
	if last == nil || count < limit {
		return nil, nil
	}
	return &judgmentLimitHit{Judgment: j, Count: count, Limit: limit, Last: *last}, nil
}

// judgmentLimitQuestion は判断点の連続失敗の上限で人間対応待ちにするときの保留の問い。
func judgmentLimitQuestion(hit *judgmentLimitHit) string {
	return fmt.Sprintf("判断点 %s の連続失敗の上限に達したため、自動の判断の呼び出しを止めて人間対応待ちにした（判断点: %s、連続失敗: %d 回、上限: %d）。\n"+
		"直近の失敗: %s（結果: %s）。回答すると、次の周から数え直して再び自動で呼び出す。",
		hit.Judgment, hit.Judgment, hit.Count, hit.Limit, hit.Last.ID, hit.Last.Result)
}

// holdForJudgmentFailureLimit は、自動の段が判断点 j を起動する直前に呼ぶ。課題の連続失敗が上限に
// 達していれば、判断を起動せず課題を人間対応待ち（T11）にして NotStarted（failure_limit）を返す。
// 達していなければ (nil, nil)。want は j を起動してよい課題の状態で、読み直した状態が違えば何もしない。
func (s *Store) holdForJudgmentFailureLimit(ctx context.Context, ch Challenge, j JudgmentPoint, want Status, decl *AgentDeclaration, cycleID string) (ns *NotStarted, skip bool, err error) {
	cid, ok := parseChallengeID(ch.ID)
	if !ok {
		return nil, false, ErrNotFound
	}
	limit := judgmentFailureLimit(decl)
	var hit *judgmentLimitHit
	err = s.db.Read(ctx, func(tx *sql.Tx) error {
		h, err := loadJudgmentLimitHit(ctx, tx, cid, ch.Version, j, limit)
		hit = h
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, false, err
	}
	if hit == nil {
		return nil, false, nil
	}
	_, err = s.transitionAsInvoker(ctx, hit.Last.ID, ch.ID, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		if tc.current.Status != want || tc.current.Version != ch.Version {
			return errReconcileNotEligible
		}
		return s.insertHoldWithOccurrence(ctx, tc, judgmentLimitQuestion(hit), holdReasonFailureLimit, cycleID)
	})
	if err != nil {
		if errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrTerminalState) || errors.Is(err, errReconcileNotEligible) {
			return nil, true, nil // 課題が対象でなくなった。起動しない
		}
		return nil, false, err
	}
	return &NotStarted{ChallengeID: ch.ID, Reason: NotStartedFailureLimit,
		Detail: fmt.Sprintf("%s: %d consecutive failures (limit %d)", hit.Judgment, hit.Count, hit.Limit)}, false, nil
}
