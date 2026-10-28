package core

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"testing"
	"time"
)

var statusBase = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// seedCycle は周を 1 件作る（result が空なら未終了）。
func seedCycle(t *testing.T, s *Store, started time.Time, ended *time.Time, result CycleResult) int64 {
	t.Helper()
	return seedCycleTrigger(t, s, "t", started, ended, result)
}

func seedCycleTrigger(t *testing.T, s *Store, trigger string, started time.Time, ended *time.Time, result CycleResult) int64 {
	t.Helper()
	var id int64
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		row, err := insertCycle(context.Background(), tx, insertCycleInput{Trigger: trigger, StartedAt: started, BudgetUSD: 1000000})
		if err != nil {
			return err
		}
		id, _ = parseCycleID(row.ID)
		if ended != nil {
			_, err = updateCycleEnd(context.Background(), tx, id, updateCycleEndInput{EndedAt: *ended, Result: result})
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func seedLock(t *testing.T, s *Store, holder int64, heartbeat time.Time) {
	t.Helper()
	host, _ := os.Hostname()
	// 存在しないことが確かな pid（終了して回収済みの子）。生きていると判定されると回収の経路を通らない。
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	dead := int64(cmd.Process.Pid)
	err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := insertLock(context.Background(), tx, insertLockInput{
			Name: cycleLockName, Holder: holder, PID: dead, Host: host, AcquiredAt: heartbeat, HeartbeatAt: heartbeat,
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReadCycleStatus_Empty(t *testing.T) {
	s := newStoreForTest(t)
	got, err := s.ReadCycleStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.State != CycleStateIdle || got.LockHeld || got.LastStartedAt != nil || got.LastEndedAt != nil || got.LastResult != nil {
		t.Errorf("got %+v", got)
	}
}

func TestReadCycleStatus_HeartbeatBoundary(t *testing.T) {
	cases := []struct {
		age  time.Duration
		want CycleState
	}{
		{299 * time.Second, CycleStateRunning},
		{300 * time.Second, CycleStateRunning},
		{301 * time.Second, CycleStateStale},
	}
	for _, c := range cases {
		s := newStoreForTest(t)
		hb := statusBase
		id := seedCycle(t, s, hb, nil, "")
		seedLock(t, s, id, hb)
		s.now = func() time.Time { return hb.Add(c.age) }
		got, err := s.ReadCycleStatus(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got.State != c.want || !got.LockHeld {
			t.Errorf("age %v: got %+v, want %s", c.age, got, c.want)
		}
	}
}

func TestReadCycleStatus_NoLockOpenCycleIsIdle(t *testing.T) {
	s := newStoreForTest(t)
	seedCycle(t, s, statusBase, nil, "")
	got, err := s.ReadCycleStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.State != CycleStateIdle || got.LockHeld {
		t.Errorf("got %+v", got)
	}
}

func TestReadCycleStatus_IndividualOpCyclesExcluded(t *testing.T) {
	triggers := []string{"run", "classify --auto", "plan --auto", "verify --auto", "improve", "learn", "op exec"}
	s := newStoreForTest(t)
	for i, tr := range triggers {
		st := statusBase.Add(time.Duration(i) * time.Minute)
		e := st.Add(time.Second)
		seedCycleTrigger(t, s, tr, st, &e, CycleResultCompleted)
	}
	got, err := s.ReadCycleStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.LastStartedAt != nil || got.LastEndedAt != nil || got.LastResult != nil {
		t.Errorf("個別の操作の周だけのとき null: %+v", got)
	}
	// ui・manual の周は数え、より新しい個別の操作の周は飛ばす。
	e := statusBase.Add(time.Hour + time.Second)
	seedCycleTrigger(t, s, "ui", statusBase.Add(time.Hour), &e, CycleResultAborted)
	e2 := statusBase.Add(2*time.Hour + time.Second)
	seedCycleTrigger(t, s, "run", statusBase.Add(2*time.Hour), &e2, CycleResultCompleted)
	got, err = s.ReadCycleStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.LastStartedAt == nil || !got.LastStartedAt.Equal(statusBase.Add(time.Hour)) ||
		got.LastResult == nil || *got.LastResult != CycleResultAborted {
		t.Errorf("got %+v", got)
	}
}

func TestReadCycleStatus_Last(t *testing.T) {
	s := newStoreForTest(t)
	e1 := statusBase.Add(time.Minute)
	e2 := statusBase.Add(3 * time.Minute)
	seedCycle(t, s, statusBase, &e1, CycleResultAborted)
	seedCycle(t, s, statusBase.Add(2*time.Minute), &e2, CycleResultCompleted)
	third := statusBase.Add(10 * time.Minute) // 未終了。last_started_at だけがこれになる
	seedCycle(t, s, third, nil, "")
	got, err := s.ReadCycleStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.State != CycleStateIdle {
		t.Errorf("state = %s", got.State)
	}
	if got.LastStartedAt == nil || !got.LastStartedAt.Equal(third) {
		t.Errorf("last_started_at = %v", got.LastStartedAt)
	}
	if got.LastEndedAt == nil || !got.LastEndedAt.Equal(e2) || got.LastResult == nil || *got.LastResult != CycleResultCompleted {
		t.Errorf("last ended = %v %v", got.LastEndedAt, got.LastResult)
	}
}

func TestReadCycleStatus_NumericIDOrder(t *testing.T) {
	s := newStoreForTest(t)
	var lastStart time.Time
	for i := 0; i < 11; i++ {
		lastStart = statusBase.Add(time.Duration(i) * time.Minute)
		e := lastStart.Add(time.Second)
		seedCycle(t, s, lastStart, &e, CycleResultCompleted)
	}
	got, err := s.ReadCycleStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.State != CycleStateIdle || !got.LastStartedAt.Equal(lastStart) {
		t.Errorf("got %+v", got)
	}
}

func TestReadCycleStatus_DoesNotReclaim(t *testing.T) {
	s := newStoreForTest(t)
	id := seedCycle(t, s, statusBase, nil, "")
	seedLock(t, s, id, statusBase)
	s.now = func() time.Time { return statusBase.Add(time.Hour) }
	if _, err := s.ReadCycleStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ReadCycleStatus(context.Background())
	if !got.LockHeld || got.State != CycleStateStale || got.LastEndedAt != nil {
		t.Errorf("read must not reclaim: %+v", got)
	}
}

// 仕様は ID の最大で決める。時計が巻き戻って ID が大きい周の時刻が古くても、ID の最大を採る。
func TestReadCycleStatus_LastIsByIDNotByTime(t *testing.T) {
	s := newStoreForTest(t)
	e1 := statusBase.Add(time.Hour)
	seedCycle(t, s, statusBase.Add(50*time.Minute), &e1, CycleResultAborted) // ID 小・時刻が新しい
	startedLater := statusBase                                               // ID 大・時刻が古い
	e2 := statusBase.Add(time.Minute)
	seedCycle(t, s, startedLater, &e2, CycleResultCompleted)
	got, err := s.ReadCycleStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastStartedAt.Equal(startedLater) || !got.LastEndedAt.Equal(e2) || *got.LastResult != CycleResultCompleted {
		t.Errorf("got %+v", got)
	}
}

// AC-179: ロックが無ければ、終了していない周が（trigger ui の周でも run の周でも）あっても idle。
func TestReadCycleStatus_NoLockOpenCycleIsIdleForEachTrigger(t *testing.T) {
	for _, tr := range []string{"ui", "run", "cron", "classify --auto", "plan --auto", "verify --auto"} {
		t.Run(tr, func(t *testing.T) {
			s := newStoreForTest(t)
			seedCycleTrigger(t, s, tr, statusBase, nil, "")
			got, err := s.ReadCycleStatus(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got.State != CycleStateIdle || got.LockHeld {
				t.Errorf("got %+v, want idle without lock", got)
			}
		})
	}
}

// AC-180: cycle の周より ID の大きい個別の操作の周（7 つの trigger のそれぞれ。終了したものと
// 終了していないもの）は、last_started_at・last_ended_at・last_result に影響しない。
func TestReadCycleStatus_NewerIndividualOpCyclesNeverAffectLast(t *testing.T) {
	for _, tr := range []string{"run", "classify --auto", "plan --auto", "verify --auto", "improve", "learn", "op exec"} {
		t.Run(tr+"/cycle は終了済み", func(t *testing.T) {
			s := newStoreForTest(t)
			cycleStart := statusBase
			cycleEnd := statusBase.Add(time.Minute)
			seedCycleTrigger(t, s, "ui", cycleStart, &cycleEnd, CycleResultCompleted)
			opEnd := statusBase.Add(11 * time.Minute)
			seedCycleTrigger(t, s, tr, statusBase.Add(10*time.Minute), &opEnd, CycleResultAborted) // 終了した操作の周
			seedCycleTrigger(t, s, tr, statusBase.Add(20*time.Minute), nil, "")                    // 終了していない操作の周
			got, err := s.ReadCycleStatus(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got.LastStartedAt == nil || !got.LastStartedAt.Equal(cycleStart) ||
				got.LastEndedAt == nil || !got.LastEndedAt.Equal(cycleEnd) ||
				got.LastResult == nil || *got.LastResult != CycleResultCompleted {
				t.Errorf("got %+v", got)
			}
		})
		t.Run(tr+"/cycle は終了していない", func(t *testing.T) {
			s := newStoreForTest(t)
			cycleStart := statusBase
			seedCycleTrigger(t, s, "cron", cycleStart, nil, "")
			opEnd := statusBase.Add(11 * time.Minute)
			seedCycleTrigger(t, s, tr, statusBase.Add(10*time.Minute), &opEnd, CycleResultCompleted)
			seedCycleTrigger(t, s, tr, statusBase.Add(20*time.Minute), nil, "")
			got, err := s.ReadCycleStatus(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got.LastStartedAt == nil || !got.LastStartedAt.Equal(cycleStart) || got.LastEndedAt != nil || got.LastResult != nil {
				t.Errorf("got %+v", got)
			}
		})
	}
}
