package core

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"
)

// このファイルは `form: cli` の契約の run の扱い（専用の kind・失敗と中止の保留への写し・start の最中の回収・
// 自動の対象選びからの除外・読みの失敗と同時の読み）を、実ストア・偽の ContractClient で検証する。

type reclaimingClient struct {
	*fakeContractClient
	onStart func()
}

func (c *reclaimingClient) Start(ctx context.Context, in ContractLaunchInput) (ContractLaunchOutput, error) {
	out, err := c.fakeContractClient.Start(ctx, in)
	if c.onStart != nil {
		c.onStart()
	}
	return out, err
}

func (f *contractFixture) exec(q string, args ...any) (sql.Result, error) {
	var res sql.Result
	err := f.s.db.Write(context.Background(), func(tx *sql.Tx) (err error) {
		res, err = tx.Exec(q, args...)
		return err
	})
	return res, err
}

func (f *contractFixture) status(t *testing.T, id string) Status {
	t.Helper()
	return f.detail(t, id).Status
}

// 契約の run は専用の kind で記録し、委譲の履歴の規則（run の一覧・連続失敗・delegation_failed）に入れない。
func TestContractRun_HasItsOwnKindAndStaysOutOfTheDelegationHistory(t *testing.T) {
	f := newContractFixture(t)
	f.client.startOut = ContractLaunchOutput{Result: RunResultTimedOut, ErrorSummary: "timed out"}
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.startOut = contractOut(ContractStateRunning, nil, nil)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{{Result: RunResultErrored, ErrorSummary: "boom"}}
	f.poll(t)

	if n := f.count(t, `SELECT COUNT(*) FROM run WHERE kind = 'contract'`); n != 3 {
		t.Fatalf("contract runs = %d, want 3", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM run WHERE kind = 'delegate'`); n != 0 {
		t.Fatalf("delegate runs = %d, want 0（契約の run は委譲の run ではない）", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM occurrence`); n != 0 {
		t.Fatalf("occurrences = %d, want 0（契約のコマンドの失敗は delegation_failed を作らない）", n)
	}
	var h *launchHistory
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
		h, err = loadLaunchHistory(context.Background(), tx, 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(h.Runs) != 0 {
		t.Fatalf("launch history has %d runs, want 0（連続失敗の数え・--resume の対象選びに入れない）", len(h.Runs))
	}
}

// failed・cancelled は課題を失敗にも再委譲にもせず保留にし、契約の summary を問いへ写す。
func TestPollContractRuns_FailedAndCancelledHoldTheChallengeWithTheSummary(t *testing.T) {
	for _, st := range []ContractState{ContractStateFailed, ContractStateCancelled} {
		t.Run(string(st), func(t *testing.T) {
			f := newContractFixture(t)
			if _, err := f.start(t); err != nil {
				t.Fatal(err)
			}
			f.client.queue = []ContractLaunchOutput{contractOut(st, nil, func(s *ContractStatus) { s.Summary = "要約ABC" })}
			reps := f.poll(t)
			if len(reps) != 1 || !reps[0].Held {
				t.Fatalf("report = %+v, want held", reps)
			}
			d := f.detail(t, f.cid)
			if d.Status != StatusAwaitingHuman {
				t.Fatalf("status = %s, want awaiting_human", d.Status)
			}
			if len(d.Holds) != 1 || !d.Holds[0].Open() || !strings.Contains(d.Holds[0].Question, "要約ABC") {
				t.Fatalf("holds = %+v, want one open hold quoting the summary", d.Holds)
			}
			if state, _, _, ended := f.contractRow(t); state != string(st) || !ended {
				t.Fatalf("contract_run state=%s ended=%v", state, ended)
			}
			if n := f.count(t, `SELECT COUNT(*) FROM occurrence`); n != 0 {
				t.Fatalf("occurrences = %d, want 0", n)
			}
			// 保留にした課題は自動では委譲し直さない。
			var targets []int64
			if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
				targets, _, err = selectDelegationTargets(context.Background(), tx)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if len(targets) != 0 {
				t.Fatalf("targets = %v, want none", targets)
			}
		})
	}
}

// succeeded・running・waiting は保留にしない。
func TestPollContractRuns_OtherStatesDoNotHold(t *testing.T) {
	for _, st := range []ContractState{ContractStateRunning, ContractStateWaiting, ContractStateSucceeded} {
		f := newContractFixture(t)
		if _, err := f.start(t); err != nil {
			t.Fatal(err)
		}
		f.client.queue = []ContractLaunchOutput{contractOut(st, nil, nil)}
		if reps := f.poll(t); len(reps) != 1 || reps[0].Held {
			t.Fatalf("%s: report = %+v, want not held", st, reps)
		}
		if got := f.status(t, f.cid); got != StatusInProgress {
			t.Fatalf("%s: status = %s, want in_progress", st, got)
		}
	}
}

// start の最中に flywheel の run が回収されたら、課題を保留にして、追われていない外部の run が残りうることを問いに書く。
func TestStartContractDelegation_ReclaimedDuringStartHoldsTheChallenge(t *testing.T) {
	f := newContractFixture(t)
	rc := &reclaimingClient{fakeContractClient: f.client}
	rc.onStart = func() {
		err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
			var id int64
			if err := tx.QueryRow(`SELECT id FROM run WHERE ended_at IS NULL`).Scan(&id); err != nil {
				return err
			}
			_, err := updateRunEnd(context.Background(), tx, id, updateRunEndInput{EndedAt: f.s.currentTime(), Result: RunResultInterrupted})
			return err
		})
		if err != nil {
			t.Fatalf("reclaim: %v", err)
		}
	}
	rep, err := f.s.StartContractDelegation(context.Background(), ContractStartInput{
		ChallengeID: f.cid, ConnectorID: "tool", AgentDecl: f.agent, ConnDecl: f.conn, Client: rc,
		CycleID: f.cycle(t, 300), MaxBudgetUSD: 5,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !rep.Held {
		t.Fatalf("report = %+v, want held", rep)
	}
	d := f.detail(t, f.cid)
	if d.Status != StatusAwaitingHuman || len(d.Holds) != 1 {
		t.Fatalf("status=%s holds=%d, want awaiting_human with one hold", d.Status, len(d.Holds))
	}
	q := d.Holds[0].Question
	if !strings.Contains(q, "ext-1") || !strings.Contains(q, "追っていない") {
		t.Fatalf("question = %q, want the external run id and the note about an untracked run", q)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM contract_run`); n != 0 {
		t.Fatalf("contract_run rows = %d, want 0（回収された start は契約の run にしない）", n)
	}
}

// 終わっていない契約の run を持つ課題は、J1・J2・J5 の自動の対象選びから外れる（終われば戻る）。
func TestContractRun_ExcludesTheChallengeFromJudgmentAutoTargets(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil { // 課題 1（着手中）の契約の run を開く
		t.Fatal(err)
	}
	type sel func(context.Context, *sql.Tx) ([]int64, []NotStarted, error)
	cases := []struct {
		name   string
		status Status
		sel    sel
	}{
		{"J1", StatusUnclassified, selectJ1AutoTargets},
		{"J2", StatusClassified, selectJ2AutoTargets},
		{"J5", StatusVerifying, selectJ5AutoTargets},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := f.newInProgress(t, "T-"+c.name, "P2", planSpec(nil))
			cid, _ := parseChallengeID(id)
			if _, err := f.exec(`UPDATE challenge SET status = ? WHERE id = ?`, string(c.status), cid); err != nil {
				t.Fatal(err)
			}
			selected := func() bool {
				var ids []int64
				if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
					ids, _, err = c.sel(context.Background(), tx)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				for _, v := range ids {
					if v == cid {
						return true
					}
				}
				return false
			}
			if !selected() {
				t.Fatalf("%s: the control challenge is not selected without a contract run", c.name)
			}
			if _, err := f.exec(`INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, max_budget_usd, started_at, observed_at)
				VALUES (?, 'tool', ?, 1, 'running', 's', 1000000, 'x', 'x')`, cid, "ext-"+c.name); err != nil {
				t.Fatal(err)
			}
			if selected() {
				t.Fatalf("%s: challenge with an open contract run is still selected", c.name)
			}
			if _, err := f.exec(`UPDATE contract_run SET state = 'succeeded', ended_at = 'x' WHERE challenge_id = ?`, cid); err != nil {
				t.Fatal(err)
			}
			if !selected() {
				t.Fatalf("%s: challenge is not selected again after the contract run ended", c.name)
			}
		})
	}
}

// 1 件の読みが失敗しても、残りの契約の run を読み続ける。
func TestPollContractRuns_ContinuesAfterOneReadFails(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	cid2 := f.newInProgress(t, "T2", "P2", planSpec(nil))
	f.client.startOut = contractOut(ContractStateRunning, nil, func(st *ContractStatus) { st.RunID = "ext-2" })
	if _, err := f.s.StartContractDelegation(context.Background(), ContractStartInput{
		ChallengeID: cid2, ConnectorID: "tool", AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client,
		CycleID: f.cycle(t, 300), MaxBudgetUSD: 5,
	}); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{
		{Result: RunResultErrored, ErrorSummary: "boom"},
		contractOut(ContractStateRunning, float64Ptr(2), func(st *ContractStatus) { st.RunID = "ext-2" }),
	}
	reps := f.poll(t)
	if len(reps) != 2 || reps[0].Result != RunResultErrored || reps[1].Result != RunResultSucceeded || reps[1].CostTotalUSD != 2 {
		t.Fatalf("reports = %+v, want the first read failed and the second read recorded", reps)
	}
}

// 複数のプロセスが同じ契約の run を同時に読んでも、費用を二重に数えない。
func TestPollContractRuns_ConcurrentReadsDoNotDoubleCountCost(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.last = contractOut(ContractStateRunning, float64Ptr(3), nil)
	cycleID := f.cycle(t, 300)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = f.s.PollContractRuns(context.Background(), ContractPollInput{AgentDecl: f.agent, ConnDecl: f.conn, Client: &statusOnlyClient{out: f.client.last}, CycleID: cycleID})
		}()
	}
	wg.Wait()
	if got := f.count(t, `SELECT COALESCE(SUM(cost_usd), 0) FROM run WHERE challenge_id = 1 AND budget_bucket = 'impl'`); got != usdToMicros(3) {
		t.Fatalf("sum of run costs = %d, want %d", got, usdToMicros(3))
	}
	if _, _, cost, _ := f.contractRow(t); cost != usdToMicros(3) {
		t.Fatalf("contract_run.cost_usd = %d, want %d", cost, usdToMicros(3))
	}
}

// statusOnlyClient はゴルーチンから安全に使える、固定の応答を返す ContractClient。
type statusOnlyClient struct{ out ContractLaunchOutput }

func (c *statusOnlyClient) Start(context.Context, ContractLaunchInput) (ContractLaunchOutput, error) {
	return c.out, nil
}

func (c *statusOnlyClient) Status(context.Context, ContractLaunchInput) (ContractLaunchOutput, error) {
	return c.out, nil
}

// 別のプロセスが回収した（run を interrupted で閉じただけの）契約の start も、課題を保留にする。それまでは委譲の対象に戻さない。
func TestHoldUnrecordedContractStops_ReclaimedStartByAnotherProcess(t *testing.T) {
	f := newContractFixture(t)
	sid, _ := newLowercaseUUIDv4()
	runID, err := f.s.beginContractRun(context.Background(), 1, nil, nil, sid, usdToMicros(5), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := updateRunEnd(context.Background(), tx, runID, updateRunEndInput{EndedAt: f.s.currentTime(), Result: RunResultInterrupted})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var targets []int64
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
		targets, _, err = selectDelegationTargets(context.Background(), tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 {
		t.Fatalf("targets = %v, want none（保留にするまで委譲し直さない）", targets)
	}
	n, err := f.s.HoldUnrecordedContractStops(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("held = %d, err = %v, want 1", n, err)
	}
	d := f.detail(t, f.cid)
	if d.Status != StatusAwaitingHuman || len(d.Holds) != 1 || !strings.Contains(d.Holds[0].Question, "追っていない") {
		t.Fatalf("status=%s holds=%+v", d.Status, d.Holds)
	}
	if n, err := f.s.HoldUnrecordedContractStops(context.Background()); err != nil || n != 0 {
		t.Fatalf("second sweep held = %d, err = %v, want 0", n, err)
	}
}

// 契約の run を終わりにしたあと保留を作る前に止まった課題も、次の読みの先頭で保留にする。
func TestHoldUnrecordedContractStops_FailedWithoutHold(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	if _, err := f.exec(`UPDATE contract_run SET state = 'failed', summary = '要約XYZ', ended_at = ?`, formatTimestamp(f.s.currentTime())); err != nil {
		t.Fatal(err)
	}
	f.poll(t)
	d := f.detail(t, f.cid)
	if d.Status != StatusAwaitingHuman || len(d.Holds) != 1 || !strings.Contains(d.Holds[0].Question, "要約XYZ") {
		t.Fatalf("status=%s holds=%+v", d.Status, d.Holds)
	}
}

// 終わっていない契約の run を持つ課題は、J6（学び）の自動の対象選びからも外れ、ID を省略した学びでも
// 判断が起動しない。契約の run が終われば対象に戻り、学びが走る。
func TestContractRun_ExcludesTheChallengeFromJ6AutoTargets(t *testing.T) {
	f := newJ6Fixture(t)
	c := f.challenge(t, StatusAwaitingHuman)
	f.occ(t, c, "plan_rejected", "bad", nil)
	cid, _ := parseChallengeID(c)
	selected := func() bool {
		var ids []int64
		if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
			ids, err = selectJ6AutoTargets(context.Background(), tx)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		for _, v := range ids {
			if v == cid {
				return true
			}
		}
		return false
	}
	if !selected() {
		t.Fatal("the control challenge is not selected without a contract run")
	}

	now := formatTimestamp(time.Now())
	rid := f.exec(t, `INSERT INTO run (challenge_id, kind, challenge_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket)
		VALUES (?, 'contract', 1, 's', 1, 'h', ?, ?, ?, 'succeeded', 1000000, 'impl')`, cid, now, now, now)
	f.exec(t, `INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, max_budget_usd, started_at, observed_at)
		VALUES (?, 'tool', 'ext-j6', ?, 'running', 's', 1000000, 'x', 'x')`, cid, rid)
	if selected() {
		t.Fatal("challenge with an open contract run is still selected for J6")
	}
	if _, err := f.s.LearnJ6(context.Background(), f.input(f.cycle(t, 300), nil)); err != nil {
		t.Fatal(err)
	}
	if len(f.inputs) != 0 {
		t.Fatalf("J6 invoked %d times while the contract run is open, want 0", len(f.inputs))
	}

	f.exec(t, `UPDATE contract_run SET state = 'succeeded', ended_at = 'x' WHERE challenge_id = ?`, cid)
	if !selected() {
		t.Fatal("challenge is not selected for J6 again after the contract run ended")
	}
	if _, err := f.s.LearnJ6(context.Background(), f.input(f.cycle(t, 300), nil)); err != nil {
		t.Fatal(err)
	}
	if len(f.inputs) != 1 {
		t.Fatalf("J6 invoked %d times after the contract run ended, want 1", len(f.inputs))
	}
}
