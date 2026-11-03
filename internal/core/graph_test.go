package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// docs/features/ui-relation-graph.md §関係グラフの API。

var graphNow = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

func graphStore(t *testing.T, statuses ...Status) (*Store, []Challenge) {
	t.Helper()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	var cs []Challenge
	for _, st := range statuses {
		cs = append(cs, cardChallenge(t, s, st))
	}
	return s, cs
}

func mustGraph(t *testing.T, s *Store) *ChallengeGraph {
	t.Helper()
	g, err := s.ChallengeGraph(context.Background(), graphNow)
	if err != nil {
		t.Fatalf("ChallengeGraph: %v", err)
	}
	return g
}

func nodeByID(t *testing.T, g *ChallengeGraph, id string) GraphNode {
	t.Helper()
	for _, n := range g.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("node %s not found in %+v", id, g.Nodes)
	return GraphNode{}
}

func TestChallengeGraph_EmptyStoreHasEmptyNonNilLists(t *testing.T) {
	s, _ := graphStore(t)
	g := mustGraph(t, s)
	if g.Nodes == nil || g.Edges == nil || len(g.Nodes) != 0 || len(g.Edges) != 0 {
		t.Errorf("graph = %+v, want empty non-nil lists", g)
	}
}

func TestChallengeGraph_NodesAreInNumericIDOrderWithTheirFields(t *testing.T) {
	statuses := make([]Status, 11)
	for i := range statuses {
		statuses[i] = StatusClassified
	}
	s, _ := graphStore(t, statuses...)
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET headline = 'いまの一言', context = '位置づけ' WHERE id = 2`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	g := mustGraph(t, s)
	var ids []string
	for _, n := range g.Nodes {
		ids = append(ids, n.ID)
	}
	want := []string{"C-1", "C-2", "C-3", "C-4", "C-5", "C-6", "C-7", "C-8", "C-9", "C-10", "C-11"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("node order = %v, want %v", ids, want)
	}
	n2 := nodeByID(t, g, "C-2")
	if n2.Title != "t" || n2.Headline == nil || *n2.Headline != "いまの一言" || n2.Context == nil || *n2.Context != "位置づけ" || n2.ParentID != nil {
		t.Errorf("C-2 = %+v", n2)
	}
	if n1 := nodeByID(t, g, "C-1"); n1.Headline != nil || n1.Context != nil {
		t.Errorf("C-1 = %+v, want nil headline/context", n1)
	}
}

// 取り下げた課題は、システムが進めている課題ではなく取り下げの区分になる。人間の操作待ちが残っていれば人間が先。
func TestChallengeGraph_WithdrawnIsItsOwnCategory(t *testing.T) {
	s, _ := graphStore(t, StatusWithdrawn, StatusWithdrawn, StatusDone)
	// C-2 は取り下げ済みだが、承認待ちの本番反映（人間の操作待ち）を持つ。
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO operation (challenge_id, kind, summary, ref, state, version, created_at)
			VALUES (2, 'release', 's', 'o/r#2', 'pending', 1, '2026-11-01T00:00:00.000Z')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	g := mustGraph(t, s)
	want := map[string]GraphCategory{"C-1": GraphCategoryWithdrawn, "C-2": GraphCategoryHuman, "C-3": GraphCategoryDone}
	for id, c := range want {
		if got := nodeByID(t, g, id).Category; got != c {
			t.Errorf("%s category = %q, want %q", id, got, c)
		}
	}
}

func TestGraphCategoryValues_IsTheFiveCategoriesInOrder(t *testing.T) {
	want := []GraphCategory{GraphCategoryHuman, GraphCategoryExternal, GraphCategoryDone, GraphCategoryWithdrawn, GraphCategorySystem}
	if got := GraphCategoryValues(); !reflect.DeepEqual(got, want) {
		t.Errorf("GraphCategoryValues = %v, want %v", got, want)
	}
}

// 区分は 人間 > 外部 > 完了 > システム の順で、カードの next_human_actions と状態から決まる。
func TestChallengeGraph_CategoryFollowsTheCardDerivation(t *testing.T) {
	s, cs := graphStore(t, StatusClassified, StatusAwaitingPlanApproval, StatusDone, StatusDone, StatusInProgress)
	// C-4 は完了だが、承認待ちの本番反映（人間の操作待ち）を持つ。
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO operation (challenge_id, kind, summary, ref, state, version, created_at)
			VALUES (4, 'release', 's', 'o/r#2', 'pending', 1, '2026-11-01T00:00:00.000Z')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_ = cs
	g := mustGraph(t, s)
	want := map[string]GraphCategory{"C-1": GraphCategorySystem, "C-2": GraphCategoryHuman, "C-3": GraphCategoryDone, "C-4": GraphCategoryHuman, "C-5": GraphCategorySystem}
	for id, c := range want {
		if got := nodeByID(t, g, id).Category; got != c {
			t.Errorf("%s category = %q, want %q", id, got, c)
		}
	}
}

func TestChallengeGraph_RunningAndStaleFollowTheHeartbeat(t *testing.T) {
	s, cs := graphStore(t, StatusInProgress, StatusInProgress, StatusInProgress)
	cardInsertRun(t, s, cs[0], graphNow.Add(-10*time.Second), nil) // 動いている
	cardInsertRun(t, s, cs[1], graphNow.Add(-time.Hour), nil)      // 記録が途絶えている
	g := mustGraph(t, s)
	for id, want := range map[string][2]bool{"C-1": {true, false}, "C-2": {true, true}, "C-3": {false, false}} {
		n := nodeByID(t, g, id)
		if n.Running != want[0] || n.Stale != want[1] {
			t.Errorf("%s running/stale = %v/%v, want %v/%v", id, n.Running, n.Stale, want[0], want[1])
		}
		if n.Category != GraphCategorySystem {
			t.Errorf("%s category = %q, want system (running は区分に効かない)", id, n.Category)
		}
	}
}

func TestChallengeGraph_ParentIDsAreReturned(t *testing.T) {
	s, _ := graphStore(t, StatusClassified, StatusClassified, StatusClassified)
	ctx := context.Background()
	if err := s.SetParent(ctx, ChannelCLI, "C-2", "C-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetParent(ctx, ChannelCLI, "C-3", "C-2"); err != nil {
		t.Fatal(err)
	}
	g := mustGraph(t, s)
	got := map[string]string{}
	for _, n := range g.Nodes {
		if n.ParentID != nil {
			got[n.ID] = *n.ParentID
		}
	}
	if !reflect.DeepEqual(got, map[string]string{"C-2": "C-1", "C-3": "C-2"}) {
		t.Errorf("parents = %v", got)
	}
}

func TestChallengeGraph_EdgesCarryKindSatisfactionAndRegistration(t *testing.T) {
	s, _ := graphStore(t, StatusDone, StatusClassified, StatusClassified, StatusClassified)
	ctx := context.Background()
	if _, err := s.AddDependencies(ctx, ChannelCLI, "C-3", []string{"C-2"}); err != nil { // manual。先行は未完了
		t.Fatal(err)
	}
	if _, err := s.AddDependencies(ctx, ChannelInvoker, "C-2", []string{"C-1"}); err != nil { // plan。先行は完了
		t.Fatal(err)
	}
	insertDepEdge(t, s, 4, 1) // 作業ログの無い登録は unknown
	g := mustGraph(t, s)

	if len(g.Edges) != 3 {
		t.Fatalf("edges = %+v", g.Edges)
	}
	type key struct{ from, to string }
	got := map[key]GraphEdge{}
	var order []key
	for _, e := range g.Edges {
		got[key{e.From, e.To}] = e
		order = append(order, key{e.From, e.To})
	}
	// from = 先行（依存先）・to = 後続（依存元）。(from, to) の数の昇順。
	if want := []key{{"C-1", "C-2"}, {"C-1", "C-4"}, {"C-2", "C-3"}}; !reflect.DeepEqual(order, want) {
		t.Fatalf("edge order = %v, want %v", order, want)
	}
	cases := []struct {
		k   key
		sat bool
		via GraphRegisteredVia
	}{
		{key{"C-1", "C-2"}, true, GraphRegisteredViaPlan},
		{key{"C-2", "C-3"}, false, GraphRegisteredViaManual},
		{key{"C-1", "C-4"}, true, GraphRegisteredViaUnknown},
	}
	for _, c := range cases {
		e := got[c.k]
		if e.Kind != GraphEdgePrerequisite || e.Satisfied != c.sat || e.RegisteredVia != c.via {
			t.Errorf("edge %v = %+v, want satisfied=%v via=%q kind=prerequisite", c.k, e, c.sat, c.via)
		}
		if e.RegisteredAt == "" {
			t.Errorf("edge %v has no registered_at", c.k)
		}
	}
	if got[key{"C-1", "C-4"}].RegisteredAt != "2026-10-24T00:00:00.000Z" {
		t.Errorf("registered_at = %q, want challenge_dependency.created_at", got[key{"C-1", "C-4"}].RegisteredAt)
	}
}

// 満たされているかは依存の評価（完了で、開いた本番反映が無い）と同じ規則。
func TestChallengeGraph_SatisfiedIsFalseWhileTheDependencyHasAnOpenRelease(t *testing.T) {
	s, _ := graphStore(t, StatusDone, StatusClassified)
	insertDepEdge(t, s, 2, 1)
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO operation (challenge_id, kind, summary, ref, state, version, created_at)
			VALUES (1, 'release', 's', 'o/r#2', 'approved', 1, '2026-11-01T00:00:00.000Z')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	g := mustGraph(t, s)
	if len(g.Edges) != 1 || g.Edges[0].Satisfied {
		t.Errorf("edges = %+v, want one unsatisfied edge", g.Edges)
	}
}

// 依存の循環がストアにあっても失敗にしない。辺はそのまま返す。
func TestChallengeGraph_ReturnsEdgesOfADependencyCycle(t *testing.T) {
	s, _ := graphStore(t, StatusDone, StatusDone)
	insertDepEdge(t, s, 1, 2)
	insertDepEdge(t, s, 2, 1)
	g := mustGraph(t, s)
	if len(g.Edges) != 2 {
		t.Fatalf("edges = %+v", g.Edges)
	}
	for _, e := range g.Edges {
		if e.Satisfied {
			t.Errorf("edge %+v is satisfied although it is on a cycle", e)
		}
	}
}

// 同じストアと同じ現在時刻からは、バイト列として同じ結果になる。ストアは書かない。
func TestChallengeGraph_IsDeterministicAndReadOnly(t *testing.T) {
	s, cs := graphStore(t, StatusInProgress, StatusAwaitingPlanApproval, StatusDone)
	ctx := context.Background()
	cardInsertRun(t, s, cs[0], graphNow.Add(-time.Hour), nil)
	if _, err := s.AddDependencies(ctx, ChannelInvoker, "C-2", []string{"C-1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetParent(ctx, ChannelCLI, "C-3", "C-1"); err != nil {
		t.Fatal(err)
	}
	before := dumpTablesForGraph(t, s)
	a, err := json.Marshal(mustGraph(t, s))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(mustGraph(t, s))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Errorf("two calls differ:\n%s\n%s", a, b)
	}
	if after := dumpTablesForGraph(t, s); after != before {
		t.Errorf("ChallengeGraph changed the store")
	}
}

func dumpTablesForGraph(t *testing.T, s *Store) string {
	t.Helper()
	data, _, err := s.Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
