package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// depFixture は n 件の課題（C-1〜C-n）を持つ実ストアを作る。
func depFixture(t *testing.T, n int) *Store {
	t.Helper()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	for i := 0; i < n; i++ {
		if _, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"}); err != nil {
			t.Fatalf("CreateChallenge: %v", err)
		}
	}
	return s
}

func depEdges(t *testing.T, s *Store) map[int64][]int64 {
	t.Helper()
	out := map[int64][]int64{}
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT challenge_id, depends_on_id FROM challenge_dependency ORDER BY challenge_id, depends_on_id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var a, b int64
			if err := rows.Scan(&a, &b); err != nil {
				return err
			}
			out[a] = append(out[a], b)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("read edges: %v", err)
	}
	return out
}

func depActivities(t *testing.T, s *Store, id string) []Activity {
	t.Helper()
	got, err := s.ListActivities(context.Background(), &id)
	if err != nil {
		t.Fatalf("ListActivities: %v", err)
	}
	return got
}

func insertDepEdge(t *testing.T, s *Store, from, to int64) {
	t.Helper()
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO challenge_dependency (challenge_id, depends_on_id, created_at) VALUES (?, ?, '2026-10-24T00:00:00.000Z')`, from, to)
		return err
	}); err != nil {
		t.Fatalf("insert edge: %v", err)
	}
}

func TestAddDependencies_CreatesRowsBumpsVersionOnceAndLogs(t *testing.T) {
	s := depFixture(t, 3)
	c, err := s.AddDependencies(context.Background(), ChannelCLI, "C-1", []string{"C-3", "C-2"})
	if err != nil {
		t.Fatalf("AddDependencies: %v", err)
	}
	if c.Version != 2 {
		t.Errorf("Version = %d, want 2 (one bump for two targets)", c.Version)
	}
	if got := depEdges(t, s); !reflect.DeepEqual(got, map[int64][]int64{1: {2, 3}}) {
		t.Errorf("edges = %v", got)
	}
	acts := depActivities(t, s, "C-1")
	last := acts[len(acts)-1]
	if last.Entity != "challenge" || last.Action != "dep_add" || last.Channel != "cli" {
		t.Fatalf("last activity = %+v", last)
	}
	var after struct {
		DependsOn []string `json:"depends_on"`
		Version   int      `json:"version"`
	}
	if err := json.Unmarshal(last.After, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.DependsOn, []string{"C-2", "C-3"}) || after.Version != 2 {
		t.Errorf("after = %+v", after)
	}
}

func TestRemoveDependencies_DeletesRowsBumpsVersionOnceAndLogs(t *testing.T) {
	s := depFixture(t, 3)
	if _, err := s.AddDependencies(context.Background(), ChannelCLI, "C-1", []string{"C-2", "C-3"}); err != nil {
		t.Fatal(err)
	}
	c, err := s.RemoveDependencies(context.Background(), ChannelCLI, "C-1", []string{"C-2", "C-3"})
	if err != nil {
		t.Fatalf("RemoveDependencies: %v", err)
	}
	if c.Version != 3 {
		t.Errorf("Version = %d, want 3", c.Version)
	}
	if got := depEdges(t, s); len(got) != 0 {
		t.Errorf("edges = %v, want none", got)
	}
	acts := depActivities(t, s, "C-1")
	if last := acts[len(acts)-1]; last.Action != "dep_rm" || last.Entity != "challenge" {
		t.Errorf("last activity = %+v", last)
	}
}

// 作業ログの after.depends_on には、実際に足した・外した依存先だけが載る（既にある・もとから無い依存先は載らない）。
func TestChangeDependencies_LogsOnlyChangedTargetsWhenMixed(t *testing.T) {
	s := depFixture(t, 3)
	ctx := context.Background()
	if _, err := s.AddDependencies(ctx, ChannelCLI, "C-1", []string{"C-2"}); err != nil {
		t.Fatal(err)
	}
	lastDependsOn := func(action string) []string {
		t.Helper()
		acts := depActivities(t, s, "C-1")
		last := acts[len(acts)-1]
		if last.Action != action {
			t.Fatalf("last activity = %+v, want %s", last, action)
		}
		var after struct {
			DependsOn []string `json:"depends_on"`
		}
		if err := json.Unmarshal(last.After, &after); err != nil {
			t.Fatal(err)
		}
		return after.DependsOn
	}

	// C-2 は既にある。足されるのは C-3 だけ。
	if _, err := s.AddDependencies(ctx, ChannelCLI, "C-1", []string{"C-2", "C-3"}); err != nil {
		t.Fatalf("AddDependencies: %v", err)
	}
	if got := lastDependsOn("dep_add"); !reflect.DeepEqual(got, []string{"C-3"}) {
		t.Errorf("dep_add after.depends_on = %v, want [C-3]", got)
	}

	// C-2 を外したあと C-2・C-3 を外す。もとから無い C-2 は載らず、外れるのは C-3 だけ。
	if _, err := s.RemoveDependencies(ctx, ChannelCLI, "C-1", []string{"C-2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RemoveDependencies(ctx, ChannelCLI, "C-1", []string{"C-2", "C-3"}); err != nil {
		t.Fatalf("RemoveDependencies: %v", err)
	}
	if got := lastDependsOn("dep_rm"); !reflect.DeepEqual(got, []string{"C-3"}) {
		t.Errorf("dep_rm after.depends_on = %v, want [C-3]", got)
	}
}

func TestChangeDependencies_IdempotentWritesNothing(t *testing.T) {
	s := depFixture(t, 2)
	if _, err := s.AddDependencies(context.Background(), ChannelCLI, "C-1", []string{"C-2"}); err != nil {
		t.Fatal(err)
	}
	n := len(depActivities(t, s, "C-1"))
	c, err := s.AddDependencies(context.Background(), ChannelCLI, "C-1", []string{"C-2"})
	if err != nil || c.Version != 2 {
		t.Fatalf("re-add: version=%v err=%v", c, err)
	}
	if _, err := s.RemoveDependencies(context.Background(), ChannelCLI, "C-2", []string{"C-1"}); err != nil {
		t.Fatalf("rm of a missing edge: %v", err)
	}
	c2, _ := s.GetChallenge(context.Background(), "C-2")
	if c2.Version != 1 {
		t.Errorf("C-2 version = %d, want 1", c2.Version)
	}
	if got := len(depActivities(t, s, "C-2")); got != 1 {
		t.Errorf("C-2 activities = %d, want only its create entry", got)
	}
	if got := len(depActivities(t, s, "C-1")); got != n {
		t.Errorf("activities %d -> %d, want unchanged", n, got)
	}
}

func TestChangeDependencies_RejectionsWriteNothing(t *testing.T) {
	s := depFixture(t, 4)
	ctx := context.Background()
	if _, err := s.AddDependencies(ctx, ChannelCLI, "C-1", []string{"C-2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddDependencies(ctx, ChannelCLI, "C-2", []string{"C-3"}); err != nil {
		t.Fatal(err)
	}
	setChallengeStatus(t, s, 4, StatusDone)
	setChallengeStatus(t, s, 3, StatusDone) // 完了の課題を含む輪

	cases := []struct {
		name string
		add  bool
		id   string
		deps []string
		want error
	}{
		{"self add", true, "C-1", []string{"C-1"}, ErrSelfDependency},
		{"self rm", false, "C-1", []string{"C-1"}, ErrSelfDependency},
		{"done self comes first", true, "C-4", []string{"C-4"}, ErrSelfDependency},
		{"self before not found", true, "C-1", []string{"C-99", "C-1"}, ErrSelfDependency},
		{"missing target add", true, "C-1", []string{"C-99"}, ErrNotFound},
		{"missing target rm", false, "C-1", []string{"C-99"}, ErrNotFound},
		{"missing source add", true, "C-99", []string{"C-1"}, ErrNotFound},
		{"missing source rm", false, "C-99", []string{"C-1"}, ErrNotFound},
		{"malformed id", true, "x", []string{"C-1"}, ErrNotFound},
		{"missing before terminal", true, "C-4", []string{"C-99"}, ErrNotFound},
		{"terminal add", true, "C-4", []string{"C-1"}, ErrTerminalState},
		{"terminal rm", false, "C-4", []string{"C-1"}, ErrTerminalState},
		{"terminal before cycle", true, "C-3", []string{"C-1"}, ErrTerminalState},
		{"cycle of two", true, "C-2", []string{"C-1"}, ErrDependencyCycle},
		{"empty targets", true, "C-1", nil, ErrValidation},
	}
	before := depEdges(t, s)
	countAll := func() (n int) {
		for _, id := range []string{"C-1", "C-2", "C-3", "C-4"} {
			n += len(depActivities(t, s, id))
		}
		return n
	}
	activitiesBefore := countAll()
	for _, c := range cases {
		var err error
		if c.add {
			_, err = s.AddDependencies(ctx, ChannelCLI, c.id, c.deps)
		} else {
			_, err = s.RemoveDependencies(ctx, ChannelCLI, c.id, c.deps)
		}
		if !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if got := depEdges(t, s); !reflect.DeepEqual(got, before) {
		t.Errorf("edges changed: %v -> %v", before, got)
	}
	if got := countAll(); got != activitiesBefore {
		t.Errorf("activities %d -> %d, want unchanged by rejections", activitiesBefore, got)
	}
	for id, want := range map[string]int{"C-2": 2, "C-3": 1, "C-4": 1} {
		if c, _ := s.GetChallenge(ctx, id); c.Version != want {
			t.Errorf("%s version = %d, want %d", id, c.Version, want)
		}
	}
	c1, _ := s.GetChallenge(ctx, "C-1")
	if c1.Version != 2 {
		t.Errorf("C-1 version = %d, want 2", c1.Version)
	}
}

func TestAddDependencies_ThreeRingAndRingThroughDoneChallengeAreCycles(t *testing.T) {
	s := depFixture(t, 3)
	ctx := context.Background()
	for _, p := range [][2]string{{"C-1", "C-2"}, {"C-2", "C-3"}} {
		if _, err := s.AddDependencies(ctx, ChannelCLI, p[0], []string{p[1]}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AddDependencies(ctx, ChannelCLI, "C-3", []string{"C-1"}); !errors.Is(err, ErrDependencyCycle) {
		t.Errorf("3-ring: err = %v", err)
	}
	// 依存元が進行中で、輪の途中が完了でも、辺は状態によらず数える。
	setChallengeStatus(t, s, 2, StatusDone)
	if _, err := s.AddDependencies(ctx, ChannelCLI, "C-3", []string{"C-1"}); !errors.Is(err, ErrDependencyCycle) {
		t.Errorf("ring via done: err = %v", err)
	}
}

func TestAddDependencies_AnyRejectedTargetAddsNone(t *testing.T) {
	s := depFixture(t, 3)
	ctx := context.Background()
	if _, err := s.AddDependencies(ctx, ChannelCLI, "C-2", []string{"C-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddDependencies(ctx, ChannelCLI, "C-1", []string{"C-3", "C-2"}); !errors.Is(err, ErrDependencyCycle) {
		t.Fatalf("err = %v", err)
	}
	if got := depEdges(t, s); !reflect.DeepEqual(got, map[int64][]int64{2: {1}}) {
		t.Errorf("edges = %v", got)
	}
}

func TestAddDependencies_ActivityFailureRollsBack(t *testing.T) {
	s := depFixture(t, 2)
	s.insertActivity = func(_ *sql.Tx, _ activityRow) error { return errors.New("boom") }
	if _, err := s.AddDependencies(context.Background(), ChannelCLI, "C-1", []string{"C-2"}); err == nil {
		t.Fatal("want error")
	}
	if got := depEdges(t, s); len(got) != 0 {
		t.Errorf("edges = %v, want rolled back", got)
	}
	c, _ := s.GetChallenge(context.Background(), "C-1")
	if c.Version != 1 {
		t.Errorf("version = %d", c.Version)
	}
}

func evalDeps(t *testing.T, s *Store) *dependencyEvaluation {
	t.Helper()
	var ev *dependencyEvaluation
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		ev, err = evaluateDependencies(context.Background(), tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return ev
}

func setReleaseOp(t *testing.T, s *Store, cid int64, state string) {
	t.Helper()
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO operation (challenge_id, kind, summary, state, created_at) VALUES (?, 'release', 's', ?, '2026-10-24T00:00:00.000Z')`, cid, state)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluateDependencies_MetOnlyWhenDoneAndNoOpenRelease(t *testing.T) {
	s := depFixture(t, 4)
	ctx := context.Background()
	if _, err := s.AddDependencies(ctx, ChannelCLI, "C-1", []string{"C-2", "C-3", "C-4"}); err != nil {
		t.Fatal(err)
	}
	// C-2: 未完了。C-3: 完了だが承認済みで未実行の release あり。C-4: 完了で release は実行済み。
	setChallengeStatus(t, s, 3, StatusDone)
	setChallengeStatus(t, s, 4, StatusDone)
	setReleaseOp(t, s, 3, "approved")
	setReleaseOp(t, s, 4, "executed")
	ev := evalDeps(t, s)
	want := []dependencyStatus{{2, false}, {3, false}, {4, true}}
	if !reflect.DeepEqual(ev.Dependencies[1], want) {
		t.Errorf("deps = %+v, want %+v", ev.Dependencies[1], want)
	}
	if len(ev.Cyclic) != 0 {
		t.Errorf("cyclic = %v", ev.Cyclic)
	}
	// pending の release も満たさない。
	setReleaseOp(t, s, 4, "pending")
	if ev := evalDeps(t, s); ev.Dependencies[1][2].Met {
		t.Error("pending release must keep the dependency unmet")
	}
	// 課題ごとに並ぶのは依存元だけ。依存の無い課題は載らない。
	if _, ok := ev.Dependencies[2]; ok {
		t.Error("C-2 has no dependencies")
	}
}

func TestEvaluateDependencies_CyclicTargetIsUnmetEvenWhenDoneAndReleased(t *testing.T) {
	s := depFixture(t, 4)
	insertDepEdge(t, s, 1, 2)
	insertDepEdge(t, s, 2, 3)
	insertDepEdge(t, s, 3, 1)
	insertDepEdge(t, s, 4, 1) // 4 は輪に含まれない（輪の課題に依存するだけ）
	for _, id := range []int64{1, 2, 3} {
		setChallengeStatus(t, s, id, StatusDone)
	}
	ev := evalDeps(t, s)
	if !reflect.DeepEqual(ev.Cyclic, map[int64]bool{1: true, 2: true, 3: true}) {
		t.Errorf("cyclic = %v", ev.Cyclic)
	}
	if d := ev.Dependencies[4]; len(d) != 1 || d[0].Met {
		t.Errorf("C-4 deps = %+v, want C-1 unmet (cyclic)", d)
	}
	for _, id := range []int64{1, 2, 3} {
		for _, d := range ev.Dependencies[id] {
			if d.Met {
				t.Errorf("C-%d -> C-%d met inside a cycle", id, d.DependsOn)
			}
		}
	}
}

// 完了で release を持たない依存先と、release が rejected だけの依存先は満たされている
// （未実行と数えるのは pending・approved だけ）。
func TestEvaluateDependencies_MetWhenDoneWithoutReleaseOrOnlyRejectedRelease(t *testing.T) {
	s := depFixture(t, 3)
	if _, err := s.AddDependencies(context.Background(), ChannelCLI, "C-1", []string{"C-2", "C-3"}); err != nil {
		t.Fatal(err)
	}
	setChallengeStatus(t, s, 2, StatusDone)
	setChallengeStatus(t, s, 3, StatusDone)
	setReleaseOp(t, s, 3, "rejected")
	ev := evalDeps(t, s)
	want := []dependencyStatus{{2, true}, {3, true}}
	if !reflect.DeepEqual(ev.Dependencies[1], want) {
		t.Errorf("deps = %+v, want %+v", ev.Dependencies[1], want)
	}
}
