package core

import (
	"context"
	"database/sql"
	"testing"
)

// このファイルは手 defer（見送り・連続の数え・3 サイクル続いたときの上げ方）と、オーナーの割り込み（判断の間の回答）と、
// 見送りを回りの終わりの数えに入れないことを、実ストアと偽の判断の IF で検証する。

type deferRow struct {
	cycle      sql.NullInt64
	count      int
	state      string
	answer     sql.NullString
	answeredBy sql.NullString
}

func (f *svFixture) deferRow(t *testing.T, id string) deferRow {
	t.Helper()
	cid, _ := parseChallengeID(id)
	var r deferRow
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT deferred_cycle_id, deferred_count, COALESCE(supervisor_state, ''), answer, answered_by FROM hold WHERE challenge_id = ?`, cid).
			Scan(&r.cycle, &r.count, &r.state, &r.answer, &r.answeredBy)
	}); err != nil {
		t.Fatal(err)
	}
	return r
}

func (f *svFixture) nextCycle(t *testing.T) {
	t.Helper()
	if _, err := f.s.EndCycle(context.Background(), f.cycle, CycleResultCompleted); err != nil {
		t.Fatal(err)
	}
	f.cycle = f.j6Fixture.cycle(t, 100)
}

func (f *svFixture) roundWork(t *testing.T) int {
	t.Helper()
	w, err := f.s.countRoundWork(context.Background(), f.agent, nil, cycleDBIDOf(f.cycle))
	if err != nil {
		t.Fatal(err)
	}
	return w.Stages.Supervise
}

// defer: 保留はスーパーバイザー待ちのまま、見送ったサイクルと連続の数 1 が残る。同じサイクルでは再び判断点を呼ばない。
func TestSuperviseStage_Defer_LeavesTheHoldPendingAndSkipsItInTheSameCycle(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindUpstreamUnreadable)
	f.inv.result = svResult(map[string]any{"hand": "defer", "note": "次のサイクルで見る"})
	res := f.stage(t)
	if res.SuperviseProcessed != 1 || res.SuperviseHands[HandDefer] != 1 {
		t.Fatalf("processed = %d, hands = %v, want 1 and defer:1", res.SuperviseProcessed, res.SuperviseHands)
	}
	r := f.deferRow(t, id)
	if r.state != "pending" || r.count != 1 || !r.cycle.Valid || r.cycle.Int64 != cycleDBIDOf(f.cycle) {
		t.Fatalf("hold = %+v, want pending, count 1, deferred in this cycle", r)
	}
	if as := f.actions(t, id); len(as) != 1 || as[0].Result != ActionExecuted || *as[0].Hand != HandDefer {
		t.Fatalf("actions = %+v, want one executed defer", as)
	}
	res = f.stage(t)
	if len(f.inputs) != 1 || res.SuperviseProcessed != 0 {
		t.Errorf("calls = %d, processed = %d, want 1, 0 (deferred in this cycle is not processed again)", len(f.inputs), res.SuperviseProcessed)
	}
	if r := f.deferRow(t, id); r.count != 1 {
		t.Errorf("count = %d, want 1 (not counted twice in a cycle)", r.count)
	}
}

// 見送りは、そのサイクルの回りの終わりの数に入れない。サイクルの外（周の ID 無し）の数えには入る。
func TestCountRoundWork_DeferredInThisCycleIsNotCounted(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	f.pendingHold(t, StopKindUpstreamUnreadable)
	if got := f.roundWork(t); got != 1 {
		t.Fatalf("before = %d, want 1", got)
	}
	f.inv.result = svResult(map[string]any{"hand": "defer", "note": "次のサイクルで見る"})
	f.stage(t)
	if got := f.roundWork(t); got != 0 {
		t.Errorf("after defer, in the cycle = %d, want 0", got)
	}
	w, err := f.s.CountRoundWork(context.Background(), f.agent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if w.Stages.Supervise != 1 {
		t.Errorf("outside the cycle = %d, want 1", w.Stages.Supervise)
	}
	f.nextCycle(t)
	if got := f.roundWork(t); got != 1 {
		t.Errorf("next cycle = %d, want 1", got)
	}
}

// defer が 3 サイクル続いたら、次のサイクルでは判断点を呼ばず、保留を escalated にする。処理した件数には入れない。
func TestSuperviseStage_ThreeDefersEscalateWithoutCallingTheJudgment(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindUpstreamUnreadable)
	f.inv.result = svResult(map[string]any{"hand": "defer", "note": "次のサイクルで見る"})
	for i := 1; i <= 3; i++ {
		res := f.stage(t)
		if res.SuperviseProcessed != 1 {
			t.Fatalf("cycle %d processed = %d, want 1", i, res.SuperviseProcessed)
		}
		if r := f.deferRow(t, id); r.count != i || r.state != "pending" {
			t.Fatalf("cycle %d hold = %+v, want count %d pending", i, r, i)
		}
		f.nextCycle(t)
	}
	res := f.stage(t)
	if len(f.inputs) != 3 || res.SuperviseProcessed != 0 || res.SuperviseHands[HandEscalate] != 1 {
		t.Errorf("calls = %d, processed = %d, hands = %v, want 3, 0, escalate:1", len(f.inputs), res.SuperviseProcessed, res.SuperviseHands)
	}
	if r := f.deferRow(t, id); r.state != "escalated" {
		t.Errorf("state = %q, want escalated", r.state)
	}
	if hr := f.holdRow(t, id); !hr.actionID.Valid {
		t.Errorf("supervisor_action_id is not set: %+v", hr)
	}
	as := f.actions(t, id)
	if last := as[len(as)-1]; last.Result != ActionExecuted || *last.Hand != HandEscalate || last.RunID != nil {
		t.Errorf("last action = %+v, want executed escalate without a run", last)
	}
}

// 判断点が走っている間にオーナーが回答したら、結果を捨てる（手は実行しない・保留と課題はオーナーの回答のまま・上げ直さない）。
func TestSuperviseStage_OwnerAnsweredDuringTheJudgment_DiscardsTheResult(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindUpstreamUnreadable)
	f.inv.result = svResult(nil) // escalate
	var statusAfterAnswer Status
	prev := f.inv.onInvoke
	f.inv.onInvoke = func(in JudgmentLaunchInput) {
		prev(in)
		answerHoldForTest(t, f.s, id, "オーナーの回答")
		ch, err := f.s.GetChallenge(context.Background(), id)
		if err != nil {
			t.Error(err)
			return
		}
		statusAfterAnswer = ch.Status
	}
	res := f.stage(t)
	if res.SuperviseProcessed != 1 || len(res.SuperviseHands) != 0 {
		t.Errorf("processed = %d, hands = %v, want 1 and none", res.SuperviseProcessed, res.SuperviseHands)
	}
	r := f.deferRow(t, id)
	if r.state == "escalated" || !r.answer.Valid || r.answer.String != "オーナーの回答" || !r.answeredBy.Valid || r.answeredBy.String == supervisorActorName {
		t.Errorf("hold = %+v, want answered by the owner and not escalated", r)
	}
	if hr := f.holdRow(t, id); hr.findings.Valid || hr.recommendation.Valid || hr.actionID.Valid {
		t.Errorf("hold supervisor columns = %+v, want untouched", hr)
	}
	ch, err := f.s.GetChallenge(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Status != statusAfterAnswer || len(ch.Holds) != 1 {
		t.Errorf("status = %s (want %s), holds = %d (want 1)", ch.Status, statusAfterAnswer, len(ch.Holds))
	}
	as := f.actions(t, id)
	if len(as) != 1 || as[0].Result != ActionRejected || as[0].RejectReason == nil || *as[0].RejectReason != RejectOwnerAnswered {
		t.Fatalf("actions = %+v, want one rejected owner_answered", as)
	}
	if n, _ := f.superviseActivities(t, id); n != 1 {
		t.Errorf("supervise activities = %d, want 1 (the rejected record only)", n)
	}
}

// 回答済み（オーナー）の保留は、次のスーパーバイザーの段で処理しない。
func TestSuperviseStage_OwnerAnsweredHoldIsNotProcessed(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindUpstreamUnreadable)
	answerHoldForTest(t, f.s, id, "先に回答")
	res := f.stage(t)
	if len(f.inputs) != 0 || res.SuperviseProcessed != 0 {
		t.Errorf("calls = %d, processed = %d, want 0, 0", len(f.inputs), res.SuperviseProcessed)
	}
}

// 出力に不備があり、しかも判断の間にオーナーが回答していたら、失敗として上げず、rejected（owner_answered）を残す。
func TestSuperviseStage_OwnerAnsweredDuringTheJudgmentWithBadOutput_IsRejectedAsOwnerAnswered(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindUpstreamUnreadable)
	f.inv.result = svResult(map[string]any{"hand": "defer", "note": ""}) // defer は note が要る（不備）
	prev := f.inv.onInvoke
	f.inv.onInvoke = func(in JudgmentLaunchInput) {
		prev(in)
		answerHoldForTest(t, f.s, id, "オーナーの回答")
	}
	f.stage(t)
	as := f.actions(t, id)
	if len(as) != 1 || as[0].Result != ActionRejected || as[0].RejectReason == nil || *as[0].RejectReason != RejectOwnerAnswered {
		t.Fatalf("actions = %+v, want one rejected owner_answered", as)
	}
	if r := f.deferRow(t, id); r.state == "escalated" || r.answer.String != "オーナーの回答" {
		t.Errorf("hold = %+v, want the owner's answer and not escalated", r)
	}
}
