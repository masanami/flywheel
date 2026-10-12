package server

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

const (
	testWatch     = 50 * time.Millisecond
	testKeepAlive = 80 * time.Millisecond
)

func startEventsServer(t *testing.T, ws ...core.FleetWorkspace) (*Server, int) {
	t.Helper()
	ln, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	s := newServer(ln, ws, eventsConfig{watchInterval: testWatch, keepAliveInterval: testKeepAlive})
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

// sseLines は購読の本文を 1 行ずつ lines へ流す。
type sseStream struct {
	resp  *http.Response
	lines chan string
}

func subscribe(t *testing.T, port int) *sseStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:"+strconv.Itoa(port)+"/api/v1/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	st := &sseStream{resp: resp, lines: make(chan string, 1024)}
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			st.lines <- sc.Text()
		}
		close(st.lines)
	}()
	return st
}

// next は within 以内に pred を満たす行を待つ。
func (s *sseStream) wait(within time.Duration, pred func(string) bool) (string, bool) {
	timer := time.NewTimer(within)
	defer timer.Stop()
	for {
		select {
		case l, ok := <-s.lines:
			if !ok {
				return "", false
			}
			if pred(l) {
				return l, true
			}
		case <-timer.C:
			return "", false
		}
	}
}

func isChange(name string) func(string) bool {
	want := `data: {"type":"workspace_changed","workspace":"` + name + `"}`
	return func(l string) bool { return l == want }
}

// isAnyChange は「何も届かない」ことの確認専用（届いた通知の中身の照合には isChange を使う）。
func isAnyChange(l string) bool { return strings.HasPrefix(l, "data: ") }

func TestEvents_ContentTypeIsEventStream(t *testing.T) { // AC-82
	_, port := startEventsServer(t)
	st := subscribe(t, port)
	if ct := st.resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
}

func TestEvents_StoreWriteNotifiesWithinTwoIntervals(t *testing.T) { // AC-83（同一プロセスの別接続）
	ws := initWorkspace(t)
	_, port := startEventsServer(t, core.FleetWorkspace{Name: "a", Path: ws}, core.FleetWorkspace{Name: "b", Path: initWorkspace(t)})
	st := subscribe(t, port)
	time.Sleep(3 * testWatch) // 基準が取れるのを待つ
	coretest.InsertChallenge(t, ws, "x")
	if _, ok := st.wait(2*testWatch+100*time.Millisecond, isChange("a")); !ok {
		t.Fatal("workspace_changed for a not received")
	}
}

// AC-45: 検証中で PR を持つ課題があっても、購読とストアの変化の観測だけでは gh を起動しない
// （/status は呼ばない。起動を記録する偽の gh で数える）。
func TestEvents_ObservationNeverLaunchesGH(t *testing.T) {
	ws := verifyingWorkspace(t)
	dir, calls := writeFakeGH(t, ghRoutesPending())
	withGHOnPATH(t, dir)
	_, port := startEventsServer(t, core.FleetWorkspace{Name: "a", Path: ws})
	st := subscribe(t, port)
	time.Sleep(3 * testWatch) // 基準が取れるのを待つ
	coretest.InsertChallenge(t, ws, "x")
	if _, ok := st.wait(2*testWatch+100*time.Millisecond, isChange("a")); !ok {
		t.Fatal("workspace_changed for a not received")
	}
	time.Sleep(2 * testWatch) // 通知の後の観測も gh を起動しないこと
	if n := calls(); n != 0 {
		t.Errorf("gh launched %d times by subscription and observation, want 0", n)
	}
}

func TestEvents_HeartbeatOnlyNotifiesAndIdleDoesNot(t *testing.T) { // AC-84・AC-85
	ws := initWorkspace(t)
	cyc := coretest.InsertCycle(t, ws, "manual", 0, "2026-09-28T00:00:00.000Z")
	coretest.InsertLock(t, ws, cyc, int64(os.Getpid()), "h", "2026-09-28T00:00:00.000Z", "2026-09-28T00:00:00.000Z")
	_, port := startEventsServer(t, core.FleetWorkspace{Name: "a", Path: ws})
	st := subscribe(t, port)
	if l, ok := st.wait(10*testWatch, isAnyChange); ok {
		t.Fatalf("idle store produced %q", l)
	}
	s, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.HeartbeatCycleLock(context.Background(), "Y-"+strconv.FormatInt(cyc, 10)); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.wait(2*testWatch+100*time.Millisecond, isChange("a")); !ok {
		t.Fatal("heartbeat-only update not notified")
	}
	if l, ok := st.wait(10*testWatch, isAnyChange); ok {
		t.Fatalf("idle store produced %q", l)
	}
}

func TestEvents_KeepAliveComments(t *testing.T) { // AC-86
	_, port := startEventsServer(t)
	st := subscribe(t, port)
	isComment := func(l string) bool { return strings.HasPrefix(l, ": keep-alive") }
	for i := 0; i < 3; i++ {
		if _, ok := st.wait(testKeepAlive*3, isComment); !ok {
			t.Fatalf("keep-alive %d not received", i)
		}
	}
}

func TestEvents_NonOKWorkspaceDoesNotStopObservation(t *testing.T) {
	good := initWorkspace(t)
	missing := t.TempDir()
	_, port := startEventsServer(t, core.FleetWorkspace{Name: "gone", Path: missing}, core.FleetWorkspace{Name: "good", Path: good})
	st := subscribe(t, port)
	time.Sleep(3 * testWatch)
	coretest.InsertChallenge(t, good, "x")
	if _, ok := st.wait(2*testWatch+100*time.Millisecond, isChange("good")); !ok {
		t.Fatal("good not notified")
	}
	// ストアが後から現れた（ok でなかったものが ok になる）ことも通知される。
	if _, err := core.Init(missing); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.wait(2*testWatch+100*time.Millisecond, isChange("gone")); !ok {
		t.Fatal("gone not notified after store appeared")
	}
}

func TestEvents_ShutdownEndsSubscription(t *testing.T) {
	ln, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	s := newServer(ln, nil, eventsConfig{watchInterval: testWatch, keepAliveInterval: testKeepAlive})
	go func() { _ = s.Serve() }()
	st := subscribe(t, ln.Addr().(*net.TCPAddr).Port)
	start := time.Now()
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Shutdown took %v", d)
	}
	if _, ok := st.wait(2*time.Second, func(string) bool { return false }); ok {
		t.Fatal("unexpected")
	}
}

func buildFlywheel(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "flywheel")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/masanami/flywheel/cmd/flywheel")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// fakeGHPath は呼ばれると marker を作る偽の gh を先頭に置いた PATH を返す。
func fakeGHPath(t *testing.T) (path, marker string) {
	t.Helper()
	dir := t.TempDir()
	marker = filepath.Join(dir, "gh-called")
	script := "#!/bin/sh\ntouch " + marker + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir + string(os.PathListSeparator) + os.Getenv("PATH"), marker
}

// 別プロセスの flywheel create での通知（AC-83）、gh を起動しないこと（AC-45）、
// 購読中の SIGTERM で 5 秒以内に終了コード 0（AC-14）。
func TestEvents_RealProcesses_CreateNotifiesNoGHAndSIGTERM(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := buildFlywheel(t)
	ws := initWorkspace(t)
	path, marker := fakeGHPath(t)
	env := append(os.Environ(), "PATH="+path)

	srv := exec.Command(bin, "serve", "--workspace", ws, "--port", "0")
	srv.Env = env
	stderr, err := srv.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	defer func() {
		if srv.ProcessState == nil {
			_ = srv.Process.Kill()
		}
	}()
	sc := bufio.NewScanner(stderr)
	var addr string
	for sc.Scan() {
		if a, ok := strings.CutPrefix(sc.Text(), "listening on "); ok {
			addr = a
			break
		}
	}
	if addr == "" {
		t.Fatal("serve did not report its address")
	}
	go func() {
		for sc.Scan() {
		}
		exited <- srv.Wait()
	}()
	_, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)

	st := subscribe(t, port)
	time.Sleep(2500 * time.Millisecond) // 既定の 1 秒の観測で基準が取れるのを待つ
	create := exec.Command(bin, "create", "--workspace", ws, "--title", "t", "--done-criteria", "d")
	create.Env = env
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	// 通知は {"type":"workspace_changed","workspace":"<名前>"} そのものである。
	entries, _ := listWorkspaces(t, port)
	if len(entries) != 1 {
		t.Fatalf("workspaces = %+v", entries)
	}
	if _, ok := st.wait(2*time.Second+500*time.Millisecond, isChange(entries[0].Name)); !ok {
		t.Fatal("no workspace_changed within 2 observation intervals")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("gh was launched")
	}

	start := time.Now()
	if err := srv.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("serve exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not exit within 5s of SIGTERM")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
}
