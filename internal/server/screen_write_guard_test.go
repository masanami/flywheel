package server

import (
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// 区切り S4d-2 で足した書き込みの 5 つの口。許可表（web/src/readonly.test.tsx）と同じ集合。
var screenWriteRoutes = []string{
	"/api/v1/workspaces/a/challenges/C-1/holds/H-1/answer",
	"/api/v1/workspaces/a/ingest",
	"/api/v1/workspaces/a/cycle/stop",
	"/api/v1/workspaces/a/followups/done",
	"/api/v1/workspaces/a/followups/drop",
}

// postWith は、CSRF の値（cookie とヘッダ）を正しく付けたうえで、Host と Origin だけを指定して POST する。
func postWith(t *testing.T, port int, path, token, host, origin string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("POST", "http://127.0.0.1:"+strconv.Itoa(port)+path, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	req.AddCookie(&http.Cookie{Name: "flywheel_csrf_" + strconv.Itoa(port), Value: token})
	req.Header.Set(CSRFHeader, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// AC-189: 足した書き込みの 5 つの口は、Host と Origin の検査を通らない要求を、CSRF の値が正しくても 403 で断り、ストアを開かない。
func TestScreenWriteRoutes_RejectBadHostAndOrigin(t *testing.T) {
	ws := initWorkspace(t)
	_, port := startFleetServer(t, core.FleetWorkspace{Name: "a", Path: ws})
	token, _ := fetchToken(t, port)
	p := strconv.Itoa(port)
	good := "127.0.0.1:" + p
	before := snapshotDir(t, filepath.Join(ws, ".flywheel"))
	for _, path := range screenWriteRoutes {
		for name, tc := range map[string][2]string{
			"別の Host":          {"evil.example:" + p, ""},
			"別の Origin":        {good, "http://evil.example"},
			"Origin が null":    {good, "null"},
			"Origin のポートが違う":   {good, "http://localhost"},
			"Host も Origin も別": {"evil.example:" + p, "http://evil.example"},
		} {
			code, body := postWith(t, port, path, token, tc[0], tc[1])
			if code != http.StatusForbidden || !strings.Contains(body, CodeForbiddenOrigin) {
				t.Errorf("POST %s (%s): %d %q, want 403 forbidden_origin", path, name, code, body)
			}
		}
	}
	// 断った要求だけでは、ストアのファイル（-wal・-shm を含む）が変わらない。
	if after := snapshotDir(t, filepath.Join(ws, ".flywheel")); !reflect.DeepEqual(before, after) {
		t.Errorf("store directory changed after rejected requests:\nbefore=%v\nafter=%v", keys(before), keys(after))
	}
	// 検査を通る Host・Origin なら、検査の外へ進む（403 forbidden_origin ではない）。
	for _, path := range screenWriteRoutes {
		if code, body := postWith(t, port, path, token, good, "http://127.0.0.1:"+p); code == http.StatusForbidden {
			t.Errorf("POST %s with a good Host and Origin was rejected: %s", path, body)
		}
	}
}
