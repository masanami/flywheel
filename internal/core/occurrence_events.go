package core

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// このファイルは、J5 の写像・判断点の run の終了・中断した run の回収が、出来事と同じ
// トランザクションの中で発生を作るための薄い呼び出しを持つ（行の挿入は insertOccurrenceRow だけ）。

// recordsFailureOccurrence は結果 r の run の終了で、失敗の発生（judgment_failed・delegation_failed）を
// 作るか。M3 の連続失敗に数える結果（isCountedFailure）のうち、枠超過の印（rateLimited）の run は除く。
// budget_exhausted は isCountedFailure が数えない。
func recordsFailureOccurrence(r RunResult, rateLimited bool) bool {
	return !rateLimited && isCountedFailure(r)
}

// insertRunOccurrence は run（内部整数 ID）を参照と原因の run とする発生を 1 行作る。課題と周の ID は
// 同じトランザクションの中で run の行から引く。課題を持たない run（衝突の予測）には作らない。
func (s *Store) insertRunOccurrence(ctx context.Context, tx *sql.Tx, runID int64, kind OccurrenceKind, outcome OccurrenceOutcome, at time.Time) error {
	r, err := loadRunByID(ctx, tx, runID)
	if err != nil {
		return err
	}
	if r == nil {
		return fmt.Errorf("core: insert run occurrence: run %d not found", runID)
	}
	if r.Kind == runKindPredict {
		return nil
	}
	cid, ok := parseChallengeID(r.ChallengeID)
	if !ok {
		return fmt.Errorf("core: insert run occurrence: malformed challenge id %q", r.ChallengeID)
	}
	var cycleID *int64
	if r.CycleID != nil {
		n, ok := parseCycleID(*r.CycleID)
		if !ok {
			return fmt.Errorf("core: insert run occurrence: malformed cycle id %q", *r.CycleID)
		}
		cycleID = &n
	}
	rid := runID
	return s.insertOccurrenceRow(tx, occurrenceInput{
		ChallengeID: cid, CycleID: cycleID, CauseRunID: &rid, Kind: kind,
		Ref: OccurrenceRef{RunID: &rid}, Outcome: outcome, At: at,
	})
}

// insertRunFailureOccurrence は、終了した run（結果 result・枠超過の印 rateLimited）が連続失敗に数える
// 結果なら、判断点の run には judgment_failed、委譲の run には delegation_failed を作る。
// 数えない結果（budget_exhausted・枠超過・succeeded など）では何もしない。
func (s *Store) insertRunFailureOccurrence(ctx context.Context, tx *sql.Tx, runID int64, result RunResult, rateLimited bool, at time.Time) error {
	if !recordsFailureOccurrence(result, rateLimited) {
		return nil
	}
	r, err := loadRunByID(ctx, tx, runID)
	if err != nil {
		return err
	}
	if r == nil {
		return fmt.Errorf("core: insert run failure occurrence: run %d not found", runID)
	}
	var kind OccurrenceKind
	switch r.Kind {
	case runKindJudgment:
		// 対象は J1〜J5 だけ（許可リスト。J6 の失敗は発生を作らない）。
		switch r.Judgment {
		case judgmentJ1, judgmentJ2, judgmentJ3, judgmentJ4, judgmentJ5:
		default:
			return nil
		}
		kind = OccurrenceKindJudgmentFailed
	case runKindDelegate:
		kind = OccurrenceKindDelegationFailed
	default:
		return nil
	}
	return s.insertRunOccurrence(ctx, tx, runID, kind, OccurrenceOutcomeBad, at)
}
