package core

import (
	"context"
	"database/sql"
)

// このファイルは課題が待っている理由（docs/features/ui-relation-graph.md §待っている理由）を持つ。
// 前提の依存（まだ終わっていない依存先）を返す。

// WaitingChallenge は待っている相手の課題 1 件。
type WaitingChallenge struct {
	ChallengeID string
	Title       string
	Status      Status
}

// WaitingReasons は課題が待っている理由。
//
//   - Prerequisites: 依存先のうち、まだ満たされていない（依存の評価で Met でない。開いた本番反映が残る完了済みの課題を含む）課題。
//     ID の数の昇順で、無ければ空スライス（nil でない）。
//
// 課題自身が完了・取り下げなら、空。
type WaitingReasons struct {
	Prerequisites []WaitingChallenge
}

func loadWaitingReasons(ctx context.Context, tx *sql.Tx, cid int64, status Status, eval *dependencyEvaluation) (WaitingReasons, error) {
	out := WaitingReasons{Prerequisites: []WaitingChallenge{}}
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
	return out, nil
}
