package core

import (
	"context"
	"database/sql"
	"testing"
)

// 区切り S4c-1 の 6: 閉じていない足す依頼（仕様待ち・オーナー待ち）と、足す依頼がまだ無い済みの expand_after のタスクを、
// 上げる依頼を始める条件・検証中へ進める条件・「いま始められるもの」の数えが読む（AC-173〜179・AC-211〜216）。実ストアで確かめる。

// markExpandAfter は課題の、キーが key のタスクに、続きを決める印（expand_after）を付ける。
func (f *contractFixture) markExpandAfter(t *testing.T, cid int64, key string) int64 {
	t.Helper()
	f.sqlExec(t, `UPDATE task SET expand_after = 1 WHERE challenge_id = ? AND key = ?`, cid, key)
	var tid int64
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT id FROM task WHERE challenge_id = ? AND key = ?`, cid, key).Scan(&tid)
	})
	return tid
}

// addExpandAddition は、出どころのタスク tid の足す依頼（expand）を作り、状態を to にする（pending のままなら動かさない）。
func (f *contractFixture) addExpandAddition(t *testing.T, challengeID string, tid int64, to TaskAdditionState) {
	t.Helper()
	ta, err := f.s.CreateTaskAddition(context.Background(), ChannelInvoker, challengeID,
		TaskAdditionInput{Origin: TaskAdditionOriginExpand, OriginTaskID: formatTaskID(tid)})
	if err != nil {
		t.Fatalf("CreateTaskAddition: %v", err)
	}
	if to == TaskAdditionPending {
		return
	}
	if to == TaskAdditionAwaitingOwner {
		f.setAdditionAwaitingOwner(t, ta.ID)
		return
	}
	if _, err := f.s.SetTaskAdditionState(context.Background(), ChannelInvoker, ta.ID, to, TaskAdditionUpdate{}); err != nil {
		t.Fatalf("SetTaskAdditionState(%s): %v", to, err)
	}
}

// setAdditionAwaitingOwner は足す依頼 id の状態を、オーナー待ちに直接置く。オーナー待ちは同じ課題の保留を指す決まり
// （setTaskAdditionStateTx の検査）だが、ここの試験は状態だけを見るので、保留を作らずに行を書く。
func (f *contractFixture) setAdditionAwaitingOwner(t *testing.T, id string) {
	t.Helper()
	placeAdditionAwaitingOwner(t, f.s, id)
}

func placeAdditionAwaitingOwner(t *testing.T, s *Store, id string) {
	t.Helper()
	aid, ok := parseTaskAdditionID(id)
	if !ok {
		t.Fatalf("task addition id %q", id)
	}
	writeForTest(t, s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task_addition SET state = ? WHERE id = ?`, string(TaskAdditionAwaitingOwner), aid)
		return err
	})
}

func (f *contractFixture) reflectIDs(t *testing.T) []int64 {
	t.Helper()
	ids, err := f.s.reflectCandidates(context.Background(), nil, f.agent)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// AC-173: 済みの expand_after のタスクがあり、足す依頼がまだ無い課題は、未着手・着手中・検証中のタスクが 0 個でも、上げる依頼を始めない。
func TestOpenAdditions_AC173_DoneExpandTaskWithoutAnAdditionDoesNotStartTheUpRequest(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, upConnectorsJSON(), multiTask{key: "a"}, multiTask{key: "b"})
	cid, _ := parseChallengeID(id)
	if got := f.reflectIDs(t); len(got) != 1 {
		t.Fatalf("precondition: candidates = %v, want 1", got)
	}
	f.markExpandAfter(t, cid, "a")
	if got := f.reflectIDs(t); len(got) != 0 {
		t.Fatalf("done expand_after task without an addition: candidates = %v, want none", got)
	}
}

// 足せない課題（見込みが不明）は、足す依頼がまだ無くても待たない（足す依頼を作らない課題を、永久に止めない）。
func TestOpenAdditions_UnaddableChallengeDoesNotWaitForAnAdditionThatIsNeverMade(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, upConnectorsJSON(), multiTask{key: "a"})
	cid, _ := parseChallengeID(id)
	f.markExpandAfter(t, cid, "a")
	if got := f.reflectIDs(t); len(got) != 0 {
		t.Fatalf("control: with a known estimate, candidates = %v, want none", got)
	}
	f.sqlExec(t, `UPDATE task SET budget_impl_micros = NULL, budget_review_micros = NULL WHERE challenge_id = ?`, cid)
	f.sqlExec(t, `UPDATE task_plan SET spec = NULL WHERE challenge_id = ?`, cid)
	readForTest(t, f.s, func(tx *sql.Tx) error {
		ok, err := expandAddable(context.Background(), tx, cid, f.agent)
		if ok {
			t.Fatalf("precondition: expandAddable = true, want false (%v)", err)
		}
		return err
	})
	if got := f.reflectIDs(t); len(got) != 1 {
		t.Fatalf("unaddable challenge: candidates = %v, want 1", got)
	}
}

// AC-174・AC-175・AC-176: 足す依頼が pending・awaiting_owner の課題は上げる依頼を始めず、none で閉じた課題は始める。
func TestOpenAdditions_AC174to176_OnlyAnOpenAdditionStopsTheUpRequest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		state     TaskAdditionState
		wantStart bool
	}{
		{TaskAdditionPending, false},       // AC-174
		{TaskAdditionAwaitingOwner, false}, // AC-175
		{TaskAdditionNone, true},           // AC-176
		{TaskAdditionApplied, true},
		{TaskAdditionDeclined, true},
	}
	for _, c := range cases {
		t.Run(string(c.state), func(t *testing.T) {
			t.Parallel()
			f, id := newUpFixture(t, upConnectorsJSON(), multiTask{key: "a"})
			cid, _ := parseChallengeID(id)
			tid := f.markExpandAfter(t, cid, "a")
			f.addExpandAddition(t, id, tid, c.state)
			if got := f.reflectIDs(t); (len(got) == 1) != c.wantStart {
				t.Fatalf("addition %s: candidates = %v, want start=%v", c.state, got, c.wantStart)
			}
		})
	}
}

// AC-177・AC-178・AC-179: 足す依頼が pending・awaiting_owner の課題、足す依頼がまだ無い課題は、レビュー待ちを読んでも検証中へ進まない
// （反映の操作も作らない）。足す依頼が none で閉じた後の読みは進む。
func TestOpenAdditions_AC177to179_TheReflectReviewDoesNotSubmitWhileAdditionsAreOpen(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(f *contractFixture, id string, cid, tid int64)
	}{
		{"pending", func(f *contractFixture, id string, _, tid int64) {
			f.addExpandAddition(t, id, tid, TaskAdditionPending)
		}},
		{"awaiting_owner", func(f *contractFixture, id string, _, tid int64) {
			f.addExpandAddition(t, id, tid, TaskAdditionAwaitingOwner)
		}},
		{"no addition yet", func(*contractFixture, string, int64, int64) {}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f, id := newUpFixture(t, upConnectorsJSON())
			cid, _ := parseChallengeID(id)
			f.setUpPR("main")
			f.startUp(t) // 上げる依頼を始める（この時点では印も足す依頼も無い）
			tid := f.markExpandAfter(t, cid, "a")
			c.setup(f, id, cid, tid)
			f.readWith(t, upReviewOut([]string{"respond", "land"}, upArtsOK...))
			if got := f.statusOf(t, id); got != StatusInProgress {
				t.Fatalf("status = %s, want in_progress (not verifying)", got)
			}
			if got := f.releases(t); len(got) != 0 {
				t.Fatalf("releases = %+v, want none", got)
			}
			if _, _, opID, ended := f.reflectRow(t); opID.Valid || ended {
				t.Fatalf("operation_id = %+v, ended = %v, want the run left open without an operation", opID, ended)
			}
			// 足す依頼が「足さない」で閉じたら、同じレビュー待ちの次の読みが検証中へ進める。
			f.sqlExec(t, `DELETE FROM task_addition WHERE challenge_id = ?`, cid)
			f.addExpandAddition(t, id, tid, TaskAdditionNone)
			f.readWith(t, upReviewOut([]string{"respond", "land"}, upArtsOK...))
			if got := f.statusOf(t, id); got != StatusVerifying {
				t.Fatalf("after the addition closed: status = %s, want verifying", got)
			}
		})
	}
}

// AC-211・AC-212: 着手中の課題に pending の足す依頼があれば、同じ課題の別のタスクが着手中でも、「いま始められるもの」に数える。
func TestOpenAdditions_AC211and212_PendingAdditionIsStartable(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	f.setTaskStatus(t, "a", TaskStatusDone)
	f.setTaskStatus(t, "b", TaskStatusVerifying) // 進められるタスクは無い
	f.expandAdditionsOnly(t)
	w := f.roundWork(t)
	if w.Stages.Plan != 1 || w.Startable < 1 {
		t.Fatalf("pending addition: plan = %d, startable = %d, want 1 and >= 1", w.Stages.Plan, w.Startable)
	}
	f.setTaskStatus(t, "b", TaskStatusInProgress)
	if w := f.roundWork(t); w.Stages.Plan != 1 {
		t.Fatalf("another task in progress: plan = %d, want the addition counted", w.Stages.Plan)
	}
}

// AC-213: 足せる着手中の課題の、済みの expand_after のタスクで足す依頼がまだ無いものは、「いま始められるもの」に数える。
func TestOpenAdditions_AC213_DoneExpandTaskWithoutAnAdditionIsStartable(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	f.setTaskStatus(t, "b", TaskStatusVerifying)
	if w := f.roundWork(t); w.Stages.Plan != 0 {
		t.Fatalf("before the task is done: plan = %d, want 0", w.Stages.Plan)
	}
	f.setTaskStatus(t, "a", TaskStatusDone)
	w := f.roundWork(t)
	if w.Stages.Plan != 1 || w.Startable < 1 {
		t.Fatalf("done expand task without an addition: plan = %d, startable = %d, want 1 and >= 1", w.Stages.Plan, w.Startable)
	}
	// 数えは足す依頼を作らない（読み取りだけ）。
	if adds := f.additions(t); len(adds) != 0 {
		t.Fatalf("additions = %+v, want none (counting only reads)", adds)
	}
	// 足す依頼の 1 件と、足す依頼がまだ無いタスクが同じ課題にあっても、課題 1 件と数える。
	f.expandAdditionsOnly(t)
	if w := f.roundWork(t); w.Stages.Plan != 1 {
		t.Fatalf("pending addition: plan = %d, want 1", w.Stages.Plan)
	}
}

// AC-214: awaiting_owner の足す依頼だけがあるとき、数えない。
func TestOpenAdditions_AC214_AwaitingOwnerAdditionIsNotStartable(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	f.setTaskStatus(t, "a", TaskStatusDone)
	f.setTaskStatus(t, "b", TaskStatusVerifying)
	f.expandAdditionsOnly(t)
	adds := f.additions(t)
	if len(adds) != 1 {
		t.Fatalf("additions = %+v", adds)
	}
	placeAdditionAwaitingOwner(t, f.s, adds[0].ID)
	if w := f.roundWork(t); w.Stages.Plan != 0 || w.Startable != 0 {
		t.Fatalf("awaiting_owner: plan = %d, startable = %d, want 0", w.Stages.Plan, w.Startable)
	}
}

// AC-215: 保留中の課題の pending の足す依頼だけがあるとき、数えない（足す依頼がまだ無いタスクも数えない）。
func TestOpenAdditions_AC215_HeldChallengeAdditionIsNotStartable(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	f.setTaskStatus(t, "a", TaskStatusDone)
	f.setTaskStatus(t, "b", TaskStatusVerifying)
	f.expandAdditionsOnly(t)
	f.raiseHold(t, StatusInProgress)
	if w := f.roundWork(t); w.Stages.Plan != 0 || w.Startable != 0 {
		t.Fatalf("held: plan = %d, startable = %d, want 0", w.Stages.Plan, w.Startable)
	}
}

// AC-216: 見込みが不明の課題の pending の足す依頼だけがあるとき、数えない。
func TestOpenAdditions_AC216_UnknownEstimateAdditionIsNotStartable(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	f.setTaskStatus(t, "a", TaskStatusDone)
	f.setTaskStatus(t, "b", TaskStatusVerifying)
	f.expandAdditionsOnly(t)
	f.makeEstimateUnknown(t)
	if w := f.roundWork(t); w.Stages.Plan != 0 || w.Startable != 0 {
		t.Fatalf("unknown estimate: plan = %d, startable = %d, want 0", w.Stages.Plan, w.Startable)
	}
}

// サイクルを始めるかの数え（CountCycleWork）も、計画の後に足す処理を計画の段の仕事として数える。
func TestOpenAdditions_CycleWorkCountsThePlanPhaseExpansion(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	f.setTaskStatus(t, "a", TaskStatusDone)
	w, err := f.s.CountCycleWork(context.Background(), f.agent, f.conn)
	if err != nil {
		t.Fatal(err)
	}
	if w.Stages.Plan != 1 {
		t.Fatalf("plan = %d, want 1", w.Stages.Plan)
	}
}

func (f *paFixture) roundWork(t *testing.T) *RoundWork {
	t.Helper()
	w, err := f.s.CountRoundWork(context.Background(), f.agent, f.conn)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// expandAdditionsOnly は足す依頼を作るだけで、判断点は呼ばない（足す依頼は pending のまま残る）。
func (f *paFixture) expandAdditionsOnly(t *testing.T) {
	t.Helper()
	if _, err := f.s.createExpandAdditions(context.Background(), f.agent); err != nil {
		t.Fatal(err)
	}
}
