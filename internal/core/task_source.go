package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// このファイルは、委譲へ渡す取り込み元（差し込み {issue_number}・{issue_url}・{external_key} の値）を、実行が結び付く
// タスクから決める口と、タスクが対応する子による「起動しない理由」を持つ。規則は core に置き、CLI・server・invoker は持たない。

// challengeIssueNumber は、統合先を使う課題で、タスクの PR と昇格の PR へ参照を書く、課題の取り込み元の Issue の番号を返す
// （タスクの数やタスクが対応する子に依らない）。sb は課題の取り込み元、repo は PR を作るリポジトリの宣言。
// 取り込み元が無いときと、取り込み元の Issue が repo の Issue でないとき（PR の本文の `#N` は PR のリポジトリの番号を指す）は ""。
func challengeIssueNumber(sb *sourceBinding, repo *ConnectorRepo) string {
	if sb == nil || repo == nil {
		return ""
	}
	name, number, ok := parseExternalKey(sb.ExternalKey)
	if !ok || !strings.EqualFold(name, repo.Remote) {
		return ""
	}
	return strconv.Itoa(number)
}

// reflectTakesChallengeIssue は、反映の依頼の口の委譲先 c（無ければ nil）が取り込み元の番号を受けるか。
// 受けるのは form: flow だけ（form: cli の commands.reflect には番号の差し込みが無い）。
func reflectTakesChallengeIssue(c *Connector) bool {
	return c != nil && c.Form == ConnectorFormFlow
}

// resolveTaskSourceBinding は、タスク t の実行へ渡す取り込み元を決める。sb は課題の取り込み元（無ければ nil）、
// planTaskCount は t の計画の版のタスクの数、children は課題の取り込み元の子の記録。
//
//  1. t が対応する子（source_key）を持つ → その子の値。子の記録が無ければ nil（課題の取り込み元へ切り替えない）
//  2. 持たず、計画の版のタスクが 1 個 → 課題の取り込み元（見直し前と同じ）
//  3. 持たずタスクが 2 個以上 → 渡さない（nil）
func resolveTaskSourceBinding(sb *sourceBinding, t *Task, planTaskCount int, children []SourceChild) *sourceBinding {
	if t == nil {
		return sb
	}
	if t.SourceKey != "" {
		for _, c := range children {
			if c.ExternalKey == t.SourceKey {
				return &sourceBinding{SourceID: c.SourceID, ExternalKey: c.ExternalKey, URL: c.URL}
			}
		}
		return nil
	}
	if planTaskCount == 1 {
		return sb
	}
	return nil
}

// loadTaskSourceBinding は課題 cid の、タスク taskID（nil なら課題の取り込み元そのまま）の実行へ渡す取り込み元を読む。
// 差し込みを埋める所（指示文・スキルを直接呼ぶ形の起動と、契約の形の start・status・resume・cancel）は、すべてこの口を通る。
func loadTaskSourceBinding(ctx context.Context, tx *sql.Tx, cid int64, taskID *int64) (*sourceBinding, error) {
	sb, err := loadSourceBindingByChallengeID(ctx, tx, cid)
	if err != nil || taskID == nil {
		return sb, err
	}
	t, err := loadTask(ctx, tx, *taskID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return sb, nil
		}
		return nil, err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task WHERE challenge_id = ? AND plan_version = ?`, cid, t.PlanVersion).Scan(&n); err != nil {
		return nil, err
	}
	var children []SourceChild
	if t.SourceKey != "" {
		if children, err = loadSourceChildren(ctx, tx, cid); err != nil {
			return nil, err
		}
	}
	return resolveTaskSourceBinding(sb, t, n, children), nil
}

// taskUpstreamExclusion は、起動しようとするタスク t が対応する子による、起動しない理由を返す（無ければ nil）。
//   - source_key が指す子の記録が無い → 状態を問わず upstream_missing
//   - 未着手（pending）で、子の上流の状態が closed → upstream_closed、missing → upstream_missing
//   - 作業中のタスクの再開・やり直しには、子の上流の状態を掛けない
func taskUpstreamExclusion(ctx context.Context, tx *sql.Tx, challengeID string, cid int64, t *Task) (*NotStarted, error) {
	if t == nil || t.SourceKey == "" {
		return nil, nil
	}
	children, err := loadSourceChildren(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	var child *SourceChild
	for i := range children {
		if children[i].ExternalKey == t.SourceKey {
			child = &children[i]
		}
	}
	ns := func(r NotStartedReason, detail string) *NotStarted {
		return &NotStarted{ChallengeID: challengeID, Reason: r, Detail: detail}
	}
	if child == nil {
		return ns(NotStartedUpstreamMissing, fmt.Sprintf("タスク %s が対応する取り込み元の子 %s の記録が無い", t.ID, t.SourceKey)), nil
	}
	if t.Status != TaskStatusPending {
		return nil, nil
	}
	detail := strings.TrimSpace(child.URL)
	if detail == "" {
		detail = child.ExternalKey
	}
	switch upstreamState(child.UpstreamState) {
	case upstreamStateClosed:
		return ns(NotStartedUpstreamClosed, detail), nil
	case upstreamStateMissing:
		return ns(NotStartedUpstreamMissing, detail), nil
	}
	return nil, nil
}
