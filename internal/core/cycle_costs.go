package core

import (
	"context"
	"database/sql"
)

// CycleCost は対象のサイクルの、終わった実行（run）を実行の種類で足した 1 行。
type CycleCost struct {
	// Kind は run.kind。Judgment は Kind が判断点のときの役割（それ以外は空）。
	Kind     RunKind
	Judgment JudgmentPoint
	Runs     int
	// SpentMicros は費用の合計（USD の 100 万分の 1 単位。丸める前の値）。SpentUSD はその USD 換算。
	SpentMicros int64
	SpentUSD    float64
}

// readCycleCosts は周 cycleID の終わった実行（ended_at が入っているもの）だけを、種類（判断点は役割）ごとに足す。
// 合計は cycleSpentMicros（サイクルの費用）と同じ条件・同じ丸め前の整数で足すので、等しくなる。
func readCycleCosts(ctx context.Context, tx *sql.Tx, cycleID int64) ([]CycleCost, int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT kind, COALESCE(judgment, ''), COUNT(*), COALESCE(SUM(cost_usd), 0)
		FROM run WHERE cycle_id = ? AND ended_at IS NOT NULL GROUP BY kind, COALESCE(judgment, '') ORDER BY kind, COALESCE(judgment, '')`, cycleID)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	out := []CycleCost{}
	var total int64
	for rows.Next() {
		var (
			c              CycleCost
			kind, judgment string
		)
		if err := rows.Scan(&kind, &judgment, &c.Runs, &c.SpentMicros); err != nil {
			return nil, 0, err
		}
		c.Kind, c.Judgment = RunKind(kind), JudgmentPoint(judgment)
		c.SpentUSD = microsToUSD(c.SpentMicros)
		total += c.SpentMicros
		out = append(out, c)
	}
	return out, total, rows.Err()
}
