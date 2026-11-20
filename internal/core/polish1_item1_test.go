package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// 仕上げ 1 項目 1: 「入れてよい」を返した後の succeeded を最初の読みで確かめきれなかったとき、次の状態の確認でやり直しても、
// 統合先へ入っている成果物を「入っていない」と判定しない。
func TestPolish1_LandedSucceededRetryWithEmptyStatusArtifactsDoesNotHoldDelegateEnded(t *testing.T) {
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
func TestPolish1_VerifyingTaskRunFailedOrCancelledIsRelaunchableAfterAnswer(t *testing.T) {
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
func TestPolish1_SucceededWithoutLandWritesTheHoldAndTheEndedRecordTogether(t *testing.T) {
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
