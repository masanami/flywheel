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

func (f *planFixture) planOnly(t *testing.T, ids ...string) {
	t.Helper()
	var targets []int64
	for _, id := range ids {
		targets = append(targets, cidOf(t, id))
	}
	if _, err := f.s.planDelegation(context.Background(), f.in(f.cycle(t, 300), nil), targets); err != nil {
		t.Fatalf("planDelegation: %v", err)
	}
}

// どちらもまだ委譲していない組は、向きの無い関係として（ID の小さい側を challenge_id にして）記録する。
// 予測した周と、予測の口を呼んだ run を持つ。
func TestConflict_PlanRecordsNeitherDelegatedPairAsUndirected(t *testing.T) {
	f := newPlanFixture(t)
	a := f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		return predictionFor(in, nil, pairOf(12, 11, shared("a.go")))
	}
	f.planOnly(t, a, b)
	want := []conflictRowForTest{{Challenge: cidOf(t, a), With: cidOf(t, b), Directed: false, HasCycle: true, HasRun: true}}
	if got := conflictRowsForTest(t, f.s); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
}

// 組の理由が shared_files・dependency・unknown_pair のどれでも、同時に委譲しない組として記録する。
// 共有ファイルが merge_friendly・ignored だけの組（直列化されない）は記録しない。
func TestConflict_PlanRecordsEverySerializingReasonOnly(t *testing.T) {
	f := newPlanFixture(t)
	a := f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	c := f.candidate(t, "multi", "P1", 13)
	d := f.candidate(t, "multi", "P1", 14)
	first := 11
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		return predictionFor(in, nil,
			pairOf(11, 12, shared("a.go")),
			PredictedPair{Issues: [2]int{12, 13}, Status: predictPairUnknown},
			PredictedPair{Issues: [2]int{13, 14}, Status: predictPairPredicted, DependencyFirst: &first},
			PredictedPair{Issues: [2]int{11, 14}, Status: predictPairPredicted, SharedFiles: []PredictedSharedFile{{Path: "m.md", MergeFriendly: true}, {Path: "i", Ignored: true}}},
		)
	}
	f.planOnly(t, a, b, c, d)
	got := conflictRowsForTest(t, f.s)
	want := [][2]int64{{cidOf(t, a), cidOf(t, b)}, {cidOf(t, b), cidOf(t, c)}, {cidOf(t, c), cidOf(t, d)}}
	var pairs [][2]int64
	for _, r := range got {
		pairs = append(pairs, [2]int64{r.Challenge, r.With})
	}
	if !reflect.DeepEqual(pairs, want) {
		t.Fatalf("pairs = %v, want %v (rows %+v)", pairs, want, got)
	}
}

// すでに委譲した（終了していない run を持つ）課題と同じ組になった後回しの課題は、
// 「後回しにした課題 → 先に委譲した課題」の向きで記録する。
func TestConflict_PlanRecordsCandidateAgainstRunningAsDirected(t *testing.T) {
	f := newPlanFixture(t)
	running := f.candidate(t, "multi", "P1", 5)
	deferred := f.candidate(t, "multi", "P1", 11)
	release := f.holdRunning(t, running)
	defer release()
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		return predictionFor(in, nil, pairOf(5, 11, shared("x.go")))
	}
	f.planOnly(t, deferred)
	want := []conflictRowForTest{{Challenge: cidOf(t, deferred), With: cidOf(t, running), Directed: true, HasCycle: true, HasRun: true}}
	if got := conflictRowsForTest(t, f.s); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
}

// 委譲を起動する（run を記録する）と、その課題との向きの無い組は「相手 → 起動した課題」の向きになる。
// 向きが決まるのは run の記録と同じトランザクションの中（委譲の最中に読んでも向きが付いている）。
func TestConflict_LaunchOrientsTheUndirectedPairsOfTheLaunchedChallenge(t *testing.T) {
	f := newPlanFixture(t)
	a := f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		return predictionFor(in, nil, pairOf(11, 12, shared("a.go")))
	}
	var during []conflictRowForTest
	prev := f.deleg.onInvoke
	f.deleg.onInvoke = func(in DelegateLaunchInput) {
		if in.SourceIssueNumber == 11 {
			during = conflictRowsForTest(t, f.s)
		}
		prev(in)
	}
	f.mustRunPlan(t)
	want := []conflictRowForTest{{Challenge: cidOf(t, b), With: cidOf(t, a), Directed: true, HasCycle: true, HasRun: true}}
	if !reflect.DeepEqual(during, want) {
		t.Fatalf("rows during the first delegation = %+v, want %+v", during, want)
	}
	// 後回しにした b も後で委譲されるので、b を後回しの側とする組は外れる
	if got := conflictRowsForTest(t, f.s); len(got) != 0 {
		t.Fatalf("rows after = %+v, want none once b was delegated", got)
	}
}

// 向きが付いた組は、後で相手が起動しても反転しない。連鎖の途中の組（b, c）は b を起動した時点で c → b になる。
func TestConflict_ChainEndsWithEachLaterChallengeYieldingToItsEarlierOne(t *testing.T) {
	f := newPlanFixture(t)
	a := f.candidate(t, "multi", "P0", 11)
	b := f.candidate(t, "multi", "P1", 12)
	c := f.candidate(t, "multi", "P2", 13)
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		return predictionFor(in, nil, pairOf(11, 12, shared("x")), pairOf(12, 13, shared("x")))
	}
	during := map[int][]conflictRowForTest{}
	prev := f.deleg.onInvoke
	f.deleg.onInvoke = func(in DelegateLaunchInput) {
		during[in.SourceIssueNumber] = conflictRowsForTest(t, f.s)
		prev(in)
	}
	f.mustRunPlan(t)
	row := func(x, y string) conflictRowForTest {
		return conflictRowForTest{Challenge: cidOf(t, x), With: cidOf(t, y), Directed: true, HasCycle: true, HasRun: true}
	}
	// a の起動中は b→a と、向きの無い b-c。b の起動中は c→b だけ（b はもう後回しではない）。c の起動中は無し
	if want := []conflictRowForTest{row(b, a), {Challenge: cidOf(t, b), With: cidOf(t, c), HasCycle: true, HasRun: true}}; !reflect.DeepEqual(during[11], want) {
		t.Errorf("during a = %+v, want %+v", during[11], want)
	}
	if want := []conflictRowForTest{row(c, b)}; !reflect.DeepEqual(during[12], want) {
		t.Errorf("during b = %+v, want %+v", during[12], want)
	}
	if len(during[13]) != 0 {
		t.Errorf("during c = %+v, want none", during[13])
	}
}

// 予測のたびに、その予測の対象どうし（候補と実行中の課題）の組を最新の結果で置き換える。
// 新しい結果に無い組は消え、対象に入らなかった課題との組は残る。
func TestConflict_ANewPredictionReplacesThePairsAmongItsOwnChallenges(t *testing.T) {
	f := newPlanFixture(t)
	a := f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	c := f.candidate(t, "multi", "P1", 13)
	d := f.candidate(t, "multi", "P1", 14)
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		return predictionFor(in, nil, pairOf(11, 12, shared("x")), pairOf(13, 14, shared("y")))
	}
	f.planOnly(t, a, b, c, d)
	if got := len(conflictRowsForTest(t, f.s)); got != 2 {
		t.Fatalf("rows after the first prediction = %d, want 2", got)
	}
	// 2 回目は a・b・c だけを予測し、a と b の衝突は無くなり、b と c が新しく衝突する。
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		return predictionFor(in, nil, pairOf(12, 13, shared("z")))
	}
	f.planOnly(t, a, b, c)
	want := []conflictRowForTest{
		{Challenge: cidOf(t, b), With: cidOf(t, c), HasCycle: true, HasRun: true},
		{Challenge: cidOf(t, c), With: cidOf(t, d), HasCycle: true, HasRun: true},
	}
	if got := conflictRowsForTest(t, f.s); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows after the second prediction = %+v, want %+v (c-d is outside the second prediction and stays)", got, want)
	}
}

// 予測が得られなかったとき（prediction_failed など）は、既存の行を消さず、行も足さない。
func TestConflict_AFailedPredictionKeepsTheExistingRowsAndAddsNone(t *testing.T) {
	f := newPlanFixture(t)
	a := f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		return predictionFor(in, nil, pairOf(11, 12, shared("x")))
	}
	f.planOnly(t, a, b)
	before := conflictRowsForTest(t, f.s)
	if len(before) != 1 {
		t.Fatalf("rows = %+v, want 1", before)
	}
	f.pred.out = func(_ PredictLaunchInput) PredictLaunchOutput {
		return PredictLaunchOutput{Result: RunResultErrored, ErrorSummary: "boom"}
	}
	f.planOnly(t, a, b)
	if got := conflictRowsForTest(t, f.s); !reflect.DeepEqual(got, before) {
		t.Fatalf("rows after a failed prediction = %+v, want the unchanged %+v", got, before)
	}
}

// 予測の口を呼ばない周（1 課題だけ・予測できる Issue が無い）も、行を変えない。
func TestConflict_NoPredictionCallLeavesTheRowsAlone(t *testing.T) {
	f := newPlanFixture(t)
	a := f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		return predictionFor(in, nil, pairOf(11, 12, shared("x")))
	}
	f.planOnly(t, a, b)
	f.planOnly(t, a)
	if got := len(conflictRowsForTest(t, f.s)); got != 1 {
		t.Fatalf("rows = %d, want the 1 recorded earlier", got)
	}
}

// 衝突は着手の門にしない: 衝突の行があっても、前提の依存が満たされていれば依存の判定は通り、
// 依存の評価は衝突の行の有無で変わらない。
func TestConflict_RowsNeverEnterTheDependencyEvaluation(t *testing.T) {
	f := newPlanFixture(t)
	a := f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		return predictionFor(in, nil, pairOf(11, 12, shared("x")))
	}
	f.planOnly(t, a, b)
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
	f := newPlanFixture(t)
	a := f.candidate(t, "multi", "P1", 11)
	b := f.candidate(t, "multi", "P1", 12)
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput {
		return predictionFor(in, nil, pairOf(11, 12, shared("x")))
	}
	f.planOnly(t, a, b)
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
	f.pred.out = func(in PredictLaunchInput) PredictLaunchOutput { return predictionFor(in, nil) }
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
