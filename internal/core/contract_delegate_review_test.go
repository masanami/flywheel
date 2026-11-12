package core

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"
)

// このファイルは form: cli の接続ツールの委譲の後半について、独立レビューの差し戻しで見つかった欠陥
// （予算超過の中止の succeeded・答えの結び付けと二重返し・resume の失敗・J2 と委譲の検査・同時委譲数・周の配線）を、
// 実ストア・偽の ContractClient で固定する。

func approveAction() *ContractRequestedAction {
	return &ContractRequestedAction{Kind: "approve", Decider: "human", Text: "承認して"}
}

func (f *contractFixture) holdQuestions(t *testing.T) []string {
	t.Helper()
	var qs []string
	err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT question FROM hold ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var q string
			if err := rows.Scan(&q); err != nil {
				return err
			}
			qs = append(qs, q)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return qs
}

// resume に渡る引数に、保留への答えの中身が入る。
func TestPollContractRuns_ResumeCarriesTheAnswerTextOfTheWaitingHold(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{waitingOut(approveAction(), nil)}
	f.poll(t)
	f.answerLatestHold(t, f.cid, "ANSWER-MARKER")
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f.poll(t)
	want := []string{"tool", "resume", "ext-1", "--answer", "ANSWER-MARKER"}
	if len(f.client.resumes) != 1 || !reflect.DeepEqual(f.client.resumes[0], want) {
		t.Fatalf("resumes = %v, want %v", f.client.resumes, want)
	}
}

// start がすぐ waiting を返す経路も、保留に写し、その保留の答えを resume で返す。
func TestStartContractDelegation_WaitingFromStartIsHeldAndItsAnswerIsReturned(t *testing.T) {
	f := newContractFixture(t)
	f.client.startOut = waitingOut(approveAction(), nil)
	rep, err := f.start(t)
	if err != nil || !rep.Held || f.status(t, f.cid) != StatusAwaitingHuman {
		t.Fatalf("report = %+v err = %v status = %s, want held on the waiting from start", rep, err, f.status(t, f.cid))
	}
	f.answerLatestHold(t, f.cid, "ANSWER-S")
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f.poll(t)
	if len(f.client.resumes) != 1 || f.client.resumes[0][len(f.client.resumes[0])-1] != "ANSWER-S" {
		t.Fatalf("resumes = %v, want the answer ANSWER-S", f.client.resumes)
	}
}

// 判断待ちを写した保留の答えだけを返す。予算超過の中止の失敗で立った保留への答えは返さない。
func TestPollContractRuns_AnswerToAnotherHoldIsNotReturned(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	// 予算を超え、求められた操作が observe の waiting（保留に写さない）を読む。中止が失敗して、別の理由の保留が立つ。
	f.client.cancelOut = ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "exit 1"}
	f.client.queue = []ContractLaunchOutput{waitingOut(&ContractRequestedAction{Kind: "observe", Decider: "any", Text: "見ている"}, float64Ptr(6))}
	f.poll(t)
	if f.status(t, f.cid) != StatusAwaitingHuman {
		t.Fatalf("status = %s, want held after the failed cancel", f.status(t, f.cid))
	}
	f.answerLatestHold(t, f.cid, "止めた")
	f.poll(t)
	if len(f.client.resumes) != 0 {
		t.Fatalf("resumes = %v, want none: the answer was for the cancel-failure hold, not the waiting", f.client.resumes)
	}
}

// 古い（既に返した）保留の答えを、後の周で返し直さない。resume が失敗しても二重に返さず、保留にする。
func TestPollContractRuns_FailedResumeHoldsAndTheAnswerIsNotReturnedTwice(t *testing.T) {
	for _, res := range []RunResult{RunResultTimedOut, RunResultErrored} {
		t.Run(string(res), func(t *testing.T) {
			f := newContractFixture(t)
			if _, err := f.start(t); err != nil {
				t.Fatal(err)
			}
			f.client.queue = []ContractLaunchOutput{waitingOut(approveAction(), nil)}
			f.poll(t)
			f.answerLatestHold(t, f.cid, "ANSWER-1")
			f.client.resumeOut = ContractLaunchOutput{Result: res, ErrorSummary: "RESUME-BOOM"}
			reps := f.poll(t)
			if len(f.client.resumes) != 1 || len(reps) != 1 || !reps[0].Held || f.status(t, f.cid) != StatusAwaitingHuman {
				t.Fatalf("resumes = %v report = %+v status = %s, want one resume and a hold", f.client.resumes, reps, f.status(t, f.cid))
			}
			qs := f.holdQuestions(t)
			if len(qs) != 2 || !strings.Contains(qs[1], "resume") || !strings.Contains(qs[1], "RESUME-BOOM") || !strings.Contains(qs[1], "返し直さない") {
				t.Fatalf("questions = %q, want the resume failure and no automatic retry", qs)
			}
			// オーナーが回答して着手中へ戻っても、同じ答えを返し直さない。
			f.answerLatestHold(t, f.cid, "確かめた")
			f.poll(t)
			f.poll(t)
			if len(f.client.resumes) != 1 {
				t.Fatalf("resumes = %d, want 1（答えは二重に返さない）", len(f.client.resumes))
			}
		})
	}
}

// resume がすぐ戻り、同じ求められた操作の waiting を返しても、同じ問いの保留を立て直さない。
func TestPollContractRuns_ResumeReturningTheSameWaitingDoesNotAskAgain(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{waitingOut(approveAction(), nil)}
	f.poll(t)
	f.answerLatestHold(t, f.cid, "ANSWER-1")
	f.client.resumeOut = waitingOut(approveAction(), nil)
	f.poll(t)
	if n := f.count(t, `SELECT COUNT(*) FROM hold`); n != 1 || f.status(t, f.cid) != StatusInProgress {
		t.Fatalf("holds = %d status = %s, want the one answered hold and in_progress", n, f.status(t, f.cid))
	}
	// 次の周の状態の読みで、まだ同じ waiting なら改めて問う（答えは返し済みなので、二重には返さない）。
	f.client.queue = []ContractLaunchOutput{waitingOut(approveAction(), nil)}
	f.poll(t)
	if n := f.count(t, `SELECT COUNT(*) FROM hold`); n != 2 {
		t.Fatalf("holds = %d, want a new hold when the next status read is still waiting", n)
	}
	if len(f.client.resumes) != 1 {
		t.Fatalf("resumes = %d, want 1", len(f.client.resumes))
	}
}

// 別の求められた操作の waiting なら、新しい問いとして保留に写す。
func TestPollContractRuns_ResumeReturningAnotherWaitingAsksTheNewQuestion(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{waitingOut(approveAction(), nil)}
	f.poll(t)
	f.answerLatestHold(t, f.cid, "ANSWER-1")
	f.client.resumeOut = waitingOut(&ContractRequestedAction{Kind: "answer", Decider: "human", Text: "NEXT-QUESTION"}, nil)
	f.poll(t)
	qs := f.holdQuestions(t)
	if len(qs) != 2 || !strings.Contains(qs[1], "NEXT-QUESTION") {
		t.Fatalf("questions = %q, want the new question", qs)
	}
}

// 宣言の resume が {answer} を含まないとき、保留の文言は答えが渡ると言わない。
func TestPollContractRuns_HoldWordingMatchesWhetherResumeCarriesTheAnswer(t *testing.T) {
	with := newContractFixture(t)
	without := newContractFixtureWith(t, strings.Replace(contractConnectorsJSON, `, "--answer", "{answer}"`, "", 1))
	for _, c := range []struct {
		f        *contractFixture
		wantPass bool
	}{{with, true}, {without, false}} {
		if _, err := c.f.start(t); err != nil {
			t.Fatal(err)
		}
		c.f.client.queue = []ContractLaunchOutput{waitingOut(approveAction(), nil)}
		c.f.poll(t)
		q := c.f.holdQuestions(t)[0]
		if got := strings.Contains(q, "その文を接続ツールへ返して続ける"); got != c.wantPass {
			t.Errorf("wantPass=%v question = %q", c.wantPass, q)
		}
		if !c.wantPass && !strings.Contains(q, "答えの差し込みを含まない") {
			t.Errorf("question = %q, want the note that the answer is not passed", q)
		}
	}
}

// 予算超過の中止の応答が succeeded（成果物つき）なら、課題を検証中へ進める。次の周が start を呼び直さない。
func TestPollContractRuns_OverBudgetCancelAnsweredSucceededMovesToVerifying(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.cancelOut = contractOut(ContractStateSucceeded, float64Ptr(6), func(st *ContractStatus) {
		st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: "https://github.com/o/r/pull/9"}}
	})
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, float64Ptr(6), nil)}
	f.poll(t)
	if got := f.status(t, f.cid); got != StatusVerifying {
		t.Fatalf("status = %s, want verifying", got)
	}
	if state, _, _, ended := f.contractRow(t); state != "succeeded" || !ended {
		t.Fatalf("contract_run state=%s ended=%v", state, ended)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM run_artifact`); n != 1 {
		t.Fatalf("artifacts = %d, want the PR imported", n)
	}
	in := f.input(f.cycle(t, 300), nil)
	in.Contract = f.client
	if _, err := f.s.RunDelegation(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(f.client.starts) != 1 {
		t.Fatalf("starts = %d, want 1（終わった契約の run の課題へ start を呼び直さない）", len(f.client.starts))
	}
}

// 比べる上限額は、課題の実装の枠とレビュー対応の枠の残りの合計（実装の枠だけではない）。
func TestRunDelegation_CLIBudgetIsTheSumOfImplAndReviewRemaining(t *testing.T) {
	f := newContractFixture(t)
	in := f.input(f.cycle(t, 300), idPtr(f.cid))
	in.Contract = f.client
	ch := f.detail(t, f.cid).Challenge
	dc, err := f.s.loadDelegationContext(context.Background(), in, ch)
	if err != nil {
		t.Fatal(err)
	}
	if dc.ReviewBudgetMicros <= 0 {
		t.Fatalf("review frame = %d, want it positive so that impl-only differs from the sum", dc.ReviewBudgetMicros)
	}
	if _, err := f.s.RunDelegation(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	var maxBudget int64
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT max_budget_usd FROM contract_run`).Scan(&maxBudget)
	}); err != nil {
		t.Fatal(err)
	}
	if want := dc.ImplBudgetMicros + dc.ReviewBudgetMicros; maxBudget != want {
		t.Fatalf("max_budget_usd = %d, want impl + review = %d", maxBudget, want)
	}
	// 実装の枠を上回り、合計以下の費用では中止しない。
	over := microsToUSD(dc.ImplBudgetMicros) + 0.01
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, &over, nil)}
	f.poll(t)
	if len(f.client.cancels) != 0 {
		t.Fatalf("cancels = %v, want none while the cost is within impl + review", f.client.cancels)
	}
}

// 取り込み元の差し込みを要する commands.start の接続ツールは、取り込み元の無い課題の計画を委譲の対象にせず、
// 黙って飛ばさず理由つきで返す（起動しない）。
func TestRunDelegation_CLIStartNeedingSourceIsReportedNotSilentlySkipped(t *testing.T) {
	f := newContractFixtureWith(t, strings.Replace(contractConnectorsJSON, `"--challenge", "{challenge_id}"`, `"--issue", "{issue_number}"`, 1))
	in := f.input(f.cycle(t, 300), nil)
	in.Contract = f.client
	res, err := f.s.RunDelegation(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.client.starts) != 0 || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedPlanUnavailable || res.NotStarted[0].ChallengeID != f.cid || res.NotStarted[0].Detail == "" {
		t.Fatalf("starts = %v not_started = %+v, want plan_unavailable with the reason", f.client.starts, res.NotStarted)
	}
}

// 委譲の側で start の差し込みを埋められなかったとき（検査をすり抜けた場合）も、周のエラーにも黙殺にもせず not_started で返す。
func TestDelegateViaContract_UnfillableStartIsNotStarted(t *testing.T) {
	f := newContractFixtureWith(t, strings.Replace(contractConnectorsJSON, `"--challenge", "{challenge_id}"`, `"--issue", "{issue_number}"`, 1))
	in := f.input(f.cycle(t, 300), nil)
	in.Contract = f.client
	dc := &delegationContext{Challenge: f.detail(t, f.cid).Challenge, Connector: f.conn.Connectors[0], ImplBudgetMicros: usdToMicros(3), ReviewBudgetMicros: usdToMicros(2)}
	item, ns, err := f.s.delegateViaContract(context.Background(), in, NewJudgmentCycle(in.CycleID), dc, nil)
	if err != nil || item != nil || ns == nil || ns.Reason != NotStartedPlanUnavailable || ns.ChallengeID != f.cid || ns.Detail == "" {
		t.Fatalf("item = %+v not_started = %+v err = %v, want plan_unavailable", item, ns, err)
	}
	if len(f.client.starts) != 0 {
		t.Fatalf("starts = %v, want none", f.client.starts)
	}
}

// 同時委譲数（max_parallel_runs）は契約の run も数える。上限に達していれば start を呼ばない。
func TestRunDelegation_CLIRespectsMaxParallelRuns(t *testing.T) {
	// 別のリポジトリ（別の直列化グループ）の課題は、同時委譲数だけで起動を止められる。
	f := newContractFixtureWith(t, strings.Replace(contractConnectorsJSON, `{"name": "main-repo"`,
		`{"name": "other-repo", "remote": "o/s", "default_branch": "main", "connector": "tool", "slots": {"provider": "clone", "paths": ["slot-s"]}},
    {"name": "main-repo"`, 1))
	f.agent.MaxParallelRuns = 1
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	second := f.newInProgress(t, "T2", "P2", planSpec(func(m map[string]any) { m["repo"] = "other-repo" }))
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, nil, nil)}
	in := f.input(f.cycle(t, 300), nil)
	in.Contract = f.client
	res, err := f.s.RunDelegation(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.client.starts) != 1 {
		t.Fatalf("starts = %v, want only the first（上限 1）", f.client.starts)
	}
	found := false
	for _, ns := range res.NotStarted {
		if ns.ChallengeID == second && ns.Reason == NotStartedSlotUnavailable {
			found = true
		}
	}
	if !found {
		t.Fatalf("not_started = %+v, want %s slot_unavailable", res.NotStarted, second)
	}
}

// 周の状態の読みが失敗したとき、周は止めず、その課題を not_started として結果に見せる。
func TestRunDelegation_ContractReadFailureIsVisibleInTheResult(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{{Result: RunResultErrored, ErrorSummary: "STATUS-BOOM"}}
	in := f.input(f.cycle(t, 300), nil)
	in.Contract = f.client
	res, err := f.s.RunDelegation(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].ChallengeID != f.cid || res.NotStarted[0].Reason != NotStartedInProgress ||
		!strings.Contains(res.NotStarted[0].Detail, "STATUS-BOOM") {
		t.Fatalf("not_started = %+v, want the failed read of %s", res.NotStarted, f.cid)
	}
}

// 周（RunCycle）は、終わっていない契約の run の状態を読む（契約の起動の口を委譲の段へ渡している）。
func TestRunCycle_PollsUnfinishedContractRuns(t *testing.T) {
	f := newCycleFixture(t)
	c := createAndAdvance(t, f.s, StatusInProgress)
	cid := mustParseChallengeID(t, c.ID)
	ctx := context.Background()
	if err := f.s.db.Write(ctx, func(tx *sql.Tx) error {
		now := time.Now()
		row, err := insertRun(ctx, tx, cid, insertRunInput{Kind: runKindContract, ChallengeVersion: 1, SessionID: "22222222-2222-4222-8222-222222222222",
			PID: 1, Host: "h", HeartbeatAt: now, StartedAt: now, MaxBudgetUSD: 1, BudgetBucket: budgetBucketImpl})
		if err != nil {
			return err
		}
		id, _ := parseRunID(row.ID)
		if _, err := updateRunEnd(ctx, tx, id, updateRunEndInput{EndedAt: now, Result: RunResultSucceeded}); err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, cost_usd, max_budget_usd, started_at, observed_at)
			VALUES (?, 'tool', 'ext-1', ?, 'running', 's', 0, 5000000, 'x', 'x')`, cid, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	conn, err := parseConnectorsDeclaration([]byte(contractConnectorsJSON))
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeContractClient{last: contractOut(ContractStateRunning, nil, nil)}
	in := f.input(mineThenPlanInvoker(t))
	in.ConnDecl, in.Contract = conn, client
	f.run(t, in)
	if len(client.statuses) != 1 {
		t.Fatalf("statuses = %d, want 1（周が契約の run の状態を読む）", len(client.statuses))
	}
}

// 差し戻しの再開の注記（本番反映のやり直し）は、戻した後に始まった契約の run があれば返さない（委譲の run と同じ）。
func TestLoadReopenNote_ContractRunAfterTheReopenSuppressesTheNote(t *testing.T) {
	f := newContractFixture(t)
	ctx := context.Background()
	cid, _ := parseChallengeID(f.cid)
	reopenedAt := time.Now().Add(-time.Hour)
	if err := f.s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO operation (challenge_id, kind, summary, ref, state, created_at) VALUES (?, 'release', 's', 'o/r#1', 'pending', ?)`, cid, formatTimestamp(reopenedAt))
		if err != nil {
			return err
		}
		op, _ := res.LastInsertId()
		_, err = tx.Exec(`INSERT INTO challenge_reopen (challenge_id, reason, operation_id, new_operation_id, created_at) VALUES (?, 'merge_conflict', ?, ?, ?)`,
			cid, op, op, formatTimestamp(reopenedAt))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	note := func() *reopenNote {
		var n *reopenNote
		readForTest(t, f.s, func(tx *sql.Tx) (err error) { n, err = loadReopenNote(ctx, tx, cid); return err })
		return n
	}
	if note() == nil {
		t.Fatal("note = nil before any run, want the reopen note")
	}
	if _, err := f.start(t); err != nil { // 戻した後に契約の run（kind=contract）が始まる
		t.Fatal(err)
	}
	if n := note(); n != nil {
		t.Fatalf("note = %+v after a contract run started, want none", n)
	}
}

// 保留に結び付けた後に求められた操作が変わったら、古い保留の答えを新しい問いへの答えとして返さない。
func TestPollContractRuns_AnswerIsNotReturnedAfterTheRequestedActionChanged(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{
		waitingOut(approveAction(), nil),
		waitingOut(&ContractRequestedAction{Kind: "answer", Decider: "human", Text: "OTHER"}, nil),
	}
	f.poll(t)
	f.poll(t) // 保留中に別の求めへ変わる
	f.answerLatestHold(t, f.cid, "OLD-ANSWER")
	f.poll(t)
	if len(f.client.resumes) != 0 {
		t.Fatalf("resumes = %v, want none for an answer to a replaced question", f.client.resumes)
	}
}

// resume の引数を埋められないとき、答えを失わない（結び付きを外さない）。
func TestPollContractRuns_UnfillableResumeKeepsTheAnswerLink(t *testing.T) {
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.client.queue = []ContractLaunchOutput{waitingOut(approveAction(), nil)}
	f.poll(t)
	f.answerLatestHold(t, f.cid, "A")
	f.conn.Connectors[0].Commands.Resume = []string{"tool", "resume", "{issue_number}"}
	if _, err := f.s.PollContractRuns(context.Background(), ContractPollInput{AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client, CycleID: f.cycle(t, 300)}); err == nil {
		t.Fatal("want the fill error")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM contract_run WHERE wait_hold_id IS NOT NULL`); n != 1 {
		t.Fatalf("answer link cleared without a resume (%d linked)", n)
	}
}

// 差し戻しの後の 2 回目の契約の run は、1 回目に使った分を引いた残りの合計を上限額に記録し、
// 2 回目の外部の run の累計（cost_usd）はその上限額とだけ比べる（1 回目の費用を足して比べない）。
func TestRunDelegation_CLISecondContractAfterSendBackDoesNotCountTheFirstCostTwice(t *testing.T) {
	f := newContractFixture(t)
	ctx := context.Background()
	in := f.input(f.cycle(t, 300), idPtr(f.cid))
	in.Contract = f.client
	ch := f.detail(t, f.cid).Challenge
	dc, err := f.s.loadDelegationContext(ctx, in, ch)
	if err != nil {
		t.Fatal(err)
	}
	original := dc.ImplBudgetMicros + dc.ReviewBudgetMicros
	if dc.ImplBudgetMicros <= 0 || dc.ReviewBudgetMicros <= 0 {
		t.Fatalf("impl=%d review=%d, want both frames positive", dc.ImplBudgetMicros, dc.ReviewBudgetMicros)
	}
	maxBudgets := func() []int64 {
		var out []int64
		readForTest(t, f.s, func(tx *sql.Tx) error {
			rows, err := tx.Query(`SELECT max_budget_usd FROM contract_run ORDER BY id`)
			if err != nil {
				return err
			}
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var v int64
				if err := rows.Scan(&v); err != nil {
					return err
				}
				out = append(out, v)
			}
			return rows.Err()
		})
		return out
	}

	// 1 回目: 実装の枠の半分を使って succeeded で終わり、検証中へ進む。
	if _, err := f.s.RunDelegation(ctx, in); err != nil {
		t.Fatal(err)
	}
	firstMicros := dc.ImplBudgetMicros / 2
	first := microsToUSD(firstMicros)
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateSucceeded, &first, func(st *ContractStatus) {
		st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: "https://github.com/o/r/pull/9"}}
	})}
	f.poll(t)
	if got := f.status(t, f.cid); got != StatusVerifying {
		t.Fatalf("status = %s, want verifying after the first contract run", got)
	}

	// 差し戻し: 課題を着手中へ戻し、2 回目の契約の run を始める。
	cid, _ := parseChallengeID(f.cid)
	if _, err := f.exec(`UPDATE challenge SET status = 'in_progress', version = version + 1 WHERE id = ?`, cid); err != nil {
		t.Fatal(err)
	}
	ext2 := func(st *ContractStatus) { st.RunID = "ext-2" }
	f.client.startOut = contractOut(ContractStateRunning, nil, ext2)
	in2 := f.input(f.cycle(t, 300), idPtr(f.cid))
	in2.Contract = f.client
	if _, err := f.s.RunDelegation(ctx, in2); err != nil {
		t.Fatal(err)
	}
	if len(f.client.starts) != 2 {
		t.Fatalf("starts = %d, want 2 (the second contract run after the send-back)", len(f.client.starts))
	}
	secondMax := original - firstMicros
	if got := maxBudgets(); len(got) != 2 || got[0] != original || got[1] != secondMax {
		t.Fatalf("max_budget_usd = %v, want [%d %d]（2 回目は使った分を引いた残りの合計）", got, original, secondMax)
	}

	// 2 回目の累計がその上限額ちょうど: 1 回目の費用と足すと 2 回目の上限額を上回るが、中止しない。
	atMax := microsToUSD(secondMax)
	if firstMicros+secondMax <= secondMax {
		t.Fatal("test setup: first + second must exceed the second run's budget")
	}
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, &atMax, ext2)}
	f.poll(t)
	if len(f.client.cancels) != 0 {
		t.Fatalf("cancels = %v, want none（2 回目の費用は 2 回目の上限額とだけ比べる）", f.client.cancels)
	}
	if got := f.status(t, f.cid); got != StatusInProgress {
		t.Fatalf("status = %s, want in_progress", got)
	}

	// 2 回目の上限額を上回ったら中止する（比べる相手が 2 回目の上限額であること）。
	over := microsToUSD(secondMax) + 0.01
	f.client.cancelOut = contractOut(ContractStateCancelled, &over, ext2)
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, &over, ext2)}
	f.poll(t)
	if len(f.client.cancels) != 1 || !reflect.DeepEqual(f.client.cancels[0], []string{"tool", "cancel", "ext-2"}) {
		t.Fatalf("cancels = %v, want one cancel for ext-2 over the second run's budget", f.client.cancels)
	}
}
