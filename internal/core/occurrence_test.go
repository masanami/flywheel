package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// AC-218 ほか: 発生の閉集合・内部ヘルパ・課題の origin（マイグレーション 0011）。

func TestParseOccurrenceKind_ClosedSetOf13(t *testing.T) {
	want := []string{
		"plan_rejected", "completion_rejected", "completion_approved", "verification_not_met",
		"verification_uncertain", "delegation_failed", "judgment_failed", "failure_limit_hold",
		"budget_exhausted", "delegation_stopped", "unapproved_merge", "release_withdrawn", "j6_observed",
	}
	for _, w := range want {
		if k, ok := ParseOccurrenceKind(w); !ok || string(k) != w {
			t.Errorf("ParseOccurrenceKind(%q) = %q, %v", w, k, ok)
		}
	}
	if len(want) != 13 {
		t.Fatalf("want list has %d entries", len(want))
	}
	for _, bad := range []string{"", "Plan_Rejected", "questions", "blocked", "other"} {
		if _, ok := ParseOccurrenceKind(bad); ok {
			t.Errorf("ParseOccurrenceKind(%q) accepted", bad)
		}
	}
}

func TestOccurrenceClosedSets(t *testing.T) {
	for _, v := range []string{"core", "j6"} {
		if _, ok := ParseOccurrenceSource(v); !ok {
			t.Errorf("source %q rejected", v)
		}
	}
	for _, v := range []string{"good", "bad"} {
		if _, ok := ParseOccurrenceOutcome(v); !ok {
			t.Errorf("outcome %q rejected", v)
		}
	}
	for _, v := range []string{"context", "permission", "performance", "none"} {
		if _, ok := ParseOccurrenceCause(v); !ok {
			t.Errorf("cause %q rejected", v)
		}
	}
	for _, v := range []string{"prompt", "connector_skill", "entry_skill", "knowledge", "none"} {
		if _, ok := ParseOccurrenceLayer(v); !ok {
			t.Errorf("layer %q rejected", v)
		}
	}
	for _, v := range []string{"ingest", "manual", "followup"} {
		if _, ok := ParseChallengeOrigin(v); !ok {
			t.Errorf("origin %q rejected", v)
		}
	}
	if _, ok := ParseOccurrenceSource("claude"); ok {
		t.Error("source claude accepted")
	}
	if _, ok := ParseOccurrenceCause("bug"); ok {
		t.Error("cause bug accepted")
	}
	if _, ok := ParseOccurrenceLayer("code"); ok {
		t.Error("layer code accepted")
	}
	if _, ok := ParseChallengeOrigin("improvement"); !ok {
		t.Error("origin improvement rejected (AC-408)")
	}
	if ActivityActionWithdraw != "withdraw" || ActivityActionImprovement != "improvement" {
		t.Error("withdraw/improvement activity actions missing (AC-410)")
	}
	if ActivityActionLearn != "learn" || ActivityActionFollowup != "followup" {
		t.Error("learn/followup activity actions missing (AC-311)")
	}
}

type occurrenceDBRow struct {
	challengeID           int64
	cycleID, causeRunID   sql.NullInt64
	kind, outcome, source string
	activityID            sql.NullInt64
	cause, layer, target  sql.NullString
	signal, handled       sql.NullString
	classifiedRunID       sql.NullInt64
	createdAt             string
}

func readOccurrences(t *testing.T, s *Store) []occurrenceDBRow {
	t.Helper()
	var out []occurrenceDBRow
	err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT challenge_id, cycle_id, cause_run_id, kind, outcome, source, activity_id,
			cause, layer, target, signal, handled, classified_run_id, created_at FROM occurrence ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var r occurrenceDBRow
			if err := rows.Scan(&r.challengeID, &r.cycleID, &r.causeRunID, &r.kind, &r.outcome, &r.source, &r.activityID,
				&r.cause, &r.layer, &r.target, &r.signal, &r.handled, &r.classifiedRunID, &r.createdAt); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("read occurrences: %v", err)
	}
	return out
}

func countActivityRows(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM activity`).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// 出来事（作業ログ）を書いて、同じトランザクションで発生を 1 行作る。
func writeEventWithOccurrence(ctx context.Context, s *Store, cid int64, cycleID *int64, outcome OccurrenceOutcome) error {
	return s.mutate(ctx, ChannelCLI, func(tx *sql.Tx, rec *activityRecorder) error {
		if err := rec.record("challenge", cid, "create", nil, map[string]any{"x": 1}); err != nil {
			return err
		}
		var actID int64
		if err := tx.QueryRow(`SELECT MAX(id) FROM activity`).Scan(&actID); err != nil {
			return err
		}
		return s.insertOccurrenceRow(tx, occurrenceInput{
			ChallengeID: cid, CycleID: cycleID, Kind: OccurrenceKindPlanRejected,
			Ref: OccurrenceRef{ActivityID: &actID}, Outcome: outcome, At: rec.at,
		})
	})
}

func TestInsertOccurrenceRow_WritesCoreRowWithNullClassification(t *testing.T) {
	s := newStoreForTest(t)
	cid := mustCreateChallengeInternalID(t, s, "t")
	before := countActivityRows(t, s)
	if err := writeEventWithOccurrence(context.Background(), s, cid, nil, OccurrenceOutcomeBad); err != nil {
		t.Fatal(err)
	}
	rows := readOccurrences(t, s)
	if len(rows) != 1 {
		t.Fatalf("occurrences = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.challengeID != cid || r.kind != "plan_rejected" || r.outcome != "bad" || r.source != "core" {
		t.Errorf("row = %+v", r)
	}
	if r.cycleID.Valid || r.causeRunID.Valid {
		t.Errorf("cycle/cause run must be NULL when absent: %+v", r)
	}
	if r.cause.Valid || r.layer.Valid || r.target.Valid || r.signal.Valid || r.classifiedRunID.Valid || r.handled.Valid {
		t.Errorf("classification columns must be NULL (AC-218): %+v", r)
	}
	if !r.activityID.Valid || r.createdAt == "" {
		t.Errorf("reference/created_at missing: %+v", r)
	}
	if countActivityRows(t, s) != before+1 {
		t.Error("event activity not written")
	}
}

func TestInsertOccurrenceRow_CarriesCycleIDAndOutcomeAsGiven(t *testing.T) {
	s := newStoreForTest(t)
	cid := mustCreateChallengeInternalID(t, s, "t")
	var cycleID int64
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO cycle (trigger, budget_usd, started_at) VALUES ('manual', 0, '2026-10-24T00:00:00.000Z')`)
		if err != nil {
			return err
		}
		cycleID, err = res.LastInsertId()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeEventWithOccurrence(context.Background(), s, cid, &cycleID, OccurrenceOutcomeGood); err != nil {
		t.Fatal(err)
	}
	rows := readOccurrences(t, s)
	if len(rows) != 1 || !rows[0].cycleID.Valid || rows[0].cycleID.Int64 != cycleID || rows[0].outcome != "good" {
		t.Fatalf("rows = %+v", rows)
	}
}

// 発生の挿入が失敗すれば、同じトランザクションの出来事も残らない（失敗注入のフック）。
func TestInsertOccurrenceRow_FailureRollsBackTheEvent(t *testing.T) {
	s := newStoreForTest(t)
	cid := mustCreateChallengeInternalID(t, s, "t")
	before := countActivityRows(t, s)
	boom := errors.New("injected occurrence failure")
	s.insertOccurrence = func(*sql.Tx, occurrenceRow) error { return boom }
	err := writeEventWithOccurrence(context.Background(), s, cid, nil, OccurrenceOutcomeBad)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want injected failure", err)
	}
	if got := countActivityRows(t, s); got != before {
		t.Errorf("activities = %d, want %d (event must roll back)", got, before)
	}
	if n := len(readOccurrences(t, s)); n != 0 {
		t.Errorf("occurrences = %d, want 0", n)
	}
}

// 出来事の書き込みが失敗すれば、発生も残らない。
func TestInsertOccurrenceRow_EventFailureLeavesNoOccurrence(t *testing.T) {
	s := newStoreForTest(t)
	cid := mustCreateChallengeInternalID(t, s, "t")
	boom := errors.New("event failed")
	err := s.mutate(context.Background(), ChannelCLI, func(tx *sql.Tx, rec *activityRecorder) error {
		if err := rec.record("challenge", cid, "create", nil, map[string]any{"x": 1}); err != nil {
			return err
		}
		var actID int64
		if err := tx.QueryRow(`SELECT MAX(id) FROM activity`).Scan(&actID); err != nil {
			return err
		}
		if err := s.insertOccurrenceRow(tx, occurrenceInput{
			ChallengeID: cid, Kind: OccurrenceKindPlanRejected, Ref: OccurrenceRef{ActivityID: &actID},
			Outcome: OccurrenceOutcomeBad, At: rec.at,
		}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if n := len(readOccurrences(t, s)); n != 0 {
		t.Errorf("occurrences = %d, want 0", n)
	}
}

func TestInsertOccurrenceRow_RejectsInvalidInput(t *testing.T) {
	s := newStoreForTest(t)
	cid := mustCreateChallengeInternalID(t, s, "t")
	one := int64(1)
	two := int64(2)
	cases := map[string]occurrenceInput{
		"j6_observed is not a core kind": {ChallengeID: cid, Kind: OccurrenceKindJ6Observed, Ref: OccurrenceRef{ActivityID: &one}, Outcome: OccurrenceOutcomeBad},
		"unknown kind":                   {ChallengeID: cid, Kind: "nope", Ref: OccurrenceRef{ActivityID: &one}, Outcome: OccurrenceOutcomeBad},
		"unknown outcome":                {ChallengeID: cid, Kind: OccurrenceKindPlanRejected, Ref: OccurrenceRef{ActivityID: &one}, Outcome: "meh"},
		"no reference":                   {ChallengeID: cid, Kind: OccurrenceKindPlanRejected, Outcome: OccurrenceOutcomeBad},
		"two references":                 {ChallengeID: cid, Kind: OccurrenceKindPlanRejected, Ref: OccurrenceRef{ActivityID: &one, HoldID: &two}, Outcome: OccurrenceOutcomeBad},
	}
	for name, in := range cases {
		in.At = time.Now()
		err := s.db.Write(context.Background(), func(tx *sql.Tx) error { return s.insertOccurrenceRow(tx, in) })
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if n := len(readOccurrences(t, s)); n != 0 {
		t.Errorf("occurrences = %d, want 0", n)
	}
}

func challengeOriginOf(t *testing.T, s *Store, id int64) (origin string, followupOf sql.NullString) {
	t.Helper()
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT origin, followup_of FROM challenge WHERE id = ?`, id).Scan(&origin, &followupOf)
	}); err != nil {
		t.Fatal(err)
	}
	return origin, followupOf
}

// AC-286: create で作った課題の origin は manual、followup_of は NULL。
func TestCreateChallenge_OriginIsManual(t *testing.T) {
	s := newStoreForTest(t)
	cid := mustCreateChallengeInternalID(t, s, "t")
	origin, followupOf := challengeOriginOf(t, s, cid)
	if origin != "manual" || followupOf.Valid {
		t.Errorf("origin = %q followup_of = %v, want manual/NULL", origin, followupOf)
	}
}

// AC-286: 取り込みで作った課題の origin は ingest。
func TestIngest_CreatedChallengeOriginIsIngest(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	src := excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})
	up := &fakeUpstream{issues: map[string][]UpstreamIssue{
		"o/r": {issueFixture("o/r#1", "https://github.com/o/r/issues/1", "t", "b", "carol", nil, nil)},
	}}
	res := runIngestForTest(t, s, []SourceEntry{src}, up)
	item := findIngestItem(res, "o/r#1")
	if item == nil {
		t.Fatal("item = nil")
	}
	id, ok := parseChallengeID(item.ChallengeID)
	if !ok {
		t.Fatalf("bad challenge id %q", item.ChallengeID)
	}
	origin, followupOf := challengeOriginOf(t, s, id)
	if origin != "ingest" || followupOf.Valid {
		t.Errorf("origin = %q followup_of = %v, want ingest/NULL", origin, followupOf)
	}
}

// insertChallengeRow は followup_of を受けられる（値を入れるのは #362）。
func TestInsertChallengeRow_AcceptsFollowupOf(t *testing.T) {
	s := newStoreForTest(t)
	of := "C-7"
	var id int64
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		id, _, err = insertChallengeRow(context.Background(), tx, time.Now(), "t", "", "", nil, "r", ChallengeOriginFollowup, &of, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	origin, followupOf := challengeOriginOf(t, s, id)
	if origin != "followup" || !followupOf.Valid || followupOf.String != "C-7" {
		t.Errorf("origin = %q followup_of = %v", origin, followupOf)
	}
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, _, err := insertChallengeRow(context.Background(), tx, time.Now(), "t", "", "", nil, "r", "bogus", nil, nil)
		return err
	})
	if err == nil {
		t.Error("invalid origin accepted")
	}
}

// coretest.FailOccurrenceInserts は DB 側で発生の挿入を失敗させ、出来事を巻き戻す
// （CLI のテストなど別プロセスから使う口）。
func TestCoretestFailOccurrenceInserts_RollsBackTheEvent(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir); err != nil {
		t.Fatal(err)
	}
	s, err := OpenWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	cid := mustCreateChallengeInternalID(t, s, "t")
	coretest.FailOccurrenceInserts(t, dir)
	before := countActivityRows(t, s)
	if err := writeEventWithOccurrence(context.Background(), s, cid, nil, OccurrenceOutcomeBad); err == nil {
		t.Fatal("event committed although the occurrence insert fails")
	}
	if got := countActivityRows(t, s); got != before {
		t.Errorf("activities = %d, want %d", got, before)
	}
	if n := coretest.CountOccurrences(t, dir); n != 0 {
		t.Errorf("occurrences = %d, want 0", n)
	}
}
