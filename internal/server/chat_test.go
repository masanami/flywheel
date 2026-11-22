package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
	"github.com/masanami/flywheel/internal/view"
)

// このファイルは対話の欄の口（docs/features/core-redesign-s4d.md S4d-1。AC-10〜AC-174 のうちサーバの分）の試験。
// 偽にするのは対話の実行のインターフェース（core.ChatInvoker）だけで、ストアは実物（t.TempDir() の SQLite）を使う。

const chatBase = "/api/v1/workspaces/w/chat"

// fakeChat は core.ChatInvoker の偽実装。run が返事の中身を決める（nil なら何も出さず done で終わる）。
type fakeChat struct {
	mu       sync.Mutex
	availErr error
	inputs   []core.ChatLaunchInput
	run      func(ctx context.Context, in core.ChatLaunchInput, ev core.ChatEvents) (core.ChatLaunchOutput, error)
	started  chan struct{} // 返事が始まるたびに 1 つ送る
}

func newFakeChat() *fakeChat { return &fakeChat{started: make(chan struct{}, 16)} }

func (f *fakeChat) Available(context.Context) error { return f.availErr }

func (f *fakeChat) InvokeChat(ctx context.Context, in core.ChatLaunchInput, ev core.ChatEvents) (core.ChatLaunchOutput, error) {
	f.mu.Lock()
	f.inputs = append(f.inputs, in)
	run := f.run
	f.mu.Unlock()
	f.started <- struct{}{}
	if run == nil {
		return core.ChatLaunchOutput{State: core.ChatStateDone}, nil
	}
	return run(ctx, in, ev)
}

func (f *fakeChat) calls() []core.ChatLaunchInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]core.ChatLaunchInput(nil), f.inputs...)
}

// blockUntilCancelled は、取り消されるまで待ち、stopped で終わる返事（止められる返事）。
func blockUntilCancelled(first string) func(context.Context, core.ChatLaunchInput, core.ChatEvents) (core.ChatLaunchOutput, error) {
	return func(ctx context.Context, _ core.ChatLaunchInput, ev core.ChatEvents) (core.ChatLaunchOutput, error) {
		if ev.Started != nil {
			ev.Started()
		}
		if first != "" {
			ev.Speech(first)
		}
		<-ctx.Done()
		return core.ChatLaunchOutput{State: core.ChatStateStopped}, nil
	}
}

func startChatServer(t *testing.T, inv core.ChatInvoker, cfg eventsConfig, ws ...core.FleetWorkspace) (*Server, int) {
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

func chatWorkspace(t *testing.T) (ws string, port int, inv *fakeChat) {
	t.Helper()
	ws = initWorkspace(t)
	inv = newFakeChat()
	_, port = startChatServer(t, inv, eventsConfig{}, core.FleetWorkspace{Name: "w", Path: ws})
	return ws, port, inv
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

// waitReply は返事（最後の発言）が replying でなくなるまで待ち、その時点の会話を返す。
func waitReply(t *testing.T, port int) view.ChatResponse {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c := getChat(t, port)
		if c.State == view.ChatStateIdle && len(c.Messages) > 0 {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("reply did not finish: %+v", c)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitStarted(t *testing.T, inv *fakeChat) {
	t.Helper()
	select {
	case <-inv.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the engine was not started")
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

// AC-10・AC-11・AC-12・AC-24・AC-48・AC-49・AC-169〜AC-172: 文を送ると、発言の行が 1 つ出来、実行エンジンが 1 回、
// 新しい会話の指定で、文の本文つきで起動され、返事が done で終わる。
func TestChat_SendStartsEngineOnceAndFinishesDone(t *testing.T) {
	ws, port, inv := chatWorkspace(t)
	empty := getChat(t, port)
	if empty.SessionID != nil || len(empty.Messages) != 0 || empty.State != view.ChatStateIdle || empty.Tools != "restricted" {
		t.Fatalf("empty chat = %+v", empty)
	}
	inv.run = func(_ context.Context, _ core.ChatLaunchInput, ev core.ChatEvents) (core.ChatLaunchOutput, error) {
		ev.Started()
		ev.Speech("こんにちは")
		return core.ChatLaunchOutput{State: core.ChatStateDone}, nil
	}
	code, body := sendChat(t, port, "状況は？")
	if code != 200 {
		t.Fatalf("POST = %d %s", code, body)
	}
	var resp view.ChatPostResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil || resp.ID == 0 {
		t.Fatalf("response = %s (%v)", body, err)
	}
	c := waitReply(t, port)
	if len(c.Messages) != 2 || c.Messages[0].Role != "owner" || c.Messages[0].ID != resp.ID || c.Messages[0].Body != "状況は？" {
		t.Fatalf("messages = %+v", c.Messages)
	}
	if r := lastReply(c); r.Role != "orchestrator" || r.State != "done" || r.Body != "こんにちは" {
		t.Errorf("reply = %+v", r)
	}
	calls := inv.calls()
	if len(calls) != 1 {
		t.Fatalf("engine started %d times, want 1", len(calls))
	}
	if calls[0].Text != "状況は？" || calls[0].Resume || calls[0].SessionID != *c.SessionID || calls[0].WorkDir != ws {
		t.Errorf("input = %+v", calls[0])
	}
	if !envHas(calls[0].Env, core.ChatEnvSession+"="+*c.SessionID) || !envHas(calls[0].Env, core.ChatEnvTools+"=restricted") {
		t.Errorf("env = %v", calls[0].Env)
	}
	// engine_started が 1 になるので、2 通目は続きの指定（同じ UUID）。
	if code, body := sendChat(t, port, "続き"); code != 200 {
		t.Fatalf("2nd POST = %d %s", code, body)
	}
	waitReply(t, port)
	calls = inv.calls()
	if len(calls) != 2 || !calls[1].Resume || calls[1].SessionID != calls[0].SessionID {
		t.Errorf("2nd input = %+v, want Resume with the same session", calls)
	}
}

func envHas(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

// 実行エンジンの PATH の先頭の flywheel は、サーバの実行ファイルを指す（返事の後に消える）。
func TestChat_PathHeadHasFlywheelSymlinkRemovedAfterReply(t *testing.T) {
	ws := initWorkspace(t)
	inv := newFakeChat()
	exe := filepath.Join(t.TempDir(), "server-bin")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	s, port := startChatServer(t, inv, eventsConfig{}, core.FleetWorkspace{Name: "w", Path: ws})
	s.chat.executable = func() (string, error) { return exe, nil }
	var dir, target string
	inv.run = func(_ context.Context, in core.ChatLaunchInput, _ core.ChatEvents) (core.ChatLaunchOutput, error) {
		for _, e := range in.Env {
			if v, ok := strings.CutPrefix(e, "PATH="); ok {
				dir = strings.SplitN(v, string(os.PathListSeparator), 2)[0]
			}
		}
		target, _ = os.Readlink(filepath.Join(dir, "flywheel"))
		return core.ChatLaunchOutput{State: core.ChatStateDone}, nil
	}
	if code, body := sendChat(t, port, "x"); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	waitReply(t, port)
	if target != exe {
		t.Errorf("flywheel -> %q, want %q", target, exe)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s was not removed", dir)
		}
		time.Sleep(10 * time.Millisecond)
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
	c := waitReply(t, port)
	v := c.Messages[0].Viewing
	if v == nil || *v.ChallengeID != "C-1" || *v.TaskID != "T-1" || *v.Title != "課題の題名" {
		t.Fatalf("viewing = %+v", v)
	}
	in := inv.calls()[0]
	if in.Viewing == nil || in.Viewing.Title != "課題の題名" || in.Viewing.ChallengeID != "C-1" || in.Viewing.TaskID != "T-1" || in.Viewing.TaskTitle != "TA" {
		t.Errorf("input viewing = %+v", in.Viewing)
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
	if len(inv.calls()) != 0 {
		t.Error("the engine was started for a refused message")
	}
}

// AC-23・AC-38・AC-39・AC-152・AC-153・AC-155: 発言は 1 つ出し終えるたびに書かれ、コマンドと成否・費用が残る。
func TestChat_SpeechCommandsAndCostAreRecordedAsTheyHappen(t *testing.T) {
	ws, port, inv := chatWorkspace(t)
	cycleID := coretest.InsertCycle(t, ws, "ui", 1_000_000, "2026-11-01T00:00:00.000Z")
	_ = cycleID
	before := coretest.DumpTables(t, ws, "cycle")

	release := make(chan struct{})
	inv.run = func(ctx context.Context, _ core.ChatLaunchInput, ev core.ChatEvents) (core.ChatLaunchOutput, error) {
		ev.Started()
		ev.Speech("1 つ目")
		ev.Command(core.ChatCommand{Command: "flywheel status", OK: true})
		select {
		case <-release:
		case <-ctx.Done():
			return core.ChatLaunchOutput{State: core.ChatStateStopped}, nil
		}
		ev.Speech("2 つ目")
		ev.Command(core.ChatCommand{Command: "flywheel run C-1", OK: false})
		return core.ChatLaunchOutput{State: core.ChatStateDone, CostUSD: 0.12}, nil
	}
	if code, body := sendChat(t, port, "x"); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		c := getChat(t, port)
		if r := lastReply(c); r.Body == "1 つ目" && len(r.Activity) == 1 {
			if c.State != view.ChatStateReplying || r.State != "replying" || !r.Activity[0].Ok {
				t.Fatalf("mid-reply = %+v", c)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the first speech was not visible before the second: %+v", c)
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	c := waitReply(t, port)
	r := lastReply(c)
	if r.Body != "1 つ目\n\n2 つ目" || len(r.Activity) != 2 || r.Activity[1].Ok || r.CostUSD != 0.12 || r.State != "done" {
		t.Errorf("reply = %+v", r)
	}
	inv.run = func(_ context.Context, _ core.ChatLaunchInput, _ core.ChatEvents) (core.ChatLaunchOutput, error) {
		return core.ChatLaunchOutput{State: core.ChatStateDone, CostUSD: 0.30}, nil
	}
	sendChat(t, port, "もう 1 回")
	waitReply(t, port)
	deadline = time.Now().Add(10 * time.Second)
	for {
		c = getChat(t, port)
		if c.State == view.ChatStateIdle && c.CostUSD > 0.41 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cost_usd = %v, want 0.42", c.CostUSD)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if c.CostUSD != 0.42 {
		t.Errorf("cost_usd = %v, want 0.42", c.CostUSD)
	}
	if after := coretest.DumpTables(t, ws, "cycle"); after != before {
		t.Errorf("cycle table changed by chat replies:\n%s\n->\n%s", before, after)
	}
}

// AC-28・AC-29・AC-58: 返事が進行中の送信と「新しい会話」は 409 で、行を増やさない。
// AC-31・AC-33・AC-34・AC-156・AC-61: 止めると実行エンジンが終わり、stopped・出し終えた発言は残り、費用は 0。
// AC-35: 進行中でない stop は何も変えずに成功。
func TestChat_ReplyingRefusesSendAndNewThenStopEndsIt(t *testing.T) {
	ws, port, inv := chatWorkspace(t)
	if code, _ := apiPost(t, port, chatBase+"/stop", ""); code/100 != 2 {
		t.Errorf("idle stop = %d, want success", code)
	}
	if n := chatCount(t, ws, "messages"); n != 0 {
		t.Errorf("idle stop changed the store: %d messages", n)
	}
	inv.run = blockUntilCancelled("止める前の発言")
	if code, body := sendChat(t, port, "長い仕事"); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	waitStarted(t, inv)
	if code, body := sendChat(t, port, "もう 1 つ"); code != 409 {
		t.Errorf("send while replying = %d %s, want 409", code, body)
	}
	if code, body := apiPost(t, port, chatBase+"/new", ""); code != 409 {
		t.Errorf("new while replying = %d %s, want 409", code, body)
	}
	if n := chatCount(t, ws, "messages"); n != 2 {
		t.Errorf("%d messages after the 409s, want 2", n)
	}
	if code, body := apiPost(t, port, chatBase+"/stop", ""); code/100 != 2 {
		t.Fatalf("stop = %d %s", code, body)
	}
	c := getChat(t, port) // stop は終わりが記録されてから返る
	if r := lastReply(c); r.State != "stopped" || r.Body != "止める前の発言" || r.CostUSD != 0 {
		t.Errorf("reply after stop = %+v", r)
	}
	// 止めた後は、続きの指定（会話の始まりの行は出ていた）で次の文を送れる。
	inv.run = nil
	if code, body := sendChat(t, port, "続き"); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	waitReply(t, port)
	if calls := inv.calls(); len(calls) != 2 || !calls[1].Resume {
		t.Errorf("after stop: %+v, want the 2nd start to resume", calls)
	}
}

// AC-50・AC-53・AC-160・AC-161・AC-163: limit で終わっても始まりの行が出ていれば次は続き。何も出さずに failed なら
// 次も新しい会話。発言の後に failed でも、発言は残り、次の文を送れる。
func TestChat_EngineStartedDecidesNewOrResume(t *testing.T) {
	_, port, inv := chatWorkspace(t)
	inv.run = func(_ context.Context, _ core.ChatLaunchInput, _ core.ChatEvents) (core.ChatLaunchOutput, error) {
		return core.ChatLaunchOutput{}, errors.New("exit status 1")
	}
	sendChat(t, port, "1")
	if r := lastReply(waitReply(t, port)); r.State != "failed" {
		t.Fatalf("reply = %+v, want failed", r)
	}
	inv.run = func(_ context.Context, _ core.ChatLaunchInput, ev core.ChatEvents) (core.ChatLaunchOutput, error) {
		ev.Started()
		ev.Speech("途中")
		return core.ChatLaunchOutput{State: core.ChatStateFailed, ErrorSummary: "exit status 1"}, nil
	}
	if code, body := sendChat(t, port, "2"); code != 200 {
		t.Fatalf("send after failed = %d %s", code, body)
	}
	if r := lastReply(waitReply(t, port)); r.State != "failed" || r.Body != "途中" {
		t.Fatalf("reply = %+v", r)
	}
	inv.run = func(_ context.Context, _ core.ChatLaunchInput, _ core.ChatEvents) (core.ChatLaunchOutput, error) {
		return core.ChatLaunchOutput{State: core.ChatStateLimit}, nil
	}
	sendChat(t, port, "3")
	waitReply(t, port)
	sendChat(t, port, "4")
	waitReply(t, port)
	calls := inv.calls()
	if len(calls) != 4 {
		t.Fatalf("%d calls", len(calls))
	}
	// 1 通目は何も出さずに failed（新しい会話のまま）、2 通目は始まりの行が出た（failed でも次は続き）。
	for i, want := range []bool{false, false, true, true} {
		if calls[i].Resume != want {
			t.Errorf("call %d Resume = %v, want %v", i+1, calls[i].Resume, want)
		}
	}
}

// 会話を続けられない返事は session_lost で記録され、会話が閉じる（次の文は新しい会話。自動では送り直さない）。
func TestChat_SessionLostClosesTheSession(t *testing.T) {
	ws, port, inv := chatWorkspace(t)
	inv.run = func(_ context.Context, _ core.ChatLaunchInput, _ core.ChatEvents) (core.ChatLaunchOutput, error) {
		return core.ChatLaunchOutput{State: core.ChatStateSessionLost}, nil
	}
	sendChat(t, port, "1")
	waitStarted(t, inv)
	deadline := time.Now().Add(10 * time.Second)
	for getChat(t, port).SessionID != nil {
		if time.Now().After(deadline) {
			t.Fatal("the session was not closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(inv.calls()) != 1 {
		t.Fatalf("calls = %d, want 1 (no automatic resend)", len(inv.calls()))
	}
	if c := getChat(t, port); len(c.Messages) != 0 {
		t.Errorf("chat = %+v, want no open session", c)
	}
	if dump := coretest.DumpTables(t, ws, "chat_message"); !strings.Contains(dump, "session_lost") {
		t.Errorf("the reply was not recorded as session_lost: %s", dump)
	}
}

// AC-54〜AC-57: new は会話を閉じ、次の送信は新しい会話を作る。閉じた会話の発言の行は残る。
func TestChat_NewClosesSessionAndKeepsRows(t *testing.T) {
	ws, port, inv := chatWorkspace(t)
	sendChat(t, port, "1")
	waitReply(t, port)
	first := *getChat(t, port).SessionID
	if code, body := apiPost(t, port, chatBase+"/new", ""); code/100 != 2 {
		t.Fatalf("new = %d %s", code, body)
	}
	if c := getChat(t, port); c.SessionID != nil || len(c.Messages) != 0 {
		t.Errorf("after new: %+v", c)
	}
	sendChat(t, port, "2")
	waitReply(t, port)
	second := *getChat(t, port).SessionID
	if second == first {
		t.Error("the second chat reused the closed session")
	}
	if calls := inv.calls(); len(calls) != 2 || calls[1].Resume {
		t.Errorf("calls = %+v, want the 2nd to be a new conversation", calls)
	}
	// AC-57: 閉じた会話の発言の行（owner の「1」と返事の 2 行）が残っている。
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if dump := coretest.DumpTables(t, ws, "chat_message"); strings.Count(dump, "orchestrator") != 2 || strings.Count(dump, "owner") != 2 {
		t.Errorf("closed session's chat_message rows are gone: %s", dump)
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
	if n := chatCount(t, ws, "messages"); n != 0 || len(inv.calls()) != 0 {
		t.Errorf("refused posts had effects (%d messages)", n)
	}
}

// AC-74: tools は宣言の道具の範囲。環境の印にも同じ値が入る。
func TestChat_ToolsFollowTheDeclaration(t *testing.T) {
	ws := initWorkspace(t)
	if err := os.MkdirAll(filepath.Join(ws, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".flywheel", "agent.json"), []byte(`{"version":1,"chat_tools":"full","chat_max_turns":2,"chat_timeout_sec":7}`), 0o644); err != nil {
		t.Fatal(err)
	}
	inv := newFakeChat()
	_, port := startChatServer(t, inv, eventsConfig{}, core.FleetWorkspace{Name: "w", Path: ws})
	if c := getChat(t, port); c.Tools != "full" {
		t.Errorf("tools = %q, want full", c.Tools)
	}
	sendChat(t, port, "x")
	waitReply(t, port)
	in := inv.calls()[0]
	if in.Tools != core.ChatToolsFull || in.MaxTurns != 2 || in.TimeoutSec != 7 || !envHas(in.Env, core.ChatEnvTools+"=full") {
		t.Errorf("input = %+v", in)
	}
}

// AC-157・AC-158: 実行エンジンのコマンドが見つからないときは 503 で、行を作らない。
func TestChat_EngineUnavailableIs503WithoutRows(t *testing.T) {
	ws, port, inv := chatWorkspace(t)
	inv.availErr = errors.New("claude not found")
	if code, body := sendChat(t, port, "x"); code != 503 {
		t.Errorf("%d %s, want 503", code, body)
	}
	if n := chatCount(t, ws, "messages"); n != 0 {
		t.Errorf("%d messages, want none", n)
	}
	if len(inv.calls()) != 0 {
		t.Error("the engine was started")
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

// AC-166: サーバを止めると、進行中の返事の実行エンジンが終わる。
func TestChat_ShutdownStopsTheEngine(t *testing.T) {
	ws := initWorkspace(t)
	inv := newFakeChat()
	finished := make(chan struct{})
	inv.run = func(ctx context.Context, _ core.ChatLaunchInput, _ core.ChatEvents) (core.ChatLaunchOutput, error) {
		<-ctx.Done()
		close(finished)
		return core.ChatLaunchOutput{State: core.ChatStateStopped}, nil
	}
	srv, port := startChatServer(t, inv, eventsConfig{}, core.FleetWorkspace{Name: "w", Path: ws})
	if code, body := sendChat(t, port, "x"); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	waitStarted(t, inv)
	_ = srv.Shutdown()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("the engine was not stopped by Shutdown")
	}
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	c, _ := st.ReadChat(context.Background())
	if c.Replying || c.Messages[1].State != core.ChatStateStopped {
		t.Errorf("reply after shutdown = %+v", c.Messages)
	}
}

// AC-167・AC-168: 起動のとき、進行中のまま残った返事は stopped になる。ストアの無いワークスペースは作らずに飛ばす。
func TestChat_StartupStopsLeftoverRepliesAndSkipsMissingStores(t *testing.T) {
	ws := initWorkspace(t)
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PostChatMessage(context.Background(), "x", nil); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	empty := t.TempDir()
	startChatServer(t, newFakeChat(), eventsConfig{},
		core.FleetWorkspace{Name: "w", Path: ws}, core.FleetWorkspace{Name: "none", Path: empty})
	st, err = core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	c, _ := st.ReadChat(context.Background())
	if c.Replying || c.Messages[1].State != core.ChatStateStopped || c.Messages[1].EndedAt == nil {
		t.Errorf("leftover reply = %+v", c.Messages)
	}
	if entries, _ := os.ReadDir(empty); len(entries) != 0 {
		t.Errorf("startup created %d entries in a workspace without a store", len(entries))
	}
}

// AC-173: 返事の行へ発言が足されると、いまある変化の知らせが流れる。
func TestChat_SpeechNotifiesExistingEvents(t *testing.T) {
	ws := initWorkspace(t)
	inv := newFakeChat()
	release := make(chan struct{})
	inv.run = func(_ context.Context, _ core.ChatLaunchInput, ev core.ChatEvents) (core.ChatLaunchOutput, error) {
		<-release
		ev.Speech("発言")
		<-release
		return core.ChatLaunchOutput{State: core.ChatStateDone}, nil
	}
	_, port := startChatServer(t, inv, eventsConfig{watchInterval: testWatch, keepAliveInterval: testKeepAlive}, core.FleetWorkspace{Name: "w", Path: ws})
	sse := subscribe(t, port)
	if _, ok := sse.wait(2*time.Second, func(l string) bool { return strings.HasPrefix(l, ": connected") }); !ok {
		t.Fatal("not connected")
	}
	sendChat(t, port, "x")
	waitStarted(t, inv)
	// 返事の行を作った分の通知を読み捨てたうえで、発言を足した後の通知を待つ。
	for {
		if _, ok := sse.wait(4*testWatch, isAnyChange); !ok {
			break // 静かになるまで読み捨てる
		}
	}
	release <- struct{}{}
	if _, ok := sse.wait(5*time.Second, isChange("w")); !ok {
		t.Error("no change event after a speech was appended")
	}
	release <- struct{}{}
}
