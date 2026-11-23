package core

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// CycleInsights は対象のサイクルの、スーパーバイザーの手と費用の内訳（サイクルの口の値）。
// CycleStatus（ターミナルの `cycle status --json` と共有する型）には足さず、別の型で返す。
type CycleInsights struct {
	// CycleID は対象のサイクル（"Y-<n>"）。サイクルを一度も回していなければ空で、手と内訳も空。
	CycleID string
	// SupervisorActions は対象のサイクルで打った手（対処の記録）を新しい順に並べたもの。
	SupervisorActions []SupervisorAction
	// Costs は対象のサイクルの終わった実行を、実行の種類（判断点は役割ごと）で足したもの。
	Costs []CycleCost
	// CostTotalMicros は Costs の SpentMicros の合計（丸める前）。サイクルの費用（終わった実行の費用の合計）と等しい。
	CostTotalMicros int64
	// CostTotalUSD は CostTotalMicros を USD にした値。
	CostTotalUSD float64
}

// ReadCycleInsights は対象のサイクルのスーパーバイザーの手と費用の内訳を読む。読むだけでストアは変わらない。
//
// 対象のサイクルは ReadCycleStatus と同じ決まりで選ぶ: 走っている（排他があり heartbeat が回収の時間以内）ならそのサイクル、
// 走っていない（排他が無い・stale）なら、個別の操作の周を除いた最後のサイクル。
func (s *Store) ReadCycleInsights(ctx context.Context) (*CycleInsights, error) {
	now, threshold := s.currentTime(), s.lockStaleThreshold()
	out := &CycleInsights{SupervisorActions: []SupervisorAction{}, Costs: []CycleCost{}}
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		id, err := targetCycleID(ctx, tx, now, threshold)
		if err != nil || id == 0 {
			return err
		}
		out.CycleID = formatCycleID(id)
		actions, err := querySupervisorActions(ctx, tx, `WHERE cycle_id = ?`, id)
		if err != nil {
			return err
		}
		for i := len(actions) - 1; i >= 0; i-- {
			out.SupervisorActions = append(out.SupervisorActions, actions[i])
		}
		out.Costs, out.CostTotalMicros, err = readCycleCosts(ctx, tx, id)
		out.CostTotalUSD = microsToUSD(out.CostTotalMicros)
		return err
	})
	if err != nil {
		return nil, classifyReadWriteErr(err)
	}
	return out, nil
}

// targetCycleID はサイクルの口の対象のサイクルの内部 ID を返す（無ければ 0）。走っているサイクル（排他の保持者）か、
// 走っていなければ個別の操作の周を除いた最後のサイクル。
func targetCycleID(ctx context.Context, tx *sql.Tx, now time.Time, threshold time.Duration) (int64, error) {
	lock, err := loadLockByName(ctx, tx, cycleLockName)
	if err != nil {
		return 0, err
	}
	if lock != nil && now.Sub(lock.HeartbeatAt) <= threshold {
		// 排他の持ち主でも、個別の操作の周なら対象にしない（その前のふつうのサイクルへ落ちる）。
		if id, ok := parseCycleID(lock.Holder); ok {
			c, err := scanOptionalCycle(tx.QueryRowContext(ctx, cycleSelectColumns+` WHERE id = ? AND `+notIndividualOpFilter, id))
			if err != nil {
				return 0, err
			}
			if c != nil {
				return id, nil
			}
		}
	}
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM cycle WHERE `+notIndividualOpFilter+` ORDER BY id DESC LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// lockStaleThreshold は排他の heartbeat が回収の時間を過ぎたと見る閾値（ReadCycleStatus・RequestCycleStop と同じ値）。
func (s *Store) lockStaleThreshold() time.Duration {
	if s.lockStaleAfter > 0 {
		return s.lockStaleAfter
	}
	return defaultLockStaleAfter
}
