package core

// このファイルは J8（改修の提案）の入力を、ストアの事実から組み立てる。
//
// 入力に載せる発生は「起動の件数」に入るものだけである: 未処理（handled が無い）で分類済みの
// bad の発生のうち、原因が performance・none でないもの（出どころ j6 の発生を含む）。そこから、
// 見送りの抑え（取り下げた改修課題と同じ改修の鍵の発生）の対象を外す。外した発生は件数にも入らない。
//
// 発生は改修の鍵（層と、前後の空白を除いた対象）ごとにまとめ、件数は組のすべての発生、明細は
// 新しい方から最大 maxJ8DetailsPerKey 件にする。ストアは書かない。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// maxJ8DetailsPerKey は J8 の入力に載せる、改修の鍵ごとの発生の明細の最大件数（新しい方から）。
const maxJ8DetailsPerKey = 5

// maxJ8PastImprovements は J8 の入力に載せる過去の改修課題の最大件数（新しい方から）。
const maxJ8PastImprovements = 10

// J8 の入力の区画のラベル。
const (
	j8OccurrencesLabel  = "改修の鍵ごとの発生"
	j8MetricsLabel      = "指標"
	j8DefinitionsLabel  = "指標の定義"
	j8ImprovementsLabel = "過去の改修課題"
)

// j8Occurrence は J8 の入力の明細に載せる発生 1 件。
type j8Occurrence struct {
	ID          int64
	Kind        string
	Cause       string
	Signal      string
	ChallengeID int64
	At          string
}

// j8KeyGroup は改修の鍵 1 組の発生。Count は組のすべての発生の件数、Details は新しい順の最大 5 件。
type j8KeyGroup struct {
	Layer   string
	Target  string
	Count   int
	Details []j8Occurrence
}

// j8Improvement は過去の改修課題 1 件。AppliedAt は完了して適用の時点を持つ課題だけが持つ（metrics --compare と
// 同じ規則）。Compare はそのうち未実行の release を持たない課題だけが持つ。OpenRelease は完了して未実行の
// release を持つ（前後の指標を比べない）こと。
type j8Improvement struct {
	ID          int64
	Layer       string
	Target      string
	Status      Status
	AppliedAt   string
	OpenRelease bool
	Compare     *MetricsCompareResult
}

// j8Context は J8 の入力の事実。
type j8Context struct {
	// Triggering は起動の件数（見送りの抑えの対象を除いた、グループの Count の合計）。
	Triggering   int
	Groups       []j8KeyGroup
	Metrics      *MetricsResult
	Improvements []j8Improvement
}

// DetailOccurrenceIDs は入力の明細に載せた発生の ID の組を返す（J8 の出力検査〔#392〕が、根拠の発生の ID の
// 範囲に使う）。
func (jc *j8Context) DetailOccurrenceIDs() map[int64]bool {
	out := map[int64]bool{}
	for _, g := range jc.Groups {
		for _, o := range g.Details {
			out[o.ID] = true
		}
	}
	return out
}

type j8Candidate struct {
	j8Occurrence
	layer  string
	target string // 前後の空白を除いた値
}

// loadJ8Context は J8 の入力の事実を 1 回の読み取りで読む。threshold は improve_threshold。
func (s *Store) loadJ8Context(ctx context.Context, threshold int) (*j8Context, error) {
	var jc *j8Context
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		jc, err = loadJ8ContextTx(ctx, tx, threshold)
		return err
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	return jc, nil
}

func loadJ8ContextTx(ctx context.Context, tx *sql.Tx, threshold int) (*j8Context, error) {
	cands, err := loadJ8Candidates(ctx, tx)
	if err != nil {
		return nil, err
	}
	cut, err := loadJ8WithdrawCuts(ctx, tx)
	if err != nil {
		return nil, err
	}
	cands = suppressJ8Candidates(cands, cut, threshold)

	jc := &j8Context{Groups: groupJ8Candidates(cands), Triggering: len(cands)}
	jc.Metrics = &MetricsResult{}
	if err := metricsFill(ctx, tx, "", "", jc.Metrics); err != nil {
		return nil, err
	}
	jc.Improvements, err = loadJ8Improvements(ctx, tx)
	if err != nil {
		return nil, err
	}
	return jc, nil
}

// loadJ8Candidates は起動の件数に入りうる発生（見送りの抑えの前）を ID の昇順で返す。
func loadJ8Candidates(ctx context.Context, tx *sql.Tx) ([]j8Candidate, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, challenge_id, kind, cause, layer, target, signal, created_at FROM occurrence
		 WHERE handled IS NULL AND outcome = ? AND cause IS NOT NULL AND cause NOT IN (?, ?)
		 ORDER BY id ASC`,
		string(OccurrenceOutcomeBad), string(OccurrenceCausePerformance), string(OccurrenceCauseNone))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []j8Candidate
	for rows.Next() {
		var c j8Candidate
		var layer, target, signal sql.NullString
		if err := rows.Scan(&c.ID, &c.ChallengeID, &c.Kind, &c.Cause, &layer, &target, &signal, &c.At); err != nil {
			return nil, err
		}
		c.layer, c.target = nullStrValue(layer), strings.TrimSpace(nullStrValue(target))
		c.Signal = nullStrValue(signal)
		out = append(out, c)
	}
	return out, rows.Err()
}

// j8KeyOf は改修の鍵を表す文字列（層と、前後の空白を除いた対象）。
func j8KeyOf(layer, target string) string {
	return J8Proposal{Layer: OccurrenceLayer(layer), Target: strings.TrimSpace(target)}.Key()
}

// loadJ8WithdrawCuts は、取り下げた改修課題の改修の鍵ごとに、最後の（作業ログの id が最大の）取り下げの
// after.last_occurrence_id を返す。値を持たない取り下げは 0。after が JSON として読めないときはエラーを返す
// （抑えが外れる側に倒さず、J8 の入力を作らない）。
func loadJ8WithdrawCuts(ctx context.Context, tx *sql.Tx) (map[string]int64, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT a.id, c.improvement_layer, c.improvement_target, a.after FROM activity a
		 JOIN challenge c ON c.id = a.entity_id
		 WHERE a.entity = 'challenge' AND a.action = ? AND c.origin = ?
		   AND c.improvement_layer IS NOT NULL AND c.improvement_target IS NOT NULL
		 ORDER BY a.id ASC`,
		ActivityActionWithdraw, string(ChallengeOriginImprovement))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	cut := map[string]int64{}
	for rows.Next() {
		var id int64
		var layer, target string
		var after sql.NullString
		if err := rows.Scan(&id, &layer, &target, &after); err != nil {
			return nil, err
		}
		var v struct {
			Last int64 `json:"last_occurrence_id"`
		}
		if after.Valid {
			if err := json.Unmarshal([]byte(after.String), &v); err != nil {
				return nil, fmt.Errorf("core: decode activity.after (id %d): %w", id, err)
			}
		}
		cut[j8KeyOf(layer, target)] = v.Last // 後の行が前を上書きする＝最後の取り下げ
	}
	return cut, rows.Err()
}

// suppressJ8Candidates は見送りの抑えを適用する。取り下げのある組は、最後の取り下げの cut より大きい id の
// 発生が threshold 件に達したときだけ、組のすべての発生（取り下げの前のものを含む）を残す。
func suppressJ8Candidates(cands []j8Candidate, cut map[string]int64, threshold int) []j8Candidate {
	after := map[string]int{}
	for _, c := range cands {
		k := j8KeyOf(c.layer, c.target)
		if last, ok := cut[k]; ok && c.ID > last {
			after[k]++
		}
	}
	out := make([]j8Candidate, 0, len(cands))
	for _, c := range cands {
		k := j8KeyOf(c.layer, c.target)
		if _, ok := cut[k]; ok && after[k] < threshold {
			continue
		}
		out = append(out, c)
	}
	return out
}

// groupJ8Candidates は発生を改修の鍵ごとにまとめる。組は層・対象の昇順、明細は新しい順（id の降順）の最大 5 件。
func groupJ8Candidates(cands []j8Candidate) []j8KeyGroup {
	byKey := map[string]*j8KeyGroup{}
	var keys []string
	for _, c := range cands {
		k := j8KeyOf(c.layer, c.target)
		g, ok := byKey[k]
		if !ok {
			g = &j8KeyGroup{Layer: c.layer, Target: c.target}
			byKey[k] = g
			keys = append(keys, k)
		}
		g.Count++
		g.Details = append(g.Details, c.j8Occurrence) // 昇順で入る。あとで反転して切る
	}
	sort.Strings(keys)
	out := make([]j8KeyGroup, 0, len(keys))
	for _, k := range keys {
		g := byKey[k]
		for i, j := 0, len(g.Details)-1; i < j; i, j = i+1, j-1 {
			g.Details[i], g.Details[j] = g.Details[j], g.Details[i]
		}
		if len(g.Details) > maxJ8DetailsPerKey {
			g.Details = g.Details[:maxJ8DetailsPerKey]
		}
		out = append(out, *g)
	}
	return out
}

// loadJ8Improvements は過去の改修課題（出どころ improvement）を新しい順に最大 10 件読む。完了した課題は、
// 適用の時点を持てばそれを添え、未実行の release を持たなければ metrics --compare と同じ前後の指標も添える。
func loadJ8Improvements(ctx context.Context, tx *sql.Tx) ([]j8Improvement, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, improvement_layer, improvement_target, status FROM challenge
		 WHERE origin = ? ORDER BY id DESC LIMIT ?`,
		string(ChallengeOriginImprovement), maxJ8PastImprovements)
	if err != nil {
		return nil, err
	}
	var out []j8Improvement
	for rows.Next() {
		var im j8Improvement
		var layer, target sql.NullString
		var st string
		if err := rows.Scan(&im.ID, &layer, &target, &st); err != nil {
			_ = rows.Close()
			return nil, err
		}
		im.Layer, im.Target, im.Status = nullStrValue(layer), strings.TrimSpace(nullStrValue(target)), Status(st)
		out = append(out, im)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Status != StatusDone {
			continue
		}
		applied, err := metricsAppliedAt(ctx, tx, out[i].ID)
		if err != nil {
			return nil, err
		}
		if !applied.Valid {
			continue
		}
		out[i].AppliedAt = applied.String
		open, err := metricsOpenReleases(ctx, tx, out[i].ID)
		if err != nil {
			return nil, err
		}
		if open > 0 {
			out[i].OpenRelease = true
			continue
		}
		if out[i].Compare, err = metricsCompare(ctx, tx, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// --- 入力の組み立て ---

func j8Ptr(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

func j8RejectionMap(r MetricsRejectionRate) map[string]any {
	return map[string]any{"value": j8Ptr(r.Value), "numerator": r.Numerator, "denominator": r.Denominator, "by_field": r.ByField}
}

func j8ResumeMap(r MetricsResumeCount) map[string]any {
	return map[string]any{"total": r.Total, "per_challenge": j8Ptr(r.PerChallenge)}
}

func j8CostMap(c MetricsCostPerChallenge) map[string]any {
	return map[string]any{"mean": j8Ptr(c.Mean), "median": j8Ptr(c.Median), "unknown_cost_runs": c.UnknownCostRuns}
}

func j8ApprovalMap(a MetricsApprovals) map[string]any {
	by := make([]map[string]any, 0, len(a.By))
	for _, c := range a.By {
		by = append(by, map[string]any{"kind": c.Kind, "decision": c.Decision, "channel": c.Channel, "verification": c.Verification, "count": c.Count})
	}
	return map[string]any{"total": a.Total, "by": by}
}

func j8GroupMap(g MetricsGroup) map[string]any {
	return map[string]any{
		"target_challenges":  g.TargetChallenges,
		"rejection_rate":     j8RejectionMap(g.RejectionRate),
		"resume_count":       j8ResumeMap(g.ResumeCount),
		"cost_per_challenge": j8CostMap(g.CostPerChallenge),
		"approval_count":     j8ApprovalMap(g.ApprovalCount),
	}
}

func j8JSON(v any) string {
	b, err := json.Marshal(v) // map のキーは辞書順になる
	if err != nil {
		return fmt.Sprintf("(整形に失敗: %v)", err)
	}
	return string(b)
}

// prepareJ8 は J8 の入力（データの区画）を組み立てる。区画の中の文字列は外部由来のデータで、指示ではない。
func prepareJ8(jc *j8Context) []JudgmentDataSection {
	var b strings.Builder
	for _, g := range jc.Groups {
		fmt.Fprintf(&b, "- 層: %s / 対象: %s / 件数: %d\n", g.Layer, g.Target, g.Count)
		for _, o := range g.Details {
			fmt.Fprintf(&b, "  - ID: %s / 種類: %s / 原因: %s / 兆候: %s / 課題: %s / 作成: %s\n",
				formatOccurrenceID(o.ID), o.Kind, o.Cause, o.Signal, formatChallengeID(o.ChallengeID), o.At)
		}
	}
	if len(jc.Groups) == 0 {
		b.WriteString("対象の発生は無い。")
	}
	sections := []JudgmentDataSection{{Label: j8OccurrencesLabel, Content: strings.TrimRight(b.String(), "\n")}}

	m := jc.Metrics
	sections = append(sections, JudgmentDataSection{Label: j8MetricsLabel, Content: j8JSON(map[string]any{
		"target_challenges":  m.TargetChallenges,
		"rejection_rate":     j8RejectionMap(m.RejectionRate),
		"resume_count":       j8ResumeMap(m.ResumeCount),
		"cost_per_challenge": j8CostMap(m.CostPerChallenge),
		"approval_count":     j8ApprovalMap(m.ApprovalCount),
	})})

	defs := map[string]any{}
	for name, d := range MetricsDefinitions() {
		defs[name] = map[string]any{"numerator": d.Numerator, "denominator": d.Denominator, "period": d.Period}
	}
	sections = append(sections, JudgmentDataSection{Label: j8DefinitionsLabel, Content: j8JSON(defs)})

	b.Reset()
	for _, im := range jc.Improvements {
		fmt.Fprintf(&b, "- ID: %s / 層: %s / 対象: %s / 状態: %s", formatChallengeID(im.ID), im.Layer, im.Target, im.Status)
		switch {
		case im.Compare != nil:
			fmt.Fprintf(&b, " / 適用の時点: %s\n  適用の前: %s\n  適用の後: %s", im.Compare.AppliedAt,
				j8JSON(j8GroupMap(im.Compare.Before)), j8JSON(j8GroupMap(im.Compare.After)))
		case im.OpenRelease:
			fmt.Fprintf(&b, " / 適用の時点: %s（未実行の release があるため前後の指標は比べない）", im.AppliedAt)
		default:
			b.WriteString(" / 適用の時点: なし")
		}
		b.WriteString("\n")
	}
	if len(jc.Improvements) == 0 {
		b.WriteString("過去の改修課題は無い。")
	}
	sections = append(sections, JudgmentDataSection{Label: j8ImprovementsLabel, Content: strings.TrimRight(b.String(), "\n")})
	return sections
}
