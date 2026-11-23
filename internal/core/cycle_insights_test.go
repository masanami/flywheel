package core

// 区切り S4d-2 の core の読み取り: サイクルの口（スーパーバイザーの手・費用の内訳。AC-74〜AC-81）と
// 知らせの口（AC-82〜AC-94）を、実ストアで検証する。

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"
)

type insightsFixture struct {
	s   *Store
	cid int64
}

func newInsightsFixture(t *testing.T) *insightsFixture {
	t.Helper()
	s := newStoreForTest(t)
	c, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "課題"})
	if err != nil {
		t.Fatal(err)
	}
	cid, _ := parseChallengeID(c.ID)
	return &insightsFixture{s: s, cid: cid}
}

func (f *insightsFixture) exec(t *testing.T, q string, args ...any) int64 {
	t.Helper()
	var id int64
	err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(q, args...)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return id
}

func (f *insightsFixture) run(t *testing.T, cycle int64, kind, judgment string, ended bool, cost int64) {
	t.Helper()
	var endedAt, result any
	if ended {
		endedAt, result = formatTimestamp(statusBase), "succeeded"
	}
	f.exec(t, `INSERT INTO run (cycle_id, challenge_id, kind, judgment, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket, cost_usd)
		VALUES (?, ?, ?, ?, 1, 'h', ?, ?, ?, ?, 2000000, 'impl', ?)`,
		cycle, f.cid, kind, nullableTextColumn(judgment), formatTimestamp(statusBase), formatTimestamp(statusBase), endedAt, result, cost)
}

func (f *insightsFixture) action(t *testing.T, cycle int64, hand SupervisorHand, note string) {
	t.Helper()
	_, err := f.s.recordSupervisorAction(context.Background(), SupervisorActionInput{
		CycleID: formatCycleID(cycle), ChallengeID: formatChallengeID(f.cid), StopKind: StopKindVerifyUncertain,
		Hand: hand, Findings: note, Note: note, Result: ActionExecuted,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *insightsFixture) insights(t *testing.T) *CycleInsights {
	t.Helper()
	got, err := f.s.ReadCycleInsights(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// AC-81: サイクルを一度も回していないとき、手と内訳は空。
func TestReadCycleInsights_NoCycleIsEmpty(t *testing.T) {
	t.Parallel()
	f := newInsightsFixture(t)
	got := f.insights(t)
	if got.CycleID != "" || got.SupervisorActions == nil || len(got.SupervisorActions) != 0 || got.Costs == nil || len(got.Costs) != 0 || got.CostTotalMicros != 0 {
		t.Errorf("insights = %+v, want empty (non-nil) lists", got)
	}
}

// AC-74: 走っているとき、そのサイクルで打った手だけを新しい順に返す。
func TestReadCycleInsights_RunningCycleActionsNewestFirst(t *testing.T) {
	t.Parallel()
	f := newInsightsFixture(t)
	old := seedCycle(t, f.s, statusBase.Add(-time.Hour), ptrTime(statusBase.Add(-30*time.Minute)), CycleResultCompleted)
	cur := seedCycleTrigger(t, f.s, "manual", statusBase, nil, "")
	seedLock(t, f.s, cur, statusBase)
	f.s.now = func() time.Time { return statusBase.Add(10 * time.Second) }
	// 排他の持ち主より新しい、個別の操作の周（対象にならない）と、持ち主でないふつうの周。
	seedCycleTrigger(t, f.s, "run", statusBase.Add(time.Minute), nil, "")
	f.action(t, old, HandRetry, "old")
	f.action(t, cur, HandRetry, "first")
	f.action(t, cur, HandDefer, "second")
	got := f.insights(t)
	if got.CycleID != formatCycleID(cur) || len(got.SupervisorActions) != 2 || got.SupervisorActions[0].Note != "second" || got.SupervisorActions[1].Note != "first" {
		t.Fatalf("insights = %+v, want the running cycle's two actions, newest first", got)
	}
	a := got.SupervisorActions[0]
	if a.ChallengeID != formatChallengeID(f.cid) || a.Hand == nil || *a.Hand != HandDefer || a.Findings != "second" || a.Result != ActionExecuted {
		t.Errorf("action = %+v", a)
	}
}

// AC-75・AC-79・stale: 走っていないとき（stale を含む）は、個別の操作の周を除いた最後のサイクルの手を返す。
func TestReadCycleInsights_NotRunningUsesLastOrdinaryCycle(t *testing.T) {
	t.Parallel()
	f := newInsightsFixture(t)
	last := seedCycle(t, f.s, statusBase, ptrTime(statusBase.Add(time.Minute)), CycleResultCompleted)
	op := seedCycleTrigger(t, f.s, "run", statusBase.Add(time.Hour), ptrTime(statusBase.Add(2*time.Hour)), CycleResultCompleted)
	f.action(t, last, HandRetry, "last")
	f.action(t, op, HandRetry, "individual")
	if got := f.insights(t); got.CycleID != formatCycleID(last) || len(got.SupervisorActions) != 1 || got.SupervisorActions[0].Note != "last" {
		t.Errorf("idle insights = %+v, want the last ordinary cycle (%d)", got, last)
	}
	// 走っていない扱い（stale）でも、対象は最後のサイクル。
	staleHolder := seedCycleTrigger(t, f.s, "manual", statusBase.Add(3*time.Hour), nil, "")
	newest := seedCycleTrigger(t, f.s, "manual", statusBase.Add(4*time.Hour), nil, "")
	seedLock(t, f.s, staleHolder, statusBase)
	f.s.now = func() time.Time { return statusBase.Add(24 * time.Hour) }
	if got := f.insights(t); got.CycleID != formatCycleID(newest) {
		t.Errorf("stale insights cycle = %s, want the last ordinary cycle %s", got.CycleID, formatCycleID(newest))
	}
}

// AC-76・AC-77・AC-78: 終わった実行だけを種類（判断点は役割）で足し、合計はサイクルの費用と等しい。
func TestReadCycleInsights_CostsByKindEndedOnly(t *testing.T) {
	t.Parallel()
	f := newInsightsFixture(t)
	other := seedCycle(t, f.s, statusBase.Add(-time.Hour), ptrTime(statusBase.Add(-30*time.Minute)), CycleResultCompleted)
	cyc := seedCycle(t, f.s, statusBase, ptrTime(statusBase.Add(time.Minute)), CycleResultCompleted)
	f.run(t, cyc, "judgment", "J2", true, 333333)
	f.run(t, cyc, "judgment", "J2", true, 111111)
	f.run(t, cyc, "judgment", "J5", true, 20000)
	f.run(t, cyc, "delegate", "", true, 1000001)
	f.run(t, cyc, "contract", "", false, 9000000)  // 終わっていない
	f.run(t, other, "delegate", "", true, 5000000) // 別のサイクル
	got := f.insights(t)
	type key struct{ kind, judgment string }
	want := map[key]int64{{"contract", ""}: 0, {"delegate", ""}: 1000001, {"judgment", "J2"}: 444444, {"judgment", "J5"}: 20000}
	delete(want, key{"contract", ""})
	if len(got.Costs) != len(want) {
		t.Fatalf("costs = %+v, want %d lines (unfinished runs excluded)", got.Costs, len(want))
	}
	var sum int64
	for _, c := range got.Costs {
		if w, ok := want[key{string(c.Kind), string(c.Judgment)}]; !ok || w != c.SpentMicros || c.SpentUSD != microsToUSD(w) {
			t.Errorf("line = %+v, want %v", c, want)
		}
		sum += c.SpentMicros
	}
	var spent int64
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
		spent, err = cycleSpentMicros(context.Background(), tx, cyc)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if sum != spent || got.CostTotalMicros != spent || spent != 1464445 {
		t.Errorf("sum = %d, total = %d, cycle cost = %d, want all 1464445", sum, got.CostTotalMicros, spent)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func (f *insightsFixture) notices(t *testing.T) []Notice {
	t.Helper()
	got, err := f.s.ListNotices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (f *insightsFixture) activity(t *testing.T, at time.Time, entity string, entityID int64, action, after string) {
	t.Helper()
	f.exec(t, `INSERT INTO activity (at, actor, channel, verification, entity, entity_id, action, after) VALUES (?, 'a', 'cli', 'none', ?, ?, ?, ?)`,
		formatTimestamp(at), entity, entityID, action, after)
}

func (f *insightsFixture) addition(t *testing.T) int64 {
	t.Helper()
	return f.exec(t, `INSERT INTO task_addition (challenge_id, origin, state, created_at) VALUES (?, 'expand', 'applied', ?)`, f.cid, formatTimestamp(statusBase))
}

func (f *insightsFixture) operation(t *testing.T) int64 {
	t.Helper()
	return f.exec(t, `INSERT INTO operation (challenge_id, kind, summary, state, created_at) VALUES (?, 'release', 's', 'executed', ?)`, f.cid, formatTimestamp(statusBase))
}

// AC-82: タスクを足した記録は task_added（課題・足した件数・出どころつき）。
func TestListNotices_TaskAdded(t *testing.T) {
	t.Parallel()
	f := newInsightsFixture(t)
	at := statusBase.Add(time.Minute)
	f.activity(t, at, "task_addition", f.addition(t), "add_tasks", `{"task_ids":["T-3","T-4"]}`)
	f.activity(t, at.Add(time.Hour), "task_addition", 1, "state", `{"state":"applied"}`) // 足した出来事ではない
	got := f.notices(t)
	if len(got) != 1 || got[0].Kind != NoticeTaskAdded || !got[0].At.Equal(at) || got[0].ChallengeID != formatChallengeID(f.cid) || got[0].AddedCount != 2 || got[0].Origin != TaskAdditionOriginExpand {
		t.Errorf("notices = %+v", got)
	}
}

// AC-83・AC-84: 後の状態が実行済みの op_execute だけが released。取り消しの記録は含めない。
func TestListNotices_ReleasedOnlyWhenExecuted(t *testing.T) {
	t.Parallel()
	f := newInsightsFixture(t)
	op := f.operation(t)
	at := statusBase.Add(time.Minute)
	f.activity(t, at, "operation", op, ActivityActionOpExecute, `{"state":"pending"}`)
	f.activity(t, at.Add(time.Second), "challenge", f.cid, ActivityActionOpExecute, `{"operation_id":"OP-1"}`)
	if got := f.notices(t); len(got) != 0 {
		t.Fatalf("notices = %+v, want none for a cancelled execution", got)
	}
	f.activity(t, at.Add(time.Minute), "operation", op, ActivityActionOpExecute, `{"state":"executed"}`)
	got := f.notices(t)
	if len(got) != 1 || got[0].Kind != NoticeReleased || got[0].ChallengeID != formatChallengeID(f.cid) || !got[0].At.Equal(at.Add(time.Minute)) {
		t.Errorf("notices = %+v, want one released", got)
	}
}

// AC-85〜AC-87: 終わった理由つきの終わったサイクルだけが cycle_ended。個別の操作の周・理由が空の周は含めない。
func TestListNotices_CycleEnded(t *testing.T) {
	t.Parallel()
	f := newInsightsFixture(t)
	end := statusBase.Add(time.Minute)
	a := seedCycle(t, f.s, statusBase, &end, CycleResultCompleted)
	b := seedCycleTrigger(t, f.s, "run", statusBase, &end, CycleResultCompleted)
	c := seedCycle(t, f.s, statusBase, &end, CycleResultCompleted)
	seedCycle(t, f.s, statusBase, nil, "") // 終わっていない
	f.exec(t, `UPDATE cycle SET end_reason = 'budget' WHERE id = ?`, a)
	f.exec(t, `UPDATE cycle SET end_reason = 'settled' WHERE id = ?`, b)
	f.exec(t, `UPDATE cycle SET end_reason = NULL WHERE id = ?`, c)
	got := f.notices(t)
	if len(got) != 1 || got[0].Kind != NoticeCycleEnded || got[0].EndReason != CycleEndBudget || got[0].CycleID != formatCycleID(a) || !got[0].At.Equal(end) {
		t.Errorf("notices = %+v, want only the ordinary cycle with a reason", got)
	}
}

// AC-88・AC-89: 印が escalated の保留だけが supervisor_escalated。時刻は対処の記録の最新、無ければ保留が立った時刻。
func TestListNotices_SupervisorEscalated(t *testing.T) {
	t.Parallel()
	f := newInsightsFixture(t)
	raised := statusBase
	taskID := f.exec(t, `INSERT INTO task (challenge_id, plan_version, key, title, body, done_criteria, repo, operation, status, created_at, updated_at)
		VALUES (?, 1, 'k', 't', 'b', 'd', 'r', 'o', 'in_progress', ?, ?)`, f.cid, formatTimestamp(raised), formatTimestamp(raised))
	h1 := f.exec(t, `INSERT INTO hold (challenge_id, question, from_status, raised_at, task_id, supervisor_state) VALUES (?, 'q', 'in_progress', ?, ?, 'escalated')`, f.cid, formatTimestamp(raised), taskID)
	// 印が空・handled の保留（開いている保留は範囲ごとに 1 つなので、別の課題に立てる）。
	for i, state := range []any{nil, "handled"} {
		c, err := f.s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: fmt.Sprintf("他%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		other, _ := parseChallengeID(c.ID)
		f.exec(t, `INSERT INTO hold (challenge_id, question, from_status, raised_at, supervisor_state) VALUES (?, 'q', 'in_progress', ?, ?)`, other, formatTimestamp(raised), state)
	}
	got := f.notices(t)
	if len(got) != 1 || got[0].Kind != NoticeSupervisorEscalated || got[0].TaskID != formatTaskID(taskID) || got[0].ChallengeID != formatChallengeID(f.cid) || !got[0].At.Equal(raised) {
		t.Fatalf("notices = %+v, want one escalated at the raised time", got)
	}
	cyc := seedCycle(t, f.s, statusBase, nil, "")
	later := statusBase.Add(time.Hour)
	f.s.now = func() time.Time { return later }
	hid := h1
	if _, err := f.s.recordSupervisorAction(context.Background(), SupervisorActionInput{
		CycleID: formatCycleID(cyc), ChallengeID: formatChallengeID(f.cid), HoldID: &hid, StopKind: StopKindVerifyUncertain, Hand: HandEscalate, Findings: "f", Note: "n", Result: ActionExecuted,
	}); err != nil {
		t.Fatal(err)
	}
	if got := f.notices(t); len(got) != 1 || !got[0].At.Equal(later) {
		t.Errorf("notices = %+v, want the time of the latest action", got)
	}
}

// AC-90・AC-91: 新しい順で、21 件以上あっても新しい 20 件だけ。
func TestListNotices_NewestFirstAtMost20(t *testing.T) {
	t.Parallel()
	f := newInsightsFixture(t)
	ad := f.addition(t)
	for i := 0; i < 25; i++ {
		f.activity(t, statusBase.Add(time.Duration(i)*time.Minute), "task_addition", ad, "add_tasks", fmt.Sprintf(`{"task_ids":["T-%d"]}`, i))
	}
	end := statusBase.Add(100 * time.Minute)
	cyc := seedCycle(t, f.s, statusBase, &end, CycleResultCompleted)
	f.exec(t, `UPDATE cycle SET end_reason = 'settled' WHERE id = ?`, cyc)
	got := f.notices(t)
	if len(got) != 20 || got[0].Kind != NoticeCycleEnded {
		t.Fatalf("got = %+v, want 20 with the newest first", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i].At.After(got[i-1].At) {
			t.Fatalf("notices not newest first at %d: %v after %v", i, got[i].At, got[i-1].At)
		}
	}
	if !got[19].At.Equal(statusBase.Add(6 * time.Minute)) {
		t.Errorf("oldest returned = %v, want the 20th newest", got[19].At)
	}
}

// AC-92・AC-93・AC-94: 読んでもストアは変わらず、会話・発言の行は増えず、対話の欄への発言に知らせの文は入らない。
func TestListNotices_ReadOnlyAndNotInChat(t *testing.T) {
	t.Parallel()
	f := newInsightsFixture(t)
	f.activity(t, statusBase, "task_addition", f.addition(t), "add_tasks", `{"task_ids":["T-1"]}`)
	counts := func() string {
		var s string
		for _, tbl := range []string{"activity", "chat_session", "chat_message", "cycle", "hold", "run", "supervisor_action"} {
			s += fmt.Sprintf("%s=%d ", tbl, chatCount(t, f.s, `SELECT COUNT(*) FROM `+tbl))
		}
		return s
	}
	before := counts()
	f.notices(t)
	f.insights(t)
	if after := counts(); after != before {
		t.Errorf("tables changed by reading: %s -> %s", before, after)
	}
	mustPost(t, f.s, "状況を教えて", nil)
	chat, err := f.s.ReadChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(chat.Messages) != 2 || chat.Messages[0].Body != "状況を教えて" {
		t.Errorf("chat messages = %+v, want only the owner's message and its reply row", chat.Messages)
	}
	if n := len(f.notices(t)); n != 1 {
		t.Errorf("notices after chat = %d, want 1", n)
	}
}

// 排他の持ち主が個別の操作の周のときは、その前のふつうのサイクルが対象になる（AC-79）。
func TestReadCycleInsights_RunningIndividualOpHolderIsSkipped(t *testing.T) {
	t.Parallel()
	f := newInsightsFixture(t)
	ordinary := seedCycle(t, f.s, statusBase.Add(-time.Hour), ptrTime(statusBase.Add(-30*time.Minute)), CycleResultCompleted)
	op := seedCycleTrigger(t, f.s, "run", statusBase, nil, "")
	seedLock(t, f.s, op, statusBase)
	f.s.now = func() time.Time { return statusBase.Add(10 * time.Second) }
	f.action(t, ordinary, HandRetry, "ordinary")
	f.action(t, op, HandRetry, "individual")
	if got := f.insights(t); got.CycleID != formatCycleID(ordinary) || len(got.SupervisorActions) != 1 || got.SupervisorActions[0].Note != "ordinary" {
		t.Errorf("insights = %+v, want the ordinary cycle", got)
	}
}
