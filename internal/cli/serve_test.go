package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/server"
)

// startServeChild は `serve --port 0` を子プロセスで起動し、標準エラーの
// 「listening on 127.0.0.1:<ポート>」の 1 行からポートを得る。
func startServeChild(t *testing.T, extra ...string) (cmd *exec.Cmd, port int, line string) {
	t.Helper()
	args := append([]string{"serve", "--port", "0"}, extra...)
	cmd, _, _ = newChildCmd(args)
	cmd.Stderr = nil
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() }) // 終了済みなら無害
	lines := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		if sc.Scan() {
			lines <- sc.Text()
		}
		_, _ = io.Copy(io.Discard, stderr)
	}()
	select {
	case line = <-lines:
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not print the listening line")
	}
	const prefix = "listening on 127.0.0.1:"
	if !strings.HasPrefix(line, prefix) {
		t.Fatalf("stderr line = %q, want %q<port>", line, prefix)
	}
	port, err = strconv.Atoi(strings.TrimPrefix(line, prefix))
	if err != nil || port == 0 {
		t.Fatalf("bad port in %q", line)
	}
	return cmd, port, line
}

func TestServe_ListeningLineAndAddr(t *testing.T) {
	cmd, port, _ := startServeChild(t)
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	// 127.0.0.1 以外の IP（このホストの非ループバック）からは届かない。
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLoopback() || ipn.IP.To4() == nil {
				continue
			}
			if c, err := net.DialTimeout("tcp", net.JoinHostPort(ipn.IP.String(), strconv.Itoa(port)), time.Second); err == nil {
				_ = c.Close()
				t.Errorf("serve is reachable on non-loopback %s", ipn.IP)
			}
			break
		}
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	waitChild(t, cmd, 10*time.Second)
}

func TestServe_SignalsExitZero(t *testing.T) {
	for name, sig := range map[string]syscall.Signal{"SIGTERM": syscall.SIGTERM, "SIGINT": syscall.SIGINT} {
		t.Run(name, func(t *testing.T) {
			cmd, port, _ := startServeChild(t)
			resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/")
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			if code := waitChild(t, cmd, 10*time.Second); code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}
			if c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second); err == nil {
				_ = c.Close()
				t.Error("still accepting after exit")
			}
		})
	}
}

func TestServe_ListenFailed(t *testing.T) {
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = occupied.Close() }()
	port := occupied.Addr().(*net.TCPAddr).Port

	var stdout, stderr bytes.Buffer
	code := run([]string{"serve", "--port", strconv.Itoa(port), "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if code != 2 {
		t.Errorf("exit code = %d, want 2 (stderr=%s)", code, stderr.String())
	}
	if got := readErrorCode(t, stderr.String()); got != "listen_failed" {
		t.Errorf("error code = %q, want listen_failed", got)
	}
	if strings.Contains(stderr.String(), "listening on") || stdout.Len() != 0 {
		t.Errorf("served despite bind failure: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestServe_BadPortIsUsageError(t *testing.T) {
	for _, p := range []string{"abc", "-1", "65536", ""} {
		var stdout, stderr bytes.Buffer
		code := run([]string{"serve", "--port", p, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
		if code != 2 || readErrorCode(t, stderr.String()) != "usage_error" {
			t.Errorf("--port %q: exit=%d stderr=%s, want usage_error", p, code, stderr.String())
		}
	}
}

// serve を起動したまま、同じワークスペースで create と status が成功する。
// 注: S1 の骨格の serve はまだストアを開かない（fleet は後続のチケット）ので、このテストは
// fleet がストアを開くようになった後も成り立つことの回帰の置き場である。
func TestServe_CoexistsWithCreateAndStatus(t *testing.T) {
	ws := initializedWorkspace(t)
	cmd, _, _ := startServeChild(t, "--workspace", ws)
	for _, args := range [][]string{
		{"create", "--title", "t", "--workspace", ws, "--json"},
		{"status", "--workspace", ws, "--json"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, strings.NewReader(""), &stdout, &stderr, defaultCommands()); code != 0 {
			t.Errorf("%v: exit=%d stderr=%s", args, code, stderr.String())
		}
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	if code := waitChild(t, cmd, 10*time.Second); code != 0 {
		t.Errorf("exit code = %d", code)
	}
}

func TestServe_ForbiddenOriginViaChild(t *testing.T) {
	cmd, port, _ := startServeChild(t)
	req, _ := http.NewRequest("GET", "http://127.0.0.1:"+strconv.Itoa(port)+"/", nil)
	req.Header.Set("Origin", "http://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var doc struct{ Error struct{ Code string } }
	_ = json.NewDecoder(resp.Body).Decode(&doc)
	if resp.StatusCode != 403 || doc.Error.Code != "forbidden_origin" {
		t.Errorf("status=%d code=%q", resp.StatusCode, doc.Error.Code)
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	waitChild(t, cmd, 10*time.Second)
}

// server が HTTP の応答に使う forbidden_origin は、CLI のエラーコードの表の値と一致し、
// 終了コード 1 に写る。listen_failed は終了コード 2。
func TestServeErrorCodes(t *testing.T) {
	if string(CodeForbiddenOrigin) != server.CodeForbiddenOrigin {
		t.Errorf("CodeForbiddenOrigin = %q, server = %q", CodeForbiddenOrigin, server.CodeForbiddenOrigin)
	}
	if ExitCodeFor(CodeForbiddenOrigin) != 1 || ExitCodeFor(CodeListenFailed) != 2 {
		t.Error("exit code mapping wrong")
	}
}
