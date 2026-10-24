package core

import (
	"context"
	"database/sql"
	"testing"
)

// このファイルは M4.5 S2 の「委譲の起動の判定に依存を足す」（実ストア・偽の claude）を検証する。

// newDepTarget は依存先として使う課題（新規）を作り、その内部 ID を返す。
func (f *delegateFixture) newDepTarget(t *testing.T) int64 {
	t.Helper()
	ch, err := f.s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "dep"})
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	id, _ := parseChallengeID(ch.ID)
	return id
}

func (f *delegateFixture) dependOn(t *testing.T, id string, targets ...int64) {
	t.Helper()
	cid, _ := parseChallengeID(id)
	for _, to := range targets {
		insertDepEdge(t, f.s, cid, to)
	}
}

func (f *delegateFixture) setReleaseState(t *testing.T, cid int64, state string) {
	t.Helper()
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE operation SET state = ? WHERE challenge_id = ? AND kind = 'release'`, state, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func assertDependencyNotStarted(t *testing.T, res *DelegateResult, id, detail string) {
	t.Helper()
	if len(res.NotStarted) != 1 || res.NotStarted[0].ChallengeID != id ||
		res.NotStarted[0].Reason != NotStartedDependency || res.NotStarted[0].Detail != detail {
		t.Fatalf("not_started = %+v, want %s dependency %q", res.NotStarted, id, detail)
	}
	if len(res.Items) != 0 {
		t.Errorf("items = %+v, want none", res.Items)
	}
}

func TestDependency_IDOmittedRun_UnmetDependencyNotStartedWithSortedDetail(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	a, b := f.newDepTarget(t), f.newDepTarget(t)
	f.dependOn(t, id, b, a)
	res, err := f.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertDependencyNotStarted(t, res, id, formatChallengeID(a)+","+formatChallengeID(b))
	if len(f.deleg.launched()) != 0 || len(f.j3Inputs) != 0 {
		t.Errorf("launched %d, J3 %d; want nothing", len(f.deleg.launched()), len(f.j3Inputs))
	}
}

func TestDependency_OnlyUnmetTargetsAppearInDetail(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	done, open := f.newDepTarget(t), f.newDepTarget(t)
	setChallengeStatus(t, f.s, done, StatusDone)
	f.dependOn(t, id, done, open)
	res, err := f.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertDependencyNotStarted(t, res, id, formatChallengeID(open))
}

func TestDependency_IDGivenRun_UnmetIsNotStartedNotAnError(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	a := f.newDepTarget(t)
	f.dependOn(t, id, a)
	res, err := f.run(t, &id)
	if err != nil {
		t.Fatalf("RunDelegation: %v", err)
	}
	assertDependencyNotStarted(t, res, id, formatChallengeID(a))
	if len(f.deleg.launched()) != 0 || len(f.j3Inputs) != 0 {
		t.Error("nothing may be launched")
	}
}

func TestDependency_MetDependencyLaunches_BothPaths(t *testing.T) {
	for _, byID := range []bool{false, true} {
		f := newResumeFixture(t)
		id := f.newInProgress(t, "t", "P1", planSpec(nil))
		a := f.newDepTarget(t)
		setChallengeStatus(t, f.s, a, StatusDone)
		f.dependOn(t, id, a)
		var arg *string
		if byID {
			arg = &id
		}
		res, err := f.run(t, arg)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.NotStarted) != 0 || len(f.deleg.launched()) != 1 {
			t.Errorf("byID=%v: not_started %+v, launches %d", byID, res.NotStarted, len(f.deleg.launched()))
		}
	}
}

// 依存先が完了でも、その release が承認済みで未実行なら満たされず、実行済みになった周に起動する（AC-178 の core 側）。
func TestDependency_OpenReleaseBlocksUntilExecuted(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	a := f.newDepTarget(t)
	setChallengeStatus(t, f.s, a, StatusDone)
	setReleaseOp(t, f.s, a, "approved")
	f.dependOn(t, id, a)
	res, err := f.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertDependencyNotStarted(t, res, id, formatChallengeID(a))

	f.setReleaseState(t, a, "executed")
	res, err = f.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NotStarted) != 0 || len(f.deleg.launched()) != 1 {
		t.Errorf("not_started %+v, launches %d, want one launch", res.NotStarted, len(f.deleg.launched()))
	}
}

// 保留への回答による --resume も依存で止め、保留と回答を消費しない。満たされた周に同じ回答で再開する。
func TestDependency_AnswerResumeIsBlockedWithoutConsumingTheAnswer(t *testing.T) {
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.questionAndAnswer(t, id, "feat/x")
	first := f.lastLaunch(t)
	n := len(f.deleg.launched())
	a := f.newDepTarget(t)
	f.dependOn(t, id, a)
	v := f.version(t, id)

	for _, byID := range []bool{false, true} {
		var arg *string
		if byID {
			arg = &id
		}
		res, err := f.run(t, arg)
		if err != nil {
			t.Fatal(err)
		}
		assertDependencyNotStarted(t, res, id, formatChallengeID(a))
	}
	if len(f.deleg.launched()) != n || f.version(t, id) != v || f.detail(t, id).Status != StatusInProgress {
		t.Fatalf("launches %d→%d, version %d→%d: the blocked resume must change nothing", n, len(f.deleg.launched()), v, f.version(t, id))
	}

	setChallengeStatus(t, f.s, a, StatusDone)
	res, err := f.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := f.lastLaunch(t)
	if len(res.NotStarted) != 0 || len(f.deleg.launched()) != n+1 || !got.IsResume ||
		got.SessionID != first.SessionID || got.ResumeKind != ResumeKindAnswer || got.ResumeAnswer != "ANS" {
		t.Errorf("not_started %+v, launch = %+v, want the answer resume of %s", res.NotStarted, got, first.SessionID)
	}
}

// 循環に含まれる課題と、それに依存する課題は起動しない。循環の相手が完了でも detail は空にならない。
func TestDependency_CycleMembersAndTheirDependentsAreNotStarted(t *testing.T) {
	f := newResumeFixture(t)
	a := f.newInProgress(t, "a", "P1", planSpec(nil))
	b := f.newDepTarget(t)
	setChallengeStatus(t, f.s, b, StatusDone)
	aid, _ := parseChallengeID(a)
	f.dependOn(t, a, b)
	insertDepEdge(t, f.s, b, aid)

	res, err := f.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertDependencyNotStarted(t, res, a, formatChallengeID(b))
	res, err = f.run(t, &a)
	if err != nil {
		t.Fatal(err)
	}
	assertDependencyNotStarted(t, res, a, formatChallengeID(b))

	// 循環の外から循環の課題 b に依存する課題も、b を満たされていない依存先として止まる。
	g := newResumeFixture(t)
	x := g.newInProgress(t, "x", "P1", planSpec(nil))
	y, z := g.newDepTarget(t), g.newDepTarget(t)
	setChallengeStatus(t, g.s, y, StatusDone)
	setChallengeStatus(t, g.s, z, StatusDone)
	insertDepEdge(t, g.s, y, z)
	insertDepEdge(t, g.s, z, y)
	g.dependOn(t, x, y)
	res, err = g.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertDependencyNotStarted(t, res, x, formatChallengeID(y))
	if len(g.deleg.launched()) != 0 {
		t.Error("nothing may be launched")
	}
}
