package core

// このファイルは衝突の記録（challenge_conflict。docs/features/ui-relation-graph.md §衝突（core））の
// 読み取りと、委譲の起動に伴う向きの更新を持つ。委譲の段は衝突の記録を新しく書かない
// （過去の記録の読み取りのために残す）。関係グラフの API が衝突の辺（kind=conflict）として返し、
// ボードの詳細が「衝突のため後回し」の理由として読む。
//
// 前提の依存（challenge_dependency）とは別の表で、依存の評価（dependency.go）・委譲の対象の選び方・
// 起動の判定は衝突の行を読まない。

import (
	"context"
	"database/sql"
	"sort"
)

// orientConflictsTx は課題 delegated の委譲の run を記録するトランザクションの中で、その課題との
// 向きの無い組を「相手（後回し）→ delegated（先に委譲）」の向きにする。すでに向きのある組は変えない。
func orientConflictsTx(ctx context.Context, tx *sql.Tx, delegated int64) error {
	// 起動した課題はもう後回しではないので、後回しの側として持つ向きのある組は外す（待っている理由に残さない）。
	if _, err := tx.ExecContext(ctx, `DELETE FROM challenge_conflict WHERE directed = 1 AND challenge_id = ?`, delegated); err != nil {
		return err
	}
	type row struct {
		a, b       int64
		cycle, run sql.NullInt64
		createdAt  string
		other      int64
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT challenge_id, conflicts_with_id, cycle_id, run_id, created_at FROM challenge_conflict
		 WHERE directed = 0 AND (challenge_id = ? OR conflicts_with_id = ?)`, delegated, delegated)
	if err != nil {
		return err
	}
	var undirected []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.a, &r.b, &r.cycle, &r.run, &r.createdAt); err != nil {
			_ = rows.Close()
			return err
		}
		r.other = r.a
		if r.a == delegated {
			r.other = r.b
		}
		undirected = append(undirected, r)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range undirected {
		if _, err := tx.ExecContext(ctx, `DELETE FROM challenge_conflict WHERE challenge_id = ? AND conflicts_with_id = ?`, r.a, r.b); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO challenge_conflict (challenge_id, conflicts_with_id, directed, cycle_id, run_id, created_at) VALUES (?, ?, 1, ?, ?, ?)`,
			r.other, delegated, r.cycle, r.run, r.createdAt); err != nil {
			return err
		}
	}
	return nil
}

// activeConflict は両端とも終わっていない（完了・取り下げ以外の）課題の衝突の行 1 つ。
type activeConflict struct {
	Challenge int64
	With      int64
	Directed  bool
	CreatedAt string
}

// loadActiveConflictsTx は challenge_conflict の行のうち、両端の課題が完了でも取り下げでもないものを返す
// （片方が終わった衝突は返さない。行は残す）。(challenge_id, conflicts_with_id) の数の昇順。
func loadActiveConflictsTx(ctx context.Context, tx *sql.Tx) ([]activeConflict, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT cc.challenge_id, cc.conflicts_with_id, cc.directed, cc.created_at
		 FROM challenge_conflict cc
		 JOIN challenge a ON a.id = cc.challenge_id
		 JOIN challenge b ON b.id = cc.conflicts_with_id
		 WHERE a.status NOT IN (?, ?) AND b.status NOT IN (?, ?)`,
		string(StatusDone), string(StatusWithdrawn), string(StatusDone), string(StatusWithdrawn))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []activeConflict
	for rows.Next() {
		var c activeConflict
		var directed int
		if err := rows.Scan(&c.Challenge, &c.With, &directed, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Directed = directed == 1
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Challenge != out[j].Challenge {
			return out[i].Challenge < out[j].Challenge
		}
		return out[i].With < out[j].With
	})
	return out, nil
}
