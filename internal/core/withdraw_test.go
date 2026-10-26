package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
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

func TestExecuteWithdraw_LastOccurrenceIDIsTheMaxOccurrenceID(t *testing.T) {
	f := newJ6Fixture(t)
	c := f.challenge(t, StatusClassified)
	f.occ(t, c, "plan_rejected", "bad", nil)
	f.occ(t, c, "plan_rejected", "bad", nil)

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
	if last.Action != "withdraw" || after["last_occurrence_id"] != float64(2) {
		t.Errorf("last activity = %s %s, want withdraw with last_occurrence_id 2", last.Action, last.After)
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

// release の実行の試行が終わっていない課題は conflict で、何も書かない（`op exec` が依頼中のマージを取りこぼさない）。
func TestWithdraw_OpenReleaseExecutionAttempt_IsConflictAndWritesNothing(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	ch := createAndAdvance(t, s, StatusAwaitingCompletionApproval)
	if _, err := s.CreateOperation(context.Background(), ChannelCLI, OperationInput{ChallengeID: ch.ID, Kind: "release", Summary: "s"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE operation SET state = 'approved'`); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO operation_execution (operation_id, started_at, pid, host) VALUES (1, '2026-01-01T00:00:00.000Z', 1, 'h')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := withdrawForTest(t, s, ch.ID, "r"); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if d, _ := s.GetChallenge(context.Background(), ch.ID); d.Status != StatusAwaitingCompletionApproval {
		t.Errorf("status = %s, want unchanged", d.Status)
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
