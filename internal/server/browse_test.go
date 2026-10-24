package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは閲覧の API（S1 の AC-45 前後〜）を、実ストア・実際の HTTP の要求で検証する。
// CLI との一致は cmd/flywheel をビルドして同じストアに対して実行して比べる。

func cliJSON(t *testing.T, ws string, args ...string) string {
	t.Helper()
	cmd := exec.Command(flywheelBinary(t), append(args, "--json", "--workspace", ws)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("flywheel %v: %v (%s)", args, err, stderr.String())
	}
	return stdout.String()
}

func apiGet(t *testing.T, port int, path string) (int, string) {
	t.Helper()
	return getPath(t, port, path, fmt.Sprintf("127.0.0.1:%d", port), "")
}

func decodeMap(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not a JSON object: %v (%q)", err, s)
	}
	return m
}

func keySet(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func assertKeys(t *testing.T, what string, m map[string]any, want ...string) {
	t.Helper()
	sort.Strings(want)
	if got := keySet(m); !reflect.DeepEqual(got, want) {
		t.Errorf("%s keys = %v, want %v", what, got, want)
	}
}

// browseWorkspace は課題 2 件（C-1 計画承認待ち・C-2 未分類）と、終了済みの run 1 件を持つワークスペース。
func browseWorkspace(t *testing.T) string {
	t.Helper()
	ws := initWorkspace(t)
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, title := range []string{"one", "two"} {
		if _, err := st.CreateChallenge(ctx, core.ChannelCLI, core.CreateInput{Title: title, DoneCriteria: "d"}); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.Close()
	coretest.SetChallengeStatus(t, ws, 1, "awaiting_plan_approval")
	coretest.InsertRun(t, ws, coretest.InsertRunInput{
		ChallengeID: 1, Kind: "delegate", ChallengeVersion: 1, SessionID: "22222222-2222-4222-8222-222222222222",
		PID: 1, Host: "h", HeartbeatAt: "2026-10-02T00:00:00.000Z", StartedAt: "2026-10-02T00:00:00.000Z",
		EndedAt: "2026-10-02T00:10:00.000Z", Result: "succeeded", MaxBudgetUSD: 5_000_000, BudgetBucket: "impl",
	})
	return ws
}

func TestBrowseAPI_EqualsCLI(t *testing.T) {
	ws := browseWorkspace(t)
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})

	// list: 各要素から card を除くと CLI と等しい
	code, body := apiGet(t, port, "/api/v1/workspaces/w/challenges")
	if code != http.StatusOK {
		t.Fatalf("list = %d %s", code, body)
	}
	m := decodeMap(t, body)
	for _, e := range m["challenges"].([]any) {
		delete(e.(map[string]any), "card")
	}
	b, _ := json.Marshal(m)
	sameJSON(t, string(b), cliJSON(t, ws, "list"))

	// show
	code, body = apiGet(t, port, "/api/v1/workspaces/w/challenges/C-1")
	if code != http.StatusOK {
		t.Fatalf("show = %d %s", code, body)
	}
	m = decodeMap(t, body)
	delete(m, "card")
	b, _ = json.Marshal(m)
	sameJSON(t, string(b), cliJSON(t, ws, "show", "C-1"))

	// log・runs
	code, body = apiGet(t, port, "/api/v1/workspaces/w/log?challenge=C-1")
	if code != http.StatusOK {
		t.Fatalf("log = %d %s", code, body)
	}
	sameJSON(t, body, cliJSON(t, ws, "log", "C-1"))
	code, body = apiGet(t, port, "/api/v1/workspaces/w/log")
	if code != http.StatusOK {
		t.Fatalf("log all = %d %s", code, body)
	}
	sameJSON(t, body, cliJSON(t, ws, "log"))
	code, body = apiGet(t, port, "/api/v1/workspaces/w/runs")
	if code != http.StatusOK {
		t.Fatalf("runs = %d %s", code, body)
	}
	sameJSON(t, body, cliJSON(t, ws, "runs"))
}

func TestBrowseAPI_Errors(t *testing.T) {
	ws := browseWorkspace(t)
	empty := t.TempDir() // ストアの無いワークスペース
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws}, core.FleetWorkspace{Name: "none", Path: empty})
	cases := []struct {
		path   string
		status int
		code   string
	}{
		{"/api/v1/workspaces/w/challenges/C-99", 404, "not_found"},
		{"/api/v1/workspaces/w/challenges/bogus", 404, "not_found"},
		{"/api/v1/workspaces/nope/challenges", 404, "not_found"},
		{"/api/v1/workspaces/nope/challenges/C-1", 404, "not_found"},
		{"/api/v1/workspaces/nope/log", 404, "not_found"},
		{"/api/v1/workspaces/nope/runs", 404, "not_found"},
		{"/api/v1/workspaces/nope/runs/R-1/report", 404, "not_found"},
		{"/api/v1/workspaces/none/challenges", 404, "store_not_found"},
		{"/api/v1/workspaces/none/runs", 404, "store_not_found"},
		{"/api/v1/workspaces/w/runs/R-99/report", 404, "not_found"},
		{"/api/v1/workspaces/w/log?challenge=C-99", 404, "not_found"},
	}
	for _, c := range cases {
		code, body := apiGet(t, port, c.path)
		var doc struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		_ = json.Unmarshal([]byte(body), &doc)
		if code != c.status || doc.Error.Code != c.code || doc.Error.Message == "" {
			t.Errorf("GET %s = %d %s, want %d %s", c.path, code, body, c.status, c.code)
		}
		assertCodeInTable(t, doc.Error.Code)
	}
	// store_too_new は 503
	tooNew := initWorkspace(t)
	coretest.SetStoreVersion(t, tooNew, 9999)
	_, port2 := startFleetServer(t, core.FleetWorkspace{Name: "new", Path: tooNew})
	if code, body := apiGet(t, port2, "/api/v1/workspaces/new/challenges"); code != 503 || !strings.Contains(body, `"store_too_new"`) {
		t.Errorf("too new = %d %s", code, body)
	}
}

func TestBrowseAPI_NonGETIs404(t *testing.T) {
	ws := browseWorkspace(t)
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	for _, p := range []string{"/api/v1/workspaces/w/challenges", "/api/v1/needs-human", "/api/v1/questions", "/api/v1/workspaces/w/runs/R-1/report"} {
		token, _ := fetchToken(t, port)
		if code, _ := post(t, port, p, token, token); code != 404 {
			t.Errorf("POST %s = %d, want 404", p, code)
		}
	}
}

func TestBrowseAPI_NeedsHuman(t *testing.T) {
	a := browseWorkspace(t)
	b := browseWorkspace(t)
	broken := t.TempDir()
	_, port := startFleetServer(t,
		core.FleetWorkspace{Name: "a", Path: a}, core.FleetWorkspace{Name: "broken", Path: broken}, core.FleetWorkspace{Name: "b", Path: b})
	code, body := apiGet(t, port, "/api/v1/needs-human")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	m := decodeMap(t, body)
	assertKeys(t, "needs-human", m, "workspaces", "unreadable_workspaces")
	if u := m["unreadable_workspaces"].([]any); len(u) != 0 {
		t.Errorf("unreadable_workspaces = %v, want []", u)
	}
	wss := m["workspaces"].([]any)
	if len(wss) != 2 {
		t.Fatalf("workspaces = %v, want a and b only", wss)
	}
	for i, name := range []string{"a", "b"} {
		e := wss[i].(map[string]any)
		assertKeys(t, "entry", e, "name", "needs_human")
		if e["name"] != name {
			t.Errorf("entry %d name = %v", i, e["name"])
		}
		_, sbody := apiGet(t, port, "/api/v1/workspaces/"+name+"/status")
		want := decodeMap(t, sbody)["needs_human"]
		if !reflect.DeepEqual(e["needs_human"], want) {
			t.Errorf("%s needs_human = %v, want %v", name, e["needs_human"], want)
		}
	}
}

func TestBrowseAPI_Questions(t *testing.T) {
	a := browseWorkspace(t)
	b := browseWorkspace(t)
	runID := coretest.InsertRun(t, b, coretest.InsertRunInput{
		ChallengeID: 2, Kind: "delegate", ChallengeVersion: 1, SessionID: "33333333-3333-4333-8333-333333333333",
		PID: 1, Host: "h", HeartbeatAt: "2026-10-02T00:00:00.000Z", StartedAt: "2026-10-02T00:00:00.000Z",
		EndedAt: "2026-10-02T00:10:00.000Z", Result: "succeeded", MaxBudgetUSD: 1, BudgetBucket: "impl",
	})
	same := "2026-10-05T00:00:00.000Z"
	coretest.InsertHold(t, b, 2, "qb2", "in_progress", same, nil)
	coretest.InsertHold(t, a, 2, "qa2", "in_progress", same, nil)
	coretest.InsertHold(t, a, 1, "qa1", "in_progress", same, nil)
	coretest.InsertHold(t, b, 1, "oldest", "in_progress", "2026-10-01T00:00:00.000Z", &runID)
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "wa", Path: a}, core.FleetWorkspace{Name: "wb", Path: b})

	code, body := apiGet(t, port, "/api/v1/questions")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	qs := decodeMap(t, body)["questions"].([]any)
	var order []string
	for _, q := range qs {
		qm := q.(map[string]any)
		assertKeys(t, "question", qm, "workspace", "challenge_id", "title", "question", "raised_at", "from_status", "run_id")
		order = append(order, qm["question"].(string))
	}
	if want := []string{"oldest", "qa1", "qa2", "qb2"}; !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
	first := qs[0].(map[string]any)
	if first["run_id"] != fmt.Sprintf("R-%d", runID) || first["workspace"] != "wb" || first["from_status"] != "in_progress" || first["raised_at"] != "2026-10-01T00:00:00.000Z" {
		t.Errorf("first = %v", first)
	}
	if qs[1].(map[string]any)["run_id"] != nil {
		t.Errorf("run_id should be null: %v", qs[1])
	}
}

func TestBrowseAPI_QuestionsOrderTiesByNumericID(t *testing.T) {
	ws := browseWorkspace(t)
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := st.CreateChallenge(context.Background(), core.ChannelCLI, core.CreateInput{Title: "x", DoneCriteria: "d"}); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.Close() // C-1..C-10
	at := "2026-10-05T00:00:00.000Z"
	coretest.InsertHold(t, ws, 10, "ten", "in_progress", at, nil)
	coretest.InsertHold(t, ws, 9, "nine", "in_progress", at, nil)
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	qs := mustJSON(t, port, "/api/v1/questions")["questions"].([]any)
	if len(qs) != 2 || qs[0].(map[string]any)["question"] != "nine" {
		t.Errorf("questions = %v, want C-9 before C-10", qs)
	}
}

// AC-55: 同じ時刻なら workspace（名前の順）が challenge_id より先に効く。fleet の宣言の順には依らない。
func TestBrowseAPI_QuestionsTieBreaksByWorkspaceNameBeforeChallengeID(t *testing.T) {
	a, b := browseWorkspace(t), browseWorkspace(t)
	same := "2026-10-05T00:00:00.000Z"
	coretest.InsertHold(t, a, 2, "wa-C2", "in_progress", same, nil)
	coretest.InsertHold(t, b, 1, "wb-C1", "in_progress", same, nil)
	wa, wb := core.FleetWorkspace{Name: "wa", Path: a}, core.FleetWorkspace{Name: "wb", Path: b}
	for name, decl := range map[string][]core.FleetWorkspace{"declared in name order": {wa, wb}, "declared in reverse name order": {wb, wa}} {
		t.Run(name, func(t *testing.T) {
			_, port := startFleetServer(t, decl...)
			var order []string
			for _, q := range mustJSON(t, port, "/api/v1/questions")["questions"].([]any) {
				order = append(order, q.(map[string]any)["question"].(string))
			}
			if want := []string{"wa-C2", "wb-C1"}; !reflect.DeepEqual(order, want) {
				t.Errorf("order = %v, want %v", order, want)
			}
		})
	}
}

func TestBrowseAPI_QuestionsEmptyAndAnsweredExcluded(t *testing.T) {
	ws := browseWorkspace(t)
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	if _, body := apiGet(t, port, "/api/v1/questions"); strings.TrimSpace(body) != `{"questions":[],"unreadable_workspaces":[]}` {
		t.Errorf("body = %s", body)
	}
	coretest.InsertHold(t, ws, 1, "open", "in_progress", "2026-10-01T00:00:00.000Z", nil)
	coretest.InsertAnsweredHold(t, ws, 2, "answered", "in_progress", "2026-10-01T00:00:00.000Z")
	qs := mustJSON(t, port, "/api/v1/questions")["questions"].([]any)
	if len(qs) != 1 || qs[0].(map[string]any)["question"] != "open" {
		t.Errorf("questions = %v, want only the unanswered one", qs)
	}
}

func TestBrowseAPI_RunReport(t *testing.T) {
	ws := browseWorkspace(t) // R-1
	mk := func(content string) string {
		id := coretest.InsertRun(t, ws, coretest.InsertRunInput{
			ChallengeID: 1, Kind: "delegate", ChallengeVersion: 1, SessionID: "44444444-4444-4444-8444-444444444444",
			PID: 1, Host: "h", HeartbeatAt: "2026-10-02T00:00:00.000Z", StartedAt: "2026-10-02T00:00:00.000Z",
			EndedAt: "2026-10-02T00:10:00.000Z", Result: "succeeded", MaxBudgetUSD: 1, BudgetBucket: "impl",
		})
		rid := fmt.Sprintf("R-%d", id)
		if content != "" {
			dir := filepath.Join(ws, ".flywheel", "runs", rid)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "stdout.json"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return rid
	}
	full := mk(`{"structured_output":{"assumptions":["a1","a2"],"unverified":["u1"]}}`)
	noFile := mk("")
	noKey := mk(`{"structured_output":{"summary":"x"}}`)
	noSO := mk(`{"result":"x"}`)
	half := mk(`{"structured_output":{"assumptions":["only"]}}`)
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})

	get := func(id string) (int, map[string]any) {
		code, body := apiGet(t, port, "/api/v1/workspaces/w/runs/"+id+"/report")
		return code, decodeMap(t, body)
	}
	code, m := get(full)
	if code != 200 {
		t.Fatalf("full = %d %v", code, m)
	}
	assertKeys(t, "report body", m, "report")
	r := m["report"].(map[string]any)
	assertKeys(t, "report", r, "run_id", "assumptions", "unverified")
	if r["run_id"] != full || !reflect.DeepEqual(r["assumptions"], []any{"a1", "a2"}) || !reflect.DeepEqual(r["unverified"], []any{"u1"}) {
		t.Errorf("report = %v", r)
	}
	for _, id := range []string{noFile, noKey, noSO} {
		code, m := get(id)
		r := m["report"].(map[string]any)
		if code != 200 || !reflect.DeepEqual(r["assumptions"], []any{}) || !reflect.DeepEqual(r["unverified"], []any{}) {
			t.Errorf("%s = %d %v, want empty arrays", id, code, m)
		}
	}
	if _, m := get(half); !reflect.DeepEqual(m["report"].(map[string]any)["assumptions"], []any{"only"}) ||
		!reflect.DeepEqual(m["report"].(map[string]any)["unverified"], []any{}) {
		t.Errorf("half = %v", m)
	}
}

func TestBrowseAPI_RunReportBadIDIsValidationFailedAndReadsNoFile(t *testing.T) {
	ws := browseWorkspace(t)
	// R-1/../x が指しうる場所に、読まれてはならないファイルを置く。
	for _, d := range []string{"R-1", "x", "R-01"} {
		dir := filepath.Join(ws, ".flywheel", "runs", d)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "stdout.json"), []byte(`{"structured_output":{"assumptions":["LEAK"]}}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	for _, id := range []string{"R-01", "R-0", "R-x", "R--1", "x", "%2e%2e%2f%2e%2e%2fetc", "R-1%2f..%2fx"} {
		code, body := apiGet(t, port, "/api/v1/workspaces/w/runs/"+id+"/report")
		if code != 400 || !strings.Contains(body, `"validation_failed"`) {
			t.Errorf("id %q = %d %s, want 400 validation_failed", id, code, body)
		}
		if strings.Contains(body, "LEAK") {
			t.Errorf("id %q read a file: %s", id, body)
		}
	}
}

// --- カード ---

func mustJSON(t *testing.T, port int, path string) map[string]any {
	t.Helper()
	code, body := apiGet(t, port, path)
	if code != 200 {
		t.Fatalf("GET %s = %d %s", path, code, body)
	}
	return decodeMap(t, body)
}

func cardOf(t *testing.T, port int, id string) map[string]any {
	t.Helper()
	m := mustJSON(t, port, "/api/v1/workspaces/w/challenges/"+id)
	c := m["card"].(map[string]any)
	assertKeys(t, "card", c, "state", "modifiers", "next_human_actions", "slot_ids", "headline", "context")
	if c["headline"] != nil || c["context"] != nil {
		t.Errorf("headline/context must be null: %v", c)
	}
	return c
}

func strs(v any) []string {
	out := []string{}
	for _, e := range v.([]any) {
		out = append(out, e.(string))
	}
	return out
}

func TestBrowseAPI_CardStatesAndActions(t *testing.T) {
	ws := browseWorkspace(t)
	states := []string{"unclassified", "classified", "awaiting_plan_approval", "in_progress", "verifying", "awaiting_completion_approval", "awaiting_human", "done"}
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	for range states[2:] { // C-1, C-2 に足して 8 件にする
		if _, err := st.CreateChallenge(context.Background(), core.ChannelCLI, core.CreateInput{Title: "x", DoneCriteria: "d"}); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.Close()
	for i, s := range states {
		coretest.SetChallengeStatus(t, ws, i+1, s)
	}
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	for i, s := range states {
		c := cardOf(t, port, fmt.Sprintf("C-%d", i+1))
		if c["state"] != s {
			t.Errorf("C-%d card.state = %v, want %s", i+1, c["state"], s)
		}
	}
	want := map[string][]string{
		"awaiting_plan_approval":       {"approve_plan"},
		"awaiting_completion_approval": {"approve_completion"},
		"awaiting_human":               {"answer_question"},
		"classified":                   {},
	}
	for i, s := range states {
		if w, ok := want[s]; ok {
			got := strs(cardOf(t, port, fmt.Sprintf("C-%d", i+1))["next_human_actions"])
			if !reflect.DeepEqual(got, w) {
				t.Errorf("%s next_human_actions = %v, want %v", s, got, w)
			}
		}
	}
	// list の要素の card は show の card と同じ
	list := mustJSON(t, port, "/api/v1/workspaces/w/challenges")["challenges"].([]any)
	if len(list) != 8 {
		t.Fatalf("list = %d", len(list))
	}
	for i, e := range list {
		em := e.(map[string]any)
		if !reflect.DeepEqual(em["card"], cardOf(t, port, fmt.Sprintf("C-%d", i+1))) {
			t.Errorf("list card != show card for %v", em["id"])
		}
	}
}

func TestBrowseAPI_CardRunModifiers(t *testing.T) {
	ws := browseWorkspace(t)
	now := time.Now().UTC()
	hb := func(d time.Duration) string { return now.Add(-d).Format("2006-01-02T15:04:05.000Z") }
	open := func(cid int, heartbeat string) {
		coretest.InsertRun(t, ws, coretest.InsertRunInput{
			ChallengeID: cid, Kind: "delegate", ChallengeVersion: 1, SessionID: "55555555-5555-4555-8555-555555555555",
			PID: 1, Host: "h", HeartbeatAt: heartbeat, StartedAt: heartbeat, MaxBudgetUSD: 1, BudgetBucket: "impl",
		})
	}
	// C-1 は新しい heartbeat、C-2 は 301 秒前（ちょうど 300 秒は境界の都合でテストしない: 現在時刻が進むため）
	open(1, hb(10*time.Second))
	open(2, hb(301*time.Second))
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	if got := strs(cardOf(t, port, "C-1")["modifiers"]); !reflect.DeepEqual(got, []string{"running"}) {
		t.Errorf("C-1 modifiers = %v", got)
	}
	if got := strs(cardOf(t, port, "C-2")["modifiers"]); !reflect.DeepEqual(got, []string{"running", "unresponsive"}) {
		t.Errorf("C-2 modifiers = %v", got)
	}
}

// cardsEndToEndWorkspace は、実ストアで次の課題を持つワークスペースを作る（カードの導出の条件を
// 実データで当てる。時刻は now を基準にする）。
//
//	C-1 検証中・PR あり（waiting_ci。偽の gh は呼び出し元が PATH に置く）
//	C-2 未分類・J1 が not_mine・未承認の不可逆操作・食い違い（上流が closed）
//	C-3 着手中・承認済みの計画（実装枠 0.5 USD → 予算の使い切り）・heartbeat が 301 秒前の run が
//	    needs_attention のスロットに結ばれている・食い違い（上流が closed）
//	C-4 着手中・heartbeat が 299 秒前の run  C-5 同 300 秒前  C-6 同 301 秒前
//
// waiting_ci は終了していない run が無い検証中の課題だけが持つので、running・unresponsive とは
// 同じ課題に重ならない。実データで 1 つの課題に重なるのは 4 つ（C-3）までで、5 つすべてに当たる課題での
// 順序の検証（AC-80 の文言どおり）は core の単体テストが担う（仕様への指摘: API では文言どおりに検証できない）。
func cardsEndToEndWorkspace(t *testing.T, now time.Time) string {
	t.Helper()
	ws := verifyingWorkspace(t) // C-1
	writeAgentJSON(t, ws, validAgentJSON)
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 2; i <= 6; i++ {
		if _, err := st.CreateChallenge(ctx, core.ChannelCLI, core.CreateInput{Title: fmt.Sprintf("c%d", i), DoneCriteria: "d"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CreateOperation(ctx, core.ChannelCLI, core.OperationInput{ChallengeID: "C-2", Kind: "release", Summary: "s"}); err != nil {
		t.Fatal(err)
	}
	c2, err := st.GetChallenge(ctx, "C-2")
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	stamp := func(ago time.Duration) string { return now.Add(-ago).Format("2006-01-02T15:04:05.000Z") }

	// C-2: triage・食い違い
	j1 := coretest.InsertRun(t, ws, coretest.InsertRunInput{
		ChallengeID: 2, Kind: "judgment", Judgment: "J1", ChallengeVersion: c2.Version, SessionID: "77777777-7777-4777-8777-777777777777",
		PID: 1, Host: "h", HeartbeatAt: stamp(time.Hour), StartedAt: stamp(time.Hour), EndedAt: stamp(59 * time.Minute),
		Result: "succeeded", MaxBudgetUSD: 1, BudgetBucket: "none",
	})
	coretest.SetRunOutput(t, ws, j1, `{"verdict":"not_mine","priority":null,"size":null,"reason":"fixture","question":null}`)
	coretest.InsertSourceBinding(t, ws, 2, "gh", "o/r#2", "https://github.com/o/r/issues/2", "fp", "closed", "in_policy", stamp(time.Hour))

	// C-3: 予算の使い切り・応答なし・スロット・食い違い
	coretest.InsertSourceBinding(t, ws, 3, "gh", "o/r#3", "https://github.com/o/r/issues/3", "fp", "closed", "in_policy", stamp(time.Hour))
	spec := `{"verdict":"plan","summary":"S","steps":["a","b"],"repo":"r","operation":"op","size":"M","done_criteria":"D","budget_impl_usd":null,"budget_review_usd":null,"cross_repo":false,"related_repos":[],"question":null}`
	coretest.InsertApprovedPlan(t, ws, 3, "PLAN", spec)
	slot := coretest.InsertSlot(t, ws, "r", "p", filepath.Join(t.TempDir(), "slot"), "needs_attention", "fixture")
	open := func(cid int, ago time.Duration) int64 {
		return coretest.InsertRun(t, ws, coretest.InsertRunInput{
			ChallengeID: cid, Kind: "delegate", ChallengeVersion: 1, SessionID: fmt.Sprintf("8888888%d-8888-4888-8888-888888888888", cid),
			PID: 1, Host: "h", HeartbeatAt: stamp(ago), StartedAt: stamp(ago), MaxBudgetUSD: 1, BudgetBucket: "impl",
		})
	}
	coretest.SetRunSlot(t, ws, open(3, 301*time.Second), slot)

	// C-4・C-5・C-6: heartbeat の境界
	for cid, ago := range map[int]time.Duration{4: 299 * time.Second, 5: 300 * time.Second, 6: 301 * time.Second} {
		coretest.SetChallengeStatus(t, ws, cid, "in_progress")
		open(cid, ago)
	}
	return ws
}

// 課題の状態・modifiers・next_human_actions の導出を、実ストアと API の経由で順序つきの完全一致で確かめる
// （AC-67・68・70・71・73・76・78・79・80〔80 は実データで重なる 4 つまで〕。時刻は server に固定の now を注入する）。
func TestBrowseAPI_CardsDerivedFromRealStoreData(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	ws := cardsEndToEndWorkspace(t, now)
	dir, _ := writeFakeGH(t, ghRoutesPending())
	withGHOnPATH(t, dir)
	ln, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	s := newServer(ln, []core.FleetWorkspace{{Name: "w", Path: ws}}, eventsConfig{now: func() time.Time { return now }})
	errc := make(chan error, 1)
	go func() { errc <- s.Serve() }()
	t.Cleanup(func() {
		_ = s.Shutdown()
		<-errc
	})
	port := ln.Addr().(*net.TCPAddr).Port

	want := map[string]map[string][]string{
		"C-1": {"modifiers": {"waiting_ci"}, "next_human_actions": {}},
		// 3 つの条件（triage・approve_operation・review_discrepancy）が表の順に並ぶ
		"C-2": {"modifiers": {"discrepancy"}, "next_human_actions": {"triage", "approve_operation", "review_discrepancy"}},
		"C-3": {"modifiers": {"running", "unresponsive", "budget_exhausted", "discrepancy"}, "next_human_actions": {"increase_budget", "review_discrepancy", "clear_slot"}},
		// heartbeat が 299 秒・300 秒ちょうどは応答なしではなく、301 秒から応答なし
		"C-4": {"modifiers": {"running"}, "next_human_actions": {}},
		"C-5": {"modifiers": {"running"}, "next_human_actions": {}},
		"C-6": {"modifiers": {"running", "unresponsive"}, "next_human_actions": {}},
	}
	for id, w := range want {
		c := cardOf(t, port, id)
		if got := strs(c["modifiers"]); !reflect.DeepEqual(got, w["modifiers"]) {
			t.Errorf("%s modifiers = %v, want %v", id, got, w["modifiers"])
		}
		if got := strs(c["next_human_actions"]); !reflect.DeepEqual(got, w["next_human_actions"]) {
			t.Errorf("%s next_human_actions = %v, want %v", id, got, w["next_human_actions"])
		}
	}
	// slot_ids: スロットの直近の run の課題（C-3）にだけ結ばれたスロットの ID が出る
	for id, want := range map[string][]string{"C-1": {}, "C-2": {}, "C-3": {"SL-1"}, "C-4": {}, "C-5": {}, "C-6": {}} {
		if got := strs(cardOf(t, port, id)["slot_ids"]); !reflect.DeepEqual(got, want) {
			t.Errorf("%s slot_ids = %v, want %v", id, got, want)
		}
	}
}

func TestBrowseAPI_CardWaitingCI(t *testing.T) {
	ws := verifyingWorkspace(t)
	dir, _ := writeFakeGH(t, ghRoutesPending())
	withGHOnPATH(t, dir)
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	if got := strs(cardOf(t, port, "C-1")["modifiers"]); !reflect.DeepEqual(got, []string{"waiting_ci"}) {
		t.Errorf("modifiers = %v, want [waiting_ci]", got)
	}
}

func TestBrowseAPI_CardOperationActions(t *testing.T) {
	ws := browseWorkspace(t)
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateOperation(context.Background(), core.ChannelCLI, core.OperationInput{ChallengeID: "C-2", Kind: "release", Summary: "s"}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	coretest.SetChallengeStatus(t, ws, 2, "done")
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	if got := strs(cardOf(t, port, "C-2")["next_human_actions"]); !reflect.DeepEqual(got, []string{"approve_operation"}) {
		t.Errorf("actions = %v", got)
	}
}

// 閲覧の API だけでは activity・run・lock の行が変わらない（古い heartbeat の run を含むストアで）。
func TestBrowseAPI_ReadOnlyDoesNotWrite(t *testing.T) {
	ws := browseWorkspace(t)
	old := time.Now().UTC().Add(-time.Hour).Format("2006-01-02T15:04:05.000Z")
	coretest.InsertRun(t, ws, coretest.InsertRunInput{
		ChallengeID: 1, Kind: "delegate", ChallengeVersion: 1, SessionID: "66666666-6666-4666-8666-666666666666",
		PID: deadPID(t), Host: hostName(t), HeartbeatAt: old, StartedAt: old, MaxBudgetUSD: 1, BudgetBucket: "impl",
	})
	coretest.InsertHold(t, ws, 1, "q", "in_progress", old, nil)
	// 期限切れ（holder の無い古い heartbeat）の lock の行も、回収されずそのまま残る。
	cyc := coretest.InsertCycle(t, ws, "manual", 1_000_000, old)
	coretest.InsertLock(t, ws, cyc, deadPID(t), hostName(t), old, old)
	before := dumpRows(t, ws)
	s, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	for _, p := range []string{
		"/api/v1/workspaces", "/api/v1/workspaces/w/status", "/api/v1/workspaces/w/challenges", "/api/v1/workspaces/w/challenges/C-1",
		"/api/v1/workspaces/w/log", "/api/v1/workspaces/w/log?challenge=C-1", "/api/v1/workspaces/w/runs", "/api/v1/workspaces/w/runs?challenge=C-1",
		"/api/v1/needs-human", "/api/v1/questions", "/api/v1/workspaces/w/runs/R-1/report", "/api/v1/workspaces/w/runs/R-2/report",
	} {
		if code, body := apiGet(t, port, p); code != 200 {
			t.Fatalf("GET %s = %d %s", p, code, body)
		}
	}
	if after := dumpRows(t, ws); after != before {
		t.Errorf("rows changed:\nbefore: %s\nafter:  %s", before, after)
	}
	// 終了（Shutdown）の後でも変わらない（終了時に何かを書かない）。
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if after := dumpRows(t, ws); after != before {
		t.Errorf("rows changed by shutdown:\nbefore: %s\nafter:  %s", before, after)
	}
}

func hostName(t *testing.T) string {
	t.Helper()
	h, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// deadPID は終了済みのプロセスの PID を返す。
func deadPID(t *testing.T) int64 {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return int64(cmd.Process.Pid)
}

// dumpRows は activity・run・lock の内容を CLI を介さず取り出す（CLI の log・runs は run を回収しうるので使わない）。
func dumpRows(t *testing.T, ws string) string {
	t.Helper()
	return coretest.DumpTables(t, ws, "activity", "run", "lock")
}

// breakReads は、ワークスペース ws のストアを「state は ok のまま、読み取りだけが失敗する」
// 状態にする（スキーマのテーブルを消す。SchemaVersion の判定は通り、課題・保留の読み取りが失敗する）。
func breakReads(t *testing.T, ws string) {
	t.Helper()
	coretest.DropTable(t, ws, "hold")
}

func unreadableOf(t *testing.T, body string, key string) []string {
	t.Helper()
	m := decodeMap(t, body)
	raw, ok := m["unreadable_workspaces"].([]any)
	if !ok {
		t.Fatalf("%s: unreadable_workspaces is not an array: %s", key, body)
	}
	out := []string{}
	for _, v := range raw {
		out = append(out, v.(string))
	}
	return out
}

// AC-54a
func TestBrowseAPI_QuestionsSkipsUnreadableWorkspace(t *testing.T) {
	a := browseWorkspace(t)
	b := browseWorkspace(t)
	coretest.InsertHold(t, a, 1, "from-a", "in_progress", "2026-10-05T00:00:00.000Z", nil)
	breakReads(t, b)
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "a", Path: a}, core.FleetWorkspace{Name: "b", Path: b})

	code, body := apiGet(t, port, "/api/v1/questions")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	qs := decodeMap(t, body)["questions"].([]any)
	if len(qs) != 1 || qs[0].(map[string]any)["question"] != "from-a" {
		t.Errorf("questions = %v, want only a's", qs)
	}
	if got := unreadableOf(t, body, "questions"); !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("unreadable_workspaces = %v, want [b]", got)
	}
}

// AC-53a
func TestBrowseAPI_NeedsHumanSkipsUnreadableWorkspace(t *testing.T) {
	a := browseWorkspace(t)
	b := browseWorkspace(t)
	coretest.DropTable(t, b, "slot")
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "a", Path: a}, core.FleetWorkspace{Name: "b", Path: b})

	code, body := apiGet(t, port, "/api/v1/needs-human")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	wss := decodeMap(t, body)["workspaces"].([]any)
	if len(wss) != 1 || wss[0].(map[string]any)["name"] != "a" {
		t.Errorf("workspaces = %v, want only a", wss)
	}
	if got := unreadableOf(t, body, "needs-human"); !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("unreadable_workspaces = %v, want [b]", got)
	}
}

// AC-53a・54a: 判定の後に借りられなくなったワークスペース（Acquire の失敗）は除いて名前を添え、残りは返す。
func TestBrowseAPI_AcquireFailureIsUnreadableAndRestIsReturned(t *testing.T) {
	a := browseWorkspace(t)
	coretest.InsertHold(t, a, 1, "from-a", "in_progress", "2026-10-05T00:00:00.000Z", nil)
	s, _ := startFleetServer(t, core.FleetWorkspace{Name: "a", Path: a})
	// ok と判定された後に fleet から借りられなくなった名前 "gone" を、古い判定結果として混ぜる。
	stale := append(s.fleet.Statuses(context.Background()), WorkspaceStatus{Name: "gone", State: StateOK})

	nh, err := collectNeedsHuman(context.Background(), s.fleet, stale)
	if err != nil {
		t.Fatal(err)
	}
	if len(nh.Workspaces) != 1 || nh.Workspaces[0].Name != "a" || !reflect.DeepEqual(nh.UnreadableWorkspaces, []string{"gone"}) {
		t.Errorf("needs-human = %+v", nh)
	}
	q, err := collectQuestions(context.Background(), s.fleet, stale)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Questions) != 1 || !reflect.DeepEqual(q.UnreadableWorkspaces, []string{"gone"}) {
		t.Errorf("questions = %+v", q)
	}
}

// 要求が取り消されて借りられなかったときは、unreadable_workspaces に入れて続けず、エラーにする。
func TestBrowseAPI_AcquireFailureOnCancelledRequestIsAnError(t *testing.T) {
	a := browseWorkspace(t)
	s, _ := startFleetServer(t, core.FleetWorkspace{Name: "a", Path: a})
	stale := []WorkspaceStatus{{Name: "gone", State: StateOK}} // 借りられないのはこの 1 件だけ（後続の読み取りの失敗で通らないように）
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := collectNeedsHuman(ctx, s.fleet, stale); err == nil {
		t.Error("needs-human: want an error for a cancelled request")
	}
	if _, err := collectQuestions(ctx, s.fleet, stale); err == nil {
		t.Error("questions: want an error for a cancelled request")
	}
}

// AC-44a（カードの API）: 遅い gh の取得の間はストアの読み取りの借りを持たない（状態更新の Lock が待たされない）。
func TestBrowseAPI_CardsDoNotHoldStoreLeaseWhileFetchingChecks(t *testing.T) {
	for _, path := range []string{"/api/v1/workspaces/w/challenges", "/api/v1/workspaces/w/challenges/C-1"} {
		t.Run(path, func(t *testing.T) {
			ws := verifyingWorkspace(t)
			dir, calls := writeFakeGH(t, ghRoutesPending())
			release := filepath.Join(dir, "release")
			orig, err := os.ReadFile(filepath.Join(dir, "gh"))
			if err != nil {
				t.Fatal(err)
			}
			wrapped := strings.Replace(string(orig), "case ", "while [ ! -e '"+release+"' ]; do sleep 0.05; done\ncase ", 1)
			if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(wrapped), 0o755); err != nil {
				t.Fatal(err)
			}
			withGHOnPATH(t, dir)
			s, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
			if e := stateOf(t, port, "w"); e.State != StateOK {
				t.Fatalf("state = %q", e.State)
			}
			type result struct {
				code int
				body string
			}
			done := make(chan result, 1)
			go func() {
				code, body := apiGet(t, port, path)
				done <- result{code, body}
			}()
			deadline := time.Now().Add(10 * time.Second)
			for calls() == 0 {
				if time.Now().After(deadline) {
					t.Fatal("gh was never started")
				}
				time.Sleep(10 * time.Millisecond)
			}
			dbDir := filepath.Join(ws, ".flywheel")
			if err := os.Rename(filepath.Join(dbDir, "flywheel.db"), filepath.Join(dbDir, "moved.db")); err != nil {
				t.Fatal(err)
			}
			acquired := make(chan struct{})
			go func() {
				if _, rel, le := s.fleet.Acquire(context.Background(), "w"); le == nil {
					rel()
				}
				close(acquired)
			}()
			select {
			case <-acquired:
			case <-time.After(5 * time.Second):
				t.Error("state update was blocked while the card request waited for gh")
			}
			if err := os.WriteFile(release, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done: // 状態が変わった後なのでコードは問わない
			case <-time.After(10 * time.Second):
				t.Fatal("card request did not finish")
			}
		})
	}
}

// M4.5 S2: 詳細の API の challenge に dependencies・dependents が載り、CLI の show と等しい。
func TestBrowseAPI_ShowChallengeCarriesDependencies(t *testing.T) {
	ws := browseWorkspace(t)
	coretest.InsertDependency(t, ws, 2, 1)
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})

	for _, tc := range []struct {
		id         string
		deps       int
		dependents []any
	}{{"C-2", 1, []any{}}, {"C-1", 0, []any{"C-2"}}} {
		code, body := apiGet(t, port, "/api/v1/workspaces/w/challenges/"+tc.id)
		if code != http.StatusOK {
			t.Fatalf("show %s = %d %s", tc.id, code, body)
		}
		ch := decodeMap(t, body)["challenge"].(map[string]any)
		deps, ok := ch["dependencies"].([]any)
		if !ok || len(deps) != tc.deps {
			t.Errorf("%s dependencies = %#v, want %d elements", tc.id, ch["dependencies"], tc.deps)
		}
		if !reflect.DeepEqual(ch["dependents"], tc.dependents) {
			t.Errorf("%s dependents = %#v, want %#v", tc.id, ch["dependents"], tc.dependents)
		}
		if tc.deps == 1 {
			d := deps[0].(map[string]any)
			if d["challenge_id"] != "C-1" || d["satisfied"] != false || d["status"] != "awaiting_plan_approval" {
				t.Errorf("dependency = %#v", d)
			}
			assertKeys(t, "dependency", d, "challenge_id", "policy_state", "satisfied", "status", "upstream_state")
		}
	}
}
