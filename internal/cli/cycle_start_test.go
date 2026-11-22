package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
	"github.com/masanami/flywheel/internal/server"
	"github.com/masanami/flywheel/internal/view"
)

// 区切り S4b: 操作が成立した後にサイクルを始める（AC-74〜111・AC-117・AC-118）。偽の「始める処理」を渡し、呼ばれた回数と
// 渡されたきっかけを確かめる。

type fakeStarter struct {
	mu       sync.Mutex
	calls    []string
	launch   server.CycleLaunch
	err      error
	atCallFn func()
}

func (f *fakeStarter) start(_ string, trigger string) (server.CycleLaunch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, trigger)
	if f.atCallFn != nil {
		f.atCallFn()
	}
	return f.launch, f.err
}

func runWithStarter(t *testing.T, st *fakeStarter, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = runWithDeps(args, strings.NewReader(""), &out, &errb, defaultCommands(), runDeps{LookPath: lookPathFor(t), CycleStart: st.start})
	return code, out.String(), errb.String()
}

// startWorkspace は、いま始められるもの（未分類の課題）と、課題へ上げられるフォローアップ F-1 を持つワークスペース。
func startWorkspace(t *testing.T) string {
	t.Helper()
	ws := setupWorkspaceWithPosition(t)
	createTitled(t, ws, "work")
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{Title: "題名 1", Detail: "説明 1"})
	return ws
}

func decodeCycle(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout)
	}
	c, ok := doc["cycle"].(map[string]any)
	if !ok {
		t.Fatalf("no cycle key in %v", doc)
	}
	return c
}

const promoteArgs = "followup promote F-1 --relayed --session S --quote Q"

// AC-74〜77・AC-100・AC-103: 成立した操作の後、始める処理が 1 回呼ばれ（きっかけは操作の名前）、cycle は started/started。
func TestStartsCycle_FollowupPromoteStartsOnceWithItsTrigger(t *testing.T) {
	parallelTools(t, false)
	ws := startWorkspace(t)
	st := &fakeStarter{launch: server.CycleLaunch{Started: true}}

	code, stdout, stderr := runWithStarter(t, st, append(strings.Fields(promoteArgs), "--workspace", ws, "--json")...)

	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	if len(st.calls) != 1 || st.calls[0] != "followup_promote" {
		t.Fatalf("starter calls = %v, want [followup_promote]", st.calls)
	}
	c := decodeCycle(t, stdout)
	if c["started"] != true || c["reason"] != "started" {
		t.Errorf("cycle = %v", c)
	}
}

// AC-107: テキストの出力に「サイクルを始めました。」の行がある。
func TestStartsCycle_TextLineWhenStarted(t *testing.T) {
	parallelTools(t, false)
	ws := startWorkspace(t)
	st := &fakeStarter{launch: server.CycleLaunch{Started: true}}
	var out, errb bytes.Buffer
	code := runWithDeps(append(strings.Fields(promoteArgs), "--workspace", ws), strings.NewReader(""), &out, &errb, defaultCommands(), runDeps{LookPath: lookPathFor(t), CycleStart: st.start})
	if code != 0 || !strings.Contains(out.String(), "\nサイクルを始めました。\n") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errb.String())
	}
}

// AC-84・AC-85・AC-110: --no-cycle → 呼ばれない・disabled。
func TestStartsCycle_NoCycleFlagDoesNotStart(t *testing.T) {
	parallelTools(t, false)
	ws := startWorkspace(t)
	st := &fakeStarter{launch: server.CycleLaunch{Started: true}}
	code, stdout, stderr := runWithStarter(t, st, append(strings.Fields(promoteArgs), "--no-cycle", "--workspace", ws, "--json")...)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	if len(st.calls) != 0 {
		t.Fatalf("starter calls = %v, want none", st.calls)
	}
	if c := decodeCycle(t, stdout); c["started"] != false || c["reason"] != "disabled" {
		t.Errorf("cycle = %v", c)
	}
}

// AC-86・AC-87: 宣言の auto_cycle が偽 → 呼ばれない・disabled。
func TestStartsCycle_AutoCycleFalseDoesNotStart(t *testing.T) {
	parallelTools(t, false)
	ws := startWorkspace(t)
	writeAgentJSONForTest(t, ws, `{"position_file":"position.md","auto_cycle":false}`)
	st := &fakeStarter{launch: server.CycleLaunch{Started: true}}
	code, stdout, _ := runWithStarter(t, st, append(strings.Fields(promoteArgs), "--workspace", ws, "--json")...)
	if code != 0 || len(st.calls) != 0 {
		t.Fatalf("exit=%d calls=%v", code, st.calls)
	}
	if c := decodeCycle(t, stdout); c["reason"] != "disabled" {
		t.Errorf("cycle = %v", c)
	}
}

// AC-89〜92・AC-111: 始める処理がエラー → 操作は成立したまま（課題が増える）・終了コード 0・unavailable・「サイクルを始められませんでした。」。
func TestStartsCycle_StarterErrorKeepsTheOperationAndExitsZero(t *testing.T) {
	parallelTools(t, false)
	ws := startWorkspace(t)
	before := fuCount(t, ws, `SELECT COUNT(*) FROM challenge`)
	st := &fakeStarter{err: errors.New("claude: executable file not found")}
	var out, errb bytes.Buffer
	code := runWithDeps(append(strings.Fields(promoteArgs), "--workspace", ws), strings.NewReader(""), &out, &errb, defaultCommands(), runDeps{LookPath: lookPathFor(t), CycleStart: st.start})
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stderr=%q)", code, errb.String())
	}
	if len(st.calls) != 1 {
		t.Fatalf("starter calls = %v", st.calls)
	}
	if after := fuCount(t, ws, `SELECT COUNT(*) FROM challenge`); after == before {
		t.Errorf("the promoted challenge was not kept (rows %s)", after)
	}
	if !strings.Contains(out.String(), "サイクルを始められませんでした。") {
		t.Errorf("stdout = %q", out.String())
	}
	// 始める処理が無い（nil）ときも、操作は成立し、unavailable。
	ws2 := startWorkspace(t)
	var out2, errb2 bytes.Buffer
	args2 := append(strings.Fields(promoteArgs), "--workspace", ws2, "--json")
	if code := runWithDeps(args2, strings.NewReader(""), &out2, &errb2, defaultCommands(), runDeps{LookPath: lookPathFor(t)}); code != 0 {
		t.Fatalf("nil starter: exit=%d stderr=%q", code, errb2.String())
	}
	if c := decodeCycle(t, out2.String()); c["reason"] != "unavailable" || c["started"] != false {
		t.Errorf("nil starter: cycle = %v", c)
	}
}

// AC-93: 始めたプロセスが排他で断られた → already_running。AC-108 のテキスト。
func TestStartsCycle_ChildRefusedByTheLockIsAlreadyRunning(t *testing.T) {
	parallelTools(t, false)
	ws := startWorkspace(t)
	st := &fakeStarter{launch: server.CycleLaunch{AlreadyRunning: true}}
	var out, errb bytes.Buffer
	code := runWithDeps(append(strings.Fields(promoteArgs), "--workspace", ws), strings.NewReader(""), &out, &errb, defaultCommands(), runDeps{LookPath: lookPathFor(t), CycleStart: st.start})
	if code != 0 || !strings.Contains(out.String(), "走っているサイクルが、続きを進めます。") {
		t.Fatalf("exit=%d stdout=%q", code, out.String())
	}
}

// AC-95: 操作が成立しなかった（存在しない ID）→ 始める処理は呼ばれず、cycle も出さない。
func TestStartsCycle_FailedOperationDoesNotStart(t *testing.T) {
	parallelTools(t, false)
	ws := startWorkspace(t)
	st := &fakeStarter{launch: server.CycleLaunch{Started: true}}
	code, stdout, _ := runWithStarter(t, st, "followup", "promote", "F-99", "--relayed", "--session", "S", "--quote", "Q", "--workspace", ws, "--json")
	if code == 0 || len(st.calls) != 0 || strings.Contains(stdout, "cycle") {
		t.Fatalf("exit=%d calls=%v stdout=%q", code, st.calls, stdout)
	}
}

// AC-80・AC-81・AC-82・AC-83・AC-108・AC-109: 排他がある・始められるものが無い → 呼ばれない。
func TestStartsCycle_RunningAndNoWorkDoNotCall(t *testing.T) {
	parallelTools(t, false)
	ws := setupWorkspaceWithPosition(t) // 課題を作らない。promote が作る課題は未分類で、始められるものになる。
	coretest.InsertFollowup(t, ws, coretest.FollowupRow{Title: "題名 1"})
	st := &fakeStarter{launch: server.CycleLaunch{Started: true}}
	// 生きている排他。
	s, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginCycle(t.Context(), core.BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	var out, errb bytes.Buffer
	code := runWithDeps(append(strings.Fields(promoteArgs), "--workspace", ws), strings.NewReader(""), &out, &errb, defaultCommands(), runDeps{LookPath: lookPathFor(t), CycleStart: st.start})
	if code != 0 || len(st.calls) != 0 || !strings.Contains(out.String(), "走っているサイクルが、続きを進めます。") {
		t.Fatalf("running: exit=%d calls=%v stdout=%q stderr=%q", code, st.calls, out.String(), errb.String())
	}
}

// AC-104: 引数の無い取り込み（追従だけ）は始めない。取り込み元を指定したものは ingest。
func TestStartsCycle_IngestStartsOnlyWithAnArgument(t *testing.T) {
	t.Parallel()
	cmd := commandByPath(t, "ingest")
	if got := cmd.StartsCycle(Args{}); got != "" {
		t.Errorf("ingest without an argument: trigger = %q, want none", got)
	}
	if got := cmd.StartsCycle(Args{Positional: []string{"o/r#1"}}); got != core.CycleTriggerIngest {
		t.Errorf("ingest <指定>: trigger = %q, want ingest", got)
	}
}

// AC-96〜103: 操作ごとに渡すきっかけ。不可逆操作の承認と差し戻しも approve・reject（同じコマンド）。
func TestStartsCycle_TriggerPerCommand(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]string{
		"approve": "approve", "reject": "reject", "answer": "answer", "budget": "budget",
		"followup promote": "followup_promote", "ingest": "ingest",
	} {
		cmd := commandByPath(t, path)
		if cmd.StartsCycle == nil {
			t.Errorf("%s: StartsCycle is nil", path)
			continue
		}
		if got := cmd.StartsCycle(Args{Positional: []string{"x"}}); got != want {
			t.Errorf("%s: trigger = %q, want %q", path, got, want)
		}
	}
	// ほかのコマンド（取り下げ・読み取り・サイクルそのもの）は始めない。
	for _, path := range []string{"withdraw", "status", "cycle", "cycle status", "cycle stop", "followup drop", "followup done"} {
		if commandByPath(t, path).StartsCycle != nil {
			t.Errorf("%s must not start a cycle", path)
		}
	}
}

func commandByPath(t *testing.T, path string) Command {
	t.Helper()
	c, ok := registeredCommands()[path]
	if !ok {
		t.Fatalf("command %q not registered", path)
	}
	return c
}

// AC-107〜111: 理由ごとのテキスト 1 行。
func TestCycleStartText(t *testing.T) {
	t.Parallel()
	for reason, want := range map[core.CycleStartReason]string{
		core.CycleStartStarted:        "サイクルを始めました。\n",
		core.CycleStartAlreadyRunning: "走っているサイクルが、続きを進めます。\n",
		core.CycleStartNoWork:         "いま進められるものが無いので、サイクルは始めていません。\n",
		core.CycleStartDisabled:       "サイクルを自動で始めない設定です。\n",
		core.CycleStartUnavailable:    "サイクルを始められませんでした。`flywheel cycle` で始められます。\n",
	} {
		if got := cycleStartText(viewCycle(reason)); got != want {
			t.Errorf("%s: %q, want %q", reason, got, want)
		}
	}
}

// AC-117: --detach は始める処理を 1 回呼んですぐ戻る（既定の trigger は manual。--trigger を渡せる）。
func TestCycleDetach_CallsTheStarterAndReturns(t *testing.T) {
	parallelTools(t, false)
	ws := startWorkspace(t)
	st := &fakeStarter{launch: server.CycleLaunch{Started: true}}
	code, stdout, stderr := runWithStarter(t, st, "cycle", "--detach", "--workspace", ws, "--json")
	if code != 0 || len(st.calls) != 1 || st.calls[0] != "manual" {
		t.Fatalf("exit=%d calls=%v stderr=%s", code, st.calls, stderr)
	}
	if c := decodeCycle(t, stdout); c["started"] != true || c["reason"] != "started" {
		t.Errorf("cycle = %v", c)
	}
	st2 := &fakeStarter{launch: server.CycleLaunch{Started: true}}
	if code, _, _ := runWithStarter(t, st2, "cycle", "--detach", "--trigger", "ui", "--workspace", ws, "--json"); code != 0 || len(st2.calls) != 1 || st2.calls[0] != "ui" {
		t.Fatalf("--trigger ui: exit=%d calls=%v", code, st2.calls)
	}
	// 始められなかったときはエラー（始めることが依頼そのもの）。
	st3 := &fakeStarter{err: errors.New("boom")}
	if code, _, _ := runWithStarter(t, st3, "cycle", "--detach", "--workspace", ws, "--json"); code == 0 {
		t.Error("a failed detach must exit non-zero")
	}
}

// AC-118: --detach の無い cycle は、始める処理を呼ばない（自分で回る。終わるまで戻る）。進められるものが無ければ始めない。
func TestCycle_WithoutDetachDoesNotCallTheStarter(t *testing.T) {
	parallelTools(t, false)
	ws := setupWorkspaceWithPosition(t)
	st := &fakeStarter{launch: server.CycleLaunch{Started: true}}
	code, stdout, stderr := runWithStarter(t, st, "cycle", "--workspace", ws, "--json")
	if code != 0 || len(st.calls) != 0 {
		t.Fatalf("exit=%d calls=%v stdout=%q stderr=%q", code, st.calls, stdout, stderr)
	}
}

func viewCycle(r core.CycleStartReason) view.CycleStart { return view.FromCycleStartReason(r) }

// server は、子が排他で断られたことを標準エラーの "(locked)" で見分ける。cli の書式とコードの値が変わったら、ここで落とす。
func TestLockedErrorTextCarriesTheMarkerTheServerLooksFor(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	writeTextError(&b, CodeLocked, "別のサイクルが走っている")
	if !strings.Contains(b.String(), "(locked)") {
		t.Fatalf("stderr line = %q", b.String())
	}
}
