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

func TestGraphCategoryValues_IsTheSixCategoriesInOrder(t *testing.T) {
	want := []GraphCategory{GraphCategoryHuman, GraphCategoryExternal, GraphCategoryParentAgent, GraphCategoryDone, GraphCategoryWithdrawn, GraphCategorySystem}
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

// 状態と人間の操作待ち（何をするか）は、カードの導出（state・next_human_actions）をそのまま持つ。
// 人間の操作待ちが無い課題は空の並び（nil ではない）。
func TestChallengeGraph_NodeCarriesTheCardStateAndNextHumanActions(t *testing.T) {
	s, _ := graphStore(t, StatusClassified, StatusAwaitingPlanApproval, StatusInProgress, StatusDone)
	// C-4 は完了だが、承認待ちの本番反映（approve_operation）を持つ。
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO operation (challenge_id, kind, summary, ref, state, version, created_at)
			VALUES (4, 'release', 's', 'o/r#2', 'pending', 1, '2026-11-01T00:00:00.000Z')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	g := mustGraph(t, s)
	want := map[string]struct {
		state   Status
		actions []CardAction
	}{
		"C-1": {StatusClassified, []CardAction{}},
		"C-2": {StatusAwaitingPlanApproval, []CardAction{CardActionApprovePlan}},
		"C-3": {StatusInProgress, []CardAction{}},
		"C-4": {StatusDone, []CardAction{CardActionApproveOperation}},
	}
	for id, w := range want {
		n := nodeByID(t, g, id)
		if n.State != w.state || n.NextHumanActions == nil || !reflect.DeepEqual(n.NextHumanActions, w.actions) {
			t.Errorf("%s state/actions = %q/%#v, want %q/%#v", id, n.State, n.NextHumanActions, w.state, w.actions)
		}
	}
}

func graphPriority(p Priority) *Priority { return &p }

// 優先度は課題の優先度（カード／一覧と同じ値）。付いていなければ nil。
func TestChallengeGraph_NodeCarriesThePriority(t *testing.T) {
	s, _ := graphStore(t, StatusClassified, StatusClassified, StatusClassified)
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET priority = CASE id WHEN 1 THEN 'P0' WHEN 2 THEN 'P2' END WHERE id IN (1, 2)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	g := mustGraph(t, s)
	want := map[string]*Priority{"C-1": graphPriority(PriorityP0), "C-2": graphPriority(PriorityP2), "C-3": nil}
	for id, w := range want {
		got := nodeByID(t, g, id).Priority
		if (got == nil) != (w == nil) || (got != nil && *got != *w) {
			t.Errorf("%s priority = %v, want %v", id, got, w)
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

func insertConflictForTest(t *testing.T, s *Store, challenge, with int64, directed bool) {
	t.Helper()
	d := 0
	if directed {
		d = 1
	}
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO challenge_conflict (challenge_id, conflicts_with_id, directed, created_at) VALUES (?, ?, ?, '2026-11-05T01:02:03.000Z')`,
			challenge, with, d)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// 衝突は kind=conflict の辺で返す。向きのある組は from＝後回しにした課題・to＝先に委譲した課題（後回し → 先に委譲）で
// directed=true、向きの無い組は directed=false（from は ID の小さい側）。satisfied は常に false、
// registered_via は prediction、registered_at は challenge_conflict.created_at。前提の辺は directed=true。
func TestChallengeGraph_ConflictEdgesCarryDirectionAndPredictionRegistration(t *testing.T) {
	s, _ := graphStore(t, StatusInProgress, StatusClassified, StatusClassified, StatusClassified)
	insertDepEdge(t, s, 4, 1)
	insertConflictForTest(t, s, 2, 1, true)  // C-2 を後回しにして C-1 を先に委譲した
	insertConflictForTest(t, s, 3, 4, false) // どちらもまだ委譲していない
	g := mustGraph(t, s)

	type key struct {
		from, to string
		kind     GraphEdgeKind
	}
	var got []GraphEdge
	var order []key
	for _, e := range g.Edges {
		got = append(got, e)
		order = append(order, key{e.From, e.To, e.Kind})
	}
	want := []key{
		{"C-1", "C-4", GraphEdgePrerequisite},
		{"C-2", "C-1", GraphEdgeConflict},
		{"C-3", "C-4", GraphEdgeConflict},
	}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("edges = %+v, want order %v", g.Edges, want)
	}
	if !got[0].Directed {
		t.Errorf("prerequisite edge = %+v, want directed", got[0])
	}
	directed := got[1]
	if !directed.Directed || directed.Satisfied || directed.RegisteredVia != GraphRegisteredViaPrediction || directed.RegisteredAt != "2026-11-05T01:02:03.000Z" {
		t.Errorf("directed conflict edge = %+v", directed)
	}
	undirected := got[2]
	if undirected.Directed || undirected.Satisfied || undirected.RegisteredVia != GraphRegisteredViaPrediction {
		t.Errorf("undirected conflict edge = %+v", undirected)
	}
}

// 片方が完了・取り下げになった衝突は返さない（行は残る）。
func TestChallengeGraph_ConflictEdgesWithAFinishedChallengeAreNotReturned(t *testing.T) {
	s, _ := graphStore(t, StatusDone, StatusClassified, StatusWithdrawn, StatusClassified, StatusClassified)
	insertConflictForTest(t, s, 2, 1, true)  // 先に委譲した側が完了
	insertConflictForTest(t, s, 3, 4, true)  // 後回しにした側が取り下げ
	insertConflictForTest(t, s, 2, 5, false) // 両方とも終わっていない
	g := mustGraph(t, s)
	if len(g.Edges) != 1 || g.Edges[0].From != "C-2" || g.Edges[0].To != "C-5" || g.Edges[0].Directed {
		t.Fatalf("edges = %+v, want only the unfinished pair C-2/C-5", g.Edges)
	}
	if n := len(conflictRowsForTest(t, s)); n != 3 {
		t.Errorf("conflict rows = %d, want all 3 kept", n)
	}
}

// 衝突の行は前提の依存の辺（satisfied・登録の経路）に影響しない。
func TestChallengeGraph_ConflictRowsDoNotChangePrerequisiteEdges(t *testing.T) {
	s, _ := graphStore(t, StatusInProgress, StatusClassified)
	insertDepEdge(t, s, 2, 1)
	before := mustGraph(t, s)
	insertConflictForTest(t, s, 2, 1, true)
	after := mustGraph(t, s)
	if len(before.Edges) != 1 || len(after.Edges) != 2 {
		t.Fatalf("edges before %+v, after %+v", before.Edges, after.Edges)
	}
	var prereq []GraphEdge
	for _, e := range after.Edges {
		if e.Kind == GraphEdgePrerequisite {
			prereq = append(prereq, e)
		}
	}
	if !reflect.DeepEqual(prereq, before.Edges) {
		t.Errorf("prerequisite edges changed: before %+v, after %+v", before.Edges, prereq)
	}
}
