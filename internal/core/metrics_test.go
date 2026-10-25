package core

import (
	"context"
	"database/sql"
	"math"
	"reflect"
	"testing"
	"time"
)

// AC-288〜AC-301: Metrics は実ストアの行から指標を数える。

func mexec(t *testing.T, s *Store, q string, args ...any) {
	t.Helper()
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(q, args...)
		return err
	}); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func mChallenge(t *testing.T, s *Store, title string) int64 {
	t.Helper()
	var id int64
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO challenge (title, status, reporter, created_at, updated_at)
			VALUES (?, 'done', 't', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, title)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mApproval(t *testing.T, s *Store, cid int64, kind, decision, channel, verification, at string, fields any) {
	t.Helper()
	mexec(t, s, `INSERT INTO approval (challenge_id, kind, decision, target_version, actor, channel, verification, decided_at, rejected_fields)
		VALUES (?, ?, ?, 1, 'a', ?, ?, ?, ?)`, cid, kind, decision, channel, verification, at, fields)
}

func mRun(t *testing.T, s *Store, cid any, kind string, resumedFrom any, cost any) int64 {
	t.Helper()
	var id int64
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO run (kind, challenge_id, resumed_from_run_id, pid, host, heartbeat_at, started_at,
			ended_at, result, max_budget_usd, budget_bucket, cost_usd) VALUES (?, ?, ?, 1, 'h', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', '2026-01-01T00:01:00.000Z', 'completed', 1, 'b', ?)`,
			kind, cid, resumedFrom, cost)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mOcc(t *testing.T, s *Store, cid int64, kind, at string, cause, layer, handled any) {
	t.Helper()
	var actID int64
	mexec(t, s, `INSERT INTO activity (at, actor, channel, verification, entity, entity_id, action) VALUES (?, 'a', 'cli', 'none', 'challenge', ?, 'x')`, at, cid)
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT MAX(id) FROM activity`).Scan(&actID)
	}); err != nil {
		t.Fatal(err)
	}
	mexec(t, s, `INSERT INTO occurrence (challenge_id, kind, activity_id, outcome, source, cause, layer, handled, created_at)
		VALUES (?, ?, ?, 'bad', 'core', ?, ?, ?, ?)`, cid, kind, actID, cause, layer, handled, at)
}

func day(t *testing.T, s string) *time.Time {
	t.Helper()
	d, err := time.ParseInLocation(MetricsDateLayout, s, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	return &d
}

// local は ローカルの時刻の日付 d の hh:mm の UTC 保存形。
func local(d string, hour, minute int) string {
	t, _ := time.ParseInLocation(MetricsDateLayout, d, time.Local)
	return formatTimestamp(time.Date(t.Year(), t.Month(), t.Day(), hour, minute, 0, 0, time.Local))
}

func TestMetrics_EmptyStore(t *testing.T) {
	s := newStoreForTest(t)
	m, err := s.Metrics(context.Background(), MetricsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if m.TargetChallenges != 0 || m.RejectionRate.Value != nil || m.ResumeCount.PerChallenge != nil ||
		m.CostPerChallenge.Mean != nil || m.CostPerChallenge.Median != nil {
		t.Errorf("empty = %+v", m)
	}
	if m.Since != nil || m.Until != nil {
		t.Errorf("period = %v %v, want nil nil", m.Since, m.Until)
	}
	if got := len(m.RejectionRate.ByField); got != 4 {
		t.Errorf("by_field has %d keys, want the 4 closed-set fields", got)
	}
}

// useJST はテストの間 time.Local を UTC+9 にする（UTC の実行環境でも、ローカルの日付と UTC の日付を区別する）。
func useJST(t *testing.T) {
	t.Helper()
	old := time.Local
	time.Local = time.FixedZone("JST", 9*3600)
	t.Cleanup(func() { time.Local = old })
}

func TestMetrics_PeriodTargetsAreInclusiveLocalDays(t *testing.T) {
	useJST(t)
	s := newStoreForTest(t)
	a := mChallenge(t, s, "a")
	b := mChallenge(t, s, "b")
	c := mChallenge(t, s, "c")
	d := mChallenge(t, s, "d")
	e := mChallenge(t, s, "e") // 完了の承認なし
	_ = e
	mApproval(t, s, a, "completion", "approved", "cli", "tty_confirm", local("2026-03-01", 0, 0), nil)   // since の日の 0:00
	mApproval(t, s, b, "completion", "approved", "cli", "tty_confirm", local("2026-03-03", 23, 59), nil) // until の日の終わり
	mApproval(t, s, c, "completion", "approved", "cli", "tty_confirm", local("2026-02-28", 23, 59), nil) // 前の日
	mApproval(t, s, d, "completion", "approved", "cli", "tty_confirm", local("2026-03-04", 0, 0), nil)   // 後の日
	mApproval(t, s, e, "plan", "approved", "cli", "tty_confirm", local("2026-03-02", 12, 0), nil)        // 計画だけ

	m, err := s.Metrics(context.Background(), MetricsOptions{Since: day(t, "2026-03-01"), Until: day(t, "2026-03-03")})
	if err != nil {
		t.Fatal(err)
	}
	if m.TargetChallenges != 2 {
		t.Errorf("targets = %d, want 2 (a and b)", m.TargetChallenges)
	}
	if m.Since == nil || *m.Since != "2026-03-01" || m.Until == nil || *m.Until != "2026-03-03" {
		t.Errorf("period = %v %v", m.Since, m.Until)
	}
	// 片側だけ。
	m, _ = s.Metrics(context.Background(), MetricsOptions{Since: day(t, "2026-03-03")})
	if m.TargetChallenges != 2 { // b と d
		t.Errorf("since only: targets = %d, want 2", m.TargetChallenges)
	}
	m, _ = s.Metrics(context.Background(), MetricsOptions{Until: day(t, "2026-02-28")})
	if m.TargetChallenges != 1 { // c
		t.Errorf("until only: targets = %d, want 1", m.TargetChallenges)
	}
	m, _ = s.Metrics(context.Background(), MetricsOptions{})
	if m.TargetChallenges != 4 {
		t.Errorf("unbounded: targets = %d, want 4", m.TargetChallenges)
	}
}

func TestMetrics_RejectionRate(t *testing.T) {
	s := newStoreForTest(t)
	at := local("2026-03-01", 12, 0)
	a := mChallenge(t, s, "a") // 完了の差し戻しを 2 回（欄 issues+expected、issues）
	b := mChallenge(t, s, "b") // 計画の差し戻しだけ
	c := mChallenge(t, s, "c") // 差し戻しなし
	x := mChallenge(t, s, "x") // 対象外（完了の承認なし）だが完了の差し戻しあり
	for _, id := range []int64{a, b, c} {
		mApproval(t, s, id, "completion", "approved", "cli", "tty_confirm", at, nil)
	}
	mApproval(t, s, a, "completion", "rejected", "cli", "tty_confirm", at, `["issues","expected"]`)
	mApproval(t, s, a, "completion", "rejected", "cli", "tty_confirm", at, `["issues"]`)
	mApproval(t, s, b, "plan", "rejected", "cli", "tty_confirm", at, `["figure"]`)
	mApproval(t, s, x, "completion", "rejected", "cli", "tty_confirm", at, `["non_goals"]`)

	m, err := s.Metrics(context.Background(), MetricsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	r := m.RejectionRate
	if r.Numerator != 1 || r.Denominator != 3 || r.Value == nil || math.Abs(*r.Value-1.0/3) > 1e-9 {
		t.Errorf("rate = %+v", r)
	}
	want := map[string]int{"issues": 2, "expected": 1, "non_goals": 0, "figure": 0}
	if !reflect.DeepEqual(r.ByField, want) {
		t.Errorf("by_field = %v, want %v (plan rejections and non-target challenges excluded)", r.ByField, want)
	}
}

func TestMetrics_ResumeAndCost(t *testing.T) {
	s := newStoreForTest(t)
	at := local("2026-03-01", 12, 0)
	a := mChallenge(t, s, "a")
	b := mChallenge(t, s, "b")
	x := mChallenge(t, s, "x") // 対象外
	for _, id := range []int64{a, b} {
		mApproval(t, s, id, "completion", "approved", "cli", "tty_confirm", at, nil)
	}
	r1 := mRun(t, s, a, "delegate", nil, int64(1_000_000))
	mRun(t, s, a, "delegate", r1, int64(2_500_000)) // 再開
	mRun(t, s, a, "judgment", r1, int64(500_000))   // 判断の run は再開に数えない
	mRun(t, s, a, "judgment", nil, nil)             // 費用不明
	mRun(t, s, b, "delegate", nil, int64(1_000_000))
	mRun(t, s, x, "delegate", r1, int64(9_000_000))    // 対象外の課題
	mRun(t, s, nil, "judgment", nil, int64(7_000_000)) // 課題に紐づかない（衝突の予測）
	mRun(t, s, nil, "judgment", nil, nil)

	m, err := s.Metrics(context.Background(), MetricsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if m.ResumeCount.Total != 1 || m.ResumeCount.PerChallenge == nil || *m.ResumeCount.PerChallenge != 0.5 {
		t.Errorf("resume = %+v", m.ResumeCount)
	}
	c := m.CostPerChallenge
	// a = 4.0、b = 1.0 → 平均 2.5、中央値 2.5。
	if c.Mean == nil || *c.Mean != 2.5 || c.Median == nil || *c.Median != 2.5 || c.UnknownCostRuns != 1 {
		t.Errorf("cost = mean %v median %v unknown %d", c.Mean, c.Median, c.UnknownCostRuns)
	}
}

func TestMetrics_CostMedianOdd(t *testing.T) {
	s := newStoreForTest(t)
	at := local("2026-03-01", 12, 0)
	for i, cost := range []int64{1_000_000, 2_000_000, 10_000_000} {
		id := mChallenge(t, s, "c")
		mApproval(t, s, id, "completion", "approved", "cli", "tty_confirm", at, nil)
		mRun(t, s, id, "delegate", nil, cost)
		_ = i
	}
	id := mChallenge(t, s, "no runs") // run の無い対象は合計 0 として平均・中央値に入る
	_ = id
	m, _ := s.Metrics(context.Background(), MetricsOptions{})
	if m.CostPerChallenge.Median == nil || *m.CostPerChallenge.Median != 2.0 {
		t.Errorf("median = %v, want 2", m.CostPerChallenge.Median)
	}
	if *m.CostPerChallenge.Mean < 4.33 || *m.CostPerChallenge.Mean > 4.34 {
		t.Errorf("mean = %v", *m.CostPerChallenge.Mean)
	}
}

func TestMetrics_ApprovalCountCoversAllRowsInPeriod(t *testing.T) {
	s := newStoreForTest(t)
	a := mChallenge(t, s, "a")
	b := mChallenge(t, s, "b") // 完了の承認を受けない課題の承認も数える
	mApproval(t, s, a, "completion", "approved", "cli", "tty_confirm", local("2026-03-02", 9, 0), nil)
	mApproval(t, s, a, "plan", "approved", "cli", "tty_confirm", local("2026-03-02", 8, 0), nil)
	mApproval(t, s, b, "plan", "approved", "cli", "tty_confirm", local("2026-03-02", 8, 5), nil)
	mApproval(t, s, b, "plan", "rejected", "cli", "relayed", local("2026-03-02", 8, 6), nil)
	mApproval(t, s, b, "budget", "approved", "cli", "tty_confirm", local("2026-03-05", 8, 6), nil) // 期間外

	m, err := s.Metrics(context.Background(), MetricsOptions{Since: day(t, "2026-03-02"), Until: day(t, "2026-03-02")})
	if err != nil {
		t.Fatal(err)
	}
	if m.ApprovalCount.Total != 4 {
		t.Fatalf("total = %d, want 4", m.ApprovalCount.Total)
	}
	want := []MetricsApprovalCount{
		{"completion", "approved", "cli", "tty_confirm", 1},
		{"plan", "approved", "cli", "tty_confirm", 2},
		{"plan", "rejected", "cli", "relayed", 1},
	}
	if !reflect.DeepEqual(m.ApprovalCount.By, want) {
		t.Errorf("by = %+v, want %+v", m.ApprovalCount.By, want)
	}
}

func TestMetrics_Occurrences(t *testing.T) {
	s := newStoreForTest(t)
	a := mChallenge(t, s, "a")
	in := local("2026-03-02", 9, 0)
	out := local("2026-04-02", 9, 0)
	mOcc(t, s, a, "completion_rejected", in, nil, nil, nil)                              // 未分類
	mOcc(t, s, a, "delegation_failed", in, "context", "knowledge", nil)                  // 未処理
	mOcc(t, s, a, "delegation_failed", in, "performance", "prompt", nil)                 // 未処理
	mOcc(t, s, a, "completion_approved", in, "none", "none", "2026-03-02T10:00:00.000Z") // 処理済み
	mOcc(t, s, a, "delegation_failed", out, "context", "knowledge", nil)                 // 期間外

	m, err := s.Metrics(context.Background(), MetricsOptions{Since: day(t, "2026-03-01"), Until: day(t, "2026-03-31")})
	if err != nil {
		t.Fatal(err)
	}
	o := m.Occurrences
	if !reflect.DeepEqual(o.ByKind, map[string]int{"completion_rejected": 1, "delegation_failed": 2, "completion_approved": 1}) {
		t.Errorf("by_kind = %v", o.ByKind)
	}
	if !reflect.DeepEqual(o.ByCause, map[string]int{"context": 1, "performance": 1, "none": 1}) {
		t.Errorf("by_cause = %v", o.ByCause)
	}
	if !reflect.DeepEqual(o.ByLayer, map[string]int{"knowledge": 1, "prompt": 1, "none": 1}) {
		t.Errorf("by_layer = %v", o.ByLayer)
	}
	if o.Unclassified != 1 || o.Unprocessed != 2 {
		t.Errorf("unclassified %d unprocessed %d, want 1 and 2", o.Unclassified, o.Unprocessed)
	}
}

func TestMetrics_DoesNotWriteAndIsDeterministic(t *testing.T) {
	s := newStoreForTest(t)
	a := mChallenge(t, s, "a")
	mApproval(t, s, a, "completion", "approved", "cli", "tty_confirm", local("2026-03-02", 9, 0), nil)
	mRun(t, s, a, "delegate", nil, int64(1_000_000))
	tables := func() string {
		var out string
		for _, tbl := range []string{"challenge", "approval", "run", "occurrence", "activity", "cycle", "hold"} {
			var n int
			var mx sql.NullInt64
			if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
				return tx.QueryRow(`SELECT COUNT(*), MAX(rowid) FROM `+tbl).Scan(&n, &mx)
			}); err != nil {
				t.Fatal(err)
			}
			out += tbl + ":" + string(rune('0'+n)) + ";"
		}
		return out
	}
	before := tables()
	m1, _ := s.Metrics(context.Background(), MetricsOptions{})
	m2, _ := s.Metrics(context.Background(), MetricsOptions{})
	if !reflect.DeepEqual(m1, m2) {
		t.Errorf("two runs differ:\n%+v\n%+v", m1, m2)
	}
	if after := tables(); after != before {
		t.Errorf("tables changed: %s -> %s", before, after)
	}
}

func TestMetricsDefinitions_CoverEveryMetricWithStrings(t *testing.T) {
	d := MetricsDefinitions()
	if len(d) != len(MetricsMetricNames) {
		t.Fatalf("definitions has %d entries, want %d", len(d), len(MetricsMetricNames))
	}
	for _, n := range MetricsMetricNames {
		x, ok := d[n]
		if !ok || x.Numerator == "" || x.Denominator == "" || x.Period == "" {
			t.Errorf("definition %q = %+v", n, x)
		}
	}
}

// AC-298: 人の承認の件数の軸（種類・決定・経路・本人確認の方式）は、経路の違う行・release・budget の行を
// 別々の組として数え、期間外の行は数えない（どれかの軸を落とすと件数の組が変わって検出できる）。
func TestMetrics_ApprovalCountSeparatesKindDecisionChannelAndVerification(t *testing.T) {
	s := newStoreForTest(t)
	a := mChallenge(t, s, "a")
	b := mChallenge(t, s, "b")
	mApproval(t, s, a, "plan", "approved", "cli", "tty_confirm", local("2026-03-02", 8, 0), nil)
	mApproval(t, s, b, "plan", "approved", "ui", "none", local("2026-03-02", 8, 1), nil)
	mApproval(t, s, b, "plan", "approved", "ui", "none", local("2026-03-02", 8, 2), nil)
	mApproval(t, s, a, "release", "approved", "cli", "relayed", local("2026-03-02", 9, 0), nil)
	mApproval(t, s, a, "release", "approved", "cli", "tty_confirm", local("2026-03-02", 9, 1), nil)
	mApproval(t, s, b, "budget", "approved", "ui", "none", local("2026-03-02", 10, 0), nil)
	mApproval(t, s, b, "budget", "rejected", "ui", "none", local("2026-03-02", 10, 1), nil)
	mApproval(t, s, b, "release", "approved", "ui", "none", local("2026-04-02", 10, 1), nil) // 期間外

	m, err := s.Metrics(context.Background(), MetricsOptions{Since: day(t, "2026-03-02"), Until: day(t, "2026-03-02")})
	if err != nil {
		t.Fatal(err)
	}
	if m.ApprovalCount.Total != 7 {
		t.Fatalf("total = %d, want 7", m.ApprovalCount.Total)
	}
	want := []MetricsApprovalCount{
		{"budget", "approved", "ui", "none", 1},
		{"budget", "rejected", "ui", "none", 1},
		{"plan", "approved", "cli", "tty_confirm", 1},
		{"plan", "approved", "ui", "none", 2},
		{"release", "approved", "cli", "relayed", 1},
		{"release", "approved", "cli", "tty_confirm", 1},
	}
	if !reflect.DeepEqual(m.ApprovalCount.By, want) {
		t.Errorf("by = %+v, want %+v", m.ApprovalCount.By, want)
	}
}
