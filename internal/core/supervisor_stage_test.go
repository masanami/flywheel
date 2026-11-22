package core

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// このファイルは区切り S4b-2 のスーパーバイザーの段（対象を選ぶ・判断点を呼ぶ・結果を確かめる・手 escalate を実行する）を、
// 実ストアと偽の判断の IF で検証する。

type svFixture struct {
	*j6Fixture
	cycle string
}

func newSVFixture(t *testing.T) *svFixture {
	t.Helper()
	f := &svFixture{j6Fixture: newJ6Fixture(t)}
	f.agent.Supervisor = true
	f.cycle = f.j6Fixture.cycle(t, 100)
	return f
}

// pendingHold は、止まり方 kind のスーパーバイザー待ちの保留を持つ課題を作り、課題の ID を返す。
func (f *svFixture) pendingHold(t *testing.T, kind StopKind) string {
	t.Helper()
	id, err := holdViaEntrance(t, f.s, StatusVerifying, HoldKindHuman, HoldOptionsInput{StopKind: kind, SupervisorEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func svResult(v map[string]any) JudgmentLaunchOutput {
	out := map[string]any{"hand": "escalate", "findings": "調べた", "note": "", "basis": []any{}, "recommendation": "推奨する", "options": []any{}, "conditions": []any{}}
	for k, x := range v {
		out[k] = x
	}
	return j6Result(out)
}

func (f *svFixture) stage(t *testing.T) CyclePhaseResult {
	t.Helper()
	res, err := f.s.superviseStage(context.Background(), CycleRunInput{AgentDecl: f.agent, Invoker: f.inv}, f.cycle, nil)
	if err != nil {
		t.Fatalf("superviseStage: %v", err)
	}
	return res
}

type svHoldRow struct {
	state, findings, recommendation sql.NullString
	actionID                        sql.NullInt64
}

func (f *svFixture) holdRow(t *testing.T, id string) svHoldRow {
	t.Helper()
	cid, _ := parseChallengeID(id)
	var r svHoldRow
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT supervisor_state, supervisor_findings, supervisor_recommendation, supervisor_action_id FROM hold WHERE challenge_id = ?`, cid).
			Scan(&r.state, &r.findings, &r.recommendation, &r.actionID)
	}); err != nil {
		t.Fatal(err)
	}
	return r
}

func (f *svFixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(q, args...)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *svFixture) actions(t *testing.T, id string) []SupervisorAction {
	t.Helper()
	as, err := f.s.ListSupervisorActionsByChallenge(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return as
}

func (f *svFixture) superviseActivities(t *testing.T, id string) (n int, channel string) {
	t.Helper()
	cid, _ := parseChallengeID(id)
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*), COALESCE(MAX(channel), '') FROM activity WHERE entity = 'challenge' AND entity_id = ? AND action = ?`, cid, ActivityActionSupervise).Scan(&n, &channel)
	}); err != nil {
		t.Fatal(err)
	}
	return
}

// 待ちが無い回りでは、判断点を呼ばない。
func TestSuperviseStage_NoPendingHold_DoesNotCallTheJudgment(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	res := f.stage(t)
	if len(f.inputs) != 0 || res.SuperviseProcessed != 0 || res.Skipped {
		t.Errorf("calls = %d, processed = %d, skipped = %v, want 0, 0, false", len(f.inputs), res.SuperviseProcessed, res.Skipped)
	}
}

// 宣言の supervisor が偽なら、段は飛ばし、判断点を呼ばない。
func TestSuperviseStage_SupervisorOffSkipsTheStage(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	f.pendingHold(t, StopKindVerifyUncertain)
	f.agent.Supervisor = false
	res := f.stage(t)
	if !res.Skipped || len(f.inputs) != 0 {
		t.Errorf("skipped = %v, calls = %d, want true, 0", res.Skipped, len(f.inputs))
	}
}

// escalate: 保留をオーナーへ上げ、findings・推奨・選択肢・対処の記録の ID を保留に残す。課題の状態は保留のまま。対処の記録と作業ログが 1 行ずつ。
func TestSuperviseStage_Escalate_MarksTheHoldAndRecordsTheAction(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindFactsUnmet)
	f.inv.result = svResult(nil)
	res := f.stage(t)
	if res.SuperviseProcessed != 1 || res.SuperviseHands[HandEscalate] != 1 {
		t.Fatalf("processed = %d, hands = %v, want 1 and escalate:1", res.SuperviseProcessed, res.SuperviseHands)
	}
	r := f.holdRow(t, id)
	if r.state.String != "escalated" || r.findings.String != "調べた" || r.recommendation.String != "推奨する" {
		t.Errorf("hold = %+v, want escalated with findings and recommendation", r)
	}
	as := f.actions(t, id)
	if len(as) != 1 || as[0].Result != ActionExecuted || as[0].Hand == nil || *as[0].Hand != HandEscalate || as[0].StopKind != StopKindFactsUnmet ||
		as[0].RunID == nil || as[0].CycleID != f.cycle || as[0].Findings != "調べた" {
		t.Fatalf("actions = %+v, want one executed escalate action with the run and the cycle", as)
	}
	if !r.actionID.Valid || r.actionID.Int64 != as[0].ID {
		t.Errorf("supervisor_action_id = %v, want %d", r.actionID, as[0].ID)
	}
	if n, ch := f.superviseActivities(t, id); n != 1 || ch != "invoker" {
		t.Errorf("supervise activities = %d (channel %q), want 1 (invoker)", n, ch)
	}
	d, err := f.s.GetChallenge(context.Background(), id)
	if err != nil || d.Status != StatusAwaitingHuman {
		t.Errorf("status = %v (%v), want awaiting_human (escalate does not move the challenge)", d.Status, err)
	}
	// 判断の費用は、課題とサイクルの両方に入る（run が課題と周に紐づき、費用を持つ）。
	var n int
	cid, _ := parseChallengeID(id)
	cyc, _ := parseCycleID(f.cycle)
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM run WHERE judgment = 'SV' AND challenge_id = ? AND cycle_id = ? AND cost_usd > 0`, cid, cyc).Scan(&n)
	}); err != nil || n != 1 {
		t.Errorf("SV runs with cost for the challenge and the cycle = %d (%v), want 1", n, err)
	}
}

// 待ちの保留を古い順に 1 件ずつ、判断点を 1 回ずつ呼ぶ。入力に止まり方・問い・選べる手の一覧を渡す。
func TestSuperviseStage_CallsOldestFirstWithTheStopKindAndOfferedHands(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	f.pendingHold(t, StopKindReworkLimit)
	f.pendingHold(t, StopKindUpstreamUnreadable)
	res := f.stage(t)
	if res.SuperviseProcessed != 2 || len(f.inputs) != 2 {
		t.Fatalf("processed = %d, calls = %d, want 2, 2", res.SuperviseProcessed, len(f.inputs))
	}
	text := func(in JudgmentLaunchInput) string {
		var b strings.Builder
		for _, s := range in.Sections {
			b.WriteString(s.Label + "\n" + s.Content + "\n")
		}
		return b.String()
	}
	first, second := text(f.inputs[0]), text(f.inputs[1])
	if !strings.Contains(first, "止まり方: rework_limit") || !strings.Contains(second, "止まり方: upstream_unreadable") {
		t.Errorf("order: first=%q second=%q, want rework_limit then upstream_unreadable", first, second)
	}
	// 保留の直前の状態が検証中なので、表の send_back は事前に外れる（replan・escalate は残る）。
	for _, h := range []string{"replan", "escalate"} {
		if !strings.Contains(first, "- "+h+":") {
			t.Errorf("the offered hands lack %s: %s", h, first)
		}
	}
	if strings.Contains(first, "- defer:") || strings.Contains(first, "- send_back:") || f.inputs[0].Judgment != JudgmentSV {
		t.Errorf("the offered hands must be the table's list only (no defer for rework_limit); judgment = %s", f.inputs[0].Judgment)
	}
}

// オーナーが先に回答した保留は、判断点を呼ばない。
func TestSuperviseStage_AnsweredHoldIsNotCalled(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindVerifyUncertain)
	cid, _ := parseChallengeID(id)
	f.exec(t, `UPDATE hold SET answer = 'はい', answered_at = '2026-11-22T00:00:00.000Z', answered_by = 'alice' WHERE challenge_id = ?`, cid)
	res := f.stage(t)
	if len(f.inputs) != 0 || res.SuperviseProcessed != 0 {
		t.Errorf("calls = %d, processed = %d, want 0, 0", len(f.inputs), res.SuperviseProcessed)
	}
}

// 判断の間にオーナーが回答していたら、手を実行せず（escalate の書き込みは待ちの保留だけを書き換える）、対処の記録は rejected（owner_answered）。保留はオーナーの回答のまま。
func TestSuperviseStage_OwnerAnswersDuringTheJudgment_IsRejected(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindVerifyUncertain)
	cid, _ := parseChallengeID(id)
	f.inv.result = svResult(nil)
	f.inv.onInvoke = func(JudgmentLaunchInput) {
		f.exec(t, `UPDATE hold SET answer = 'はい', answered_at = '2026-11-22T00:00:00.000Z', answered_by = 'alice' WHERE challenge_id = ?`, cid)
	}
	res := f.stage(t)
	as := f.actions(t, id)
	if len(as) != 1 || as[0].Result != ActionRejected || as[0].RejectReason == nil || *as[0].RejectReason != RejectOwnerAnswered {
		t.Fatalf("actions = %+v, want one rejected(owner_answered)", as)
	}
	if res.SuperviseHands[HandEscalate] != 0 || f.holdRow(t, id).state.String != "pending" {
		t.Errorf("hands = %v, state = %q, want no executed hand and the hold untouched", res.SuperviseHands, f.holdRow(t, id).state.String)
	}
}

// 返ってきた手が、渡した選べる手の一覧に無いとき: 手を実行せず、対処の記録を failed にして上げる。
func TestSuperviseStage_HandOutsideTheOfferedList_FailsAndEscalates(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindFactsUnmet) // retry は選べない
	f.inv.result = svResult(map[string]any{"hand": "retry"})
	res := f.stage(t)
	as := f.actions(t, id)
	if len(as) != 1 || as[0].Result != ActionFailed || as[0].Hand != nil {
		t.Fatalf("actions = %+v, want one failed action without a hand", as)
	}
	if r := f.holdRow(t, id); r.state.String != "escalated" || !r.actionID.Valid || r.actionID.Int64 != as[0].ID {
		t.Errorf("hold = %+v, want escalated by the failed action", r)
	}
	if res.SuperviseProcessed != 1 || len(res.SuperviseHands) != 0 {
		t.Errorf("processed = %d, hands = %v, want 1 and none", res.SuperviseProcessed, res.SuperviseHands)
	}
}

// まだ実装の無い手は、実行できなかったとして、対処の記録を failed にして上げる（書き込みは巻き戻る）。
func TestSuperviseStage_HandNotImplementedYet_FailsAndEscalates(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindDelegateStopped)
	f.inv.result = svResult(map[string]any{"hand": "retry", "note": "調べた結果"})
	f.stage(t)
	as := f.actions(t, id)
	if len(as) != 1 || as[0].Result != ActionFailed || as[0].Hand == nil || *as[0].Hand != HandRetry {
		t.Fatalf("actions = %+v, want one failed retry action (the executed row is rolled back)", as)
	}
	if f.holdRow(t, id).state.String != "escalated" {
		t.Error("the hold must be escalated")
	}
}

// 回りの終わりの数えは、スーパーバイザー待ちの保留を数える。宣言が偽なら数えない。
func TestCountRoundWork_CountsSupervisorPendingHolds(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	f.pendingHold(t, StopKindVerifyUncertain)
	w, err := f.s.CountRoundWork(context.Background(), f.agent, nil)
	if err != nil || w.Stages.Supervise != 1 || w.Startable != 1 {
		t.Fatalf("work = %+v (%v), want supervise 1 and startable 1", w, err)
	}
	if _, err := f.s.EndCycle(context.Background(), f.cycle, CycleResultCompleted); err != nil {
		t.Fatal(err)
	}
	if w, _ := f.s.CountRoundWork(context.Background(), f.agent, nil); w.Stages.Supervise != 1 {
		t.Errorf("after the cycle ended: supervise = %d, want 1 (the next cycle handles it)", w.Stages.Supervise)
	}
	f.agent.Supervisor = false
	if w, _ := f.s.CountRoundWork(context.Background(), f.agent, nil); w.Stages.Supervise != 0 {
		t.Errorf("supervisor off: supervise = %d, want 0", w.Stages.Supervise)
	}
}

// 利用上限に当たった判断は、保留をスーパーバイザー待ちのまま残し、対処の記録を残さない。
func TestSuperviseStage_RateLimited_KeepsTheHoldPending(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindVerifyUncertain)
	f.inv.result = JudgmentLaunchOutput{Result: RunResultErrored, RateLimited: true}
	res := f.stage(t)
	if len(f.actions(t, id)) != 0 || f.holdRow(t, id).state.String != "pending" || len(res.NotStarted) != 1 || res.SuperviseProcessed != 0 {
		t.Errorf("actions = %d, state = %q, not_started = %v, processed = %d, want none, pending, 1, 0", len(f.actions(t, id)), f.holdRow(t, id).state.String, res.NotStarted, res.SuperviseProcessed)
	}
}

func TestNumberedLines(t *testing.T) {
	t.Parallel()
	got := numberedLines("- 一つ目\n\n2. 二つ目\n1.5GB 以下にする\n* 三つ目")
	want := "1. 一つ目\n2. 二つ目\n3. 1.5GB 以下にする\n4. 三つ目"
	if got != want {
		t.Errorf("numberedLines = %q, want %q", got, want)
	}
	if numberedLines("  ") != "(なし)" {
		t.Error("an empty text must read (なし)")
	}
}

// 保留がタスクを指さないとき、入力は課題の完了条件（番号つき）と課題のタスクの一覧（題名と状態）を持ち、課題の題名を持つ。
func TestSuperviseStage_InputForAHoldWithoutATask_HasTheChallengeCriteriaAndTasks(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindVerifyUncertain)
	cid, _ := parseChallengeID(id)
	f.exec(t, `UPDATE challenge SET done_criteria = ? WHERE id = ?`, "- 条件A\n- 条件B", cid)
	f.stage(t)
	if len(f.inputs) != 1 {
		t.Fatalf("calls = %d, want 1", len(f.inputs))
	}
	var body string
	for _, sec := range f.inputs[0].Sections {
		if sec.Label == "課題とタスク" {
			body = sec.Content
		}
	}
	for _, w := range []string{"課題: t", "課題の完了条件（番号つき）:\n1. 条件A\n2. 条件B", "課題のタスク（題名と状態）:"} {
		if !strings.Contains(body, w) {
			t.Errorf("the challenge section lacks %q: %s", w, body)
		}
	}
}
