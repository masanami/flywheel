package core

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// このファイルは、区切り S4b-2 の受入基準のうち「実装は基準どおりだが、その場面を直接固定する試験が無かった」5 件
// （AC-27・AC-59・AC-196・AC-244・AC-245）を、実ストアと偽の判断の IF で固定する。AC-n は親 #1071 の本文の「## 受入基準」の並びの順。

// AC-27: 契約の判断待ちのうち、承認の待ち（委譲先が、求める操作 approve で待ちを返したとき）で作られた保留は、
// 止まり方とスーパーバイザー待ちが NULL で、スーパーバイザーの段は判断点を呼ばない。
func TestSuperviseStage_ContractApprovalWaitHold_IsNotPendingAndNotCalled_AC27(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	f.agent.Supervisor = true // 宣言が有効でも、承認の待ちは対象外
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{waitingOut(&ContractRequestedAction{Kind: "approve", Decider: "human", Text: "承認して"}, nil)}
	f.poll(t)
	wantLastHoldStop(t, f.s, stopNone)

	inv := &fakeJudgmentInvoker{result: svResult(nil)}
	calls := 0
	inv.onInvoke = func(JudgmentLaunchInput) { calls++ }
	res, err := f.s.superviseStage(context.Background(), CycleRunInput{AgentDecl: f.agent, Invoker: inv}, f.cycle(t, 300), nil)
	if err != nil {
		t.Fatalf("superviseStage: %v", err)
	}
	if calls != 0 || res.SuperviseProcessed != 0 || len(res.SuperviseHands) != 0 {
		t.Errorf("calls = %d, processed = %d, hands = %v, want 0, 0, none (the approval wait is not a target)", calls, res.SuperviseProcessed, res.SuperviseHands)
	}
	if got := f.count(t, `SELECT COUNT(*) FROM supervisor_action`); got != 0 {
		t.Errorf("supervisor_action rows = %d, want 0", got)
	}
}

// AC-59: タスクを指す保留の、判断点の入力に、課題の題名（「課題: …」）とタスクの題名（「タスク: …」）がある。
func TestSupervisorInput_TaskHold_HasTheChallengeTitleAndTheTaskTitle_AC59(t *testing.T) {
	t.Parallel()
	f := newSVInputFixture(t)
	var taskTitle string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT title FROM task WHERE id = ?`, f.taskID).Scan(&taskTitle)
	}); err != nil || taskTitle == "" {
		t.Fatalf("task title = %q (%v), want non-empty", taskTitle, err)
	}
	var challengeTitle string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT title FROM challenge WHERE id = ?`, f.cidInt).Scan(&challengeTitle)
	}); err != nil || challengeTitle == "" {
		t.Fatalf("challenge title = %q (%v), want non-empty", challengeTitle, err)
	}
	body := f.sections(t, StopKindVerifyUncertain)["課題とタスク"]
	for _, want := range []string{"課題: " + challengeTitle + "\n", "タスク: " + taskTitle + "\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("the challenge-and-task section lacks %q: %s", want, body)
		}
	}
}

// AC-196: オーナーが判断の間に先に答えたために結果を捨てたとき、その判断の実行の費用が課題の費用に入っている
// （判断の実行の記録が課題を指し、費用が 0 より大きい）。
func TestSuperviseStage_OwnerAnsweredDuringTheJudgment_TheRunCostStaysOnTheChallenge_AC196(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	id := f.pendingHold(t, StopKindUpstreamUnreadable)
	f.inv.result = svResult(nil)
	prev := f.inv.onInvoke
	f.inv.onInvoke = func(in JudgmentLaunchInput) {
		prev(in)
		answerHoldForTest(t, f.s, id, "オーナーの回答")
	}
	f.stage(t)
	as := f.actions(t, id)
	if len(as) != 1 || as[0].Result != ActionRejected || as[0].RejectReason == nil || *as[0].RejectReason != RejectOwnerAnswered {
		t.Fatalf("actions = %+v, want one rejected owner_answered (the result was discarded)", as)
	}
	if as[0].RunID == nil {
		t.Fatal("the discarded action must keep the judgment run")
	}
	cid, _ := parseChallengeID(id)
	runID, ok := parseRunID(*as[0].RunID)
	if !ok {
		t.Fatalf("run id %q is not a run id", *as[0].RunID)
	}
	var runChallenge sql.NullInt64
	var cost float64
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT challenge_id, cost_usd FROM run WHERE id = ?`, runID).Scan(&runChallenge, &cost)
	}); err != nil {
		t.Fatal(err)
	}
	if !runChallenge.Valid || runChallenge.Int64 != cid || cost <= 0 {
		t.Errorf("run challenge = %v, cost = %v, want the challenge %d and a cost above 0", runChallenge, cost, cid)
	}
	var sum float64
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COALESCE(SUM(cost_usd), 0) FROM run WHERE judgment = 'SV' AND challenge_id = ?`, cid).Scan(&sum)
	}); err != nil || sum <= 0 {
		t.Errorf("the cost of the challenge's judgment runs = %v (%v), want above 0", sum, err)
	}
}

// AC-244・AC-245: 1 つのサイクルで判断点を 2 回呼び、1 回は executed の reverify、1 回は failed のとき、
// 段 supervise の処理した件数は 2、手ごとの件数は reverify が 1（failed は手として数えない）。
// 判断点は同じ出力（reverify）を返し、verify_uncertain では選べる手なので executed、upstream_unreadable では選べる手の外なので failed になる。
func TestSuperviseStage_ExecutedReverifyAndFailedCall_ProcessedIsTwoAndReverifyIsOne_AC244_AC245(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t)
	okID := f.pendingHold(t, StopKindVerifyUncertain)
	failID := f.pendingHold(t, StopKindUpstreamUnreadable)
	f.inv.result = svResult(map[string]any{"hand": "reverify", "findings": "調べた", "note": "もう一度確かめる"})
	res := f.stage(t)
	if len(f.inputs) != 2 {
		t.Fatalf("calls = %d, want 2", len(f.inputs))
	}
	if as := f.actions(t, okID); len(as) != 1 || as[0].Result != ActionExecuted || as[0].Hand == nil || *as[0].Hand != HandReverify {
		t.Fatalf("actions of the first hold = %+v, want one executed reverify", as)
	}
	if as := f.actions(t, failID); len(as) != 1 || as[0].Result != ActionFailed {
		t.Fatalf("actions of the second hold = %+v, want one failed", as)
	}
	if res.SuperviseProcessed != 2 { // AC-244
		t.Errorf("processed = %d, want 2", res.SuperviseProcessed)
	}
	if res.SuperviseHands[HandReverify] != 1 || len(res.SuperviseHands) != 1 { // AC-245
		t.Errorf("hands = %v, want reverify:1 only", res.SuperviseHands)
	}
}
