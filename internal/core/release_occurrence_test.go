package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// 取り消し・merged_unapproved の経路が作る発生（AC-211〜AC-214）のテスト。

type relOccurrence struct {
	challengeID int64
	cycleID     sql.NullInt64
	kind        string
	outcome     string
	source      string
	executionID sql.NullInt64
}

func (f *relFixture) occurrences(t *testing.T) []relOccurrence {
	t.Helper()
	var out []relOccurrence
	readForTest(t, f.s, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT challenge_id, cycle_id, kind, outcome, source, execution_id FROM occurrence ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var o relOccurrence
			if err := rows.Scan(&o.challengeID, &o.cycleID, &o.kind, &o.outcome, &o.source, &o.executionID); err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	return out
}

func (f *relFixture) beginCycle(t *testing.T) (string, int64) {
	t.Helper()
	cyc, err := f.s.BeginCycle(context.Background(), BeginCycleInput{Trigger: "op exec", BudgetUSD: 1})
	if err != nil {
		t.Fatal(err)
	}
	n, ok := parseCycleID(cyc.ID)
	if !ok {
		t.Fatalf("bad cycle id %q", cyc.ID)
	}
	return cyc.ID, n
}

func (f *relFixture) wantSingleOccurrence(t *testing.T, kind string) relOccurrence {
	t.Helper()
	occ := f.occurrences(t)
	if len(occ) != 1 {
		t.Fatalf("occurrences = %+v, want exactly 1 (%s)", occ, kind)
	}
	o := occ[0]
	at := f.attempts(t)
	if o.kind != kind || o.outcome != "bad" || o.source != "core" || o.challengeID != f.cid ||
		!o.executionID.Valid || o.executionID.Int64 != at[len(at)-1].ID {
		t.Errorf("occurrence = %+v, want kind %s bad core challenge %d execution %d", o, kind, f.cid, at[len(at)-1].ID)
	}
	return o
}

// AC-211: 取り消しの結果ごとに release_withdrawn が 1 行。参照は試行。周の ID を持つ。
func TestRelease_CancelResultsCreateReleaseWithdrawn(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *relFixture)
		want  ExecutionResult
	}{
		{"head_changed", func(f *relFixture) { f.fake.pr.HeadSHA = "zzz" }, ExecutionResultHeadChanged},
		{"closed", func(f *relFixture) { f.fake.pr.State = "closed" }, ExecutionResultClosed},
		{"base_changed", func(f *relFixture) { f.fake.pr.Base = "dev" }, ExecutionResultBaseChanged},
		{"no_approved_head", func(f *relFixture) {
			writeForTest(t, f.s, func(tx *sql.Tx) error {
				_, err := tx.Exec(`UPDATE approval SET head_sha = NULL WHERE operation_id = ?`, f.opID)
				return err
			})
		}, ExecutionResultNoApprovedHead},
		{"unsupported", func(f *relFixture) {
			writeForTest(t, f.s, func(tx *sql.Tx) error {
				_, err := tx.Exec(`UPDATE operation SET ref = 'not a url' WHERE id = ?`, f.opID)
				return err
			})
		}, ExecutionResultUnsupported},
		{"limit_reached", func(f *relFixture) {
			f.limit = 1
			f.fake.pr.Checks = []UpstreamCheck{{Status: "completed", Conclusion: "failure"}}
		}, ExecutionResultLimitReached},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newRelFixture(t)
			c.setup(f)
			cycleStr, cycleN := f.beginCycle(t)
			o := f.opts()
			o.CycleID = cycleStr
			res, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, o)
			if err != nil || len(res.Items) != 1 || res.Items[0].Result != c.want {
				t.Fatalf("res = %+v err = %v, want %s", res, err, c.want)
			}
			occ := f.wantSingleOccurrence(t, string(OccurrenceKindReleaseWithdrawn))
			if !occ.cycleID.Valid || occ.cycleID.Int64 != cycleN {
				t.Errorf("cycle_id = %+v, want %d", occ.cycleID, cycleN)
			}
		})
	}
}

// 周を持たない呼び出し（CycleID が空）の発生は cycle_id が NULL。
func TestRelease_CancelWithoutCycleHasNullCycleID(t *testing.T) {
	f := newRelFixture(t)
	f.fake.pr.HeadSHA = "zzz"
	f.run(t)
	if occ := f.wantSingleOccurrence(t, string(OccurrenceKindReleaseWithdrawn)); occ.cycleID.Valid {
		t.Errorf("cycle_id = %+v, want NULL", occ.cycleID)
	}
}

func TestRelease_InvalidOrUnknownCycleIDIsValidationBeforeAnyExecution(t *testing.T) {
	for _, id := range []string{"bogus", "Y-999"} {
		f := newRelFixture(t)
		o := f.opts()
		o.CycleID = id
		if _, err := f.s.ExecuteReleases(context.Background(), nil, o); !errors.Is(err, ErrValidation) {
			t.Fatalf("%s: err = %v, want ErrValidation", id, err)
		}
		if f.fake.getCalls != 0 || len(f.attempts(t)) != 0 {
			t.Errorf("%s: nothing may run (gets %d, attempts %d)", id, f.fake.getCalls, len(f.attempts(t)))
		}
	}
}

// AC-212: merged_unapproved は unapproved_merge だけ（release_withdrawn は作らない）。
func TestRelease_MergedUnapprovedCreatesOnlyUnapprovedMerge(t *testing.T) {
	// 先頭の読み取りで承認と違う head のままマージ済み。
	f := newRelFixture(t)
	f.fake.pr.State = "merged"
	f.fake.pr.HeadSHA = "zzz"
	if item := f.run(t); item.Result != ExecutionResultMergedUnapproved {
		t.Fatalf("item = %+v", item)
	}
	f.wantSingleOccurrence(t, string(OccurrenceKindUnapprovedMerge))

	// 依頼の後の読み取りで承認と違う head でマージ済み。
	g := newRelFixture(t)
	g.fake.afterPR = &UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: "zzz", MergeCommitSHA: "m"}
	if item := g.run(t); item.Result != ExecutionResultMergedUnapproved {
		t.Fatalf("item = %+v", item)
	}
	g.wantSingleOccurrence(t, string(OccurrenceKindUnapprovedMerge))
}

// AC-214: 待つ結果・実行済み・interrupted では発生を作らない。
func TestRelease_NoOccurrenceForWaitingExecutedInterrupted(t *testing.T) {
	f := newRelFixture(t)
	f.limit = 100
	want := func(r ExecutionResult) {
		t.Helper()
		if got := f.run(t).Result; got != r {
			t.Fatalf("result = %s, want %s", got, r)
		}
	}
	f.fake.pr.Checks = []UpstreamCheck{{Status: "queued"}}
	want(ExecutionResultWaitingChecks)
	f.fake.getErr = errors.New("x")
	want(ExecutionResultUpstreamFailed)
	f.fake.getErr = nil
	f.fake.pr.Checks = []UpstreamCheck{{Status: "completed", Conclusion: "failure"}}
	want(ExecutionResultChecksFailed)
	f.fake.pr.Checks = nil
	f.fake.mergeErr = errors.New("rejected")
	want(ExecutionResultMergeFailed)
	f.fake.mergeErr = nil
	f.fake.pr.Mergeable = nil
	want(ExecutionResultWaitingChecks)
	notMergeable := false
	f.fake.pr.Mergeable = &notMergeable
	want(ExecutionResultNotMergeable)
	f.fake.pr.Mergeable = truePtr()
	if occ := f.occurrences(t); len(occ) != 0 {
		t.Fatalf("occurrences after waiting results = %+v, want none", occ)
	}

	f.fake.pr = openPR()
	f.fake.afterPR = &UpstreamPullRequestChecks{URL: relRef, State: "open", Base: "main", HeadSHA: relHead}
	if item := f.run(t); item.Result != ExecutionResultMergeUnconfirmed {
		t.Fatalf("item = %+v", item)
	}
	f.fake.afterPR = &UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: relHead, MergeCommitSHA: "m1"}
	if item := f.run(t); item.Result != ExecutionResultMerged && item.Result != ExecutionResultAlreadyMerged {
		t.Fatalf("item = %+v", item)
	}
	if occ := f.occurrences(t); len(occ) != 0 {
		t.Fatalf("occurrences after executed = %+v, want none", occ)
	}

	// interrupted: 止まった試行を回収して閉じる経路。
	h := newRelFixture(t)
	writeForTest(t, h.s, func(tx *sql.Tx) error {
		_, err := beginOperationExecution(context.Background(), tx, h.opID, beginOperationExecutionInput{StartedAt: execTime(1), ApprovedHead: relHead, PID: 999999, Host: "other-host"})
		return err
	})
	h.now = execTime(100000)
	h.fake.pr.Checks = []UpstreamCheck{{Status: "queued"}}
	h.run(t)
	if at := h.attempts(t); len(at) != 2 || at[0].Result != ExecutionResultInterrupted {
		t.Fatalf("attempts = %+v, want the stale attempt closed as interrupted", at)
	}
	if occ := h.occurrences(t); len(occ) != 0 {
		t.Fatalf("occurrences after interrupted = %+v, want none", occ)
	}
}

// 発生の挿入が失敗すれば、取り消し（試行の閉じ・状態・版・作業ログ）も残らない。
func TestRelease_OccurrenceInsertFailureRollsBackCancel(t *testing.T) {
	for _, mode := range []string{"head_changed", "merged_unapproved"} {
		t.Run(mode, func(t *testing.T) {
			f := newRelFixture(t)
			if mode == "head_changed" {
				f.fake.pr.HeadSHA = "zzz"
			} else {
				f.fake.pr.State = "merged"
				f.fake.pr.HeadSHA = "zzz"
			}
			boom := errors.New("injected occurrence failure")
			f.s.insertOccurrence = func(*sql.Tx, occurrenceRow) error { return boom }
			before := f.state(t)
			_, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, f.opts())
			if !errors.Is(err, boom) {
				t.Fatalf("err = %v, want injected failure", err)
			}
			st := f.state(t)
			if st.opState != "approved" || st.opVersion != 2 || st.chVersion != before.chVersion {
				t.Errorf("operation = %+v, want approved/untouched", st)
			}
			if len(f.opExecuteLogs(t)) != 0 {
				t.Errorf("op_execute logs = %d, want 0", len(f.opExecuteLogs(t)))
			}
			for _, a := range f.attempts(t) {
				if a.Result == ExecutionResultHeadChanged || a.Result == ExecutionResultMergedUnapproved {
					t.Errorf("attempt %+v must not keep the cancel result", a)
				}
			}
			if occ := f.occurrences(t); len(occ) != 0 {
				t.Errorf("occurrences = %+v, want none", occ)
			}
		})
	}
}

// 実行済み（merged）の経路は、発生の挿入の失敗の影響を受けない（発生を作らないため）。
func TestRelease_ExecutedIgnoresOccurrenceHook(t *testing.T) {
	f := newRelFixture(t)
	f.fake.afterPR = &UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: relHead, MergeCommitSHA: "m1"}
	f.s.insertOccurrence = func(*sql.Tx, occurrenceRow) error { return errors.New("must not be called") }
	if item := f.run(t); item.Result != ExecutionResultMerged {
		t.Fatalf("item = %+v", item)
	}
}

// --- AC-213: 取り消された後にマージされた release（AC-52a）---

func TestRelease_WithdrawnThenMergedWritesOneTransactionLikeCancel(t *testing.T) {
	f := newRelFixture(t)
	f.fake.afterPR = &UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: relHead, MergeCommitSHA: "m1"}
	f.fake.onMerge = func() { coretest.SetOperationState(t, f.s.workspace, f.opID, "pending") }
	before := f.state(t)
	cycleStr, cycleN := f.beginCycle(t)
	o := f.opts()
	o.CycleID = cycleStr
	res, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, o)
	if err != nil || len(res.Items) != 1 || res.Items[0].Result != ExecutionResultMergedUnapproved {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	occ := f.wantSingleOccurrence(t, string(OccurrenceKindUnapprovedMerge))
	if !occ.cycleID.Valid || occ.cycleID.Int64 != cycleN {
		t.Errorf("cycle_id = %+v, want %d", occ.cycleID, cycleN)
	}
	st := f.state(t)
	if st.opState != "pending" || st.opVersion != before.opVersion+1 || st.chVersion != before.chVersion+1 {
		t.Errorf("state = %+v before = %+v, want pending with both versions +1", st, before)
	}
	var entities []string
	for _, a := range f.opExecuteLogs(t) {
		entities = append(entities, a.Entity)
	}
	if len(entities) != 2 || entities[0] != "operation" || entities[1] != "challenge" {
		t.Errorf("op_execute entities = %v, want [operation challenge]", entities)
	}
}

// 試行がすでに閉じていて新しい試行を足す場合も、発生の参照は足した試行。
func TestRelease_WithdrawnThenMergedNewAttemptIsReferenced(t *testing.T) {
	f := newRelFixture(t)
	f.fake.afterPR = &UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: relHead, MergeCommitSHA: "m1"}
	f.fake.onMerge = func() {
		coretest.SetOperationState(t, f.s.workspace, f.opID, "pending")
		writeForTest(t, f.s, func(tx *sql.Tx) error {
			last, err := latestOperationExecution(context.Background(), tx, f.opID)
			if err != nil {
				return err
			}
			return closeOperationExecution(context.Background(), tx, last.ID, closeOperationExecutionInput{EndedAt: f.now, Result: ExecutionResultInterrupted})
		})
	}
	res, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, f.opts())
	if err != nil || len(res.Items) != 1 || res.Items[0].Result != ExecutionResultMergedUnapproved {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if n := len(f.attempts(t)); n != 2 {
		t.Fatalf("attempts = %d, want 2", n)
	}
	f.wantSingleOccurrence(t, string(OccurrenceKindUnapprovedMerge))
}

// 別の呼び出しが同じマージを記録済みで何も書かなかったときは、発生・作業ログ・版の更新を作らない。
func TestRelease_WithdrawnAlreadyRecordedWritesNothing(t *testing.T) {
	f := newRelFixture(t)
	f.fake.afterPR = &UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: relHead, MergeCommitSHA: "m1"}
	f.fake.onMerge = func() {
		coretest.SetOperationState(t, f.s.workspace, f.opID, "pending")
		writeForTest(t, f.s, func(tx *sql.Tx) error {
			last, err := latestOperationExecution(context.Background(), tx, f.opID)
			if err != nil {
				return err
			}
			if err := closeOperationExecution(context.Background(), tx, last.ID, closeOperationExecutionInput{EndedAt: f.now, Result: ExecutionResultInterrupted}); err != nil {
				return err
			}
			a, err := beginOperationExecution(context.Background(), tx, f.opID, beginOperationExecutionInput{StartedAt: f.now, ApprovedHead: relHead, PID: 1, Host: "h"})
			if err != nil {
				return err
			}
			return closeOperationExecution(context.Background(), tx, a.ID, closeOperationExecutionInput{EndedAt: f.now, Result: ExecutionResultMergedUnapproved})
		})
	}
	before := f.state(t)
	if _, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, f.opts()); err != nil {
		t.Fatal(err)
	}
	if occ := f.occurrences(t); len(occ) != 0 {
		t.Errorf("occurrences = %+v, want none", occ)
	}
	if st := f.state(t); st.opVersion != before.opVersion || st.chVersion != before.chVersion {
		t.Errorf("state = %+v before = %+v, want versions untouched", st, before)
	}
	if n := len(f.opExecuteLogs(t)); n != 0 {
		t.Errorf("op_execute logs = %d, want 0", n)
	}
}

// 取り消されておらず（approved）記録が衝突しただけの経路は、発生を作らない。
func TestRelease_ConflictWithoutWithdrawalCreatesNoOccurrence(t *testing.T) {
	f := newRelFixture(t)
	f.fake.afterPR = &UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: relHead, MergeCommitSHA: "m1"}
	f.fake.onMerge = func() {
		writeForTest(t, f.s, func(tx *sql.Tx) error {
			last, err := latestOperationExecution(context.Background(), tx, f.opID)
			if err != nil {
				return err
			}
			return closeOperationExecution(context.Background(), tx, last.ID, closeOperationExecutionInput{EndedAt: f.now, Result: ExecutionResultInterrupted})
		})
	}
	if _, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, f.opts()); err != nil {
		t.Fatal(err)
	}
	if occ := f.occurrences(t); len(occ) != 0 {
		t.Errorf("occurrences = %+v, want none", occ)
	}
}

// 発生の挿入が失敗すれば、この経路の試行の閉じ・版の更新・作業ログも残らず、merge_unconfirmed になる。
func TestRelease_WithdrawnOccurrenceInsertFailureRollsBack(t *testing.T) {
	f := newRelFixture(t)
	f.fake.afterPR = &UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: relHead, MergeCommitSHA: "m1"}
	f.fake.onMerge = func() { coretest.SetOperationState(t, f.s.workspace, f.opID, "pending") }
	boom := errors.New("injected occurrence failure")
	f.s.insertOccurrence = func(*sql.Tx, occurrenceRow) error { return boom }
	before := f.state(t)
	res, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, f.opts())
	if err != nil || len(res.Items) != 1 || res.Items[0].Result != ExecutionResultMergeUnconfirmed {
		t.Fatalf("res = %+v err = %v, want merge_unconfirmed", res, err)
	}
	for _, a := range f.attempts(t) {
		if a.Result == ExecutionResultMergedUnapproved {
			t.Errorf("attempt %+v must not be merged_unapproved", a)
		}
	}
	if st := f.state(t); st.opVersion != before.opVersion || st.chVersion != before.chVersion {
		t.Errorf("state = %+v before = %+v, want versions untouched", st, before)
	}
	if n := len(f.opExecuteLogs(t)); n != 0 {
		t.Errorf("op_execute logs = %d, want 0", n)
	}
	if occ := f.occurrences(t); len(occ) != 0 {
		t.Errorf("occurrences = %+v, want none", occ)
	}
}
