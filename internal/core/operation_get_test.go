package core

import (
	"context"
	"errors"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

func connectorsWithRepoForTest(remote, branch string) *ConnectorsDeclaration {
	return &ConnectorsDeclaration{Repos: []ConnectorRepo{{Name: "r", Remote: remote, DefaultBranch: branch}}}
}

func TestGetOperation_ReturnsOperationChallengeAndTarget(t *testing.T) {
	t.Parallel()
	s, cid, opID := releaseFixtureForTest(t)

	d, err := s.GetOperation(context.Background(), formatOperationID(opID), connectorsWithRepoForTest("O/R", "trunk"))
	if err != nil {
		t.Fatalf("GetOperation() error = %v", err)
	}
	c, err := s.GetChallenge(context.Background(), formatChallengeID(cid))
	if err != nil {
		t.Fatalf("GetChallenge() error = %v", err)
	}
	if d.Operation.ID != formatOperationID(opID) || d.Operation.ChallengeID != c.ID || d.ChallengeTitle != c.Title ||
		d.Operation.Kind != OperationKindRelease || d.Operation.State != OperationStateApproved {
		t.Errorf("detail = %+v", d)
	}
	// 実行の表示用の値も show・status の要素と同じく埋まる。
	if d.Operation.ApprovedHead == nil || *d.Operation.ApprovedHead != "a1b2c3" {
		t.Errorf("ApprovedHead = %v, want a1b2c3", d.Operation.ApprovedHead)
	}
	tg := d.Target
	if tg.Repo == nil || *tg.Repo != "o/r" || tg.PullRequestNumber == nil || *tg.PullRequestNumber != 1 ||
		tg.PullRequestURL == nil || *tg.PullRequestURL != "https://github.com/o/r/pull/1" || tg.BaseBranch == nil || *tg.BaseBranch != "trunk" {
		t.Errorf("target = %+v", tg)
	}
}

// 反映先は、反映の実行と同じく宣言のそのリポジトリの既定ブランチ。成果物の記録の base は使わず、宣言に無ければ nil。
func TestGetOperation_BaseBranch(t *testing.T) {
	t.Parallel()
	s, cid, opID := releaseFixtureForTest(t)
	run := coretest.InsertRun(t, s.workspace, coretest.InsertRunInput{
		ChallengeID: int(cid), Kind: "delegate", ChallengeVersion: 1, SessionID: "s", PID: 1, Host: "h", HeartbeatAt: execTS(0), StartedAt: execTS(0),
		EndedAt: execTS(1), Result: "succeeded", MaxBudgetUSD: 1, BudgetBucket: "impl",
	})
	coretest.InsertRunArtifact(t, s.workspace, run, "pr", "https://github.com/o/r/pull/1", "open", "develop")
	get := func(decl *ConnectorsDeclaration) *string {
		t.Helper()
		d, err := s.GetOperation(context.Background(), formatOperationID(opID), decl)
		if err != nil {
			t.Fatalf("GetOperation() error = %v", err)
		}
		return d.Target.BaseBranch
	}
	if got := get(connectorsWithRepoForTest("o/r", "trunk")); got == nil || *got != "trunk" {
		t.Errorf("base = %v, want trunk", got)
	}
	if got := get(nil); got != nil {
		t.Errorf("no declaration: base = %q, want nil", *got)
	}
	if got := get(connectorsWithRepoForTest("o/other", "trunk")); got != nil {
		t.Errorf("declaration of another repo: base = %q, want nil", *got)
	}
}

// release でない操作と、PR の URL でない参照は、反映先を持たない。
func TestGetOperation_NonReleaseAndFreeRef(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusUnclassified)
	decl := connectorsWithRepoForTest("o/r", "trunk")
	pr, free := "https://github.com/o/r/pull/3", "bucket/x"
	for _, in := range []OperationInput{
		{ChallengeID: c.ID, Kind: string(OperationKindDelete), Summary: "a", Ref: &pr},
		{ChallengeID: c.ID, Kind: string(OperationKindRelease), Summary: "b", Ref: &free},
	} {
		op, err := s.CreateOperation(context.Background(), ChannelCLI, in)
		if err != nil {
			t.Fatalf("CreateOperation() error = %v", err)
		}
		d, err := s.GetOperation(context.Background(), op.ID, decl)
		if err != nil {
			t.Fatalf("GetOperation(%s) error = %v", op.ID, err)
		}
		if d.Target.BaseBranch != nil {
			t.Errorf("%s: base = %q, want nil", op.ID, *d.Target.BaseBranch)
		}
		if (in.Ref == &free) != (d.Target.Repo == nil) {
			t.Errorf("%s: repo = %v", op.ID, d.Target.Repo)
		}
	}
}

func TestGetOperation_NotFound(t *testing.T) {
	t.Parallel()
	s, _, _ := releaseFixtureForTest(t)
	for _, id := range []string{"OP-99", "OP-0", "OP-01", "OP-", "C-1", "1"} {
		if _, err := s.GetOperation(context.Background(), id, nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetOperation(%q) error = %v, want ErrNotFound", id, err)
		}
	}
}
