package view

import "github.com/masanami/flywheel/internal/core"

// このファイルは `flywheel metrics --json` の成功時の形（docs/features/m4.5-learning-and-self-improvement.md
// §IF / API の `metrics` の行。M45H25）を置く。types.gen.ts の根には入れない。

// MetricsResponse は metrics の --json の最上位。
type MetricsResponse struct {
	Definitions      map[string]MetricsDefinition `json:"definitions"`
	Metrics          MetricsValues                `json:"metrics"`
	Occurrences      MetricsOccurrences           `json:"occurrences"`
	Period           MetricsPeriod                `json:"period"`
	TargetChallenges int                          `json:"target_challenges"`
}

// MetricsPeriod は期間の端（YYYY-MM-DD）。省略した端は null。
type MetricsPeriod struct {
	Since *string `json:"since"`
	Until *string `json:"until"`
}

// MetricsDefinition は指標の定義（いずれも文字列）。
type MetricsDefinition struct {
	Denominator string `json:"denominator"`
	Numerator   string `json:"numerator"`
	Period      string `json:"period"`
}

// MetricsValues は 4 つの指標。
type MetricsValues struct {
	ApprovalCount    MetricsApprovalCount    `json:"approval_count"`
	CostPerChallenge MetricsCostPerChallenge `json:"cost_per_challenge"`
	RejectionRate    MetricsRejectionRate    `json:"rejection_rate"`
	ResumeCount      MetricsResumeCount      `json:"resume_count"`
}

// MetricsRejectionRate は差し戻し率。対象が 0 件なら value は null。
type MetricsRejectionRate struct {
	ByField     map[string]int `json:"by_field"`
	Denominator int            `json:"denominator"`
	Numerator   int            `json:"numerator"`
	Value       *float64       `json:"value"`
}

// MetricsResumeCount は resume の回数。
type MetricsResumeCount struct {
	PerChallenge *float64 `json:"per_challenge"`
	Total        int      `json:"total"`
}

// MetricsCostPerChallenge は 1 課題あたりの費用（USD）。
type MetricsCostPerChallenge struct {
	Mean            *float64 `json:"mean"`
	Median          *float64 `json:"median"`
	UnknownCostRuns int      `json:"unknown_cost_runs"`
}

// MetricsApprovalCountRow は人の承認の件数の 1 区分。
type MetricsApprovalCountRow struct {
	Channel      string `json:"channel"`
	Count        int    `json:"count"`
	Decision     string `json:"decision"`
	Kind         string `json:"kind"`
	Verification string `json:"verification"`
}

// MetricsApprovalCount は人の承認の件数。
type MetricsApprovalCount struct {
	By    []MetricsApprovalCountRow `json:"by"`
	Total int                       `json:"total"`
}

// MetricsOccurrences は発生の件数。
type MetricsOccurrences struct {
	ByCause      map[string]int `json:"by_cause"`
	ByKind       map[string]int `json:"by_kind"`
	ByLayer      map[string]int `json:"by_layer"`
	Unclassified int            `json:"unclassified"`
	Unprocessed  int            `json:"unprocessed"`
}

// FromMetrics は core の集計結果を --json の形へ写す。
func FromMetrics(m *core.MetricsResult) MetricsResponse {
	defs := map[string]MetricsDefinition{}
	for name, d := range core.MetricsDefinitions() {
		defs[name] = MetricsDefinition{Denominator: d.Denominator, Numerator: d.Numerator, Period: d.Period}
	}
	return MetricsResponse{
		Definitions: defs,
		Metrics:     metricsValues(m.RejectionRate, m.ResumeCount, m.CostPerChallenge, m.ApprovalCount),
		Occurrences: MetricsOccurrences{
			ByCause: m.Occurrences.ByCause, ByKind: m.Occurrences.ByKind, ByLayer: m.Occurrences.ByLayer,
			Unclassified: m.Occurrences.Unclassified, Unprocessed: m.Occurrences.Unprocessed,
		},
		Period:           MetricsPeriod{Since: m.Since, Until: m.Until},
		TargetChallenges: m.TargetChallenges,
	}
}

// MetricsCompareResponse は `metrics --compare --json` の最上位（仕様の AC-398。M45P31）。
type MetricsCompareResponse struct {
	Compare MetricsCompare `json:"compare"`
}

// MetricsCompare は課題 1 件の適用の時点の前後の比較。
type MetricsCompare struct {
	After       MetricsGroup `json:"after"`
	AppliedAt   string       `json:"applied_at"`
	Before      MetricsGroup `json:"before"`
	ChallengeID string       `json:"challenge_id"`
}

// MetricsGroup は比べる組 1 つ分。`metrics --json` の `metrics` と `target_challenges` と同じ形。
type MetricsGroup struct {
	Metrics          MetricsValues `json:"metrics"`
	TargetChallenges int           `json:"target_challenges"`
}

func metricsValues(rr core.MetricsRejectionRate, rc core.MetricsResumeCount, cp core.MetricsCostPerChallenge, ac core.MetricsApprovals) MetricsValues {
	by := make([]MetricsApprovalCountRow, 0, len(ac.By))
	for _, r := range ac.By {
		by = append(by, MetricsApprovalCountRow{Channel: r.Channel, Count: r.Count, Decision: r.Decision, Kind: r.Kind, Verification: r.Verification})
	}
	return MetricsValues{
		ApprovalCount:    MetricsApprovalCount{By: by, Total: ac.Total},
		CostPerChallenge: MetricsCostPerChallenge{Mean: cp.Mean, Median: cp.Median, UnknownCostRuns: cp.UnknownCostRuns},
		RejectionRate:    MetricsRejectionRate{ByField: rr.ByField, Denominator: rr.Denominator, Numerator: rr.Numerator, Value: rr.Value},
		ResumeCount:      MetricsResumeCount{PerChallenge: rc.PerChallenge, Total: rc.Total},
	}
}

func fromMetricsGroup(g core.MetricsGroup) MetricsGroup {
	return MetricsGroup{
		Metrics:          metricsValues(g.RejectionRate, g.ResumeCount, g.CostPerChallenge, g.ApprovalCount),
		TargetChallenges: g.TargetChallenges,
	}
}

// FromMetricsCompare は core の比較の結果を --json の形へ写す。
func FromMetricsCompare(r *core.MetricsCompareResult) MetricsCompareResponse {
	return MetricsCompareResponse{Compare: MetricsCompare{
		After: fromMetricsGroup(r.After), AppliedAt: r.AppliedAt,
		Before: fromMetricsGroup(r.Before), ChallengeID: r.ChallengeID,
	}}
}
