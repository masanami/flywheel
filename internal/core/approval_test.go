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

// --- D12: 完了の承認は未承認の release を同じトランザクションで一括承認する（#13） ---

// setUpChallengeAwaitingCompletionWithOperations は、完了確認待ちの課題に
// pending の release を2件・delete/external_send/other を1件ずつ登録した
// 状態を作る。D12 のテストが共有する。
func setUpChallengeAwaitingCompletionWithOperations(t *testing.T, s *Store) (c *Challenge, releases []*IrreversibleOperation, others []*IrreversibleOperation) {
	t.Helper()
	c = createAndAdvance(t, s, StatusUnclassified)
	r1 := createPendingOperation(t, s, c.ID, "release")
	r2 := createPendingOperation(t, s, c.ID, "release")
	del := createPendingOperation(t, s, c.ID, "delete")
	ext := createPendingOperation(t, s, c.ID, "external_send")
	oth := createPendingOperation(t, s, c.ID, "other")
	setChallengeStatus(t, s, mustParseChallengeID(t, c.ID), StatusAwaitingCompletionApproval)
	return c, []*IrreversibleOperation{r1, r2}, []*IrreversibleOperation{del, ext, oth}
}

func TestPrepareApproval_CompletionListsPendingReleasesAndOtherOperations(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c, releases, others := setUpChallengeAwaitingCompletionWithOperations(t, s)

	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("PrepareApproval() error = %v", err)
	}
	if len(prev.PendingReleases) != len(releases) {
		t.Fatalf("PendingReleases = %+v, want %d entries", prev.PendingReleases, len(releases))
	}
	for i, r := range releases {
		if prev.PendingReleases[i].ID != r.ID {
			t.Errorf("PendingReleases[%d].ID = %q, want %q", i, prev.PendingReleases[i].ID, r.ID)
		}
	}
	if len(prev.OtherPendingOperations) != len(others) {
		t.Fatalf("OtherPendingOperations = %+v, want %d entries", prev.OtherPendingOperations, len(others))
	}
}

func TestExecuteApproval_CompletionApprovesAllPendingReleasesButNotOtherKinds(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c, releases, others := setUpChallengeAwaitingCompletionWithOperations(t, s)

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
	if c2.Status != StatusDone {
		t.Fatalf("Status = %q, want done", c2.Status)
	}
	if approval.Kind != ApprovalKindCompletion {
		t.Errorf("approval.Kind = %q, want completion", approval.Kind)
	}

	detail, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	byID := map[string]IrreversibleOperation{}
	for _, op := range detail.Operations {
		byID[op.ID] = op
	}
	for _, r := range releases {
		if got := byID[r.ID]; got.State != "approved" {
			t.Errorf("release %s state = %q, want approved", r.ID, got.State)
		}
	}
	for _, o := range others {
		if got := byID[o.ID]; got.State != "pending" {
			t.Errorf("non-release %s state = %q, want pending (unchanged)", o.ID, got.State)
		}
	}

	// D12: 完了の承認と release ごとの承認は別々の approval 記録として残る
	// （kind=completion が1件、kind=release が release の数だけ）。
	var completionCount, releaseCount int
	for _, a := range detail.Approvals {
		switch a.Kind {
		case ApprovalKindCompletion:
			completionCount++
		case ApprovalKindRelease:
			releaseCount++
			if a.OperationID == nil {
				t.Errorf("release approval missing OperationID: %+v", a)
			}
		}
	}
	if completionCount != 1 {
		t.Errorf("completion approval count = %d, want 1", completionCount)
	}
	if releaseCount != len(releases) {
		t.Errorf("release approval count = %d, want %d", releaseCount, len(releases))
	}
}

// AC-56 相当（core 側）: 作業ログには完了の承認と release ごとの承認が別々の
// エントリとして（それぞれ actor・経路・本人確認の方式つきで）残る。
func TestExecuteApproval_CompletionRecordsSeparateActivityEntriesForEachRelease(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c, releases, _ := setUpChallengeAwaitingCompletionWithOperations(t, s)

	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("PrepareApproval() error = %v", err)
	}
	att := verifiedAttestationForTest(t, c.ID)
	if _, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
	}, att); err != nil {
		t.Fatalf("ExecuteApproval() error = %v", err)
	}

	var challengeApproveCount int
	operationApproveCount := map[string]int{}
	all, err := s.ListActivities(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}
	for _, a := range all {
		if a.Entity == "challenge" && a.EntityID == c.ID && a.Action == "approve" {
			challengeApproveCount++
			if a.Verification != string(VerificationTTYConfirm) || a.Actor != "alice" {
				t.Errorf("completion activity actor/verification = %q/%q, unexpected", a.Actor, a.Verification)
			}
		}
		if a.Entity == "operation" && a.Action == "approve" {
			operationApproveCount[a.EntityID]++
			if a.Verification != string(VerificationTTYConfirm) || a.Actor != "alice" {
				t.Errorf("operation activity actor/verification = %q/%q, unexpected", a.Actor, a.Verification)
			}
		}
	}
	if challengeApproveCount != 1 {
		t.Errorf("challenge-entity approve activity count = %d, want 1", challengeApproveCount)
	}
	for _, r := range releases {
		if operationApproveCount[r.ID] != 1 {
			t.Errorf("operation-entity approve activity count for %s = %d, want 1", r.ID, operationApproveCount[r.ID])
		}
	}
}

// T14: `approve --hold-release` は完了だけを承認し release は未承認のまま残す。
func TestExecuteApproval_HoldReleaseApprovesCompletionOnlyLeavingReleasesPending(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c, releases, _ := setUpChallengeAwaitingCompletionWithOperations(t, s)

	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("PrepareApproval() error = %v", err)
	}
	att := verifiedAttestationForTest(t, c.ID)
	c2, approval, err := s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved, HoldRelease: true,
	}, att)
	if err != nil {
		t.Fatalf("ExecuteApproval() error = %v", err)
	}
	if c2.Status != StatusDone {
		t.Fatalf("Status = %q, want done", c2.Status)
	}
	if approval.Kind != ApprovalKindCompletion {
		t.Errorf("approval.Kind = %q, want completion", approval.Kind)
	}

	detail, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	byID := map[string]IrreversibleOperation{}
	for _, op := range detail.Operations {
		byID[op.ID] = op
	}
	for _, r := range releases {
		if got := byID[r.ID]; got.State != "pending" {
			t.Errorf("release %s state = %q, want pending (--hold-release must not approve it)", r.ID, got.State)
		}
	}
	for _, a := range detail.Approvals {
		if a.Kind == ApprovalKindRelease {
			t.Errorf("unexpected release approval recorded despite --hold-release: %+v", a)
		}
	}

	// T14 の作業ログの action は OpApproveHoldRelease（"approve_hold_release"）
	// であり、log の action の閉集合に含まれる（self-review 指摘: #13 で初めて
	// この経路が到達可能になったが、値が仕様の閉集合にもテストにも無かった）。
	all, err := s.ListActivities(context.Background(), &c.ID)
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}
	var holdReleaseEntries int
	for _, a := range all {
		if a.Entity == "challenge" && a.Action == string(OpApproveHoldRelease) {
			holdReleaseEntries++
		}
		if a.Entity == "operation" && (a.Action == string(OpApprove) || a.Action == string(OpReject)) {
			t.Errorf("unexpected operation approve/reject activity despite --hold-release: %+v", a)
		}
	}
	if holdReleaseEntries != 1 {
		t.Errorf("approve_hold_release activity count = %d, want 1", holdReleaseEntries)
	}
}

// TestApprovalPreview_ReleaseEffect は D12 の規則（要約の振り分け）を固定する。
// self-review 指摘の再発防止: 要約は「同時に承認される release」を示すが、
// --hold-release・差し戻しでは release は 1 件も承認されない。分類は core が
// 実挙動と同じ述語で決める。
func TestApprovalPreview_ReleaseEffect(t *testing.T) {
	rel := IrreversibleOperation{ID: "OP-1", Kind: OperationKindRelease, State: OperationStatePending}
	other := IrreversibleOperation{ID: "OP-2", Kind: OperationKindDelete, State: OperationStatePending}
	completion := &ApprovalPreview{
		Kind:                   ApprovalKindCompletion,
		PendingReleases:        []IrreversibleOperation{rel},
		OtherPendingOperations: []IrreversibleOperation{other},
	}

	tests := []struct {
		name            string
		preview         *ApprovalPreview
		decision        ApprovalDecision
		holdRelease     bool
		wantApproved    []string
		wantNotApproved []string
	}{
		{"完了の承認", completion, ApprovalDecisionApproved, false, []string{"OP-1"}, []string{"OP-2"}},
		{"完了の承認 --hold-release", completion, ApprovalDecisionApproved, true, nil, []string{"OP-1", "OP-2"}},
		{"完了の差し戻し", completion, ApprovalDecisionRejected, false, nil, []string{"OP-1", "OP-2"}},
		{
			"計画の承認",
			&ApprovalPreview{Kind: ApprovalKindPlan},
			ApprovalDecisionApproved, false, nil, nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effect := tt.preview.ReleaseEffect(tt.decision, tt.holdRelease)
			if got := operationIDsForTest(effect.Approved); !equalStringsForTest(got, tt.wantApproved) {
				t.Errorf("Approved = %v, want %v", got, tt.wantApproved)
			}
			if got := operationIDsForTest(effect.NotApproved); !equalStringsForTest(got, tt.wantNotApproved) {
				t.Errorf("NotApproved = %v, want %v", got, tt.wantNotApproved)
			}
		})
	}
}

func operationIDsForTest(ops []IrreversibleOperation) []string {
	ids := make([]string, 0, len(ops))
	for _, op := range ops {
		ids = append(ids, op.ID)
	}
	return ids
}

func equalStringsForTest(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// T14 は完了確認待ち以外の対象には core レベルでも invalid_transition
// （終端なら terminal_state）で拒否される。usage_error は CLI 層の責務。
func TestExecuteApproval_HoldReleaseOnPlanApprovalIsInvalidTransition(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c.ID, 1, "x")

	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("PrepareApproval() error = %v", err)
	}
	att := verifiedAttestationForTest(t, c.ID)
	_, _, err = s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved, HoldRelease: true,
	}, att)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v, want ErrInvalidTransition", err)
	}
}

func TestExecuteApproval_RejectedWithHoldReleaseIsValidationError(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c.ID, 1, "x")
	att := verifiedAttestationForTest(t, c.ID)

	reason := "no"
	_, _, err := s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: 1, Decision: ApprovalDecisionRejected, HoldRelease: true, Reason: &reason,
	}, att)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation (HoldRelease is only meaningful for an approval)", err)
	}
}

// AC-46 相当（D12 の conflict の一方）: 完了の承認の要約表示後・確認前に
// 別プロセスが対象の release を先に承認すると、完了の承認は成立しない。
func TestExecuteApproval_ConflictWhenAReleaseIsApprovedStandaloneAfterSummaryShown(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c, releases, _ := setUpChallengeAwaitingCompletionWithOperations(t, s)

	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("PrepareApproval() error = %v", err)
	}

	// 別プロセス: 対象の release の1つを先に単独承認する。
	opPrev, err := s.PrepareOperationApproval(context.Background(), releases[0].ID)
	if err != nil {
		t.Fatalf("PrepareOperationApproval() error = %v", err)
	}
	opAtt := verifiedAttestationForTest(t, releases[0].ID)
	if _, _, err := s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: releases[0].ID, ExpectedVersion: opPrev.Version, ExpectedChallengeVersion: opPrev.ChallengeVersion, Decision: ApprovalDecisionApproved,
	}, opAtt); err != nil {
		t.Fatalf("ExecuteOperationApproval() error = %v", err)
	}

	// 完了の承認は、Prepare 時点の古い版のままでは conflict。
	att := verifiedAttestationForTest(t, c.ID)
	_, _, err = s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
	}, att)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}

	detail, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if detail.Status != StatusAwaitingCompletionApproval {
		t.Errorf("Status = %q, want awaiting_completion_approval (completion must not have applied)", detail.Status)
	}
}

// AC-46 相当（D12 の conflict のもう一方）: 完了の承認の要約表示後・確認前に
// 別プロセスが op add で不可逆操作を足すと、完了の承認は成立しない。
func TestExecuteApproval_ConflictWhenOpAddHappensAfterSummaryShown(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c, _, _ := setUpChallengeAwaitingCompletionWithOperations(t, s)

	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("PrepareApproval() error = %v", err)
	}

	// 別プロセス: 新しい不可逆操作を追加する。
	if _, err := s.CreateOperation(context.Background(), ChannelCLI, OperationInput{
		ChallengeID: c.ID, Kind: "release", Summary: "late addition",
	}); err != nil {
		t.Fatalf("CreateOperation() error = %v", err)
	}

	att := verifiedAttestationForTest(t, c.ID)
	_, _, err = s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
	}, att)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}
