package core

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// 上限だけを変えた承認は、枠の使い切りの止まりを解かない。枠を増やした承認だけが解く。

func (f *delegateFixture) stopTaskForBudget(t *testing.T, cid, task int64) {
	t.Helper()
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO run (challenge_id, kind, challenge_version, plan_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket, task_id)
			VALUES (?, 'contract', 1, 1, 's', 1, 'h', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:01.000Z', 'budget_exhausted', 1000000, 'impl', ?)`, cid, task)
		return err
	})
}

func TestBudgetLimit_DoesNotReleaseABudgetExhaustedStop(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(30, 2, 40, 5))
	cid, _ := parseChallengeID(id)
	tasks := taskRows(t, f.s, cid)
	a := mustTaskID(t, tasks[0].ID)
	f.stopTaskForBudget(t, cid, a)
	if _, err := f.s.ExecuteBudget(context.Background(), f.limitRequest(t, id, 100), f.budgetAttestation(t, id)); err != nil {
		t.Fatal(err)
	}
	if got := f.overview(t).NeedsHumanBudgetExhausted; len(got) != 1 || got[0].TaskID != tasks[0].ID {
		t.Fatalf("budget_exhausted = %+v, want task %s still stopped after --limit", got, tasks[0].ID)
	}
	var sp bucketSpend
	readForTest(t, f.s, func(tx *sql.Tx) (err error) {
		sp, err = loadTaskBucketSpend(context.Background(), tx, a, 1)
		return err
	})
	if !sp.ImplStopped {
		t.Errorf("ImplStopped = false after --limit, want true")
	}
	// 枠を増やす操作では解ける。
	if _, err := f.raise(t, id, tasks[0].ID, 40, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.overview(t).NeedsHumanBudgetExhausted; len(got) != 0 {
		t.Fatalf("budget_exhausted = %+v, want none after raising the frame", got)
	}
}

func TestBudgetLimit_ChallengeLevelStopIsNotReleasedByLimitOnly(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 4, 5))
	cid, _ := parseChallengeID(id)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO run (challenge_id, kind, challenge_version, plan_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket)
			VALUES (?, 'contract', 1, 1, 's', 1, 'h', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:01.000Z', 'budget_exhausted', 1000000, 'impl')`, cid)
		return err
	})
	check := func(want bool) {
		t.Helper()
		var sp bucketSpend
		readForTest(t, f.s, func(tx *sql.Tx) (err error) {
			sp, err = loadBucketSpend(context.Background(), tx, cid, 1)
			return err
		})
		if sp.ImplStopped != want {
			t.Errorf("ImplStopped = %v, want %v", sp.ImplStopped, want)
		}
	}
	check(true)
	if _, err := f.s.ExecuteBudget(context.Background(), f.limitRequest(t, id, 100), f.budgetAttestation(t, id)); err != nil {
		t.Fatal(err)
	}
	check(true)
}

func TestHoldAnswerTaskAddition_AddDoesNotReleaseABudgetExhaustedStop(t *testing.T) {
	t.Parallel()
	f := overLimitHeld(t)
	b := mustTaskID(t, f.taskByKey(t, "b").ID)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO run (challenge_id, kind, challenge_version, plan_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket, task_id)
			VALUES (?, 'contract', 1, 1, 's', 1, 'h', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:01.000Z', 'budget_exhausted', 1000000, 'impl', ?)`, f.cid(), b)
		return err
	})
	if _, err := f.answerHold(t, taskAddOptionAdd); err != nil {
		t.Fatal(err)
	}
	if got := f.limitMicros(t); got != 15_000_000 {
		t.Fatalf("limit = %d, want 15000000 (the add answer raised it)", got)
	}
	var sp bucketSpend
	readForTest(t, f.s, func(tx *sql.Tx) (err error) {
		sp, err = loadTaskBucketSpend(context.Background(), tx, b, 1)
		return err
	})
	if !sp.ImplStopped {
		t.Errorf("ImplStopped = false after the add answer, want true")
	}
}

// 足す依頼の問いは、保留に記録した足す依頼で選ぶ。別の足す依頼の問いと回答は入らない。

func TestExpandQuestionAnswerText_IsolatesAdditionsOfTheSameChallenge(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	f.expand(t, &expandRecorder{out: []byte(`{"verdict":"ask","question":"最初の問い"}`)})
	f.answerAsk(t, "", "最初の答え")
	f.expand(t, &expandRecorder{out: []byte(`{"verdict":"ask","question":"次の問い"}`)})
	f.answerAsk(t, "", "次の答え")

	first := f.additions(t)[0]
	firstID, _ := parseTaskAdditionID(first.ID)
	// 同じ課題の 2 つ目の足す依頼（別の出どころのタスク）が、あとから問いを立てて答えられた。
	var secondID, hid int64
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO task_addition (challenge_id, origin, origin_task_id, state, created_at)
			VALUES (?, 'expand', (SELECT id FROM task WHERE challenge_id = ? AND key = 'b'), 'pending', '2026-11-01T00:00:00.000Z')`, f.cid(), f.cid())
		if err != nil {
			return err
		}
		secondID, _ = res.LastInsertId()
		hr, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at, answer, answered_at, answered_by, reason)
			VALUES (?, '別の問い', 'in_progress', '2026-11-01T00:00:01.000Z', '別の答え', '2026-11-01T00:00:02.000Z', 'x', ?)`, f.cid(), string(holdReasonTaskAddQuestion))
		if err != nil {
			return err
		}
		hid, _ = hr.LastInsertId()
		return nil
	})
	// 足す依頼の保留を結ぶ本番の書き込み（保留の側へ足す依頼を記録する）。
	err := f.s.mutateAs(context.Background(), "t", ChannelCLI, VerificationNone, func(tx *sql.Tx, rec *activityRecorder) error {
		_, err := setTaskAdditionStateTx(context.Background(), tx, rec, secondID, TaskAdditionPending, TaskAdditionUpdate{HoldID: &hid})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	text := func(aid int64) string {
		var out string
		readForTest(t, f.s, func(tx *sql.Tx) (err error) {
			out, err = expandQuestionAnswerText(context.Background(), tx, aid)
			return err
		})
		return out
	}
	got1, got2 := text(firstID), text(secondID)
	if !strings.Contains(got1, "最初の問い") || !strings.Contains(got1, "次の答え") || strings.Contains(got1, "別の問い") {
		t.Errorf("first = %q, want its own two rounds only", got1)
	}
	if !strings.Contains(got2, "別の問い") || strings.Contains(got2, "最初の問い") || strings.Contains(got2, "次の答え") {
		t.Errorf("second = %q, want only its own round", got2)
	}
	if strings.Index(got1, "最初の問い") > strings.Index(got1, "次の問い") {
		t.Errorf("first = %q, want oldest first", got1)
	}
}
