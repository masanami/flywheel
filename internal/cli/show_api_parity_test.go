package cli

import (
	"encoding/json"
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
}
