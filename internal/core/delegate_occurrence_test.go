package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// S3（#355）: 委譲の失敗・予算の使い切り・保留で発生を記録する（AC-203・205〜210）。

type occRow struct {
	kind                string
	cycleID, causeRunID sql.NullInt64
	holdID, runID       sql.NullInt64
	outcome, source     string
}

func readOccRows(t *testing.T, s *Store) []occRow {
	t.Helper()
	var out []occRow
	err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT kind, cycle_id, cause_run_id, hold_id, run_id, outcome, source FROM occurrence ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var r occRow
			if err := rows.Scan(&r.kind, &r.cycleID, &r.causeRunID, &r.holdID, &r.runID, &r.outcome, &r.source); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func occKinds(rows []occRow) []string {
	var k []string
	for _, r := range rows {
		k = append(k, r.kind)
	}
	return k
}

func latestHoldID(t *testing.T, s *Store) int64 {
	t.Helper()
	var id int64
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT MAX(id) FROM hold`).Scan(&id)
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

// wantLatestCycle は発生の cycle_id が、その出来事を起こした周（最後に作られた周）の ID と値で一致することを
// 確かめる（AC-217。NULL でないことだけでなく、どの周かまで照合する）。
func wantLatestCycle(t *testing.T, s *Store, o occRow) {
	t.Helper()
	var want int64
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT MAX(id) FROM cycle`).Scan(&want)
	}); err != nil {
		t.Fatal(err)
	}
	if !o.cycleID.Valid || o.cycleID.Int64 != want || want == 0 {
		t.Errorf("cycle_id = %+v, want the latest cycle %d", o.cycleID, want)
	}
}

func wantKinds(t *testing.T, rows []occRow, want ...string) {
	t.Helper()
	got := occKinds(rows)
	if len(got) != len(want) {
		t.Fatalf("occurrence kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("occurrence kinds = %v, want %v", got, want)
		}
	}
}

func TestOccurrence_CountedFailureCreatesDelegationFailedWithRunRefCauseAndCycle(t *testing.T) {
	for _, r := range []RunResult{RunResultErrored, RunResultMalformed, RunResultInvalidOutput, RunResultTimedOut, RunResultLaunchFailed} {
		t.Run(string(r), func(t *testing.T) {
			f := newResumeFixture(t)
			id := f.newInProgress(t, "t", "P1", planSpec(nil))
			f.deleg.result = failOut(r)
			f.runDelegation(t, id)
			rows := readOccRows(t, f.s)
			wantKinds(t, rows, "delegation_failed")
			runs := f.delegateRuns(t)
			n := runIntID(runs[0])
			o := rows[0]
			if !o.runID.Valid || o.runID.Int64 != n || !o.causeRunID.Valid || o.causeRunID.Int64 != n {
				t.Errorf("ref/cause = %+v, want run %d", o, n)
			}
			if !o.cycleID.Valid || o.outcome != "bad" || o.source != "core" || o.holdID.Valid {
				t.Errorf("row = %+v", o)
			}
			wantLatestCycle(t, f.s, o)
		})
	}
}

func TestOccurrence_RateLimitedFailureAndSuccessCreateNothing(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	limited := failOut(RunResultErrored)
	limited.RateLimited = true
	f.deleg.result = limited
	f.runDelegation(t, id)
	wantKinds(t, readOccRows(t, f.s))

	f2 := newReconFixture(t, "none")
	if _, err := f2.run(t, nil); err != nil {
		t.Fatal(err)
	}
	wantKinds(t, readOccRows(t, f2.s))
}

func TestOccurrence_BudgetExhaustedCreatesOnlyBudgetExhausted(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.result = failOut(RunResultBudgetExhausted)
	f.runDelegation(t, id)
	rows := readOccRows(t, f.s)
	wantKinds(t, rows, "budget_exhausted")
	n := runIntID(f.delegateRuns(t)[0])
	if !rows[0].runID.Valid || rows[0].runID.Int64 != n || !rows[0].causeRunID.Valid || rows[0].causeRunID.Int64 != n || !rows[0].cycleID.Valid {
		t.Errorf("row = %+v", rows[0])
	}
	wantLatestCycle(t, f.s, rows[0])
}

func TestOccurrence_QuestionsAndBlockedCreateDelegationStoppedReferencingTheHold(t *testing.T) {
	for name, out := range map[string]JudgmentLaunchOutput{
		"questions": questionsOut("feat/x"),
		"blocked": {Result: RunResultSucceeded, ReportedTotalCostUSD: float64Ptr(1), StructuredOutput: reportJSON(func(m map[string]any) {
			m["outcome"] = "blocked"
		})},
	} {
		t.Run(name, func(t *testing.T) {
			f := newResumeFixture(t)
			id := f.newInProgress(t, "t", "P1", planSpec(nil))
			f.deleg.result = out
			f.runDelegation(t, id)
			rows := readOccRows(t, f.s)
			wantKinds(t, rows, "delegation_stopped")
			if !rows[0].holdID.Valid || rows[0].holdID.Int64 != latestHoldID(t, f.s) || !rows[0].cycleID.Valid || rows[0].outcome != "bad" {
				t.Errorf("row = %+v", rows[0])
			}
			wantLatestCycle(t, f.s, rows[0])
		})
	}
}

func TestOccurrence_MissingArtifactHoldCreatesNothing(t *testing.T) {
	f := newReconFixture(t, "impl") // 成果物の種類が pr で、見つからない
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	if f.status(t) != StatusAwaitingHuman {
		t.Fatalf("status = %s", f.status(t))
	}
	wantKinds(t, readOccRows(t, f.s))
}

func TestOccurrence_UnapprovedMergeCreatesUnapprovedMergeReferencingTheHold(t *testing.T) {
	f := newReconFixture(t, "impl")
	f.branches.prs["feat/x"] = []UpstreamPullRequest{mkPR(1, "merged", "main")}
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	rows := readOccRows(t, f.s)
	wantKinds(t, rows, "unapproved_merge")
	if !rows[0].holdID.Valid || rows[0].holdID.Int64 != latestHoldID(t, f.s) || !rows[0].cycleID.Valid {
		t.Errorf("row = %+v", rows[0])
	}
	wantLatestCycle(t, f.s, rows[0])
}

func TestOccurrence_NotEligibleReconciliationCreatesNothing(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.result = questionsOut("feat/x")
	f.deleg.onInvoke = func(DelegateLaunchInput) {
		if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE challenge SET status = 'verifying', version = version + 1 WHERE id = 1`)
			return err
		}); err != nil {
			t.Error(err)
		}
	}
	f.runDelegation(t, id)
	wantKinds(t, readOccRows(t, f.s))
}

func TestOccurrence_DelegationFailureLimitHoldCreatesFailureLimitHold(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.queue = []JudgmentLaunchOutput{failOut(RunResultErrored), failOut(RunResultTimedOut)}
	f.runDelegation(t, id)
	f.runDelegation(t, id)
	res, err := f.s.RunDelegation(context.Background(), f.input(f.cycle(t, 300), nil))
	if err != nil || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedFailureLimit {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	rows := readOccRows(t, f.s)
	wantKinds(t, rows, "delegation_failed", "delegation_failed", "failure_limit_hold")
	h := rows[2]
	if !h.holdID.Valid || h.holdID.Int64 != latestHoldID(t, f.s) || !h.cycleID.Valid || h.outcome != "bad" {
		t.Errorf("row = %+v", h)
	}
	wantLatestCycle(t, f.s, h)
}

func TestOccurrence_ReworkLimitHoldCreatesFailureLimitHold(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.agent.ReworkLimit = 2
	f.notMetRound(t, "C-1")
	f.runDelegation(t, "C-1")
	f.notMetRound(t, "C-1")
	if res := f.runDelegation(t, "C-1"); len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedReworkLimit {
		t.Fatalf("res = %+v", res)
	}
	rows := readOccRows(t, f.s)
	last := rows[len(rows)-1]
	if last.kind != "failure_limit_hold" || !last.holdID.Valid || last.holdID.Int64 != latestHoldID(t, f.s) || !last.cycleID.Valid {
		t.Fatalf("rows = %v last = %+v", occKinds(rows), last)
	}
}

func TestOccurrence_JudgmentFailureLimitHoldCreatesFailureLimitHold(t *testing.T) {
	s := newStoreForTest(t)
	decl := newAgentDeclForJ1Test(t, s, "pos")
	if _, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"}); err != nil {
		t.Fatal(err)
	}
	inv := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultErrored}}
	for i := 0; i <= decl.FailureLimit; i++ {
		autoJ1Once(t, s, decl, inv)
	}
	rows := readOccRows(t, s)
	if len(rows) == 0 {
		t.Fatal("no occurrence")
	}
	last := rows[len(rows)-1]
	if last.kind != "failure_limit_hold" || !last.holdID.Valid || last.holdID.Int64 != latestHoldID(t, s) || !last.cycleID.Valid {
		t.Fatalf("kinds = %v last = %+v", occKinds(rows), last)
	}
}

// 発生の挿入が失敗すれば、run の終了も保留も残らない。
func TestOccurrence_InsertFailureRollsBackRunEnd(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	boom := errors.New("injected")
	f.s.insertOccurrence = func(*sql.Tx, occurrenceRow) error { return boom }
	f.deleg.result = failOut(RunResultErrored)
	if _, err := f.run(t, &id); err == nil {
		t.Fatal("run end committed although the occurrence insert fails")
	}
	runs := f.delegateRuns(t)
	if len(runs) != 1 || runs[0].EndedAt != nil {
		t.Errorf("runs = %+v, want the run left open", runs)
	}
	wantKinds(t, readOccRows(t, f.s))
}

func TestOccurrence_InsertFailureRollsBackHolds(t *testing.T) {
	boom := errors.New("injected")
	holds := func(t *testing.T, s *Store) int {
		var n int
		if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT COUNT(*) FROM hold`).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	t.Run("questions", func(t *testing.T) {
		f := newResumeFixture(t)
		id := f.newInProgress(t, "t", "P1", planSpec(nil))
		f.deleg.result = questionsOut("feat/x")
		f.s.insertOccurrence = func(*sql.Tx, occurrenceRow) error { return boom }
		if _, err := f.run(t, &id); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if holds(t, f.s) != 0 || f.detail(t, id).Status != StatusInProgress {
			t.Errorf("hold or status change committed: status=%s", f.detail(t, id).Status)
		}
	})
	t.Run("delegation failure limit", func(t *testing.T) {
		f := newResumeFixture(t)
		id := f.newInProgress(t, "t", "P1", planSpec(nil))
		f.deleg.queue = []JudgmentLaunchOutput{failOut(RunResultErrored), failOut(RunResultErrored)}
		f.runDelegation(t, id)
		f.runDelegation(t, id)
		f.s.insertOccurrence = func(*sql.Tx, occurrenceRow) error { return boom }
		if _, err := f.s.RunDelegation(context.Background(), f.input(f.cycle(t, 300), nil)); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if holds(t, f.s) != 0 || f.detail(t, id).Status != StatusInProgress {
			t.Errorf("hold committed: status=%s", f.detail(t, id).Status)
		}
	})
	t.Run("judgment failure limit", func(t *testing.T) {
		s := newStoreForTest(t)
		decl := newAgentDeclForJ1Test(t, s, "pos")
		ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
		if err != nil {
			t.Fatal(err)
		}
		inv := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultErrored}}
		for i := 0; i < decl.FailureLimit; i++ {
			autoJ1Once(t, s, decl, inv)
		}
		s.insertOccurrence = func(*sql.Tx, occurrenceRow) error { return boom }
		_, err = s.ClassifyAutoJ1(context.Background(), J1AutoInput{AgentDecl: decl, Invoker: inv, CycleID: beginJ1Cycle(t, s, 300)})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if holds(t, s) != 0 {
			t.Error("hold committed")
		}
		if got, _ := s.GetChallenge(context.Background(), ch.ID); got.Status == StatusAwaitingHuman {
			t.Error("status changed")
		}
	})
}
