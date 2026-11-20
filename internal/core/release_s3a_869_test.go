package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// S3a の直し（#869）: 持ち越した終わりの判定・完了から戻す確かめ・反映の依頼の同時数の確かめを実ストアで固定する。

// 終わりを読んで持ち越している契約の実行（succeeded）は、委譲先の status を呼ばず、記録した状態から判定し、
// 成果物が承認した中身で入っていれば landed を記録して契約の実行を終わった記録にする。
func TestS3a869_HeldSucceededRunIsJudgedWithoutCallingStatusAndLands(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.holdEnd(t)
	f.merged()
	before := len(f.client.statuses)
	if item := f.one(t); item.Result != ExecutionResultLanded {
		t.Fatalf("item = %+v, want landed", item)
	}
	if got := len(f.client.statuses); got != before {
		t.Errorf("status calls = %d, want %d (the held end is judged from the record)", got, before)
	}
	if r := f.only(t); !r.ended {
		t.Errorf("contract run = %+v, want ended", r)
	}
	f.wantOpState(t, string(OperationStateExecuted))
}

// 持ち越した終わりが failed で、成果物が入っていなければ delegate_ended（status は呼ばない）。
func TestS3a869_HeldFailedRunWithoutALandingIsDelegateEnded(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.sendLand(t)
	f.fake.getErr = errors.New("github is down")
	f.queue(contractOut(ContractStateFailed, nil, nil))
	if item := f.one(t); item.Result != ExecutionResultUpstreamFailed {
		t.Fatalf("item = %+v, want upstream_failed", item)
	}
	f.fake.getErr = nil
	before := len(f.client.statuses)
	if item := f.one(t); item.Result != ExecutionResultDelegateEnded {
		t.Fatalf("item = %+v, want delegate_ended", item)
	}
	if got := len(f.client.statuses); got != before {
		t.Errorf("status calls = %d, want %d", got, before)
	}
	if r := f.only(t); !r.ended {
		t.Errorf("contract run = %+v, want ended", r)
	}
}

// 完了から戻す書き込みは、戻す対象でない同じ課題の反映の操作に終わっていない試行があっても、何も書かず競合で終える。
func TestS3a869_ReopenIsBlockedByAnOpenAttemptOfASiblingRelease(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	siblingID := coretest.InsertApprovedRelease(t, f.s.workspace, int(f.cid), "o/other#9", relHead, execTS(0))
	coretest.InsertOperationExecution(t, f.s.workspace, siblingID, coretest.ExecutionAttempt{StartedAt: execTS(1), ApprovedHead: relHead})
	ctx := context.Background()
	actor, err := f.s.resolveActor()
	if err != nil {
		t.Fatal(err)
	}
	err = f.s.mutateAs(ctx, actor, ChannelCLI, VerificationNone, func(tx *sql.Tx, rec *activityRecorder) error {
		_, err := reopenForReleaseInTx(ctx, tx, rec, f.opID, ReopenReasonContentChanged, nil)
		return err
	})
	if !errors.Is(err, errReopenBlockedByRun) || !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want errReopenBlockedByRun (ErrConflict)", err)
	}
	var n int
	readForTest(t, f.s, func(tx *sql.Tx) error { return tx.QueryRow(`SELECT COUNT(*) FROM challenge_reopen`).Scan(&n) })
	if n != 0 || f.state(t).chStatus != string(StatusDone) {
		t.Errorf("reopens = %d, status = %s, want nothing written", n, f.state(t).chStatus)
	}
}

// 反映の依頼の run を記録する書き込みの中で、同時に走らせる数が超えていれば、何も書かず、commands.reflect は呼ばれない。
func TestS3a869_BeginReflectRunWritesNothingWhenTheParallelLimitIsReached(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	other := f.secondCid(t)
	insertOpenContractRun(t, f.s, other, "ext-other")
	var row *operationRow
	readForTest(t, f.s, func(tx *sql.Tx) (err error) {
		row, err = loadOperationRow(context.Background(), tx, f.opID)
		return err
	})
	_, err := f.s.beginReflectRun(context.Background(), row, nil, 1_000_000, 1)
	if !errors.Is(err, errReflectSlotFull) {
		t.Fatalf("err = %v, want errReflectSlotFull", err)
	}
	var runs int
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM run WHERE challenge_id = ?`, f.cid).Scan(&runs)
	})
	if runs != 0 || len(f.client.starts) != 0 {
		t.Errorf("runs = %d, starts = %v, want nothing", runs, f.client.starts)
	}
}

func (f *reflFixture) secondCid(t *testing.T) int64 {
	t.Helper()
	cid, _ := f.secondRelease(t, "o/second#5")
	return cid
}

// 持ち越した終わりが failed でも、成果物が承認した中身で入っていれば landed ではなく already_merged（landed は委譲先が succeeded で終わったときだけ）。
func TestS3a869_HeldFailedRunWithAMergedArtifactIsAlreadyMergedNotLanded(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.sendLand(t)
	f.fake.getErr = errors.New("github is down")
	f.queue(contractOut(ContractStateFailed, nil, nil))
	if item := f.one(t); item.Result != ExecutionResultUpstreamFailed {
		t.Fatalf("item = %+v, want upstream_failed", item)
	}
	f.fake.getErr = nil
	f.merged()
	if item := f.one(t); item.Result != ExecutionResultAlreadyMerged {
		t.Fatalf("item = %+v, want already_merged", item)
	}
	if r := f.only(t); !r.ended {
		t.Errorf("contract run = %+v, want ended", r)
	}
}
