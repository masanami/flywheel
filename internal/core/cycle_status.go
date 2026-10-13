package core

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// CycleState はサイクルの実行の状態（UI の「1 周回す」の表示用）。
type CycleState string

// CycleState の 3 値。
const (
	CycleStateIdle    CycleState = "idle"
	CycleStateRunning CycleState = "running"
	CycleStateStale   CycleState = "stale"
)

// CycleStatus は ReadCycleStatus の結果。
type CycleStatus struct {
	State CycleState
	// LockHeld はサイクルの排他ロックの行があること（heartbeat の新旧によらない）。
	LockHeld bool
	// LastStartedAt は ID が最大の周の開始時刻。LastEndedAt・LastResult は
	// 終了した周のうち ID が最大のものの値。該当が無ければ nil。
	LastStartedAt *time.Time
	LastEndedAt   *time.Time
	LastResult    *CycleResult
}

// ReadCycleStatus はサイクルの排他ロックと周の記録を読んで状態を導く。読むだけで、
// 古いロックの回収・周の終了はしない（回収は次の cycle の BeginCycle が行う）。
//
// ロックがあれば、heartbeat_at が閾値（300 秒。BeginCycle の回収と同じ値）以内なら
// running、より古ければ stale（保持者のプロセスの生死は見ない）。ロックが無ければ、
// 終了していない周が無いとき idle、あるとき stale。
func (s *Store) ReadCycleStatus(ctx context.Context) (*CycleStatus, error) {
	now := s.currentTime()
	threshold := s.lockStaleAfter
	if threshold <= 0 {
		threshold = defaultLockStaleAfter
	}
	out := &CycleStatus{}
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		lock, err := loadLockByName(ctx, tx, cycleLockName)
		if err != nil {
			return err
		}
		var openCycles int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM cycle WHERE ended_at IS NULL`).Scan(&openCycles); err != nil {
			return err
		}
		switch {
		case lock != nil:
			out.LockHeld = true
			if now.Sub(lock.HeartbeatAt) <= threshold {
				out.State = CycleStateRunning
			} else {
				out.State = CycleStateStale
			}
		case openCycles > 0:
			out.State = CycleStateStale
		default:
			out.State = CycleStateIdle
		}

		// ID は整数の主キー（表示は "Y-<n>"）なので、最大は整数で比べる。
		latest, err := scanOptionalCycle(tx.QueryRowContext(ctx, cycleSelectColumns+` ORDER BY id DESC LIMIT 1`))
		if err != nil {
			return err
		}
		if latest != nil {
			t := latest.StartedAt
			out.LastStartedAt = &t
		}
		ended, err := scanOptionalCycle(tx.QueryRowContext(ctx, cycleSelectColumns+` WHERE ended_at IS NOT NULL ORDER BY id DESC LIMIT 1`))
		if err != nil {
			return err
		}
		if ended != nil {
			e := *ended.EndedAt
			r := ended.Result
			out.LastEndedAt, out.LastResult = &e, &r
		}
		return nil
	})
	if err != nil {
		return nil, classifyReadWriteErr(err)
	}
	return out, nil
}

func scanOptionalCycle(row interface{ Scan(dest ...any) error }) (*cycleRow, error) {
	c, err := scanCycleRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}
