package core

// このファイルは、走っているサイクルの様子（running・watching・last_end_reason）と止める指示（RequestCycleStop）を、
// 実ストアで検証する（区切り S4a。AC-130〜AC-148・AC-157・AC-160・AC-165・AC-170・AC-171）。

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"
)

func (f *twoTaskFixture) runningAt(t *testing.T, hb time.Time) int64 {
	t.Helper()
	id := seedCycleTrigger(t, f.s, "manual", statusBase, nil, "")
	seedLock(t, f.s, id, hb)
	f.s.now = func() time.Time { return hb.Add(10 * time.Second) }
	return id
}

func (f *twoTaskFixture) watchingByTask(t *testing.T) map[string]WatchedRun {
	t.Helper()
	got, err := f.s.ReadCycleStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Running == nil {
		t.Fatalf("Running = nil, want a running cycle (%+v)", got)
	}
	out := map[string]WatchedRun{}
	for _, w := range got.Running.Watching {
		out[w.TaskID] = w
	}
	return out
}

// AC-130〜AC-143: 走っているサイクルの running の値。
func TestReadCycleStatus_Running_Values(t *testing.T) {
	t.Parallel()
	f := newTwoTaskFixture(t)
	id := f.runningAt(t, statusBase)
	if _, err := f.exec(`UPDATE cycle SET rounds = 3 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	// 判断点 0.50 と、終わっていない実行の読み取り 1.20（どちらも終わった run の費用）。
	for _, c := range []struct {
		kind string
		cost int64
	}{{"judgment", 500000}, {"contract", 1200000}} {
		if _, err := f.exec(`INSERT INTO run (cycle_id, challenge_id, kind, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket, cost_usd)
			VALUES (?, ?, ?, 1, 'h', ?, ?, ?, 'succeeded', 2000000, 'impl', ?)`,
			id, f.cidInt, c.kind, formatTimestamp(statusBase), formatTimestamp(statusBase), formatTimestamp(statusBase), c.cost); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.exec(`UPDATE contract_run SET cost_usd = 1200000 WHERE id = ?`, f.run1); err != nil {
		t.Fatal(err)
	}
	got, err := f.s.ReadCycleStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := got.Running
	if r == nil {
		t.Fatal("Running = nil")
	}
	if r.Trigger != "manual" || r.Rounds != 3 || !r.StartedAt.Equal(statusBase) || r.SpentUSD != 1.7 || r.BudgetUSD != 1 || r.MaxParallelRuns != 2 {
		t.Errorf("running = %+v", r)
	}
	if len(r.Watching) != 2 || r.ActiveRuns != 2 || r.StopRequested {
		t.Fatalf("watching = %d active = %d stop = %v, want 2 working runs", len(r.Watching), r.ActiveRuns, r.StopRequested)
	}
	w := f.watchingByTask(t)[formatTaskID(f.task1)]
	if w.ChallengeID != f.cid || w.Title == "" || w.SpentUSD != 1.2 || w.State != WatchStateWorking || w.RunStartedAt.IsZero() {
		t.Errorf("watching[task1] = %+v", w)
	}
}

// AC-144〜AC-146: 作業している・レビュー待ち・オーナー待ちの実行の様子。active_runs は working の件数。
func TestReadCycleStatus_Running_WatchStates(t *testing.T) {
	t.Parallel()
	f := newTwoTaskFixture(t)
	f.runningAt(t, statusBase)
	f.setRun(t, f.run1, "waiting", "review")
	f.setRun(t, f.run2, "waiting", "question")
	if _, err := f.exec(`UPDATE challenge SET status = 'verifying' WHERE id = ?`, f.cidInt); err != nil {
		t.Fatal(err)
	}
	w := f.watchingByTask(t)
	if got := w[formatTaskID(f.task1)].State; got != WatchStateVerifying {
		t.Errorf("review-waiting state = %s, want verifying", got)
	}
	// 課題が保留（awaiting_human）の間は、その課題の終わっていない実行は、オーナーの回答を待っている。
	if _, err := f.exec(`UPDATE challenge SET status = 'awaiting_human' WHERE id = ?`, f.cidInt); err != nil {
		t.Fatal(err)
	}
	w = f.watchingByTask(t)
	if got := w[formatTaskID(f.task2)].State; got != WatchStateWaitingOwner {
		t.Errorf("owner-waiting state = %s, want waiting_owner", got)
	}
	if st, _ := f.s.ReadCycleStatus(context.Background()); st.Running.ActiveRuns != 0 {
		t.Errorf("ActiveRuns = %d, want 0 (nothing is working)", st.Running.ActiveRuns)
	}
	if _, err := f.exec(`UPDATE challenge SET status = 'awaiting_completion_approval' WHERE id = ?`, f.cidInt); err != nil {
		t.Fatal(err)
	}
	if got := f.watchingByTask(t)[formatTaskID(f.task1)].State; got != WatchStateWaitingOwner {
		t.Errorf("completion-approval state = %s, want waiting_owner", got)
	}
	// 作業している契約の実行は working で、active_runs は同時に走らせる数の数え方と同じ。
	f.setRun(t, f.run1, "running", "")
	if _, err := f.exec(`UPDATE challenge SET status = 'in_progress' WHERE id = ?`, f.cidInt); err != nil {
		t.Fatal(err)
	}
	st, _ := f.s.ReadCycleStatus(context.Background())
	var n int
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
		n, err = activeDelegationCount(context.Background(), tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if st.Running.ActiveRuns != n || n == 0 {
		t.Errorf("ActiveRuns = %d, activeDelegationCount = %d", st.Running.ActiveRuns, n)
	}
}

// AC-147・AC-148: 走っていなければ running は nil。last_end_reason は最後に終わったサイクルの終わった理由。
func TestReadCycleStatus_NotRunning_NilRunningAndLastEndReason(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	end := statusBase.Add(time.Hour)
	id := seedCycle(t, s, statusBase, &end, CycleResultCompleted)
	if _, err := execStore(s, `UPDATE cycle SET end_reason = 'budget' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadCycleStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Running != nil {
		t.Errorf("Running = %+v, want nil", got.Running)
	}
	if got.LastEndReason == nil || *got.LastEndReason != CycleEndBudget {
		t.Errorf("LastEndReason = %v, want budget", got.LastEndReason)
	}
}

// 古い排他（stale）のときも running は nil。
func TestReadCycleStatus_Stale_NilRunning(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	id := seedCycle(t, s, statusBase, nil, "")
	seedLock(t, s, id, statusBase)
	s.now = func() time.Time { return statusBase.Add(time.Hour) }
	got, err := s.ReadCycleStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.State != CycleStateStale || got.Running != nil {
		t.Errorf("got state=%s running=%+v, want stale and nil", got.State, got.Running)
	}
}

func execStore(s *Store, q string, args ...any) (sql.Result, error) {
	var res sql.Result
	err := s.db.Write(context.Background(), func(tx *sql.Tx) (err error) {
		res, err = tx.Exec(q, args...)
		return err
	})
	return res, err
}

func stopRequestedAt(t *testing.T, s *Store, id int64) sql.NullString {
	t.Helper()
	var v sql.NullString
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT stop_requested_at FROM cycle WHERE id = ?`, id).Scan(&v)
	}); err != nil {
		t.Fatal(err)
	}
	return v
}

// AC-157・AC-160: 走っているサイクルがあれば stop_requested_at に時刻が入り、running.stop_requested は真になる。
func TestRequestCycleStop_Running_WritesStopRequestedAt(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	id := seedCycle(t, s, statusBase, nil, "")
	seedLock(t, s, id, statusBase)
	now := statusBase.Add(30 * time.Second)
	s.now = func() time.Time { return now }

	res, err := s.RequestCycleStop(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Requested || res.CycleID != formatCycleID(id) {
		t.Errorf("res = %+v", res)
	}
	if v := stopRequestedAt(t, s, id); !v.Valid || v.String != formatTimestamp(now) {
		t.Errorf("stop_requested_at = %+v, want %s", v, formatTimestamp(now))
	}
	st, _ := s.ReadCycleStatus(context.Background())
	if st.Running == nil || !st.Running.StopRequested {
		t.Errorf("Running = %+v, want stop_requested", st.Running)
	}
	// 2 回目は時刻を書き換えない。
	s.now = func() time.Time { return now.Add(time.Second) }
	if _, err := s.RequestCycleStop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := stopRequestedAt(t, s, id); v.String != formatTimestamp(now) {
		t.Errorf("stop_requested_at = %q after 2nd stop, want unchanged %s", v.String, formatTimestamp(now))
	}
}

// AC-165: heartbeat が回収の時間を過ぎている排他は、走っているサイクルが無い扱い（書かない）。
func TestRequestCycleStop_StaleLock_WritesNothing(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	id := seedCycle(t, s, statusBase, nil, "")
	seedLock(t, s, id, statusBase)
	s.now = func() time.Time { return statusBase.Add(301 * time.Second) }
	res, err := s.RequestCycleStop(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Requested || stopRequestedAt(t, s, id).Valid {
		t.Errorf("res = %+v, stop_requested_at = %+v; want nothing written", res, stopRequestedAt(t, s, id))
	}
}

// AC-170・AC-171: 走っているサイクルが無ければエラーにせず、サイクルの記録を変えない。
func TestRequestCycleStop_NoCycle_NoErrorAndRecordsUnchanged(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	end := statusBase.Add(time.Hour)
	id := seedCycle(t, s, statusBase, &end, CycleResultCompleted)
	res, err := s.RequestCycleStop(context.Background())
	if err != nil || res.Requested {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if stopRequestedAt(t, s, id).Valid {
		t.Error("stop_requested_at written for an ended cycle")
	}
}

// 宣言（agent.json）が不備でも、走っていなければ state・last_* は読める（上限を読むのは走っているときだけ）。
func TestReadCycleStatus_InvalidDeclaration_StillReadableWhenNotRunning(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	if err := os.WriteFile(agentFilePath(s.Workspace()), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadCycleStatus(context.Background())
	if err != nil || got.State != CycleStateIdle {
		t.Fatalf("got %+v, err %v; want idle without error", got, err)
	}
}
