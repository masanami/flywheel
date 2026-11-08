package core_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// RelationGraphSeedEnvVar は、関係グラフの本物の serve の E2E（web/e2e-real/。`make e2e-real`）の種の
// ストアを作るワークスペースのディレクトリ。渡されなければ t.TempDir() に作り、make check では種の中身の
// 検査だけをする（docs/features/ui-relation-graph.md §本物の serve の E2E）。
const RelationGraphSeedEnvVar = "FLYWHEEL_E2E_SEED_WORKSPACE"

// relationGraphSeed は種の課題の一覧。ID は作る順（C-1 から）で、web/e2e-real/relation-graph.spec.ts の
// 期待値はこの ID に依る。
//
//	C-1  人間の操作待ち（未回答の問い）。注目の起点。C-12（完了）の後続
//	C-2  計画待ち P2。C-1 の後続（依存で 1 段）
//	C-3  計画待ち P2。C-2 の後続（依存で 2 段）。C-14（取り下げ）の後続
//	C-4  着手中。注目の起点。C-5 の親
//	C-5  計画待ち P2。C-4 の子（親子で 1 段）。C-6 の親
//	C-6  分類待ち（優先度なし）。C-5 の子・C-4 の孫（親子で 2 段）
//	C-7  計画待ち P0。線なし（注目の起点）
//	C-8  分類待ち P0。線なし（注目の起点）
//	C-9  計画待ち P2。線なし（ほかの課題）
//	C-10 計画待ち P2。C-11 の先行（起点を含まないまとまり＝ほかの課題）
//	C-11 計画待ち P2。C-10 の後続
//	C-12 完了。C-1 の前提（灰色で残る）
//	C-13 完了。線なし（既定で隠れる）
//	C-14 取り下げ。C-3 の前提（取り下げの札つきで残る）
//	C-15 完了。C-16 の親（灰色で残る）
//	C-16 人間の操作待ち（不可逆操作の承認）。C-15 の子
//	C-17 取り下げ。線なし（既定で隠れる）
func TestRelationGraphSeed(t *testing.T) {
	dir := os.Getenv(RelationGraphSeedEnvVar)
	if dir == "" {
		dir = t.TempDir()
	} else if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		// 既存のストアに足すと ID がずれるので、空のディレクトリ（か存在しない場所）だけを受ける。
		t.Fatalf("%s=%s is not empty", RelationGraphSeedEnvVar, dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	st, err := core.OpenWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	titles := []string{
		"人間待ちの起点", "依存で 1 段先", "依存で 2 段先", "着手中の起点", "親子で 1 段先", "親子で 2 段先",
		"P0 の計画待ち", "P0 の分類待ち", "線の無い計画待ち", "ほかのまとまりの先行", "ほかのまとまりの後続",
		"完了した前提", "完了した孤立", "取り下げた前提", "完了した親", "完了した親の子で人間待ち", "取り下げた孤立",
	}
	for _, title := range titles {
		_, err := st.CreateChallenge(ctx, core.ChannelCLI, core.CreateInput{Title: title, DoneCriteria: "d"})
		must(err)
	}
	classify := map[string]string{
		"C-1": "P1", "C-2": "P2", "C-3": "P2", "C-4": "P2", "C-5": "P2", "C-7": "P0", "C-8": "P0",
		"C-9": "P2", "C-10": "P2", "C-11": "P2",
	}
	for _, id := range sortedKeys(classify) {
		_, err := st.ClassifyChallenge(ctx, core.ChannelCLI, id, core.ClassifyInput{Priority: classify[id]})
		must(err)
	}
	// 依存（後続 → 先行）
	for to, from := range map[string]string{"C-1": "C-12", "C-2": "C-1", "C-11": "C-10"} {
		_, err := st.AddDependencies(ctx, core.ChannelCLI, to, []string{from})
		must(err)
	}
	_, err = st.AddDependencies(ctx, core.ChannelCLI, "C-3", []string{"C-2", "C-14"})
	must(err)
	// 親子（子 → 親）
	for child, parent := range map[string]string{"C-5": "C-4", "C-6": "C-5", "C-16": "C-15"} {
		must(st.SetParent(ctx, core.ChannelCLI, child, parent))
	}
	// 人間の操作待ち
	_, err = st.HoldChallenge(ctx, core.ChannelCLI, "C-1", core.HoldInput{Question: "どちらの案で進めますか"})
	must(err)
	_, err = st.CreateOperation(ctx, core.ChannelCLI, core.OperationInput{ChallengeID: "C-16", Kind: "release", Summary: "本番へ反映する"})
	must(err)
	must(st.Close())

	// 公開 API では作れない状態（完了・取り下げ・着手中・優先度つきの分類待ち）だけを直接書く。
	for id, status := range map[int]string{4: "in_progress", 8: "unclassified", 12: "done", 13: "done", 14: "withdrawn", 15: "done", 17: "withdrawn"} {
		coretest.SetChallengeStatus(t, dir, id, status)
	}

	assertRelationGraphSeed(t, dir)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// assertRelationGraphSeed は種の中身（区分・状態ごとの件数・線・親子）を、関係グラフの API と同じ core の導出で確かめる。
func assertRelationGraphSeed(t *testing.T, dir string) {
	t.Helper()
	st, err := core.OpenWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	g, err := st.ChallengeGraph(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}

	byCategory := map[string][]string{}
	byState := map[string][]string{}
	parents := map[string]string{}
	humans := map[string][]string{}
	priorities := map[string]string{}
	for _, n := range g.Nodes {
		byCategory[string(n.Category)] = append(byCategory[string(n.Category)], n.ID)
		byState[string(n.State)] = append(byState[string(n.State)], n.ID)
		if n.ParentID != nil {
			parents[n.ID] = *n.ParentID
		}
		if len(n.NextHumanActions) > 0 {
			for _, a := range n.NextHumanActions {
				humans[n.ID] = append(humans[n.ID], string(a))
			}
		}
		if n.Priority != nil {
			priorities[n.ID] = string(*n.Priority)
		}
	}
	wantCategory := map[string][]string{
		"human":     {"C-1", "C-16"},
		"system":    {"C-2", "C-3", "C-4", "C-5", "C-6", "C-7", "C-8", "C-9", "C-10", "C-11"},
		"done":      {"C-12", "C-13", "C-15"},
		"withdrawn": {"C-14", "C-17"},
	}
	if !reflect.DeepEqual(byCategory, wantCategory) {
		t.Errorf("categories = %v, want %v", byCategory, wantCategory)
	}
	wantState := map[string][]string{
		"awaiting_human": {"C-1"},
		"classified":     {"C-2", "C-3", "C-5", "C-7", "C-9", "C-10", "C-11"},
		"in_progress":    {"C-4"},
		"unclassified":   {"C-6", "C-8", "C-16"},
		"done":           {"C-12", "C-13", "C-15"},
		"withdrawn":      {"C-14", "C-17"},
	}
	if !reflect.DeepEqual(byState, wantState) {
		t.Errorf("states = %v, want %v", byState, wantState)
	}
	if want := map[string][]string{"C-1": {"answer_question"}, "C-16": {"approve_operation"}}; !reflect.DeepEqual(humans, want) {
		t.Errorf("next_human_actions = %v, want %v", humans, want)
	}
	if priorities["C-7"] != "P0" || priorities["C-8"] != "P0" || priorities["C-6"] != "" {
		t.Errorf("priorities = %v", priorities)
	}
	if want := map[string]string{"C-5": "C-4", "C-6": "C-5", "C-16": "C-15"}; !reflect.DeepEqual(parents, want) {
		t.Errorf("parents = %v, want %v", parents, want)
	}
	var edges [][2]string
	for _, e := range g.Edges {
		edges = append(edges, [2]string{e.From, e.To})
		if e.Kind != core.GraphEdgePrerequisite {
			t.Errorf("edge %s→%s kind = %s", e.From, e.To, e.Kind)
		}
	}
	if want := [][2]string{{"C-1", "C-2"}, {"C-2", "C-3"}, {"C-10", "C-11"}, {"C-12", "C-1"}, {"C-14", "C-3"}}; !reflect.DeepEqual(edges, want) {
		t.Errorf("edges = %v, want %v", edges, want)
	}
}
