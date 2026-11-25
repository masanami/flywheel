package invoker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

const waitLimit = 15 * time.Second

// convRecord は途中の分を、届いた順に 1 件 1 行で残す。
type convRecord struct {
	mu      sync.Mutex
	log     []string
	prompts []core.ChatPromptRequest
	ends    []core.ChatReplyEnd
	exits   []core.ChatExit
	changed chan struct{}
}

func newConvRecord() *convRecord { return &convRecord{changed: make(chan struct{}, 1)} }

func (r *convRecord) add(line string, f func()) {
	r.mu.Lock()
	r.log = append(r.log, line)
	if f != nil {
		f()
	}
	r.mu.Unlock()
	select {
	case r.changed <- struct{}{}:
	default:
	}
}

func (r *convRecord) events() core.ChatConversationEvents {
	return core.ChatConversationEvents{
		Started: func() { r.add("started", nil) },
		Speech:  func(s string) { r.add("speech "+s, nil) },
		ToolStarted: func(u core.ChatToolUse) {
			r.add("tool "+u.ID+" "+u.Tool+" "+u.Target, nil)
		},
		ToolFinished: func(id string, ok bool) { r.add(fmt.Sprintf("result %s %t", id, ok), nil) },
		Prompt: func(p core.ChatPromptRequest) {
			r.add("prompt "+p.RequestID, func() { r.prompts = append(r.prompts, p) })
		},
		PromptCancelled: func(id string) { r.add("cancelled "+id, nil) },
		ReplyEnded: func(e core.ChatReplyEnd) {
			r.add("end "+string(e.State), func() { r.ends = append(r.ends, e) })
		},
		Exited: func(e core.ChatExit) { r.add("exited", func() { r.exits = append(r.exits, e) }) },
	}
}

func (r *convRecord) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.log)
}

// waitFor は cond（排他を取った状態で呼ぶ）が真になるまで待つ。
func (r *convRecord) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(waitLimit)
	for {
		r.mu.Lock()
		ok := cond()
		r.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-r.changed:
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for %s; got %q", what, r.snapshot())
		}
	}
}

func (r *convRecord) waitEnds(t *testing.T, n int) {
	t.Helper()
	r.waitFor(t, "reply end "+strconv.Itoa(n), func() bool { return len(r.ends) >= n })
}

func (r *convRecord) waitPrompts(t *testing.T, n int) {
	t.Helper()
	r.waitFor(t, "prompt "+strconv.Itoa(n), func() bool { return len(r.prompts) >= n })
}

func (r *convRecord) waitExit(t *testing.T) {
	t.Helper()
	r.waitFor(t, "exit", func() bool { return len(r.exits) > 0 })
}

func startInput(t *testing.T) core.ChatStartInput {
	t.Helper()
	return core.ChatStartInput{SessionID: chatSession, WorkDir: t.TempDir()}
}

// startFake は偽の claude を PATH に置いて会話を起動する。試験の終わりに止める。
func startFake(t *testing.T, fx fakeClaudeFixture, in core.ChatStartInput) (core.ChatConversation, *convRecord) {
	t.Helper()
	setFakeClaudePath(t, newFakeClaudeDir(t))
	t.Setenv(envFixture, writeFixture(t, fx))
	rec := newConvRecord()
	conv, err := NewLauncher().StartConversation(context.Background(), in, rec.events())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
		defer cancel()
		if err := conv.Stop(ctx); err != nil {
			t.Errorf("Stop at cleanup: %v", err)
		}
	})
	return conv, rec
}

func send(t *testing.T, conv core.ChatConversation, text string) {
	t.Helper()
	if err := conv.Send(context.Background(), core.ChatUtterance{Text: text}); err != nil {
		t.Fatalf("Send: %v", err)
	}
}

func captureLines(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.SplitAfter(strings.TrimRight(string(b), "\n")+"\n", "\n")
}

// splitAfterType は写しを、type が typ の行の直後で区切る（区切りの行は前の塊の末尾に残る）。
func splitAfterType(t *testing.T, name, typ string) []string {
	t.Helper()
	var chunks []string
	var cur strings.Builder
	for _, line := range captureLines(t, name) {
		if line == "" {
			continue
		}
		var l struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		cur.WriteString(line)
		if l.Type == typ {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		chunks = append(chunks, cur.String())
	}
	return chunks
}

// readSteps は塊ごとに「標準入力を 1 行読んでから出す」手順にする。
func readSteps(chunks ...string) []fakeStep {
	var out []fakeStep
	for _, c := range chunks {
		out = append(out, fakeStep{Read: true, Out: c})
	}
	return out
}

func stdinLines(t *testing.T, path string, n int) []string {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for {
		b, _ := os.ReadFile(path)
		lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		if len(b) > 0 && len(lines) >= n && strings.HasSuffix(string(b), "\n") {
			return lines
		}
		if time.Now().After(deadline) {
			t.Fatalf("stdin has %q, want %d lines", string(b), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func decodeJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not JSON: %q: %v", s, err)
	}
	return m
}

func conversationArgv(t *testing.T, resume bool) []string {
	t.Helper()
	argvLog := filepath.Join(t.TempDir(), "argv.log")
	in := startInput(t)
	in.Resume = resume
	conv, rec := startFake(t, fakeClaudeFixture{ArgvLogPath: argvLog, Script: []fakeStep{{Read: true, Out: initLine + resultLine("success", false, "x", "0", "completed")}}}, in)
	send(t, conv, "x")
	rec.waitEnds(t, 1)
	return readArgv(t, argvLog)
}

func TestConversation_Args(t *testing.T) {
	for _, resume := range []bool{false, true} {
		argv := conversationArgv(t, resume)
		if len(argv) == 0 || argv[0] != "-p" {
			t.Errorf("argv does not start with -p: %q", argv)
		}
		for flag, want := range map[string][]string{
			"--input-format": {"stream-json"}, "--output-format": {"stream-json"},
			"--permission-prompts": {"host"}, "--permission-prompt-tool": {"stdio"},
		} {
			if got := argsAfter(argv, flag); !slices.Equal(got, want) {
				t.Errorf("resume=%t: %s = %q, want %q", resume, flag, got, want)
			}
		}
		if !slices.Contains(argv, "--verbose") {
			t.Error("argv lacks --verbose")
		}
		for _, bad := range []string{"--setting-sources", "--allowedTools", "--disallowedTools", "--permission-mode", "--max-turns", "--dangerously-skip-permissions"} {
			if slices.Contains(argv, bad) {
				t.Errorf("resume=%t: argv has %s", resume, bad)
			}
		}
		flag, other := "--session-id", "--resume"
		if resume {
			flag, other = other, flag
		}
		if got := argsAfter(argv, flag); !slices.Equal(got, []string{chatSession}) || slices.Contains(argv, other) {
			t.Errorf("resume=%t: argv = %q", resume, argv)
		}
		ins, err := ChatInstructions()
		if err != nil {
			t.Fatal(err)
		}
		if i := slices.Index(argv, "--append-system-prompt"); i < 0 || i+1 >= len(argv) || argv[i+1] != ins {
			t.Error("--append-system-prompt does not carry chat.md")
		}
	}
}

func TestConversation_WorkDirAndEnv(t *testing.T) {
	cwdLog := filepath.Join(t.TempDir(), "cwd")
	envLog := filepath.Join(t.TempDir(), "env")
	t.Setenv(core.DelegatedRunEnvVar, "7")
	// 過去の印は、親の環境にあっても渡さない。
	t.Setenv(core.ChatEnvMessage, "1")
	t.Setenv(core.ChatEnvTools, "restricted")
	in := startInput(t)
	in.Env = core.ChatSessionMarkEnv(chatSession)
	conv, rec := startFake(t, fakeClaudeFixture{CwdLogPath: cwdLog, EnvDumpPath: envLog, Script: []fakeStep{{Read: true, Out: initLine}}}, in)
	send(t, conv, "x")
	rec.waitFor(t, "started", func() bool { return len(rec.log) > 0 })
	cwd, _ := os.ReadFile(cwdLog)
	want, _ := filepath.EvalSymlinks(in.WorkDir)
	if got, _ := filepath.EvalSymlinks(string(cwd)); got != want {
		t.Errorf("cwd = %q, want %q", got, want)
	}
	env, _ := os.ReadFile(envLog)
	lines := strings.Split(string(env), "\n")
	if !slices.Contains(lines, "FLYWHEEL_CHAT_SESSION="+chatSession) {
		t.Error("the session mark is missing")
	}
	for _, l := range lines {
		if strings.HasPrefix(l, core.ChatEnvMessage+"=") || strings.HasPrefix(l, core.ChatEnvTools+"=") {
			t.Errorf("an old chat mark reached the engine: %s", l)
		}
		if strings.HasPrefix(l, core.DelegatedRunEnvVar+"=") {
			t.Errorf("the delegated marker reached the engine: %s", l)
		}
	}
}

func TestConversation_StartErrors(t *testing.T) {
	setFakeClaudePath(t, t.TempDir())
	_, err := NewLauncher().StartConversation(context.Background(), startInput(t), core.ChatConversationEvents{})
	if !errors.Is(err, core.ErrChatEngineNotFound) || !errors.Is(err, ErrClaudeNotFound) {
		t.Errorf("err = %v, want ErrChatEngineNotFound", err)
	}

	setFakeClaudePath(t, newFakeClaudeDir(t))
	in := startInput(t)
	in.WorkDir = filepath.Join(in.WorkDir, "missing")
	if _, err := NewLauncher().StartConversation(context.Background(), in, core.ChatConversationEvents{}); err == nil || errors.Is(err, core.ErrChatEngineNotFound) {
		t.Errorf("missing work dir: err = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewLauncher().StartConversation(ctx, startInput(t), core.ChatConversationEvents{}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled ctx: err = %v", err)
	}
}

// 起動の後で起動の ctx を取り消しても、プロセスは止まらない。
func TestConversation_CancelingTheStartContextKeepsTheProcess(t *testing.T) {
	setFakeClaudePath(t, newFakeClaudeDir(t))
	t.Setenv(envFixture, writeFixture(t, fakeClaudeFixture{Script: []fakeStep{{Read: true, Out: initLine + resultLine("success", false, "x", "0", "completed")}}}))
	rec := newConvRecord()
	ctx, cancel := context.WithCancel(context.Background())
	conv, err := NewLauncher().StartConversation(ctx, startInput(t), rec.events())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conv.Stop(context.Background()) }()
	cancel()
	time.Sleep(200 * time.Millisecond)
	send(t, conv, "x")
	rec.waitEnds(t, 1)
}

// 実物の写し: 同じプロセスへ 2 通の発言を送り、返事ごとに途中の分と費用の累計を受ける。
func TestConversation_TwoRepliesFromTheRealCapture(t *testing.T) {
	argvLog := filepath.Join(t.TempDir(), "argv.log")
	stdinLog := filepath.Join(t.TempDir(), "stdin.log")
	chunks := splitAfterType(t, "chat_host_two_replies.jsonl", "result")
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	conv, rec := startFake(t, fakeClaudeFixture{ArgvLogPath: argvLog, StdinLogPath: stdinLog, Script: readSteps(chunks...)}, startInput(t))
	send(t, conv, "1 通目")
	rec.waitEnds(t, 1)
	send(t, conv, "2 通目")
	rec.waitEnds(t, 2)

	want := []string{
		"started",
		"tool toolu_016ob73H8z9ZZkJbuJrVBG1e Bash echo hello",
		"result toolu_016ob73H8z9ZZkJbuJrVBG1e true",
		"speech hello",
		"end done",
		"speech ok",
		"end done",
	}
	if got := rec.snapshot(); !slices.Equal(got, want) {
		t.Errorf("events = %q, want %q", got, want)
	}
	for i, total := range []float64{0.00442549, 0.0047786899999999995} {
		if e := rec.ends[i]; e.CostTotalUSD == nil || *e.CostTotalUSD != total || e.ErrorSummary != "" {
			t.Errorf("end %d = %+v, want total %v", i, e, total)
		}
	}
	if got := len(captureArgvLines(t, argvLog)); got != 1 {
		t.Errorf("the engine was started %d times, want 1", got)
	}
	lines := stdinLines(t, stdinLog, 2)
	m := decodeJSON(t, lines[1])
	msg, _ := m["message"].(map[string]any)
	if m["type"] != "user" || msg["role"] != "user" || msg["content"] != "# オーナーの発言\n\n2 通目\n" {
		t.Errorf("second utterance = %q", lines[1])
	}
}

func captureArgvLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// 写しの 2 回の返事の累計を core へ渡すと、2 回目の返事の費用は累計の差になる。
func TestConversation_SecondReplyCostIsTheDifferenceOfTheTotals(t *testing.T) {
	conv, rec := startFake(t, fakeClaudeFixture{Script: readSteps(splitAfterType(t, "chat_host_two_replies.jsonl", "result")...)}, startInput(t))
	send(t, conv, "a")
	rec.waitEnds(t, 1)
	send(t, conv, "b")
	rec.waitEnds(t, 2)

	ctx := context.Background()
	ws := t.TempDir()
	if _, err := core.Init(ws); err != nil {
		t.Fatal(err)
	}
	store, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	for i, e := range rec.ends {
		turn, err := store.PostChatMessage(ctx, "発言"+strconv.Itoa(i), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.FinishChatReplyWithTotal(ctx, turn.ReplyMessageID, e.State, e.CostTotalUSD); err != nil {
			t.Fatal(err)
		}
	}
	chat, err := store.ReadChat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var costs []float64
	for _, m := range chat.Messages {
		if m.Role == core.ChatRoleOrchestrator {
			costs = append(costs, m.CostUSD)
		}
	}
	// 100 万分の 1 USD の整数で記録する: 4425 と、4779 - 4425 = 354。
	if !slices.Equal(costs, []float64{0.004425, 0.000354}) {
		t.Errorf("reply costs = %v", costs)
	}
}

func TestConversation_PermissionPromptFromTheRealCapture(t *testing.T) {
	for _, tc := range []struct {
		capture, reqID string
		answer         core.ChatPromptAnswer
		want           string
		ok             string
	}{
		{"chat_host_permission_allow.jsonl", "ffa3dda9-d989-4dbf-82c3-0253d5614431", core.ChatPromptAnswer{Decision: core.ChatDecisionAllow},
			`{"behavior":"allow","updatedInput":{"command":"curl -s https://example.com | head -c 20","description":"Fetch example.com and print first 20 bytes"}}`, "true"},
		{"chat_host_permission_deny.jsonl", "0fff2793-e2c8-44e1-9554-98cc6637f30f", core.ChatPromptAnswer{Decision: core.ChatDecisionDeny, Message: "no"},
			`{"behavior":"deny","message":"no"}`, "false"},
		{"chat_host_permission_deny.jsonl", "0fff2793-e2c8-44e1-9554-98cc6637f30f", core.ChatPromptAnswer{Decision: core.ChatDecisionDeny},
			`{"behavior":"deny","message":"` + chatDefaultDenyMessage + `"}`, "false"},
	} {
		t.Run(tc.capture+" "+tc.answer.Message, func(t *testing.T) {
			stdinLog := filepath.Join(t.TempDir(), "stdin.log")
			chunks := splitAfterType(t, tc.capture, "control_request")
			conv, rec := startFake(t, fakeClaudeFixture{StdinLogPath: stdinLog, Script: readSteps(chunks...)}, startInput(t))
			send(t, conv, "x")
			rec.waitPrompts(t, 1)
			p := rec.prompts[0]
			if p.RequestID != tc.reqID || p.Kind != core.ChatPromptPermission || p.Tool != "Bash" ||
				p.Target != "curl -s https://example.com | head -c 20" || len(p.Questions) != 0 {
				t.Errorf("prompt = %+v", p)
			}
			if err := conv.Answer(context.Background(), p.RequestID, tc.answer); err != nil {
				t.Fatal(err)
			}
			rec.waitEnds(t, 1)
			assertControlResponse(t, stdinLines(t, stdinLog, 2)[1], tc.reqID, tc.want)
			if !slices.ContainsFunc(rec.snapshot(), func(s string) bool { return strings.HasPrefix(s, "result ") && strings.HasSuffix(s, " "+tc.ok) }) {
				t.Errorf("events = %q", rec.snapshot())
			}
			// 同じ確認への 2 回目の答えは送らない。
			if err := conv.Answer(context.Background(), p.RequestID, tc.answer); err == nil {
				t.Error("the second answer was accepted")
			}
		})
	}
}

func assertControlResponse(t *testing.T, line, reqID, wantResponse string) {
	t.Helper()
	got := decodeJSON(t, line)
	want := decodeJSON(t, `{"type":"control_response","response":{"subtype":"success","request_id":"`+reqID+`","response":`+wantResponse+`}}`)
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Errorf("control response = %s, want %s", g, w)
	}
}

func TestConversation_QuestionFromTheRealCapture(t *testing.T) {
	stdinLog := filepath.Join(t.TempDir(), "stdin.log")
	replies := splitAfterType(t, "chat_host_question.jsonl", "result")
	var second []string
	{
		// 2 通目の返事は、問いの確認の行で区切る。
		i := strings.Index(replies[1], `{"type": "control_request"`)
		j := i + strings.Index(replies[1][i:], "\n") + 1
		second = []string{replies[1][:j], replies[1][j:]}
	}
	conv, rec := startFake(t, fakeClaudeFixture{StdinLogPath: stdinLog, Script: readSteps(replies[0], second[0], second[1])}, startInput(t))
	send(t, conv, "a")
	rec.waitEnds(t, 1)
	send(t, conv, "b")
	rec.waitPrompts(t, 1)
	p := rec.prompts[0]
	wantQ := []core.ChatQuestion{{Question: "Which color do you prefer?", Options: []core.ChatQuestionOption{
		{Label: "Red", Description: "Choose red"}, {Label: "Blue", Description: "Choose blue"}}}}
	if p.Kind != core.ChatPromptQuestion || p.Tool != "AskUserQuestion" || fmt.Sprint(p.Questions) != fmt.Sprint(wantQ) {
		t.Errorf("prompt = %+v", p)
	}
	// 問いに「許可する」だけの答えは送らない（確認は待ったまま）。
	if err := conv.Answer(context.Background(), p.RequestID, core.ChatPromptAnswer{Decision: core.ChatDecisionAllow}); err == nil {
		t.Error("allow without answers was accepted for a question")
	}
	if err := conv.Answer(context.Background(), p.RequestID, core.ChatPromptAnswer{Answers: map[string]string{"Which color do you prefer?": "Red"}}); err != nil {
		t.Fatal(err)
	}
	rec.waitEnds(t, 2)
	// 実物で受け付けられた形（chat_host_stdin.jsonl の 4 行目）と同じ。
	stdinCapture := captureLines(t, "chat_host_stdin.jsonl")[3]
	assertControlResponse(t, stdinLines(t, stdinLog, 3)[2], p.RequestID,
		mustResponse(t, stdinCapture))
}

func mustResponse(t *testing.T, line string) string {
	t.Helper()
	var l struct {
		Response struct {
			Response json.RawMessage `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(line), &l); err != nil {
		t.Fatal(err)
	}
	return string(l.Response.Response)
}

func controlLine(t *testing.T, reqID, tool, input string, interaction bool) string {
	t.Helper()
	return fmt.Sprintf(`{"type":"control_request","request_id":%q,"request":{"subtype":"can_use_tool","tool_name":%q,"input":%s,"requires_user_interaction":%t}}`+"\n", reqID, tool, input, interaction)
}

func TestConversation_PromptKindAndTarget(t *testing.T) {
	long := strings.Repeat("あ", 1000)
	q := `{"questions":[{"question":"q?","header":"h","options":[{"label":"a","description":"d"}],"multiSelect":true}]}`
	out := controlLine(t, "r1", "Write", `{"file_path":"/x","content":"<a&b>`+long+`"}`, false) +
		controlLine(t, "r2", "AskUserQuestion", q, false) +
		controlLine(t, "r3", "AskUserQuestion", q, true) +
		// 大文字の違うキーは、コマンドの全文として読まない（実行されるのは command の値）。ほかの指定として見せる。
		controlLine(t, "r4", "Bash", `{"command":"rm -rf x\necho done","Command":"ls"}`, true) +
		controlLine(t, "r5", "Bash", `{"cmd":"ls"}`, false) +
		controlLine(t, "r6", "AskUserQuestion", `{"questions":[]}`, true)
	conv, rec := startFake(t, fakeClaudeFixture{Script: []fakeStep{{Read: true, Out: out}}}, startInput(t))
	send(t, conv, "x")
	rec.waitPrompts(t, 6)
	got := rec.prompts
	if got[0].Kind != core.ChatPromptPermission || got[0].Target != `{"file_path":"/x","content":"<a&b>`+long+`"}` {
		t.Errorf("Write: %+v", got[0])
	}
	if got[1].Kind != core.ChatPromptPermission || got[1].Target != q {
		t.Errorf("AskUserQuestion without interaction: %+v", got[1])
	}
	if got[2].Kind != core.ChatPromptQuestion || !got[2].Questions[0].MultiSelect {
		t.Errorf("question: %+v", got[2])
	}
	if got[3].Kind != core.ChatPromptPermission || got[3].Target != "rm -rf x\necho done\n\n指定: {\"Command\":\"ls\"}" {
		t.Errorf("Bash with interaction: %+v", got[3])
	}
	if got[4].Target != `{"cmd":"ls"}` {
		t.Errorf("Bash without command: %+v", got[4])
	}
	// 問いの中身が読めなければ、記録できる形（許可の確認）にする。
	if got[5].Kind != core.ChatPromptPermission || len(got[5].Questions) != 0 {
		t.Errorf("question without questions: %+v", got[5])
	}
}

func TestConversation_ExitPlanModeCaptureIsAPermission(t *testing.T) {
	var out strings.Builder
	for _, l := range captureLines(t, "chat_host_plan.jsonl") {
		out.WriteString(l)
	}
	conv, rec := startFake(t, fakeClaudeFixture{Script: []fakeStep{{Read: true, Out: out.String()}}}, startInput(t))
	send(t, conv, "x")
	rec.waitEnds(t, 1)
	if len(rec.prompts) != 2 || rec.prompts[0].Tool != "ExitPlanMode" || rec.prompts[0].Kind != core.ChatPromptPermission {
		t.Errorf("prompts = %+v", rec.prompts)
	}
}

// すべての道具を一覧に渡し、対象の 1 行は道具の種類で決める。サブエージェントの中の行と結果の中身は渡さない。
func TestConversation_ToolLines(t *testing.T) {
	use := func(id, name, input string) string {
		return `{"type":"assistant","parent_tool_use_id":null,"message":{"content":[{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + input + `}]}}` + "\n"
	}
	cases := []struct{ name, input, target string }{
		{"Bash", `{"command":"a \\\n b\r\nc"}`, "a \\  b c"},
		{"Read", `{"file_path":"/r"}`, "/r"},
		{"Write", `{"file_path":"/w","content":"SECRET"}`, "/w"},
		{"Edit", `{"file_path":"/e"}`, "/e"},
		{"MultiEdit", `{"file_path":"/m"}`, "/m"},
		{"NotebookEdit", `{"notebook_path":"/n"}`, "/n"},
		{"Grep", `{"pattern":"pat","path":"/p"}`, "pat"},
		{"Glob", `{"pattern":"*.go"}`, "*.go"},
		{"WebSearch", `{"query":"words"}`, "words"},
		{"ToolSearch", `{"query":"select:X"}`, "select:X"},
		{"Skill", `{"skill":"define-feature","args":"x"}`, "define-feature"},
		{"Task", `{"description":"調べる","prompt":"SECRET"}`, "調べる"},
		{"Agent", `{"description":"直す","prompt":"SECRET"}`, "直す"},
		{"WebFetch", `{"url":"https://example.com"}`, ""},
		{"mcp__x__y", `{"command":"SECRET"}`, ""},
	}
	var out strings.Builder
	var want []string
	for i, c := range cases {
		id := "t" + strconv.Itoa(i)
		out.WriteString(use(id, c.name, c.input))
		want = append(want, "tool "+id+" "+c.name+" "+c.target)
	}
	sub := `{"type":"assistant","parent_tool_use_id":"t11","message":{"content":[{"type":"text","text":"子の発言"},{"type":"tool_use","id":"sub","name":"Bash","input":{"command":"ls"}}]}}` + "\n" +
		`{"type":"user","parent_tool_use_id":"t11","message":{"content":[{"type":"tool_result","tool_use_id":"sub","is_error":false},{"type":"tool_result","tool_use_id":"t0","is_error":false}]}}` + "\n"
	out.WriteString(sub)
	// 文字列の content の行・渡していない道具の結果・2 回目の結果は渡さない。
	out.WriteString(`{"type":"user","message":{"role":"user","content":"echo"}}` + "\n")
	out.WriteString("not json\n")
	out.WriteString(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"unknown"},{"type":"tool_result","tool_use_id":"t1","content":"SECRET RESULT"},{"type":"tool_result","tool_use_id":"t2","is_error":true,"content":[{"type":"text","text":"SECRET RESULT"}]}]}}` + "\n")
	out.WriteString(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1"}]}}` + "\n")
	out.WriteString(resultLine("success", false, "", "0", "completed"))
	want = append(want, "result t1 true", "result t2 false", "end done")

	conv, rec := startFake(t, fakeClaudeFixture{Script: []fakeStep{{Read: true, Out: out.String()}}}, startInput(t))
	send(t, conv, "x")
	rec.waitEnds(t, 1)
	if got := rec.snapshot(); !slices.Equal(got, want) {
		t.Errorf("events =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestConversation_ReplyEndStates(t *testing.T) {
	lost, err := os.ReadFile(filepath.Join("testdata", "chat_stream_session_lost.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lostErr, err := os.ReadFile(filepath.Join("testdata", "chat_stream_session_lost.stderr.txt"))
	if err != nil {
		t.Fatal(err)
	}
	limitText := "You've hit your session limit · resets 3am"
	for _, tc := range []struct {
		name   string
		resume bool
		step   fakeStep
		want   core.ChatMessageState
		cost   string
	}{
		{"success", false, fakeStep{Out: initLine + resultLine("success", false, "x", "0.5", "completed")}, core.ChatStateDone, "0.5"},
		{"rate limited", false, fakeStep{Out: initLine + resultLine("success", true, limitText, "0.5", "completed")}, core.ChatStateRateLimited, "0.5"},
		{"rate limit words in a successful reply", false, fakeStep{Out: initLine + resultLine("success", false, limitText, "0.5", "completed")}, core.ChatStateDone, "0.5"},
		{"max turns is not limit", false, fakeStep{Out: initLine + resultLine("error_max_turns", true, "", "0.5", "max_turns")}, core.ChatStateFailed, "0.5"},
		{"failed", false, fakeStep{Out: initLine + resultLine("error_during_execution", true, "boom", "0.5", "completed")}, core.ChatStateFailed, "0.5"},
		{"session lost result line", true, fakeStep{Out: string(lost), Exit: true}, core.ChatStateSessionLost, "0"},
		{"session lost stderr only", true, fakeStep{Stderr: string(lostErr), Exit: true}, core.ChatStateSessionLost, ""},
		{"rate limit on stderr only", false, fakeStep{Stderr: limitText, Exit: true}, core.ChatStateRateLimited, ""},
		{"marker words after the conversation started", true, fakeStep{Out: initLine + resultLine("error_during_execution", true, "No conversation found", "0.5", "completed")}, core.ChatStateFailed, "0.5"},
		{"new conversation is never session lost", false, fakeStep{Out: string(lost)}, core.ChatStateFailed, "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := startInput(t)
			in.Resume = tc.resume
			step := tc.step
			// すぐ終わる場面は、発言を待たない（実物の続きの失敗は、起動してすぐ終わる）。
			step.Read = !step.Exit
			conv, rec := startFake(t, fakeClaudeFixture{Script: []fakeStep{step}, ExitCode: 1}, in)
			if step.Read {
				send(t, conv, "x")
			}
			rec.waitEnds(t, 1)
			e := rec.ends[0]
			if e.State != tc.want {
				t.Errorf("state = %s, want %s", e.State, tc.want)
			}
			got := ""
			if e.CostTotalUSD != nil {
				got = strconv.FormatFloat(*e.CostTotalUSD, 'f', -1, 64)
			}
			if got != tc.cost {
				t.Errorf("cost total = %q, want %q", got, tc.cost)
			}
			if (e.ErrorSummary == "") != (tc.want == core.ChatStateDone) {
				t.Errorf("error summary = %q", e.ErrorSummary)
			}
		})
	}
}

func TestConversation_ExitMidReply(t *testing.T) {
	conv, rec := startFake(t, fakeClaudeFixture{ExitCode: 2, Script: []fakeStep{{Read: true, Out: initLine + assistantText("途中"), Stderr: "crashed", Exit: true}}}, startInput(t))
	send(t, conv, "x")
	rec.waitExit(t)
	want := []string{"started", "speech 途中", "exited"}
	if got := rec.snapshot(); !slices.Equal(got, want) {
		t.Errorf("events = %q, want %q（終わりの行が無ければ返事の終わりは渡さない）", got, want)
	}
	if rec.exits[0].ErrorSummary != "crashed" {
		t.Errorf("exit = %+v", rec.exits[0])
	}
	if err := conv.Send(context.Background(), core.ChatUtterance{Text: "y"}); err == nil {
		t.Error("Send after exit succeeded")
	}
	if err := conv.Stop(context.Background()); err != nil {
		t.Errorf("Stop after exit: %v", err)
	}
}

func TestConversation_CleanExitHasNoErrorSummary(t *testing.T) {
	conv, rec := startFake(t, fakeClaudeFixture{Stderr: "", Script: []fakeStep{{Read: true, Stderr: "warning", Exit: true}}}, startInput(t))
	send(t, conv, "x")
	rec.waitExit(t)
	if rec.exits[0].ErrorSummary != "" {
		t.Errorf("exit = %+v", rec.exits[0])
	}
}

func TestConversation_AnswerAfterExitFails(t *testing.T) {
	conv, rec := startFake(t, fakeClaudeFixture{Script: []fakeStep{{Read: true, Out: controlLine(t, "r1", "Bash", `{"command":"ls"}`, false), Exit: true}}}, startInput(t))
	send(t, conv, "x")
	rec.waitExit(t)
	if err := conv.Answer(context.Background(), "r1", core.ChatPromptAnswer{Decision: core.ChatDecisionAllow}); err == nil {
		t.Error("Answer after exit succeeded")
	}
}

func TestConversation_AnswerRejectsUnknownAndCanceledRequests(t *testing.T) {
	out := controlLine(t, "r1", "Bash", `{"command":"ls"}`, false) + `{"type":"control_cancel_request","request_id":"r1"}` + "\n" + resultLine("success", false, "", "0", "completed")
	conv, rec := startFake(t, fakeClaudeFixture{Script: []fakeStep{{Read: true, Out: out}}}, startInput(t))
	send(t, conv, "x")
	rec.waitEnds(t, 1)
	for _, id := range []string{"r1", "nope"} {
		if err := conv.Answer(context.Background(), id, core.ChatPromptAnswer{Decision: core.ChatDecisionAllow}); err == nil {
			t.Errorf("Answer(%s) succeeded", id)
		}
	}
}

// 実行エンジンが確認を取り下げたら、呼び出し元へ取り下げを渡す（確認の後・返事の終わりの前の順で）。
func TestConversation_CancelledPromptIsPassedToTheCaller(t *testing.T) {
	out := controlLine(t, "r1", "Bash", `{"command":"ls"}`, false) + `{"type":"control_cancel_request","request_id":"r1"}` + "\n" + resultLine("success", false, "", "0", "completed")
	conv, rec := startFake(t, fakeClaudeFixture{Script: []fakeStep{{Read: true, Out: out}}}, startInput(t))
	send(t, conv, "x")
	rec.waitEnds(t, 1)
	if got, want := rec.snapshot(), []string{"prompt r1", "cancelled r1", "end done"}; !slices.Equal(got, want) {
		t.Errorf("log = %q, want %q", got, want)
	}
}

// 知らない識別の取り下げは、呼び出し元へ渡さない。
func TestConversation_CancelOfAnUnknownRequestIsNotPassed(t *testing.T) {
	out := `{"type":"control_cancel_request","request_id":"nope"}` + "\n" + resultLine("success", false, "", "0", "completed")
	conv, rec := startFake(t, fakeClaudeFixture{Script: []fakeStep{{Read: true, Out: out}}}, startInput(t))
	send(t, conv, "x")
	rec.waitEnds(t, 1)
	if got, want := rec.snapshot(), []string{"end done"}; !slices.Equal(got, want) {
		t.Errorf("log = %q, want %q", got, want)
	}
}

// 返事の合間の確認へ、呼び出し元は途中の分の関数の中から答えられる。
func TestConversation_AnswerFromInsideThePromptCallback(t *testing.T) {
	setFakeClaudePath(t, newFakeClaudeDir(t))
	stdinLog := filepath.Join(t.TempDir(), "stdin.log")
	t.Setenv(envFixture, writeFixture(t, fakeClaudeFixture{StdinLogPath: stdinLog, Script: []fakeStep{
		{Read: true, Out: controlLine(t, "r1", "Bash", `{"command":"ls"}`, false)},
		{Read: true, Out: resultLine("success", false, "", "0", "completed")},
	}}))
	rec := newConvRecord()
	ev := rec.events()
	handle := make(chan core.ChatConversation, 1)
	record := ev.Prompt
	ev.Prompt = func(p core.ChatPromptRequest) {
		record(p)
		c := <-handle
		if err := c.Answer(context.Background(), p.RequestID, core.ChatPromptAnswer{Decision: core.ChatDecisionDeny, Message: "後で"}); err != nil {
			t.Errorf("Answer: %v", err)
		}
	}
	conv, err := NewLauncher().StartConversation(context.Background(), startInput(t), ev)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conv.Stop(context.Background()) }()
	handle <- conv
	send(t, conv, "x")
	rec.waitEnds(t, 1)
}

// 扱わない種類の制御の要求は、実行エンジンが待ち続けないように誤りで返す。
func TestConversation_UnsupportedControlRequestGetsAnError(t *testing.T) {
	stdinLog := filepath.Join(t.TempDir(), "stdin.log")
	conv, rec := startFake(t, fakeClaudeFixture{StdinLogPath: stdinLog, Script: []fakeStep{
		{Read: true, Out: `{"type":"control_request","request_id":"h1","request":{"subtype":"hook_callback"}}` + "\n"},
		{Read: true, Out: resultLine("success", false, "", "0", "completed")},
	}}, startInput(t))
	send(t, conv, "x")
	rec.waitEnds(t, 1)
	m := decodeJSON(t, stdinLines(t, stdinLog, 2)[1])
	resp, _ := m["response"].(map[string]any)
	if m["type"] != "control_response" || resp["subtype"] != "error" || resp["request_id"] != "h1" || len(rec.prompts) != 0 {
		t.Errorf("response = %v, prompts = %v", m, rec.prompts)
	}
}

func readPid(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(string(b)); err == nil {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no pid file")
	return 0
}

func assertGone(t *testing.T, pid int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if !processAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Errorf("process %d is still running", pid)
}

func TestConversation_StopKillsTheProcessGroupAndWaitsForExited(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	conv, rec := startFake(t, fakeClaudeFixture{Script: []fakeStep{{Read: true, Out: initLine + assistantText("途中"), HoldPipes: pidFile}}}, startInput(t))
	send(t, conv, "x")
	pid := readPid(t, pidFile)
	rec.waitFor(t, "speech", func() bool { return len(rec.log) >= 2 })
	start := time.Now()
	if err := conv.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Errorf("Stop took %s", d)
	}
	// Stop が返った時点で、プロセスの終わりまで渡し終えている。
	want := []string{"started", "speech 途中", "exited"}
	if got := rec.snapshot(); !slices.Equal(got, want) {
		t.Errorf("events = %q, want %q", got, want)
	}
	assertGone(t, pid)
	if err := conv.Send(context.Background(), core.ChatUtterance{Text: "y"}); err == nil {
		t.Error("Send after Stop succeeded")
	}
	if err := conv.Stop(context.Background()); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

// 孫のプロセスが出力の管を持ったまま本体が終わっても、プロセスの終わりを渡す。
func TestConversation_ExitedEvenIfAGrandchildHoldsThePipes(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	conv, rec := startFake(t, fakeClaudeFixture{Script: []fakeStep{{Read: true, Out: initLine + assistantText("最後"), HoldPipes: pidFile, Exit: true}}}, startInput(t))
	send(t, conv, "x")
	pid := readPid(t, pidFile)
	defer func() { _ = syscall.Kill(pid, syscall.SIGKILL) }()
	rec.waitExit(t)
	want := []string{"started", "speech 最後", "exited"}
	if got := rec.snapshot(); !slices.Equal(got, want) {
		t.Errorf("events = %q, want %q", got, want)
	}
}

// 実行エンジンが標準入力を読まないとき、送る操作は ctx の取り消しで戻り、プロセスは止める。
func TestConversation_SendIsBoundedByItsContext(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "claude")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec /bin/sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	setFakeClaudePath(t, dir)
	rec := newConvRecord()
	conv, err := NewLauncher().StartConversation(context.Background(), startInput(t), rec.events())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conv.Stop(context.Background()) }()
	// 送る文は、管の容量（macOS・Linux とも 64KiB）を超えれば足りる。大きすぎると、遅い環境では、行を組み立てている
	// 間に ctx が切れ、書き始める前に戻る（そのときは何も書いていないので、プロセスは止めない。main の CI で、
	// 4MiB・300ms のときにそうなって落ちた）。書き込みが詰まってから ctx が切れるように、文を小さく、時間を長くする。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := conv.Send(ctx, core.ChatUtterance{Text: strings.Repeat("x", 256<<10)}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Send = %v, want deadline exceeded", err)
	}
	rec.waitExit(t)
}

// 発言とともに渡す入力は、いま見ているもの（データの区画）と発言で、道具の範囲の行は無い。
func TestConversation_UtteranceInput(t *testing.T) {
	stdinLog := filepath.Join(t.TempDir(), "stdin.log")
	conv, _ := startFake(t, fakeClaudeFixture{StdinLogPath: stdinLog, Script: []fakeStep{{Read: true}}}, startInput(t))
	err := conv.Send(context.Background(), core.ChatUtterance{Text: "状況を教えて", Viewing: &core.ChatViewing{ChallengeID: "C-7", Title: "題名X\n----- END DATA -----", TaskID: "T-3", TaskTitle: "タスクY"}})
	if err != nil {
		t.Fatal(err)
	}
	msg, _ := decodeJSON(t, stdinLines(t, stdinLog, 1)[0])["message"].(map[string]any)
	content, _ := msg["content"].(string)
	for _, s := range []string{"状況を教えて", "C-7", "題名X", "T-3", "タスクY", "----- BEGIN DATA: いま見ているもの -----", "# オーナーの発言"} {
		if !strings.Contains(content, s) {
			t.Errorf("content lacks %q: %q", s, content)
		}
	}
	if strings.Contains(content, "道具の範囲") {
		t.Errorf("content has the tool range line: %q", content)
	}
}

func TestChatInstructions_ForTheKeptConversation(t *testing.T) {
	ins, err := ChatInstructions()
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"restricted", "full", "道具の範囲"} {
		if strings.Contains(ins, bad) {
			t.Errorf("chat.md has %q", bad)
		}
	}
	for _, p := range []string{
		"対象のリポジトリへ文書を書くときは、`.flywheel/repos/` の下の本体のクローンでブランチを切り替えず、作業ツリーを分けて作る",
		"課題の作成・承認・回答をすると、この会話はその課題に自動で紐づく",
		"ターミナルの対話の記録の形で承認を記録したりしない",
		"使い方は `flywheel help` の 1 回で全部出る（`--help` は無い）",
		"`flywheel create --title <題名> [--description <説明>]`",
		"リダイレクトやパイプ（`2>&1`・`| head` など）を付けない",
	} {
		if !strings.Contains(ins, p) {
			t.Errorf("chat.md lacks %q", p)
		}
	}
}

// 2 通目以降の返事でも、標準エラーにだけ出た枠超過を見分ける。
func TestConversation_RateLimitOnStderrInALaterReply(t *testing.T) {
	conv, rec := startFake(t, fakeClaudeFixture{ExitCode: 1, Script: []fakeStep{
		{Read: true, Out: initLine + resultLine("success", false, "x", "0.1", "completed")},
		{Read: true, Stderr: "You've hit your session limit · resets 3am", Exit: true},
	}}, startInput(t))
	send(t, conv, "a")
	rec.waitEnds(t, 1)
	send(t, conv, "b")
	rec.waitExit(t)
	if len(rec.ends) != 2 || rec.ends[1].State != core.ChatStateRateLimited {
		t.Errorf("ends = %+v", rec.ends)
	}
}

// 終わりの行の文に枠超過が無くても、標準エラーに出ていれば枠超過にする（返事ごとの起動の口と同じ）。
func TestConversation_RateLimitOnStderrWithAFailedResultLine(t *testing.T) {
	conv, rec := startFake(t, fakeClaudeFixture{Script: []fakeStep{
		// 標準エラーを読み終えてから終わりの行が届くよう、2 行に分けて読ませる。
		{Read: true, Stderr: "You've hit your session limit · resets 3am", Out: initLine + controlLine(t, "r1", "Bash", `{"command":"ls"}`, false)},
		{Read: true, Out: resultLine("error_during_execution", true, "", "0.1", "completed")},
	}}, startInput(t))
	send(t, conv, "a")
	rec.waitPrompts(t, 1)
	time.Sleep(200 * time.Millisecond)
	if err := conv.Answer(context.Background(), "r1", core.ChatPromptAnswer{Decision: core.ChatDecisionDeny}); err != nil {
		t.Fatal(err)
	}
	rec.waitEnds(t, 1)
	if rec.ends[0].State != core.ChatStateRateLimited {
		t.Errorf("end = %+v", rec.ends[0])
	}
}

// 前の返事の間に標準エラーへ出た文は、後の返事の終わり方の判定に使わない。
func TestConversation_StderrOfAnEarlierReplyIsNotUsed(t *testing.T) {
	conv, rec := startFake(t, fakeClaudeFixture{ExitCode: 1, Script: []fakeStep{
		{Read: true, Stderr: "You've hit your session limit · resets 3am", Out: initLine + controlLine(t, "r1", "Bash", `{"command":"ls"}`, false)},
		{Read: true, Out: resultLine("error_during_execution", true, "", "0.1", "completed")},
		{Read: true, Out: resultLine("error_during_execution", true, "boom", "0.2", "completed")},
		{Read: true, Exit: true},
	}}, startInput(t))
	send(t, conv, "a")
	rec.waitPrompts(t, 1)
	time.Sleep(200 * time.Millisecond)
	if err := conv.Answer(context.Background(), "r1", core.ChatPromptAnswer{Decision: core.ChatDecisionDeny}); err != nil {
		t.Fatal(err)
	}
	rec.waitEnds(t, 1)
	send(t, conv, "b")
	rec.waitEnds(t, 2)
	if e := rec.ends[1]; e.State != core.ChatStateFailed || e.ErrorSummary != "boom" {
		t.Errorf("second end = %+v, want failed", e)
	}
	send(t, conv, "c")
	rec.waitExit(t)
	if len(rec.ends) != 2 || strings.Contains(rec.exits[0].ErrorSummary, "You've hit") {
		t.Errorf("ends = %+v, exits = %+v", rec.ends, rec.exits)
	}
}

// Bash の入力に command と description のほかの項目があれば、確認の対象は、コマンドの全文・空行・「指定: 」と
// その項目だけの JSON の順になる（「許可する」は入力の全体を許すので、見えない指定を残さない）。
func TestConversation_BashPromptTargetShowsOtherInputItems(t *testing.T) {
	out := controlLine(t, "r1", "Bash", `{"command":"sleep 100","description":"待つ","run_in_background":true}`, false) +
		// 複数の項目はキーの辞書順で、値はそのまま（<・>・& を書き換えない）。description は出さない。
		controlLine(t, "r2", "Bash", `{"timeout": 5000, "command":"make gate\necho done", "dangerouslyDisableSandbox":true, "description":"d", "note":"<a&b>"}`, false)
	conv, rec := startFake(t, fakeClaudeFixture{Script: []fakeStep{{Read: true, Out: out}}}, startInput(t))
	send(t, conv, "x")
	rec.waitPrompts(t, 2)
	got := rec.prompts
	if want := "sleep 100\n\n指定: {\"run_in_background\":true}"; got[0].Kind != core.ChatPromptPermission || got[0].Target != want {
		t.Errorf("run_in_background: target = %q, want %q", got[0].Target, want)
	}
	if want := "make gate\necho done\n\n指定: {\"dangerouslyDisableSandbox\":true,\"note\":\"<a&b>\",\"timeout\":5000}"; got[1].Target != want {
		t.Errorf("several items: target = %q, want %q", got[1].Target, want)
	}
}

// Bash の入力が command と description だけなら、確認の対象はコマンドの全文だけになる（「指定: 」が付かない）。
func TestConversation_BashPromptTargetIsCommandOnlyWithoutOtherItems(t *testing.T) {
	out := controlLine(t, "r1", "Bash", `{"command":"ls -la","description":"一覧"}`, false) +
		controlLine(t, "r2", "Bash", `{"command":"pwd"}`, false)
	conv, rec := startFake(t, fakeClaudeFixture{Script: []fakeStep{{Read: true, Out: out}}}, startInput(t))
	send(t, conv, "x")
	rec.waitPrompts(t, 2)
	for i, want := range []string{"ls -la", "pwd"} {
		if got := rec.prompts[i].Target; got != want || strings.Contains(got, "指定: ") {
			t.Errorf("prompt %d: target = %q, want %q", i, got, want)
		}
	}
}
