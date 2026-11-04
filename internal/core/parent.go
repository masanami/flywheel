package core

import (
	"context"
	"database/sql"
)

// このファイルは課題の親子（challenge_parent。docs/features/ui-relation-graph.md §親子）の
// 登録と取り外しを持つ。親子は関係グラフの入れ子の表示にだけ使い、状態機械・委譲の起動の判定・
// 依存の評価には使わない。親は 0 個か 1 個（2 つ目の登録は付け替え）。課題の版は上げない
// （版は承認の照合に使うので、表示だけの関係の変更では動かさない）。

// SetParent は id の課題の親を parentID の課題にする。検査の順は 自己参照（ErrSelfParent）→
// 存在（ErrNotFound。子・親のどちらも。形式不正の ID を含む）→ 循環（ErrParentCycle）で、
// 最初に当たったもので何も書かずに終わる。既に同じ親なら何もしない。親がある課題には付け替えになる。
// 課題の状態（完了を含む）は問わない。変えたときは作業ログに entity=challenge・action=parent_set を残す。
func (s *Store) SetParent(ctx context.Context, ch Channel, id, parentID string) error {
	cid, parsedChild := parseChallengeID(id)
	pid, parsedParent := parseChallengeID(parentID)
	if parsedChild && parsedParent && cid == pid {
		return ErrSelfParent
	}
	if !parsedChild || !parsedParent {
		return ErrNotFound
	}
	return s.mutate(ctx, ch, func(tx *sql.Tx, rec *activityRecorder) error {
		if err := challengeExists(ctx, tx, cid); err != nil {
			return err
		}
		if err := challengeExists(ctx, tx, pid); err != nil {
			return err
		}
		_, err := setParentInTx(ctx, tx, rec, cid, pid)
		return err
	})
}

// ClearParent は id の課題の親を外す。親が無ければ何もしない。外したときは作業ログに
// entity=challenge・action=parent_clear を残す。
func (s *Store) ClearParent(ctx context.Context, ch Channel, id string) error {
	cid, ok := parseChallengeID(id)
	if !ok {
		return ErrNotFound
	}
	return s.mutate(ctx, ch, func(tx *sql.Tx, rec *activityRecorder) error {
		if err := challengeExists(ctx, tx, cid); err != nil {
			return err
		}
		_, err := clearParentInTx(ctx, tx, rec, cid)
		return err
	})
}

func challengeExists(ctx context.Context, tx *sql.Tx, id int64) error {
	var one int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM challenge WHERE id = ?`, id).Scan(&one); err != nil {
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// loadParentOf は cid の親の内部 ID を返す（無ければ ok=false）。
func loadParentOf(ctx context.Context, tx *sql.Tx, cid int64) (parent int64, ok bool, err error) {
	err = tx.QueryRowContext(ctx, `SELECT parent_id FROM challenge_parent WHERE challenge_id = ?`, cid).Scan(&parent)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return parent, true, nil
}

// parentWouldCycle は cid の親を pid にすると輪ができるか（pid から親をたどって cid に届くか）。
// 親は 1 つまでなので、たどる道は 1 本。ストアに既にある輪に入り込んでも止まるよう、訪れた課題を覚える。
func parentWouldCycle(ctx context.Context, tx *sql.Tx, cid, pid int64) (bool, error) {
	seen := map[int64]bool{}
	for cur := pid; ; {
		if cur == cid {
			return true, nil
		}
		if seen[cur] {
			return false, nil
		}
		seen[cur] = true
		next, ok, err := loadParentOf(ctx, tx, cur)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
		cur = next
	}
}

// setParentInTx は cid の親を pid にする（存在の確認は呼び出し側）。変えたときだけ true。
// 循環になるなら ErrParentCycle を返し、何も書かない。
func setParentInTx(ctx context.Context, tx *sql.Tx, rec *activityRecorder, cid, pid int64) (bool, error) {
	old, had, err := loadParentOf(ctx, tx, cid)
	if err != nil {
		return false, err
	}
	if had && old == pid {
		return false, nil
	}
	cycle, err := parentWouldCycle(ctx, tx, cid, pid)
	if err != nil {
		return false, err
	}
	if cycle {
		return false, ErrParentCycle
	}
	if had {
		_, err = tx.ExecContext(ctx, `UPDATE challenge_parent SET parent_id = ?, created_at = ? WHERE challenge_id = ?`, pid, formatTimestamp(rec.at), cid)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO challenge_parent (challenge_id, parent_id, created_at) VALUES (?, ?, ?)`, cid, pid, formatTimestamp(rec.at))
	}
	if err != nil {
		return false, err
	}
	var before map[string]any
	if had {
		before = map[string]any{"parent_id": formatChallengeID(old)}
	}
	if err := rec.record("challenge", cid, ActivityActionParentSet, before, map[string]any{"parent_id": formatChallengeID(pid)}); err != nil {
		return false, err
	}
	return true, nil
}

// clearParentInTx は cid の親を外す。外したときだけ true。
func clearParentInTx(ctx context.Context, tx *sql.Tx, rec *activityRecorder, cid int64) (bool, error) {
	old, had, err := loadParentOf(ctx, tx, cid)
	if err != nil || !had {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM challenge_parent WHERE challenge_id = ?`, cid); err != nil {
		return false, err
	}
	if err := rec.record("challenge", cid, ActivityActionParentClear, map[string]any{"parent_id": formatChallengeID(old)}, nil); err != nil {
		return false, err
	}
	return true, nil
}
