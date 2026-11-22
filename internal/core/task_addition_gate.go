package core

// このファイルは、足す依頼（表 task_addition）が閉じていないことを、上げる依頼を始める条件・検証中へ進める条件・
// 「いま始められるもの」の数えが共有して読む規則を持つ（区切り S4c-1。docs/features/core-redesign-s4c.md）。
// 足せる課題かの判定（expandAddable）と、済みの expand_after のタスクで足す依頼がまだ無いもの（unaddedExpandTasks）は、
// 計画の段（judgment_j2_expand.go）と同じ関数を使い、規則を 2 か所に書かない。

import (
	"context"
	"database/sql"
)

// challengeHasOpenAddition は課題 cid に、閉じていない足す依頼（pending〔仕様待ち〕・awaiting_owner〔オーナー待ち〕）があるか。
// 課題が足せる状態かは見ない（足す依頼ができた後に見込みが不明になった課題の pending も、待つものとして数える）。
func challengeHasOpenAddition(ctx context.Context, tx *sql.Tx, cid int64) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_addition WHERE challenge_id = ? AND state IN (?, ?)`,
		cid, string(TaskAdditionPending), string(TaskAdditionAwaitingOwner)).Scan(&n)
	return n > 0, err
}

// challengeAwaitsAddition は課題 cid が、足す依頼を待つ（閉じていない足す依頼がある、または、足せる課題に、済みの expand_after の
// タスクで足す依頼がまだ無いものがある）ために、上げる依頼を始めず・検証中へ進まないかを返す。
// 足す依頼が none・applied・declined・superseded で閉じていれば待たない。
func challengeAwaitsAddition(ctx context.Context, tx *sql.Tx, cid int64, agent *AgentDeclaration) (bool, error) {
	open, err := challengeHasOpenAddition(ctx, tx, cid)
	if err != nil || open {
		return open, err
	}
	pairs, err := unaddedExpandTasks(ctx, tx, &cid)
	if err != nil || len(pairs) == 0 {
		return false, err
	}
	return expandAddable(ctx, tx, cid, agent)
}

// countExpandWork は、計画の段が「足す」の形で進められる課題の数（課題ごとに 1 と数える。判断点は課題ごとに 1 回呼ぶ）。
// 着手中の課題で、判断点を呼ぶ対象（selectJ2ExpandTargets。pending の足す依頼を持ち、足せて、タスクに結び付かない実行が無い）、
// または、足せる課題に、済みの expand_after のタスクで足す依頼がまだ無いもの（段が足す依頼を作ってから呼ぶ）。
// 同じ課題の別のタスクが着手中でも数える。awaiting_owner の足す依頼・保留中の課題・見込みが不明の課題は数えない。
func countExpandWork(ctx context.Context, tx *sql.Tx, agent *AgentDeclaration) (int, error) {
	targets, _, err := selectJ2ExpandTargets(ctx, tx, agent)
	if err != nil {
		return 0, err
	}
	counted := map[int64]bool{}
	for _, tg := range targets {
		counted[tg.Challenge] = true
	}
	pairs, err := unaddedExpandTasks(ctx, tx, nil)
	if err != nil {
		return 0, err
	}
	checked := map[int64]bool{}
	for _, p := range pairs {
		if checked[p.cid] || counted[p.cid] {
			continue
		}
		checked[p.cid] = true
		ch, err := loadChallenge(ctx, tx, p.cid)
		if err != nil {
			return 0, err
		}
		if ch.Status != StatusInProgress {
			continue // 保留中の課題は、足す依頼は作るが判断点を呼ばない
		}
		if ok, _, err := expandCallable(ctx, tx, p.cid, agent); err != nil {
			return 0, err
		} else if !ok {
			continue
		}
		counted[p.cid] = true
	}
	return len(counted), nil
}
