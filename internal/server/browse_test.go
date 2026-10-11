package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
		req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d%s", port, p), nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Errorf("POST %s = %d, want 404", p, resp.StatusCode)
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
	assertKeys(t, "needs-human", m, "workspaces")
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

func TestBrowseAPI_QuestionsEmptyAndAnsweredExcluded(t *testing.T) {
	ws := browseWorkspace(t)
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	if _, body := apiGet(t, port, "/api/v1/questions"); strings.TrimSpace(body) != `{"questions":[]}` {
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
	assertKeys(t, "card", c, "state", "modifiers", "next_human_actions", "headline", "context")
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
	before := dumpRows(t, ws)
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
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
