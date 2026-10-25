package core

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"
)

// AC-201・AC-202・AC-204: J5 の写像・判断点と委譲の run の失敗・回収の経路が、出来事と同じトランザクションで発生を作る。

func failOccurrenceInsert(s *Store) error {
	boom := errors.New("injected occurrence failure")
	s.insertOccurrence = func(*sql.Tx, occurrenceRow) error { return boom }
	return boom
}

func (f *j5Fixture) occurrencesOf(t *testing.T) []occurrenceDBRow { return readOccurrences(t, f.s) }

func cycleIntID(t *testing.T, id string) int64 {
	t.Helper()
	n, ok := parseCycleID(id)
	if !ok {
		t.Fatalf("parseCycleID(%q)", id)
	}
	return n
}

func testRunInt(t *testing.T, id string) int64 {
	t.Helper()
	n, ok := parseRunID(id)
	if !ok {
		t.Fatalf("parseRunID(%q)", id)
	}
	return n
}

func (f *j5Fixture) assertOneJ5Occurrence(t *testing.T, kind string, cycleID string) {
	t.Helper()
	occ := f.occurrencesOf(t)
	runs := f.j5Runs(t)
	if len(occ) != 1 || len(runs) != 1 {
		t.Fatalf("occurrences=%+v runs=%d", occ, len(runs))
	}
	o := occ[0]
	rid := testRunInt(t, runs[0].ID)
	if o.kind != kind || o.outcome != "bad" || o.source != "core" {
		t.Errorf("occurrence = %+v", o)
	}
	if !o.causeRunID.Valid || o.causeRunID.Int64 != rid {
		t.Errorf("cause_run_id = %v, want %d", o.causeRunID, rid)
	}
	if !o.cycleID.Valid || o.cycleID.Int64 != cycleIntID(t, cycleID) {
		t.Errorf("cycle_id = %v, want %s", o.cycleID, cycleID)
	}
	var refRun sql.NullInt64
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT run_id FROM occurrence`).Scan(&refRun)
	}); err != nil {
		t.Fatal(err)
	}
	if !refRun.Valid || refRun.Int64 != rid {
		t.Errorf("run_id = %v, want %d", refRun, rid)
	}
}

func TestJ5Occurrence_NotMet(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.j5.result = j5Out("not_met", "FB", nil)
	cyc := f.cycle(t, 300)
	if _, err := f.s.VerifyAutoJ5(context.Background(), f.input(cyc, nil)); err != nil {
		t.Fatal(err)
	}
	f.assertOneJ5Occurrence(t, "verification_not_met", cyc)
}

func TestJ5Occurrence_Uncertain(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.j5.result = j5Out("uncertain", nil, "Q")
	cyc := f.cycle(t, 300)
	if _, err := f.s.VerifyAutoJ5(context.Background(), f.input(cyc, nil)); err != nil {
		t.Fatal(err)
	}
	f.assertOneJ5Occurrence(t, "verification_uncertain", cyc)
}

func TestJ5Occurrence_MetWithActualMismatchIsUncertain(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.setExpected(t, "C-1", 2)
	f.j5.result = j5OutWithActual("met", actualItems(1))
	cyc := f.cycle(t, 300)
	if _, err := f.s.VerifyAutoJ5(context.Background(), f.input(cyc, nil)); err != nil {
		t.Fatal(err)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingHuman {
		t.Fatalf("status = %s", got)
	}
	f.assertOneJ5Occurrence(t, "verification_uncertain", cyc)
}

func TestJ5Occurrence_MetMakesNone(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if occ := f.occurrencesOf(t); len(occ) != 0 {
		t.Errorf("occurrences = %+v", occ)
	}
}

func TestJ5Occurrence_InsertFailureLeavesNoTransition(t *testing.T) {
	cases := map[string]func(t *testing.T, f *j5Fixture){
		"not_met":   func(_ *testing.T, f *j5Fixture) { f.j5.result = j5Out("not_met", "FB", nil) },
		"uncertain": func(_ *testing.T, f *j5Fixture) { f.j5.result = j5Out("uncertain", nil, "Q") },
		"mismatch": func(t *testing.T, f *j5Fixture) {
			f.setExpected(t, "C-1", 2)
			f.j5.result = j5OutWithActual("met", actualItems(1))
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newJ5Fixture(t, "impl")
			f.delegateToVerifying(t, "C-1")
			setup(t, f)
			before := countActivityRows(t, f.s)
			boom := failOccurrenceInsert(f.s)
			if _, err := f.verify(t, nil); !errors.Is(err, boom) {
				t.Fatalf("err = %v, want injected failure", err)
			}
			if got := f.detail(t, "C-1").Status; got != StatusVerifying {
				t.Errorf("status = %s, want verifying (transition must roll back)", got)
			}
			if got := countActivityRows(t, f.s); got != before {
				t.Errorf("activities = %d, want %d", got, before)
			}
			if got := f.queryString(t, `SELECT COUNT(*) FROM hold`); got != "0" {
				t.Errorf("holds = %s, want 0", got)
			}
			if occ := f.occurrencesOf(t); len(occ) != 0 {
				t.Errorf("occurrences = %+v", occ)
			}
		})
	}
}

// --- RunJudgment の終了（judgment_failed） ---

func runJudgmentWith(t *testing.T, s *Store, id string, cycleID *string, out JudgmentLaunchOutput, invokeErr error) (*RunJudgmentResult, error) {
	t.Helper()
	inv := &fakeJudgmentInvoker{result: out, invokeErr: invokeErr}
	return s.RunJudgment(context.Background(), RunJudgmentInput{
		ChallengeID: id, Judgment: JudgmentJ1, MaxBudgetUSD: 1, Stdin: []byte("x"), Invoker: inv, CycleID: cycleID,
	})
}

func TestJudgmentOccurrence_CountedFailuresMakeJudgmentFailed(t *testing.T) {
	for _, r := range []RunResult{RunResultErrored, RunResultMalformed, RunResultInvalidOutput, RunResultTimedOut, RunResultInterrupted} {
		t.Run(string(r), func(t *testing.T) {
			s := newStoreForTest(t)
			id := createChallengeForJudgmentTest(t, s)
			cyc, err := s.BeginCycle(context.Background(), BeginCycleInput{Trigger: "run", BudgetUSD: 100})
			if err != nil {
				t.Fatal(err)
			}
			res, err := runJudgmentWith(t, s, id, &cyc.ID, JudgmentLaunchOutput{Result: r}, nil)
			if err != nil {
				t.Fatal(err)
			}
			occ := readOccurrences(t, s)
			if len(occ) != 1 {
				t.Fatalf("occurrences = %+v", occ)
			}
			o := occ[0]
			rid := testRunInt(t, res.RunID)
			if got := occurrenceRunRef(t, s); got != rid {
				t.Errorf("run_id = %d, want %d", got, rid)
			}
			if o.kind != "judgment_failed" || o.outcome != "bad" || !o.causeRunID.Valid || o.causeRunID.Int64 != rid ||
				!o.cycleID.Valid || o.cycleID.Int64 != cycleIntID(t, cyc.ID) {
				t.Errorf("occurrence = %+v", o)
			}
		})
	}
}

func TestJudgmentOccurrence_LaunchFailedMakesJudgmentFailed(t *testing.T) {
	s := newStoreForTest(t)
	id := createChallengeForJudgmentTest(t, s)
	if _, err := runJudgmentWith(t, s, id, nil, JudgmentLaunchOutput{}, errors.New("cannot start")); err != nil {
		t.Fatal(err)
	}
	occ := readOccurrences(t, s)
	if len(occ) != 1 || occ[0].kind != "judgment_failed" || occ[0].cycleID.Valid {
		t.Fatalf("occurrences = %+v (cycle must be NULL without a cycle)", occ)
	}
}

func TestJudgmentOccurrence_NotCountedResultsMakeNone(t *testing.T) {
	cases := map[string]JudgmentLaunchOutput{
		"succeeded":        {Result: RunResultSucceeded},
		"budget_exhausted": {Result: RunResultBudgetExhausted},
		"rate_limited":     {Result: RunResultErrored, RateLimited: true},
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			s := newStoreForTest(t)
			id := createChallengeForJudgmentTest(t, s)
			if _, err := runJudgmentWith(t, s, id, nil, out, nil); err != nil {
				t.Fatal(err)
			}
			if occ := readOccurrences(t, s); len(occ) != 0 {
				t.Errorf("occurrences = %+v", occ)
			}
		})
	}
}

func TestJudgmentOccurrence_InsertFailureLeavesTheRunOpen(t *testing.T) {
	s := newStoreForTest(t)
	id := createChallengeForJudgmentTest(t, s)
	boom := failOccurrenceInsert(s)
	_, err := runJudgmentWith(t, s, id, nil, JudgmentLaunchOutput{Result: RunResultErrored}, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want injected failure", err)
	}
	cid, _ := parseChallengeID(id)
	active, err := loadActiveRunForTest(t, s, cid)
	if err != nil || active == nil {
		t.Fatalf("active run = %v, %v; the run end must roll back", active, err)
	}
	if occ := readOccurrences(t, s); len(occ) != 0 {
		t.Errorf("occurrences = %+v", occ)
	}
}

// --- 回収の経路（interrupted） ---

func TestReapOccurrence_InterruptedRunsMakeFailureOccurrences(t *testing.T) {
	host, _ := os.Hostname()
	base := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		kind    runKind
		judg    judgmentPoint
		want    string
		bucket  budgetBucket
		version int64
	}{
		"judgment": {runKindJudgment, judgmentJ1, "judgment_failed", budgetBucketJudgment, 1},
		"delegate": {runKindDelegate, "", "delegation_failed", budgetBucketImpl, 1},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := newStoreForTest(t)
			s.staleAfter = 100 * time.Millisecond
			s.now = func() time.Time { return base }
			cid := mustCreateChallengeInternalID(t, s, "t")
			cyc, err := insertCycleForTest(t, s, insertCycleInput{Trigger: "run", StartedAt: base, BudgetUSD: 300_000_000})
			if err != nil {
				t.Fatal(err)
			}
			cycInt := cycleIntID(t, cyc.ID)
			row, err := insertRunForTest(t, s, cid, insertRunInput{
				CycleID: &cycInt, Kind: c.kind, Judgment: c.judg, ChallengeVersion: c.version,
				SessionID: "11111111-1111-1111-1111-111111111111", PID: deadPID(t), Host: host,
				HeartbeatAt: base, StartedAt: base, MaxBudgetUSD: 500_000, BudgetBucket: c.bucket,
			})
			if err != nil {
				t.Fatal(err)
			}
			s.now = func() time.Time { return base.Add(time.Second) }

			boom := failOccurrenceInsert(s)
			if err := s.ReapInterruptedRuns(context.Background()); !errors.Is(err, boom) {
				t.Fatalf("err = %v, want injected failure", err)
			}
			got, _ := loadRunForTest(t, s, testRunInt(t, row.ID))
			if got.EndedAt != nil || len(readOccurrences(t, s)) != 0 {
				t.Fatalf("run end or occurrence survived the failure: %+v", got)
			}

			s.insertOccurrence = nil
			if err := s.ReapInterruptedRuns(context.Background()); err != nil {
				t.Fatal(err)
			}
			occ := readOccurrences(t, s)
			if got := occurrenceRunRef(t, s); got != testRunInt(t, row.ID) {
				t.Errorf("run_id = %d, want %s", got, row.ID)
			}
			if len(occ) != 1 || occ[0].kind != c.want || occ[0].outcome != "bad" ||
				occ[0].causeRunID.Int64 != testRunInt(t, row.ID) || occ[0].cycleID.Int64 != cycInt {
				t.Fatalf("occurrences = %+v, want %s", occ, c.want)
			}
		})
	}
}

func TestReapOccurrence_PredictRunMakesNone(t *testing.T) {
	s := newStoreForTest(t)
	s.staleAfter = 100 * time.Millisecond
	var rid int64
	host, _ := os.Hostname()
	base := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		r, err := insertRun(context.Background(), tx, 0, insertRunInput{
			Kind: runKindPredict, PID: deadPID(t), Host: host, HeartbeatAt: base, StartedAt: base,
			MaxBudgetUSD: 1, BudgetBucket: budgetBucketPredict, Repo: "o/r",
		})
		if err != nil {
			return err
		}
		rid = testRunInt(t, r.ID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return base.Add(time.Second) }
	if err := s.ReapInterruptedRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := loadRunForTest(t, s, rid); got.Result != RunResultInterrupted {
		t.Fatalf("result = %s", got.Result)
	}
	if occ := readOccurrences(t, s); len(occ) != 0 {
		t.Errorf("occurrences = %+v", occ)
	}
}

func TestReclaimOccurrence_DeadHolderRunMakesFailureOccurrence(t *testing.T) {
	ctx := context.Background()
	s := newStoreForTest(t)
	s.lockStaleAfter = 100 * time.Millisecond
	base := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }
	oldCyc, err := insertCycleForTest(t, s, insertCycleInput{Trigger: "old", StartedAt: base, BudgetUSD: 300_000_000})
	if err != nil {
		t.Fatal(err)
	}
	oldInt := cycleIntID(t, oldCyc.ID)
	host, _ := os.Hostname()
	pid := deadPID(t)
	if _, err := insertLockForTest(t, s, insertLockInput{Name: cycleLockName, Holder: oldInt, PID: pid, Host: host, AcquiredAt: base, HeartbeatAt: base}); err != nil {
		t.Fatal(err)
	}
	cid := mustCreateChallengeInternalID(t, s, "t")
	row, err := insertRunForTest(t, s, cid, insertRunInput{
		CycleID: &oldInt, Kind: runKindDelegate, ChallengeVersion: 1, SessionID: "11111111-1111-1111-1111-111111111111",
		PID: pid, Host: host, HeartbeatAt: base.Add(50 * time.Millisecond), StartedAt: base, MaxBudgetUSD: 400_000, BudgetBucket: budgetBucketImpl,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return base.Add(200 * time.Millisecond) }

	boom := failOccurrenceInsert(s)
	if _, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want injected failure", err)
	}
	if got, _ := loadRunForTest(t, s, testRunInt(t, row.ID)); got.EndedAt != nil {
		t.Fatal("run end must roll back with the failed occurrence")
	}

	s.insertOccurrence = nil
	if _, err := s.BeginCycle(ctx, BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true}); err != nil {
		t.Fatal(err)
	}
	occ := readOccurrences(t, s)
	if got := occurrenceRunRef(t, s); got != testRunInt(t, row.ID) {
		t.Errorf("run_id = %d, want %s", got, row.ID)
	}
	if len(occ) != 1 || occ[0].kind != "delegation_failed" || occ[0].cycleID.Int64 != oldInt || occ[0].causeRunID.Int64 != testRunInt(t, row.ID) {
		t.Fatalf("occurrences = %+v", occ)
	}
}

func occurrenceRunRef(t *testing.T, s *Store) int64 {
	t.Helper()
	var n sql.NullInt64
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT run_id FROM occurrence`).Scan(&n)
	}); err != nil || !n.Valid {
		t.Fatalf("run_id = %v, %v", n, err)
	}
	return n.Int64
}

// 再検査で invalid_output に書き換える経路（succeeded で閉じた後）も judgment_failed を作る。
func TestJ5Occurrence_InvalidOutputRewriteMakesJudgmentFailedAndRollsBack(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.j5.result = JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{"verdict":"bogus"}`)}
	cyc := f.cycle(t, 300)
	if _, err := f.s.VerifyAutoJ5(context.Background(), f.input(cyc, nil)); err != nil {
		t.Fatal(err)
	}
	runs := f.j5Runs(t)
	if len(runs) != 1 || runs[0].Result != RunResultInvalidOutput {
		t.Fatalf("runs = %+v", runs)
	}
	occ := f.occurrencesOf(t)
	if len(occ) != 1 || occ[0].kind != "judgment_failed" || occ[0].causeRunID.Int64 != testRunInt(t, runs[0].ID) ||
		occ[0].cycleID.Int64 != cycleIntID(t, cyc) {
		t.Fatalf("occurrences = %+v", occ)
	}

	f2 := newJ5Fixture(t, "impl")
	f2.delegateToVerifying(t, "C-1")
	f2.j5.result = JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: []byte(`{"verdict":"bogus"}`)}
	boom := failOccurrenceInsert(f2.s)
	if _, err := f2.verify(t, nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want injected failure", err)
	}
	if r := f2.j5Runs(t); len(r) != 1 || r[0].Result == RunResultInvalidOutput {
		t.Errorf("invalid_output rewrite must roll back: %+v", r)
	}
}
