package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// docs/features/ui-relation-graph.md §関係グラフの API を、実ストア・実際の HTTP の要求で検証する。

func startServerAt(t *testing.T, now time.Time, ws ...core.FleetWorkspace) int {
	t.Helper()
	ln, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	s := newServer(ln, ws, eventsConfig{now: func() time.Time { return now }})
	errc := make(chan error, 1)
	go func() { errc <- s.Serve() }()
	t.Cleanup(func() {
		_ = s.Shutdown()
		<-errc
	})
	return ln.Addr().(*net.TCPAddr).Port
}

// graphWorkspace は cardsEndToEndWorkspace（C-1〜C-6。人間の操作待ち・応答なし・CI の待ちを含む）に、
// 依存・親子を足したワークスペース。
func graphWorkspace(t *testing.T, now time.Time) string {
	t.Helper()
	ws := cardsEndToEndWorkspace(t, now)
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := st.AddDependencies(ctx, core.ChannelCLI, "C-4", []string{"C-5"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetParent(ctx, core.ChannelCLI, "C-5", "C-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetParent(ctx, core.ChannelCLI, "C-6", "C-1"); err != nil {
		t.Fatal(err)
	}
	// C-7 は取り下げた課題（category は withdrawn になる）。
	if _, err := st.CreateChallenge(ctx, core.ChannelCLI, core.CreateInput{Title: "c7", DoneCriteria: "d"}); err != nil {
		t.Fatal(err)
	}
	// C-7 にだけ優先度を付ける（他は付いていない＝ null）。
	if _, err := st.ClassifyChallenge(ctx, core.ChannelCLI, "C-7", core.ClassifyInput{Priority: "P0"}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	coretest.SetChallengeStatus(t, ws, 7, "withdrawn")
	return ws
}

func TestGraphAPI_NodesFollowTheBoardCardsAndEdgesAndParentsAreReturned(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	ws := graphWorkspace(t, now)
	dir, calls := writeFakeGH(t, ghRoutesPending())
	withGHOnPATH(t, dir)
	port := startServerAt(t, now, core.FleetWorkspace{Name: "w", Path: ws})

	// ボードの課題の一覧（waiting_ci を得るために gh を呼ぶ）から、導出の期待値を作る。
	board := mustJSON(t, port, "/api/v1/workspaces/w/challenges")["challenges"].([]any)
	callsAfterBoard := calls()
	if callsAfterBoard == 0 {
		t.Fatal("setup: the board request should have used gh for waiting_ci")
	}

	code, body := apiGet(t, port, "/api/v1/workspaces/w/graph")
	if code != http.StatusOK {
		t.Fatalf("graph = %d %s", code, body)
	}
	if got := calls(); got != callsAfterBoard {
		t.Errorf("the graph request called gh %d times (graph は上流に問い合わせない)", got-callsAfterBoard)
	}
	m := decodeMap(t, body)
	assertKeys(t, "graph", m, "nodes", "edges")
	nodes := m["nodes"].([]any)
	if len(nodes) != len(board) {
		t.Fatalf("nodes = %d, board = %d", len(nodes), len(board))
	}
	for i, n := range nodes {
		node := n.(map[string]any)
		assertKeys(t, "node", node, "id", "title", "headline", "context", "category", "running", "stale", "parent_id", "state", "next_human_actions", "priority")
		c := board[i].(map[string]any)
		card := c["card"].(map[string]any)
		if node["id"] != c["id"] || node["title"] != c["title"] {
			t.Errorf("node %d = %v, board = %v", i, node, c["id"])
		}
		mods := strs(card["modifiers"])
		has := func(m string) bool {
			for _, x := range mods {
				if x == m {
					return true
				}
			}
			return false
		}
		wantCategory := "system"
		switch {
		case len(strs(card["next_human_actions"])) > 0:
			wantCategory = "human"
		case c["status"] == "done":
			wantCategory = "done"
		case c["status"] == "withdrawn":
			wantCategory = "withdrawn"
		}
		// 状態と人間の操作待ちの中身は、同じ応答のカードの値そのまま（並びも同じ）
		if node["state"] != card["state"] || !reflect.DeepEqual(strs(node["next_human_actions"]), strs(card["next_human_actions"])) {
			t.Errorf("%v state/next_human_actions = %v/%v, card = %v/%v", node["id"], node["state"], node["next_human_actions"], card["state"], card["next_human_actions"])
		}
		// 優先度は一覧の値そのまま（付いていなければ null）
		if node["priority"] != c["priority"] {
			t.Errorf("%v priority = %v, board = %v", node["id"], node["priority"], c["priority"])
		}
		if node["category"] != wantCategory {
			t.Errorf("%v category = %v, want %v", node["id"], node["category"], wantCategory)
		}
		if node["running"] != (has("running") || has("unresponsive")) || node["stale"] != has("unresponsive") {
			t.Errorf("%v running/stale = %v/%v, modifiers = %v", node["id"], node["running"], node["stale"], mods)
		}
	}
	byID := map[string]map[string]any{}
	for _, n := range nodes {
		byID[n.(map[string]any)["id"].(string)] = n.(map[string]any)
	}
	if byID["C-2"]["category"] != "human" || byID["C-6"]["stale"] != true || byID["C-5"]["running"] != true || byID["C-5"]["stale"] != false {
		t.Errorf("fixture expectations broken: C-2=%v C-5=%v C-6=%v", byID["C-2"], byID["C-5"], byID["C-6"])
	}
	if byID["C-7"]["priority"] != "P0" || byID["C-1"]["priority"] != nil {
		t.Errorf("priority: C-7=%v C-1=%v, want P0 / null", byID["C-7"]["priority"], byID["C-1"]["priority"])
	}
	if byID["C-7"]["category"] != "withdrawn" {
		t.Errorf("C-7 category = %v, want withdrawn", byID["C-7"]["category"])
	}
	if byID["C-5"]["parent_id"] != "C-1" || byID["C-1"]["parent_id"] != nil {
		t.Errorf("parent ids: C-5=%v C-1=%v", byID["C-5"]["parent_id"], byID["C-1"]["parent_id"])
	}

	edges := m["edges"].([]any)
	if len(edges) != 1 {
		t.Fatalf("edges = %v", edges)
	}
	e := edges[0].(map[string]any)
	assertKeys(t, "edge", e, "from", "to", "kind", "directed", "satisfied", "registered_via", "registered_at")
	if e["from"] != "C-5" || e["to"] != "C-4" || e["kind"] != "prerequisite" || e["directed"] != true || e["satisfied"] != false || e["registered_via"] != "manual" {
		t.Errorf("edge = %v", e)
	}
}

// 同じストアと同じ現在時刻からは、バイト列として同じ応答になる。
func TestGraphAPI_IsDeterministic(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	ws := graphWorkspace(t, now)
	port := startServerAt(t, now, core.FleetWorkspace{Name: "w", Path: ws})
	_, a := apiGet(t, port, "/api/v1/workspaces/w/graph")
	_, b := apiGet(t, port, "/api/v1/workspaces/w/graph")
	if a != b || a == "" {
		t.Errorf("two responses differ:\n%s\n%s", a, b)
	}
}

func TestGraphAPI_EmptyWorkspaceHasEmptyArrays(t *testing.T) {
	ws := initWorkspace(t)
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	code, body := apiGet(t, port, "/api/v1/workspaces/w/graph")
	if code != http.StatusOK || body != "{\"edges\":[],\"nodes\":[]}\n" && body != "{\"edges\":[],\"nodes\":[]}" {
		t.Errorf("graph = %d %q", code, body)
	}
}

func TestGraphAPI_ErrorsAndNonGET(t *testing.T) {
	ws := browseWorkspace(t)
	empty := t.TempDir()
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws}, core.FleetWorkspace{Name: "none", Path: empty})
	for path, want := range map[string]struct {
		status int
		code   string
	}{
		"/api/v1/workspaces/nope/graph": {404, "not_found"},
		"/api/v1/workspaces/none/graph": {404, "store_not_found"},
	} {
		code, body := apiGet(t, port, path)
		if code != want.status || !reflect.DeepEqual(errorCode(t, body), want.code) {
			t.Errorf("GET %s = %d %s, want %d %s", path, code, body, want.status, want.code)
		}
	}
	token, _ := fetchToken(t, port)
	if code, _ := post(t, port, "/api/v1/workspaces/w/graph", token, token); code != 404 {
		t.Errorf("POST graph = %d, want 404", code)
	}
}

func errorCode(t *testing.T, body string) string {
	t.Helper()
	m := decodeMap(t, body)
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

// 実データの形: ストアに入れた衝突の行が、API を経由して kind=conflict の辺になる（向きあり・向きなし）。
// 片方が完了・取り下げになった衝突は返さない。前提の辺は変わらない。
func TestGraphAPI_ConflictEdgesComeFromTheStoredRows(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	ws := graphWorkspace(t, now)
	const at = "2026-11-05T01:02:03.000Z"
	ins := `INSERT INTO challenge_conflict (challenge_id, conflicts_with_id, directed, created_at) VALUES (?, ?, ?, ?)`
	coretest.Exec(t, ws, ins, 6, 5, 1, at) // C-6 を後回しにして C-5 を先に委譲した
	coretest.Exec(t, ws, ins, 2, 3, 0, at) // C-2 と C-3 はまだどちらも委譲していない
	coretest.Exec(t, ws, ins, 3, 7, 1, at) // C-7 は取り下げ済み（返さない）
	dir, _ := writeFakeGH(t, ghRoutesPending())
	withGHOnPATH(t, dir)
	port := startServerAt(t, now, core.FleetWorkspace{Name: "w", Path: ws})

	m := mustJSON(t, port, "/api/v1/workspaces/w/graph")
	type edge struct {
		From, To, Kind, Via string
		Directed, Satisfied bool
	}
	var got []edge
	for _, e := range m["edges"].([]any) {
		x := e.(map[string]any)
		assertKeys(t, "edge", x, "from", "to", "kind", "directed", "satisfied", "registered_via", "registered_at")
		got = append(got, edge{x["from"].(string), x["to"].(string), x["kind"].(string), x["registered_via"].(string), x["directed"].(bool), x["satisfied"].(bool)})
		if x["kind"] == "conflict" && x["registered_at"] != at {
			t.Errorf("conflict registered_at = %v, want %s", x["registered_at"], at)
		}
	}
	want := []edge{
		{"C-2", "C-3", "conflict", "prediction", false, false},
		{"C-5", "C-4", "prerequisite", "manual", true, false},
		{"C-6", "C-5", "conflict", "prediction", true, false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edges = %+v, want %+v", got, want)
	}
}

// ボードの詳細の API（GET …/challenges/{id}）の waiting は、まだ終わっていない前提の課題と、
// 衝突のため後回しにした相手を別の一覧で返す。先に委譲した側・向きの無い組には付かない。
func TestChallengeAPI_WaitingListsPrerequisitesAndTheConflictSeparately(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	ws := graphWorkspace(t, now)
	const at = "2026-11-05T01:02:03.000Z"
	ins := `INSERT INTO challenge_conflict (challenge_id, conflicts_with_id, directed, created_at) VALUES (?, ?, ?, ?)`
	coretest.Exec(t, ws, ins, 4, 6, 1, at) // C-4 は C-6 に譲った（C-4 は前提の C-5 も待っている）
	coretest.Exec(t, ws, ins, 2, 3, 0, at)
	dir, _ := writeFakeGH(t, ghRoutesPending())
	withGHOnPATH(t, dir)
	port := startServerAt(t, now, core.FleetWorkspace{Name: "w", Path: ws})

	waiting := func(id string) map[string]any {
		w := mustJSON(t, port, "/api/v1/workspaces/w/challenges/"+id)["waiting"].(map[string]any)
		assertKeys(t, "waiting", w, "prerequisites", "conflicts")
		return w
	}
	w4 := waiting("C-4")
	pre := w4["prerequisites"].([]any)
	conf := w4["conflicts"].([]any)
	if len(pre) != 1 || len(conf) != 1 {
		t.Fatalf("C-4 waiting = %v", w4)
	}
	p0 := pre[0].(map[string]any)
	assertKeys(t, "prerequisite", p0, "challenge_id", "status", "title")
	if p0["challenge_id"] != "C-5" {
		t.Errorf("prerequisite = %v, want C-5", p0)
	}
	if c0 := conf[0].(map[string]any); c0["challenge_id"] != "C-6" {
		t.Errorf("conflict = %v, want C-6", c0)
	}
	for _, id := range []string{"C-6", "C-2", "C-3"} {
		w := waiting(id)
		if len(w["conflicts"].([]any)) != 0 {
			t.Errorf("%s waiting = %v, want no conflicts (not the deferred side)", id, w)
		}
	}
}

// 画面（web）の「実データの形」の確かめ。ストアに衝突の行を入れ、API を経由した値（graph と、課題ごとの waiting）を
// web/src/testing/conflict-api.golden.json と照合する。web の Vitest は同じファイルを読んで、関係グラフとボードの詳細を
// 描く（mock の手書きの形ではなく、本物の API の形で画面を確かめる）。更新は FLYWHEEL_UPDATE_GOLDEN=1 で書き直す。
func TestGraphAPI_RealShapeGoldenForTheWebScreen(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	ws := graphWorkspace(t, now)
	const at = "2026-11-05T01:02:03.000Z"
	ins := `INSERT INTO challenge_conflict (challenge_id, conflicts_with_id, directed, created_at) VALUES (?, ?, ?, ?)`
	coretest.Exec(t, ws, ins, 4, 6, 1, at) // C-4 は前提の C-5 を待ち、C-6 に譲って後回しにした
	coretest.Exec(t, ws, ins, 2, 3, 0, at) // 向きの無い組
	coretest.Exec(t, ws, ins, 3, 7, 1, at) // 相手の C-7 が取り下げ済み（返さない）
	// 前提の登録の日時は実時計なので、照合できるよう固定する（作業ログの at も同じ値にして経路 manual を保つ）
	coretest.Exec(t, ws, `UPDATE challenge_dependency SET created_at = ?`, "2026-11-02T00:00:00.000Z")
	coretest.Exec(t, ws, `UPDATE activity SET at = ? WHERE action = 'dep_add'`, "2026-11-02T00:00:00.000Z")
	dir, _ := writeFakeGH(t, ghRoutesPending())
	withGHOnPATH(t, dir)
	port := startServerAt(t, now, core.FleetWorkspace{Name: "w", Path: ws})

	out := map[string]any{"graph": mustJSON(t, port, "/api/v1/workspaces/w/graph")}
	waiting := map[string]any{}
	for _, id := range []string{"C-2", "C-3", "C-4", "C-6"} {
		waiting[id] = mustJSON(t, port, "/api/v1/workspaces/w/challenges/"+id)["waiting"]
	}
	out["waiting"] = waiting
	got, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	const golden = "../../web/src/testing/conflict-api.golden.json"
	if os.Getenv("FLYWHEEL_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v (FLYWHEEL_UPDATE_GOLDEN=1 go test ./internal/server -run RealShapeGolden で書き出す)", err)
	}
	if string(want) != string(got) {
		t.Errorf("API の応答が web/src/testing/conflict-api.golden.json と違う。意図した変更なら FLYWHEEL_UPDATE_GOLDEN=1 で書き直し、web の Vitest も通す\n got: %s", got)
	}
}
