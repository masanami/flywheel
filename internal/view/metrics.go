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
	by := make([]MetricsApprovalCountRow, 0, len(m.ApprovalCount.By))
	for _, r := range m.ApprovalCount.By {
		by = append(by, MetricsApprovalCountRow{Channel: r.Channel, Count: r.Count, Decision: r.Decision, Kind: r.Kind, Verification: r.Verification})
	}
	return MetricsResponse{
		Definitions: defs,
		Metrics: MetricsValues{
			ApprovalCount:    MetricsApprovalCount{By: by, Total: m.ApprovalCount.Total},
			CostPerChallenge: MetricsCostPerChallenge{Mean: m.CostPerChallenge.Mean, Median: m.CostPerChallenge.Median, UnknownCostRuns: m.CostPerChallenge.UnknownCostRuns},
			RejectionRate: MetricsRejectionRate{
				ByField: m.RejectionRate.ByField, Denominator: m.RejectionRate.Denominator,
				Numerator: m.RejectionRate.Numerator, Value: m.RejectionRate.Value,
			},
			ResumeCount: MetricsResumeCount{PerChallenge: m.ResumeCount.PerChallenge, Total: m.ResumeCount.Total},
		},
		Occurrences: MetricsOccurrences{
			ByCause: m.Occurrences.ByCause, ByKind: m.Occurrences.ByKind, ByLayer: m.Occurrences.ByLayer,
			Unclassified: m.Occurrences.Unclassified, Unprocessed: m.Occurrences.Unprocessed,
		},
		Period:           MetricsPeriod{Since: m.Since, Until: m.Until},
		TargetChallenges: m.TargetChallenges,
	}
}
