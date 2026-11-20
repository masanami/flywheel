package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// 仕上げ 1 項目 1: 「入れてよい」を返した後の succeeded を最初の読みで確かめきれなかったとき、次の状態の確認でやり直しても、
// 統合先へ入っている成果物を「入っていない」と判定しない。
func TestStuckPaths_LandedSucceededRetryWithEmptyStatusArtifactsDoesNotHoldDelegateEnded(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.landed(t)
	f.checks.err = errors.New("boom")
	f.succeed(t) // 上流を読めない: 何も書かない（行は開いたまま、状態は succeeded）
	if f.taskStatus(t) != "verifying" || f.count(t, `SELECT COUNT(*) FROM hold`) != 0 {
		t.Fatalf("precondition: task = %s", f.taskStatus(t))
	}
	f.checks.err = nil
	// 次の状態の確認は、成果物の一覧を返さない（記録済みの状態が終わりなので、読み直した status から引けない）。
	f.read(t, contractOut(ContractStateSucceeded, nil, nil))
	if got := f.count(t, `SELECT COUNT(*) FROM hold`); got != 0 {
		t.Fatalf("holds = %d (reason %q), want 0", got, f.lastHoldReason(t))
	}
	if f.taskStatus(t) != "done" {
		t.Fatalf("task = %s, want done", f.taskStatus(t))
	}
}

// launchTaskIDs は、いまの記録で委譲の段が起動するタスクの ID。
func (f *igLandFixture) launchTaskIDs(t *testing.T) []string {
	t.Helper()
	cid, _ := parseChallengeID(f.cid)
	var sel *launchSelection
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
		sel, err = selectLaunchTasks(context.Background(), tx, f.conn, f.cid, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, tk := range sel.Tasks {
		ids = append(ids, tk.ID)
	}
	return ids
}

// 項目 2: 検証中のタスクの実行が failed・cancelled で終わったとき、保留への回答の後に、そのタスクを委譲し直せる。
func TestStuckPaths_VerifyingTaskRunFailedOrCancelledIsRelaunchableAfterAnswer(t *testing.T) {
	t.Parallel()
	for _, state := range []ContractState{ContractStateFailed, ContractStateCancelled} {
		state := state
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			f := newIgLandFixture(t)
			if f.taskStatus(t) != "verifying" {
				t.Fatalf("precondition: task = %s", f.taskStatus(t))
			}
			f.read(t, contractOut(state, nil, nil))
			if f.status(t, f.cid) != StatusAwaitingHuman {
				t.Fatalf("status = %s", f.status(t, f.cid))
			}
			f.answer(t, "続けてください")
			if f.status(t, f.cid) != StatusInProgress {
				t.Fatalf("status = %s", f.status(t, f.cid))
			}
			if got := f.launchTaskIDs(t); len(got) != 1 {
				t.Fatalf("launchable tasks = %v (task status %s), want the failed task", got, f.taskStatus(t))
			}
		})
	}
}

// 項目 6: レビュー待ちを経ない成功（land_ref が無い）で、保留の書き込みが失敗しても、終わりの記録だけが残らない。次の状態の確認が読み直して保留にする。
func TestStuckPaths_SucceededWithoutLandWritesTheHoldAndTheEndedRecordTogether(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.sql(t, `UPDATE contract_run SET verified_ref = NULL, land_ref = NULL`)
	f.setPR(7, landPRURL, "merged", integBranch) // 成果物は統合先へ入っている（保留の理由は merged_unverified）
	f.sql(t, `CREATE TRIGGER fail_hold BEFORE INSERT ON hold BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateSucceeded, nil, func(st *ContractStatus) { st.Artifacts = landedArts })}
	_, _ = f.s.PollContractRuns(context.Background(), ContractPollInput{
		AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client, CycleID: f.cycle(t, 300), Reconcile: f.branches, Checks: f.checks, Compare: f.compare,
	})
	if f.count(t, `SELECT COUNT(*) FROM hold`) != 0 {
		t.Fatal("a hold remains although its write failed")
	}
	if f.text(t, `SELECT COALESCE(ended_at, '') FROM contract_run`) != "" {
		t.Fatal("the contract run is ended without a hold (the next poll would pick up nothing)")
	}
	f.sql(t, `DROP TRIGGER fail_hold`)
	// やり直しの読みは成果物を返さない（記録済みの状態が終わり）。それでも、書き込みを失敗した最初の読みと同じ理由で保留にする。
	f.read(t, contractOut(ContractStateSucceeded, nil, nil))
	if got := f.lastHoldReason(t); got != string(holdReasonMergedUnverified) {
		t.Fatalf("reason = %q, want merged_unverified", got)
	}
	if f.count(t, `SELECT COUNT(*) FROM hold`) != 1 || f.text(t, `SELECT COALESCE(ended_at, '') FROM contract_run`) == "" {
		t.Fatalf("holds = %d, want the retry to hold and end the run", f.count(t, `SELECT COUNT(*) FROM hold`))
	}
}

// heldWithTaskRunSucceeded は、タスクの契約の実行が「入れてよい」を返さずに（land_ref が無い）走っている間に、課題が保留になった場面を作る。
// タスクの状態は taskStatus（検証中・着手中）。保留は 1 件（いまの理由は、実行の成功とは別のもの）。
func (f *igLandFixture) heldWithTaskRun(t *testing.T, taskStatus string) {
	t.Helper()
	f.sql(t, `UPDATE contract_run SET verified_ref = NULL, land_ref = NULL, land_delivered = 0, state = 'running',
		requested_kind = NULL, requested_decider = NULL, requested_text = NULL, requested_resolvable_by = NULL`)
	f.sql(t, `UPDATE task SET status = ?`, taskStatus)
	f.sql(t, `INSERT INTO hold (challenge_id, question, reason, from_status, raised_at, task_id) VALUES (?, 'q', 'owner_wait', 'in_progress', '2026-11-20T00:00:00.000Z',
		(SELECT task_id FROM contract_run))`, f.intID(t))
	f.sql(t, `UPDATE challenge SET status = 'awaiting_human', version = version + 1`)
	f.setPR(7, landPRURL, "merged", integBranch) // 成果物は統合先へ入っている（保留の理由は merged_unverified）
}

// 保留中に、「入れてよい」を返していないタスクの実行の succeeded を読んでも、契約の実行を終わった記録にしない（開けたまま残す）。
// 回答で着手中へ戻った後の状態の確認が、同じ終わりを読み直して保留（merged_unverified）を立て、そのとき終わった記録にする。
// 検証中のタスクも着手中のタスクも同じ。開いている間は、タスクは二重に起動されず、計画を作り直す回答の「作業している契約の実行」にも数えない。
func TestStuckPaths_SucceededReadWhileHeldIsHandledAfterTheAnswer(t *testing.T) {
	t.Parallel()
	for _, taskStatus := range []string{"verifying", "in_progress"} {
		taskStatus := taskStatus
		t.Run(taskStatus, func(t *testing.T) {
			t.Parallel()
			f := newIgLandFixture(t)
			f.heldWithTaskRun(t, taskStatus)
			f.succeed(t)
			if f.text(t, `SELECT COALESCE(ended_at, '') FROM contract_run`) != "" {
				t.Fatal("the contract run is ended while the challenge is held (the ending would be lost)")
			}
			if f.ref(t, "state") != string(ContractStateSucceeded) {
				t.Fatalf("recorded state = %q, want succeeded", f.ref(t, "state"))
			}
			if got := f.count(t, `SELECT COUNT(*) FROM hold`); got != 1 {
				t.Fatalf("holds = %d, want only the one raised before", got)
			}
			// 開いている間、同じタスクは起動されない。
			if got := f.launchTaskIDs(t); len(got) != 0 {
				t.Fatalf("launchable tasks = %v while the contract run is open, want none", got)
			}
			f.answer(t, "続けてください")
			if f.status(t, f.cid) != StatusInProgress {
				t.Fatalf("status = %s", f.status(t, f.cid))
			}
			// 回答の後・状態の確認の前も、二重に起動されない。
			if got := f.launchTaskIDs(t); len(got) != 0 {
				t.Fatalf("launchable tasks = %v before the next status read, want none", got)
			}
			f.read(t, contractOut(ContractStateSucceeded, nil, nil))
			if got := f.lastHoldReason(t); got != string(holdReasonMergedUnverified) {
				t.Fatalf("reason = %q, want merged_unverified", got)
			}
			if got := f.count(t, `SELECT COUNT(*) FROM hold`); got != 2 {
				t.Fatalf("holds = %d, want 2 (one before, one raised by the read after the answer)", got)
			}
			if f.text(t, `SELECT COALESCE(ended_at, '') FROM contract_run`) == "" {
				t.Fatal("the contract run is still open after the hold was raised")
			}
		})
	}
}

// 保留中に読んで開けたまま残した succeeded の実行（state は succeeded・land_ref は空）は、計画を作り直す回答の「作業している契約の実行」に数えない。
func TestStuckPaths_OpenSucceededRunWhileHeldDoesNotBlockReplan(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.heldWithTaskRun(t, "in_progress")
	f.succeed(t)
	p, err := f.s.PrepareAnswer(context.Background(), f.cid, "NEW-SCOPE")
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: f.cid, ExpectedVersion: p.Version, Answer: "NEW-SCOPE", Replan: true},
		verifiedAttestationForTest(t, f.cid))
	if err != nil || c.Status != StatusClassified {
		t.Fatalf("replan with an open succeeded run: %+v err %v, want classified", c, err)
	}
}

// 保留を立てようが無い課題（取り下げ）の succeeded は、いまのとおり終わった記録にする（開けたまま残さない）。
func TestStuckPaths_SucceededReadOfAWithdrawnChallengeEndsTheRun(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.heldWithTaskRun(t, "in_progress")
	f.sql(t, `UPDATE challenge SET status = 'withdrawn'`)
	f.succeed(t)
	if f.text(t, `SELECT COALESCE(ended_at, '') FROM contract_run`) == "" {
		t.Fatal("the contract run of a withdrawn challenge stays open")
	}
}

// 保留中に読んで開けたまま残した succeeded の実行は、そのタスクが取りやめになったとき、取りやめのタスクの実行の取り消し（obsoleteContractRun）が
// 取り消して終わった記録にする。保留は増えない。
func TestStuckPaths_OpenSucceededRunWhileHeldIsCancelledWhenItsTaskIsCancelled(t *testing.T) {
	t.Parallel()
	f := newIgLandFixture(t)
	f.heldWithTaskRun(t, "in_progress")
	f.succeed(t)
	f.sql(t, `UPDATE task SET status = 'cancelled'`)
	f.client.cancelOut = contractOut(ContractStateCancelled, nil, nil)
	f.pollLand(t)
	if len(f.client.cancels) != 1 {
		t.Fatalf("cancels = %d, want 1", len(f.client.cancels))
	}
	if f.text(t, `SELECT COALESCE(ended_at, '') FROM contract_run`) == "" {
		t.Fatal("the contract run of the cancelled task is still open")
	}
	if got := f.count(t, `SELECT COUNT(*) FROM hold`); got != 1 {
		t.Fatalf("holds = %d, want only the one raised before", got)
	}
}
