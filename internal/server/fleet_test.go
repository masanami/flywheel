package server

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// initWorkspace は t.TempDir() に core.Init 済みのワークスペースを作る。
func initWorkspace(t *testing.T) string {
	t.Helper()
	ws := t.TempDir()
	if _, err := core.Init(ws); err != nil {
		t.Fatal(err)
	}
	return ws
}

// snapshotDir は dir 直下のファイル名と内容を返す。
func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = string(b)
	}
	return out
}

func startFleetServer(t *testing.T, ws ...core.FleetWorkspace) (*Server, int) {
	t.Helper()
	ln, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	s := New(ln, ws)
	errc := make(chan error, 1)
	go func() { errc <- s.Serve() }()
	t.Cleanup(func() {
		_ = s.Shutdown()
		if err := <-errc; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return s, ln.Addr().(*net.TCPAddr).Port
}

func getPath(t *testing.T, port int, path, host, origin string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("GET", "http://127.0.0.1:"+strconv.Itoa(port)+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

type wsEntry struct {
	Name  string  `json:"name"`
	Path  string  `json:"path"`
	State string  `json:"state"`
	Error *string `json:"error"`
}

func listWorkspaces(t *testing.T, port int) (entries []wsEntry, raw string) {
	t.Helper()
	code, body := getPath(t, port, "/api/v1/workspaces", "127.0.0.1:"+strconv.Itoa(port), "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	var doc struct {
		Workspaces []wsEntry `json:"workspaces"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	return doc.Workspaces, body
}

func stateOf(t *testing.T, port int, name string) wsEntry {
	t.Helper()
	entries, _ := listWorkspaces(t, port)
	for _, e := range entries {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("workspace %q not listed", name)
	return wsEntry{}
}

func latestVersion(t *testing.T) int {
	t.Helper()
	v, err := core.LatestSchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestWorkspacesAPI_ListsInDeclarationOrderWithNullError(t *testing.T) {
	a, b := initWorkspace(t), initWorkspace(t)
	_, port := startFleetServer(t,
		core.FleetWorkspace{Name: "zeta", Path: b},
		core.FleetWorkspace{Name: "alpha", Path: a})
	entries, raw := listWorkspaces(t, port)
	want := []wsEntry{{Name: "zeta", Path: b, State: "ok"}, {Name: "alpha", Path: a, State: "ok"}}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("entries = %+v, want %+v", entries, want)
	}
	var generic struct {
		Workspaces []map[string]json.RawMessage `json:"workspaces"`
	}
	_ = json.Unmarshal([]byte(raw), &generic)
	for _, w := range generic.Workspaces {
		if string(w["error"]) != "null" || len(w) != 4 {
			t.Errorf("entry = %v, want 4 keys and error null", w)
		}
	}
}

func TestWorkspacesAPI_EmptyFleetIsEmptyArray(t *testing.T) {
	_, port := startFleetServer(t)
	_, raw := listWorkspaces(t, port)
	if want := `{"workspaces":[]}`; raw != want+"\n" {
		t.Errorf("body = %q, want %q", raw, want)
	}
}

func TestWorkspacesAPI_MissingStoreDoesNotStopOthers(t *testing.T) {
	good, empty := initWorkspace(t), t.TempDir()
	s, port := startFleetServer(t,
		core.FleetWorkspace{Name: "empty", Path: empty},
		core.FleetWorkspace{Name: "good", Path: good})
	e := stateOf(t, port, "empty")
	if e.State != StateStoreNotFound || e.Error == nil || *e.Error == "" {
		t.Errorf("empty = %+v, want store_not_found with a message", e)
	}
	if g := stateOf(t, port, "good"); g.State != StateOK || g.Error != nil {
		t.Errorf("good = %+v", g)
	}
	st, release, le := s.fleet.Acquire(t.Context(), "good")
	if le != nil || st == nil {
		t.Fatalf("Acquire(good) = %v", le)
	}
	release()
	// ストアを作っていない（開けないだけ。server が作らない）。
	if _, err := os.Stat(filepath.Join(empty, ".flywheel")); !os.IsNotExist(err) {
		t.Errorf("server created %s/.flywheel (err=%v)", empty, err)
	}
}

func TestStoreTooNew_AtStartIsReportedAndFileUnchanged(t *testing.T) {
	ws := initWorkspace(t)
	coretest.SetStoreVersion(t, ws, latestVersion(t)+1)
	before := snapshotDir(t, filepath.Join(ws, ".flywheel"))
	s, port := startFleetServer(t, core.FleetWorkspace{Name: "new", Path: ws})
	for i := 0; i < 2; i++ { // 要求のたびに開き直しても変わらない
		if e := stateOf(t, port, "new"); e.State != StateStoreTooNew || e.Error == nil {
			t.Errorf("entry = %+v, want store_too_new", e)
		}
	}
	if _, _, le := s.fleet.Acquire(t.Context(), "new"); le == nil || le.Code != CodeStoreTooNew || le.Status != http.StatusServiceUnavailable {
		t.Errorf("Acquire = %+v, want 503 store_too_new", le)
	}
	if after := snapshotDir(t, filepath.Join(ws, ".flywheel")); !reflect.DeepEqual(before, after) {
		t.Errorf("store directory changed: before=%v after=%v", keys(before), keys(after))
	}
}

func TestStoreTooNew_AppearingWhileRunningFollowsAndRecovers(t *testing.T) {
	ws := initWorkspace(t)
	s, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	if e := stateOf(t, port, "w"); e.State != StateOK {
		t.Fatalf("state = %q, want ok", e.State)
	}
	coretest.SetStoreVersion(t, ws, latestVersion(t)+1)
	if e := stateOf(t, port, "w"); e.State != StateStoreTooNew {
		t.Fatalf("state = %q, want store_too_new", e.State)
	}
	if _, _, le := s.fleet.Acquire(t.Context(), "w"); le == nil || le.Code != CodeStoreTooNew {
		t.Errorf("Acquire = %+v, want store_too_new", le)
	}
	// 最新版のストアのファイルに置き換えると、次の要求から ok に戻る。
	replacement, err := os.ReadFile(filepath.Join(initWorkspace(t), ".flywheel", "flywheel.db"))
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(ws, ".flywheel", "flywheel.db")
	if err := os.WriteFile(dbPath+".tmp", replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dbPath+".tmp", dbPath); err != nil {
		t.Fatal(err)
	}
	if e := stateOf(t, port, "w"); e.State != StateOK || e.Error != nil {
		t.Errorf("state = %+v, want ok", e)
	}
	st, release, le := s.fleet.Acquire(t.Context(), "w")
	if le != nil || st == nil {
		t.Fatalf("Acquire = %v", le)
	}
	release()
}

func TestStoreCreatedAfterStartBecomesOK(t *testing.T) {
	ws := t.TempDir()
	s, port := startFleetServer(t, core.FleetWorkspace{Name: "late", Path: ws})
	if e := stateOf(t, port, "late"); e.State != StateStoreNotFound {
		t.Fatalf("state = %q, want store_not_found", e.State)
	}
	if _, _, le := s.fleet.Acquire(t.Context(), "late"); le == nil || le.Code != CodeStoreNotFound || le.Status != http.StatusNotFound {
		t.Errorf("Acquire = %+v, want 404 store_not_found", le)
	}
	if _, err := core.Init(ws); err != nil {
		t.Fatal(err)
	}
	if e := stateOf(t, port, "late"); e.State != StateOK {
		t.Errorf("state = %q, want ok", e.State)
	}
	st, release, le := s.fleet.Acquire(t.Context(), "late")
	if le != nil || st == nil {
		t.Fatalf("Acquire = %v", le)
	}
	release()
}

func TestStoreError_UnreadableStoreIsStoreError(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	junk := "this is not an sqlite database, just text padding it out to be long enough to look like a header"
	if err := os.WriteFile(filepath.Join(ws, ".flywheel", "flywheel.db"), []byte(junk), 0o600); err != nil {
		t.Fatal(err)
	}
	s, port := startFleetServer(t, core.FleetWorkspace{Name: "bad", Path: ws})
	if e := stateOf(t, port, "bad"); e.State != StateStoreError || e.Error == nil {
		t.Errorf("entry = %+v, want store_error", e)
	}
	if _, _, le := s.fleet.Acquire(t.Context(), "bad"); le == nil || le.Code != CodeStoreError || le.Status != http.StatusServiceUnavailable {
		t.Errorf("Acquire = %+v, want 503 store_error", le)
	}
}

func TestAcquire_UnknownNameIs404NotFound(t *testing.T) {
	s, _ := startFleetServer(t, core.FleetWorkspace{Name: "a", Path: initWorkspace(t)})
	_, _, le := s.fleet.Acquire(t.Context(), "nope")
	if le == nil || le.Status != http.StatusNotFound || le.Code != CodeNotFound {
		t.Errorf("Acquire = %+v, want 404 not_found", le)
	}
}

func TestAcquire_HoldsStoreWhileOK(t *testing.T) {
	ws := initWorkspace(t)
	s, _ := startFleetServer(t, core.FleetWorkspace{Name: "a", Path: ws})
	first, rel1, le := s.fleet.Acquire(t.Context(), "a")
	if le != nil {
		t.Fatal(le)
	}
	// 借りている間も、同じワークスペースの別の要求は並行に進める。
	second, rel2, le := s.fleet.Acquire(t.Context(), "a")
	if le != nil {
		t.Fatal(le)
	}
	if first != second {
		t.Error("an ok workspace's Store was not kept between requests")
	}
	rel1()
	rel1() // 冪等
	rel2()
}

func TestShutdownClosesHeldStores(t *testing.T) {
	ws := initWorkspace(t)
	ln, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	s := New(ln, []core.FleetWorkspace{{Name: "a", Path: ws}})
	go func() { _ = s.Serve() }()
	st, release, le := s.fleet.Acquire(t.Context(), "a")
	if le != nil {
		t.Fatal(le)
	}
	release()
	if _, err := st.SchemaVersion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SchemaVersion(t.Context()); err == nil {
		t.Error("the held Store was not closed by Shutdown")
	}
}

func TestStoreFileDeletedOrReplacedWhileOKIsFollowed(t *testing.T) {
	ws := initWorkspace(t)
	s, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	if e := stateOf(t, port, "w"); e.State != StateOK {
		t.Fatalf("state = %q", e.State)
	}
	dir := filepath.Join(ws, ".flywheel")
	if err := os.Rename(filepath.Join(dir, "flywheel.db"), filepath.Join(dir, "moved.db")); err != nil {
		t.Fatal(err)
	}
	if e := stateOf(t, port, "w"); e.State != StateStoreNotFound {
		t.Errorf("after delete: state = %q, want store_not_found", e.State)
	}
	// 別のファイルに置き換わったら、新しいファイルを開き直す。
	if err := os.Rename(filepath.Join(dir, "moved.db"), filepath.Join(dir, "flywheel.db")); err != nil {
		t.Fatal(err)
	}
	first, rel, le := s.fleet.Acquire(t.Context(), "w")
	if le != nil {
		t.Fatal(le)
	}
	rel()
	fresh := initWorkspace(t)
	b, err := os.ReadFile(filepath.Join(fresh, ".flywheel", "flywheel.db"))
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "flywheel.db")
	if err := os.WriteFile(dbPath+".tmp", b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dbPath+".tmp", dbPath); err != nil {
		t.Fatal(err)
	}
	second, rel, le := s.fleet.Acquire(t.Context(), "w")
	if le != nil {
		t.Fatal(le)
	}
	rel()
	if first == second {
		t.Error("a replaced store file was not reopened")
	}
}

// 要求の取り消し（クライアントの切断）は、ストアの状態ではない。
func TestCancelledRequestDoesNotChangeState(t *testing.T) {
	ws := initWorkspace(t)
	s, port := startFleetServer(t, core.FleetWorkspace{Name: "w", Path: ws})
	if e := stateOf(t, port, "w"); e.State != StateOK {
		t.Fatal(e.State)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, st := range s.fleet.Statuses(ctx) {
		if st.State != StateOK {
			t.Errorf("cancelled request changed state to %q", st.State)
		}
	}
	if e := stateOf(t, port, "w"); e.State != StateOK {
		t.Errorf("state = %q after a cancelled request, want ok", e.State)
	}
}
