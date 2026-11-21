package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
	"github.com/masanami/flywheel/internal/server"
)

// AC-233: 課題の詳細の API の応答は、`flywheel show --json` と同じキーを持つ（タスクの枠・使った額と統合先の識別を含め、
// 同じ課題について、challenge と tasks[] の全キーと値が一致する）。同じ実ストアを、CLI と実際の API の経路の両方で読んで比べる。
func TestChallengeDetailAPI_MatchesShowJSONKeysAndValues(t *testing.T) {
	t.Parallel()
	ws, id := budgetShownWorkspace(t)
	coretest.Exec(t, ws, `UPDATE challenge SET integration_ref = 'flywheel/integration/c-1' WHERE id = 1`)

	coretest.Exec(t, ws, `INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, requested_kind, facts,
		land_ref, verified_ref, max_budget_usd, started_at, observed_at, task_id, kind)
		VALUES (1, 'tool', 'ext-1', 1, 'waiting', 'ok', 'review', '{"self_review":"complete"}', NULL, 'v1', 1, '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z', 1, 'task')`)
	coretest.Exec(t, ws, `INSERT INTO run_artifact (run_id, kind, ref, state, role) VALUES (1, 'pr', 'https://example.test/pr/1', 'open', NULL), (1, 'branch', 'flywheel/integration/c-1', NULL, 'integration')`)
	coretest.Exec(t, ws, `INSERT INTO activity (at, actor, channel, verification, entity, entity_id, action, after)
		VALUES ('2026-11-01T00:00:02.000Z', 'x', 'invoker', 'none', 'task', 1, 'task_verify', '{"verdict":"met","reason":"ok"}')`)
	api := apiDetail(t, ws, id)
	cli := runJSON(t, ws, "show", id, "--json")

	apiCh, cliCh := api["challenge"].(map[string]any), cli["challenge"].(map[string]any)
	if !reflect.DeepEqual(apiCh, cliCh) {
		t.Errorf("challenge differs\napi: %v\ncli: %v", apiCh, cliCh)
	}
	if apiCh["integration_ref"] != "flywheel/integration/c-1" {
		t.Errorf("challenge.integration_ref = %v", apiCh["integration_ref"])
	}
	apiTasks, cliTasks := objectList(t, api["tasks"], "api tasks"), objectList(t, cli["tasks"], "cli tasks")
	if len(apiTasks) == 0 || !reflect.DeepEqual(apiTasks, cliTasks) {
		t.Errorf("tasks differ\napi: %v\ncli: %v", apiTasks, cliTasks)
	}
	for _, k := range []string{"budget_impl_usd", "budget_review_usd", "spent_usd"} {
		if _, ok := apiTasks[0][k].(float64); !ok {
			t.Errorf("api tasks[0].%s = %#v, want a number", k, apiTasks[0][k])
		}
	}
	if apiTasks[0]["spent_usd"] != 3.5 || apiTasks[0]["budget_impl_usd"] != float64(30) {
		t.Errorf("api tasks[0] = %v, want spent 3.5 and impl frame 30", apiTasks[0])
	}
	d, _ := apiTasks[0]["delegate"].(map[string]any)
	if d == nil || d["state"] != "waiting" || d["summary"] != "ok" || d["requested_kind"] != "review" || d["run_id"] != "R-1" {
		t.Errorf("api tasks[0].delegate = %v", apiTasks[0]["delegate"])
	}
	if f, _ := apiTasks[0]["facts"].(map[string]any); f["self_review"] != "complete" {
		t.Errorf("api tasks[0].facts = %v", apiTasks[0]["facts"])
	}
	if arts, _ := apiTasks[0]["artifacts"].([]any); len(arts) != 2 || arts[1].(map[string]any)["role"] != "integration" || arts[0].(map[string]any)["role"] != nil {
		t.Errorf("api tasks[0].artifacts = %v", apiTasks[0]["artifacts"])
	}
	if v, _ := apiTasks[0]["verification"].(map[string]any); v["verdict"] != "met" || v["reason"] != "ok" || v["at"] != "2026-11-01T00:00:02.000Z" {
		t.Errorf("api tasks[0].verification = %v", apiTasks[0]["verification"])
	}
	if apiTasks[0]["landing"] != "verified" {
		t.Errorf("api tasks[0].landing = %v, want verified", apiTasks[0]["landing"])
	}
	if apiTasks[1]["delegate"] != nil || apiTasks[1]["landing"] != nil || apiTasks[1]["verification"] != nil {
		t.Errorf("api tasks[1] = %v, want null delegate/landing/verification", apiTasks[1])
	}
}

// apiDetail は課題の詳細の API を、実際の経路で GET して JSON の対応として返す。
func apiDetail(t *testing.T, ws, id string) map[string]any {
	t.Helper()
	ln, err := server.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New(ln, []core.FleetWorkspace{{Name: "w", Path: ws}})
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve() }()
	t.Cleanup(func() {
		_ = srv.Shutdown()
		<-errc
	})
	resp, err := http.Get("http://" + ln.Addr().String() + "/api/v1/workspaces/w/challenges/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("GET = %d %s", resp.StatusCode, body)
	}
	var api map[string]any
	if err := json.Unmarshal(body, &api); err != nil {
		t.Fatal(err)
	}
	return api
}

// AC-61・AC-62: 課題の詳細の API の runs[] は、21 件なら 21 件、101 件なら新しい順の 100 件。
func TestChallengeDetailAPI_RunsLimitIs100NewestFirst(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ n, want int }{{21, 21}, {101, 100}} {
		ws := initializedWorkspace(t)
		id := createForCase(t, ws)
		insertEndedRuns(t, ws, id, c.n)
		runs := objectList(t, apiDetail(t, ws, id)["runs"], "api runs")
		if len(runs) != c.want {
			t.Fatalf("%d runs: len(runs[]) = %d, want %d", c.n, len(runs), c.want)
		}
		if runs[0]["id"] != fmt.Sprintf("R-%d", c.n) || runs[len(runs)-1]["id"] != fmt.Sprintf("R-%d", c.n-c.want+1) {
			t.Errorf("%d runs: first/last = %v / %v, want newest first", c.n, runs[0]["id"], runs[len(runs)-1]["id"])
		}
	}
}
