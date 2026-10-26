package core

// このファイルは `flywheel metrics --compare <C-ID>`（docs/features/m4.5-learning-and-self-improvement.md
// §S4: flywheel metrics --compare）の集計を持つ。適用の時点の前後それぞれ最大 10 件の完了した課題を
// 組にし、`metrics` と同じ 4 つの指標を組ごとに数える。判断点も `claude` も起動せず、ストアを書かない。

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// MetricsCompareGroupSize は前後の組に入れる課題の最大件数。
const MetricsCompareGroupSize = 10

// MetricsGroup は比べる組 1 つ分の指標。TargetChallenges は組に入った課題の件数。
type MetricsGroup struct {
	TargetChallenges int
	RejectionRate    MetricsRejectionRate
	ResumeCount      MetricsResumeCount
	CostPerChallenge MetricsCostPerChallenge
	ApprovalCount    MetricsApprovals
}

// MetricsCompareResult は課題 1 件の適用の時点の前後の比較。AppliedAt はストアの保存形（RFC3339・UTC・ミリ秒）。
type MetricsCompareResult struct {
	ChallengeID string
	AppliedAt   string
	Before      MetricsGroup
	After       MetricsGroup
}

// MetricsCompare は challengeID の課題の適用の時点の前後を比べる。存在しない ID は ErrNotFound、
// 完了していない課題・未実行（pending・approved）の release を持つ課題は ErrValidation。ストアを書かない。
func (s *Store) MetricsCompare(ctx context.Context, challengeID string) (*MetricsCompareResult, error) {
	cid, ok := parseChallengeID(challengeID)
	if !ok {
		return nil, ErrNotFound
	}
	var res *MetricsCompareResult
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		res, err = metricsCompare(ctx, tx, cid)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return res, nil
}

// metricsCompare は課題 cid（内部 ID）の前後の組の指標を数える（内部関数。J8 の入力〔#391〕から呼ぶ予定）。
func metricsCompare(ctx context.Context, tx *sql.Tx, cid int64) (*MetricsCompareResult, error) {
	c, err := loadChallenge(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	if c.Status != StatusDone {
		return nil, fmt.Errorf("%w: %s は完了していない課題なので比べられない（状態 %s）", ErrValidation, c.ID, c.Status)
	}
	open, err := metricsOpenReleases(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	if open > 0 {
		return nil, fmt.Errorf("%w: %s は未実行（pending・approved）の release を持つので比べられない", ErrValidation, c.ID)
	}
	applied, err := metricsAppliedAt(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	if !applied.Valid {
		return nil, fmt.Errorf("%w: %s は適用の時点（release の実行・完了の承認）を持たない", ErrValidation, c.ID)
	}
	out := &MetricsCompareResult{ChallengeID: c.ID, AppliedAt: applied.String}
	before, err := metricsCompareTargets(ctx, tx, cid, "<", "DESC", applied.String)
	if err != nil {
		return nil, err
	}
	after, err := metricsCompareTargets(ctx, tx, cid, ">", "ASC", applied.String)
	if err != nil {
		return nil, err
	}
	if out.Before, err = metricsGroup(ctx, tx, before); err != nil {
		return nil, err
	}
	if out.After, err = metricsGroup(ctx, tx, after); err != nil {
		return nil, err
	}
	return out, nil
}

// metricsOpenReleases は課題 cid の未実行（pending・approved）の release の件数を返す。
func metricsOpenReleases(ctx context.Context, tx *sql.Tx, cid int64) (int, error) {
	var open int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM operation WHERE challenge_id = ? AND kind = ? AND state IN (?, ?)`,
		cid, string(OperationKindRelease), string(OperationStatePending), string(OperationStateApproved)).Scan(&open)
	return open, err
}

// metricsAppliedAt は課題 cid の適用の時点を返す: 実行済みの release の最も遅い executed_at、無ければ完了の承認の
// 最も遅い decided_at。どちらも無ければ Valid でない値。
func metricsAppliedAt(ctx context.Context, tx *sql.Tx, cid int64) (sql.NullString, error) {
	var applied sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT MAX(executed_at) FROM operation WHERE challenge_id = ? AND kind = ? AND state = ?`,
		cid, string(OperationKindRelease), string(OperationStateExecuted)).Scan(&applied); err != nil {
		return applied, err
	}
	if applied.Valid {
		return applied, nil
	}
	err := tx.QueryRowContext(ctx,
		`SELECT MAX(decided_at) FROM approval WHERE challenge_id = ? AND kind = ? AND decision = ?`,
		cid, string(ApprovalKindCompletion), string(ApprovalDecisionApproved)).Scan(&applied)
	return applied, err
}

// metricsCompareTargets は、完了の承認の日時（課題ごとの最も遅いもの）が applied と op（"<" か ">"）の関係にある課題
// （cid を除く）の内部 ID を、dir（DESC か ASC）の日時の順に最大 MetricsCompareGroupSize 件返す。
func metricsCompareTargets(ctx context.Context, tx *sql.Tx, cid int64, op, dir, applied string) ([]int64, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT challenge_id FROM approval WHERE kind = ? AND decision = ? AND challenge_id <> ?
		 GROUP BY challenge_id HAVING MAX(decided_at) `+op+` ?
		 ORDER BY MAX(decided_at) `+dir+`, challenge_id `+dir+` LIMIT ?`,
		string(ApprovalKindCompletion), string(ApprovalDecisionApproved), cid, applied, MetricsCompareGroupSize)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// metricsGroup は targets（課題の内部 ID）の組の 4 つの指標を数える。S3 の metrics と同じ数え方で、
// approval_count だけは期間でなく課題の ID の組で数える。
func metricsGroup(ctx context.Context, tx *sql.Tx, targets []int64) (MetricsGroup, error) {
	g := MetricsGroup{TargetChallenges: len(targets)}
	var err error
	if g.RejectionRate, err = metricsRejection(ctx, tx, targets); err != nil {
		return g, err
	}
	if g.ResumeCount, err = metricsResume(ctx, tx, targets); err != nil {
		return g, err
	}
	if g.CostPerChallenge, err = metricsCost(ctx, tx, targets); err != nil {
		return g, err
	}
	g.ApprovalCount, err = metricsApprovalsOf(ctx, tx, targets)
	return g, err
}

// metricsApprovalsOf は targets の課題の承認と差し戻しの行を、種類・決定・経路・本人確認の方式ごとに数える。
func metricsApprovalsOf(ctx context.Context, tx *sql.Tx, targets []int64) (MetricsApprovals, error) {
	out := MetricsApprovals{By: []MetricsApprovalCount{}}
	if len(targets) == 0 {
		return out, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(targets)), ", ")
	args := make([]any, 0, len(targets))
	for _, id := range targets {
		args = append(args, id)
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT kind, decision, channel, verification, COUNT(*) FROM approval WHERE challenge_id IN (`+marks+`)
		 GROUP BY kind, decision, channel, verification
		 ORDER BY kind, decision, channel, verification`, args...)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var c MetricsApprovalCount
		if err := rows.Scan(&c.Kind, &c.Decision, &c.Channel, &c.Verification, &c.Count); err != nil {
			return out, err
		}
		out.Total += c.Count
		out.By = append(out.By, c)
	}
	return out, rows.Err()
}
