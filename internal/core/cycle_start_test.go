package core

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// 区切り S4b: 操作が成立した後に、サイクルを始めるかの判定（CheckCycleStart）と、閉じた一覧。

func startAgent(autoCycle bool) *AgentDeclaration {
	return &AgentDeclaration{AutoCycle: autoCycle}
}

func checkStart(t *testing.T, s *Store, agent *AgentDeclaration) CycleStartReason {
	t.Helper()
	r, err := s.CheckCycleStart(context.Background(), agent)
	if err != nil {
		t.Fatalf("CheckCycleStart: %v", err)
	}
	return r
}

// AC-74: 走っておらず、いま始められるものが 1 件以上 → 始めてよい。
func TestCheckCycleStart_IdleWithWorkIsReady(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	mustCreateChallenge(t, s, "work")
	if got := checkStart(t, s, startAgent(true)); got != CycleStartStarted {
		t.Fatalf("got %q, want started", got)
	}
}

// AC-80: 生きている排他があれば already_running。
func TestCheckCycleStart_LiveLockIsAlreadyRunning(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	mustCreateChallenge(t, s, "work")
	id := seedCycle(t, s, statusBase, nil, "")
	seedLock(t, s, id, statusBase)
	s.now = func() time.Time { return statusBase.Add(10 * time.Second) }
	if got := checkStart(t, s, startAgent(true)); got != CycleStartAlreadyRunning {
		t.Fatalf("got %q, want already_running", got)
	}
}

// AC-94: heartbeat が回収の時間を過ぎた排他は、走っていない扱い。
func TestCheckCycleStart_StaleLockCountsAsNotRunning(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	mustCreateChallenge(t, s, "work")
	id := seedCycle(t, s, statusBase, nil, "")
	seedLock(t, s, id, statusBase)
	s.now = func() time.Time { return statusBase.Add(301 * time.Second) }
	if got := checkStart(t, s, startAgent(true)); got != CycleStartStarted {
		t.Fatalf("got %q, want started", got)
	}
}

// AC-82・AC-83: いま始められるものが 0 件なら no_work。
func TestCheckCycleStart_NoWork(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	if got := checkStart(t, s, startAgent(true)); got != CycleStartNoWork {
		t.Fatalf("got %q, want no_work", got)
	}
	if got := checkStart(t, s, nil); got != CycleStartNoWork {
		t.Fatalf("agent nil: got %q, want no_work", got)
	}
}

// AC-86・AC-87: auto_cycle が偽なら disabled（始められるものがあっても）。
func TestCheckCycleStart_AutoCycleFalseIsDisabled(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	mustCreateChallenge(t, s, "work")
	if got := checkStart(t, s, startAgent(false)); got != CycleStartDisabled {
		t.Fatalf("got %q, want disabled", got)
	}
}

// AC-105: 学びの段だけに対象が残っているときは no_work（「進められるもの」の多めの数えを使わない）。
func TestCheckCycleStart_OnlyLearnWorkIsNoWork(t *testing.T) {
	t.Parallel()
	f := newRoundsFixture(t)
	f.seedLearnAndImprove(t)
	all, err := f.s.CountCycleWork(context.Background(), f.agent, f.conn)
	if err != nil || all.Stages.Learn == 0 {
		t.Fatalf("precondition: CountCycleWork learn = %+v err=%v", all, err)
	}
	f.agent.AutoCycle = true
	if got := checkStart(t, f.s, f.agent); got != CycleStartNoWork {
		t.Fatalf("got %q, want no_work", got)
	}
}

// trigger と理由の閉じた一覧（internal/view/closedsets.go が写す）。
func TestCycleStartClosedSets(t *testing.T) {
	t.Parallel()
	if got, want := CycleTriggerValues(), []string{"manual", "ui", "approve", "reject", "answer", "budget", "ingest", "followup_promote", "continue"}; !reflect.DeepEqual(got, want) {
		t.Errorf("triggers = %v", got)
	}
	var reasons []string
	for _, r := range CycleStartReasonValues() {
		reasons = append(reasons, string(r))
	}
	if want := []string{"started", "already_running", "no_work", "disabled", "unavailable"}; !reflect.DeepEqual(reasons, want) {
		t.Errorf("reasons = %v", reasons)
	}
}

// 新しい trigger の値の周は、個別の操作の周として数えない（最後の周の表示に入る）。
func TestReadCycleStatus_NewTriggersAreNotIndividualOperations(t *testing.T) {
	t.Parallel()
	for _, trig := range CycleTriggerValues() {
		s := newStoreForTest(t)
		end := statusBase.Add(time.Minute)
		seedCycleTrigger(t, s, trig, statusBase, &end, CycleResultCompleted)
		got, err := s.ReadCycleStatus(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got.LastStartedAt == nil || got.LastResult == nil {
			t.Errorf("trigger %q was excluded as an individual operation: %+v", trig, got)
		}
	}
}
