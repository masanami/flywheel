package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// このファイルは課題の取り下げ（docs/features/m4.5-learning-and-self-improvement.md S4。T16）の core の
// 振る舞いを確かめる。CLI の受入基準（終了コード・JSON の形）は internal/cli/withdraw_test.go が担う。

// withdrawForTest は id の課題を本人確認つきで取り下げる（① → 本人確認 → ②）。
func withdrawForTest(t *testing.T, s *Store, id, reason string) (*Challenge, error) {
	t.Helper()
	prev, err := s.PrepareWithdraw(context.Background(), id, reason)
	if err != nil {
		return nil, err
	}
	return s.ExecuteWithdraw(context.Background(), WithdrawRequest{
		ChallengeID: id, ExpectedVersion: prev.Version, Reason: reason,
	}, verifiedAttestationForTest(t, id))
}

func TestPrepareWithdraw_ValidationNotFoundAndPreview(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := createAndAdvance(t, s, StatusAwaitingHuman)
	insertUnansweredHold(t, s, 1, StatusClassified, "what?")
	if _, err := s.CreateOperation(context.Background(), ChannelCLI, OperationInput{ChallengeID: ch.ID, Kind: "other", Summary: "s"}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.PrepareWithdraw(context.Background(), ch.ID, "  "); !errors.Is(err, ErrValidation) {
		t.Errorf("empty reason: err = %v, want ErrValidation", err)
	}
	if _, err := s.PrepareWithdraw(context.Background(), "C-99", "r"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}
	p, err := s.PrepareWithdraw(context.Background(), ch.ID, "r")
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusAwaitingHuman || p.Version != 2 || len(p.RejectedOperations) != 1 || p.ClosedHold == nil || *p.ClosedHold != "what?" {
		t.Errorf("preview = %+v", p)
	}
}

func TestExecuteWithdraw_RejectsWrongAttestationAndStaleVersion(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := createAndAdvance(t, s, StatusClassified)

	// 別の対象向けの本人確認は拒否する。
	_, err := s.ExecuteWithdraw(context.Background(), WithdrawRequest{ChallengeID: ch.ID, ExpectedVersion: 1, Reason: "r"}, verifiedAttestationForTest(t, "C-2"))
	if !errors.Is(err, ErrVerificationRejected) {
		t.Errorf("wrong target: err = %v, want ErrVerificationRejected", err)
	}
	// 版が古ければ conflict。
	_, err = s.ExecuteWithdraw(context.Background(), WithdrawRequest{ChallengeID: ch.ID, ExpectedVersion: 7, Reason: "r"}, verifiedAttestationForTest(t, ch.ID))
	if !errors.Is(err, ErrConflict) {
		t.Errorf("stale version: err = %v, want ErrConflict", err)
	}
	// 理由が空なら validation。
	_, err = s.ExecuteWithdraw(context.Background(), WithdrawRequest{ChallengeID: ch.ID, ExpectedVersion: 1, Reason: ""}, verifiedAttestationForTest(t, ch.ID))
	if !errors.Is(err, ErrValidation) {
		t.Errorf("empty reason: err = %v, want ErrValidation", err)
	}
	if d, _ := s.GetChallenge(context.Background(), ch.ID); d.Status != StatusClassified || d.Version != 1 {
		t.Errorf("challenge changed despite rejections: %s v%d", d.Status, d.Version)
	}
}

// (ui, none) は取り下げを受けない（UI には口を置かない。M45H28）。
func TestExecuteWithdraw_UINoneAttestationIsRejected(t *testing.T) {
	if registryPermits(ChannelUI, VerificationNone, actionWithdraw) {
		t.Error("(ui, none) must not permit withdraw")
	}
	if !registryPermits(ChannelCLI, VerificationTTYConfirm, actionWithdraw) || !registryPermits(ChannelCLI, VerificationRelayed, actionWithdraw) {
		t.Error("(cli, tty_confirm) and (cli, relayed) must permit withdraw")
	}
}

// last_occurrence_id は取り下げた時点の発生の id の全体の最大（課題を問わない。#391 の見送りの抑えの鍵）。
// 別の課題の発生を後に作り、途中の id を欠番にして、件数・課題単位の最大と区別する。
func TestExecuteWithdraw_LastOccurrenceIDIsTheMaxOccurrenceID(t *testing.T) {
	f := newJ6Fixture(t)
	c := f.challenge(t, StatusClassified)
	other := f.challenge(t, StatusClassified)
	f.occ(t, c, "plan_rejected", "bad", nil)     // O-1
	f.occ(t, c, "plan_rejected", "bad", nil)     // O-2（欠番にする）
	f.occ(t, other, "plan_rejected", "bad", nil) // O-3
	f.occ(t, other, "plan_rejected", "bad", nil) // O-4
	f.exec(t, `DELETE FROM occurrence WHERE id = 2`)

	if _, err := withdrawForTest(t, f.s, c, "r"); err != nil {
		t.Fatal(err)
	}
	acts, err := f.s.ListActivities(context.Background(), &c)
	if err != nil {
		t.Fatal(err)
	}
	last := acts[len(acts)-1]
	var after map[string]any
	if err := json.Unmarshal(last.After, &after); err != nil {
		t.Fatal(err)
	}
	// 件数は 3・この課題の最大は 1・全体の最大は 4。
	if last.Action != "withdraw" || after["last_occurrence_id"] != float64(4) {
		t.Errorf("last activity = %s %s, want withdraw with last_occurrence_id 4", last.Action, last.After)
	}
}

// AC-389: 取り下げは課題の出どころ（取り込み・人の手・フォローアップ・改修課題）を問わない。
func TestWithdraw_AnyOrigin_Succeeds(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	base := createAndAdvance(t, s, StatusClassified)
	for _, tc := range []struct {
		origin      ChallengeOrigin
		followupOf  *string
		improvement *ImprovementKey
	}{
		{origin: ChallengeOriginIngest},
		{origin: ChallengeOriginManual},
		{origin: ChallengeOriginFollowup, followupOf: &base.ID},
		{origin: ChallengeOriginImprovement, improvement: &ImprovementKey{Layer: "prompt", Target: "t"}},
	} {
		t.Run(string(tc.origin), func(t *testing.T) {
			var cid int64
			if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
				var err error
				cid, _, err = insertChallengeRow(context.Background(), tx, time.Now(), "origin "+string(tc.origin), "", "", nil, "alice", tc.origin, tc.followupOf, tc.improvement)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			id := formatChallengeID(cid)
			got, err := withdrawForTest(t, s, id, "r")
			if err != nil {
				t.Fatalf("withdraw %s: %v", tc.origin, err)
			}
			if got.Status != StatusWithdrawn {
				t.Errorf("status = %s, want withdrawn", got.Status)
			}
		})
	}
}

// 着手中で終わっていない委譲の run がある課題は conflict で、何も書かない（M45H29）。
func TestWithdraw_InProgressWithOpenDelegationRun_IsConflictAndWritesNothing(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := createAndAdvance(t, s, StatusInProgress)
	runID := insertActiveFakeRunForTest(t, s, ch.ID, judgmentJ1)
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE run SET kind = 'delegate', judgment = NULL WHERE id = ?`, runID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := withdrawForTest(t, s, ch.ID, "r"); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if d, _ := s.GetChallenge(context.Background(), ch.ID); d.Status != StatusInProgress || d.Version != 1 {
		t.Errorf("challenge changed: %s v%d", d.Status, d.Version)
	}
	if n := len(activitiesFor(t, s, ch.ID)); n != 1 {
		t.Errorf("activities = %d, want 1", n)
	}
}

// 終わった委譲の run だけなら取り下げられる。判断の run（開いていても）は取り下げを止めない。
func TestWithdraw_InProgressWithOnlyJudgmentOrClosedRuns_Succeeds(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := createAndAdvance(t, s, StatusInProgress)
	closedID := insertFakeRunForTest(t, s, ch.ID, judgmentJ1) // 終わった run を委譲の run にする
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE run SET kind = 'delegate', judgment = NULL WHERE id = ?`, closedID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	insertActiveFakeRunForTest(t, s, ch.ID, judgmentJ1) // 開いている判断の run
	if _, err := withdrawForTest(t, s, ch.ID, "r"); err != nil {
		t.Fatalf("err = %v, want success", err)
	}
}

// AC-376: ① の後・② の前に委譲の run が始まると、版の照合では検出できない（run の記録は課題の版を上げない）。
// ② のトランザクションの中の再検査が conflict にし、何も書かない。
func TestWithdraw_DelegationStartedAfterPrepare_IsConflictAndWritesNothing(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := createAndAdvance(t, s, StatusInProgress)
	prev, err := s.PrepareWithdraw(context.Background(), ch.ID, "r")
	if err != nil {
		t.Fatal(err)
	}
	runID := insertActiveFakeRunForTest(t, s, ch.ID, judgmentJ1)
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE run SET kind = 'delegate', judgment = NULL WHERE id = ?`, runID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	_, err = s.ExecuteWithdraw(context.Background(), WithdrawRequest{ChallengeID: ch.ID, ExpectedVersion: prev.Version, Reason: "r"}, verifiedAttestationForTest(t, ch.ID))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if strings.Contains(err.Error(), "approval summary") || !strings.Contains(err.Error(), "delegation run") {
		t.Errorf("err message = %q, want the withdraw reason (unfinished delegation run)", err.Error())
	}
	if d, _ := s.GetChallenge(context.Background(), ch.ID); d.Status != StatusInProgress || d.Version != prev.Version {
		t.Errorf("challenge changed: %s v%d", d.Status, d.Version)
	}
	if n := len(activitiesFor(t, s, ch.ID)); n != 1 {
		t.Errorf("activities = %d, want 1", n)
	}
}

// approvedReleaseWithAttempt は課題に approved の release を作り、終わっていない試行（pid・host・開始時刻）を 1 つ足す。
func approvedReleaseWithAttempt(t *testing.T, s *Store, id string, pid int64, host string, started time.Time) {
	t.Helper()
	if _, err := s.CreateOperation(context.Background(), ChannelCLI, OperationInput{ChallengeID: id, Kind: "release", Summary: "s"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE operation SET state = 'approved'`); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO operation_execution (operation_id, started_at, pid, host) VALUES (1, ?, ?, ?)`,
			formatTimestamp(started), pid, host)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// プロセスの生きている試行（`op exec` がマージを依頼している最中）は conflict で、何も書かない。
func TestWithdraw_LiveReleaseExecutionAttempt_IsConflictAndWritesNothing(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := createAndAdvance(t, s, StatusAwaitingCompletionApproval)
	host, _ := os.Hostname()
	approvedReleaseWithAttempt(t, s, ch.ID, int64(os.Getpid()), host, time.Now())

	_, err := withdrawForTest(t, s, ch.ID, "r")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if strings.Contains(err.Error(), "approval summary") || !strings.Contains(err.Error(), "release execution") {
		t.Errorf("err message = %q, want the withdraw reason (release execution in progress)", err.Error())
	}
	if d, _ := s.GetChallenge(context.Background(), ch.ID); d.Status != StatusAwaitingCompletionApproval {
		t.Errorf("status = %s, want unchanged", d.Status)
	}
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		r, err := getOperationExecution(context.Background(), tx, 1)
		if err != nil {
			return err
		}
		if !r.open() {
			t.Error("the live attempt was closed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// プロセスの死んだ（止まった）試行は開いていないとみなし、取り下げのトランザクションで interrupted として閉じる
// （`op exec` の再実行＝本番へのマージ以外に閉じる経路を残す。M45H29）。
func TestWithdraw_StaleReleaseExecutionAttempt_IsClosedAsInterruptedAndWithdraws(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := createAndAdvance(t, s, StatusAwaitingCompletionApproval)
	host, _ := os.Hostname()
	approvedReleaseWithAttempt(t, s, ch.ID, deadPID(t), host, time.Now())

	got, err := withdrawForTest(t, s, ch.ID, "r")
	if err != nil {
		t.Fatalf("err = %v, want success", err)
	}
	if got.Status != StatusWithdrawn {
		t.Errorf("status = %s, want withdrawn", got.Status)
	}
	if err := s.db.Read(context.Background(), func(tx *sql.Tx) error {
		r, err := getOperationExecution(context.Background(), tx, 1)
		if err != nil {
			return err
		}
		if r.open() || r.Result != ExecutionResultInterrupted {
			t.Errorf("attempt = %+v, want closed as interrupted", r)
		}
		var state string
		if err := tx.QueryRow(`SELECT state FROM operation WHERE id = 1`).Scan(&state); err != nil {
			return err
		}
		if state != string(OperationStateRejected) {
			t.Errorf("operation state = %s, want rejected", state)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// J6 の入力の「承認と保留の記録」は、閉じた保留を未回答と書かない（#385 の申し送り）。
func TestWithdraw_J6Input_ClosedHoldIsNotUnanswered(t *testing.T) {
	f := newJ6Fixture(t)
	c := f.challenge(t, StatusWithdrawn)
	cid, _ := parseChallengeID(c)
	now := formatTimestamp(time.Now())
	f.exec(t, `INSERT INTO hold (challenge_id, question, from_status, raised_at, closed_at) VALUES (?, 'CLOSED-Q', 'verifying', ?, ?)`, cid, now, now)
	f.occ(t, c, "plan_rejected", "bad", nil)

	f.mustLearn(t, nil)
	got := f.section(t, f.inputs[0], "承認と保留の記録")
	if !strings.Contains(got, "CLOSED-Q") || !strings.Contains(got, "(回答なしで閉じた)") || strings.Contains(got, "(未回答)") {
		t.Errorf("approvals/holds section = %q, want the closed marker instead of (未回答)", got)
	}
}

// 判断の run が走っている間に取り下げられた課題へは、写像が何も書かずに終わる（J1〜J5。M45H13）。
func TestWithdraw_J1Mapping_OnWithdrawnChallenge_WritesNothing(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := createAndAdvance(t, s, StatusUnclassified)
	runID := insertFakeRunForTest(t, s, ch.ID, judgmentJ1)
	if _, err := withdrawForTest(t, s, ch.ID, "r"); err != nil {
		t.Fatal(err)
	}
	before := len(activitiesFor(t, s, ch.ID))

	mapped, err := s.applyJ1Mine(context.Background(), formatRunID(runID), ch.ID, PriorityP1, nil, nil)
	if err != nil || mapped {
		t.Errorf("applyJ1Mine: mapped=%v err=%v, want not mapped and no error", mapped, err)
	}
	mapped, err = s.applyJ1Uncertain(context.Background(), formatRunID(runID), ch.ID, "q?", nil, nil)
	if err != nil || mapped {
		t.Errorf("applyJ1Uncertain: mapped=%v err=%v, want not mapped and no error", mapped, err)
	}
	d, _ := s.GetChallenge(context.Background(), ch.ID)
	if d.Status != StatusWithdrawn || len(activitiesFor(t, s, ch.ID)) != before {
		t.Errorf("status=%s activities=%d (was %d), want unchanged", d.Status, len(activitiesFor(t, s, ch.ID)), before)
	}
}

func TestWithdraw_J2Mapping_DuringJ2_OnlyTheRunOutputIsKept(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "withdrawn during J2", "P1")
	inv := succeededInvoker(j2OutputFor(false, nil))
	inv.onInvoke = func(JudgmentLaunchInput) {
		if _, err := withdrawForTest(t, f.s, ch.ID, "r"); err != nil {
			t.Errorf("withdraw: %v", err)
		}
	}
	res, err := f.run(t, inv, idPtr(ch.ID))
	if err != nil {
		t.Fatalf("PlanAutoJ2: %v", err)
	}
	if res.Items[0].Status != nil || res.Items[0].Result != RunResultSucceeded {
		t.Errorf("item = %+v, want succeeded and not mapped", res.Items[0])
	}
	d := f.detail(t, ch.ID)
	if d.Status != StatusWithdrawn || len(d.Plans) != 0 {
		t.Errorf("status = %s plans = %v, want withdrawn with no plan", d.Status, d.Plans)
	}
}

func TestWithdraw_J3_DuringJ3_NothingIsLaunched(t *testing.T) {
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.j3.onInvoke = func(in JudgmentLaunchInput) {
		f.j3Inputs = append(f.j3Inputs, in)
		if _, err := withdrawForTest(t, f.s, id, "r"); err != nil {
			t.Errorf("withdraw: %v", err)
		}
	}
	if _, err := f.run(t, &id); err != nil && !errors.Is(err, ErrInvalidTransition) && !errors.Is(err, ErrTerminalState) {
		t.Fatalf("RunDelegation: %v", err)
	}
	if len(f.deleg.launched()) != 0 {
		t.Errorf("a delegation was launched for a withdrawn challenge: %d", len(f.deleg.launched()))
	}
	if d := f.detail(t, id); d.Status != StatusWithdrawn {
		t.Errorf("status = %s, want withdrawn", d.Status)
	}
}

func TestWithdraw_J5_DuringJ5_VerdictRecordedButNotMapped(t *testing.T) {
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.j5.onInvoke = func(JudgmentLaunchInput) {
		if _, err := withdrawForTest(t, f.s, "C-1", "r"); err != nil {
			t.Errorf("withdraw: %v", err)
		}
	}
	res, err := f.verify(t, nil)
	if err != nil || len(res.Items) != 1 {
		t.Fatal(res, err)
	}
	if res.Items[0].Outcome != "met" || res.Items[0].Status != nil {
		t.Errorf("item = %+v, want the verdict recorded but no state written", res.Items[0])
	}
	if got := f.detail(t, "C-1").Status; got != StatusWithdrawn {
		t.Errorf("status = %s, want withdrawn", got)
	}
}

// 取り下げた課題を依存先にした依存は満たされない（M45P14）。
func TestWithdraw_DependencyOnWithdrawnChallenge_IsNotSatisfied(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	a := f.newDepTarget(t)
	setChallengeStatus(t, f.s, a, StatusWithdrawn)
	f.dependOn(t, id, a)
	res, err := f.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertDependencyNotStarted(t, res, id, formatChallengeID(a))
	if len(f.deleg.launched()) != 0 {
		t.Error("nothing may be launched")
	}
}

// 取り下げた課題の未分類の発生は J6 の対象になる（M45P32）。
func TestWithdraw_J6AutoTarget_IncludesWithdrawnChallengeWithUnclassifiedOccurrence(t *testing.T) {
	f := newJ6Fixture(t)
	c := f.challenge(t, StatusWithdrawn)
	o := f.occ(t, c, "plan_rejected", "bad", nil)
	f.inv.result = j6Out([]any{j6Cls(o, "context", "prompt")}, nil, nil, nil)

	res := f.mustLearn(t, nil)
	if len(res.Items) != 1 || res.Items[0].ChallengeID != c || res.Items[0].Result != RunResultSucceeded {
		t.Fatalf("items = %+v, want one succeeded item for %s", res.Items, c)
	}
	if f.unclassified(t) != 0 {
		t.Error("the occurrence of the withdrawn challenge was not classified")
	}
	if !learnableStatus(StatusWithdrawn) {
		t.Error("learnableStatus(withdrawn) = false")
	}
}

// 閉じた保留はどの読み取りでも開いている保留として扱わない（#397 の申し送り）。
func TestWithdraw_ClosedHold_IsNotOpenInAnyReadPath(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := createAndAdvance(t, s, StatusVerifying)
	if _, err := s.VerifyChallenge(context.Background(), ChannelCLI, ch.ID, VerifyInput{Result: "uncertain", Question: strPtr("why?")}); err != nil {
		t.Fatal(err)
	}
	if hs, err := s.ListUnansweredHolds(context.Background()); err != nil || len(hs) != 1 {
		t.Fatalf("open holds before = %v err=%v", hs, err)
	}

	if _, err := withdrawForTest(t, s, ch.ID, "r"); err != nil {
		t.Fatal(err)
	}

	d, err := s.GetChallenge(context.Background(), ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Holds) != 1 || d.Holds[0].Answer != nil || d.Holds[0].ClosedAt == nil || d.Holds[0].Open() {
		t.Fatalf("holds = %+v, want one closed hold with no answer", d.Holds)
	}
	if hs, err := s.ListUnansweredHolds(context.Background()); err != nil || len(hs) != 0 {
		t.Errorf("open holds after = %v err=%v, want none", hs, err)
	}
	if got := formatHoldsSection(d.Holds); !strings.Contains(got, "回答なしで閉じた") || strings.Contains(got, "(未回答)") {
		t.Errorf("formatHoldsSection = %q, want the closed marker instead of (未回答)", got)
	}
}
