package core

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
)

// docs/features/ui-relation-graph.md §衝突（core）。衝突の予測の結果を challenge_conflict へ記録する。
// 前提の依存（challenge_dependency）とは別の表で、依存の評価・委譲の起動の判定は読まない。

type conflictRowForTest struct {
	Challenge, With int64
	Directed        bool
	HasCycle        bool
	HasRun          bool
}

func conflictRowsForTest(t *testing.T, s *Store) []conflictRowForTest {
	t.Helper()
	var out []conflictRowForTest
	err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT challenge_id, conflicts_with_id, directed, cycle_id IS NOT NULL, run_id IS NOT NULL
			FROM challenge_conflict ORDER BY challenge_id, conflicts_with_id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var r conflictRowForTest
			if err := rows.Scan(&r.Challenge, &r.With, &r.Directed, &r.HasCycle, &r.HasRun); err != nil {
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

func cidOf(t *testing.T, id string) int64 {
	t.Helper()
	n, ok := parseChallengeID(id)
	if !ok {
		t.Fatalf("bad challenge id %q", id)
	}
	return n
}

// 衝突は着手の門にしない: 衝突の行があっても、前提の依存が満たされていれば依存の判定は通り、
// 依存の評価は衝突の行の有無で変わらない。
func TestConflict_RowsNeverEnterTheDependencyEvaluation(t *testing.T) {
	t.Parallel()
	f := newPlanFixture(t)
	a := f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	insertConflictForTest(t, f.s, cidOf(t, a), cidOf(t, b), false)
	if len(conflictRowsForTest(t, f.s)) != 1 {
		t.Fatal("precondition: a conflict row must exist")
	}
	evalOf := func() *dependencyEvaluation {
		var ev *dependencyEvaluation
		if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
			ev, err = evaluateDependencies(context.Background(), tx)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return ev
	}
	if ev := evalOf(); len(ev.Dependencies) != 0 || len(ev.Cyclic) != 0 {
		t.Errorf("evaluation = %+v, want no dependencies from a conflict row", ev)
	}
	// 前提の依存（b は a に依存。a は完了していない）は、衝突の行とは別に効く。
	if _, err := f.s.AddDependencies(context.Background(), ChannelCLI, b, []string{a}); err != nil {
		t.Fatal(err)
	}
	ev := evalOf()
	if got := ev.Dependencies[cidOf(t, b)]; len(got) != 1 || got[0].Met {
		t.Errorf("b's dependencies = %+v, want one unmet", got)
	}
	// 衝突の行を消しても、依存の評価は同じ。
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM challenge_conflict`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(evalOf(), ev) {
		t.Errorf("evaluation changed when the conflict rows were removed")
	}
}

// 委譲の対象の選び方（依存の判定）は衝突の行を読まない: 衝突の行があっても、前提の無い課題は対象になる。
func TestConflict_RowDoesNotKeepAChallengeOutOfTheDelegationTargets(t *testing.T) {
	t.Parallel()
	f := newPlanFixture(t)
	a := f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	insertConflictForTest(t, f.s, cidOf(t, a), cidOf(t, b), false)
	var targets []int64
	var skipped []NotStarted
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
		targets, skipped, err = selectDelegationTargets(context.Background(), tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || len(skipped) != 0 {
		t.Fatalf("targets = %v, skipped = %+v, want both challenges and no skip", targets, skipped)
	}
}

// 前提の依存が満たされていれば、衝突の行（相手は未完了）があっても委譲は起動する（衝突は着手の門にしない）。
func TestConflict_SatisfiedPrerequisiteStartsEvenWithAConflictRow(t *testing.T) {
	t.Parallel()
	f := newPlanFixture(t)
	pre := f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	other := f.candidate(t, "multi", "P1", 13)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET status = 'done' WHERE id = ?`, cidOf(t, pre))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.AddDependencies(context.Background(), ChannelCLI, b, []string{pre}); err != nil {
		t.Fatal(err)
	}
	// b は other に譲って後回しにした記録（other は着手中で未完了）。b の前提 pre は完了している。
	insertConflictForTest(t, f.s, cidOf(t, b), cidOf(t, other), true)
	res, err := f.s.RunDelegation(context.Background(), f.in(f.cycle(t, 300), &b))
	if err != nil {
		t.Fatalf("RunDelegation(%s): %v", b, err)
	}
	if len(res.Items) != 1 || res.Items[0].ChallengeID != b || len(res.NotStarted) != 0 {
		t.Fatalf("result = items %+v, not started %+v; want %s delegated", res.Items, res.NotStarted, b)
	}
}

// 後回しにした課題を委譲すると、その課題を後回しの側とする向きのある組は外れる（待っている理由に残さない）。
func TestConflict_LaunchClearsTheDirectedPairsOfTheDeferredChallenge(t *testing.T) {
	t.Parallel()
	f := newPlanFixture(t)
	a := f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	insertConflictForTest(t, f.s, cidOf(t, a), cidOf(t, b), true)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		return orientConflictsTx(context.Background(), tx, cidOf(t, a))
	}); err != nil {
		t.Fatal(err)
	}
	if got := conflictRowsForTest(t, f.s); len(got) != 0 {
		t.Fatalf("rows = %+v, want none", got)
	}
}
