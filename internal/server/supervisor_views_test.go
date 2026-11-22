package server

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// S4b-2: 状況の API・課題の詳細の API・承認待ちの API が、スーパーバイザーの値を `flywheel status`・`flywheel show` と同じキーで出す。

func idsIn(t *testing.T, v any) []string {
	t.Helper()
	out := []string{}
	for _, e := range v.([]any) {
		out = append(out, e.(map[string]any)["id"].(string))
	}
	return out
}

func TestStatusAPI_SupervisorPendingIsCheckingNotOwnerWaiting_AC235_AC236(t *testing.T) {
	ws := browseWorkspace(t) // C-1（計画承認待ち）・C-2
	at := "2026-11-22T00:00:00.000Z"
	coretest.SetChallengeStatus(t, ws, 1, "awaiting_human")
	coretest.SetChallengeStatus(t, ws, 2, "awaiting_human")
	coretest.InsertSupervisorHold(t, ws, 1, "q1", "verifying", at, "verify_uncertain", "pending", "", "")
	coretest.InsertSupervisorHold(t, ws, 2, "q2", "verifying", at, "verify_uncertain", "escalated", "調べた", "推奨")
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})

	code, body := apiGet(t, port, "/api/v1/workspaces/w/status")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	doc := decodeMap(t, body)
	if got := idsIn(t, doc["needs_human"].(map[string]any)["challenges"]); !reflect.DeepEqual(got, []string{"C-2"}) {
		t.Errorf("needs_human.challenges = %v, want [C-2]", got)
	}
	checking := doc["supervisor_checking"].(map[string]any)
	if got := idsIn(t, checking["challenges"]); !reflect.DeepEqual(got, []string{"C-1"}) || checking["count"] != float64(1) {
		t.Errorf("supervisor_checking = %v, want C-1 の 1 件", checking)
	}
	_, ibody := statusRequestIngestable(t, port, "w")
	sameJSON(t, ibody, cliStatusJSON(t, ws))

	// 課題の詳細の API: 保留の値と supervisor_actions（無ければ []）。
	code, detail := apiGet(t, port, "/api/v1/workspaces/w/challenges/C-2")
	if code != 200 {
		t.Fatalf("%d %s", code, detail)
	}
	d := decodeMap(t, detail)
	h := d["holds"].([]any)[0].(map[string]any)
	for k, v := range map[string]any{"stop_kind": "verify_uncertain", "supervisor_state": "escalated", "supervisor_findings": "調べた", "supervisor_recommendation": "推奨"} {
		if h[k] != v {
			t.Errorf("hold[%s] = %v, want %v", k, h[k], v)
		}
	}
	if v, ok := d["supervisor_actions"].([]any); !ok || len(v) != 0 {
		t.Errorf("supervisor_actions = %#v, want []", d["supervisor_actions"])
	}
}

func TestChallengeAPI_SupervisorActionsHaveTheSameKeysAsShowJSON_AC230(t *testing.T) {
	ws := browseWorkspace(t)
	cycle := coretest.InsertCycle(t, ws, "manual", 0, "2026-11-22T00:00:00.000Z")
	coretest.InsertSupervisorAction(t, ws, cycle, 2, "verify_uncertain", "reverify", "executed", "f", "2026-11-22T00:01:00.000Z")
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})

	code, body := apiGet(t, port, "/api/v1/workspaces/w/challenges/C-2")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	api := decodeMap(t, body)
	acts := api["supervisor_actions"].([]any)
	if len(acts) != 1 {
		t.Fatalf("supervisor_actions = %v", acts)
	}
	if !reflect.DeepEqual(api["supervisor_hands_taken"], map[string]any{"reverify": float64(1)}) {
		t.Errorf("supervisor_hands_taken = %v", api["supervisor_hands_taken"])
	}
	cli := decodeMap(t, cliJSON(t, ws, "show", "C-2"))
	if !reflect.DeepEqual(cli["supervisor_actions"], api["supervisor_actions"]) {
		t.Errorf("API の supervisor_actions が CLI の show --json と違う:\n api=%v\n cli=%v", api["supervisor_actions"], cli["supervisor_actions"])
	}
}

// 承認待ちの API の supervisor_hands_taken は、結果が executed の対処の記録を手ごとに数える（failed は入れない）。
func TestApprovalAPI_HandsTakenCountsExecutedActions_AC243(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "awaiting_completion_approval")
	cycle := coretest.InsertCycle(t, ws, "manual", 0, "2026-11-22T00:00:00.000Z")
	for i, c := range []struct{ hand, result string }{{"reverify", "executed"}, {"reverify", "executed"}, {"defer", "executed"}, {"reverify", "failed"}} {
		coretest.InsertSupervisorAction(t, ws, cycle, 1, "verify_uncertain", c.hand, c.result, "f", "2026-11-22T00:0"+string(rune('1'+i))+":00.000Z")
	}
	port := approvalServer(t, ws)
	code, body := apiGet(t, port, approvalPath)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	got := decodeMap(t, body)["approval"].(map[string]any)["supervisor_hands_taken"]
	if !reflect.DeepEqual(got, map[string]any{"reverify": float64(2), "defer": float64(1)}) {
		t.Errorf("supervisor_hands_taken = %v", got)
	}
}
