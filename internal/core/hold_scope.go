package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

// HoldScope は保留の止める範囲。保留を立てるときに決めて hold.scope へ記録し、後から変えない。
// 空は課題を止める保留（hold.scope は NULL。S4c-2 までの保留と同じ）、HoldScopeTask はタスクを止める保留
// （そのタスクだけを止める。hold.task_id が結び付く）。
type HoldScope string

// HoldScopeTask はタスクを止める保留（hold.scope = 'task'）。
const HoldScopeTask HoldScope = "task"

// holdScopeLabel は作業ログへ残す、止める範囲の語（task か challenge）。
func holdScopeLabel(s HoldScope) string {
	if s == HoldScopeTask {
		return string(HoldScopeTask)
	}
	return "challenge"
}

var holdIDPattern = regexp.MustCompile(`^H-[1-9][0-9]*$`)

// formatHoldID は保留の表示用の ID（H-<番号>）。番号は保留の行の番号と同じで、列は持たない。過去の保留・閉じた保留も同じ形で読める。
func formatHoldID(id int64) string { return fmt.Sprintf("H-%d", id) }

// parseHoldID は H-<番号> を保留の行の番号として読む。形式が違えば ok=false。
func parseHoldID(s string) (int64, bool) {
	if !holdIDPattern.MatchString(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(s[2:], 10, 64)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// taskMayProceed は「このタスクを進めてよいか」の述語（全部の所が同じものを使う）。課題が着手中、または、課題が人間対応待ちで、
// 開いている保留がどれもタスクを止める保留で、そのタスクに開いている保留が無いとき真。
func taskMayProceed(ctx context.Context, tx *sql.Tx, challengeID, taskID int64) (bool, error) {
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM challenge WHERE id = ?`, challengeID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, err
	}
	switch Status(status) {
	case StatusInProgress:
		return true, nil
	case StatusAwaitingHuman:
		if taskID <= 0 {
			return false, nil // タスクに結び付かない起動・処理は、保留中の課題では進めない
		}
		// 開いている保留が 1 つも無い人間対応待ち（保留の行と合わない状態）は、進めない（保留の行が、進めてよい根拠）。
		var open, blocking int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*), COALESCE(SUM(COALESCE(scope, '') <> 'task' OR task_id = ?), 0) FROM hold WHERE challenge_id = ? AND answer IS NULL AND closed_at IS NULL`,
			taskID, challengeID).Scan(&open, &blocking); err != nil {
			return false, err
		}
		return open > 0 && blocking == 0, nil
	default:
		return false, nil
	}
}

// taskMayProceedSQL は taskMayProceed と同じ述語を、SQL の条件の断片にしたもの（課題の行の別名 c と、タスクの番号の式 taskExpr を受け取る）。
// SQL の中で使う所（契約の待ちの結び付きを外す UPDATE・周の数え）は、自前で書かずこれを使う。
// 開いている保留が 1 つも無い人間対応待ち（保留の行と合わない状態）は進めない。断片は課題が人間対応待ちのときの条件を含むので、taskExpr が NULL になりうるときは呼び出し側で IS NOT NULL を併記する。
func taskMayProceedSQL(c, taskExpr string) string {
	return "(" + c + ".status = 'in_progress' OR (" + c + ".status = 'awaiting_human' AND " + taskExpr + " IS NOT NULL AND EXISTS (" +
		"SELECT 1 FROM hold oa WHERE oa.challenge_id = " + c + ".id AND oa.answer IS NULL AND oa.closed_at IS NULL) AND NOT EXISTS (" +
		"SELECT 1 FROM hold o WHERE o.challenge_id = " + c + ".id AND o.answer IS NULL AND o.closed_at IS NULL " +
		"AND (COALESCE(o.scope, '') <> 'task' OR o.task_id = " + taskExpr + "))))"
}

// holdNotOpenTaskScopedSQL は、課題の単位で「最後の保留」を読んで境を決める所が、ほかのタスクの開いている保留
// （タスクを止める保留で、回答も閉じもまだ無いもの）を境に数えないための条件（hold の列を修飾なしで使う）。
const holdNotOpenTaskScopedSQL = `NOT (COALESCE(scope, '') = 'task' AND answer IS NULL AND closed_at IS NULL)`

// challengeScopedHoldSQL は、課題を止める保留に絞って読む条件（hold の列を修飾なしで使う）。
const challengeScopedHoldSQL = `COALESCE(scope, '') <> 'task'`

// countOpenHolds は、課題の開いている保留（回答が無く、閉じていない）の数。閉じる書き込みと同じトランザクションの中で数える。
func countOpenHolds(ctx context.Context, tx *sql.Tx, challengeID int64) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM hold WHERE challenge_id = ? AND answer IS NULL AND closed_at IS NULL`, challengeID).Scan(&n)
	return n, err
}

// taskStopOptions は、保留を作る呼び出し元が止める範囲を渡すための指定を、opts へ足して返す。タスクを止める保留にするのは、
// 統合先を使う課題で、着手中の課題の 1 つのタスクの作業の中で起きた止まり（tc.taskID が結び付いている）だけ。
// それ以外（タスクに結び付かない・統合先を使わない・検証中から立つ）は課題を止める保留のまま（opts を変えない）。
func taskStopOptions(ctx context.Context, tc *transitionCtx, opts HoldOptionsInput) (HoldOptionsInput, error) {
	if opts.ChallengeWide || tc.taskID == nil || (tc.current.Status != StatusInProgress && tc.current.Status != StatusAwaitingHuman) {
		return opts, nil
	}
	integ, err := challengeUsesIntegration(ctx, tc.tx, tc.id)
	if err != nil {
		return opts, err
	}
	if integ != nil && *integ {
		opts.Scope = HoldScopeTask
	}
	return opts, nil
}

// closeOtherOpenHolds は、exceptHoldID 以外の開いている保留を、回答なしで閉じる共通の関数（計画を作り直すときに使う）。
// 閉じた保留の ID（H-<番号>）と理由 reason を、遷移の作業ログ（after）へ残す。閉じた保留の ID の並びを返す。
func closeOtherOpenHolds(ctx context.Context, tc *transitionCtx, exceptHoldID int64, reason string) ([]string, error) {
	rows, err := tc.tx.QueryContext(ctx,
		`SELECT id FROM hold WHERE challenge_id = ? AND id <> ? AND answer IS NULL AND closed_at IS NULL ORDER BY id ASC`, tc.id, exceptHoldID)
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
	if err := rows.Close(); err != nil {
		return nil, err
	}
	closed := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, err := tc.tx.ExecContext(ctx, `UPDATE hold SET closed_at = ? WHERE id = ? AND answer IS NULL AND closed_at IS NULL`, tc.nowStr, id); err != nil {
			return nil, err
		}
		closed = append(closed, formatHoldID(id))
	}
	if len(closed) > 0 {
		tc.after["closed_holds"] = closed
		tc.after["closed_holds_reason"] = reason
	}
	return closed, nil
}

// mayProceedOn は、タスク taskID（nil ならタスクに結び付かない作業）の作業を進めてよいか。タスクに結び付くときは taskMayProceed、
// 結び付かないときは課題が着手中のときだけ。
func mayProceedOn(ctx context.Context, tx *sql.Tx, challengeID int64, taskID *int64) (bool, error) {
	if taskID != nil {
		return taskMayProceed(ctx, tx, challengeID, *taskID)
	}
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM challenge WHERE id = ?`, challengeID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, err
	}
	return Status(status) == StatusInProgress, nil
}

// tcTaskMayProceed は、遷移の中（tc）で「このタスクを進めてよいか」を述語 taskMayProceed と同じ規則で確かめる（課題の状態は tc の
// 現在の値で見る）。taskID が nil（タスクに結び付かない）なら、課題が着手中のときだけ真。
func tcTaskMayProceed(ctx context.Context, tc *transitionCtx, taskID *int64) (bool, error) {
	switch tc.current.Status {
	case StatusInProgress:
		return true, nil
	case StatusAwaitingHuman:
		if taskID == nil {
			return false, nil
		}
		return taskMayProceed(ctx, tc.tx, tc.id, *taskID)
	}
	return false, nil
}

// taskMayProceedFor は taskMayProceed を、読み取りの中で呼ぶ版。taskID が nil（タスクに結び付かない）なら、課題が着手中のときだけ真。
func (s *Store) taskMayProceedFor(ctx context.Context, cid int64, taskID *int64) (bool, error) {
	var ok bool
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		if taskID != nil {
			v, err := taskMayProceed(ctx, tx, cid, *taskID)
			ok = v
			return err
		}
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM challenge WHERE id = ?`, cid).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		ok = Status(status) == StatusInProgress
		return nil
	})
	return ok, classifyReadWriteErr(err)
}

// loadChallengesWithProceedableTasks は、タスクを進めうる課題（着手中の課題と、開いている保留がどれもタスクを止める保留の人間対応待ちの課題）を、
// 優先度→ID の順に返す。人間対応待ちの課題の、どのタスクを進めてよいかは、taskMayProceed が決める。
func loadChallengesWithProceedableTasks(ctx context.Context, tx *sql.Tx) ([]Challenge, error) {
	in, err := loadChallengesByStatusSorted(ctx, tx, StatusInProgress)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, challengeSelectColumns+` WHERE status = ? AND NOT EXISTS
		(SELECT 1 FROM hold h WHERE h.challenge_id = challenge.id AND h.answer IS NULL AND h.closed_at IS NULL AND COALESCE(h.scope, '') <> 'task')
		AND EXISTS (SELECT 1 FROM hold h WHERE h.challenge_id = challenge.id AND h.answer IS NULL AND h.closed_at IS NULL) ORDER BY id ASC`,
		string(StatusAwaitingHuman))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := in
	for rows.Next() {
		c, err := scanChallengeRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	stableSortChallengesByPriority(out)
	return out, nil
}

// openTaskHoldTaskIDs は、課題の、開いているタスクを止める保留が結び付くタスクの内部 ID の集合。
func openTaskHoldTaskIDs(ctx context.Context, tx *sql.Tx, cid int64) (map[int64]bool, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT task_id FROM hold WHERE challenge_id = ? AND scope = 'task' AND task_id IS NOT NULL AND answer IS NULL AND closed_at IS NULL`, cid)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// contractRunTaskIDFor は contractRunTaskID を読み取りの中で呼ぶ版。
func (s *Store) contractRunTaskIDFor(ctx context.Context, cid int64, externalRunID string) (*int64, error) {
	var id *int64
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		v, err := contractRunTaskID(ctx, tx, cid, externalRunID)
		id = v
		return err
	})
	return id, classifyReadWriteErr(err)
}

// challengeHasOnlyTaskHolds は、課題に開いている保留があり、そのどれもがタスクを止める保留か。
func challengeHasOnlyTaskHolds(ctx context.Context, tx *sql.Tx, cid int64) (bool, error) {
	var open, blocking int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(COALESCE(scope, '') <> 'task'), 0) FROM hold WHERE challenge_id = ? AND answer IS NULL AND closed_at IS NULL`, cid).Scan(&open, &blocking)
	return open > 0 && blocking == 0, err
}

// childQuestionTargetMayProceed は、子の問い（保留を作る前）へのスーパーバイザーの手を、判断の間に状態が変わっていないか確かめて実行してよいか
// （課題が着手中、または、タスクを止める保留だけが開いていて、このタスクに保留が無い）。
func childQuestionTargetMayProceed(ctx context.Context, tx *sql.Tx, ch *Challenge, taskID *int64) (bool, error) {
	if ch.Status == StatusInProgress {
		return true, nil
	}
	if ch.Status != StatusAwaitingHuman || taskID == nil {
		return false, nil
	}
	cid, ok := parseChallengeID(ch.ID)
	if !ok {
		return false, nil
	}
	return taskMayProceed(ctx, tx, cid, *taskID)
}
