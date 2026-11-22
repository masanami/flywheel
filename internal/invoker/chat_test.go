package invoker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

const chatSession = "22222222-2222-2222-2222-222222222222"

func chatInput(t *testing.T) core.ChatLaunchInput {
	t.Helper()
	return core.ChatLaunchInput{
		Text:       "状況を教えて",
		SessionID:  chatSession,
		WorkDir:    t.TempDir(),
		Tools:      core.ChatToolsRestricted,
		MaxTurns:   30,
		TimeoutSec: 20,
	}
}

type chatRecord struct {
	started  int
	speeches []string
	commands []core.ChatCommand
	order    []string
}

func (r *chatRecord) events() core.ChatEvents {
	return core.ChatEvents{
		Started: func() { r.started++; r.order = append(r.order, "started") },
		Speech:  func(s string) { r.speeches = append(r.speeches, s); r.order = append(r.order, "speech") },
		Command: func(c core.ChatCommand) { r.commands = append(r.commands, c); r.order = append(r.order, "command") },
	}
}

// fakeChat は偽の claude を PATH に置き、fx を渡して InvokeChat を 1 回呼ぶ。
func fakeChat(t *testing.T, fx fakeClaudeFixture, in core.ChatLaunchInput) (core.ChatLaunchOutput, *chatRecord, error) {
	t.Helper()
	setFakeClaudePath(t, newFakeClaudeDir(t))
	t.Setenv(envFixture, writeFixture(t, fx))
	rec := &chatRecord{}
	out, err := NewLauncher().InvokeChat(context.Background(), in, rec.events())
	return out, rec, err
}

func resultLine(subtype string, isErr bool, result string, cost string, terminal string) string {
	return `{"type":"result","subtype":"` + subtype + `","is_error":` + strconv.FormatBool(isErr) +
		`,"result":` + strconv.Quote(result) + `,"terminal_reason":"` + terminal + `","total_cost_usd":` + cost + `}` + "\n"
}

func assistantText(text string) string {
	b, _ := json.Marshal(text)
	return `{"type":"assistant","message":{"content":[{"type":"text","text":` + string(b) + `}]}}` + "\n"
}

const initLine = `{"type":"system","subtype":"init","session_id":"s"}` + "\n"

func testdataAbs(t *testing.T, name string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// 実物の写しを読み、会話の始まり・発言・コマンドの成否・費用を順に渡す。
func TestInvokeChat_ReplaysTheRealStream(t *testing.T) {
	out, rec, err := fakeChat(t, fakeClaudeFixture{StdoutFile: testdataAbs(t, "chat_stream_success.jsonl")}, chatInput(t))
	if err != nil {
		t.Fatal(err)
	}
	if out.State != core.ChatStateDone || out.CostUSD != 0.3389226 {
		t.Errorf("out = %+v, want done with cost 0.3389226", out)
	}
	if rec.started != 1 {
		t.Errorf("started = %d, want 1", rec.started)
	}
	if len(rec.speeches) != 1 || !strings.Contains(rec.speeches[0], "終了コード127") {
		t.Errorf("speeches = %q", rec.speeches)
	}
	want := []core.ChatCommand{{Command: "echo hello", OK: true}, {Command: "nosuchcmd_xyz", OK: false}}
	if !slices.Equal(rec.commands, want) {
		t.Errorf("commands = %+v, want %+v", rec.commands, want)
	}
	if got := strings.Join(rec.order, ","); got != "started,command,command,speech" {
		t.Errorf("order = %s", got)
	}
}

func TestInvokeChat_TwoSpeechesAreDeliveredOneByOne(t *testing.T) {
	stdout := initLine + assistantText("一つ目") + assistantText("二つ目") + resultLine("success", false, "二つ目", "0.12", "completed")
	out, rec, err := fakeChat(t, fakeClaudeFixture{Stdout: stdout}, chatInput(t))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rec.speeches, []string{"一つ目", "二つ目"}) || out.State != core.ChatStateDone || out.CostUSD != 0.12 {
		t.Errorf("speeches = %q, out = %+v", rec.speeches, out)
	}
}

// 続きの会話の先頭にも会話の始まりの行が出る。
func TestInvokeChat_ResumeStreamReplay(t *testing.T) {
	in := chatInput(t)
	in.Resume = true
	out, rec, err := fakeChat(t, fakeClaudeFixture{StdoutFile: testdataAbs(t, "chat_stream_resume.jsonl")}, in)
	if err != nil || out.State != core.ChatStateDone || rec.started != 1 || len(rec.speeches) != 1 {
		t.Errorf("out = %+v, err = %v, rec = %+v", out, err, rec)
	}
}

func readArgv(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var argv []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(b))), &argv); err != nil {
		t.Fatal(err)
	}
	return argv
}

// argsAfter は flag の後ろから、次の "--" で始まる引数の手前までを返す。
func argsAfter(argv []string, flag string) []string {
	i := slices.Index(argv, flag)
	if i < 0 {
		return nil
	}
	var out []string
	for _, a := range argv[i+1:] {
		if strings.HasPrefix(a, "--") {
			break
		}
		out = append(out, a)
	}
	return out
}

func runArgv(t *testing.T, in core.ChatLaunchInput) ([]string, string) {
	t.Helper()
	argvLog := filepath.Join(t.TempDir(), "argv.log")
	stdinLog := filepath.Join(t.TempDir(), "stdin.log")
	_, _, err := fakeChat(t, fakeClaudeFixture{Stdout: initLine + resultLine("success", false, "x", "0", "completed"), ArgvLogPath: argvLog, StdinLogPath: stdinLog}, in)
	if err != nil {
		t.Fatal(err)
	}
	stdin, _ := os.ReadFile(stdinLog)
	return readArgv(t, argvLog), string(stdin)
}

// AC-75〜78・97: restricted の道具の範囲とターン数の上限。
func TestInvokeChat_RestrictedArgs(t *testing.T) {
	in := chatInput(t)
	in.MaxTurns = 2
	argv, _ := runArgv(t, in)
	if got := argsAfter(argv, "--allowedTools"); !slices.Equal(got, []string{"Read", "Grep", "Glob", "Bash(flywheel:*)"}) {
		t.Errorf("--allowedTools = %q", got)
	}
	if got := argsAfter(argv, "--disallowedTools"); !slices.Equal(got, []string{"Edit", "Write", "NotebookEdit", "WebFetch", "WebSearch", "Task"}) {
		t.Errorf("--disallowedTools = %q", got)
	}
	if got := argsAfter(argv, "--permission-mode"); !slices.Equal(got, []string{"default"}) {
		t.Errorf("--permission-mode = %q", got)
	}
	for _, bad := range []string{"--dangerously-skip-permissions", "bypassPermissions"} {
		if slices.Contains(argv, bad) {
			t.Errorf("argv has %s", bad)
		}
	}
	if got := argsAfter(argv, "--max-turns"); !slices.Equal(got, []string{"2"}) {
		t.Errorf("--max-turns = %q, want 2", got)
	}
	if got := argsAfter(argv, "--output-format"); !slices.Equal(got, []string{"stream-json"}) || !slices.Contains(argv, "--verbose") {
		t.Errorf("output format = %q, verbose = %v", got, slices.Contains(argv, "--verbose"))
	}
	if slices.Contains(argv, "--include-partial-messages") {
		t.Error("argv has --include-partial-messages")
	}
	if got := argsAfter(argv, "--session-id"); !slices.Equal(got, []string{chatSession}) || slices.Contains(argv, "--resume") {
		t.Errorf("new conversation: --session-id = %q, argv = %q", got, argv)
	}
}

// AC-66・67: full は道具の指定が無く、許可の扱いはすべて許す。
func TestInvokeChat_FullArgs(t *testing.T) {
	in := chatInput(t)
	in.Tools = core.ChatToolsFull
	argv, stdin := runArgv(t, in)
	for _, f := range []string{"--allowedTools", "--disallowedTools"} {
		if slices.Contains(argv, f) {
			t.Errorf("argv has %s", f)
		}
	}
	if got := argsAfter(argv, "--permission-mode"); !slices.Equal(got, []string{"bypassPermissions"}) {
		t.Errorf("--permission-mode = %q", got)
	}
	if !strings.Contains(stdin, "道具の範囲: full") {
		t.Errorf("stdin = %q, want the tools range", stdin)
	}
}

// 2 通目からは --resume。
func TestInvokeChat_ResumeUsesResumeFlag(t *testing.T) {
	in := chatInput(t)
	in.Resume = true
	argv, _ := runArgv(t, in)
	if got := argsAfter(argv, "--resume"); !slices.Equal(got, []string{chatSession}) || slices.Contains(argv, "--session-id") {
		t.Errorf("argv = %q", argv)
	}
}

// AC-143: 指示文が追加の指示に入る。発言・いま見ているものは標準入力で渡す。
func TestInvokeChat_InstructionsAndStdin(t *testing.T) {
	in := chatInput(t)
	in.Viewing = &core.ChatViewing{ChallengeID: "C-7", Title: "題名X", TaskID: "T-3", TaskTitle: "タスクY"}
	argv, stdin := runArgv(t, in)
	want, err := ChatInstructions()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.Index(argv, "--append-system-prompt")
	if i < 0 || i+1 >= len(argv) || argv[i+1] != want {
		t.Errorf("--append-system-prompt does not carry chat.md: %q", argv)
	}
	for _, s := range []string{"状況を教えて", "C-7", "題名X", "T-3", "タスクY", "道具の範囲: restricted"} {
		if !strings.Contains(stdin, s) {
			t.Errorf("stdin lacks %q:\n%s", s, stdin)
		}
	}
	begin := strings.Index(stdin, "----- BEGIN DATA: いま見ているもの -----")
	end := strings.Index(stdin, dataSectionEnd)
	for _, s := range []string{"C-7", "題名X", "T-3", "タスクY"} {
		if i := strings.Index(stdin, s); begin < 0 || end < begin || i < begin || i > end {
			t.Errorf("%q is not inside the data section:\n%s", s, stdin)
		}
	}
	if i := strings.Index(stdin, "状況を教えて"); i < end {
		t.Error("the owner's text must come after the data section")
	}
	for _, a := range argv {
		if strings.Contains(a, "状況を教えて") || strings.Contains(a, "題名X") {
			t.Errorf("argv leaks the owner's text: %q", a)
		}
	}
}

// AC-79・82・83・65・80: 作業ディレクトリと環境。
func TestInvokeChat_EnvAndWorkDir(t *testing.T) {
	in := chatInput(t)
	shimDir := t.TempDir()
	in.Env = append(core.ChatMarkEnv(chatSession, 42, core.ChatToolsRestricted), "PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FLYWHEEL_DELEGATED_RUN", "R-9")
	t.Setenv("INHERITED_MARK", "1")
	envDump := filepath.Join(t.TempDir(), "env.txt")
	cwdLog := filepath.Join(t.TempDir(), "cwd.txt")
	if _, _, err := fakeChat(t, fakeClaudeFixture{Stdout: initLine + resultLine("success", false, "x", "0", "completed"), EnvDumpPath: envDump, CwdLogPath: cwdLog}, in); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(envDump)
	env := strings.Split(string(b), "\n")
	for _, w := range []string{
		core.ChatEnvSession + "=" + chatSession,
		core.ChatEnvMessage + "=42",
		core.ChatEnvTools + "=restricted",
		"INHERITED_MARK=1",
	} {
		if !slices.Contains(env, w) {
			t.Errorf("env lacks %q", w)
		}
	}
	pathCount := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			pathCount++
			if !strings.HasPrefix(kv, "PATH="+shimDir+string(os.PathListSeparator)) {
				t.Errorf("PATH = %q, want it to start with the shim dir", kv)
			}
		}
		if strings.HasPrefix(kv, "FLYWHEEL_DELEGATED_RUN=") {
			t.Errorf("env carries the delegation mark: %q", kv)
		}
	}
	if pathCount != 1 {
		t.Errorf("PATH appears %d times", pathCount)
	}
	wd, _ := os.ReadFile(cwdLog)
	if got, want := mustEval(t, string(wd)), mustEval(t, in.WorkDir); got != want {
		t.Errorf("cwd = %q, want %q", got, want)
	}
}

// AC-101: ターン数の上限で終わった返事は limit。
func TestInvokeChat_MaxTurnsIsLimit(t *testing.T) {
	stdout := initLine + assistantText("途中") + resultLine("error_max_turns", true, "", "0.2", "max_turns")
	out, rec, err := fakeChat(t, fakeClaudeFixture{Stdout: stdout, ExitCode: 1}, chatInput(t))
	if err != nil || out.State != core.ChatStateLimit || out.CostUSD != 0.2 || len(rec.speeches) != 1 {
		t.Errorf("out = %+v, err = %v, speeches = %q", out, err, rec.speeches)
	}
}

// AC-60: 続きの会話が見つからないときは session_lost。
func TestInvokeChat_SessionLost(t *testing.T) {
	in := chatInput(t)
	in.Resume = true
	out, _, err := fakeChat(t, fakeClaudeFixture{Stderr: "No conversation found with session ID: " + chatSession + "\n", ExitCode: 1}, in)
	if err != nil || out.State != core.ChatStateSessionLost {
		t.Errorf("out = %+v, err = %v", out, err)
	}
	// 新しい会話の失敗は session_lost にしない。
	in.Resume = false
	out, _, _ = fakeChat(t, fakeClaudeFixture{Stderr: "No conversation found\n", ExitCode: 1}, in)
	if out.State != core.ChatStateFailed {
		t.Errorf("new conversation: State = %s, want failed", out.State)
	}
}

// AC-164: 利用上限は rate_limited。判定の規則は core.IsRateLimited。
func TestInvokeChat_RateLimited(t *testing.T) {
	stdout := initLine + resultLine("success", true, testRateLimitPrefix+"limit · resets 5pm", "0", "completed")
	out, _, err := fakeChat(t, fakeClaudeFixture{Stdout: stdout, ExitCode: 1}, chatInput(t))
	if err != nil || out.State != core.ChatStateRateLimited {
		t.Errorf("out = %+v, err = %v", out, err)
	}
	// 通常の返事が同じ文で始まっても、失敗でなければ done のまま。
	stdout = initLine + assistantText(testRateLimitPrefix+"x") + resultLine("success", false, testRateLimitPrefix+"x", "0", "completed")
	out, _, _ = fakeChat(t, fakeClaudeFixture{Stdout: stdout}, chatInput(t))
	if out.State != core.ChatStateDone {
		t.Errorf("State = %s, want done", out.State)
	}
}

// 0 でない終了は failed。出し終えていた発言は渡し済み。
func TestInvokeChat_NonZeroExitIsFailed(t *testing.T) {
	out, rec, err := fakeChat(t, fakeClaudeFixture{Stdout: initLine + assistantText("途中まで"), Stderr: "boom", ExitCode: 3}, chatInput(t))
	if err != nil || out.State != core.ChatStateFailed || out.ErrorSummary != "boom" {
		t.Errorf("out = %+v, err = %v", out, err)
	}
	if !slices.Equal(rec.speeches, []string{"途中まで"}) {
		t.Errorf("speeches = %q", rec.speeches)
	}
}

// 終わりの行が無いまま 0 で終わった出力は、読めた返事として扱わない。
func TestInvokeChat_NoResultLineIsFailed(t *testing.T) {
	out, _, err := fakeChat(t, fakeClaudeFixture{Stdout: initLine}, chatInput(t))
	if err != nil || out.State != core.ChatStateFailed {
		t.Errorf("out = %+v, err = %v", out, err)
	}
}

func TestInvokeChat_ClaudeNotFound(t *testing.T) {
	setFakeClaudePath(t, t.TempDir())
	_, err := NewLauncher().InvokeChat(context.Background(), chatInput(t), core.ChatEvents{})
	if !errors.Is(err, ErrClaudeNotFound) {
		t.Errorf("err = %v, want ErrClaudeNotFound", err)
	}
}

// AC-99・100: 時間の上限を過ぎたら止めて limit。
func TestInvokeChat_TimeoutIsLimit(t *testing.T) {
	in := chatInput(t)
	in.TimeoutSec = 1
	start := time.Now()
	out, _, err := fakeChat(t, fakeClaudeFixture{SleepSeconds: 30}, in)
	if err != nil || out.State != core.ChatStateLimit || out.CostUSD != 0 {
		t.Errorf("out = %+v, err = %v", out, err)
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Errorf("took %s, the engine was not stopped", d)
	}
}

// AC-32: ctx の取り消しで止め、実行エンジンが起動した子のプロセスもまとめて止める。
func TestInvokeChat_CancelStopsTheProcessGroup(t *testing.T) {
	setFakeClaudePath(t, newFakeClaudeDir(t))
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv(envFixture, writeFixture(t, fakeClaudeFixture{SleepSeconds: 30, ChildPidPath: pidFile}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		out core.ChatLaunchOutput
		err error
	}
	done := make(chan result, 1)
	in := chatInput(t)
	go func() {
		out, err := NewLauncher().InvokeChat(ctx, in, core.ChatEvents{})
		done <- result{out, err}
	}()
	var childPid int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && childPid == 0 {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(string(b)); err == nil {
				childPid = pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if childPid == 0 {
		t.Fatal("the fake engine did not start its child")
	}
	cancel()
	select {
	case r := <-done:
		if r.err != nil || r.out.State != core.ChatStateStopped || r.out.CostUSD != 0 {
			t.Errorf("out = %+v, err = %v, want stopped", r.out, r.err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("InvokeChat did not return after cancel")
	}
	// 孫は init に引き取られるまでゾンビで残りうるので、動いていないことを ps の状態で確かめる。
	gone := false
	for i := 0; i < 100 && !gone; i++ {
		gone = !processAlive(childPid)
		if !gone {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !gone {
		_ = syscall.Kill(childPid, syscall.SIGKILL)
		t.Errorf("the child process %d is still running", childPid)
	}
}

// processAlive は pid が動いているか（ゾンビでないか）を ps で確かめる。
func processAlive(pid int) bool {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	s := strings.TrimSpace(string(out))
	return s != "" && !strings.HasPrefix(s, "Z")
}

// AC-143〜149: 指示文に決まった語句が入っている。
func TestChatInstructions_ContainRequiredPhrases(t *testing.T) {
	ins, err := ChatInstructions()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"オーナーの指示が無い承認を記録しない",
		"記録する前に、何が承認されるかの要約を示す",
		"記録した後に、何を記録したかを伝える",
		"ID を単独で出さない",
		"`answer --replan`",
		"課題にして委譲先に任せる",
	} {
		if !strings.Contains(ins, p) {
			t.Errorf("chat.md lacks %q", p)
		}
	}
}

// AC-151: chat.md は分量の上限の検査の対象で、実物は上限内。
func TestChatPrompt_IsInTheSizeCheck(t *testing.T) {
	if judgmentPromptLimit["chat.md"] <= 0 {
		t.Fatal("chat.md is not in the size limit table")
	}
	fsys, err := promptsSubFS()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckPromptSizes(fsys); err != nil {
		t.Errorf("CheckPromptSizes(embedded) = %v", err)
	}
}

// 実物の写し: 続きの会話が無いときは、終わりの行と標準エラーの両方に文言が出る。
func TestInvokeChat_SessionLostRealCapture(t *testing.T) {
	in := chatInput(t)
	in.Resume = true
	stderr, err := os.ReadFile(testdataAbs(t, "chat_stream_session_lost.stderr.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// 標準エラーなし（終わりの行だけ）でも、標準出力なし（標準エラーだけ）でも見分けられる。
	out, _, err := fakeChat(t, fakeClaudeFixture{StdoutFile: testdataAbs(t, "chat_stream_session_lost.jsonl"), ExitCode: 1}, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != core.ChatStateSessionLost {
		t.Errorf("result line only: State = %s", out.State)
	}
	out, _, err = fakeChat(t, fakeClaudeFixture{Stderr: string(stderr), ExitCode: 1}, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != core.ChatStateSessionLost {
		t.Errorf("stderr only: State = %s", out.State)
	}
}

// 子のエージェントの発言・コマンドは、オーケストレーターのものとして渡さない。
func TestInvokeChat_SubagentLinesAreSkipped(t *testing.T) {
	sub := `{"type":"assistant","parent_tool_use_id":"toolu_1","message":{"content":[{"type":"text","text":"子の発言"},{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"ls"}}]}}` + "\n" +
		`{"type":"user","parent_tool_use_id":"toolu_1","message":{"content":[{"type":"tool_result","tool_use_id":"t2","is_error":false}]}}` + "\n"
	out, rec, err := fakeChat(t, fakeClaudeFixture{Stdout: initLine + sub + assistantText("親の発言") + resultLine("success", false, "親の発言", "0", "completed")}, chatInput(t))
	if err != nil || out.State != core.ChatStateDone {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	if !slices.Equal(rec.speeches, []string{"親の発言"}) || len(rec.commands) != 0 {
		t.Errorf("speeches = %q, commands = %+v", rec.speeches, rec.commands)
	}
}

// 終わりの行が費用を返していれば、止めた返事でもその費用を残す（読めていなければ 0）。
func TestReportedCost(t *testing.T) {
	cost := 0.5
	res := chatStreamResult{hasResult: true}
	res.last.TotalCostUSD = &cost
	if got := reportedCost(res); got != 0.5 {
		t.Errorf("reportedCost = %v, want 0.5", got)
	}
	if got := reportedCost(chatStreamResult{}); got != 0 {
		t.Errorf("reportedCost(no result) = %v, want 0", got)
	}
}

// 費用を返した後に居座って時間切れになった返事は、limit のまま返った費用を残す。
func TestInvokeChat_TimeoutAfterResultKeepsTheCost(t *testing.T) {
	in := chatInput(t)
	in.TimeoutSec = 1
	stdout := initLine + resultLine("success", false, "x", "0.5", "completed")
	out, _, err := fakeChat(t, fakeClaudeFixture{Stdout: stdout, SleepSeconds: 30, PrintBeforeSleep: true}, in)
	if err != nil || out.State != core.ChatStateLimit || out.CostUSD != 0.5 {
		t.Errorf("out = %+v, err = %v, want limit with cost 0.5", out, err)
	}
}
