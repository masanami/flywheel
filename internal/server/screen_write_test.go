package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// 区切り S4d-2 の T4: 取り込み・サイクルを止める・フォローアップを済みにする／捨てる・知らせ・サイクルの口の足し値を、実ストアと実際の HTTP の要求で確かめる。

const ingestIssue7 = `[{"number":7,"title":"取り込める","body":"","user":{"login":"me"},"assignees":[],"labels":[],"state":"open","html_url":"https://github.com/o/r/issues/7","repository_url":"https://api.github.com/repos/o/r","comments":0,"updated_at":"2026-01-01T00:00:00Z"}]`

func ingestWorkspace(t *testing.T) string {
	t.Helper()
	ws := initWorkspaceWithWork(t)
	if err := os.WriteFile(filepath.Join(ws, ".flywheel", "sources.json"), []byte(ingestableTestSources), 0o644); err != nil {
		t.Fatal(err)
	}
	return ws
}

func withUpstream(t *testing.T, listBody string) {
	t.Helper()
	routes := map[string]string{"repos/o/r/issues?state=open&per_page=100&page=1": listBody}
	dir, _ := writeFakeGH(t, routes)
	withGHOnPATH(t, dir)
}

func challengeCount(t *testing.T, ws string) int {
	t.Helper()
	cs, err := openStore(t, ws).ListChallenges(context.Background(), core.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return len(cs)
}

// AC-53〜: 取り込みが成立すると課題ができ、サイクルを始める処理が 1 回、きっかけ ingest で呼ばれ、応答に ingested と cycle が載る。
func TestIngestAPI_CreatesChallengeAndStartsTheCycleOnce(t *testing.T) {
	ws := ingestWorkspace(t)
	withUpstream(t, ingestIssue7)
	port, rec := recordingServer(t, ws, CycleLaunch{Started: true})
	before := challengeCount(t, ws)
	code, body := apiPost(t, port, "/api/v1/workspaces/w/ingest", `{"items":["o/r#7"]}`)
	if code != http.StatusOK {
		t.Fatalf("ingest = %d %s", code, body)
	}
	var doc struct {
		Cycle    struct{ Started bool } `json:"cycle"`
		Ingested []struct {
			ChallengeID string `json:"challenge_id"`
			ExternalKey string `json:"external_key"`
		} `json:"ingested"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Ingested) != 1 || doc.Ingested[0].ExternalKey != "o/r#7" || !doc.Cycle.Started {
		t.Fatalf("body = %s", body)
	}
	if challengeCount(t, ws) != before+1 {
		t.Errorf("challenge not created")
	}
	if rec.calls != 1 || rec.trigger != core.CycleTriggerIngest {
		t.Errorf("calls=%d trigger=%q, want 1 / ingest", rec.calls, rec.trigger)
	}
	// 作業ログの経路は ui。
	log, err := openStore(t, ws).ListActivities(context.Background(), &doc.Ingested[0].ChallengeID)
	if err != nil || len(log) == 0 || log[0].Channel != string(core.ChannelUI) {
		t.Errorf("activities = %+v, %v; want channel ui", log, err)
	}
}

// 空の指定・取り込めるものでない指定・取り込み元の宣言が無いときは 400 で、課題は 1 つもできず、サイクルも始めない。
func TestIngestAPI_BadRequestsCreateNothing(t *testing.T) {
	ws := ingestWorkspace(t)
	withUpstream(t, ingestIssue7)
	port, rec := recordingServer(t, ws, CycleLaunch{Started: true})
	before := challengeCount(t, ws)
	for name, body := range map[string]string{
		"empty":          `{"items":[]}`,
		"not ingestable": `{"items":["o/r#7","o/r#99"]}`,
		"malformed spec": `{"items":["nonsense"]}`,
		"duplicated":     `{"items":["o/r#7","o/r#7"]}`,
		"bad body":       `{"items":"x"}`,
	} {
		code, resp := apiPost(t, port, "/api/v1/workspaces/w/ingest", body)
		if code != http.StatusBadRequest || !strings.Contains(resp, CodeValidationFailed) {
			t.Errorf("%s: %d %s, want 400 validation_failed", name, code, resp)
		}
	}
	if challengeCount(t, ws) != before || rec.calls != 0 {
		t.Errorf("challenges %d→%d, starts=%d; want nothing", before, challengeCount(t, ws), rec.calls)
	}
	// 宣言が無い。
	ws2 := initWorkspaceWithWork(t)
	port2, _ := recordingServer(t, ws2, CycleLaunch{})
	code, resp := apiPost(t, port2, "/api/v1/workspaces/w/ingest", `{"items":["o/r#7"]}`)
	if code != http.StatusBadRequest || !strings.Contains(resp, CodeValidationFailed) {
		t.Errorf("no declaration: %d %s", code, resp)
	}
}

// 上流を読めないときは 502 upstream_unavailable で、ストアは変わらず、サイクルも始めない。
func TestIngestAPI_UpstreamFailureIs502(t *testing.T) {
	ws := ingestWorkspace(t)
	dir, _ := writeFakeGH(t, map[string]string{}) // 路が無い → gh の取得が失敗する
	withGHOnPATH(t, dir)
	port, rec := recordingServer(t, ws, CycleLaunch{Started: true})
	before := challengeCount(t, ws)
	code, resp := apiPost(t, port, "/api/v1/workspaces/w/ingest", `{"items":["o/r#7"]}`)
	if code != http.StatusBadGateway || !strings.Contains(resp, CodeUpstreamUnavailable) {
		t.Fatalf("%d %s, want 502 upstream_unavailable", code, resp)
	}
	if challengeCount(t, ws) != before || rec.calls != 0 {
		t.Errorf("store changed or cycle started")
	}
}

// 走っているサイクルがあれば、重ねて始めない（already_running）。
func TestIngestAPI_DoesNotStartAgainWhileRunning(t *testing.T) {
	ws := ingestWorkspace(t)
	withUpstream(t, ingestIssue7)
	port, rec := recordingServer(t, ws, CycleLaunch{AlreadyRunning: true})
	code, body := apiPost(t, port, "/api/v1/workspaces/w/ingest", `{"items":["o/r#7"]}`)
	if code != http.StatusOK || !strings.Contains(body, `"already_running"`) {
		t.Fatalf("%d %s", code, body)
	}
	if rec.calls != 1 {
		t.Errorf("calls = %d", rec.calls)
	}
}

// 走っていないときは、200 で走っていない旨（requested=false・running=false）を返し、記録は変わらない。
func TestCycleStopAPI_NotRunning(t *testing.T) {
	ws := initWorkspaceWithWork(t)
	port, _ := recordingServer(t, ws, CycleLaunch{})
	code, body := apiPost(t, port, "/api/v1/workspaces/w/cycle/stop", ``)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	m := decodeMap(t, body)
	if m["requested"] != false || m["running"] != false || m["cycle_id"] != nil {
		t.Errorf("body = %s", body)
	}
}

// 走っているときは止める指示が記録され、サイクルの口の stop_requested が真になる。
func TestCycleStopAPI_RunningRecordsTheRequest(t *testing.T) {
	ws := initWorkspaceWithWork(t)
	if _, err := openStore(t, ws).BeginCycle(context.Background(), core.BeginCycleInput{Trigger: "manual", BudgetUSD: 1, Exclusive: true}); err != nil {
		t.Fatal(err)
	}
	port, _ := recordingServer(t, ws, CycleLaunch{})
	code, body := apiPost(t, port, "/api/v1/workspaces/w/cycle/stop", ``)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	m := decodeMap(t, body)
	if m["requested"] != true || m["running"] != true || m["cycle_id"] == nil {
		t.Fatalf("body = %s", body)
	}
	_, cb := apiGet(t, port, "/api/v1/workspaces/w/cycle")
	running, _ := decodeMap(t, cb)["running"].(map[string]any)
	if running == nil || running["stop_requested"] != true {
		t.Errorf("cycle = %s", cb)
	}
}

func openFollowup(t *testing.T, ws, title string) string {
	t.Helper()
	f, err := openStore(t, ws).CreateFollowup(context.Background(), core.ChannelCLI, core.FollowupInput{Title: title, Detail: "d", Source: core.FollowupSourceLearn})
	if err != nil {
		t.Fatal(err)
	}
	return f.ID
}

func followupStatus(t *testing.T, ws, id string) core.FollowupStatus {
	t.Helper()
	fs, err := openStore(t, ws).ListFollowups(context.Background(), core.FollowupFilter{Status: "all"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.ID == id {
			return f.Status
		}
	}
	t.Fatalf("no followup %s", id)
	return ""
}

// 済み／捨てるは、成立すれば変えたフォローアップを返し、作業ログ・承認の記録の経路は ui。
func TestFollowupsDoneAndDropAPI(t *testing.T) {
	ws := initWorkspaceWithWork(t)
	a, b := openFollowup(t, ws, "a"), openFollowup(t, ws, "b")
	port, _ := recordingServer(t, ws, CycleLaunch{})

	code, body := apiPost(t, port, "/api/v1/workspaces/w/followups/done", `{"ids":["`+a+`"],"note":"確かめた"}`)
	if code != http.StatusOK || !strings.Contains(body, `"status":"done"`) || followupStatus(t, ws, a) != core.FollowupStatusDone {
		t.Fatalf("done = %d %s", code, body)
	}
	code, body = apiPost(t, port, "/api/v1/workspaces/w/followups/drop", `{"ids":["`+b+`"],"reason":"不要"}`)
	if code != http.StatusOK || !strings.Contains(body, `"status":"dropped"`) || followupStatus(t, ws, b) != core.FollowupStatusDropped {
		t.Fatalf("drop = %d %s", code, body)
	}
	acts, err := openStore(t, ws).ListActivities(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, x := range acts {
		if x.Action == "followup_done" || x.Action == "followup_drop" {
			if x.Channel != string(core.ChannelUI) {
				t.Errorf("%s channel = %q, want ui", x.Action, x.Channel)
			}
			seen[x.Action] = true
		}
	}
	if !seen["followup_done"] || !seen["followup_drop"] {
		t.Errorf("activities = %+v", acts)
	}
}

// 空の文・未でないものを含む ID・空の ids・同じ ID の 2 度の指定は 400 で、どれも変えない（全部か無しか）。
func TestFollowupsDoneAndDropAPI_AllOrNothing(t *testing.T) {
	ws := initWorkspaceWithWork(t)
	open1, open2, closed := openFollowup(t, ws, "o1"), openFollowup(t, ws, "o2"), openFollowup(t, ws, "c")
	if _, err := openStore(t, ws).CompleteFollowups(context.Background(), core.ChannelCLI, []string{closed}, "済"); err != nil {
		t.Fatal(err)
	}
	port, _ := recordingServer(t, ws, CycleLaunch{})
	for _, tc := range []struct{ path, body string }{
		{"done", `{"ids":["` + open1 + `"],"note":" "}`},
		{"done", `{"ids":["` + open1 + `","` + closed + `"],"note":"x"}`},
		{"done", `{"ids":[],"note":"x"}`},
		{"done", `{"ids":["` + open1 + `","` + open1 + `"],"note":"x"}`},
		{"drop", `{"ids":["` + open1 + `"],"reason":""}`},
		{"drop", `{"ids":["` + open1 + `","` + open2 + `","` + closed + `"],"reason":"x"}`},
		{"drop", `{"ids":[],"reason":"x"}`},
	} {
		code, body := apiPost(t, port, "/api/v1/workspaces/w/followups/"+tc.path, tc.body)
		if code != http.StatusBadRequest {
			t.Errorf("%s %s: %d %s, want 400", tc.path, tc.body, code, body)
		}
	}
	if followupStatus(t, ws, open1) != core.FollowupStatusOpen || followupStatus(t, ws, open2) != core.FollowupStatusOpen {
		t.Errorf("an open followup was changed")
	}
}

// 知らせは作業ログとサイクルの記録から導き、種類と値だけを返す（文は持たない）。会話・発言の表には書かない。
func TestNoticesAPI_DerivedFromRecordsAndWritesNothing(t *testing.T) {
	ws := initWorkspaceWithWork(t)
	st := openStore(t, ws)
	c, err := st.BeginCycle(context.Background(), core.BeginCycleInput{Trigger: "manual", BudgetUSD: 1, Exclusive: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EndCycleWithReason(context.Background(), c.ID, core.CycleResultCompleted, core.CycleEndStopped); err != nil {
		t.Fatal(err)
	}
	port, _ := recordingServer(t, ws, CycleLaunch{})
	code, body := apiGet(t, port, "/api/v1/workspaces/w/notices")
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	ns, _ := decodeMap(t, body)["notices"].([]any)
	if len(ns) != 1 {
		t.Fatalf("notices = %s", body)
	}
	n := ns[0].(map[string]any)
	assertKeys(t, "notice", n, "added_count", "at", "challenge_id", "challenge_title", "cycle_id", "end_reason", "kind", "origin", "task_id", "task_title")
	if n["kind"] != "cycle_ended" || n["end_reason"] != "stopped" || n["cycle_id"] != c.ID || n["challenge_id"] != nil || n["task_title"] != nil {
		t.Errorf("notice = %v", n)
	}
	// 何も書かない（2 度読んでも同じ）。
	_, body2 := apiGet(t, port, "/api/v1/workspaces/w/notices")
	if body2 != body {
		t.Errorf("second read differs: %s vs %s", body2, body)
	}
}

// 知らせの口は読み取りだけ（POST は 404）。
func TestNoticesAPI_PostIs404(t *testing.T) {
	ws := initWorkspaceWithWork(t)
	port, _ := recordingServer(t, ws, CycleLaunch{})
	if code, _ := apiPost(t, port, "/api/v1/workspaces/w/notices", ``); code != http.StatusNotFound {
		t.Errorf("POST notices = %d", code)
	}
}

// AC-80: サイクルの口には supervisor_actions・costs が足され、ターミナルの `cycle status --json` には足されない。
func TestCycleAPI_AddsSupervisorActionsAndCostsButCLIIsUnchanged(t *testing.T) {
	ws := initWorkspaceWithWork(t)
	port, _ := recordingServer(t, ws, CycleLaunch{})
	code, body := apiGet(t, port, "/api/v1/workspaces/w/cycle")
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	m := decodeMap(t, body)
	for _, k := range []string{"supervisor_actions", "costs"} {
		if a, ok := m[k].([]any); !ok || len(a) != 0 {
			t.Errorf("%s = %v, want []", k, m[k])
		}
	}
	cli := decodeMap(t, cliJSON(t, ws, "cycle", "status"))
	for _, k := range []string{"supervisor_actions", "costs"} {
		if _, ok := cli[k]; ok {
			t.Errorf("cycle status --json has %s", k)
		}
	}
	for k := range cli {
		if _, ok := m[k]; !ok {
			t.Errorf("API lacks CLI key %s", k)
		}
	}
}

// サイクルの口の supervisor_actions は、対象のサイクルの手を、課題の題名つきで返す。
func TestCycleAPI_SupervisorActionsOfTheTargetCycle(t *testing.T) {
	ws := browseWorkspace(t)
	cycle := coretest.InsertCycle(t, ws, "manual", 0, "2026-11-22T00:00:00.000Z")
	coretest.InsertSupervisorAction(t, ws, cycle, 2, "verify_uncertain", "reverify", "executed", "調べたこと", "2026-11-22T00:01:00.000Z")
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	code, body := apiGet(t, port, "/api/v1/workspaces/w/cycle")
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	acts, _ := decodeMap(t, body)["supervisor_actions"].([]any)
	if len(acts) != 1 {
		t.Fatalf("supervisor_actions = %s", body)
	}
	a := acts[0].(map[string]any)
	assertKeys(t, "action", a, "at", "challenge_id", "challenge_title", "findings", "hand", "id", "reject_reason", "result", "task_id", "task_title")
	if a["challenge_id"] != "C-2" || a["challenge_title"] == "" || a["hand"] != "reverify" || a["result"] != "executed" || a["findings"] != "調べたこと" || a["task_id"] != nil {
		t.Errorf("action = %v", a)
	}
}
