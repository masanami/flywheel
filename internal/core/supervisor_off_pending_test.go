package core

import (
	"context"
	"testing"
	"time"
)

// 宣言の supervisor が偽の間は、スーパーバイザー待ち（pending）のまま残った保留を、ふつうのオーナーの回答待ちとして扱う。
// 保留の行は書き換えず、宣言を真に戻せばまたスーパーバイザー待ちとして数える。
func TestSupervisorOff_PendingHoldIsAnOrdinaryOwnerQuestion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newJ5Fixture(t, "impl")
	id, err := holdViaEntrance(t, f.s, StatusVerifying, HoldKindHuman, HoldOptionsInput{StopKind: StopKindVerifyUncertain, SupervisorEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	off := *f.agent
	off.Supervisor = false
	on := *f.agent
	on.Supervisor = true

	// 真のとき（変えない）: 数えに入り、確認中に出る。
	w, err := f.s.CountCycleWork(ctx, &on, f.conn)
	if err != nil || w.Stages.Supervise != 1 {
		t.Fatalf("宣言が真: supervise = %+v, err %v", w, err)
	}
	ov, err := f.s.GetOverviewFor(ctx, &on)
	if err != nil || len(ov.SupervisorCheckingChallenges) != 1 {
		t.Fatalf("宣言が真: checking = %+v, err %v", ov, err)
	}

	rw, err := f.s.CountRoundWork(ctx, &on, f.conn)
	if err != nil || rw.Stages.Supervise != 1 {
		t.Fatalf("宣言が真: CountRoundWork supervise = %+v, err %v", rw, err)
	}

	// 偽のとき: 数えず、確認中に出さず、オーナーの回答待ちに出す。
	w, err = f.s.CountCycleWork(ctx, &off, f.conn)
	if err != nil || w.Stages.Supervise != 0 {
		t.Fatalf("宣言が偽: CountCycleWork supervise = %+v, err %v", w, err)
	}
	rw, err = f.s.CountRoundWork(ctx, &off, f.conn)
	if err != nil || rw.Stages.Supervise != 0 {
		t.Fatalf("宣言が偽: CountRoundWork supervise = %+v, err %v", rw, err)
	}
	ov, err = f.s.GetOverviewFor(ctx, &off)
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.SupervisorCheckingChallenges) != 0 {
		t.Fatalf("宣言が偽なのに確認中に出ている: %+v", ov.SupervisorCheckingChallenges)
	}
	found := false
	for _, c := range ov.NeedsHumanChallenges {
		found = found || c.ID == id
	}
	if !found {
		t.Fatalf("オーナーの回答待ちに出ていない: %+v", ov.NeedsHumanChallenges)
	}
	d, err := f.s.GetChallenge(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	card, err := f.s.DeriveCard(ctx, d.Challenge, ov, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	hasAnswer := false
	for _, a := range card.NextHumanActions {
		hasAnswer = hasAnswer || a == CardActionAnswerQuestion
	}
	if !hasAnswer {
		t.Fatalf("カードの次の操作に回答が無い: %+v", card.NextHumanActions)
	}
	unanswered, err := f.s.ListUnansweredHolds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, u := range unanswered {
		listed = listed || u.ChallengeID == id
	}
	if !listed {
		t.Fatalf("未回答の保留に出ていない: %v", unanswered)
	}

	// 保留の行は書き換わらない。
	if _, state := holdColumns(t, f.s, id); state.String != string(SupervisorStatePending) {
		t.Fatalf("supervisor_state = %q, want pending", state.String)
	}

	// 真に戻せば、また確認中・数えの対象。
	w, err = f.s.CountCycleWork(ctx, &on, f.conn)
	if err != nil || w.Stages.Supervise != 1 {
		t.Fatalf("宣言を真に戻した: supervise = %+v, err %v", w, err)
	}
	ov, err = f.s.GetOverviewFor(ctx, &on)
	if err != nil || len(ov.SupervisorCheckingChallenges) != 1 {
		t.Fatalf("宣言を真に戻した: checking = %+v, err %v", ov, err)
	}
}

// 宣言が偽のとき、オーナーが回答でき、回答すると保留は閉じて課題は保留の前の状態へ戻る。
func TestSupervisorOff_OwnerCanAnswerPendingHold(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newJ5Fixture(t, "impl")
	id, err := holdViaEntrance(t, f.s, StatusVerifying, HoldKindHuman, HoldOptionsInput{StopKind: StopKindVerifyUncertain, SupervisorEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	off := *f.agent
	off.Supervisor = false
	ov, err := f.s.GetOverviewFor(ctx, &off)
	if err != nil {
		t.Fatal(err)
	}
	waiting := false
	for _, n := range ov.NeedsHumanChallenges {
		waiting = waiting || n.ID == id
	}
	if !waiting {
		t.Fatalf("回答前: オーナー待ちに出ていない")
	}
	c, h, err := answerWith(t, f.s, id, "進めてよい", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != StatusVerifying || h.Answer == nil {
		t.Fatalf("回答後 status = %s, hold = %+v", c.Status, h)
	}
	unanswered, err := f.s.ListUnansweredHolds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range unanswered {
		if u.ChallengeID == id {
			t.Fatalf("回答後も未回答の保留に残っている")
		}
	}
	ov, err = f.s.GetOverviewFor(ctx, &off)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range ov.NeedsHumanChallenges {
		if n.ID == id {
			t.Fatalf("回答後もオーナー待ちに残っている")
		}
	}
}
