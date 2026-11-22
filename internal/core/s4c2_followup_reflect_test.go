package core

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"
)

// 区切り S4c-2 の 4: 上げる前の値（--preview・要約の元）・上限が変わらないこと・上げ直し・開いている上げる依頼の取り消しと始め直し
// （AC-20〜AC-24・AC-43・AC-76〜AC-79）。

// promoteUpFixture は、着手中で、上げる依頼が開いている足せる課題と、その課題に属する未のフォローアップを作る。
// withOperation が真なら、上げる依頼が読まれて反映の操作ができた状態にする（課題は検証中へ進むので、着手中へ戻して、
// 着手中で操作が残る状態を作る）。偽なら、上げる依頼を始めただけで、反映の操作はまだ無い。
func promoteUpFixture(t *testing.T, withOperation bool) (f *contractFixture, challengeID, followupID string) {
	t.Helper()
	f, challengeID = newUpFixture(t, upConnectorsJSON())
	cid, _ := parseChallengeID(challengeID)
	f.sqlExec(t, `UPDATE challenge SET budget_limit_micros = 100000000, budget_limit_reason = '続きを足す分' WHERE id = ?`, cid)
	f.sqlExec(t, `UPDATE task SET budget_impl_micros = 3000000, budget_review_micros = 2000000 WHERE challenge_id = ?`, cid)
	if withOperation {
		f.openReflectRun(t)
		f.sqlExec(t, `UPDATE challenge SET status = ? WHERE id = ?`, string(StatusInProgress), cid)
	} else {
		f.setUpPR("main")
		f.startUp(t)
	}
	return f, challengeID, s4c2OpenFollowup(t, f.s, challengeID)
}

func (f *contractFixture) openReleaseOperations(t *testing.T) (open, closed int) {
	t.Helper()
	readForTest(t, f.s, func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT COUNT(*) FROM operation WHERE kind = ? AND state IN (?, ?)`,
			string(OperationKindRelease), string(OperationStatePending), string(OperationStateApproved)).Scan(&open); err != nil {
			return err
		}
		return tx.QueryRow(`SELECT COUNT(*) FROM operation WHERE kind = ? AND state = ?`, string(OperationKindRelease), string(OperationStateClosed)).Scan(&closed)
	})
	return
}

// AC-21〜AC-24: 上げる前の値は、足すタスクの枠・足した後の見込み・上限・超える額・上げた後の上限・上げる先の題名を持つ。
func TestPromotePlan_CarriesFramesEstimateLimitAndExceedingAmount_AC21_AC22_AC23_AC24(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t) // 見込み 10 USD・上限 14 USD
	fid := destFixtureFollowup(t, f)
	plan, err := f.s.PrepareFollowupPromote(context.Background(), fid, destSmall, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Dest != FollowupPromoteToTask || plan.TargetChallengeTitle == "" || plan.BudgetImplUSD != 1 || plan.BudgetReviewUSD != 1 ||
		plan.EstimateAfterUSD != 12 || plan.LimitUSD != 14 || plan.OverLimit() || plan.ExceedsByUSD() != 0 || plan.LimitAfterUSD() != 14 {
		t.Errorf("plan (within limit) = %+v", plan)
	}
	over, err := f.s.PrepareFollowupPromote(context.Background(), fid, FollowupPromoteOptions{BudgetImplUSD: destFloat(3), BudgetReviewUSD: destFloat(3)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !over.OverLimit() || over.EstimateAfterUSD != 16 || over.ExceedsByUSD() != 2 || over.LimitAfterUSD() != 16 || over.LimitUSD != 14 {
		t.Errorf("plan (over the limit) = %+v", over)
	}
}

// AC-25: 上げる前の値を読んでも、フォローアップ・課題・足す依頼は変わらない。
func TestPromotePlan_PrepareWritesNothing_AC25(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	fid := destFixtureFollowup(t, f)
	snap := func() [][]any {
		var out [][]any
		readForTest(t, f.s, func(tx *sql.Tx) error {
			for _, q := range []string{`SELECT * FROM followup`, `SELECT id, version, status, budget_limit_micros FROM challenge`, `SELECT COUNT(*) FROM task_addition`, `SELECT COUNT(*) FROM activity`} {
				rows, err := tx.Query(q)
				if err != nil {
					return err
				}
				cols, _ := rows.Columns()
				for rows.Next() {
					vals := make([]any, len(cols))
					ptrs := make([]any, len(cols))
					for i := range vals {
						ptrs[i] = &vals[i]
					}
					if err := rows.Scan(ptrs...); err != nil {
						return err
					}
					out = append(out, vals)
				}
				_ = rows.Close()
			}
			return nil
		})
		return out
	}
	before := snap()
	if _, err := f.s.PrepareFollowupPromote(context.Background(), fid, destSmall, nil); err != nil {
		t.Fatal(err)
	}
	if after := snap(); !reflect.DeepEqual(before, after) {
		t.Errorf("state changed by prepare:\nbefore=%v\nafter =%v", before, after)
	}
}

// AC-43: 足した後の見込みが上限以下のフォローアップをタスクとして上げても、上限は変わらない。
func TestPromoteTask_WithinLimitLeavesTheLimitUnchanged_AC43(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	fid := destFixtureFollowup(t, f)
	limitOf := func() (v int64, reason string) {
		readForTest(t, f.s, func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT budget_limit_micros, budget_limit_reason FROM challenge WHERE id = ?`, f.cid()).Scan(&v, &reason)
		})
		return
	}
	beforeV, beforeR := limitOf()
	if _, res, err := destPromote(t, f.s, fid, destSmall); err != nil || res.Addition == nil {
		t.Fatalf("promote: res=%+v err=%v", res, err)
	}
	if v, r := limitOf(); v != beforeV || r != beforeR || v != 14_000_000 {
		t.Errorf("limit = %d %q, want unchanged %d %q", v, r, beforeV, beforeR)
	}
}

// AC-76: 上げた先の課題が取り下げのフォローアップが、足せる着手中の課題に属するとき、上げ直すと、その課題に足す依頼ができる。
func TestPromoteTask_RepromoteFromAWithdrawnChallengeAddsToTheInProgressChallenge_AC76(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	fid := destFixtureFollowup(t, f)
	// 新しい課題へ上げ、その課題を取り下げにする。
	_, res, err := destPromote(t, f.s, fid, FollowupPromoteOptions{NewChallenge: true})
	if err != nil || res.Challenge == nil {
		t.Fatalf("promote to a challenge: %+v %v", res, err)
	}
	pid, _ := parseChallengeID(res.Challenge.ID)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET status = ? WHERE id = ?`, string(StatusWithdrawn), pid)
		return err
	})
	plan, res2, err := destPromote(t, f.s, fid, destSmall)
	if err != nil {
		t.Fatalf("re-promote: %v", err)
	}
	if plan.Dest != FollowupPromoteToTask || res2.Addition == nil || res2.Addition.ChallengeID != f.id {
		t.Fatalf("plan=%+v res=%+v, want an addition on %s", plan, res2, f.id)
	}
	if got := f.additions(t); len(got) != 1 || got[0].State != TaskAdditionPending {
		t.Errorf("additions = %+v, want one pending", got)
	}
}

// AC-78: 上げる依頼が開いている着手中の課題にタスクとして上げると、同じ書き込みで、作ってあった反映の操作は閉じる。
func TestPromoteTask_ClosesTheReflectOperationInTheSameWrite_AC78(t *testing.T) {
	t.Parallel()
	f, _, fid := promoteUpFixture(t, true)
	if open, _ := f.openReleaseOperations(t); open == 0 {
		t.Fatal("no waiting reflect operation before promoting")
	}
	_, res, err := destPromote(t, f.s, fid, destSmall)
	if err != nil || res.Addition == nil {
		t.Fatalf("promote: res=%+v err=%v", res, err)
	}
	if open, closed := f.openReleaseOperations(t); open != 0 || closed == 0 {
		t.Errorf("operations open=%d closed=%d, want all closed", open, closed)
	}
	if !f.reflectOpen(t) {
		t.Error("the reflect request itself must stay open until the next cycle cancels it")
	}
}

// 上げる依頼が開いていない課題では、上げても反映の操作を閉じる処理は何も起こさない（足す依頼だけができる）。
func TestPromoteTask_WithoutAnOpenReflectRequestClosesNoOperation(t *testing.T) {
	t.Parallel()
	f := newExpandFixture(t)
	fid := destFixtureFollowup(t, f)
	// 上げる依頼に結び付かない、待っている反映の操作（上げる依頼が開いていないので、閉じてはならない）。
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO operation (challenge_id, kind, summary, ref, state, created_at) VALUES (?, 'release', 's', 'o/r#1', 'pending', ?)`, f.cid(), formatTimestamp(time.Now()))
		return err
	})
	if _, res, err := destPromote(t, f.s, fid, destSmall); err != nil || res.Addition == nil {
		t.Fatalf("promote: res=%+v err=%v", res, err)
	}
	if n := len(fuActs(t, f.s, "operation", ActivityActionOpClose)); n != 0 {
		t.Errorf("operation close activities = %d, want 0", n)
	}
	var state string
	readForTest(t, f.s, func(tx *sql.Tx) error { return tx.QueryRow(`SELECT state FROM operation`).Scan(&state) })
	if state != string(OperationStatePending) {
		t.Errorf("operation state = %s, want pending", state)
	}
}

// AC-77・AC-79: 上げる依頼を取り消した課題で、次の 1 回の反映の段が終わると上げる依頼は取り消し済みになり、足したタスクが統合先に入ると上げる依頼が始まる。
func TestPromoteTask_CancelsTheOpenReflectRequestAndRestartsAfterTheAddedTaskLands_AC77_AC79(t *testing.T) {
	t.Parallel()
	for name, withOperation := range map[string]bool{"without an operation": false, "with an operation": true} {
		withOperation := withOperation
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cancelsAndRestarts(t, withOperation)
		})
	}
}

func cancelsAndRestarts(t *testing.T, withOperation bool) {
	t.Helper()
	f, id, fid := promoteUpFixture(t, withOperation)
	cid, _ := parseChallengeID(id)
	var reflectRun string
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT external_run_id FROM contract_run WHERE kind = 'reflect' AND ended_at IS NULL`).Scan(&reflectRun)
	})
	startsBefore := len(f.reflectStarts())
	if _, res, err := destPromote(t, f.s, fid, destSmall); err != nil || res.Addition == nil {
		t.Fatalf("promote: res=%+v err=%v", res, err)
	}
	f.client.cancelOut = contractOut(ContractStateCancelled, nil, nil)
	f.runDelegationStage(t, nil)
	if !reflect.DeepEqual(f.cancelledRunIDs(), []string{reflectRun}) { // AC-77
		t.Fatalf("cancels = %v, want one commands.cancel for %s", f.client.cancels, reflectRun)
	}
	if f.reflectOpen(t) {
		t.Fatal("the reflect request is still open after the next cycle (AC-77)")
	}
	if n := len(f.reflectStarts()); n != startsBefore {
		t.Fatalf("commands.reflect calls = %d, want %d: no restart while the addition is open", n, startsBefore)
	}
	// 足す依頼が足されて（足したタスクが未着手）、まだ統合先に入っていない間は、始めない。
	f.sqlExec(t, `UPDATE task_addition SET state = 'applied' WHERE challenge_id = ?`, cid)
	f.sqlExec(t, `UPDATE task SET status = 'pending' WHERE challenge_id = ? AND key = 'a'`, cid)
	f.client.echoRunning = false
	f.runDelegationStage(t, nil)
	if n := len(f.reflectStarts()); n != startsBefore {
		t.Fatalf("commands.reflect calls = %d, want %d while the added task is unfinished", n, startsBefore)
	}
	// 足したタスクが統合先に入る（済みになる）と、上げる依頼を始め直す。
	setTaskStatusRaw(t, f.s, cid, "a", TaskStatusDone)
	f.sqlExec(t, `UPDATE contract_run SET ended_at = '2026-09-26T00:00:00.000Z' WHERE ended_at IS NULL AND kind = 'task'`)
	f.runDelegationStage(t, nil)
	if n := len(f.reflectStarts()); n != startsBefore+1 { // AC-79
		t.Fatalf("commands.reflect calls = %d, want %d after the added task landed", n, startsBefore+1)
	}
}
