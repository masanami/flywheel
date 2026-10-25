package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/view"
)

// runMetrics は `flywheel metrics [--since <日付>] [--until <日付>] [--json]` の実装
// （docs/features/m4.5-learning-and-self-improvement.md §効果の測定）。core.Store.Metrics を呼ぶだけで、
// 集計の規則は core が持つ。判断点も `claude` も起動せず、ストアを書かない。
// `--compare` は S4 で足すので、S3 では未知のフラグとして usage_error になる。
func runMetrics(a Args) (any, error) {
	var opt core.MetricsOptions
	for _, f := range []struct {
		name string
		dst  **time.Time
	}{{"since", &opt.Since}, {"until", &opt.Until}} {
		v, ok := a.Values[f.name]
		if !ok {
			continue
		}
		d, err := time.ParseInLocation(core.MetricsDateLayout, v, time.Local)
		if err != nil {
			return nil, NewError(CodeUsageError, fmt.Sprintf("--%s は YYYY-MM-DD の日付で指定してください: %q", f.name, v))
		}
		*f.dst = &d
	}
	m, err := a.Store.Metrics(context.Background(), opt)
	if err != nil {
		return nil, mapCoreErr(err)
	}
	return textOutput{json: view.FromMetrics(m), text: metricsText(m)}, nil
}

func metricsFloat(v *float64, format string) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf(format, *v)
}

func metricsCounts(m map[string]int) string {
	if len(m) == 0 {
		return "なし"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}

// metricsText は --json 無しの metrics の表示（形式の安定は保証しない）。
func metricsText(m *core.MetricsResult) string {
	bound := func(p *string) string {
		if p == nil {
			return "(制限なし)"
		}
		return *p
	}
	var b strings.Builder
	fmt.Fprintf(&b, "期間: %s 〜 %s\n", bound(m.Since), bound(m.Until))
	fmt.Fprintf(&b, "対象の課題: %d 件\n", m.TargetChallenges)
	r := m.RejectionRate
	fmt.Fprintf(&b, "差し戻し率: %s (%d/%d) 欄ごと: %s\n", metricsFloat(r.Value, "%.3f"), r.Numerator, r.Denominator, metricsCounts(r.ByField))
	fmt.Fprintf(&b, "resume の回数: %d 課題あたり %s\n", m.ResumeCount.Total, metricsFloat(m.ResumeCount.PerChallenge, "%.3f"))
	c := m.CostPerChallenge
	fmt.Fprintf(&b, "1 課題あたりの費用: 平均 %s USD 中央値 %s USD 費用不明の run %d 件\n",
		metricsFloat(c.Mean, "%.4f"), metricsFloat(c.Median, "%.4f"), c.UnknownCostRuns)
	fmt.Fprintf(&b, "人の承認の件数: %d 件\n", m.ApprovalCount.Total)
	for _, row := range m.ApprovalCount.By {
		fmt.Fprintf(&b, "  %s %s channel=%s verification=%s: %d\n", row.Kind, row.Decision, row.Channel, row.Verification, row.Count)
	}
	o := m.Occurrences
	fmt.Fprintf(&b, "発生の件数: 種類 %s\n", metricsCounts(o.ByKind))
	fmt.Fprintf(&b, "  原因 %s\n", metricsCounts(o.ByCause))
	fmt.Fprintf(&b, "  層 %s\n", metricsCounts(o.ByLayer))
	fmt.Fprintf(&b, "  未分類 %d 未処理 %d\n", o.Unclassified, o.Unprocessed)
	return b.String()
}
