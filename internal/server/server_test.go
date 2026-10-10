package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

func startServer(t *testing.T) (*Server, int) {
	t.Helper()
	ln, err := Listen(0)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	s := New(ln)
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

func get(t *testing.T, port int, host string, origin string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("GET", "http://127.0.0.1:"+strconv.Itoa(port)+"/anything", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestListenBindsLoopbackOnly(t *testing.T) {
	s, port := startServer(t)
	tcp := s.Addr().(*net.TCPAddr)
	if !tcp.IP.Equal(net.IPv4(127, 0, 0, 1)) || tcp.IP.IsUnspecified() {
		t.Fatalf("Addr = %v, want 127.0.0.1:%d", tcp, port)
	}
	if DefaultPort != 4318 {
		t.Errorf("DefaultPort = %d, want 4318", DefaultPort)
	}
}

func TestHostCheck(t *testing.T) {
	_, port := startServer(t)
	p := strconv.Itoa(port)
	for _, host := range []string{"localhost:" + p, "127.0.0.1:" + p} {
		if code, _ := get(t, port, host, ""); code == http.StatusForbidden {
			t.Errorf("Host %q was rejected", host)
		}
	}
	for _, host := range []string{"evil.example:" + p, "evil.example:4318", "localhost", "localhost:1", "127.0.0.1"} {
		code, body := get(t, port, host, "")
		if code != http.StatusForbidden {
			t.Errorf("Host %q: status %d, want 403", host, code)
		}
		assertForbiddenBody(t, body)
	}
}

func TestMissingHostIsRejected(t *testing.T) {
	s, _ := startServer(t)
	conn, err := net.Dial("tcp", s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	// HTTP/1.0 は Host ヘッダ無しの要求を許す。
	if _, err := conn.Write([]byte("GET / HTTP/1.0\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	assertForbiddenBody(t, string(b))
}

func TestOriginCheck(t *testing.T) {
	_, port := startServer(t)
	p := strconv.Itoa(port)
	host := "127.0.0.1:" + p
	for _, o := range []string{"http://localhost:" + p, "http://127.0.0.1:" + p} {
		if code, _ := get(t, port, host, o); code == http.StatusForbidden {
			t.Errorf("Origin %q was rejected", o)
		}
	}
	for _, o := range []string{"http://evil.example", "http://evil.example:" + p, "null", "https://localhost:" + p, "http://localhost"} {
		code, body := get(t, port, host, o)
		if code != http.StatusForbidden {
			t.Errorf("Origin %q: status %d, want 403", o, code)
		}
		assertForbiddenBody(t, body)
	}
}

func assertForbiddenBody(t *testing.T, body string) {
	t.Helper()
	var doc struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, body)
	}
	if doc.Error.Code != "forbidden_origin" || doc.Error.Message == "" {
		t.Errorf("body = %q, want forbidden_origin with a message", body)
	}
}

// 拒否した要求ではストアのファイル（WAL を含む .flywheel 配下）の内容が変わらない。
// 注: S1 の骨格の server はまだストアを開かない（fleet は後続のチケット）ので、現時点では
// 変化しないことの回帰の置き場であり、fleet が入ったら束ねたワークスペースで検査し直す。
func TestRejectedRequestsDoNotChangeStoreFile(t *testing.T) {
	ws := t.TempDir()
	res, err := core.Init(ws)
	if err != nil {
		t.Fatal(err)
	}
	_ = res
	read := func() []byte {
		var all []byte
		entries, err := os.ReadDir(filepath.Join(ws, ".flywheel"))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			b, err := os.ReadFile(filepath.Join(ws, ".flywheel", e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			all = append(all, []byte(e.Name())...)
			all = append(all, b...)
		}
		return all
	}
	before := read()
	_, port := startServer(t)
	get(t, port, "evil.example:"+strconv.Itoa(port), "")
	get(t, port, "127.0.0.1:"+strconv.Itoa(port), "http://evil.example")
	if !bytes.Equal(before, read()) {
		t.Error("store file changed after rejected requests")
	}
}

func TestShutdownClosesDoneAndStopsAccepting(t *testing.T) {
	ln, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	s := New(ln)
	errc := make(chan error, 1)
	go func() { errc <- s.Serve() }()
	select {
	case <-s.Done():
		t.Fatal("Done closed before Shutdown")
	default:
	}
	addr := s.Addr().String()
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("Done not closed after Shutdown")
	}
	if err := <-errc; err != nil {
		t.Errorf("Serve = %v, want nil", err)
	}
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		_ = c.Close()
		t.Error("server still accepts connections after Shutdown")
	}
}

// 処理中の要求を待ってから Shutdown が返る。
func TestShutdownWaitsForInflightRequest(t *testing.T) {
	ln, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	s := New(ln)
	entered := make(chan struct{})
	release := make(chan struct{})
	s.http.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		_, _ = io.WriteString(w, "done")
	})
	go func() { _ = s.Serve() }()
	port := ln.Addr().(*net.TCPAddr).Port
	body := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/")
		if err != nil {
			body <- "ERR " + err.Error()
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		body <- string(b)
	}()
	<-entered
	shut := make(chan error, 1)
	go func() { shut <- s.Shutdown() }()
	select {
	case <-shut:
		t.Fatal("Shutdown returned while a request was in flight")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if got := <-body; !strings.Contains(got, "done") {
		t.Errorf("in-flight response = %q", got)
	}
	if err := <-shut; err != nil {
		t.Errorf("Shutdown = %v", err)
	}
}
