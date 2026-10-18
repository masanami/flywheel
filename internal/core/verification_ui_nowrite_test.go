package core

import (
	"context"
	"errors"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// 経路 ui・方式 none の本人確認の結果は、不可逆操作の承認・差し戻し・予算の増額を
// verification_rejected で拒否し、ストアのどの表（作業ログ activity を含む）にも何も書かない。
// 拒否だけを見ても、対象が無いための拒否と区別できないので、同じ入力を経路 cli・方式
// tty_confirm の結果で呼ぶと成立すること（＝対象は実在し、経路だけが理由であること）も確かめる。

var allStoreTables = []string{
	"challenge", "task_plan", "approval", "operation", "activity", "hold",
	"run", "run_artifact", "cycle", "lock", "slot", "source_binding",
}

func newWorkspaceStoreForTest(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	if _, err := Init(dir); err != nil {
		t.Fatalf("Init() setup error = %v", err)
	}
	s, err := OpenWorkspace(dir)
	if err != nil {
		t.Fatalf("OpenWorkspace() setup error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

func assertNothingWritten(t *testing.T, dir, before string) {
	t.Helper()
	if after := coretest.DumpTables(t, dir, allStoreTables...); after != before {
		t.Errorf("拒否した操作がストアに書いた\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestUIRefusedOperationApprovalWritesNothing(t *testing.T) { // AC-169
	s, dir := newWorkspaceStoreForTest(t)
	fixedActor(t, "alice")
	_, releases, _ := setUpChallengeAwaitingCompletionWithOperations(t, s)
	id := releases[0].ID
	prev, err := s.PrepareOperationApproval(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	req := OperationApprovalRequest{OperationID: id, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion, Decision: ApprovalDecisionApproved}
	before := coretest.DumpTables(t, dir, allStoreTables...)
	if _, _, err := s.ExecuteOperationApproval(context.Background(), req, uiAttestationForTest(t, id)); !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("err = %v, want ErrVerificationRejected", err)
	}
	assertNothingWritten(t, dir, before)
	// 対照: 経路 cli・方式 tty_confirm なら同じ要求が成立する。
	if _, _, err := s.ExecuteOperationApproval(context.Background(), req, verifiedAttestationForTest(t, id)); err != nil {
		t.Fatalf("control (cli, tty_confirm): err = %v", err)
	}
}

func TestUIRefusedOperationRejectionWritesNothing(t *testing.T) { // AC-170
	s, dir := newWorkspaceStoreForTest(t)
	fixedActor(t, "alice")
	_, releases, _ := setUpChallengeAwaitingCompletionWithOperations(t, s)
	id := releases[0].ID
	reason := "no"
	prev, err := s.PrepareOperationRejection(context.Background(), id, reason)
	if err != nil {
		t.Fatal(err)
	}
	req := OperationApprovalRequest{OperationID: id, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion, Decision: ApprovalDecisionRejected, Reason: &reason}
	before := coretest.DumpTables(t, dir, allStoreTables...)
	if _, _, err := s.ExecuteOperationApproval(context.Background(), req, uiAttestationForTest(t, id)); !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("err = %v, want ErrVerificationRejected", err)
	}
	assertNothingWritten(t, dir, before)
	if _, _, err := s.ExecuteOperationApproval(context.Background(), req, verifiedAttestationForTest(t, id)); err != nil {
		t.Fatalf("control (cli, tty_confirm): err = %v", err)
	}
}

func TestUIRefusedBudgetIncreaseWritesNothing(t *testing.T) { // AC-171（存在する課題・承認済みの計画に対して呼ぶ）
	s, dir := newWorkspaceStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	coretest.InsertApprovedPlan(t, dir, int(mustParseChallengeID(t, c.ID)), "PLAN", `{"verdict":"plan","size":"M","budget_impl_usd":50,"budget_review_usd":30}`)
	cur, err := s.GetChallenge(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	req := BudgetRequest{ChallengeID: c.ID, ExpectedVersion: cur.Version, PlanVersion: 1, ImplUSD: 80}
	before := coretest.DumpTables(t, dir, allStoreTables...)
	if _, err := s.ExecuteBudget(context.Background(), req, uiAttestationForTest(t, c.ID)); !errors.Is(err, ErrVerificationRejected) {
		t.Fatalf("err = %v, want ErrVerificationRejected", err)
	}
	assertNothingWritten(t, dir, before)
	if _, err := s.ExecuteBudget(context.Background(), req, verifiedAttestationForTest(t, c.ID)); err != nil {
		t.Fatalf("control (cli, tty_confirm): 課題と計画が実在し、経路だけが拒否の理由のはず: err = %v", err)
	}
}
