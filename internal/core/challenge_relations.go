package core

import (
	"context"
	"database/sql"
)

// このファイルは課題の親子の記録を読む口を持つ。課題のツリー（tree.go）が使う。
// どれも呼び出し側の読み取りのトランザクションの中で使い、何も書かない。

// loadParentMap は課題の親子（子の内部 ID → 親の内部 ID）を読む。親の無い課題は含めない。
func loadParentMap(ctx context.Context, tx *sql.Tx) (map[int64]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT challenge_id, parent_id FROM challenge_parent`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]int64{}
	for rows.Next() {
		var c, p int64
		if err := rows.Scan(&c, &p); err != nil {
			return nil, err
		}
		out[c] = p
	}
	return out, rows.Err()
}
