package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core/coretest"
)

const execBase = "2026-10-22T00:00:00.000Z"

func execTime(sec int) time.Time {
	return time.Date(2026, 10, 22, 0, 0, sec, 0, time.UTC)
}

func execTS(sec int) string { return formatTimestamp(execTime(sec)) }

// releaseFixtureForTest は課題 1 件と、その課題の承認済みの release（head_sha つきの承認の記録）を作る。
func releaseFixtureForTest(t *testing.T) (s *Store, challengeID, opID int64) {
	t.Helper()
	s = newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	challengeID = mustParseChallengeID(t, c.ID)
	opID = coretest.InsertApprovedRelease(t, s.workspace, int(challengeID), "https://github.com/o/r/pull/1", "a1b2c3", execTS(0))
	return s, challengeID, opID
}

func writeForTest(t *testing.T, s *Store, fn func(tx *sql.Tx) error) {
	t.Helper()
	if err := s.db.Write(context.Background(), fn); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readForTest(t *testing.T, s *Store, fn func(tx *sql.Tx) error) {
	t.Helper()
	if err := s.db.Read(context.Background(), fn); err != nil {
		t.Fatalf("read: %v", err)
	}
}

// --- 状態 executed は未承認にも承認済み（未実行）にも数えない（AC-136・AC-137） ---

func TestOperationStateExecuted_IsNotCountedInStatusLists(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	cid := int(mustParseChallengeID(t, c.ID))
	coretest.InsertReleaseOperation(t, s.workspace, cid, "https://github.com/o/r/pull/1", string(OperationStateExecuted))

	ov, err := s.GetOverview(context.Background())
	if err != nil {
		t.Fatalf("GetOverview() error = %v", err)
	}
	if len(ov.NeedsHumanOperations) != 0 {
		t.Errorf("NeedsHumanOperations = %+v, want empty (executed is not pending)", ov.NeedsHumanOperations)
	}
	if len(ov.ApprovedOperations) != 0 {
		t.Errorf("ApprovedOperations = %+v, want empty (executed is not approved-and-unexecuted)", ov.ApprovedOperations)
	}
}

func TestDeriveCard_ExecutedOperationIsNotAPendingApproval(t *testing.T) {
	st := &Overview{NeedsHumanOperations: []IrreversibleOperation{{ChallengeID: "C-1", State: OperationStateExecuted}}}
	card := deriveCard(Challenge{ID: "C-1", Status: StatusDone}, st, nil, nil, time.Now(), defaultStaleAfter)
	if len(card.NextHumanActions) != 0 {
		t.Errorf("actions = %v, want none", card.NextHumanActions)
	}
}

// --- M3P15: executed の release は承認済みとして数える ---

func TestReleaseApproved_CountsExecutedButNotPendingOrRejected(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	cid := mustParseChallengeID(t, c.ID)
	for state, pr := range map[OperationState]string{
		OperationStateApproved: "https://github.com/o/r/pull/1",
		OperationStateExecuted: "https://github.com/o/r/pull/2",
		OperationStatePending:  "https://github.com/o/r/pull/3",
		OperationStateRejected: "https://github.com/o/r/pull/4",
	} {
		coretest.InsertReleaseOperation(t, s.workspace, int(cid), pr, string(state))
	}
	for pr, want := range map[string]bool{
		"https://github.com/o/r/pull/1": true,
		"https://github.com/o/r/pull/2": true,
		"https://github.com/o/r/pull/3": false,
		"https://github.com/o/r/pull/4": false,
		"https://github.com/o/r/pull/5": false,
	} {
		got, err := s.releaseApproved(context.Background(), cid, pr)
		if err != nil {
			t.Fatalf("releaseApproved(%s) error = %v", pr, err)
		}
		if got != want {
			t.Errorf("releaseApproved(%s) = %v, want %v", pr, got, want)
		}
	}
}

// --- 試行の書き込み・閉じる・読む（AC-139・AC-140） ---

func TestOperationExecution_BeginCloseRoundTrip(t *testing.T) {
	s, _, opID := releaseFixtureForTest(t)
	ctx := context.Background()
	var id int64
	writeForTest(t, s, func(tx *sql.Tx) error {
		r, err := beginOperationExecution(ctx, tx, opID, beginOperationExecutionInput{
			StartedAt: execTime(1), ApprovedHead: "a1b2c3", PID: 42, Host: "h1",
		})
		if err != nil {
			return err
		}
		id = r.ID
		if !r.open() || r.Result != "" || r.ApprovedHead != "a1b2c3" || r.PID != 42 || r.Host != "h1" || !r.StartedAt.Equal(execTime(1)) {
			t.Errorf("begun row = %+v", r)
		}
		return nil
	})
	writeForTest(t, s, func(tx *sql.Tx) error {
		return closeOperationExecution(ctx, tx, id, closeOperationExecutionInput{
			EndedAt: execTime(2), Result: ExecutionResultMerged, ObservedHead: "a1b2c3", Detail: "d",
		})
	})
	readForTest(t, s, func(tx *sql.Tx) error {
		rows, err := listOperationExecutions(ctx, tx, opID)
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			t.Fatalf("rows = %d, want 1", len(rows))
		}
		r := rows[0]
		if r.open() || r.Result != ExecutionResultMerged || r.ObservedHead != "a1b2c3" || r.Detail != "d" || !r.EndedAt.Equal(execTime(2)) {
			t.Errorf("closed row = %+v", r)
		}
		latest, err := latestOperationExecution(ctx, tx, opID)
		if err != nil || latest == nil || latest.ID != id {
			t.Errorf("latestOperationExecution = %+v, %v", latest, err)
		}
		none, err := latestOperationExecution(ctx, tx, opID+100)
		if err != nil || none != nil {
			t.Errorf("latestOperationExecution(no attempts) = %+v, %v; want nil", none, err)
		}
		return nil
	})
}

func TestOperationExecution_SecondActiveAttemptIsRejectedUntilFirstIsClosed(t *testing.T) {
	s, _, opID := releaseFixtureForTest(t)
	ctx := context.Background()
	in := beginOperationExecutionInput{StartedAt: execTime(1), PID: 1, Host: "h"}
	var first int64
	writeForTest(t, s, func(tx *sql.Tx) error {
		r, err := beginOperationExecution(ctx, tx, opID, in)
		if r != nil {
			first = r.ID
		}
		return err
	})
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := beginOperationExecution(ctx, tx, opID, in)
		return err
	})
	if !errors.Is(err, errActiveExecutionExists) {
		t.Fatalf("second begin error = %v, want errActiveExecutionExists", err)
	}
	writeForTest(t, s, func(tx *sql.Tx) error {
		return closeOperationExecution(ctx, tx, first, closeOperationExecutionInput{EndedAt: execTime(2), Result: ExecutionResultWaitingChecks})
	})
	writeForTest(t, s, func(tx *sql.Tx) error {
		_, err := beginOperationExecution(ctx, tx, opID, in)
		return err
	})
}

func TestOperationExecution_ValidationAndDoubleClose(t *testing.T) {
	s, _, opID := releaseFixtureForTest(t)
	ctx := context.Background()
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := beginOperationExecution(ctx, tx, 0, beginOperationExecutionInput{StartedAt: execTime(1), PID: 1, Host: "h"})
		return err
	}); !errors.Is(err, ErrValidation) {
		t.Errorf("begin (op 0) error = %v, want ErrValidation", err)
	}
	for name, in := range map[string]beginOperationExecutionInput{
		"no host":      {StartedAt: execTime(1), PID: 1},
		"no pid":       {StartedAt: execTime(1), Host: "h"},
		"no time":      {PID: 1, Host: "h"},
		"negative pid": {StartedAt: execTime(1), PID: -1, Host: "h"},
	} {
		err := s.db.Write(ctx, func(tx *sql.Tx) error {
			_, err := beginOperationExecution(ctx, tx, opID, in)
			return err
		})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("begin (%s) error = %v, want ErrValidation", name, err)
		}
	}
	var id int64
	writeForTest(t, s, func(tx *sql.Tx) error {
		r, err := beginOperationExecution(ctx, tx, opID, beginOperationExecutionInput{StartedAt: execTime(1), PID: 1, Host: "h"})
		if r != nil {
			id = r.ID
		}
		return err
	})
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		return closeOperationExecution(ctx, tx, id, closeOperationExecutionInput{EndedAt: execTime(2), Result: "bogus"})
	})
	if !errors.Is(err, ErrValidation) {
		t.Errorf("close with unknown result error = %v, want ErrValidation", err)
	}
	writeForTest(t, s, func(tx *sql.Tx) error {
		return closeOperationExecution(ctx, tx, id, closeOperationExecutionInput{EndedAt: execTime(2), Result: ExecutionResultInterrupted})
	})
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		return closeOperationExecution(ctx, tx, id, closeOperationExecutionInput{EndedAt: execTime(3), Result: ExecutionResultMerged})
	})
	if !errors.Is(err, errExecutionNotOpen) {
		t.Errorf("double close error = %v, want errExecutionNotOpen", err)
	}
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		return closeOperationExecution(ctx, tx, id+99, closeOperationExecutionInput{EndedAt: execTime(3), Result: ExecutionResultMerged})
	})
	if !errors.Is(err, errExecutionNotOpen) {
		t.Errorf("close of missing attempt error = %v, want errExecutionNotOpen", err)
	}
}

// --- 連続の回数の導出 ---

func TestConsecutiveFailures(t *testing.T) {
	type attempt struct {
		sec    int
		result ExecutionResult // 空なら終了していない試行
	}
	tests := []struct {
		name     string
		attempts []attempt
		want     int
	}{
		{"no attempts", nil, 0},
		{"counting results of different kinds are consecutive", []attempt{
			{10, ExecutionResultChecksFailed}, {20, ExecutionResultNotMergeable}, {30, ExecutionResultMergeFailed}}, 3},
		{"non-counting waits neither add nor break the streak", []attempt{
			{10, ExecutionResultChecksFailed}, {20, ExecutionResultWaitingChecks}, {30, ExecutionResultUpstreamFailed},
			{40, ExecutionResultMergeUnconfirmed}, {50, ExecutionResultChecksFailed}}, 2},
		{"interrupted neither adds nor breaks the streak", []attempt{
			{10, ExecutionResultMergeFailed}, {20, ExecutionResultInterrupted}, {30, ExecutionResultMergeFailed}}, 2},
		{"an unfinished attempt is not counted", []attempt{
			{10, ExecutionResultNotMergeable}, {20, ""}}, 1},
		{"attempts before the last cancellation are not counted", []attempt{
			{10, ExecutionResultChecksFailed}, {20, ExecutionResultChecksFailed}, {30, ExecutionResultHeadChanged},
			{50, ExecutionResultMergeFailed}}, 1},
		{"only non-counting results after re-approval", []attempt{
			{10, ExecutionResultChecksFailed}, {30, ExecutionResultClosed}, {50, ExecutionResultWaitingChecks}}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _, opID := releaseFixtureForTest(t)
			// 時計に依存しないことを固定する: 試行の開始・終了はすべて同じ時刻にし、
			// 承認し直した記録も同じ時刻で足す（境界は行の順だけで決まる）。
			for _, a := range tt.attempts {
				att := coretest.ExecutionAttempt{StartedAt: execTS(0), ApprovedHead: "a1b2c3"}
				if a.result != "" {
					att.EndedAt, att.Result = execTS(0), string(a.result)
				}
				coretest.InsertOperationExecution(t, s.workspace, opID, att)
				if c, _ := a.result.Class(); c == ExecutionClassCancel {
					coretest.InsertReleaseApproval(t, s.workspace, opID, "d4e5f6", execTS(0))
				}
			}
			readForTest(t, s, func(tx *sql.Tx) error {
				got, err := consecutiveFailures(context.Background(), tx, opID)
				if err != nil {
					return err
				}
				if got != tt.want {
					t.Errorf("consecutiveFailures = %d, want %d", got, tt.want)
				}
				return nil
			})
		})
	}
}

func TestConsecutiveFailures_IgnoresOtherOperations(t *testing.T) {
	s, cid, op1 := releaseFixtureForTest(t)
	op2 := coretest.InsertApprovedRelease(t, s.workspace, int(cid), "https://github.com/o/r/pull/2", "ff", execTS(0))
	coretest.InsertOperationExecution(t, s.workspace, op2, coretest.ExecutionAttempt{
		StartedAt: execTS(10), EndedAt: execTS(11), Result: string(ExecutionResultChecksFailed)})
	coretest.InsertOperationExecution(t, s.workspace, op1, coretest.ExecutionAttempt{
		StartedAt: execTS(200), EndedAt: execTS(201), Result: string(ExecutionResultNotMergeable)})
	readForTest(t, s, func(tx *sql.Tx) error {
		for opID, want := range map[int64]int{op1: 1, op2: 1} {
			got, err := consecutiveFailures(context.Background(), tx, opID)
			if err != nil {
				return err
			}
			if got != want {
				t.Errorf("consecutiveFailures(op %d) = %d, want %d", opID, got, want)
			}
		}
		return nil
	})
}

// --- coretest のフィクスチャ（後続のチケットが使う） ---

func TestCoretestFixtures_ApprovalHeadAndVerifiedHead(t *testing.T) {
	s, cid, opID := releaseFixtureForTest(t)
	noHead := coretest.InsertApprovedRelease(t, s.workspace, int(cid), "https://github.com/o/r/pull/9", "", execBase)
	runID := coretest.InsertRun(t, s.workspace, coretest.InsertRunInput{
		ChallengeID: int(cid), Kind: "delegate", ChallengeVersion: 1, SessionID: "s", PID: 1, Host: "h",
		HeartbeatAt: execBase, StartedAt: execBase, MaxBudgetUSD: 1, BudgetBucket: "impl",
	})
	coretest.InsertRunArtifact(t, s.workspace, runID, "pr", "https://github.com/o/r/pull/1", "open", "main")
	coretest.SetRunArtifactVerifiedHead(t, s.workspace, runID, "https://github.com/o/r/pull/1", "a1b2c3")
	attemptID := coretest.InsertOperationExecution(t, s.workspace, opID, coretest.ExecutionAttempt{StartedAt: execBase})

	readForTest(t, s, func(tx *sql.Tx) error {
		var state string
		var head, verified sql.NullString
		if err := tx.QueryRow(`SELECT o.state, a.head_sha FROM operation o JOIN approval a ON a.operation_id = o.id WHERE o.id = ?`, opID).Scan(&state, &head); err != nil {
			return err
		}
		if state != "approved" || head.String != "a1b2c3" {
			t.Errorf("approved release: state=%q head_sha=%v", state, head)
		}
		if err := tx.QueryRow(`SELECT head_sha FROM approval WHERE operation_id = ?`, noHead).Scan(&head); err != nil {
			return err
		}
		if head.Valid {
			t.Errorf("head_sha = %v, want NULL", head)
		}
		if err := tx.QueryRow(`SELECT verified_head_sha FROM run_artifact WHERE run_id = ?`, runID).Scan(&verified); err != nil {
			return err
		}
		if verified.String != "a1b2c3" {
			t.Errorf("verified_head_sha = %v", verified)
		}
		r, err := getOperationExecution(context.Background(), tx, attemptID)
		if err != nil {
			return err
		}
		if !r.open() || r.Host != "testhost" || r.PID != 1 {
			t.Errorf("attempt = %+v", r)
		}
		return nil
	})
}
