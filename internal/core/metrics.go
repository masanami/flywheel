package core

// このファイルは `flywheel metrics`（docs/features/m4.5-learning-and-self-improvement.md
// §効果の測定・§S3: flywheel metrics）の集計を持つ。判断点も `claude` も起動せず、ストアを
// 読むだけで（書かない）数える。規則の正本は仕様の §決定 M45H23・M45P23。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// MetricsDateLayout は `--since`・`--until` の日付の書式（ローカルの時刻の日付）。
const MetricsDateLayout = "2006-01-02"

// MetricsOptions は Metrics の期間。Since・Until はローカルの時刻の日付（その日の 0 時）で、
// 両端の日を含む。nil の端は制限なし。
type MetricsOptions struct {
	Since *time.Time
	Until *time.Time
}

// MetricsRejectionRate は差し戻し率。Value は対象が 0 件なら nil。ByField はずれた欄
// （RejectedField の閉集合の 4 値すべて）ごとの件数。
type MetricsRejectionRate struct {
	Value       *float64
	Numerator   int
	Denominator int
	ByField     map[string]int
}

// MetricsResumeCount は resume の回数。PerChallenge は対象が 0 件なら nil。
type MetricsResumeCount struct {
	Total        int
	PerChallenge *float64
}

// MetricsCostPerChallenge は 1 課題あたりの費用（USD）。Mean・Median は対象が 0 件なら nil。
type MetricsCostPerChallenge struct {
	Mean            *float64
	Median          *float64
	UnknownCostRuns int
}

// MetricsApprovalCount は人の承認の件数の 1 つの区分。
type MetricsApprovalCount struct {
	Kind         string
	Decision     string
	Channel      string
	Verification string
	Count        int
}

// MetricsApprovals は人の承認の件数。
type MetricsApprovals struct {
	Total int
	By    []MetricsApprovalCount
}

// MetricsOccurrences は発生の件数。ByCause・ByLayer は分類済みの発生だけを数える。
type MetricsOccurrences struct {
	ByKind       map[string]int
	ByCause      map[string]int
	ByLayer      map[string]int
	Unclassified int
	Unprocessed  int
}

// MetricsResult は Metrics の結果。Since・Until は期間の端の日付（YYYY-MM-DD）で、制限なしは nil。
type MetricsResult struct {
	Since            *string
	Until            *string
	TargetChallenges int
	RejectionRate    MetricsRejectionRate
	ResumeCount      MetricsResumeCount
	CostPerChallenge MetricsCostPerChallenge
	ApprovalCount    MetricsApprovals
	Occurrences      MetricsOccurrences
}

// MetricsMetricNames は指標の名前（J8 の metric の閉集合）。
var MetricsMetricNames = []string{"approval_count", "cost_per_challenge", "rejection_rate", "resume_count"}

// MetricsDefinition は指標の定義（分子・分母・期間の取り方）。
type MetricsDefinition struct {
	Numerator   string
	Denominator string
	Period      string
}

// MetricsDefinitions は指標の名前をキーにした定義を返す（M45P23）。
func MetricsDefinitions() map[string]MetricsDefinition {
	return map[string]MetricsDefinition{
		"rejection_rate": {
			Numerator:   "対象の課題のうち、完了の差し戻しを 1 回以上受けた課題の数（計画の差し戻しは入れない）",
			Denominator: "対象の課題の数",
			Period:      "対象の課題は、期間（ローカルの時刻の日付。両端を含む）に完了の承認を受けた課題",
		},
		"resume_count": {
			Numerator:   "対象の課題の委譲の run のうち、再開（resumed_from_run_id を持つ）の件数",
			Denominator: "対象の課題の数（課題あたりの平均）",
			Period:      "対象の課題は、期間（ローカルの時刻の日付。両端を含む）に完了の承認を受けた課題",
		},
		"cost_per_challenge": {
			Numerator:   "対象の課題ごとの、課題に紐づくすべての run の cost_usd の合計（NULL の run は入れず unknown_cost_runs に件数を添える）",
			Denominator: "対象の課題の数（平均と中央値）",
			Period:      "対象の課題は、期間（ローカルの時刻の日付。両端を含む）に完了の承認を受けた課題",
		},
		"approval_count": {
			Numerator:   "期間に記録した承認と差し戻しの行の数（種類・決定・経路・本人確認の方式ごと）",
			Denominator: "なし（対象の課題に限らず、期間の全行を数える）",
			Period:      "承認の決定の時刻が期間（ローカルの時刻の日付。両端を含む）にある行",
		},
	}
}

// Metrics は期間に完了の承認を受けた課題を対象に、指標を数える。ストアを書かない。
// 同じ入力に対して同じ結果を返す（並びは固定）。
func (s *Store) Metrics(ctx context.Context, opt MetricsOptions) (*MetricsResult, error) {
	res := &MetricsResult{}
	var lo, hi string // [lo, hi) の UTC の保存形。空は制限なし。
	if opt.Since != nil {
		d := *opt.Since
		lo = formatTimestamp(time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, d.Location()))
		v := d.Format(MetricsDateLayout)
		res.Since = &v
	}
	if opt.Until != nil {
		d := *opt.Until
		hi = formatTimestamp(time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, d.Location()))
		v := d.Format(MetricsDateLayout)
		res.Until = &v
	}
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		targets, err := metricsTargets(ctx, tx, lo, hi)
		if err != nil {
			return err
		}
		res.TargetChallenges = len(targets)
		if res.RejectionRate, err = metricsRejection(ctx, tx, targets); err != nil {
			return err
		}
		if res.ResumeCount, err = metricsResume(ctx, tx, targets); err != nil {
			return err
		}
		if res.CostPerChallenge, err = metricsCost(ctx, tx, targets); err != nil {
			return err
		}
		if res.ApprovalCount, err = metricsApprovals(ctx, tx, lo, hi); err != nil {
			return err
		}
		res.Occurrences, err = metricsOccurrences(ctx, tx, lo, hi)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return res, nil
}

// metricsRange は列 col が [lo, hi) にある条件と引数を返す（空の端は条件を足さない）。
func metricsRange(col, lo, hi string) (string, []any) {
	var conds []string
	var args []any
	if lo != "" {
		conds = append(conds, col+" >= ?")
		args = append(args, lo)
	}
	if hi != "" {
		conds = append(conds, col+" < ?")
		args = append(args, hi)
	}
	if len(conds) == 0 {
		return "1 = 1", nil
	}
	return strings.Join(conds, " AND "), args
}

// metricsTargets は期間に完了の承認を受けた課題の内部 ID（昇順）。
func metricsTargets(ctx context.Context, tx *sql.Tx, lo, hi string) ([]int64, error) {
	cond, args := metricsRange("decided_at", lo, hi)
	rows, err := tx.QueryContext(ctx,
		`SELECT DISTINCT challenge_id FROM approval
		 WHERE kind = ? AND decision = ? AND `+cond+` ORDER BY challenge_id`,
		append([]any{string(ApprovalKindCompletion), string(ApprovalDecisionApproved)}, args...)...)
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

func metricsTargetSet(targets []int64) map[int64]bool {
	m := make(map[int64]bool, len(targets))
	for _, id := range targets {
		m[id] = true
	}
	return m
}

func metricsRatio(n, d int) *float64 {
	if d == 0 {
		return nil
	}
	v := float64(n) / float64(d)
	return &v
}

func metricsRejection(ctx context.Context, tx *sql.Tx, targets []int64) (MetricsRejectionRate, error) {
	out := MetricsRejectionRate{Denominator: len(targets), ByField: map[string]int{}}
	for _, f := range rejectedFieldValues {
		out.ByField[string(f)] = 0
	}
	set := metricsTargetSet(targets)
	rows, err := tx.QueryContext(ctx,
		`SELECT challenge_id, rejected_fields FROM approval WHERE kind = ? AND decision = ? ORDER BY id`,
		string(ApprovalKindCompletion), string(ApprovalDecisionRejected))
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	rejected := map[int64]bool{}
	for rows.Next() {
		var cid int64
		var fields sql.NullString
		if err := rows.Scan(&cid, &fields); err != nil {
			return out, err
		}
		if !set[cid] {
			continue
		}
		rejected[cid] = true
		if fields.Valid && fields.String != "" {
			var fs []string
			if err := json.Unmarshal([]byte(fields.String), &fs); err != nil {
				return out, fmt.Errorf("core: decode approval.rejected_fields: %w", err)
			}
			for _, f := range fs {
				out.ByField[f]++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.Numerator = len(rejected)
	out.Value = metricsRatio(out.Numerator, out.Denominator)
	return out, nil
}

func metricsResume(ctx context.Context, tx *sql.Tx, targets []int64) (MetricsResumeCount, error) {
	out := MetricsResumeCount{}
	set := metricsTargetSet(targets)
	rows, err := tx.QueryContext(ctx,
		`SELECT challenge_id FROM run WHERE kind = ? AND resumed_from_run_id IS NOT NULL AND challenge_id IS NOT NULL`,
		string(RunKindDelegate))
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid int64
		if err := rows.Scan(&cid); err != nil {
			return out, err
		}
		if set[cid] {
			out.Total++
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.PerChallenge = metricsRatio(out.Total, len(targets))
	return out, nil
}

func metricsCost(ctx context.Context, tx *sql.Tx, targets []int64) (MetricsCostPerChallenge, error) {
	out := MetricsCostPerChallenge{}
	set := metricsTargetSet(targets)
	rows, err := tx.QueryContext(ctx, `SELECT challenge_id, cost_usd FROM run WHERE challenge_id IS NOT NULL`)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	sums := make(map[int64]int64, len(targets))
	for _, id := range targets {
		sums[id] = 0
	}
	for rows.Next() {
		var cid int64
		var cost sql.NullInt64
		if err := rows.Scan(&cid, &cost); err != nil {
			return out, err
		}
		if !set[cid] {
			continue
		}
		if !cost.Valid {
			out.UnknownCostRuns++
			continue
		}
		sums[cid] += cost.Int64
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if len(targets) == 0 {
		return out, nil
	}
	totals := make([]int64, 0, len(sums))
	var all int64
	for _, v := range sums {
		totals = append(totals, v)
		all += v
	}
	sort.Slice(totals, func(i, j int) bool { return totals[i] < totals[j] })
	mean := microsToUSD(all) / float64(len(totals))
	var median float64
	if n := len(totals); n%2 == 1 {
		median = microsToUSD(totals[n/2])
	} else {
		median = (microsToUSD(totals[n/2-1]) + microsToUSD(totals[n/2])) / 2
	}
	out.Mean, out.Median = &mean, &median
	return out, nil
}

func metricsApprovals(ctx context.Context, tx *sql.Tx, lo, hi string) (MetricsApprovals, error) {
	out := MetricsApprovals{By: []MetricsApprovalCount{}}
	cond, args := metricsRange("decided_at", lo, hi)
	rows, err := tx.QueryContext(ctx,
		`SELECT kind, decision, channel, verification, COUNT(*) FROM approval WHERE `+cond+`
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

func metricsOccurrences(ctx context.Context, tx *sql.Tx, lo, hi string) (MetricsOccurrences, error) {
	out := MetricsOccurrences{ByKind: map[string]int{}, ByCause: map[string]int{}, ByLayer: map[string]int{}}
	cond, args := metricsRange("created_at", lo, hi)
	rows, err := tx.QueryContext(ctx, `SELECT kind, cause, layer, handled FROM occurrence WHERE `+cond, args...)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var kind string
		var cause, layer, handled sql.NullString
		if err := rows.Scan(&kind, &cause, &layer, &handled); err != nil {
			return out, err
		}
		out.ByKind[kind]++
		if !cause.Valid {
			out.Unclassified++
			continue
		}
		out.ByCause[cause.String]++
		if layer.Valid {
			out.ByLayer[layer.String]++
		}
		if !handled.Valid {
			out.Unprocessed++
		}
	}
	return out, rows.Err()
}
