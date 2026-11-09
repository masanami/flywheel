package core

import (
	"context"
	"database/sql"
)

// このファイルは課題が待っている理由（docs/features/ui-relation-graph.md §待っている理由）を持つ。
// 前提の依存（まだ終わっていない依存先）と、衝突のため後回しにした相手（challenge_conflict）を、別の一覧で返す。
// 衝突は前提ではない（着手の門にしない）ので、前提の一覧には混ぜない。

// WaitingChallenge は待っている相手の課題 1 件。
type WaitingChallenge struct {
	ChallengeID string
	Title       string
	Status      Status
}

// WaitingReasons は課題が待っている理由。どちらも ID の数の昇順で、無ければ空スライス（nil でない）。
//
//   - Prerequisites: 依存先のうち、まだ満たされていない（依存の評価で Met でない。開いた本番反映が残る完了済みの課題を含む）課題。
//   - Conflicts: 衝突のため後回しにした相手（向きのある衝突の challenge_id がこの課題で、相手も終わっていないもの）。
//     先に委譲した側・向きの無い組には付かない（待っている側が無い）。
//
// 課題自身が完了・取り下げなら、どちらも空。
type WaitingReasons struct {
	Prerequisites []WaitingChallenge
	Conflicts     []WaitingChallenge
}

func loadWaitingReasons(ctx context.Context, tx *sql.Tx, cid int64, status Status, eval *dependencyEvaluation) (WaitingReasons, error) {
	out := WaitingReasons{Prerequisites: []WaitingChallenge{}, Conflicts: []WaitingChallenge{}}
	if status == StatusDone || status == StatusWithdrawn {
		return out, nil
	}
	load := func(id int64) (WaitingChallenge, error) {
		var title, st string
		if err := tx.QueryRowContext(ctx, `SELECT title, status FROM challenge WHERE id = ?`, id).Scan(&title, &st); err != nil {
			return WaitingChallenge{}, err
		}
		return WaitingChallenge{ChallengeID: formatChallengeID(id), Title: title, Status: Status(st)}, nil
	}
	for _, d := range eval.Dependencies[cid] {
		if d.Met {
			continue
		}
		w, err := load(d.DependsOn)
		if err != nil {
			return out, err
		}
		out.Prerequisites = append(out.Prerequisites, w)
	}
	conflicts, err := loadActiveConflictsTx(ctx, tx)
	if err != nil {
		return out, err
	}
	for _, c := range conflicts {
		if c.Challenge != cid || !c.Directed {
			continue
		}
		w, err := load(c.With)
		if err != nil {
			return out, err
		}
		out.Conflicts = append(out.Conflicts, w)
	}
	return out, nil
}
