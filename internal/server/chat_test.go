package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
	"github.com/masanami/flywheel/internal/view"
)

// このファイルは対話の欄の口と、会話の実行エンジンのプロセスの寿命（docs/features/chat-session.md）の試験。
// 偽にするのは対話の実行のインターフェース（core.ChatConversationInvoker。テスト用の実行エンジン）と、休ませるための
// 時計だけで、ストアは実物（t.TempDir() の SQLite）を使う。

const chatBase = "/api/v1/workspaces/w/chat"

// fakeChat はテスト用の実行エンジン。起動のたびに fakeConv を作り、試験が途中の分を 1 つずつ流す。
type fakeChat struct {
	mu       sync.Mutex
	availErr error
	startErr error
	sendErr  error
	convs    []*fakeConv
	// onStart は起動のたびに呼ぶ。
	onStart func(core.ChatStartInput)
}

func newFakeChat() *fakeChat { return &fakeChat{} }

func (f *fakeChat) Available(context.Context) error { return f.availErr }

func (f *fakeChat) ReadTranscript(context.Context, core.ChatTranscriptRef) ([]core.ChatTranscriptEntry, error) {
	return nil, core.ErrChatTranscriptUnavailable
}

func (f *fakeChat) HasTranscript(context.Context, core.ChatTranscriptRef) bool { return false }

func (f *fakeChat) StartConversation(_ context.Context, in core.ChatStartInput, ev core.ChatConversationEvents) (core.ChatConversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.onStart != nil {
		f.onStart(in)
	}
	if f.startErr != nil {
		return nil, f.startErr
	}
	// 2 つ目のプロセスを起動していないことを、起動のたびに記録する。
	overlap := slices.ContainsFunc(f.convs, func(c *fakeConv) bool { return c.in.WorkDir == in.WorkDir && !c.isExited() })
	c := &fakeConv{in: in, ev: ev, overlap: overlap, sendErr: f.sendErr, sent: make(chan core.ChatUtterance, 16), answers: make(chan fakeAnswer, 16)}
	f.convs = append(f.convs, c)
	return c, nil
}

func (f *fakeChat) starts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.convs)
}

// conv は i 番目に起動した会話を返す。起動は要求の中で済むので、待たない。
func (f *fakeChat) conv(t *testing.T, i int) *fakeConv {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.convs) {
		t.Fatalf("the engine was started %d times, want at least %d", len(f.convs), i+1)
	}
	return f.convs[i]
}

type fakeAnswer struct {
	requestID string
	answer    core.ChatPromptAnswer
}

// fakeConv は起動した会話（プロセス）1 つ。途中の分は events の排他の中で呼び、実物と同じく重ねない。
type fakeConv struct {
	in      core.ChatStartInput
	ev      core.ChatConversationEvents
	overlap bool
	sendErr error
	sent    chan core.ChatUtterance
	answers chan fakeAnswer

	events sync.Mutex
	mu     sync.Mutex
	exited bool
	stops  int
}

func (c *fakeConv) Send(_ context.Context, u core.ChatUtterance) error {
	if c.sendErr != nil {
		return c.sendErr
	}
	c.sent <- u
	return nil
}

func (c *fakeConv) Answer(_ context.Context, requestID string, a core.ChatPromptAnswer) error {
	c.answers <- fakeAnswer{requestID, a}
	return nil
}

func (c *fakeConv) Stop(context.Context) error {
	c.mu.Lock()
	c.stops++
	c.mu.Unlock()
	c.exit("signal: killed")
	return nil
}

// exit はプロセスの終わりを 1 回だけ渡す。
func (c *fakeConv) exit(summary string) {
	c.events.Lock()
	defer c.events.Unlock()
	if c.isExited() {
		return
	}
	c.ev.Exited(core.ChatExit{ErrorSummary: summary})
	c.mu.Lock()
	c.exited = true
	c.mu.Unlock()
}

func (c *fakeConv) isExited() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exited
}

func (c *fakeConv) emit(fn func(core.ChatConversationEvents)) {
	c.events.Lock()
	defer c.events.Unlock()
	if !c.isExited() {
		fn(c.ev)
	}
}

func (c *fakeConv) started() {
	c.emit(func(ev core.ChatConversationEvents) { ev.Started() })
}

func (c *fakeConv) speech(text string) {
	c.emit(func(ev core.ChatConversationEvents) { ev.Speech(text) })
}

func (c *fakeConv) prompt(req core.ChatPromptRequest) {
	c.emit(func(ev core.ChatConversationEvents) { ev.Prompt(req) })
}

func (c *fakeConv) reply(state core.ChatMessageState, total *float64) {
	c.emit(func(ev core.ChatConversationEvents) {
		ev.ReplyEnded(core.ChatReplyEnd{State: state, CostTotalUSD: total})
	})
}

func (c *fakeConv) waitSend(t *testing.T) core.ChatUtterance {
	t.Helper()
	select {
	case u := <-c.sent:
		return u
	case <-time.After(10 * time.Second):
		t.Fatal("no utterance was sent to the engine")
		return core.ChatUtterance{}
	}
}

// fakeClock は休ませるための時計。advance で、期限の来た関数を呼ぶ。
type fakeClock struct {
	mu     sync.Mutex
	now    time.Duration
	timers []*fakeTimer
}

type fakeTimer struct {
	at   time.Duration
	f    func()
	done bool
}

func (k *fakeClock) afterFunc(d time.Duration, f func()) func() {
	k.mu.Lock()
	defer k.mu.Unlock()
	tm := &fakeTimer{at: k.now + d, f: f}
	k.timers = append(k.timers, tm)
	return func() {
		k.mu.Lock()
		defer k.mu.Unlock()
		tm.done = true
	}
}

func (k *fakeClock) advance(d time.Duration) {
	k.mu.Lock()
	k.now += d
	var due []*fakeTimer
	for _, tm := range k.timers {
		if !tm.done && tm.at <= k.now {
			tm.done = true
			due = append(due, tm)
		}
	}
	k.mu.Unlock()
	for _, tm := range due {
		tm.f()
	}
}

// testIdle は試験で使う放置の時間（短くした値）。
const testIdle = time.Minute

func startChatServer(t *testing.T, inv core.ChatConversationInvoker, cfg eventsConfig, ws ...core.FleetWorkspace) (*Server, int) {
	t.Helper()
	ln, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	s := newServer(ln, ws, cfg)
	if inv != nil {
		s.SetChatInvoker(inv)
	}
	errc := make(chan error, 1)
	go func() { errc <- s.Serve() }()
	t.Cleanup(func() {
		_ = s.Shutdown()
		<-errc
	})
	return s, ln.Addr().(*net.TCPAddr).Port
}

type chatEnvT struct {
	ws    string
	s     *Server
	port  int
	inv   *fakeChat
	clock *fakeClock
}

func newChatEnv(t *testing.T) *chatEnvT {
	t.Helper()
	e := &chatEnvT{ws: initWorkspace(t), inv: newFakeChat(), clock: &fakeClock{}}
	e.s, e.port = startChatServer(t, e.inv, eventsConfig{}, core.FleetWorkspace{Name: "w", Path: e.ws})
	e.s.chat.idle, e.s.chat.afterFunc = testIdle, e.clock.afterFunc
	return e
}

func chatWorkspace(t *testing.T) (ws string, port int, inv *fakeChat) {
	t.Helper()
	e := newChatEnv(t)
	return e.ws, e.port, e.inv
}

// send は発言を送り、i 番目に起動した会話に届くまで待つ。
func (e *chatEnvT) send(t *testing.T, i int, text string) *fakeConv {
	t.Helper()
	if code, body := sendChat(t, e.port, text); code != 200 {
		t.Fatalf("POST = %d %s", code, body)
	}
	c := e.inv.conv(t, i)
	if u := c.waitSend(t); u.Text != text {
		t.Fatalf("sent %q, want %q", u.Text, text)
	}
	return c
}

func (e *chatEnvT) post(t *testing.T, path string, want int) {
	t.Helper()
	if code, body := apiPost(t, e.port, chatBase+path, `{}`); code != want {
		t.Fatalf("POST chat%s = %d %s, want %d", path, code, body, want)
	}
}

func sendChat(t *testing.T, port int, text string) (int, string) {
	t.Helper()
	b, _ := json.Marshal(view.ChatPostRequest{Text: text})
	return apiPost(t, port, chatBase+"/messages", string(b))
}

func getChat(t *testing.T, port int) view.ChatResponse {
	t.Helper()
	code, body := apiGet(t, port, chatBase)
	if code != 200 {
		t.Fatalf("GET chat = %d %s", code, body)
	}
	var out view.ChatResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// waitReply は返事が進行中でなくなるまで待ち、その時点の会話を返す。
func waitReply(t *testing.T, port int) view.ChatResponse {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c := getChat(t, port)
		if c.State == view.ChatStateIdle {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("reply did not finish: %+v", c)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func lastReply(c view.ChatResponse) view.ChatMessage { return c.Messages[len(c.Messages)-1] }

func chatCount(t *testing.T, ws, query string) int {
	t.Helper()
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	c, err := st.ReadChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	switch query {
	case "messages":
		return len(c.Messages)
	case "sessions":
		if c.Session == nil {
			return 0
		}
		return 1
	}
	t.Fatalf("unknown query %q", query)
	return 0
}

func usdTotal(v float64) *float64 { return &v }

func permissionPrompt(id string) core.ChatPromptRequest {
	return core.ChatPromptRequest{RequestID: id, Kind: core.ChatPromptPermission, Tool: "Bash", Target: "rm -rf build"}
}

func pathHead(env []string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "PATH="); ok {
			return strings.SplitN(v, string(os.PathListSeparator), 2)[0]
		}
	}
	return ""
}

// 2 通目は同じプロセスへ送る。途中の分は届くたびにストアへ書き、費用は累計の差で数える。
func TestChat_SecondMessageGoesToTheSameProcess(t *testing.T) {
	e := newChatEnv(t)
	empty := getChat(t, e.port)
	if empty.SessionID != nil || len(empty.Messages) != 0 || empty.State != view.ChatStateIdle {
		t.Fatalf("empty chat = %+v", empty)
	}
	conv := e.send(t, 0, "状況は？")
	conv.started()
	conv.speech("確かめます。")
	conv.emit(func(ev core.ChatConversationEvents) {
		ev.ToolStarted(core.ChatToolUse{ID: "t1", Tool: "Bash", Target: "flywheel status"})
	})
	if r := lastReply(getChat(t, e.port)); r.State != "replying" || r.Body != "確かめます。" || len(r.Activity) != 1 || r.Activity[0].State != "running" {
		t.Fatalf("reply in progress = %+v", r)
	}
	conv.emit(func(ev core.ChatConversationEvents) { ev.ToolFinished("t1", true) })
	conv.reply(core.ChatStateDone, usdTotal(0.25))
	c := waitReply(t, e.port)
	if r := lastReply(c); r.State != "done" || r.CostUSD != 0.25 || r.Activity[0].State != "ok" {
		t.Fatalf("reply = %+v", r)
	}
	if got := e.send(t, 0, "続き"); got != conv || e.inv.starts() != 1 {
		t.Fatalf("the second message started another process (%d starts)", e.inv.starts())
	}
	conv.reply(core.ChatStateDone, usdTotal(0.40))
	if r := lastReply(waitReply(t, e.port)); r.CostUSD != 0.15 {
		t.Errorf("2nd cost = %v, want 0.15", r.CostUSD)
	}
	if conv.in.Resume || conv.in.SessionID != *c.SessionID || conv.in.WorkDir != e.ws {
		t.Errorf("start input = %+v", conv.in)
	}
}

// 作業フォルダはワークスペースで、環境の印は会話の識別だけ。PATH の先頭の flywheel はサーバの実行ファイルを指し、
// そのディレクトリはプロセスが終わるまで残って、終わると消える。
func TestChat_ProcessEnvAndPathDirectoryLastAsLongAsTheProcess(t *testing.T) {
	e := newChatEnv(t)
	exe := filepath.Join(t.TempDir(), "server-bin")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.s.chat.executable = func() (string, error) { return exe, nil }
	conv := e.send(t, 0, "x")
	if len(conv.in.Env) != 2 || conv.in.Env[0] != core.ChatEnvSession+"="+conv.in.SessionID {
		t.Errorf("env = %v, want the session mark and PATH only", conv.in.Env)
	}
	dir := pathHead(conv.in.Env)
	if target, err := os.Readlink(filepath.Join(dir, "flywheel")); err != nil || target != exe {
		t.Errorf("flywheel -> %q (%v), want %q", target, err, exe)
	}
	conv.reply(core.ChatStateDone, nil)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the directory was removed while the process is running: %v", err)
	}
	conv.exit("")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("%s was not removed after the process exited (%v)", dir, err)
	}
}

// 返事が進行中の送信と「新しい会話」は 409 で、行を増やさず、会話もプロセスも変えない。
func TestChat_ReplyingRefusesSendAndNew(t *testing.T) {
	e := newChatEnv(t)
	conv := e.send(t, 0, "x")
	before := getChat(t, e.port)
	if code, body := sendChat(t, e.port, "もう 1 つ"); code != 409 {
		t.Fatalf("send while replying = %d %s", code, body)
	}
	e.post(t, "/new", 409)
	after := getChat(t, e.port)
	if len(after.Messages) != 2 || after.SessionID == nil || *after.SessionID != *before.SessionID || after.State != "replying" {
		t.Errorf("chat = %+v", after)
	}
	if conv.isExited() || e.inv.starts() != 1 {
		t.Errorf("the process was stopped (%v) or started again (%d)", conv.isExited(), e.inv.starts())
	}
}

// 放置の時間が過ぎるとプロセスを止める（会話は開いたまま）。時計は返事の終わりから数える。次の発言は続きで起動する。
func TestChat_IdleProcessIsStoppedAndResumed(t *testing.T) {
	e := newChatEnv(t)
	conv := e.send(t, 0, "x")
	conv.started()
	conv.reply(core.ChatStateDone, nil)
	e.clock.advance(testIdle - time.Second)
	if conv.isExited() {
		t.Fatal("stopped before the idle time passed")
	}
	// 次の発言で時計は止まり、返事の終わりから数え直す。
	e.send(t, 0, "y")
	conv.reply(core.ChatStateDone, nil)
	e.clock.advance(testIdle - time.Second)
	if conv.isExited() {
		t.Fatal("the clock was not restarted by the next message")
	}
	before := getChat(t, e.port)
	e.clock.advance(time.Second)
	if !conv.isExited() {
		t.Fatal("the idle process was not stopped")
	}
	after := getChat(t, e.port)
	if after.SessionID == nil || *after.SessionID != *before.SessionID || len(after.Messages) != 4 || lastReply(after).State != "done" {
		t.Errorf("chat after resting = %+v", after)
	}
	next := e.send(t, 1, "z")
	if !next.in.Resume || next.in.SessionID != conv.in.SessionID || next.overlap {
		t.Errorf("restart input = %+v (overlap %v), want resume", next.in, next.overlap)
	}
}

func TestChat_DefaultIdleTimeIs30Minutes(t *testing.T) {
	c := newChatRunner(NewFleet(nil))
	if c.idle != 30*time.Minute {
		t.Errorf("idle = %v, want 30m", c.idle)
	}
}

// 返事が進行中の間は、答えを待つ確認があっても無くても、放置の時間が過ぎてもプロセスを止めない。600 秒を超えても
// 返事を打ち切らない。
func TestChat_ReplyInProgressIsNeverCutOff(t *testing.T) {
	for name, withPrompt := range map[string]bool{"replying": false, "waiting for an answer": true} {
		t.Run(name, func(t *testing.T) {
			e := newChatEnv(t)
			conv := e.send(t, 0, "x")
			conv.reply(core.ChatStateDone, nil) // 前の返事で始まった時計は、次の発言で取り消される
			e.send(t, 0, "y")
			if withPrompt {
				conv.prompt(permissionPrompt("r1"))
			}
			e.clock.advance(601 * time.Second)
			e.clock.advance(24 * time.Hour)
			c := getChat(t, e.port)
			if conv.isExited() || c.State != "replying" {
				t.Fatalf("exited = %v, chat = %+v", conv.isExited(), c)
			}
			if withPrompt {
				if p := lastReply(c).Prompts; len(p) != 1 || p[0].State != "pending" {
					t.Errorf("prompts = %+v", p)
				}
			}
		})
	}
}

// stop はプロセスを止めて返事を stopped にする（failed にしない）。足された文は残る。次の起動は、始まりの行が
// 出た会話なら続き、出ていない会話なら新しい会話の指定。
func TestChat_StopEndsTheReplyAndTheNextStartDependsOnTheStartedLine(t *testing.T) {
	for name, started := range map[string]bool{"started": true, "not started": false} {
		t.Run(name, func(t *testing.T) {
			e := newChatEnv(t)
			conv := e.send(t, 0, "x")
			if started {
				conv.started()
			}
			conv.speech("途中の文")
			conv.prompt(permissionPrompt("r1"))
			dir := pathHead(conv.in.Env)
			e.post(t, "/stop", 204)
			// 204 の時点で、止め終わって記録も済んでいる。
			c := getChat(t, e.port)
			if r := lastReply(c); c.State != "idle" || r.State != "stopped" || r.Body != "途中の文" || r.CostUSD != 0 || r.Prompts[0].State != "expired" {
				t.Fatalf("chat = %+v", c)
			}
			if !conv.isExited() {
				t.Error("the process is still running")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Errorf("the PATH directory remains: %v", err)
			}
			next := e.send(t, 1, "y")
			if next.in.Resume != started || next.in.SessionID != conv.in.SessionID {
				t.Errorf("restart input = %+v, want resume %v", next.in, started)
			}
		})
	}
}

// 進行中の返事が無いときの stop は 204 で、ストアを変えず、プロセスも止めない。
func TestChat_StopWithoutReplyChangesNothing(t *testing.T) {
	e := newChatEnv(t)
	e.post(t, "/stop", 204)
	conv := e.send(t, 0, "x")
	conv.reply(core.ChatStateDone, nil)
	before := coretest.DumpTables(t, e.ws, "chat_session", "chat_message", "chat_prompt")
	e.post(t, "/stop", 204)
	if after := coretest.DumpTables(t, e.ws, "chat_session", "chat_message", "chat_prompt"); after != before {
		t.Errorf("stop changed the store:\n%s\n->\n%s", before, after)
	}
	if conv.isExited() {
		t.Error("stop without a reply stopped the process")
	}
	if got := e.send(t, 0, "y"); got != conv {
		t.Error("the same process was not reused")
	}
}

// 「新しい会話」は会話を閉じてプロセスを止める。次の発言は新しい会話で起動する。閉じた会話の行は残る。
func TestChat_NewClosesTheSessionAndStopsTheProcess(t *testing.T) {
	e := newChatEnv(t)
	conv := e.send(t, 0, "x")
	conv.started()
	conv.reply(core.ChatStateDone, nil)
	e.post(t, "/new", 204)
	if !conv.isExited() {
		t.Fatal("the process is still running")
	}
	if c := getChat(t, e.port); c.SessionID != nil || len(c.Messages) != 0 {
		t.Errorf("chat = %+v", c)
	}
	next := e.send(t, 1, "y")
	if next.in.Resume || next.in.SessionID == conv.in.SessionID || next.overlap {
		t.Errorf("next input = %+v", next.in)
	}
	if got := coretest.DumpTables(t, e.ws, "chat_message"); !strings.Contains(got, "x") {
		t.Errorf("the closed session's rows are gone: %s", got)
	}
}

// 返事の途中でプロセスが終わったら failed（それまでの文は残す）。次の起動は、始まりの行が出た会話なら続き。
func TestChat_ExitMidReplyIsFailed(t *testing.T) {
	for name, started := range map[string]bool{"started": true, "not started": false} {
		t.Run(name, func(t *testing.T) {
			e := newChatEnv(t)
			conv := e.send(t, 0, "x")
			if started {
				conv.started()
			}
			conv.speech("途中の文")
			conv.exit("exit status 1")
			if r := lastReply(waitReply(t, e.port)); r.State != "failed" || r.Body != "途中の文" {
				t.Fatalf("reply = %+v", r)
			}
			next := e.send(t, 1, "y")
			if next.in.Resume != started || next.in.SessionID != conv.in.SessionID {
				t.Errorf("restart input = %+v, want resume %v", next.in, started)
			}
		})
	}
}

// 返事の合間のプロセスの終わりは、何も記録しない。
func TestChat_ExitBetweenRepliesRecordsNothing(t *testing.T) {
	e := newChatEnv(t)
	conv := e.send(t, 0, "x")
	conv.started()
	conv.reply(core.ChatStateDone, nil)
	before := coretest.DumpTables(t, e.ws, "chat_session", "chat_message", "chat_prompt")
	conv.exit("exit status 1")
	if after := coretest.DumpTables(t, e.ws, "chat_session", "chat_message", "chat_prompt"); after != before {
		t.Errorf("the store changed:\n%s\n->\n%s", before, after)
	}
	if next := e.send(t, 1, "y"); !next.in.Resume {
		t.Errorf("restart input = %+v", next.in)
	}
}

// 返事の合間に届いた確認は断り、答えを待つ確認を作らず、最後の返事の行に denied を 1 件記録する。
func TestChat_PromptBetweenRepliesIsDenied(t *testing.T) {
	e := newChatEnv(t)
	conv := e.send(t, 0, "x")
	conv.reply(core.ChatStateDone, nil)
	conv.prompt(permissionPrompt("late"))
	select {
	case a := <-conv.answers:
		if a.requestID != "late" || a.answer.Decision != core.ChatDecisionDeny || !strings.Contains(a.answer.Message, "次の返事") {
			t.Errorf("answer = %+v", a)
		}
	default:
		t.Fatal("no answer was returned to the engine")
	}
	r := lastReply(getChat(t, e.port))
	if len(r.Prompts) != 1 || r.Prompts[0].State != "denied" || r.State != "done" {
		t.Errorf("reply = %+v", r)
	}
}

// 続きの会話が無い返事は session_lost で、会話が閉じる。そのプロセスは止め、次の発言は新しい会話になる。
func TestChat_SessionLostClosesTheSession(t *testing.T) {
	e := newChatEnv(t)
	conv := e.send(t, 0, "x")
	conv.reply(core.ChatStateSessionLost, nil)
	if c := getChat(t, e.port); c.SessionID != nil {
		t.Fatalf("chat = %+v, want closed", c)
	}
	e.clock.advance(0)
	if !conv.isExited() {
		t.Error("the process of the closed session is still running")
	}
	if next := e.send(t, 1, "y"); next.in.Resume || next.in.SessionID == conv.in.SessionID || next.overlap {
		t.Errorf("next input = %+v (overlap %v)", next.in, next.overlap)
	}
}

// 止める前の時計で、閉じた会話のプロセスが残っていても、次の発言は終わらせてから起動する。
func TestChat_NeverStartsASecondProcess(t *testing.T) {
	e := newChatEnv(t)
	conv := e.send(t, 0, "x")
	conv.reply(core.ChatStateSessionLost, nil) // 時計を進めない
	next := e.send(t, 1, "y")
	if !conv.isExited() || next.overlap {
		t.Errorf("old exited = %v, overlap = %v", conv.isExited(), next.overlap)
	}
}

// 起動できない・送れない返事は failed で、次の発言を送れる。一時のディレクトリは残らない。
func TestChat_StartOrSendFailureEndsTheReplyAsFailed(t *testing.T) {
	t.Run("start", func(t *testing.T) {
		e := newChatEnv(t)
		var dir string
		e.inv.startErr = errors.New("boom")
		e.inv.onStart = func(in core.ChatStartInput) { dir = pathHead(in.Env) }
		if code, body := sendChat(t, e.port, "x"); code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		if r := lastReply(waitReply(t, e.port)); r.State != "failed" {
			t.Errorf("reply = %+v", r)
		}
		if _, err := os.Stat(dir); dir == "" || !os.IsNotExist(err) {
			t.Errorf("PATH directory %q remains: %v", dir, err)
		}
	})
	t.Run("send", func(t *testing.T) {
		e := newChatEnv(t)
		e.inv.sendErr = errors.New("broken pipe")
		if code, body := sendChat(t, e.port, "x"); code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		if r := lastReply(waitReply(t, e.port)); r.State != "failed" {
			t.Errorf("reply = %+v", r)
		}
	})
}

// 実行エンジンが見つからないときは 503 で、行を作らない。
func TestChat_EngineUnavailableIs503WithoutRows(t *testing.T) {
	ws, port, inv := chatWorkspace(t)
	inv.availErr = core.ErrChatEngineNotFound
	code, body := sendChat(t, port, "x")
	if code != 503 || !strings.Contains(body, CodeEngineUnavailable) {
		t.Errorf("%d %s", code, body)
	}
	if chatCount(t, ws, "sessions") != 0 || inv.starts() != 0 {
		t.Error("the refused message left a session or started the engine")
	}
}

// 無くした項目が残る宣言では、起動したままのプロセスがあっても発言を受けない。
func TestChat_RemovedDeclarationKeyRefusesTheMessage(t *testing.T) {
	e := newChatEnv(t)
	conv := e.send(t, 0, "x")
	conv.reply(core.ChatStateDone, nil)
	if err := os.WriteFile(filepath.Join(e.ws, ".flywheel", "agent.json"), []byte(`{"chat_tools":"restricted"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, body := sendChat(t, e.port, "y")
	if code == 200 || !strings.Contains(body, "config_invalid") || !strings.Contains(body, "chat_tools") || !strings.Contains(body, "removed") {
		t.Errorf("%d %s", code, body)
	}
	if n := chatCount(t, e.ws, "messages"); n != 2 {
		t.Errorf("messages = %d, want 2", n)
	}
}

// GET …/chat に tools は無い。過去の形の道具の一覧の要素は tool が Bash、state が ok か error になる。
func TestChat_GetHasNoToolsAndReadsTheLegacyActivity(t *testing.T) {
	e := newChatEnv(t)
	conv := e.send(t, 0, "x")
	conv.reply(core.ChatStateDone, nil)
	coretest.Exec(t, e.ws, `UPDATE chat_message SET activity = '[{"command":"ls","ok":true},{"command":"nosuch","ok":false}]' WHERE role = 'orchestrator'`)
	code, body := apiGet(t, e.port, chatBase)
	if code != 200 || strings.Contains(body, `"tools"`) {
		t.Fatalf("%d %s", code, body)
	}
	got := lastReply(getChat(t, e.port)).Activity
	want := []view.ChatActivity{{Tool: "Bash", Target: "ls", State: "ok"}, {Tool: "Bash", Target: "nosuch", State: "error"}}
	if !slices.Equal(got, want) {
		t.Errorf("activity = %+v, want %+v", got, want)
	}
}

// サーバを止めると、すべてのプロセスが終わり、進行中の返事は stopped、答えを待つ確認は expired になる。
func TestChat_ShutdownStopsEveryProcess(t *testing.T) {
	ws1, ws2 := initWorkspace(t), initWorkspace(t)
	inv := newFakeChat()
	s, port := startChatServer(t, inv, eventsConfig{},
		core.FleetWorkspace{Name: "w", Path: ws1}, core.FleetWorkspace{Name: "v", Path: ws2})
	if code, body := sendChat(t, port, "x"); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	replying := inv.conv(t, 0)
	replying.waitSend(t)
	replying.speech("途中")
	replying.prompt(permissionPrompt("r1"))
	if code, body := apiPost(t, port, "/api/v1/workspaces/v/chat/messages", `{"text":"y"}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	idle := inv.conv(t, 1) // エージェントごとに 1 つで、またぐ上限は無い
	idle.waitSend(t)
	idle.reply(core.ChatStateDone, nil)
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if !replying.isExited() || !idle.isExited() {
		t.Errorf("exited = %v, %v", replying.isExited(), idle.isExited())
	}
	for ws, want := range map[string]core.ChatMessageState{ws1: core.ChatStateStopped, ws2: core.ChatStateDone} {
		st, err := core.OpenWorkspace(ws)
		if err != nil {
			t.Fatal(err)
		}
		c, err := st.ReadChat(context.Background())
		_ = st.Close()
		if err != nil || c.Messages[1].State != want {
			t.Errorf("chat = %+v (%v), want %s", c, err, want)
			continue
		}
		if want == core.ChatStateStopped && (len(c.Messages[1].Prompts) != 1 || c.Messages[1].Prompts[0].State != core.ChatPromptExpired) {
			t.Errorf("prompts = %+v", c.Messages[1].Prompts)
		}
	}
}

// 起動のとき、残った進行中の返事は stopped、残った答えを待つ確認は expired になる。ストアの無いワークスペースは
// 作らずに飛ばす。その後の発言は、始まりの行が出た会話なら続きで起動する。
func TestChat_StartupCleansUpLeftoversAndSkipsMissingStores(t *testing.T) {
	ws := initWorkspace(t)
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	turn, err := st.PostChatMessage(ctx, "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkChatEngineStarted(ctx, turn.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordChatPrompt(ctx, turn.ReplyMessageID, permissionPrompt("r1")); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	// 返事が進行中でないのに確認だけが残ったワークスペース。
	onlyPrompt := initWorkspace(t)
	st, err = core.OpenWorkspace(onlyPrompt)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.PostChatMessage(ctx, "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishChatReplyWithTotal(ctx, other.ReplyMessageID, core.ChatStateDone, nil); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	coretest.Exec(t, onlyPrompt, `INSERT INTO chat_prompt (message_id, request_id, kind, tool, target, state, created_at)
		VALUES (?, 'z', 'permission', 'Bash', 'z', 'pending', '2026-11-25T00:00:00.000Z')`, other.ReplyMessageID)

	empty := t.TempDir()
	inv := newFakeChat()
	_, port := startChatServer(t, inv, eventsConfig{},
		core.FleetWorkspace{Name: "w", Path: ws}, core.FleetWorkspace{Name: "p", Path: onlyPrompt}, core.FleetWorkspace{Name: "none", Path: empty})
	c := getChat(t, port)
	if r := lastReply(c); c.State != "idle" || r.State != "stopped" || r.Prompts[0].State != "expired" {
		t.Errorf("leftover chat = %+v", c)
	}
	if got := coretest.DumpTables(t, onlyPrompt, "chat_prompt"); strings.Contains(got, "pending") {
		t.Errorf("a pending prompt remains: %s", got)
	}
	if entries, _ := os.ReadDir(empty); len(entries) != 0 {
		t.Errorf("startup created %d entries in a workspace without a store", len(entries))
	}
	if code, body := sendChat(t, port, "続き"); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if in := inv.conv(t, 0).in; !in.Resume || in.SessionID != turn.EngineSession {
		t.Errorf("start input = %+v, want resume", in)
	}
}

// AC-13〜AC-18: 課題・タスクの識別を付けると、題名は core から引かれ、行と実行エンジンへの入力に載る。
func TestChat_ViewingTitlesComeFromCore(t *testing.T) {
	ws, port, inv := chatWorkspace(t)
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateChallenge(context.Background(), core.ChannelCLI, core.CreateInput{Title: "課題の題名", DoneCriteria: "d"}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	coretest.InsertApprovedPlan(t, ws, 1, "body", approvalPlanSpec)

	send := func(v string) (int, string) {
		return apiPost(t, port, chatBase+"/messages", `{"text":"これは？","viewing":`+v+`}`)
	}
	if code, body := send(`{"challenge_id":"C-1","task_id":"T-1","title":"偽の題名"}`); code != 400 {
		t.Fatalf("unknown field in viewing = %d %s, want 400", code, body)
	}
	if code, body := send(`{"challenge_id":"C-1","task_id":"T-1"}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	conv := inv.conv(t, 0)
	sent := conv.waitSend(t)
	conv.reply(core.ChatStateDone, nil)
	c := waitReply(t, port)
	v := c.Messages[0].Viewing
	if v == nil || *v.ChallengeID != "C-1" || *v.TaskID != "T-1" || *v.Title != "課題の題名" {
		t.Fatalf("viewing = %+v", v)
	}
	in := sent
	if in.Viewing == nil || in.Viewing.Title != "課題の題名" || in.Viewing.ChallengeID != "C-1" || in.Viewing.TaskID != "T-1" || in.Viewing.TaskTitle != "TA" {
		t.Errorf("sent viewing = %+v", in.Viewing)
	}
}

// AC-19〜AC-22・AC-42: 引けない識別と空白だけの文は 400 で、行を作らない（実行エンジンも起動しない）。
func TestChat_BadViewingAndBlankTextAre400WithoutRows(t *testing.T) {
	ws, port, inv := chatWorkspace(t)
	st, _ := core.OpenWorkspace(ws)
	for _, title := range []string{"a", "b"} {
		if _, err := st.CreateChallenge(context.Background(), core.ChannelCLI, core.CreateInput{Title: title, DoneCriteria: "d"}); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.Close()
	coretest.InsertApprovedPlan(t, ws, 1, "body", approvalPlanSpec)
	for name, body := range map[string]string{
		"no_such_challenge": `{"text":"x","viewing":{"challenge_id":"C-99"}}`,
		"no_such_task":      `{"text":"x","viewing":{"challenge_id":"C-1","task_id":"T-99"}}`,
		"foreign_task":      `{"text":"x","viewing":{"challenge_id":"C-2","task_id":"T-1"}}`,
		"task_only":         `{"text":"x","viewing":{"task_id":"T-1"}}`,
		"blank":             `{"text":" \n\t "}`,
		"not_json":          `nope`,
	} {
		if code, resp := apiPost(t, port, chatBase+"/messages", body); code != 400 {
			t.Errorf("%s: %d %s, want 400", name, code, resp)
		}
	}
	if n := chatCount(t, ws, "messages"); n != 0 {
		t.Errorf("%d messages, want none", n)
	}
	if inv.starts() != 0 {
		t.Error("the engine was started for a refused message")
	}
}

// AC-43〜AC-45: CSRF の値が無い POST は断られる。
func TestChat_PostsNeedCSRF(t *testing.T) {
	ws, port, inv := chatWorkspace(t)
	for _, p := range []string{"/messages", "/stop", "/new"} {
		if code, _ := post(t, port, "/api/v1/workspaces/w/chat"+p, "", ""); code != 403 {
			t.Errorf("POST chat%s without CSRF = %d, want 403", p, code)
		}
	}
	if n := chatCount(t, ws, "messages"); n != 0 || inv.starts() != 0 {
		t.Errorf("refused posts had effects (%d messages)", n)
	}
}

// AC-174: 4 本とも、存在しないエージェントには 404。
func TestChat_UnknownAgentIs404(t *testing.T) {
	_, port, _ := chatWorkspace(t)
	if code, body := apiGet(t, port, "/api/v1/workspaces/nope/chat"); code != 404 {
		t.Errorf("GET = %d %s", code, body)
	}
	for _, p := range []string{"/messages", "/stop", "/new"} {
		if code, body := apiPost(t, port, "/api/v1/workspaces/nope/chat"+p, `{"text":"x"}`); code != 404 {
			t.Errorf("POST chat%s = %d %s, want 404", p, code, body)
		}
	}
}

// 欄の返事に費用があっても、サイクルの記録は変わらない。
func TestChat_ReplyCostDoesNotChangeCycleSpentUSD(t *testing.T) {
	e := newChatEnv(t)
	coretest.InsertCycle(t, e.ws, "ui", 1_000_000, "2026-11-01T00:00:00.000Z")
	coretest.InsertCycle(t, e.ws, "cli", 2_000_000, "2026-11-02T00:00:00.000Z")
	before := coretest.DumpTables(t, e.ws, "cycle", "run")
	if strings.Count(before, "\n") < 3 { // 見出し 1 行 + 周の記録 2 行
		t.Fatalf("fixture has no cycle rows: %q", before)
	}
	conv := e.send(t, 0, "費用のある返事")
	conv.reply(core.ChatStateDone, usdTotal(0.5))
	if r := lastReply(waitReply(t, e.port)); r.CostUSD != 0.5 {
		t.Fatalf("reply = %+v", r)
	}
	if after := coretest.DumpTables(t, e.ws, "cycle", "run"); after != before {
		t.Fatalf("cycle/run tables changed:\n%s\n->\n%s", before, after)
	}
}

// AC-173: 返事の行へ発言が足されると、いまある変化の知らせが流れる。
func TestChat_SpeechNotifiesExistingEvents(t *testing.T) {
	ws := initWorkspace(t)
	inv := newFakeChat()
	_, port := startChatServer(t, inv, eventsConfig{watchInterval: testWatch, keepAliveInterval: testKeepAlive}, core.FleetWorkspace{Name: "w", Path: ws})
	sse := subscribe(t, port)
	if _, ok := sse.wait(2*time.Second, func(l string) bool { return strings.HasPrefix(l, ": connected") }); !ok {
		t.Fatal("not connected")
	}
	sendChat(t, port, "x")
	conv := inv.conv(t, 0)
	conv.waitSend(t)
	// 返事の行を作った分の通知を読み捨てたうえで、発言を足した後の通知を待つ。
	for {
		if _, ok := sse.wait(4*testWatch, isAnyChange); !ok {
			break // 静かになるまで読み捨てる
		}
	}
	conv.speech("発言")
	if _, ok := sse.wait(5*time.Second, isChange("w")); !ok {
		t.Error("no change event after a speech was appended")
	}
}
