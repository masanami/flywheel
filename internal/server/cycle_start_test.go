package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
	"github.com/masanami/flywheel/internal/view"
)

// 区切り S4b: 操作が成立した後にサイクルを始めるかの判定と、切り離して始める処理（StartCycle の形）。

type startRecorder struct {
	calls    int32
	trigger  string
	launch   CycleLaunch
	err      error
	workPath string
}

func (r *startRecorder) start(ws, trigger string) (CycleLaunch, error) {
	atomic.AddInt32(&r.calls, 1)
	r.trigger, r.workPath = trigger, ws
	return r.launch, r.err
}

func openStore(t *testing.T, ws string) *core.Store {
	t.Helper()
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func afterOp(t *testing.T, ws string, noCycle bool, r *startRecorder) view.CycleStart {
	t.Helper()
	return StartCycleAfterOperation(context.Background(), openStore(t, ws), "approve", noCycle, r.start)
}

// AC-74〜78: 始められるものがあれば、始める処理が 1 回呼ばれ、渡すきっかけは操作の名前。結果は started。
func TestStartCycleAfterOperation_StartsOnceWithTheTrigger(t *testing.T) {
	ws := initWorkspaceWithWork(t)
	r := &startRecorder{launch: CycleLaunch{Started: true}}
	got := afterOp(t, ws, false, r)
	if got != (view.CycleStart{Started: true, Reason: "started"}) {
		t.Fatalf("got %+v", got)
	}
	if r.calls != 1 || r.trigger != "approve" {
		t.Fatalf("calls=%d trigger=%q", r.calls, r.trigger)
	}
}

// AC-82・AC-83: 始められるものが 0 件 → 呼ばない・no_work。
func TestStartCycleAfterOperation_NoWork(t *testing.T) {
	ws := initWorkspace(t)
	r := &startRecorder{launch: CycleLaunch{Started: true}}
	if got := afterOp(t, ws, false, r); got.Reason != "no_work" || got.Started || r.calls != 0 {
		t.Fatalf("got %+v calls=%d", got, r.calls)
	}
}

// AC-84・AC-85: --no-cycle → 呼ばない・disabled。
func TestStartCycleAfterOperation_NoCycleFlagIsDisabled(t *testing.T) {
	ws := initWorkspaceWithWork(t)
	r := &startRecorder{launch: CycleLaunch{Started: true}}
	if got := afterOp(t, ws, true, r); got.Reason != "disabled" || got.Started || r.calls != 0 {
		t.Fatalf("got %+v calls=%d", got, r.calls)
	}
}

// AC-86・AC-87: 宣言の auto_cycle が偽 → 呼ばない・disabled。
func TestStartCycleAfterOperation_AutoCycleFalseIsDisabled(t *testing.T) {
	ws := initWorkspaceWithWork(t)
	writeAgentAutoCycleFalse(t, ws)
	r := &startRecorder{launch: CycleLaunch{Started: true}}
	if got := afterOp(t, ws, false, r); got.Reason != "disabled" || got.Started || r.calls != 0 {
		t.Fatalf("got %+v calls=%d", got, r.calls)
	}
}

// AC-80・AC-81: 生きている排他がある → 呼ばない・already_running。
func TestStartCycleAfterOperation_LiveLockIsAlreadyRunning(t *testing.T) {
	ws := initWorkspaceWithWork(t)
	st := openStore(t, ws)
	if _, err := st.BeginCycle(context.Background(), core.BeginCycleInput{Trigger: "cron", BudgetUSD: 10, Exclusive: true}); err != nil {
		t.Fatal(err)
	}
	r := &startRecorder{launch: CycleLaunch{Started: true}}
	got := StartCycleAfterOperation(context.Background(), st, "approve", false, r.start)
	if got.Reason != "already_running" || got.Started || r.calls != 0 {
		t.Fatalf("got %+v calls=%d", got, r.calls)
	}
}

// AC-89〜92: 始める処理がエラーを返しても、結果は unavailable（操作の成否には影響しない）。
func TestStartCycleAfterOperation_StarterErrorIsUnavailable(t *testing.T) {
	ws := initWorkspaceWithWork(t)
	r := &startRecorder{err: errors.New("claude not found")}
	if got := afterOp(t, ws, false, r); got.Reason != "unavailable" || got.Started || r.calls != 1 {
		t.Fatalf("got %+v calls=%d", got, r.calls)
	}
	// 始める処理そのものが無い（nil）ときも unavailable。
	if got := StartCycleAfterOperation(context.Background(), openStore(t, ws), "approve", false, nil); got.Reason != "unavailable" {
		t.Fatalf("nil starter: got %+v", got)
	}
}

// AC-93: 始めたプロセスが排他で断られた → already_running。
func TestStartCycleAfterOperation_ChildRefusedByTheLockIsAlreadyRunning(t *testing.T) {
	ws := initWorkspaceWithWork(t)
	r := &startRecorder{launch: CycleLaunch{AlreadyRunning: true}}
	if got := afterOp(t, ws, false, r); got.Reason != "already_running" || got.Started {
		t.Fatalf("got %+v", got)
	}
}

// 子が周を始めずに終了コード 0 で終わった（読んだ後に対象が無くなった）→ no_work。
func TestStartCycleAfterOperation_ChildThatStartedNothingIsNoWork(t *testing.T) {
	ws := initWorkspaceWithWork(t)
	r := &startRecorder{}
	if got := afterOp(t, ws, false, r); got.Reason != "no_work" || got.Started {
		t.Fatalf("got %+v", got)
	}
}

// 切り離して始める処理: 子の標準エラーに locked のコードがあれば AlreadyRunning（エラーにしない）、
// ほかの失敗はエラー、合図があれば Started。引数は `cycle --trigger <t> --workspace <path>`。
func TestNewCycleStarter_MapsTheChildsOutcome(t *testing.T) {
	ws := initWorkspaceWithWork(t)
	cases := []struct {
		name, script string
		want         CycleLaunch
		wantErr      bool
	}{
		{"locked", "echo 'flywheel: another cycle is running (locked)' >&2\nexit 1\n", CycleLaunch{AlreadyRunning: true}, false},
		{"other failure", "echo 'flywheel: boom (config_invalid)' >&2\nexit 2\n", CycleLaunch{}, true},
		{"nothing started", "exit 0\n", CycleLaunch{}, false},
		{"signal", "echo '周 Y-1 を開始した'\nexit 0\n", CycleLaunch{Started: true}, false},
	}
	for _, c := range cases {
		exe := scriptExecutable(t, c.script)
		got, err := NewCycleStarterWait(func() (string, error) { return exe, nil }, testSignalWait)(ws, "approve")
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("%s: got %+v err=%v", c.name, got, err)
		}
	}
	// 実行ファイルを解決できない → エラー。
	if _, err := NewCycleStarter(func() (string, error) { return "", errors.New("no exe") })(ws, "approve"); err == nil {
		t.Error("unresolvable executable must be an error")
	}
}

// 引数に、操作の名前の trigger とワークスペースが渡る。
func TestNewCycleStarter_PassesTheTriggerAndWorkspace(t *testing.T) {
	ws := initWorkspaceWithWork(t)
	exe, rec := fakeExecutable(t)
	if got, err := NewCycleStarterWait(func() (string, error) { return exe, nil }, testSignalWait)(ws, "followup_promote"); err != nil || !got.Started {
		t.Fatalf("got %+v err=%v", got, err)
	}
	args := waitFile(t, rec)
	if want := "cycle --trigger followup_promote --workspace " + ws; len(args) < len(want) || args[:len(want)] != want {
		t.Errorf("args = %q, want prefix %q", args, want)
	}
}

// go test のバイナリは、サイクルの子として起動しない。
func TestDefaultExecutable_RefusesATestBinary(t *testing.T) {
	if _, err := defaultExecutable(); err == nil {
		t.Fatal("the test binary must not be launched as a cycle child")
	}
}

func writeAgentAutoCycleFalse(t *testing.T, ws string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ws, ".flywheel", "agent.json"), []byte(`{"position_file":"position.md","auto_cycle":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 画面の「サイクルを回す」も、heartbeat が回収の時間を過ぎた排他は走っていない扱いで、子を起動する（409 にしない）。
func TestCyclePostWithStaleLockStartsTheChild(t *testing.T) {
	ws := initWorkspaceWithWork(t)
	id := coretest.InsertCycle(t, ws, "cron", 10_000_000, "2026-01-01T00:00:00.000Z")
	coretest.InsertLock(t, ws, id, 1, "h", "2026-01-01T00:00:00.000Z", "2026-01-01T00:00:00.000Z")
	port, calls := countingServer(t, ws)
	code, body := apiPost(t, port, cyclePath, "")
	if code != 200 {
		t.Fatalf("POST = %d %s", code, body)
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Errorf("起動が %d 回呼ばれた、want 1", n)
	}
}

// 子が排他で断られたことを見分ける印は、cli の標準エラーの書式（internal/cli の同名の試験が確かめる）と同じ値。
func TestCycleLockedMarkerValue(t *testing.T) {
	if cycleLockedMarker != "(locked)" {
		t.Fatalf("marker = %q", cycleLockedMarker)
	}
}
