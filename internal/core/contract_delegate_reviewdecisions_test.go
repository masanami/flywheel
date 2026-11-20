package core

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// このファイルは、レビュー待ちの契約の run をめぐる決定（2026-11-15 オーナー。PR の親のレビューの後の差し戻し）を固定する:
// 観測の待ちの resume は着手中だけ・完了の間もレビュー待ちを実行中に数えない・取り下げと計画の作り直しの前に開いた run を cancel で止める・
// 差し戻しの上限の保留の問いと回答の返し方・検証中の問いの保留の間は status を呼ばない。

func (f *contractFixture) setStatus(t *testing.T, st Status) {
	t.Helper()
	n, _ := parseChallengeID(f.cid)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE challenge SET status = ?, version = version + 1 WHERE id = ?`, string(st), n)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// 1: オーナーの保留の間は、観測の待ちでも答え無しの resume を呼ばない。
func TestPollContractRuns_ObserveWaitIsNotResumedWhileTheOwnerHold(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	observe := waitingOut(&ContractRequestedAction{Kind: "observe", Decider: "any", Text: "見ている"}, nil)
	f.client.queue = []ContractLaunchOutput{observe, observe}
	f.poll(t)
	f.setStatus(t, StatusAwaitingHuman)
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f.poll(t)
	if len(f.client.resumes) != 0 {
		t.Fatalf("resumes = %v, want none while the challenge is held for the owner", f.client.resumes)
	}
}

// 2・6(a)・AC-30: 開いたレビュー待ちの run は、課題の状態によらず、実行中の数え・同時委譲数に入らない（着手中でも入らない）。
func TestContractReviewWait_IsNotCountedAsActiveWhileVerifyingOrDone(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	f.toVerifying(t)
	sch := &delegationScheduler{s: f.s, maxParallel: 1, preRepo: map[string]int{}}
	n, _ := parseChallengeID(f.cid)
	for _, tc := range []struct {
		st     Status
		active bool
	}{
		{StatusVerifying, false}, {StatusAwaitingCompletionApproval, false}, {StatusDone, false}, {StatusInProgress, false},
	} {
		f.setStatus(t, tc.st)
		var got bool
		if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
			got, err = challengeHasActiveRun(context.Background(), tx, n)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if got != tc.active {
			t.Errorf("%s: challengeHasActiveRun = %v, want %v", tc.st, got, tc.active)
		}
		c, err := sch.capacity(context.Background(), ConnectorRepo{Name: "none"})
		if err != nil {
			t.Fatal(err)
		}
		if fits := sch.fitsLocked(slotCapacity{usable: 5, dbTotal: c.dbTotal}, "none"); fits == tc.active {
			t.Errorf("%s: another delegation fits = %v with max_parallel_runs = 1, want %v", tc.st, fits, !tc.active)
		}
	}
}

// 6(b): 完了の検証の問い（J5 の uncertain）で保留の間は、status を呼ばない。
func TestPollContractRuns_DoesNotReadStatusWhileJ5QuestionHold(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	f.toVerifying(t)
	f.j5.j5.result = j5Out("uncertain", nil, "QUESTION-TEXT")
	if _, err := f.s.VerifyAutoJ5(context.Background(), f.j5.input(f.cycle(t, 300), &f.cid)); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", got)
	}
	before := len(f.client.statuses)
	f.poll(t)
	if len(f.client.statuses) != before || len(f.client.resumes) != 0 {
		t.Fatalf("status calls %d→%d, resumes %v, want no call while held by the J5 question", before, len(f.client.statuses), f.client.resumes)
	}
}

// 4・6(c): 差し戻しの上限の保留の問いは契約の run の動きに合わせ、回答の後は同じ run へ respond を返し、note にオーナーの回答が入る。
func TestPollContractRuns_ReworkLimitHoldQuestionAndAnswerIsReturnedByRespond(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	f.agent.ReworkLimit = 1
	f.toVerifying(t)
	if _, err := f.s.VerifyAutoJ5(context.Background(), f.j5.input(f.cycle(t, 300), &f.cid)); err != nil {
		t.Fatal(err)
	}
	f.poll(t)
	qs := f.holdQuestions(t)
	if len(qs) != 1 || !strings.Contains(qs[0], "同じ run へ") || strings.Contains(qs[0], "委譲を起動し直す") {
		t.Fatalf("hold questions = %q, want the contract run wording", qs)
	}
	f.answerLatestHold(t, f.cid, "OWNER-ANSWER")
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f.poll(t)
	want := []string{"tool", "resume", "ext-1", "--choice", "respond", "--note", "FEEDBACK-TEXT\n\nオーナーの回答: OWNER-ANSWER"}
	if len(f.client.resumes) != 1 || !reflect.DeepEqual(f.client.resumes[0], want) {
		t.Fatalf("resumes = %v, want %v", f.client.resumes, want)
	}
}

func (f *reviewFlowFixture) cancelOpen(t *testing.T) (int, error) {
	t.Helper()
	return f.s.CancelOpenContractRuns(context.Background(), ContractCancelInput{
		ChallengeID: f.cid, AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client,
	})
}

// 3: 取り下げは、開いた契約の run を止めてから進める。止めないまま取り下げようとすると conflict。
func TestWithdraw_StopsTheOpenContractRunWithCancelFirst(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	f.toVerifying(t)
	if _, err := withdrawForTest(t, f.s, f.cid, "r"); !errors.Is(err, ErrConflict) {
		t.Fatalf("withdraw with an open contract run: err = %v, want ErrConflict", err)
	}
	// 要約（①）は開いた run があっても通る（CLI は本人確認の後に cancel するため。①で拒むと cancel に届かない）。
	if _, err := f.s.PrepareWithdraw(context.Background(), f.cid, "r"); err != nil {
		t.Fatalf("PrepareWithdraw with an open contract run: %v, want ok", err)
	}
	f.client.cancelOut = contractOut(ContractStateCancelled, nil, nil)
	n, err := f.cancelOpen(t)
	if err != nil || n != 1 || len(f.client.cancels) != 1 {
		t.Fatalf("cancelled %d (err %v), cancels = %v, want one cancel", n, err, f.client.cancels)
	}
	if state, _, _, ended := f.contractRow(t); state != "cancelled" || !ended {
		t.Fatalf("contract_run state=%s ended=%v, want cancelled and ended", state, ended)
	}
	ch, err := withdrawForTest(t, f.s, f.cid, "r")
	if err != nil || ch.Status != StatusWithdrawn {
		t.Fatalf("withdraw after cancel: %+v err %v, want withdrawn", ch, err)
	}
	if open, err := f.s.HasOpenContractRun(context.Background(), f.cid); err != nil || open {
		t.Fatalf("open contract run = %v (err %v), want none", open, err)
	}
}

// cancel が失敗したときは進めない（conflict。run は開いたまま）。
func TestCancelOpenContractRuns_FailureIsAConflictAndTheRunStaysOpen(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	f.toVerifying(t)
	f.client.cancelOut = ContractLaunchOutput{Result: RunResultErrored}
	if _, err := f.cancelOpen(t); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if _, _, _, ended := f.contractRow(t); ended {
		t.Fatal("contract run ended although the cancel failed")
	}
}

// 開いた run が無ければ、宣言も client も要らず何もしない。
func TestCancelOpenContractRuns_NothingOpenIsANoOp(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	if n, err := f.s.CancelOpenContractRuns(context.Background(), ContractCancelInput{ChallengeID: f.cid}); err != nil || n != 0 {
		t.Fatalf("n = %d err = %v, want 0 and nil", n, err)
	}
}

// 3: 計画の作り直し（--replan）は、開いた契約の run を止めてから進める。止めないままだと拒む。
func TestAnswerReplan_StopsTheOpenContractRunWithCancelFirst(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	f.agent.ReworkLimit = 1
	f.toVerifying(t)
	if _, err := f.s.VerifyAutoJ5(context.Background(), f.j5.input(f.cycle(t, 300), &f.cid)); err != nil {
		t.Fatal(err)
	}
	f.poll(t) // 差し戻しの上限の保留
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", got)
	}
	replan := func() (*Challenge, error) {
		p, err := f.s.PrepareAnswer(context.Background(), f.cid, "NEW-SCOPE")
		if err != nil {
			t.Fatal(err)
		}
		c, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{
			ChallengeID: f.cid, ExpectedVersion: p.Version, Answer: "NEW-SCOPE", Replan: true,
		}, verifiedAttestationForTest(t, f.cid))
		return c, err
	}
	if _, err := replan(); !errors.Is(err, ErrValidation) {
		t.Fatalf("replan with an open contract run: err = %v, want ErrValidation", err)
	}
	f.client.cancelOut = contractOut(ContractStateCancelled, nil, nil)
	if _, err := f.cancelOpen(t); err != nil {
		t.Fatal(err)
	}
	c, err := replan()
	if err != nil || c.Status != StatusClassified {
		t.Fatalf("replan after cancel: %+v err %v, want classified", c, err)
	}
}

// 3: cancel の応答が succeeded（止めた時点で接続ツールの run がちょうど成功していた）でも、保留の間の成功の印を外し、
// 計画の作り直し（--replan）へ進める。作り直す前の計画の成果物は、保留が解けた後に提出し直さない。
func TestAnswerReplan_ProceedsWhenTheCancelReturnsSucceeded(t *testing.T) {
	t.Parallel()
	f := newReviewFlowFixture(t)
	f.agent.ReworkLimit = 1
	f.toVerifying(t)
	if _, err := f.s.VerifyAutoJ5(context.Background(), f.j5.input(f.cycle(t, 300), &f.cid)); err != nil {
		t.Fatal(err)
	}
	f.poll(t) // 差し戻しの上限の保留
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", got)
	}
	f.client.cancelOut = contractOut(ContractStateSucceeded, nil, nil)
	if n, err := f.cancelOpen(t); err != nil || n != 1 {
		t.Fatalf("cancelled %d err %v, want one", n, err)
	}
	var held int
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM contract_run WHERE held_success = 1`).Scan(&held)
	}); err != nil {
		t.Fatal(err)
	}
	if held != 0 {
		t.Fatalf("held_success rows = %d, want 0 after the cancel for withdraw/replan", held)
	}
	p, err := f.s.PrepareAnswer(context.Background(), f.cid, "NEW-SCOPE")
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{
		ChallengeID: f.cid, ExpectedVersion: p.Version, Answer: "NEW-SCOPE", Replan: true,
	}, verifiedAttestationForTest(t, f.cid))
	if err != nil || c.Status != StatusClassified {
		t.Fatalf("replan after a cancel answered succeeded: %+v err %v, want classified", c, err)
	}
}
