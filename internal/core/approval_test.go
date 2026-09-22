package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// このファイルは verifiedAttestationForTest（transition_exec_test.go。
// fakeVerifier を通して本人確認を成立させる共通ヘルパー）を使う
// （self-review 指摘: 以前は同じ本体のヘルパーを verifiedAttestation として
// 別名で重複定義していた）。

// --- PrepareApproval / PrepareRejection ---

func TestPrepareApproval_PlanApprovalIncludesLatestPlanVersionAndBody(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusClassified)
	if _, _, err := s.PlanChallenge(context.Background(), ChannelCLI, c.ID, PlanInput{Body: "v1"}); err != nil {
		t.Fatalf("PlanChallenge() error = %v", err)
	}
	if _, _, err := s.PlanChallenge(context.Background(), ChannelCLI, c.ID, PlanInput{Body: "v2"}); err != nil {
		t.Fatalf("PlanChallenge() error = %v", err)
	}

	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("PrepareApproval() error = %v", err)
	}
	if prev.Kind != ApprovalKindPlan {
		t.Errorf("Kind = %q, want %q", prev.Kind, ApprovalKindPlan)
	}
	if prev.PlanVersion != 2 || prev.PlanBody != "v2" {
		t.Errorf("PlanVersion/PlanBody = %d/%q, want 2/v2", prev.PlanVersion, prev.PlanBody)
	}
	if prev.Title != c.Title {
		t.Errorf("Title = %q, want %q", prev.Title, c.Title)
	}
}

func TestPrepareApproval_CompletionApprovalIncludesDoneCriteria(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t", DoneCriteria: "it works"})
	if err != nil {
		t.Fatalf("CreateChallenge() error = %v", err)
	}
	cid, _ := parseChallengeID(c.ID)
	setChallengeStatus(t, s, cid, StatusAwaitingCompletionApproval)

	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("PrepareApproval() error = %v", err)
	}
	if prev.Kind != ApprovalKindCompletion {
		t.Errorf("Kind = %q, want %q", prev.Kind, ApprovalKindCompletion)
	}
	if prev.DoneCriteria != "it works" {
		t.Errorf("DoneCriteria = %q, want %q", prev.DoneCriteria, "it works")
	}
}

func TestPrepareApproval_WrongStateIsInvalidTransition(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)

	_, err := s.PrepareApproval(context.Background(), c.ID)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v, want ErrInvalidTransition", err)
	}
}

func TestPrepareApproval_DoneIsTerminalState(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusDone)

	_, err := s.PrepareApproval(context.Background(), c.ID)
	if !errors.Is(err, ErrTerminalState) {
		t.Fatalf("err = %v, want ErrTerminalState", err)
	}
}

func TestPrepareApproval_NotFoundForMissingOrMalformedID(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	for _, id := range []string{"C-999", "OP-1", "foo"} {
		_, err := s.PrepareApproval(context.Background(), id)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("id=%q: err = %v, want ErrNotFound", id, err)
		}
	}
}

func TestPrepareRejection_EmptyReasonIsValidationErrorBeforeTouchingStore(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)

	for _, reason := range []string{"", "   ", "\t\n"} {
		_, err := s.PrepareRejection(context.Background(), c.ID, reason)
		if !errors.Is(err, ErrValidation) {
			t.Errorf("reason=%q: err = %v, want ErrValidation", reason, err)
		}
	}
}

// --- PrepareAnswer ---

func TestPrepareAnswer_ReturnsQuestionAndFromStatus(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusVerifying)
	q := "why uncertain?"
	if _, err := s.VerifyChallenge(context.Background(), ChannelCLI, c.ID, VerifyInput{Result: "uncertain", Question: &q}); err != nil {
		t.Fatalf("VerifyChallenge() error = %v", err)
	}

	prev, err := s.PrepareAnswer(context.Background(), c.ID, "because")
	if err != nil {
		t.Fatalf("PrepareAnswer() error = %v", err)
	}
	if prev.Question != q {
		t.Errorf("Question = %q, want %q", prev.Question, q)
	}
	if prev.FromStatus != StatusVerifying {
		t.Errorf("FromStatus = %q, want %q", prev.FromStatus, StatusVerifying)
	}
	if prev.Answer != "because" {
		t.Errorf("Answer = %q, want %q", prev.Answer, "because")
	}
}

func TestPrepareAnswer_EmptyAnswerIsValidationErrorBeforeTouchingStore(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	if _, err := s.HoldChallenge(context.Background(), ChannelCLI, c.ID, HoldInput{Question: "why?"}); err != nil {
		t.Fatalf("HoldChallenge() error = %v", err)
	}

	for _, answer := range []string{"", "   ", "\t\n"} {
		_, err := s.PrepareAnswer(context.Background(), c.ID, answer)
		if !errors.Is(err, ErrValidation) {
			t.Errorf("answer=%q: err = %v, want ErrValidation", answer, err)
		}
	}
}

func TestPrepareAnswer_WrongStateIsInvalidTransition(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)

	_, err := s.PrepareAnswer(context.Background(), c.ID, "because")
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v, want ErrInvalidTransition", err)
	}
}

// --- AC-33: T12 の戻り先は「保留に入る直前の状態」。4 つの from_status を列挙する ---

func TestExecuteAnswer_ReturnsToPrecedingHoldStatusForAllFourSources(t *testing.T) {
	for _, from := range []Status{StatusUnclassified, StatusClassified, StatusInProgress, StatusVerifying} {
		s := newStoreForTest(t)
		fixedActor(t, "alice")
		c := createAndAdvance(t, s, from)

		got, err := s.HoldChallenge(context.Background(), ChannelCLI, c.ID, HoldInput{Question: "why?"})
		if err != nil {
			t.Fatalf("from=%q: HoldChallenge() error = %v", from, err)
		}
		if got.Status != StatusAwaitingHuman {
			t.Fatalf("from=%q: Status = %q, want %q", from, got.Status, StatusAwaitingHuman)
		}

		prev, err := s.PrepareAnswer(context.Background(), c.ID, "because")
		if err != nil {
			t.Fatalf("from=%q: PrepareAnswer() error = %v", from, err)
		}
		if prev.FromStatus != from {
			t.Fatalf("from=%q: preview.FromStatus = %q, want %q", from, prev.FromStatus, from)
		}

		att := verifiedAttestationForTest(t, c.ID)
		c2, hold, err := s.ExecuteAnswer(context.Background(), AnswerRequest{
			ChallengeID: c.ID, ExpectedVersion: prev.Version, Answer: "because",
		}, att)
		if err != nil {
			t.Fatalf("from=%q: ExecuteAnswer() error = %v", from, err)
		}
		if c2.Status != from {
			t.Errorf("from=%q: Status = %q, want %q", from, c2.Status, from)
		}
		if hold.Answer == nil || *hold.Answer != "because" {
			t.Errorf("from=%q: hold.Answer = %v, want %q", from, hold.Answer, "because")
		}
		if hold.AnsweredBy == nil || *hold.AnsweredBy != "alice" {
			t.Errorf("from=%q: hold.AnsweredBy = %v, want %q", from, hold.AnsweredBy, "alice")
		}
	}
}

// --- AC-34: 再び保留に入ると、現在の保留は未回答。以前の回答は以前の保留にだけ残る ---

func TestExecuteAnswer_ReenteringHoldLeavesOldAnswerOnOldHoldAndNewHoldUnanswered(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)

	if _, err := s.HoldChallenge(context.Background(), ChannelCLI, c.ID, HoldInput{Question: "first?"}); err != nil {
		t.Fatalf("HoldChallenge() 1 error = %v", err)
	}
	prev, err := s.PrepareAnswer(context.Background(), c.ID, "first answer")
	if err != nil {
		t.Fatalf("PrepareAnswer() error = %v", err)
	}
	att := verifiedAttestationForTest(t, c.ID)
	if _, _, err := s.ExecuteAnswer(context.Background(), AnswerRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Answer: "first answer",
	}, att); err != nil {
		t.Fatalf("ExecuteAnswer() error = %v", err)
	}

	if _, err := s.HoldChallenge(context.Background(), ChannelCLI, c.ID, HoldInput{Question: "second?"}); err != nil {
		t.Fatalf("HoldChallenge() 2 error = %v", err)
	}

	detail, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if len(detail.Holds) != 2 {
		t.Fatalf("len(Holds) = %d, want 2", len(detail.Holds))
	}
	first, second := detail.Holds[0], detail.Holds[1]
	if first.Question != "first?" || first.Answer == nil || *first.Answer != "first answer" {
		t.Errorf("first hold = %+v, want answered with 'first answer'", first)
	}
	if second.Question != "second?" || second.Answer != nil {
		t.Errorf("second hold = %+v, want unanswered", second)
	}
}

// --- AC-46 相当（core 側）: 要約表示後・書き込みまでの間に対象が変わると conflict ---

func TestExecuteApproval_VersionMismatchIsConflictAndChangesNothing(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c.ID, 1, "x")

	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("PrepareApproval() error = %v", err)
	}

	// 要約の表示後、別の変更が対象に加わる（edit で完了条件を変える）。
	dc := "changed"
	if _, err := s.EditChallenge(context.Background(), ChannelCLI, c.ID, EditInput{DoneCriteria: &dc}); err != nil {
		t.Fatalf("EditChallenge() error = %v", err)
	}

	att := verifiedAttestationForTest(t, c.ID)
	_, _, err = s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
	}, att)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("ExecuteApproval() error = %v, want ErrConflict", err)
	}

	after, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if len(after.Approvals) != 0 {
		t.Errorf("Approvals = %+v, want none (conflict must not record an approval)", after.Approvals)
	}
	if after.Status != StatusAwaitingPlanApproval {
		t.Errorf("Status = %q, want unchanged", after.Status)
	}
}

// 「改訂された計画に古い承認は効かない」: PrepareApproval の後に計画を改訂すると、
// 古い版を条件にした承認は conflict になる。
func TestExecuteApproval_RevisedPlanInvalidatesPriorPreview(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusClassified)
	if _, _, err := s.PlanChallenge(context.Background(), ChannelCLI, c.ID, PlanInput{Body: "v1"}); err != nil {
		t.Fatalf("PlanChallenge() error = %v", err)
	}

	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("PrepareApproval() error = %v", err)
	}
	if prev.PlanBody != "v1" {
		t.Fatalf("PlanBody = %q, want v1", prev.PlanBody)
	}

	if _, _, err := s.PlanChallenge(context.Background(), ChannelCLI, c.ID, PlanInput{Body: "v2"}); err != nil {
		t.Fatalf("PlanChallenge() revision error = %v", err)
	}

	att := verifiedAttestationForTest(t, c.ID)
	_, _, err = s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
	}, att)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("ExecuteApproval() error = %v, want ErrConflict (stale plan version)", err)
	}
}

// --- AC-47・AC-49: 承認・差し戻しの記録 ---

func TestExecuteApproval_RecordsApprovalWithActorChannelVerificationTargetVersion(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c.ID, 1, "x")

	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("PrepareApproval() error = %v", err)
	}
	att := verifiedAttestationForTest(t, c.ID)
	c2, approval, err := s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
	}, att)
	if err != nil {
		t.Fatalf("ExecuteApproval() error = %v", err)
	}
	if c2.Status != StatusInProgress {
		t.Errorf("Status = %q, want %q", c2.Status, StatusInProgress)
	}
	if approval.Kind != ApprovalKindPlan || approval.Decision != ApprovalDecisionApproved {
		t.Errorf("Kind/Decision = %q/%q, want plan/approved", approval.Kind, approval.Decision)
	}
	if approval.Actor != "alice" || approval.Channel != string(ChannelCLI) || approval.Verification != string(VerificationTTYConfirm) {
		t.Errorf("Actor/Channel/Verification = %q/%q/%q, want alice/cli/tty_confirm", approval.Actor, approval.Channel, approval.Verification)
	}
	if approval.TargetVersion != prev.Version {
		t.Errorf("TargetVersion = %d, want %d", approval.TargetVersion, prev.Version)
	}
	if approval.Reason != nil {
		t.Errorf("Reason = %v, want nil for an approval", approval.Reason)
	}

	acts := activitiesFor(t, s, c.ID)
	last := acts[len(acts)-1]
	if last.Action != "approve" {
		t.Errorf("Action = %q, want %q", last.Action, "approve")
	}
	if last.Verification != string(VerificationTTYConfirm) {
		t.Errorf("activity Verification = %q, want %q", last.Verification, VerificationTTYConfirm)
	}
}

func TestExecuteApproval_RejectedRecordsReason(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c.ID, 1, "x")

	reason := "not ready"
	prev, err := s.PrepareRejection(context.Background(), c.ID, reason)
	if err != nil {
		t.Fatalf("PrepareRejection() error = %v", err)
	}
	att := verifiedAttestationForTest(t, c.ID)
	c2, approval, err := s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionRejected, Reason: &reason,
	}, att)
	if err != nil {
		t.Fatalf("ExecuteApproval() error = %v", err)
	}
	if c2.Status != StatusClassified {
		t.Errorf("Status = %q, want %q", c2.Status, StatusClassified)
	}
	if approval.Decision != ApprovalDecisionRejected {
		t.Errorf("Decision = %q, want rejected", approval.Decision)
	}
	if approval.Reason == nil || *approval.Reason != reason {
		t.Errorf("Reason = %v, want %q", approval.Reason, reason)
	}
}

// --- AC-48: 理由が空の reject は validation_failed ---

func TestExecuteApproval_RejectedWithEmptyReasonIsValidationError(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	att := verifiedAttestationForTest(t, c.ID)

	empty := "   "
	for _, reason := range []*string{nil, &empty} {
		_, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{
			ChallengeID: c.ID, ExpectedVersion: 1, Decision: ApprovalDecisionRejected, Reason: reason,
		}, att)
		if !errors.Is(err, ErrValidation) {
			t.Errorf("reason=%v: err = %v, want ErrValidation", reason, err)
		}
	}
}

// --- ゼロ値・登録簿外の Attestation は fail-closed に拒否する ---

func TestExecuteApproval_ZeroValueAttestationIsRejectedWithoutChangingState(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c.ID, 1, "x")
	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("PrepareApproval() error = %v", err)
	}

	_, _, err = s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
	}, Attestation{})
	if !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("err = %v, want ErrVerificationRejected", err)
	}

	after, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if after.Version != 1 || after.Status != StatusAwaitingPlanApproval || len(after.Approvals) != 0 {
		t.Fatalf("state changed despite rejected attestation: %+v", after)
	}
}

func TestExecuteAnswer_ZeroValueAttestationIsRejected(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	if _, err := s.HoldChallenge(context.Background(), ChannelCLI, c.ID, HoldInput{Question: "why?"}); err != nil {
		t.Fatalf("HoldChallenge() error = %v", err)
	}
	prev, err := s.PrepareAnswer(context.Background(), c.ID, "because")
	if err != nil {
		t.Fatalf("PrepareAnswer() error = %v", err)
	}

	_, _, err = s.ExecuteAnswer(context.Background(), AnswerRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Answer: "because",
	}, Attestation{})
	if !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("err = %v, want ErrVerificationRejected", err)
	}
}

// TestExecuteApproval_NonEmptyActorButUnregisteredChannelIsRejected は
// self-review 指摘の再発防止: ゼロ値（actor=="" かつ登録簿外）だけでなく、
// actor が非空でも (channel, verification) が登録簿に無ければ
// ErrVerificationRejected になることを固定する（以前のテストはゼロ値の
// Attestation しか検証しておらず、attestationValid から registryAllows の
// 検査を落としても green のままだった）。
func TestExecuteApproval_NonEmptyActorButUnregisteredChannelIsRejected(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c.ID, 1, "x")
	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("PrepareApproval() error = %v", err)
	}

	att := Attestation{actor: "alice", channel: Channel("mobile"), verification: VerificationTTYConfirm, target: c.ID}
	_, _, err = s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
	}, att)
	if !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("err = %v, want ErrVerificationRejected", err)
	}
}

// TestExecuteApproval_AttestationForDifferentTargetIsRejected は
// self-review 指摘の再発防止: C-2 向けに成立した Attestation を C-1 の
// ExecuteApproval へ使い回すと拒否される（H9「確認の入力は対象の ID の
// 完全一致」が Verifier の中だけでなく core の API 境界でも担保されている
// ことの検証）。
func TestExecuteApproval_AttestationForDifferentTargetIsRejected(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c1 := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c1.ID, 1, "x")
	c2 := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c2.ID, 1, "y")

	prev1, err := s.PrepareApproval(context.Background(), c1.ID)
	if err != nil {
		t.Fatalf("PrepareApproval(c1) error = %v", err)
	}

	// c2 に対して成立した Attestation を c1 の承認に使い回す。
	att := verifiedAttestationForTest(t, c2.ID)
	_, _, err = s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c1.ID, ExpectedVersion: prev1.Version, Decision: ApprovalDecisionApproved,
	}, att)
	if !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("err = %v, want ErrVerificationRejected (attestation was confirmed for a different target)", err)
	}

	after, err := s.GetChallenge(context.Background(), c1.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if len(after.Approvals) != 0 || after.Status != StatusAwaitingPlanApproval {
		t.Fatalf("c1 changed despite the mismatched-target attestation: %+v", after)
	}
}

// insertPlanRowForTest は approval_test.go 専用の薄いラッパー
// （transition_exec_test.go の insertPlanRow は内部整数 ID を取るため、
// ここでは表示形の課題 ID から変換する）。
func insertPlanRowForTest(t *testing.T, s *Store, challengeID string, version int, body string) {
	t.Helper()
	cid, ok := parseChallengeID(challengeID)
	if !ok {
		t.Fatalf("insertPlanRowForTest: malformed challenge id %q", challengeID)
	}
	insertPlanRow(t, s, cid, version, body)
}

// --- 作業ログへの書き込み失敗はロールバックする（承認の記録も残らない） ---

func TestExecuteApproval_ActivityInsertFailureRollsBackApproval(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c.ID, 1, "x")
	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("PrepareApproval() error = %v", err)
	}
	att := verifiedAttestationForTest(t, c.ID)

	s.insertActivity = func(_ *sql.Tx, _ activityRow) error {
		return errors.New("boom: injected activity insert failure")
	}
	_, _, err = s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
	}, att)
	if err == nil {
		t.Fatal("ExecuteApproval() error = nil, want an error from the injected failure")
	}
	s.insertActivity = nil

	after, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if after.Version != 1 || after.Status != StatusAwaitingPlanApproval || len(after.Approvals) != 0 {
		t.Fatalf("approve was not rolled back: %+v", after)
	}
}

// --- AC-50 相当（core 側の直接確認）: 登録簿に無い組で承認を要求すると拒否される ---
// (internal/core/verification_test.go の TestVerify_Rejects* が Verify レベルで
// 既に検証しているため、ここでは ExecuteApproval・ExecuteAnswer が独自に
// fail-closed であること（Verify を経由しない直接呼び出し）だけを確認する。)

func TestExecuteApproval_DecisionOutsideClosedSetIsValidationError(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	att := verifiedAttestationForTest(t, c.ID)

	_, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: 1, Decision: ApprovalDecision("bogus"),
	}, att)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
}
