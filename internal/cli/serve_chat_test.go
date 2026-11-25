package cli

// このファイルは、対話の欄の層をまたぐ振る舞い（口 → サーバ → 実行エンジンの起動 → ストア）の試験。サーバと
// invoker は `serve` と同じ組み立てで実物を使い、偽にするのは claude（テスト用の実行エンジン。シェルのスクリプト）
// だけ。

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/invoker"
	"github.com/masanami/flywheel/internal/server"
	"github.com/masanami/flywheel/internal/view"
)

// chatFakeEngine は、起動ごとに引数・作業フォルダ・環境を dir へ残し、標準入力の発言の語で場面を選ぶ。
const chatFakeEngine = `#!/bin/sh
n=1
while [ -e "$LOG/argv.$n" ]; do n=$((n+1)); done
printf '%s\n' "$@" > "$LOG/argv.$n"
pwd -P > "$LOG/cwd.$n"
env > "$LOG/env.$n"
readlink "${PATH%%:*}/flywheel" > "$LOG/link.$n"
init='{"type":"system","subtype":"init","session_id":"s"}'
speech='{"type":"assistant","message":{"content":[{"type":"text","text":"partial"}]}}'
resume=no
for a in "$@"; do [ "$a" = "--resume" ] && resume=yes; done
while IFS= read -r line; do
  case "$line" in
    *hang-with-child*)
      sleep 300 &
      echo $! > "$LOG/child"
      echo $$ > "$LOG/self"
      printf '%s\n%s\n' "$init" "$speech" ;;
    *hang-before-start*) echo $$ > "$LOG/self" ;;
    *crash-after-start*) printf '%s\n%s\n' "$init" "$speech"; exit 1 ;;
    *crash-before-start*) exit 1 ;;
    *lose-session*)
      if [ $resume = yes ]; then
        echo 'No conversation found with session ID: s' >&2
        exit 1
      fi
      printf '%s\n%s\n' "$init" '{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0.1}'
      exit 0 ;;
    *) printf '%s\n%s\n' "$init" '{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0.1}' ;;
  esac
done
`

type chatServeEnv struct {
	t         *testing.T
	ws, log   string
	port      int
	srv       *server.Server
	csrfToken string
}

func startChatServe(t *testing.T) *chatServeEnv {
	t.Helper()
	e := &chatServeEnv{t: t, ws: t.TempDir(), log: t.TempDir()}
	if _, err := core.Init(e.ws); err != nil {
		t.Fatal(err)
	}
	engine := filepath.Join(t.TempDir(), "claude")
	script := strings.Replace(chatFakeEngine, "#!/bin/sh\n", "#!/bin/sh\nLOG='"+e.log+"'\n", 1)
	if err := os.WriteFile(engine, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := server.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	e.srv = server.New(ln, []core.FleetWorkspace{{Name: "w", Path: e.ws}})
	e.srv.SetChatInvoker(invoker.NewLauncherWithLookPath(func(string) (string, error) { return engine, nil }))
	done := make(chan error, 1)
	go func() { done <- e.srv.Serve() }()
	t.Cleanup(func() {
		_ = e.srv.Shutdown()
		<-done
	})
	e.port, err = strconv.Atoi(ln.Addr().String()[strings.LastIndex(ln.Addr().String(), ":")+1:])
	if err != nil {
		t.Fatal(err)
	}
	_, page := e.request("GET", "/", "")
	m := regexp.MustCompile(`<meta name="flywheel-csrf" content="([^"]+)" />`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no CSRF value: %q", page)
	}
	e.csrfToken = m[1]
	return e
}

func (e *chatServeEnv) request(method, path, body string) (int, string) {
	e.t.Helper()
	req, err := http.NewRequest(method, "http://127.0.0.1:"+strconv.Itoa(e.port)+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	if method == "POST" {
		req.AddCookie(&http.Cookie{Name: "flywheel_csrf_" + strconv.Itoa(e.port), Value: e.csrfToken})
		req.Header.Set(server.CSRFHeader, e.csrfToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (e *chatServeEnv) post(path, body string, want int) {
	e.t.Helper()
	if code, got := e.request("POST", "/api/v1/workspaces/w/chat"+path, body); code != want {
		e.t.Fatalf("POST chat%s = %d %s, want %d", path, code, got, want)
	}
}

func (e *chatServeEnv) send(text string) {
	e.t.Helper()
	e.post("/messages", `{"text":"`+text+`"}`, 200)
}

// waitLast は最後の返事が cond を満たすまで待つ。
func (e *chatServeEnv) waitLast(what string, cond func(view.ChatResponse) bool) view.ChatResponse {
	e.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, body := e.request("GET", "/api/v1/workspaces/w/chat", "")
		var c view.ChatResponse
		if err := json.Unmarshal([]byte(body), &c); err != nil {
			e.t.Fatal(err)
		}
		if cond(c) {
			return c
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s: %s", what, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (e *chatServeEnv) waitState(state string, messages int) view.ChatResponse {
	e.t.Helper()
	return e.waitLast(state, func(c view.ChatResponse) bool {
		return len(c.Messages) == messages && c.Messages[messages-1].State == state
	})
}

func (e *chatServeEnv) read(name string) string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.log, name))
	if err != nil {
		e.t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// sessionArg は n 番目の起動の引数のうち、会話の識別を渡した引数の名前（--resume か --session-id）。
func (e *chatServeEnv) sessionArg(n int) string {
	e.t.Helper()
	for _, a := range strings.Split(e.read("argv."+strconv.Itoa(n)), "\n") {
		if a == "--resume" || a == "--session-id" {
			return a
		}
	}
	return ""
}

func (e *chatServeEnv) pid(name string) int {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		b, _ := os.ReadFile(filepath.Join(e.log, name))
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			return n
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("no pid in %s", name)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitGone は pid のプロセスが終わる（ゾンビを含めて動いていない）まで待つ。
func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		if s := strings.TrimSpace(string(out)); err != nil || s == "" || strings.HasPrefix(s, "Z") {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("process %d is still running", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// 作業フォルダ・環境・PATH、同じプロセスへの 2 通目、「止める」がプロセスとその子を止めること、その後の --resume。
func TestServeChat_ProcessEnvStopAndResume(t *testing.T) {
	t.Setenv(core.DelegatedRunEnvVar, "7")
	t.Setenv(core.ChatEnvMessage, "1")
	t.Setenv(core.ChatEnvTools, "restricted")
	e := startChatServe(t)
	e.send("hello")
	c := e.waitState("done", 2)
	e.send("hang-with-child")
	e.waitLast("speech", func(c view.ChatResponse) bool { return len(c.Messages) == 4 && c.Messages[3].Body == "partial" })
	if _, err := os.Stat(filepath.Join(e.log, "argv.2")); !os.IsNotExist(err) {
		t.Fatal("the second message started another process")
	}
	wantCwd, _ := filepath.EvalSymlinks(e.ws)
	if got := e.read("cwd.1"); got != wantCwd {
		t.Errorf("cwd = %q, want %q", got, wantCwd)
	}
	var head string
	env := strings.Split(e.read("env.1"), "\n")
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			head = strings.SplitN(v, ":", 2)[0]
		}
		for _, banned := range []string{core.ChatEnvMessage, core.ChatEnvTools, core.DelegatedRunEnvVar} {
			if strings.HasPrefix(kv, banned+"=") {
				t.Errorf("the engine environment has %s", kv)
			}
		}
	}
	if !strings.Contains("\n"+strings.Join(env, "\n")+"\n", "\n"+core.ChatEnvSession+"="+*c.SessionID+"\n") {
		t.Error("the session mark is missing")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if got := e.read("link.1"); got != exe {
		t.Errorf("flywheel on PATH -> %q, want the server executable %q", got, exe)
	}
	self, child := e.pid("self"), e.pid("child")

	e.post("/stop", "", 204)
	if got := e.waitState("stopped", 4).Messages[3]; got.Body != "partial" {
		t.Errorf("reply = %+v", got)
	}
	waitGone(t, self)
	waitGone(t, child)
	if _, err := os.Stat(head); !os.IsNotExist(err) {
		t.Errorf("the PATH directory %q remains: %v", head, err)
	}
	e.send("again")
	e.waitState("done", 6)
	if e.sessionArg(1) != "--session-id" || e.sessionArg(2) != "--resume" {
		t.Errorf("session args = %q, %q", e.sessionArg(1), e.sessionArg(2))
	}
}

// 会話の始まりの行が出る前に止めた・失敗した会話は --session-id、出た後に失敗した会話は --resume で起動する。
// 新しい会話とサーバの停止は、プロセスを終わらせる。
func TestServeChat_RestartArgsAfterStopAndFailure(t *testing.T) {
	e := startChatServe(t)
	e.send("hang-before-start")
	first := e.pid("self")
	e.post("/stop", "", 204)
	waitGone(t, first)
	e.waitState("stopped", 2)

	e.send("crash-before-start")
	e.waitState("failed", 4)
	e.send("crash-after-start")
	if got := e.waitState("failed", 6).Messages[5]; got.Body != "partial" {
		t.Errorf("reply = %+v", got)
	}
	e.send("hang-with-child")
	e.waitLast("speech", func(c view.ChatResponse) bool { return len(c.Messages) == 8 && c.Messages[7].Body == "partial" })
	for n, want := range []string{"--session-id", "--session-id", "--session-id", "--resume"} {
		if got := e.sessionArg(n + 1); got != want {
			t.Errorf("start %d = %q, want %q", n+1, got, want)
		}
	}
	self, child := e.pid("self"), e.pid("child")
	if err := e.srv.Shutdown(); err != nil {
		t.Fatal(err)
	}
	waitGone(t, self)
	waitGone(t, child)
	st, err := core.OpenWorkspace(e.ws)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	chat, err := st.ReadChat(t.Context())
	if err != nil || chat.Messages[7].State != core.ChatStateStopped {
		t.Errorf("chat = %+v (%v), want the reply stopped by the shutdown", chat, err)
	}
}

// 続きの会話が見つからない出力を読むと、返事は session_lost になり、会話が閉じる。
func TestServeChat_SessionLostClosesTheSession(t *testing.T) {
	e := startChatServe(t)
	e.send("lose-session") // 始まりの行を出して返事を終え、プロセスが終わる
	e.waitState("done", 2)
	// 前のプロセスの終わりが処理されるのを待たずに送ると、その返事は failed になりうるので、起動が増えるまで見る。
	time.Sleep(500 * time.Millisecond)
	e.send("lose-session")
	e.waitLast("closed session", func(c view.ChatResponse) bool { return c.SessionID == nil })
	st, err := core.OpenWorkspace(e.ws)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	sessions, err := st.ListChatSessions(t.Context(), nil)
	if err != nil || len(sessions) != 1 || sessions[0].Open {
		t.Errorf("sessions = %+v (%v)", sessions, err)
	}
	if e.sessionArg(2) != "--resume" {
		t.Errorf("2nd start = %q, want --resume", e.sessionArg(2))
	}
}
