package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは課題 #773（区切り S2b）のフォローアップの一覧の API を、実ストアと実際の HTTP の要求で確かめる。

func followupWorkspace(t *testing.T) string {
	t.Helper()
	ws := browseWorkspace(t)
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{ChallengeID: 1, Title: "未の 1", Detail: "説明"})
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{ChallengeID: 1, Title: "済みの 1", Status: "done", Note: "確かめた"})
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{ChallengeID: 2, Title: "捨てた 1", Status: "dropped", Reason: "不要"})
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{Title: "課題に属さない"})
	return ws
}

// AC-45 #773: status なしの応答は、同じストアでの `flywheel followup list --json` と同じ。
func TestFollowupsAPI_AC45_EqualsCLIDefault(t *testing.T) {
	ws := followupWorkspace(t)
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	code, body := apiGet(t, port, "/api/v1/workspaces/w/followups")
	if code != http.StatusOK {
		t.Fatalf("GET = %d %s", code, body)
	}
	sameJSON(t, body, cliJSON(t, ws, "followup", "list"))
	if n := len(decodeMap(t, body)["followups"].([]any)); n != 2 {
		t.Errorf("default list has %d items, want the 2 open ones", n)
	}
}

// AC-46 #773: status=all の応答は、`flywheel followup list --status all --json` と同じ。
func TestFollowupsAPI_AC46_StatusAllEqualsCLI(t *testing.T) {
	ws := followupWorkspace(t)
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	code, body := apiGet(t, port, "/api/v1/workspaces/w/followups?status=all")
	if code != http.StatusOK {
		t.Fatalf("GET = %d %s", code, body)
	}
	sameJSON(t, body, cliJSON(t, ws, "followup", "list", "--status", "all"))
	// challenge の絞り込みもフラグと同じ意味。
	code, body = apiGet(t, port, "/api/v1/workspaces/w/followups?status=all&challenge=C-1")
	if code != http.StatusOK {
		t.Fatalf("GET = %d %s", code, body)
	}
	sameJSON(t, body, cliJSON(t, ws, "followup", "list", "--status", "all", "--challenge", "C-1"))
}

func TestFollowupsAPI_Errors(t *testing.T) {
	ws := followupWorkspace(t)
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	for path, want := range map[string]string{
		"/api/v1/workspaces/w/followups?challenge=C-99": "not_found",
		"/api/v1/workspaces/w/followups?status=bogus":   "validation_failed",
		"/api/v1/workspaces/w/followups?status=":        "validation_failed",
		"/api/v1/workspaces/nope/followups":             "not_found",
	} {
		code, body := apiGet(t, port, path)
		var doc struct {
			Error struct{ Code string } `json:"error"`
		}
		_ = json.Unmarshal([]byte(body), &doc)
		if code < 400 || doc.Error.Code != want {
			t.Errorf("GET %s = %d %s, want error %s", path, code, body, want)
		}
	}
}

// 書き込みの口は無い（GET 以外は受けない）。
func TestFollowupsAPI_ReadOnly(t *testing.T) {
	ws := followupWorkspace(t)
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	before := dumpRows(t, ws)
	if code, body := apiGet(t, port, "/api/v1/workspaces/w/followups?status=all"); code != 200 {
		t.Fatalf("GET = %d %s", code, body)
	}
	if dumpRows(t, ws) != before {
		t.Error("GET followups changed rows")
	}
}

// AC-294 #773: 課題の詳細の API の根に followups があり、課題の全部（状態を問わず）を `show --json` と同じ形で持つ。
func TestChallengeAPI_AC294_DetailHasFollowups(t *testing.T) {
	ws := followupWorkspace(t)
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	code, body := apiGet(t, port, "/api/v1/workspaces/w/challenges/C-1")
	if code != http.StatusOK {
		t.Fatalf("GET = %d %s", code, body)
	}
	m := decodeMap(t, body)
	l, ok := m["followups"].([]any)
	if !ok || len(l) != 2 {
		t.Fatalf("AC-294: followups = %#v, want 2 elements", m["followups"])
	}
	var cli map[string]any
	if err := json.Unmarshal([]byte(cliJSON(t, ws, "show", "C-1")), &cli); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(m["followups"])
	b, _ := json.Marshal(cli["followups"])
	sameJSON(t, string(a), string(b))
}

// AC-312 #773: m4-ui-server.md の API の表にフォローアップの一覧の口がある。
func TestSpec_AC312_APITableHasFollowupsRoute(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "features", "m4-ui-server.md"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "| `GET /api/v1/workspaces/{name}/followups") {
			found = true
		}
	}
	if !found {
		t.Error("AC-312: the API table has no row for GET /api/v1/workspaces/{name}/followups")
	}
}
