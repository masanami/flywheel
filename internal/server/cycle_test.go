package server

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

// fakeExecutable は引数と作業ディレクトリを rec に書いてから長く眠る実行ファイルを作る。
// テストの終了時に子を止める。
func fakeExecutable(t *testing.T) (exe, rec string) {
	t.Helper()
	dir := t.TempDir()
	rec = filepath.Join(dir, "rec")
	exe = filepath.Join(dir, "fake-flywheel")
	script := "#!/bin/sh\n" +
		"echo $$ > '" + rec + ".pid'\n" +
		"{ echo \"$@\"; pwd -P; } > '" + rec + ".tmp'\n" +
		"mv '" + rec + ".tmp' '" + rec + "'\n" +
		"exec sleep 60\n"
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for i := 0; i < 40; i++ { // 起動済みの子が pid を書くのを短く待つ
			if _, err := os.Stat(rec + ".pid"); err == nil {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		if b, err := os.ReadFile(rec + ".pid"); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	return exe, rec
}

func startCycleServer(t *testing.T, exe, ws string) int {
	t.Helper()
	ln, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	s := newServerWithLauncher(ln, []core.FleetWorkspace{{Name: "w", Path: ws}}, eventsConfig{},
		execLauncher(func() (string, error) { return exe, nil }))
	errc := make(chan error, 1)
	go func() { errc <- s.Serve() }()
	t.Cleanup(func() {
		_ = s.Shutdown()
		<-errc
	})
	return ln.Addr().(*net.TCPAddr).Port
}

// countingServer は起動の回数を数えるだけの launcher で server を立てる。
func countingServer(t *testing.T, ws string) (port int, calls *int32) {
	t.Helper()
	ln, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	calls = new(int32)
	s := newServerWithLauncher(ln, []core.FleetWorkspace{{Name: "w", Path: ws}}, eventsConfig{},
		func(string) error { atomic.AddInt32(calls, 1); return nil })
	errc := make(chan error, 1)
	go func() { errc <- s.Serve() }()
	t.Cleanup(func() {
		_ = s.Shutdown()
		<-errc
	})
	return ln.Addr().(*net.TCPAddr).Port, calls
}

func waitFile(t *testing.T, p string) string {
	t.Helper()
	for i := 0; i < 200; i++ {
		if b, err := os.ReadFile(p); err == nil {
			return string(b)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("%s が書かれない", p)
	return ""
}

const cyclePath = "/api/v1/workspaces/w/cycle"

func TestCyclePostStartsProcessWithArgsAndCwd(t *testing.T) {
	ws := initWorkspace(t)
	exe, rec := fakeExecutable(t)
	port := startCycleServer(t, exe, ws)
	code, body := apiPost(t, port, cyclePath, "")
	if code != 200 || strings.TrimSpace(body) != `{"started":true}` {
		t.Fatalf("POST = %d %s", code, body)
	}
	lines := strings.Split(strings.TrimSpace(waitFile(t, rec)), "\n")
	if len(lines) != 2 {
		t.Fatalf("rec = %q", lines)
	}
	wsReal, _ := filepath.EvalSymlinks(ws)
	gotWs, _ := filepath.EvalSymlinks(strings.Fields(lines[0])[len(strings.Fields(lines[0]))-1])
	if !strings.HasPrefix(lines[0], "cycle --trigger ui --workspace ") || gotWs != wsReal {
		t.Errorf("args = %q", lines[0])
	}
	if lines[1] != wsReal {
		t.Errorf("cwd = %q, want %q", lines[1], wsReal)
	}
}

func TestCyclePostRespondsWithoutWaitingForChild(t *testing.T) {
	ws := initWorkspace(t)
	exe, _ := fakeExecutable(t) // 60 秒眠る
	port := startCycleServer(t, exe, ws)
	start := time.Now()
	if code, body := apiPost(t, port, cyclePath, ""); code != 200 {
		t.Fatalf("POST = %d %s", code, body)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("応答に %v かかった", d)
	}
}

func TestCyclePostWithoutCSRFIsForbiddenAndStartsNothing(t *testing.T) {
	ws := initWorkspace(t)
	port, calls := countingServer(t, ws)
	code, body := post(t, port, cyclePath, "", "")
	if code != 403 || errCode(t, body) != "forbidden_origin" {
		t.Fatalf("POST = %d %s", code, body)
	}
	if n := atomic.LoadInt32(calls); n != 0 {
		t.Errorf("起動が %d 回呼ばれた", n)
	}
}

func TestCyclePostWithLockIsConflictAndStartsNothing(t *testing.T) {
	ws := initWorkspace(t)
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.BeginCycle(context.Background(), core.BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true}); err != nil {
		t.Fatal(err)
	}
	port, calls := countingServer(t, ws)
	code, body := apiPost(t, port, cyclePath, "")
	if code != 409 || errCode(t, body) != "locked" {
		t.Fatalf("POST = %d %s", code, body)
	}
	if n := atomic.LoadInt32(calls); n != 0 {
		t.Errorf("起動が %d 回呼ばれた", n)
	}
}

func TestCycleUnknownWorkspaceAndMethods(t *testing.T) {
	ws := initWorkspace(t)
	exe, _ := fakeExecutable(t)
	port := startCycleServer(t, exe, ws)
	if code, _ := apiPost(t, port, "/api/v1/workspaces/zz/cycle", ""); code != 404 {
		t.Errorf("unknown POST = %d", code)
	}
	if code, _ := getPath(t, port, "/api/v1/workspaces/zz/cycle", "", ""); code != 404 {
		t.Errorf("unknown GET = %d", code)
	}
}

func TestCycleGetStates(t *testing.T) {
	ws := initWorkspace(t)
	exe, _ := fakeExecutable(t)
	port := startCycleServer(t, exe, ws)

	type resp struct {
		State         string  `json:"state"`
		LastStartedAt *string `json:"last_started_at"`
		LastEndedAt   *string `json:"last_ended_at"`
		LastResult    *string `json:"last_result"`
	}
	get := func() (resp, string) {
		code, body := getPath(t, port, cyclePath, "", "")
		if code != 200 {
			t.Fatalf("GET = %d %s", code, body)
		}
		var r resp
		if err := json.Unmarshal([]byte(body), &r); err != nil {
			t.Fatal(err)
		}
		return r, body
	}
	r, body := get()
	if r.State != "idle" || !strings.Contains(body, `"last_ended_at":null`) || !strings.Contains(body, `"last_result":null`) || !strings.Contains(body, `"last_started_at":null`) {
		t.Errorf("idle: %s", body)
	}

	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	cyc, err := st.BeginCycle(context.Background(), core.BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true})
	if err != nil {
		t.Fatal(err)
	}
	r, _ = get()
	if r.State != "running" || r.LastStartedAt == nil || *r.LastStartedAt != cyc.StartedAt.UTC().Format("2006-01-02T15:04:05.000Z") || r.LastEndedAt != nil {
		t.Errorf("running: %+v", r)
	}

	if _, err := st.EndCycle(context.Background(), cyc.ID, core.CycleResultCompleted); err != nil {
		t.Fatal(err)
	}
	r, _ = get()
	if r.State != "idle" || r.LastEndedAt == nil || r.LastResult == nil || *r.LastResult != "completed" {
		t.Errorf("ended: %+v", r)
	}
}
