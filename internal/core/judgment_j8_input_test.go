package core

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// M4.5 S4 AC-322〜324・AC-335〜339・AC-362〜365: J8 の入力は実ストアの発生から、起動の件数に入るものだけを
// 改修の鍵ごとにまとめて組み立てる。

type j8Row struct {
	outcome string
	source  string
	cause   any
	layer   any
	target  any
	handled any
}

func j8MaxID(t *testing.T, s *Store, table string) int64 {
	t.Helper()
	var id int64
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM ` + table).Scan(&id)
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

// j8Occ は課題 cid の発生を 1 行足し、その id を返す。既定は bad・core・原因 context・層 prompt・対象 T。
func j8Occ(t *testing.T, s *Store, cid int64, r j8Row) int64 {
	t.Helper()
	if r.outcome == "" {
		r.outcome = "bad"
	}
	if r.source == "" {
		r.source = "core"
	}
	if r.cause == nil {
		r.cause = "context"
	}
	if r.layer == nil {
		r.layer = "prompt"
	}
	if r.target == nil {
		r.target = "T"
	}
	mexec(t, s, `INSERT INTO activity (at, actor, channel, verification, entity, entity_id, action) VALUES ('2026-03-01T00:00:00.000Z', 'a', 'cli', 'none', 'challenge', ?, 'x')`, cid)
	act := j8MaxID(t, s, "activity")
	mexec(t, s, `INSERT INTO occurrence (challenge_id, kind, activity_id, outcome, source, cause, layer, target, signal, handled, created_at)
		VALUES (?, 'plan_rejected', ?, ?, ?, ?, ?, ?, 'sig', ?, '2026-03-01T00:00:00.000Z')`,
		cid, act, r.outcome, r.source, r.cause, r.layer, r.target, r.handled)
	return j8MaxID(t, s, "occurrence")
}

// j8Withdrawn は改修の鍵を持つ取り下げ済みの改修課題を作り、取り下げの作業ログ（last_occurrence_id 付き）を足す。
func j8Withdrawn(t *testing.T, s *Store, layer, target string, last int64) int64 {
	t.Helper()
	cid := j8InsertImprovement(t, s, layer, target, "withdrawn")
	mexec(t, s, `INSERT INTO activity (at, actor, channel, verification, entity, entity_id, action, after)
		VALUES ('2026-03-02T00:00:00.000Z', 'a', 'cli', 'tty_confirm', 'challenge', ?, 'withdraw', ?)`,
		cid, fmt.Sprintf(`{"status":"withdrawn","last_occurrence_id":%d}`, last))
	return cid
}

func j8InsertImprovement(t *testing.T, s *Store, layer, target, status string) int64 {
	t.Helper()
	mexec(t, s, `INSERT INTO challenge (title, status, reporter, created_at, updated_at, origin, improvement_layer, improvement_target)
		VALUES ('imp', ?, 't', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', 'improvement', ?, ?)`, status, layer, target)
	return j8MaxID(t, s, "challenge")
}

func j8Load(t *testing.T, s *Store, threshold int) *j8Context {
	t.Helper()
	jc, err := s.loadJ8Context(context.Background(), threshold)
	if err != nil {
		t.Fatal(err)
	}
	return jc
}

func j8Section(t *testing.T, jc *j8Context, label string) string {
	t.Helper()
	for _, sec := range prepareJ8(jc) {
		if sec.Label == label {
			return sec.Content
		}
	}
	t.Fatalf("section %q not found", label)
	return ""
}

func TestJ8Input_TriggeringCountOnlyCountsUnhandledClassifiedBadNonPerformance(t *testing.T) {
	s := newStoreForTest(t)
	c := mChallenge(t, s, "c")
	want := map[int64]bool{
		j8Occ(t, s, c, j8Row{}): true, // 入る
		j8Occ(t, s, c, j8Row{cause: "permission", layer: "knowledge"}): true, // 入る
		j8Occ(t, s, c, j8Row{source: "j6"}):                            true, // 入る（出どころ j6）
	}
	j8Occ(t, s, c, j8Row{cause: "performance"})
	j8Occ(t, s, c, j8Row{cause: "none", layer: "none"})
	j8Occ(t, s, c, j8Row{outcome: "good"})
	j8Occ(t, s, c, j8Row{handled: "wait"})
	j8Occ(t, s, c, j8Row{handled: "C-9"})
	j8Occ(t, s, c, j8Row{handled: "not_applicable"})
	un := j8Occ(t, s, c, j8Row{})
	mexec(t, s, `UPDATE occurrence SET cause = NULL, layer = NULL WHERE id = ?`, un) // 未分類
	jc := j8Load(t, s, 10)
	if jc.Triggering != 3 || !reflect.DeepEqual(jc.DetailOccurrenceIDs(), want) {
		t.Errorf("Triggering = %d ids = %v, want 3 %v", jc.Triggering, jc.DetailOccurrenceIDs(), want)
	}
}

func TestJ8Input_GroupsByKeyWithCountAndFiveNewestDetails(t *testing.T) {
	s := newStoreForTest(t)
	c := mChallenge(t, s, "c")
	var ids []int64
	for i := 0; i < 6; i++ {
		target := "T"
		if i%2 == 1 {
			target = "  T \n" // 前後の空白は同じ鍵
		}
		ids = append(ids, j8Occ(t, s, c, j8Row{target: target}))
	}
	other := j8Occ(t, s, c, j8Row{layer: "knowledge", target: "K"})

	jc := j8Load(t, s, 10)
	if len(jc.Groups) != 2 {
		t.Fatalf("groups = %+v", jc.Groups)
	}
	k, p := jc.Groups[0], jc.Groups[1] // 層の昇順: knowledge, prompt
	if k.Layer != "knowledge" || k.Count != 1 || len(k.Details) != 1 || k.Details[0].ID != other {
		t.Errorf("knowledge group = %+v", k)
	}
	if p.Layer != "prompt" || p.Target != "T" || p.Count != 6 || len(p.Details) != 5 {
		t.Fatalf("prompt group = %+v", p)
	}
	var got []int64
	for _, o := range p.Details {
		got = append(got, o.ID)
	}
	want := []int64{ids[5], ids[4], ids[3], ids[2], ids[1]}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("detail ids = %v, want newest-first %v", got, want)
	}
	d := p.Details[0]
	if d.Kind != "plan_rejected" || d.Cause != "context" || d.Signal != "sig" || d.ChallengeID != c || d.At == "" {
		t.Errorf("detail = %+v", d)
	}
	text := j8Section(t, jc, j8OccurrencesLabel)
	if !strings.Contains(text, "層: prompt / 対象: T / 件数: 6") || !strings.Contains(text, formatOccurrenceID(ids[5])) ||
		strings.Contains(text, formatOccurrenceID(ids[0])) {
		t.Errorf("section = %s", text)
	}
	set := jc.DetailOccurrenceIDs()
	if len(set) != 6 || !set[other] || set[ids[0]] || !set[ids[1]] {
		t.Errorf("DetailOccurrenceIDs = %v", set)
	}
}

func TestJ8Input_WithdrawnKeyIsSuppressedUntilThresholdOccurrencesAfterTheWithdrawal(t *testing.T) {
	s := newStoreForTest(t)
	c := mChallenge(t, s, "c")
	before1 := j8Occ(t, s, c, j8Row{})
	before2 := j8Occ(t, s, c, j8Row{})
	other := j8Occ(t, s, c, j8Row{layer: "knowledge", target: "K"})
	j8Withdrawn(t, s, "prompt", "T", before2)

	// 取り下げの後の発生が 0 件: 組は入力にも件数にも入らない。
	jc := j8Load(t, s, 2)
	if jc.Triggering != 1 || len(jc.Groups) != 1 || jc.Groups[0].Layer != "knowledge" {
		t.Fatalf("suppressed: Triggering=%d groups=%+v", jc.Triggering, jc.Groups)
	}
	// 1 件（しきい値 2 未満）でもまだ外す。他の鍵・処理済みは数えない。
	a1 := j8Occ(t, s, c, j8Row{})
	j8Occ(t, s, c, j8Row{handled: "wait"})
	j8Occ(t, s, c, j8Row{cause: "performance"})
	if jc = j8Load(t, s, 2); jc.Triggering != 1 {
		t.Fatalf("one after: Triggering = %d, want 1", jc.Triggering)
	}
	// しきい値に達したら、取り下げの前の発生も含めて入る。
	a2 := j8Occ(t, s, c, j8Row{target: " T "})
	jc = j8Load(t, s, 2)
	if jc.Triggering != 5 {
		t.Fatalf("reached: Triggering = %d, want 5", jc.Triggering)
	}
	set := jc.DetailOccurrenceIDs()
	for _, id := range []int64{before1, before2, a1, a2, other} {
		if !set[id] {
			t.Errorf("O-%d missing from details %v", id, set)
		}
	}
}

func TestJ8Input_LastWithdrawalOfTheKeyDecidesAndNoOccurrenceMeansZero(t *testing.T) {
	s := newStoreForTest(t)
	c := mChallenge(t, s, "c")
	first := j8Occ(t, s, c, j8Row{})
	j8Withdrawn(t, s, "prompt", "T", 0) // 発生が無い時点の取り下げ（0）
	second := j8Occ(t, s, c, j8Row{})
	j8Withdrawn(t, s, "prompt", " T", second) // 最後の取り下げ（鍵は空白を除いて一致）
	third := j8Occ(t, s, c, j8Row{})

	if jc := j8Load(t, s, 2); jc.Triggering != 0 {
		t.Errorf("last withdrawal cut=%d: only O-%d is newer, Triggering = %d, want 0", second, third, jc.Triggering)
	}
	j8Occ(t, s, c, j8Row{})
	jc := j8Load(t, s, 2)
	if jc.Triggering != 4 || !jc.DetailOccurrenceIDs()[first] {
		t.Errorf("Triggering = %d (first included: %v), want 4", jc.Triggering, jc.DetailOccurrenceIDs()[first])
	}

	// 取り下げの作業ログの値が 0 なら、その鍵のすべての発生が「取り下げの後」になる。
	s2 := newStoreForTest(t)
	c2 := mChallenge(t, s2, "c")
	j8Withdrawn(t, s2, "prompt", "T", 0)
	j8Occ(t, s2, c2, j8Row{})
	if jc := j8Load(t, s2, 1); jc.Triggering != 1 {
		t.Errorf("zero cut: Triggering = %d, want 1", jc.Triggering)
	}
}

func TestJ8Input_NonWithdrawnImprovementDoesNotSuppress(t *testing.T) {
	s := newStoreForTest(t)
	c := mChallenge(t, s, "c")
	j8Occ(t, s, c, j8Row{})
	j8InsertImprovement(t, s, "prompt", "T", "done")
	j8InsertImprovement(t, s, "prompt", "T", "in_progress")
	if jc := j8Load(t, s, 10); jc.Triggering != 1 {
		t.Errorf("Triggering = %d, want 1", jc.Triggering)
	}
}

func TestJ8Input_MetricsAndDefinitionsAreIncluded(t *testing.T) {
	s := newStoreForTest(t)
	mDone(t, s, "done", 1)
	jc := j8Load(t, s, 10)
	res, err := s.Metrics(context.Background(), MetricsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wantMetrics := j8JSON(map[string]any{
		"target_challenges": res.TargetChallenges, "rejection_rate": j8RejectionMap(res.RejectionRate),
		"resume_count": j8ResumeMap(res.ResumeCount), "cost_per_challenge": j8CostMap(res.CostPerChallenge),
		"approval_count": j8ApprovalMap(res.ApprovalCount),
	})
	m := j8Section(t, jc, j8MetricsLabel)
	if m != wantMetrics || !strings.Contains(m, `"denominator":1`) || !strings.Contains(m, `"total":1`) {
		t.Errorf("metrics section = %s, want %s", m, wantMetrics)
	}
	d := j8Section(t, jc, j8DefinitionsLabel)
	for name, def := range MetricsDefinitions() {
		if !strings.Contains(d, `"`+name+`"`) || !strings.Contains(d, j8JSON(def.Period)) ||
			!strings.Contains(d, j8JSON(def.Numerator)) || !strings.Contains(d, j8JSON(def.Denominator)) {
			t.Errorf("definitions section lacks %s: %s", name, d)
		}
	}
	if jc.Metrics.TargetChallenges != 1 {
		t.Errorf("metrics target = %d, want 1 (no period)", jc.Metrics.TargetChallenges)
	}
	if got := j8Section(t, jc, j8OccurrencesLabel); got != "対象の発生は無い。" {
		t.Errorf("empty occurrences = %q", got)
	}
}

func TestJ8Input_PastImprovementsNewestTenWithCompareOnlyWhenApplied(t *testing.T) {
	s := newStoreForTest(t)
	var ids []int64
	for i := 0; i < 11; i++ {
		ids = append(ids, j8InsertImprovement(t, s, "prompt", fmt.Sprintf("T%d", i), "in_progress"))
	}
	applied := ids[10]
	mexec(t, s, `UPDATE challenge SET status = 'done' WHERE id = ?`, applied)
	mApproval(t, s, applied, "completion", "approved", "cli", "tty_confirm", mAt(5), nil)
	noPoint := ids[9]
	mexec(t, s, `UPDATE challenge SET status = 'done' WHERE id = ?`, noPoint) // 完了の承認も release も無い
	mDone(t, s, "before", 2)
	mDone(t, s, "after", 8)

	jc := j8Load(t, s, 10)
	if len(jc.Improvements) != 10 || jc.Improvements[0].ID != ids[10] || jc.Improvements[9].ID != ids[1] {
		t.Fatalf("improvements = %+v", jc.Improvements)
	}
	if c := jc.Improvements[0].Compare; c == nil || c.AppliedAt != mAt(5) || c.Before.TargetChallenges != 1 || c.After.TargetChallenges != 1 {
		t.Errorf("compare of applied = %+v", c)
	}
	if jc.Improvements[1].Compare != nil {
		t.Errorf("done without an application point must not carry a comparison")
	}
	if jc.Improvements[2].Compare != nil {
		t.Errorf("not done must not carry a comparison")
	}
	// metrics --compare と同じ値。
	want, err := s.MetricsCompare(context.Background(), formatChallengeID(applied))
	if err != nil || !reflect.DeepEqual(want, jc.Improvements[0].Compare) {
		t.Errorf("compare differs from MetricsCompare: %v / %+v", err, want)
	}
	text := j8Section(t, jc, j8ImprovementsLabel)
	if !strings.Contains(text, formatChallengeID(ids[10])+" / 層: prompt / 対象: T10 / 状態: done / 適用の時点: "+mAt(5)) ||
		!strings.Contains(text, "適用の前:") || !strings.Contains(text, "適用の後:") ||
		strings.Contains(text, formatChallengeID(ids[0])+" ") {
		t.Errorf("section = %s", text)
	}
}

func TestJ8Input_DoesNotWriteAndIsDeterministic(t *testing.T) {
	s := newStoreForTest(t)
	c := mChallenge(t, s, "c")
	j8Occ(t, s, c, j8Row{})
	j8InsertImprovement(t, s, "prompt", "T", "in_progress")
	before := j8MaxID(t, s, "activity")
	a := prepareJ8(j8Load(t, s, 10))
	b := prepareJ8(j8Load(t, s, 10))
	if !reflect.DeepEqual(a, b) {
		t.Error("input is not deterministic")
	}
	if j8MaxID(t, s, "activity") != before {
		t.Error("loading wrote to the store")
	}
}
