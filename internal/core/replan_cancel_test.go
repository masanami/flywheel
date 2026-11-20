package core

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
)

// このファイルは、計画の作り直しと取り下げ（区切り S3b の「計画の作り直しと取り下げ」）を、実ストアと偽の委譲先で検証する:
// 回答の検査（--replan）・J2 の入力と出力の検査・承認での取りやめ・契約の実行の取り消し（承認の後の状態の確認・取り下げ）。

// replanTo は課題 id を、保留 → --replan の回答 → 版 2 の計画（tasks。統合先を使う）の承認、まで進める。
// 回答が拒まれたら、そのエラーを返す。
func (f *contractFixture) replanTo(t *testing.T, id string, tasks ...multiTask) error {
	t.Helper()
	ctx := context.Background()
	cid, _ := parseChallengeID(id)
	if st := f.statusOf(t, id); st != StatusAwaitingHuman {
		if _, err := f.s.HoldChallenge(ctx, ChannelCLI, id, HoldInput{Question: "範囲を変えたい"}); err != nil {
			t.Fatalf("hold: %v", err)
		}
	}
	if _, _, err := f.answer(t, id, "新しい範囲", true); err != nil {
		return err
	}
	if got := f.statusOf(t, id); got != StatusClassified {
		t.Fatalf("status after the replan answer = %s, want classified", got)
	}
	f.sqlExec(t, `INSERT INTO task_plan (challenge_id, version, body, created_at, spec) VALUES (?, 2, 'PLAN-V2', '2026-09-26T00:00:00.000Z', ?)`, cid, multiPlan(true, tasks...))
	f.sqlExec(t, `UPDATE challenge SET status = 'awaiting_plan_approval', version = version + 1 WHERE id = ?`, cid)
	if err := approvePlanForTest(t, f.s, id, ttyAtt(t, id)); err != nil {
		t.Fatalf("approve the new plan: %v", err)
	}
	return nil
}

func (f *contractFixture) cancelledRunIDs() []string {
	var out []string
	for _, c := range f.client.cancels {
		out = append(out, c[len(c)-1])
	}
	return out
}

func (f *contractFixture) openContractRunIDs(t *testing.T, id string) []string {
	t.Helper()
	cid, _ := parseChallengeID(id)
	var out []string
	readForTest(t, f.s, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT external_run_id FROM contract_run WHERE challenge_id = ? AND ended_at IS NULL ORDER BY id`, cid)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return err
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	return out
}

// 2 本のタスクの実行が開いている（レビュー待ち）統合先を使う課題。
func (f *contractFixture) twoTaskRunsWaitingForReview(t *testing.T) string {
	t.Helper()
	id := f.newMulti(t, "two", "P1", true, multiTask{key: "a"}, multiTask{key: "b"})
	f.runDelegationStage(t, nil)
	if got := f.startedTasks(t); len(got) != 2 {
		t.Fatalf("started tasks = %v, want 2", got)
	}
	f.client.echoRunning = false
	f.sqlExec(t, `UPDATE contract_run SET state = 'waiting', requested_kind = 'review', requested_decider = 'any' WHERE ended_at IS NULL`)
	return id
}

// AC-214・AC-215・AC-212・AC-213: 作り直した計画の承認で、取りやめになった 2 個のタスクの契約の実行の両方へ commands.cancel が呼ばれ、
// 新しい版のタスクは同じ課題の識別と {uses_integration} = true で起動される（済みのタスクは済みのまま）。
func TestReplanApproval_AC214_AC215_CancelsTheRunsOfTheCancelledTasksAndStartsTheNewTasks(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 3)
	id := f.twoTaskRunsWaitingForReview(t)
	cid, _ := parseChallengeID(id)
	var bRun string
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT external_run_id FROM contract_run WHERE task_id = (SELECT id FROM task WHERE challenge_id = ? AND key = 'b')`, cid).Scan(&bRun)
	})
	setTaskStatusRaw(t, f.s, cid, "a", TaskStatusDone) // 済み。契約の実行は開いたまま（終わりを読んでいない）にしても、取り消しの対象は取りやめのタスクだけ
	f.sqlExec(t, `UPDATE contract_run SET ended_at = '2026-09-25T00:00:00.000Z' WHERE task_id = (SELECT id FROM task WHERE challenge_id = ? AND key = 'a')`, cid)
	f.sqlExec(t, `UPDATE task SET status = 'in_progress' WHERE challenge_id = ? AND key = 'b'`, cid)
	if err := f.replanTo(t, id, multiTask{key: "c"}, multiTask{key: "d"}); err != nil {
		t.Fatalf("replan answer: %v", err)
	}
	got := taskStatusMap(t, f.s, cid)
	if got["a"] != TaskStatusDone { // AC-212
		t.Errorf("task a = %s, want done (kept)", got["a"])
	}
	if got["b"] != TaskStatusCancelled { // AC-213
		t.Errorf("task b = %s, want cancelled", got["b"])
	}
	f.client.cancelOut = contractOut(ContractStateCancelled, nil, nil)
	startedBefore := len(f.client.starts)
	f.runDelegationStage(t, nil)
	if !reflect.DeepEqual(f.cancelledRunIDs(), []string{bRun}) {
		t.Fatalf("cancels = %v, want the one open run of the cancelled task b (%s)", f.client.cancels, bRun)
	}
	for _, args := range f.client.starts[startedBefore:] { // AC-215
		if argAfter(t, args, "--challenge") != id || argAfter(t, args, "--integration") != "true" {
			t.Errorf("start %v, want --challenge %s --integration true", args, id)
		}
	}
	if len(f.client.starts) == startedBefore {
		t.Fatal("the tasks of the new version were not started")
	}
}

// AC-214: 取りやめになったタスクが 2 個あり、どちらも契約の実行が開いているとき、両方へ commands.cancel が呼ばれる。
func TestReplanApproval_AC214_BothOpenRunsOfTwoCancelledTasksAreCancelled(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 3)
	id := f.twoTaskRunsWaitingForReview(t)
	if err := f.replanTo(t, id, multiTask{key: "c"}); err != nil {
		t.Fatalf("replan answer: %v", err)
	}
	f.client.cancelOut = contractOut(ContractStateCancelled, nil, nil)
	f.runDelegationStage(t, nil)
	got := f.cancelledRunIDs()
	if len(got) != 2 || !containsAll(got, "ext-1", "ext-2") {
		t.Fatalf("cancels = %v, want both runs ext-1 and ext-2", got)
	}
	if open := f.openContractRunIDs(t, id); containsAny(open, "ext-1", "ext-2") {
		t.Errorf("open runs = %v, want only runs of the new tasks", open)
	}
	f.runDelegationStage(t, nil) // 次の状態の確認でも、取り消した実行を保留にし損ねたものと数えない
	if got := f.statusOf(t, id); got != StatusInProgress {
		t.Errorf("status after the next check = %s, want in_progress", got)
	}
	if held := f.holdReasons(t, id); len(held) != 1 {
		t.Errorf("holds = %v, want only the one the owner answered (cancelling the runs does not hold the challenge)", held)
	}
}

// AC-223: commands.cancel が失敗した契約の実行は、終わっていない記録のまま残り、次の状態の確認でもう一度 commands.cancel が呼ばれる。
func TestReplanApproval_AC223_AFailedCancelIsRetriedAtTheNextStatusCheck(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 3)
	id := f.twoTaskRunsWaitingForReview(t)
	if err := f.replanTo(t, id, multiTask{key: "c"}); err != nil {
		t.Fatalf("replan answer: %v", err)
	}
	f.client.cancelOut = ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "exit 1"}
	f.runDelegationStage(t, nil)
	if n := len(f.client.cancels); n != 2 {
		t.Fatalf("cancels = %d, want 2 (one per run, both failed)", n)
	}
	open := f.openContractRunIDs(t, id)
	if !containsAll(open, "ext-1", "ext-2") {
		t.Fatalf("open runs = %v, want the failed ones left open", open)
	}
	if got := f.statusOf(t, id); got != StatusInProgress {
		t.Errorf("status = %s, want in_progress (a failed cancel does not hold)", got)
	}
	f.client.cancelOut = contractOut(ContractStateCancelled, nil, nil)
	f.runDelegationStage(t, nil)
	if n := len(f.client.cancels); n != 4 {
		t.Fatalf("cancels = %d, want 4 after the retry", n)
	}
	if open := f.openContractRunIDs(t, id); containsAll(open, "ext-1") || containsAll(open, "ext-2") {
		t.Errorf("open runs = %v, want the retried runs ended", open)
	}
}

func containsAny(have []string, want ...string) bool {
	for _, w := range want {
		if containsAll(have, w) {
			return true
		}
	}
	return false
}

func containsAll(have []string, want ...string) bool {
	set := map[string]bool{}
	for _, h := range have {
		set[h] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

// 取りやめのタスクの実行は、レビュー待ちを受け取らない（読まずに取り消す）。
func TestReplanApproval_TheReviewOfACancelledTaskIsNotReceived(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 3)
	id := f.twoTaskRunsWaitingForReview(t)
	if err := f.replanTo(t, id, multiTask{key: "c"}); err != nil {
		t.Fatalf("replan answer: %v", err)
	}
	f.client.cancelOut = ContractLaunchOutput{Result: RunResultErrored}
	f.client.last = contractOut(ContractStateWaiting, nil, func(st *ContractStatus) {
		st.RequestedAction = &ContractRequestedAction{Kind: "review", Decider: "any", Text: "レビュー待ち"}
		st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: upPRURL}}
	})
	statuses := len(f.client.statuses)
	f.runDelegationStage(t, nil)
	if got := f.statusOf(t, id); got != StatusInProgress {
		t.Errorf("status = %s, want in_progress (no review was received)", got)
	}
	if n := len(f.client.statuses) - statuses; n != 0 {
		t.Errorf("commands.status was called %d times for the cancelled tasks' runs", n)
	}
}

// AC-221: 最後に読んだ状態が running のタスクの実行がある課題の保留への --replan の回答は、validation_failed（ErrValidation）で断られる。
func TestAnswerReplan_AC221_ARunningTaskRunRefusesTheReplanAnswer(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 3)
	id := f.newMulti(t, "run", "P1", true, multiTask{key: "a"}, multiTask{key: "b"})
	f.runDelegationStage(t, nil)
	f.client.echoRunning = false
	f.sqlExec(t, `UPDATE contract_run SET state = 'waiting', requested_kind = 'review', requested_decider = 'any' WHERE external_run_id = 'ext-1'`) // 1 本はレビュー待ち。もう 1 本は running のまま
	if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, id, HoldInput{Question: "範囲"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.answer(t, id, "新しい範囲", true); !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation (a task run is running)", err)
	}
	if got := f.statusOf(t, id); got != StatusAwaitingHuman {
		t.Errorf("status = %s, want awaiting_human (the answer was refused)", got)
	}
}

// 「入れてよい」を返して終わりを読んでいないタスクの実行は、最後に読んだ状態がレビュー待ちでも、作業している実行として断る。
func TestAnswerReplan_AC221_ARunAlreadyToldToLandRefusesTheReplanAnswer(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 3)
	id := f.newMulti(t, "land", "P1", true, multiTask{key: "a"})
	f.runDelegationStage(t, nil)
	f.client.echoRunning = false
	f.sqlExec(t, `UPDATE contract_run SET state = 'waiting', requested_kind = 'review', requested_decider = 'any', land_ref = 'feat/x@abc' WHERE external_run_id = 'ext-1'`)
	if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, id, HoldInput{Question: "範囲"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.answer(t, id, "新しい範囲", true); !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
}

// AC-220: レビュー待ちのタスクの実行だけが開いている課題の保留への --replan の回答は、受け付けられる。
func TestAnswerReplan_AC220_AReviewWaitingTaskRunDoesNotRefuseTheReplanAnswer(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 3)
	id := f.twoTaskRunsWaitingForReview(t)
	if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, id, HoldInput{Question: "範囲"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.answer(t, id, "新しい範囲", true); err != nil {
		t.Fatalf("err = %v, want the answer accepted", err)
	}
	if got := f.statusOf(t, id); got != StatusClassified {
		t.Errorf("status = %s, want classified", got)
	}
	if len(f.client.cancels) != 0 {
		t.Errorf("cancels = %v, want none at the answer", f.client.cancels)
	}
}

// 保留の間に成功していた（held_success）タスクの実行は、最後に読んだ状態がレビュー待ちでも、まだ取り込んでいない成果物を持つので、断る。
func TestAnswerReplan_AHeldSuccessTaskRunRefusesTheReplanAnswer(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 3)
	id := f.twoTaskRunsWaitingForReview(t)
	f.sqlExec(t, `UPDATE contract_run SET held_success = 1 WHERE external_run_id = 'ext-1'`)
	if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, id, HoldInput{Question: "範囲"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.answer(t, id, "新しい範囲", true); !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
}

// 終わった（ended_at がある）実行は、記録の状態が running のままでも、作業している実行として数えない。
func TestAnswerReplan_AnEndedTaskRunDoesNotRefuseTheReplanAnswer(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 3)
	id := f.newMulti(t, "ended", "P1", true, multiTask{key: "a"})
	f.runDelegationStage(t, nil)
	f.client.echoRunning = false
	f.sqlExec(t, `UPDATE contract_run SET state = 'running', ended_at = '2026-09-25T00:00:00.000Z' WHERE external_run_id = 'ext-1'`)
	if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, id, HoldInput{Question: "範囲"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.answer(t, id, "新しい範囲", true); err != nil {
		t.Fatalf("err = %v, want the answer accepted", err)
	}
}

// 上げる依頼が開いている（レビュー待ち）課題を作る（統合先を使う課題。タスク a が済み。上げる依頼は検証中で開いている）。
func (f *contractFixture) openReflectRun(t *testing.T) string {
	t.Helper()
	f.setUpPR("main")
	f.startUp(t)
	f.readWith(t, upReviewOut([]string{"respond", "land"}, upArtsOK...))
	if got := f.reflectOpen(t); !got {
		t.Fatal("the reflect request is not open")
	}
	return ""
}

func (f *contractFixture) reflectOpen(t *testing.T) (open bool) {
	t.Helper()
	readForTest(t, f.s, func(tx *sql.Tx) error {
		var n int
		err := tx.QueryRow(`SELECT COUNT(*) FROM contract_run WHERE kind = 'reflect' AND ended_at IS NULL`).Scan(&n)
		open = n > 0
		return err
	})
	return
}

// AC-219・AC-222・AC-216: 上げる依頼が開いている（レビュー待ち）課題の保留への --replan の回答は受け付けられ、作り直した計画の承認の後の状態の確認が
// 上げる依頼を commands.cancel で取り消し、新しい版のタスクが全部済みになると、もう一度 commands.reflect が呼ばれる。
func TestReplan_AC219_AC222_AC216_TheOpenReflectRequestIsCancelledAfterTheApprovalAndRequestedAgainWhenTheNewTasksAreDone(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, upConnectorsJSON())
	cid, _ := parseChallengeID(id)
	f.openReflectRun(t)
	if err := f.replanTo(t, id, multiTask{key: "c"}); err != nil { // AC-219
		t.Fatalf("the replan answer was refused: %v", err)
	}
	if len(f.client.cancels) != 0 {
		t.Fatalf("cancels = %v, want none before the approval", f.client.cancels)
	}
	if !f.reflectOpen(t) {
		t.Fatal("the reflect request was closed before the approval")
	}
	var reflectRun string
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT external_run_id FROM contract_run WHERE kind = 'reflect' AND ended_at IS NULL`).Scan(&reflectRun)
	})
	f.client.cancelOut = contractOut(ContractStateCancelled, nil, nil)
	f.runDelegationStage(t, nil)
	if !reflect.DeepEqual(f.cancelledRunIDs(), []string{reflectRun}) { // AC-222
		t.Fatalf("cancels = %v, want one commands.cancel for the reflect request %s", f.client.cancels, reflectRun)
	}
	if f.reflectOpen(t) {
		t.Error("the reflect request is still open after it was cancelled")
	}
	if n := len(f.reflectStarts()); n != 1 {
		t.Errorf("commands.reflect calls = %d, want 1 (the new task is not done yet)", n)
	}
	// 新しい版のタスクが全部済みになると、もう一度上げる依頼を始める（AC-216）。
	for _, row := range taskRows(t, f.s, cid) {
		if row.Status == TaskStatusPending || row.Status == TaskStatusInProgress {
			setTaskStatusRaw(t, f.s, cid, row.Key, TaskStatusDone)
		}
	}
	f.sqlExec(t, `UPDATE contract_run SET ended_at = '2026-09-26T00:00:00.000Z' WHERE ended_at IS NULL AND kind = 'task'`)
	f.client.echoRunning = false
	f.runDelegationStage(t, nil)
	if n := len(f.reflectStarts()); n != 2 {
		t.Fatalf("commands.reflect calls = %d, want 2 after the new tasks are done", n)
	}
}

// 上げる依頼（タスクの実行ではない）は、最後に読んだ状態が running でも、--replan の回答を断らない（承認の後に取り消す）。
func TestAnswerReplan_ARunningReflectRequestDoesNotRefuseTheReplanAnswer(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, upConnectorsJSON())
	f.openReflectRun(t)
	f.sqlExec(t, `UPDATE contract_run SET state = 'running' WHERE kind = 'reflect' AND ended_at IS NULL`)
	if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, id, HoldInput{Question: "範囲"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.answer(t, id, "新しい範囲", true); err != nil {
		t.Fatalf("err = %v, want the answer accepted", err)
	}
	if len(f.client.cancels) != 0 {
		t.Errorf("cancels = %v, want none at the answer", f.client.cancels)
	}
}

// AC-218: 上げる依頼が走っている課題を取り下げると、上げる依頼に commands.cancel が呼ばれる。
func TestWithdraw_AC218_TheReflectRequestIsCancelled(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, upConnectorsJSON())
	f.openReflectRun(t)
	f.client.cancelOut = contractOut(ContractStateCancelled, nil, nil)
	n, err := f.s.CancelOpenContractRuns(context.Background(), ContractCancelInput{ChallengeID: id, AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client})
	if err != nil || n != 1 || len(f.client.cancels) != 1 {
		t.Fatalf("cancelled %d (err %v), cancels = %v, want one commands.cancel", n, err, f.client.cancels)
	}
	ch, err := withdrawForTest(t, f.s, id, "r")
	if err != nil || ch.Status != StatusWithdrawn {
		t.Fatalf("withdraw: %+v err %v", ch, err)
	}
}

// AC-217: タスクの実行が 2 本走っている課題を取り下げると、両方に commands.cancel が呼ばれる（1 本の取り消しの失敗が、ほかの取り消しを止めない）。
func TestWithdraw_AC217_BothTaskRunsAreCancelled(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 3)
	id := f.newMulti(t, "two", "P1", true, multiTask{key: "a"}, multiTask{key: "b"})
	f.runDelegationStage(t, nil)
	if got := f.openContractRunIDs(t, id); len(got) != 2 {
		t.Fatalf("open runs = %v, want 2", got)
	}
	in := ContractCancelInput{ChallengeID: id, AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client}
	f.client.cancelOut = ContractLaunchOutput{Result: RunResultErrored}
	if _, err := f.s.CancelOpenContractRuns(context.Background(), in); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict when a cancel fails", err)
	}
	if n := len(f.client.cancels); n != 2 {
		t.Fatalf("cancels = %d, want both runs tried although the first failed", n)
	}
	f.client.cancelOut = contractOut(ContractStateCancelled, nil, nil)
	f.client.cancels = nil
	n, err := f.s.CancelOpenContractRuns(context.Background(), in)
	if err != nil || n != 2 {
		t.Fatalf("cancelled %d (err %v), want 2", n, err)
	}
	if got := f.cancelledRunIDs(); !containsAll(got, "ext-1", "ext-2") || len(got) != 2 {
		t.Fatalf("cancels = %v, want ext-1 and ext-2", got)
	}
	ch, err := withdrawForTest(t, f.s, id, "r")
	if err != nil || ch.Status != StatusWithdrawn {
		t.Fatalf("withdraw: %+v err %v", ch, err)
	}
	if open := f.openContractRunIDs(t, id); len(open) != 0 {
		t.Errorf("open runs = %v, want none", open)
	}
}
