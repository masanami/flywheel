package core

// このファイルは、区切り S4c-3 のうち「タスクを止める保留として渡す入口」と「保留が結び付くタスク以外を起動する・数える」を、
// 実ストアで検証する。受入基準の番号（AC-n）は、親の Issue の本文の「## 受入基準」の並びの順。

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
)

// holdScopeOf は、最後の保留の止める範囲（NULL は "NULL"）。
func (f *holdScopeFixture) holdScopeOf(t *testing.T) string {
	t.Helper()
	return f.q(t, `SELECT COALESCE(scope, 'NULL') FROM hold ORDER BY id DESC LIMIT 1`)
}

// newHoldScopeLaunchFixture は、統合先を使う、tasks を持つ着手中の課題（委譲の段は回さない）。
func newHoldScopeLaunchFixture(t *testing.T, tasks ...multiTask) *holdScopeFixture {
	t.Helper()
	f := newMultiFixture(t, 3)
	id := f.newMulti(t, "launch", "P1", true, tasks...)
	cid, _ := parseChallengeID(id)
	hf := &holdScopeFixture{contractFixture: f, id: id, cid: cid}
	if len(tasks) >= 1 {
		hf.taskA = mustTaskInt(f.taskID(t, id, tasks[0].key))
	}
	if len(tasks) >= 2 {
		hf.taskB = mustTaskInt(f.taskID(t, id, tasks[1].key))
	}
	return hf
}

// 契約の待ち（オーナーへの問い）を写した保留の scope は task（AC-1・AC-12）。
func TestHoldScope_AC12_ContractWaitHoldIsTaskScoped(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	f.waitHold(t, f.runA)
	if got := f.holdScopeOf(t); got != "task" {
		t.Errorf("scope = %q, want task", got)
	}
}

// 契約の start 失敗・停止（holdContractWithStop）の保留の scope は task（run がタスクに結び付くとき）。
func TestHoldScope_ContractStopHoldIsTaskScoped(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	held, err := f.s.holdContractWithReason(context.Background(), f.runA.RunID, f.id, "止まった", holdReasonContractStopped)
	if err != nil || !held {
		t.Fatalf("holdContractWithReason = %v, %v", held, err)
	}
	if got := f.holdScopeOf(t); got != "task" {
		t.Errorf("scope = %q, want task", got)
	}
	if got := f.q(t, `SELECT task_id FROM hold ORDER BY id DESC LIMIT 1`); got != strconv.FormatInt(f.taskA, 10) {
		t.Errorf("hold.task_id = %s, want %d", got, f.taskA)
	}
}

// 上げる依頼の保留（reflect_failed）は、run からタスクが 1 個に決まる場合でも、課題を止める保留（NULL）。
func TestHoldScope_ReflectHoldIsChallengeScopedEvenWhenTheTaskIsResolved(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	held, err := f.s.holdContractWithReason(context.Background(), f.runA.RunID, f.id, "上げる依頼が失敗した", holdReasonReflectFailed)
	if err != nil || !held {
		t.Fatalf("holdContractWithReason = %v, %v", held, err)
	}
	if got := f.holdScopeOf(t); got != "NULL" {
		t.Errorf("scope = %q, want NULL", got)
	}
}

// 委譲の子の問い・停止（mapReconciliationOptions）の保留の scope は task。
func TestHoldScope_DelegationChildStopHoldIsTaskScoped(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	dc := f.delegationContextOf(t, f.taskA)
	status, _, err := f.s.mapReconciliationStop(context.Background(), dc, f.runA.RunID, "子の問い", holdReasonContractStopped, "", false, StopKindChildQuestion)
	if err != nil || status == nil {
		t.Fatalf("mapReconciliationStop = %v, %v", status, err)
	}
	if got := f.holdScopeOf(t); got != "task" {
		t.Errorf("scope = %q, want task", got)
	}
}

func (f *holdScopeFixture) delegationContextOf(t *testing.T, task int64) *delegationContext {
	t.Helper()
	dc := &delegationContext{Agent: f.agent}
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		ch, err := loadChallenge(context.Background(), tx, f.cid)
		if err != nil {
			return err
		}
		dc.Challenge = *ch
		ts, err := listTasks(context.Background(), tx, f.cid)
		for _, tk := range ts {
			if taskIntID(&tk) == task {
				dc.Task = tk
			}
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return dc
}

// レビュー待ちの取り込みの止まり（holdContractReview*）の保留の scope は task。
func TestHoldScope_ReviewIntakeHoldIsTaskScoped(t *testing.T) {
	t.Parallel()
	f := newIntegrationFixture(t)
	f.readOnce(t, intakeOut([]string{"respond", "ready"}, integArtsOK, nil)) // land が無い → 保留
	if got := f.text(t, `SELECT COALESCE(scope, 'NULL') FROM hold ORDER BY id DESC LIMIT 1`); got != "task" {
		t.Errorf("scope = %q, want task", got)
	}
}

// タスクの検証が判断できないときの保留の scope は task（AC-13）。
func TestHoldScope_AC13_TaskVerifyUncertainHoldIsTaskScoped(t *testing.T) {
	t.Parallel()
	f := newTaskVerifyFixture(t)
	f.setVerdict("uncertain")
	if _, err := f.verifyTasks(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.text(t, `SELECT COALESCE(scope, 'NULL') FROM hold ORDER BY id DESC LIMIT 1`); got != "task" {
		t.Errorf("scope = %q, want task", got)
	}
}

// 差し戻しの上限の保留の scope は task（AC-14）。
func TestHoldScope_AC14_ReworkLimitHoldIsTaskScoped(t *testing.T) {
	t.Parallel()
	f := newTaskVerifyFixture(t)
	f.agent.ReworkLimit = 1
	f.setVerdict("not_met")
	if _, err := f.verifyTasks(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.text(t, `SELECT COALESCE(stop_kind, '') || '/' || COALESCE(scope, 'NULL') FROM hold ORDER BY id DESC LIMIT 1`); got != "rework_limit/task" {
		t.Errorf("stop_kind/scope = %q, want rework_limit/task", got)
	}
}

// 進めようが無い課題の保留（tasks_blocked）の scope は NULL（AC-7）。
func TestHoldScope_AC7_TasksBlockedHoldIsChallengeScoped(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "blocked", "P1", true, multiTask{key: "a"}, multiTask{key: "b", deps: []string{"a"}})
	cid, _ := parseChallengeID(id)
	setTaskStatusRaw(t, f.s, cid, "a", TaskStatusCancelled)
	f.runDelegationStage(t, nil)
	if got := f.delegateFixture.queryString(t, `SELECT COALESCE(scope, 'NULL') FROM hold WHERE challenge_id = ?`, cid); got != "NULL" {
		t.Errorf("scope = %q, want NULL", got)
	}
}

// 統合先を使わない課題（タスクが 1 個）の、タスクに結び付く保留は NULL（AC-3）。個別の試験は
// TestHoldScope_NonIntegrationTaskHoldIsChallengeScoped。ここでは、重ねる保留もできないことを確かめる。
func TestHoldScope_AC3_NonIntegrationChallengeNeverGetsATaskScopedHold(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "one", "P1", false, multiTask{key: "a"})
	f.runDelegationStage(t, nil)
	cid, _ := parseChallengeID(id)
	hf := &holdScopeFixture{contractFixture: f, id: id, cid: cid, taskA: mustTaskInt(f.taskID(t, id, "a"))}
	hf.waitHold(t, hf.reportOf(t, hf.taskA))
	if got := hf.holdScopeOf(t); got != "NULL" {
		t.Errorf("scope = %q, want NULL", got)
	}
}

// タスク a を止める保留が立つと、課題の状態は人間対応待ちになる（AC-17）。
func TestHoldScope_AC17_TaskHoldPutsTheChallengeToAwaitingHuman(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	f.waitHold(t, f.runA)
	if got := f.challengeStatus(t); got != "awaiting_human" {
		t.Errorf("status = %s, want awaiting_human", got)
	}
}

// タスク a を止める保留が開いている間、未着手で前提が済みのタスク b は起動する（AC-18）。a を前提に持つ c（AC-19）と、a 自身（AC-30）は起動しない。
func TestHoldScope_AC18_AC19_AC30_OnlyTheTaskWithoutAHoldOrAHeldPrerequisiteIsStarted(t *testing.T) {
	t.Parallel()
	f := newHoldScopeLaunchFixture(t, multiTask{key: "a"}, multiTask{key: "b"}, multiTask{key: "c", deps: []string{"a"}})
	if err := f.overlay(f.taskA, HoldOptionsInput{Scope: HoldScopeTask}); err != nil {
		t.Fatal(err)
	}
	if got := f.challengeStatus(t); got != "awaiting_human" {
		t.Fatalf("precondition: status = %s", got)
	}
	f.runDelegationStage(t, nil)
	want := []string{f.taskID(t, f.id, "b")}
	if got := f.startedTasks(t); !equalStrings(got, want) {
		t.Errorf("started tasks = %v, want %v（b だけ。a は保留、c は前提の a が済みでない）", got, want)
	}
	if got := f.challengeStatus(t); got != "awaiting_human" {
		t.Errorf("status = %s, want awaiting_human（保留は a のまま）", got)
	}
}

// 課題を止める保留が開いている間は、未着手で前提が済みのタスク b も起動しない（AC-31）。
func TestHoldScope_AC31_ChallengeHoldStartsNoTask(t *testing.T) {
	t.Parallel()
	f := newHoldScopeLaunchFixture(t, multiTask{key: "a"}, multiTask{key: "b"})
	if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, f.id, HoldInput{Question: "課題を止める"}); err != nil {
		t.Fatal(err)
	}
	f.runDelegationStage(t, nil)
	if got := f.startedTasks(t); len(got) != 0 {
		t.Errorf("started tasks = %v, want none", got)
	}
}

// 起動の ID 指定（課題を指した起動）も、タスクを止める保留だけが開いている課題では、保留の無いタスクを起動する。
func TestHoldScope_ExplicitChallengeLaunchStartsTheTaskWithoutAHold(t *testing.T) {
	t.Parallel()
	f := newHoldScopeLaunchFixture(t, multiTask{key: "a"}, multiTask{key: "b"})
	if err := f.overlay(f.taskA, HoldOptionsInput{Scope: HoldScopeTask}); err != nil {
		t.Fatal(err)
	}
	id := f.id
	f.runDelegationStage(t, &id)
	want := []string{f.taskID(t, f.id, "b")}
	if got := f.startedTasks(t); !equalStrings(got, want) {
		t.Errorf("started tasks = %v, want %v", got, want)
	}
}

// タスク a を止める保留が開いている間は、残りの未着手のタスクがどれも起動できなくても、tasks_blocked の保留は立たない（AC-36）。
func TestHoldScope_AC36_TasksBlockedIsNotRaisedWhileATaskHoldIsOpen(t *testing.T) {
	t.Parallel()
	f := newHoldScopeLaunchFixture(t, multiTask{key: "a"}, multiTask{key: "b"}, multiTask{key: "c", deps: []string{"b"}})
	cid := f.cid
	setTaskStatusRaw(t, f.s, cid, "a", TaskStatusDone)
	setTaskStatusRaw(t, f.s, cid, "b", TaskStatusCancelled)
	blocked := func() int {
		var n int
		if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
			out, err := selectDelegationTargetsFor(context.Background(), tx, "", f.conn)
			n = len(out.Blocked)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := blocked(); got != 1 {
		t.Fatalf("precondition: blocked = %d, want 1（c は取りやめの b を前提に持つ）", got)
	}
	if err := f.overlay(f.taskA, HoldOptionsInput{Scope: HoldScopeTask}); err != nil {
		t.Fatal(err)
	}
	if got := blocked(); got != 0 {
		t.Errorf("blocked = %d, want 0（タスクを止める保留が開いている間は立てない）", got)
	}
	f.runDelegationStage(t, nil)
	if got := f.holdReasons(t, f.id); len(got) != 0 {
		for _, r := range got {
			if r == "tasks_blocked" {
				t.Errorf("hold reasons = %v, want no tasks_blocked", got)
			}
		}
	}
}

// サイクルの「いま始められるもの」の数えは、タスクを止める保留だけが開いている課題の、進められるタスクを入れる。課題を止める保留が開いている課題は入れない。
func TestHoldScope_CycleWorkCountsTheProceedableTaskOfAHeldChallenge(t *testing.T) {
	t.Parallel()
	count := func(f *holdScopeFixture) int {
		w, err := f.s.CountCycleWork(context.Background(), f.agent, f.conn)
		if err != nil {
			t.Fatal(err)
		}
		return w.Stages.Run
	}
	f := newHoldScopeLaunchFixture(t, multiTask{key: "a"}, multiTask{key: "b"})
	if err := f.overlay(f.taskA, HoldOptionsInput{Scope: HoldScopeTask}); err != nil {
		t.Fatal(err)
	}
	if got := count(f); got < 1 {
		t.Errorf("run stage work with a task hold = %d, want >= 1（b は進められる）", got)
	}
	g := newHoldScopeLaunchFixture(t, multiTask{key: "a"}, multiTask{key: "b"})
	if _, err := g.s.HoldChallenge(context.Background(), ChannelCLI, g.id, HoldInput{Question: "課題を止める"}); err != nil {
		t.Fatal(err)
	}
	if got := count(g); got != 0 {
		t.Errorf("run stage work with a challenge hold = %d, want 0", got)
	}
}

// 起動できるタスクがすべて保留で外れたときは、起動しなかった理由を残す（人間対応待ちの分岐が、理由を落とさない）。
func TestHoldScope_SelectLaunchTasksKeepsTheNotStartedReasonWhenAllTasksAreHeld(t *testing.T) {
	t.Parallel()
	f := newHoldScopeLaunchFixture(t, multiTask{key: "a"}, multiTask{key: "b", deps: []string{"a"}})
	if err := f.overlay(f.taskA, HoldOptionsInput{Scope: HoldScopeTask}); err != nil {
		t.Fatal(err)
	}
	cid, _ := parseChallengeID(f.id)
	var sel *launchSelection
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		sel, err = selectLaunchTasks(context.Background(), tx, "", nil, f.id, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(sel.Tasks) != 0 {
		t.Fatalf("tasks = %v, want none", sel.Tasks)
	}
	if len(sel.NotStarted) == 0 && !sel.Busy {
		t.Errorf("NotStarted が空: 起動しなかった理由を引き継ぐ")
	}
}
