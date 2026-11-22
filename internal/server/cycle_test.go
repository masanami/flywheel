package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// initWorkspaceWithWork は、サイクルを回すと進むもの（未分類の課題 1 件）を持つワークスペースを返す。
// 進むものが無いワークスペースでは、POST は子を起動せずに started: false を返す。
func initWorkspaceWithWork(t *testing.T) string {
	t.Helper()
	ws := initWorkspace(t)
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.CreateChallenge(context.Background(), core.ChannelCLI, core.CreateInput{Title: "t", DoneCriteria: "d"}); err != nil {
		t.Fatal(err)
	}
	return ws
}

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
		"echo '周 Y-1 を開始した'\n" +
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

// testSignalWait は、合図か子の終了で結果が決まるテストが渡す待ち時間。本番の 3 秒（cycleSignalWait）を
// そのまま使うと、検査を重ねて遅いときに、偽の子が終わる前に待ち時間が切れて「始まった」が返り、テストが
// 落ちる（flywheel#825）。長い値にして、時計ではなく合図か子の終了で結果が決まるようにする。
const testSignalWait = 2 * time.Minute

// startCycleServer は、合図か子の終了で結果が決まる待ち時間（testSignalWait）で server を立てる。
func startCycleServer(t *testing.T, exe, ws string) int {
	t.Helper()
	return startCycleServerWait(t, exe, ws, testSignalWait)
}

// startCycleServerWait は、合図を待つ時間を渡して server を立てる。
func startCycleServerWait(t *testing.T, exe, ws string, signalWait time.Duration) int {
	t.Helper()
	return startCycleServerWith(t, ws, NewCycleStarterWait(func() (string, error) { return exe, nil }, signalWait))
}

// startCycleServerWith は、起動の口を渡して server を立てる。
func startCycleServerWith(t *testing.T, ws string, launch CycleStarter) int {
	t.Helper()
	ln, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	s := newServerWithLauncher(ln, []core.FleetWorkspace{{Name: "w", Path: ws}}, eventsConfig{}, launch)
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
		func(string, string) (CycleLaunch, error) {
			atomic.AddInt32(calls, 1)
			return CycleLaunch{Started: true}, nil
		})
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
	ws := initWorkspaceWithWork(t)
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
	ws := initWorkspaceWithWork(t)
	exe, _ := fakeExecutable(t) // 60 秒眠る
	port := startCycleServer(t, exe, ws)
	start := time.Now()
	if code, body := apiPost(t, port, cyclePath, ""); code != 200 {
		t.Fatalf("POST = %d %s", code, body)
	}
	// 合図で返る（子の 60 秒の終了は待たない）。上限は、検査を重ねて遅いときでも合図までに十分な長さにする。
	if d := time.Since(start); d > 30*time.Second {
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

// AC-152: cookie はあるがヘッダが無い cycle の要求も拒否し、起動しない。
func TestCyclePostWithCookieButNoHeaderIsForbiddenAndStartsNothing(t *testing.T) {
	ws := initWorkspace(t)
	port, calls := countingServer(t, ws)
	token, _ := fetchToken(t, port)
	code, body := post(t, port, cyclePath, token, "")
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

// scriptLauncherServer は script を実行ファイルとして POST を受ける server を立てる（待ち時間は testSignalWait）。
func scriptLauncherServer(t *testing.T, script string) (port int, ws string) {
	t.Helper()
	return scriptLauncherServerWait(t, script, testSignalWait)
}

// scriptLauncherServerWait は、合図を待つ時間を渡す版。
func scriptLauncherServerWait(t *testing.T, script string, signalWait time.Duration) (port int, ws string) {
	t.Helper()
	ws = initWorkspaceWithWork(t)
	return startCycleServerWait(t, scriptExecutable(t, script), ws, signalWait), ws
}

// scriptExecutable は script を偽の実行ファイルとして書き出す。
func scriptExecutable(t *testing.T, script string) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "fake-flywheel")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestCyclePostChildExitsBeforeSignalIsInternalError(t *testing.T) {
	port, _ := scriptLauncherServer(t, "echo 'config_invalid: 宣言の不備' >&2\nexit 3\n")
	code, body := apiPost(t, port, cyclePath, "")
	if code != 500 || errCode(t, body) != "internal_error" || strings.Contains(body, "started") {
		t.Fatalf("POST = %d %s", code, body)
	}
	if !strings.Contains(body, "終了コード 3") || !strings.Contains(body, "config_invalid: 宣言の不備") {
		t.Errorf("message に終了コードと標準エラーが無い: %s", body)
	}
}

// AC-177a: 合図の前に終了コード 1 で終わる偽の実行ファイルでの POST は、500・internal_error で返り、
// message に終了コードと標準エラーの末尾（2,048 バイト以内）を含み、started を返さない。
func TestCyclePostExitCode1StderrCases(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		want   func(tail string) bool // tail の追加検査
	}{
		{"短い標準エラーは全文", "boom: 失敗\n", func(tail string) bool { return strings.Contains(tail, "boom: 失敗") }},
		{"標準エラーが空", "", func(tail string) bool { return tail == "" }},
		// 3 バイト文字 1,000 個 = 3,000 バイト。末尾 2,048 バイトの先頭は文字の途中になる。
		{"3 バイト文字は 682 文字（2,046 バイト）", strings.Repeat("あ", 1000), func(tail string) bool {
			return strings.Count(tail, "あ") == 682 && len(tail) == 682*3
		}},
		{"ASCII は 2,048 バイトちょうど", strings.Repeat("x", 3000), func(tail string) bool {
			return len(tail) == 2048
		}},
		{"先頭が捨てられ末尾が残る", "HEAD" + strings.Repeat("x", 3000) + "TAIL", func(tail string) bool {
			return strings.HasSuffix(tail, "TAIL") && !strings.Contains(tail, "HEAD") && len(tail) == 2048
		}},
		{"ちょうど 2,048 バイトは全文", strings.Repeat("y", 2048), func(tail string) bool {
			return len(tail) == 2048
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := filepath.Join(t.TempDir(), "err.txt")
			if err := os.WriteFile(data, []byte(tc.stderr), 0o644); err != nil {
				t.Fatal(err)
			}
			port, _ := scriptLauncherServer(t, "cat '"+data+"' >&2\nexit 1\n")
			code, body := apiPost(t, port, cyclePath, "")
			if code != 500 || errCode(t, body) != "internal_error" || strings.Contains(body, "started") {
				t.Fatalf("POST = %d %s", code, body)
			}
			var r struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(body), &r); err != nil {
				t.Fatal(err)
			}
			msg := r.Error.Message
			const marker = "終了コード 1"
			_, rest, ok := strings.Cut(msg, marker)
			if !ok {
				t.Fatalf("message に終了コードが無い: %q", msg)
			}
			_, tail, ok := strings.Cut(rest, "）: ")
			if !ok {
				t.Fatalf("message から標準エラーの末尾を取り出せない: %q", msg)
			}
			if !utf8.ValidString(msg) || strings.Contains(msg, "\uFFFD") {
				t.Errorf("不正な UTF-8: %q", msg)
			}
			if len(tail) > 2048 {
				t.Errorf("標準エラーの末尾が %d バイトで 2,048 を超える", len(tail))
			}
			if !tc.want(tail) {
				t.Errorf("tail = %d バイト: %.80q", len(tail), tail)
			}
		})
	}
}

// 合図も終了も無い子（60 秒眠る）では、本番の口（execLauncher。待ち時間は 3 秒）は、3 秒待ってから、
// 子の終了を待たずに started: true を返す。結果は時間の経過だけで決まる（子は合図も終了もしない）ので、
// 検査を重ねて遅いときでも、下限は割らない。
func TestCyclePostNoSignalNorExitReturnsStartedAfterThreeSeconds(t *testing.T) {
	ws := initWorkspaceWithWork(t)
	exe := scriptExecutable(t, "exec sleep 60\n")
	port := startCycleServerWith(t, ws, NewCycleStarter(func() (string, error) { return exe, nil }))
	start := time.Now()
	code, body := apiPost(t, port, cyclePath, "")
	if code != 200 || strings.TrimSpace(body) != `{"started":true}` {
		t.Fatalf("POST = %d %s", code, body)
	}
	// 下限: 本番の待ち時間（3 秒）より前には返らない。上限: 子（60 秒眠る）の終了は待たない。
	if d := time.Since(start); d < 2500*time.Millisecond || d > 30*time.Second {
		t.Errorf("応答までの時間 = %v", d)
	}
}

// 待ち時間を渡す口（NewCycleStarterWait）は、渡した待ち時間のとおりに待つ。本番の 3 秒より長い値を渡して、
// それより前に返らないことを確かめる（引数を見ずに 3 秒で返ると落ちる。下限だけなので、遅いときも落ちない）。
func TestCyclePostNoSignalNorExitHonorsThePassedWait(t *testing.T) {
	const wait = cycleSignalWait + 1500*time.Millisecond
	port, _ := scriptLauncherServerWait(t, "exec sleep 60\n", wait)
	start := time.Now()
	code, body := apiPost(t, port, cyclePath, "")
	if code != 200 || strings.TrimSpace(body) != `{"started":true}` {
		t.Fatalf("POST = %d %s", code, body)
	}
	if d := time.Since(start); d < wait {
		t.Errorf("待ち時間（%v）より前に返った: %v", wait, d)
	}
}

func TestCyclePostSignalThenExitStillStarted(t *testing.T) {
	port, _ := scriptLauncherServer(t, "echo '周 Y-1 を開始した'\nexit 2\n")
	if code, body := apiPost(t, port, cyclePath, ""); code != 200 || strings.TrimSpace(body) != `{"started":true}` {
		t.Fatalf("POST = %d %s", code, body)
	}
}

// サイクルを回すと進むものが無いときは、子のプロセスを起動せずに started: false を返す（エラーにしない）。
func TestCyclePostNothingToAdvanceReturnsNotStartedWithoutLaunching(t *testing.T) {
	ws := initWorkspace(t)
	if err := os.WriteFile(filepath.Join(ws, ".flywheel", "agent.json"), []byte(`{"version":1,"position_file":"position.md"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "position.md"), []byte("p"), 0o644); err != nil {
		t.Fatal(err)
	}
	port, calls := countingServer(t, ws)
	code, body := apiPost(t, port, cyclePath, "")
	if code != 200 || strings.TrimSpace(body) != `{"started":false}` {
		t.Fatalf("POST = %d %s", code, body)
	}
	if n := atomic.LoadInt32(calls); n != 0 {
		t.Errorf("起動が %d 回呼ばれた", n)
	}
	// サイクルの記録は作られない（AC-225）。
	if n := fmt.Sprint(coretest.QueryRows(t, ws, "SELECT COUNT(*) FROM cycle")[0][0]); n != "0" {
		t.Errorf("cycle rows = %s, want 0", n)
	}
}

// 合図の前に終了コード 0 で終わった子は、周を始めなかった（started: false。エラーにしない）。
func TestCyclePostChildExitsZeroBeforeSignalIsNotStarted(t *testing.T) {
	port, _ := scriptLauncherServer(t, "echo '進められるものが無いので、サイクルを始めなかった（理由: nothing_to_advance）'\nexit 0\n")
	if code, body := apiPost(t, port, cyclePath, ""); code != 200 || strings.TrimSpace(body) != `{"started":false}` {
		t.Fatalf("POST = %d %s", code, body)
	}
}

// 最初の行が合図の形でなければ合図にしない。「を開始した」で終わる行だけが開始の合図。
func TestCyclePostOnlyTheSignalLineShapeCountsAsStarted(t *testing.T) {
	port, _ := scriptLauncherServer(t, "echo 'warning: something'\necho '周 Y-1 を開始した'\nexec sleep 60\n")
	if code, body := apiPost(t, port, cyclePath, ""); code != 200 || strings.TrimSpace(body) != `{"started":true}` {
		t.Fatalf("POST = %d %s", code, body)
	}
}

// AC-149・AC-150: GET …/cycle は running と last_end_reason を返し、いまあるキーは変えない。走っているサイクルの
// running は、止める指示の後に stop_requested が真になる。終わった後の last_end_reason は最後の周の終わった理由。
func TestCycleGetRunningAndLastEndReason(t *testing.T) {
	ws := initWorkspace(t)
	exe, _ := fakeExecutable(t)
	port := startCycleServer(t, exe, ws)
	get := func() map[string]any {
		code, body := getPath(t, port, cyclePath, "", "")
		if code != 200 {
			t.Fatalf("GET = %d %s", code, body)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	m := get()
	for _, k := range []string{"state", "last_started_at", "last_ended_at", "last_result", "running", "last_end_reason"} {
		if _, ok := m[k]; !ok {
			t.Errorf("key %q missing: %v", k, m)
		}
	}
	if m["running"] != nil || m["last_end_reason"] != nil {
		t.Errorf("idle: running=%v last_end_reason=%v, want null", m["running"], m["last_end_reason"])
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
	r, ok := get()["running"].(map[string]any)
	if !ok || r["trigger"] != "cron" || r["budget_usd"] != 10.0 || r["stop_requested"] != false || r["max_parallel_runs"] != float64(2) {
		t.Fatalf("running = %v", r)
	}
	if res, err := st.RequestCycleStop(context.Background()); err != nil || !res.Requested {
		t.Fatalf("RequestCycleStop = %+v, %v", res, err)
	}
	if r, _ := get()["running"].(map[string]any); r == nil || r["stop_requested"] != true {
		t.Errorf("running after stop = %v", r)
	}

	if _, err := st.EndCycleWithReason(context.Background(), cyc.ID, core.CycleResultCompleted, core.CycleEndStopped); err != nil {
		t.Fatal(err)
	}
	m = get()
	if m["running"] != nil || m["last_end_reason"] != "stopped" {
		t.Errorf("ended: running=%v last_end_reason=%v", m["running"], m["last_end_reason"])
	}
}
