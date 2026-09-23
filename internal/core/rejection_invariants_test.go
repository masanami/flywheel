package core

import (
	"context"
	"errors"
	"testing"
)

// このファイルは masanami/flywheel#29 の受入基準を、既存テストファイルへの
// 追記ではなく新規ファイルとして固定する。
//
//   - AC-65（core 側の一部): 拒否・失敗した操作は作業ログにエントリを追加しない。
//     conflict・verification_rejected の経路を、対象の作業ログ
//     （ListActivities）の件数が呼び出し前後で変わらないことで確認する。
//     tty_required の経路は core レベルでは意味のある検証にならない
//     （Verifier が ErrTTYRequired を返した時点で Attestation を得られず
//     ExecuteApproval 自体を呼べないため）。tty_required は
//     internal/cli/rejection_invariants_test.go の疑似端末テストが担う。
//   - AC-47: 承認が成立すると、承認の記録の decided_at が設定される。
//   - AC-56: D12 で release ごとに記録される作業ログの経路（channel）が
//     cli であることを確認する（既存の
//     TestExecuteApproval_CompletionRecordsSeparateActivityEntriesForEachRelease
//     は actor・verification は見ているが channel は見ていない）。

// --- AC-65: conflict の経路は作業ログを増やさない ---

func TestExecuteApproval_ConflictDoesNotAddActivityLogEntry(t *testing.T) {
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

	before := activitiesFor(t, s, c.ID)

	att := verifiedAttestationForTest(t, c.ID)
	_, _, err = s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
	}, att)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("ExecuteApproval() error = %v, want ErrConflict", err)
	}

	after := activitiesFor(t, s, c.ID)
	if len(after) != len(before) {
		t.Fatalf("activity log = %d entries, want unchanged (%d) after conflict\nbefore=%+v\nafter=%+v", len(after), len(before), before, after)
	}
}

// --- AC-65: verification_rejected の経路は作業ログを増やさない ---

func TestExecuteApproval_VerificationRejectedDoesNotAddActivityLogEntry(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c.ID, 1, "x")
	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("PrepareApproval() error = %v", err)
	}

	before := activitiesFor(t, s, c.ID)

	// ゼロ値の Attestation は登録簿外（(channel, verification) が空）であり、
	// core.Verify を経由せずに直接 ExecuteApproval を呼んだ場合と同じ
	// fail-closed の経路を通る（§クリティカル設計決定 1）。
	_, _, err = s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
	}, Attestation{})
	if !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("err = %v, want ErrVerificationRejected", err)
	}

	after := activitiesFor(t, s, c.ID)
	if len(after) != len(before) {
		t.Fatalf("activity log = %d entries, want unchanged (%d) after verification_rejected\nbefore=%+v\nafter=%+v", len(after), len(before), before, after)
	}
}

// answer（T12）でも同じ不変条件が成り立つことを確認する（AC-65 は
// 「承認・差し戻し・保留への回答」に共通する操作全般の性質であり、
// approve だけの検証に閉じない）。
func TestExecuteAnswer_VerificationRejectedDoesNotAddActivityLogEntry(t *testing.T) {
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

	before := activitiesFor(t, s, c.ID)

	_, _, err = s.ExecuteAnswer(context.Background(), AnswerRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Answer: "because",
	}, Attestation{})
	if !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("err = %v, want ErrVerificationRejected", err)
	}

	after := activitiesFor(t, s, c.ID)
	if len(after) != len(before) {
		t.Fatalf("activity log = %d entries, want unchanged (%d) after verification_rejected\nbefore=%+v\nafter=%+v", len(after), len(before), before, after)
	}
}

// --- AC-47: 承認が成立すると decided_at が設定される ---

func TestExecuteApproval_RecordsDecidedAt(t *testing.T) {
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusAwaitingPlanApproval)
	insertPlanRowForTest(t, s, c.ID, 1, "x")
	prev, err := s.PrepareApproval(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("PrepareApproval() error = %v", err)
	}
	att := verifiedAttestationForTest(t, c.ID)

	_, approval, err := s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
	}, att)
	if err != nil {
		t.Fatalf("ExecuteApproval() error = %v", err)
	}
	if approval.DecidedAt.IsZero() {
		t.Fatalf("approval.DecidedAt is zero, want set")
	}

	// 呼び出しの戻り値だけでなく、ストアへ実際に書き込まれた値も確認する
	// （読み戻し。ExecuteApproval が構築した構造体の値だけが正しくても、
	// INSERT 文に decided_at を渡し忘れていれば読み戻しでは失われる）。
	detail, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if len(detail.Approvals) != 1 {
		t.Fatalf("Approvals = %+v, want 1 entry", detail.Approvals)
	}
	stored := detail.Approvals[0]
	if stored.DecidedAt.IsZero() {
		t.Fatalf("stored approval.DecidedAt is zero, want set")
	}
	if !stored.DecidedAt.Equal(approval.DecidedAt) {
		t.Fatalf("stored DecidedAt = %v, want equal to returned DecidedAt %v", stored.DecidedAt, approval.DecidedAt)
	}
}

// --- AC-56: D12 で release ごとに記録される作業ログの channel は cli ---

func TestExecuteApproval_CompletionReleaseApprovalActivityChannelIsCLI(t *testing.T) {
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

	all, err := s.ListActivities(context.Background(), &c.ID)
	if err != nil {
		t.Fatalf("ListActivities() error = %v", err)
	}
	var challengeApproveChannel string
	var sawChallengeApprove bool
	operationApproveChannel := map[string]string{}
	for _, a := range all {
		if a.Entity == "challenge" && a.Action == "approve" {
			sawChallengeApprove = true
			challengeApproveChannel = a.Channel
		}
		if a.Entity == "operation" && a.Action == "approve" {
			operationApproveChannel[a.EntityID] = a.Channel
		}
	}
	if !sawChallengeApprove {
		t.Fatalf("no challenge-entity approve activity found: %+v", all)
	}
	if challengeApproveChannel != string(ChannelCLI) {
		t.Errorf("challenge approve activity channel = %q, want %q", challengeApproveChannel, ChannelCLI)
	}
	for _, r := range releases {
		got, ok := operationApproveChannel[r.ID]
		if !ok {
			t.Errorf("no operation-entity approve activity found for %s: %+v", r.ID, all)
			continue
		}
		if got != string(ChannelCLI) {
			t.Errorf("operation %s approve activity channel = %q, want %q", r.ID, got, ChannelCLI)
		}
	}
}
