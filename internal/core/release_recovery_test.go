package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// 本番反映の立て直し（#485）: 本番反映でマージできなくなった完了の課題を着手中へ戻し、マージ済みの操作を閉じる。

func (f *relFixture) setOpState(t *testing.T, state OperationState) {
	t.Helper()
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE operation SET state = ? WHERE id = ?`, string(state), f.opID)
		return err
	})
}

// setVerifiedHead は、J5 が relRef の PR を head で検証したことにする（検証の head の記録）。
func (f *relFixture) setVerifiedHead(t *testing.T, head string) {
	t.Helper()
	runID := coretest.InsertRun(t, f.s.workspace, coretest.InsertRunInput{
		ChallengeID: int(f.cid), Kind: "delegate", ChallengeVersion: 1, SessionID: "22222222-2222-4222-8222-222222222222",
		PID: 1, Host: "h", HeartbeatAt: execTS(0), StartedAt: execTS(0), MaxBudgetUSD: 1_000_000, BudgetBucket: "impl",
	})
	coretest.InsertRunArtifact(t, f.s.workspace, runID, "pr", relRef, "open", "main")
	coretest.SetRunArtifactVerifiedHead(t, f.s.workspace, runID, relRef, head)
}

func (f *relFixture) runAll(t *testing.T, compare UpstreamCompareSource) *ReleaseExecResult {
	t.Helper()
	opts := f.opts()
	opts.Compare = compare
	res, err := f.s.ExecuteReleases(context.Background(), nil, opts)
	if err != nil {
		t.Fatalf("ExecuteReleases(all) error = %v", err)
	}
	f.now = f.now.Add(time.Second)
	return res
}

func (f *relFixture) wantNoDiscrepancyFor(t *testing.T, opID int64) {
	t.Helper()
	ov, err := f.s.GetOverview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ov.Discrepancies {
		if d.OperationID == formatOperationID(opID) {
			t.Errorf("discrepancies = %+v, want none for the closed release %s", ov.Discrepancies, formatOperationID(opID))
		}
	}
}

func (f *relFixture) challengeStatus(t *testing.T) (Status, int) {
	t.Helper()
	snap := snapshotReopen(t, f.s, f.cid)
	return Status(snap.chStatus), snap.chVersion
}

// (i) 完了の課題の release が not_mergeable になったら、試行を not_mergeable で閉じ、課題を着手中へ戻す。
func TestRelease_NotMergeableOnADoneChallengeReopensItAndReplacesTheRelease(t *testing.T) {
	f := newRelFixture(t)
	prior := insertCompletionApprovalForTest(t, f.s, f.cid)
	no := false
	f.fake.pr.Mergeable = &no

	item := f.run(t)
	if item.Result != ExecutionResultNotMergeable || !item.Reopened || item.ReopenReason != ReopenReasonNotMergeable || item.NewOperationID == "" {
		t.Fatalf("item = %+v, want not_mergeable with the challenge reopened", item)
	}
	if len(f.fake.mergeArgs) != 0 {
		t.Errorf("merge calls = %d, want none", len(f.fake.mergeArgs))
	}
	at := f.attempts(t)
	if len(at) != 1 || at[0].Result != ExecutionResultNotMergeable {
		t.Fatalf("attempts = %+v, want one not_mergeable attempt", at)
	}
	snap := snapshotReopen(t, f.s, f.cid)
	if snap.chStatus != string(StatusInProgress) || snap.ops[0].state != string(OperationStateClosed) || snap.ops[0].closed.String != string(ClosedReasonSuperseded) ||
		snap.ops[1].state != string(OperationStatePending) || formatOperationID(snap.ops[1].id) != item.NewOperationID {
		t.Fatalf("snapshot = %+v", snap)
	}
	if len(snap.reopens) != 1 || snap.reopens[0].reason != string(ReopenReasonNotMergeable) || snap.reopens[0].approvalID.Int64 != prior {
		t.Errorf("reopens = %+v", snap.reopens)
	}
	// 取り消しと同じく、release_withdrawn の発生を残す（学習が見る）。
	f.wantSingleOccurrence(t, "release_withdrawn")
	// 次の周は、もう承認済みの release が無いので何も実行しない。
	if res := f.runAll(t, nil); len(res.Items) != 0 {
		t.Errorf("second run items = %+v, want none", res.Items)
	}
}

// 連続の上限に達した not_mergeable でも、完了の課題なら戻す。戻した理由は limit_reached。
func TestRelease_NotMergeableAtTheFailureLimitReopensWithReasonLimitReached(t *testing.T) {
	f := newRelFixture(t)
	f.limit = 2
	f.fake.pr.Checks = []UpstreamCheck{{Status: "completed", Conclusion: "failure"}}
	if item := f.run(t); item.Result != ExecutionResultChecksFailed || item.Reopened {
		t.Fatalf("first = %+v, want checks_failed counted without reopening", item)
	}
	no := false
	f.fake.pr.Checks = nil
	f.fake.pr.Mergeable = &no
	item := f.run(t)
	if item.Result != ExecutionResultNotMergeable || !item.Reopened || item.ReopenReason != ReopenReasonLimitReached {
		t.Fatalf("second = %+v, want not_mergeable reopened with limit_reached", item)
	}
	if snap := snapshotReopen(t, f.s, f.cid); len(snap.reopens) != 1 || snap.reopens[0].reason != string(ReopenReasonLimitReached) {
		t.Errorf("reopens = %+v", snap.reopens)
	}
}

// 課題が完了でなければ、今までどおり数える待ちで、上限で取り消して pending へ戻す。
func TestRelease_NotMergeableOnAChallengeThatIsNotDoneKeepsTheWaitAndLimitBehavior(t *testing.T) {
	f := newRelFixtureAt(t, StatusInProgress)
	f.limit = 2
	no := false
	f.fake.pr.Mergeable = &no
	item := f.run(t)
	if item.Result != ExecutionResultNotMergeable || item.Reopened || item.ConsecutiveFailures != 1 {
		t.Fatalf("first = %+v, want a counted wait", item)
	}
	if st := f.state(t); st.opState != "approved" {
		t.Errorf("state = %s, want approved", st.opState)
	}
	item = f.run(t)
	if item.Result != ExecutionResultLimitReached || item.Reopened {
		t.Fatalf("second = %+v, want limit_reached without reopening", item)
	}
	if st := f.state(t); st.opState != "pending" || st.chStatus != string(StatusInProgress) {
		t.Errorf("state = %+v, want the release pending and the challenge untouched", st)
	}
}

// (e) 承認していない head でマージ済みになった release は、pending へ戻さず closed（merged_unapproved）にする。
func TestRelease_MergedUnapprovedClosesTheReleaseKeepingTheOccurrence(t *testing.T) {
	f := newRelFixture(t)
	f.fake.pr = UpstreamPullRequestChecks{State: "merged", Base: "main", HeadSHA: "zzz"}
	item := f.run(t)
	if item.Result != ExecutionResultMergedUnapproved {
		t.Fatalf("item = %+v", item)
	}
	snap := snapshotReopen(t, f.s, f.cid)
	if snap.ops[0].state != string(OperationStateClosed) || snap.ops[0].closed.String != string(ClosedReasonMergedUnapproved) || !snap.ops[0].closedAt.Valid {
		t.Errorf("operation = %+v, want closed with merged_unapproved", snap.ops[0])
	}
	if snap.chStatus != string(StatusDone) || len(snap.ops) != 1 {
		t.Errorf("challenge/ops = %s %d, want the done challenge untouched and no new release", snap.chStatus, len(snap.ops))
	}
	f.wantSingleOccurrence(t, "unapproved_merge")
	// 閉じた操作は人が対応する対象ではないので、status の食い違いには出さない。
	f.wantNoDiscrepancyFor(t, f.opID)
	// 閉じた操作は承認も差し戻しもできない。
	if _, err := f.s.PrepareOperationApproval(context.Background(), formatOperationID(f.opID)); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("PrepareOperationApproval(closed) err = %v, want ErrInvalidTransition", err)
	}
	if _, err := f.s.PrepareOperationRejection(context.Background(), formatOperationID(f.opID), "no"); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("PrepareOperationRejection(closed) err = %v, want ErrInvalidTransition", err)
	}
}

// (e) 承認待ちの release の PR がマージ済みなら、周の release の段が closed（merged_outside）にする。
func TestRelease_SweepClosesAPendingReleaseWhoseHasBeenMerged(t *testing.T) {
	f := newRelFixture(t)
	f.setOpState(t, OperationStatePending)
	f.fake.pr = UpstreamPullRequestChecks{State: "merged", Base: "main", HeadSHA: relHead}
	_, v0 := f.challengeStatus(t)

	res := f.runAll(t, nil)
	if len(res.Items) != 0 || len(res.Tidied) != 1 {
		t.Fatalf("result = %+v, want one tidied item and no execution", res)
	}
	got := res.Tidied[0]
	if got.Action != ReleaseTidyClosed || got.ClosedReason != ClosedReasonMergedOutside || got.OperationID != formatOperationID(f.opID) {
		t.Errorf("tidied = %+v", got)
	}
	snap := snapshotReopen(t, f.s, f.cid)
	if snap.ops[0].state != string(OperationStateClosed) || snap.ops[0].closed.String != string(ClosedReasonMergedOutside) {
		t.Errorf("operation = %+v", snap.ops[0])
	}
	if snap.chVersion != v0+1 {
		t.Errorf("challenge version = %d, want %d (closing a pending release changes the set of unapproved operations)", snap.chVersion, v0+1)
	}
	// 試行を作らず、発生も作らない（flywheel の外のマージは flywheel の失敗ではない）。
	if at := f.attempts(t); len(at) != 0 {
		t.Errorf("attempts = %+v, want none", at)
	}
	if occ := f.occurrences(t); len(occ) != 0 {
		t.Errorf("occurrences = %+v, want none", occ)
	}
}

// 承認待ちの release の PR がマージされずに閉じられていれば、周の release の段が closed（pr_closed）にする。
func TestRelease_SweepClosesAPendingReleaseWhosePRWasClosedUnmerged(t *testing.T) {
	f := newRelFixture(t)
	f.setOpState(t, OperationStatePending)
	f.fake.pr = UpstreamPullRequestChecks{State: "closed", Base: "main", HeadSHA: relHead}
	_, v0 := f.challengeStatus(t)

	res := f.runAll(t, nil)
	if len(res.Items) != 0 || len(res.Tidied) != 1 {
		t.Fatalf("result = %+v, want one tidied item and no execution", res)
	}
	got := res.Tidied[0]
	if got.Action != ReleaseTidyClosed || got.ClosedReason != ClosedReasonPRClosed || got.OperationID != formatOperationID(f.opID) {
		t.Errorf("tidied = %+v", got)
	}
	snap := snapshotReopen(t, f.s, f.cid)
	if snap.ops[0].state != string(OperationStateClosed) || snap.ops[0].closed.String != string(ClosedReasonPRClosed) || !snap.ops[0].closedAt.Valid {
		t.Errorf("operation = %+v", snap.ops[0])
	}
	if snap.chVersion != v0+1 {
		t.Errorf("challenge version = %d, want %d", snap.chVersion, v0+1)
	}
	if at := f.attempts(t); len(at) != 0 {
		t.Errorf("attempts = %+v, want none", at)
	}
	if occ := f.occurrences(t); len(occ) != 0 {
		t.Errorf("occurrences = %+v, want none", occ)
	}
	// 閉じた release は終端。PR が開き直されても戻さない。
	f.fake.pr = UpstreamPullRequestChecks{State: "open", Base: "main", HeadSHA: relHead}
	if res := f.runAll(t, nil); len(res.Tidied) != 0 {
		t.Errorf("tidied after reopen = %+v, want none", res.Tidied)
	}
	after := snapshotReopen(t, f.s, f.cid)
	if after.ops[0].state != string(OperationStateClosed) || after.ops[0].closed.String != string(ClosedReasonPRClosed) || after.chVersion != snap.chVersion {
		t.Errorf("after reopen: operation = %+v version = %d, want still closed/pr_closed at %d", after.ops[0], after.chVersion, snap.chVersion)
	}
}

// pr_closed で閉じた後に PR が開き直されて本番反映が要るとき、戻す道を固定する（#539）。
// 課題が完了でなければ、人の登録（CreateOperation。`op add --kind release`）は同じ参照でも通る。
// 委譲の照合の登録（createOperation の skipDuplicate）は、閉じた release を同じ参照の重複として数えて作らない。
func TestRelease_PRClosedReleaseIsRestoredOnlyByAHumanRegistrationWhileNotDone(t *testing.T) {
	ctx := context.Background()
	f := newRelFixtureAt(t, StatusInProgress)
	f.setOpState(t, OperationStatePending)
	f.fake.pr = UpstreamPullRequestChecks{State: "closed", Base: "main", HeadSHA: relHead}
	if res := f.runAll(t, nil); len(res.Tidied) != 1 || res.Tidied[0].ClosedReason != ClosedReasonPRClosed {
		t.Fatalf("tidied = %+v, want one pr_closed", res.Tidied)
	}
	ref := relRef
	in := OperationInput{ChallengeID: formatChallengeID(f.cid), Kind: string(OperationKindRelease), Summary: "PR", Ref: &ref}

	// 委譲の照合の道は通らない（閉じた release が重複として数えられる）。
	if _, err := f.s.createOperation(ctx, ChannelInvoker, nil, true, in); !errors.Is(err, errOperationDuplicate) {
		t.Fatalf("createOperation(skipDuplicate) error = %v, want errOperationDuplicate", err)
	}
	// 人の登録の道は通る。
	op, err := f.s.CreateOperation(ctx, ChannelCLI, in)
	if err != nil {
		t.Fatalf("CreateOperation() error = %v", err)
	}
	if op.State != OperationStatePending || op.Ref == nil || *op.Ref != relRef || op.ID == formatOperationID(f.opID) {
		t.Errorf("operation = %+v, want a new pending release for the same ref", op)
	}
}

// 課題が完了なら、pr_closed で閉じた release を人の登録で戻す道も、委譲の照合の道も断られる（ErrTerminalState。#539）。
func TestRelease_PRClosedReleaseCannotBeRegisteredAgainWhenTheChallengeIsDone(t *testing.T) {
	ctx := context.Background()
	f := newRelFixture(t)
	f.setOpState(t, OperationStatePending)
	f.fake.pr = UpstreamPullRequestChecks{State: "closed", Base: "main", HeadSHA: relHead}
	if res := f.runAll(t, nil); len(res.Tidied) != 1 || res.Tidied[0].ClosedReason != ClosedReasonPRClosed {
		t.Fatalf("tidied = %+v, want one pr_closed", res.Tidied)
	}
	ref := relRef
	in := OperationInput{ChallengeID: formatChallengeID(f.cid), Kind: string(OperationKindRelease), Summary: "PR", Ref: &ref}
	if _, err := f.s.CreateOperation(ctx, ChannelCLI, in); !errors.Is(err, ErrTerminalState) {
		t.Fatalf("CreateOperation() error = %v, want ErrTerminalState", err)
	}
	if _, err := f.s.createOperation(ctx, ChannelInvoker, nil, true, in); !errors.Is(err, ErrTerminalState) {
		t.Fatalf("createOperation(skipDuplicate) error = %v, want ErrTerminalState", err)
	}
}

// 直前の試行の結果が merged_unapproved の承認待ちの release は、その理由を保って閉じる。
func TestRelease_SweepKeepsMergedUnapprovedAsTheClosedReason(t *testing.T) {
	f := newRelFixture(t)
	f.fake.pr = UpstreamPullRequestChecks{State: "merged", Base: "main", HeadSHA: "zzz"}
	// 取り消しの後でマージされた事象（pending のまま、直前の試行が merged_unapproved）を作る。
	f.setOpState(t, OperationStatePending)
	coretest.InsertOperationExecution(t, f.s.workspace, f.opID, coretest.ExecutionAttempt{
		StartedAt: execTS(10), EndedAt: execTS(11), Result: string(ExecutionResultMergedUnapproved), ApprovedHead: relHead})

	res := f.runAll(t, nil)
	if len(res.Tidied) != 1 || res.Tidied[0].ClosedReason != ClosedReasonMergedUnapproved {
		t.Fatalf("tidied = %+v, want closed with merged_unapproved", res.Tidied)
	}
	f.wantNoDiscrepancyFor(t, f.opID)
	if snap := snapshotReopen(t, f.s, f.cid); snap.ops[0].closed.String != string(ClosedReasonMergedUnapproved) {
		t.Errorf("operation = %+v", snap.ops[0])
	}
}

func TestRelease_SweepLeavesOpenAndUnreadablePendingReleasesAlone(t *testing.T) {
	cases := []struct {
		name string
		mod  func(f *relFixture)
	}{
		{"open PR", func(*relFixture) {}},
		{"read failure", func(f *relFixture) { f.fake.getErr = errors.New("gh down") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRelFixture(t)
			f.setOpState(t, OperationStatePending)
			tc.mod(f)
			res := f.runAll(t, nil)
			if len(res.Tidied) != 0 {
				t.Fatalf("tidied = %+v, want none", res.Tidied)
			}
			if snap := snapshotReopen(t, f.s, f.cid); snap.ops[0].state != string(OperationStatePending) {
				t.Errorf("operation = %+v, want pending", snap.ops[0])
			}
		})
	}
}

// ID を指定した op exec は承認待ちの release を読まない。
func TestRelease_SweepOnlyRunsWhenNoIDIsGiven(t *testing.T) {
	f := newRelFixture(t)
	f.setOpState(t, OperationStatePending)
	f.fake.pr = UpstreamPullRequestChecks{State: "merged", Base: "main", HeadSHA: relHead}
	res, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, f.opts())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tidied) != 0 {
		t.Errorf("tidied = %+v, want none for an explicit ID", res.Tidied)
	}
	if snap := snapshotReopen(t, f.s, f.cid); snap.ops[0].state != string(OperationStatePending) {
		t.Errorf("operation = %+v, want pending", snap.ops[0])
	}
}

// (iii) 完了の課題の承認待ちの release の PR に、検証の後に取り込み以外の変更が入っていれば、課題を着手中へ戻す。
func TestRelease_SweepReopensADoneChallengeWhenTheVerifiedPRContentChanged(t *testing.T) {
	f := newRelFixture(t)
	f.setOpState(t, OperationStatePending)
	f.setVerifiedHead(t, "verified")
	f.fake.pr.HeadSHA = "newhead"
	cmp := newFakeCompare()
	cmp.set("verified", "newhead", ahead(commit("c1", "add feature", "verified"), commit("newhead", "Merge", "c1", "m1"), commit("m1", "main", "m0")))
	cmp.set("main", "newhead", ahead(commit("verified", "fix", "p0"), commit("c1", "add feature", "verified"), commit("newhead", "Merge", "c1", "m1")))

	res := f.runAll(t, cmp)
	if len(res.Tidied) != 1 || res.Tidied[0].Action != ReleaseTidyReopened || res.Tidied[0].ReopenReason != ReopenReasonContentChanged || res.Tidied[0].NewOperationID == "" {
		t.Fatalf("tidied = %+v, want reopened with content_changed", res.Tidied)
	}
	snap := snapshotReopen(t, f.s, f.cid)
	if snap.chStatus != string(StatusInProgress) || snap.ops[0].state != string(OperationStateClosed) || len(snap.ops) != 2 {
		t.Errorf("snapshot = %+v", snap)
	}
}

func TestRelease_SweepDoesNotReopenForAMergeInOnlyChangeOrWithoutASource(t *testing.T) {
	merge := func() *fakeCompare {
		cmp := newFakeCompare()
		cmp.set("verified", "newhead", withFiles(ahead(commit("m1", "main", "m0"), commit("newhead", "Merge", "verified", "m1")), UpstreamFile{Filename: "a", Patch: "p"}))
		cmp.set("main", "newhead", ahead(commit("verified", "fix", "p0"), commit("newhead", "Merge", "verified", "m1")))
		cmp.set("m1", "newhead", withFiles(ahead(commit("verified", "fix", "p0")), UpstreamFile{Filename: "a", Patch: "q"}))
		return cmp
	}
	cases := []struct {
		name    string
		compare func() UpstreamCompareSource
		mod     func(f *relFixture)
	}{
		{"merge-in only", func() UpstreamCompareSource { return merge() }, func(*relFixture) {}},
		{"no compare source", func() UpstreamCompareSource { return nil }, func(*relFixture) {}},
		{"head unchanged", func() UpstreamCompareSource { return newFakeCompare() }, func(f *relFixture) { f.fake.pr.HeadSHA = "verified" }},
		{"challenge not done", func() UpstreamCompareSource { return newFakeCompare() }, func(f *relFixture) { setChallengeStatus(t, f.s, f.cid, StatusInProgress) }},
		{"comparison failure", func() UpstreamCompareSource { return newFakeCompare() }, func(*relFixture) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRelFixture(t)
			f.setOpState(t, OperationStatePending)
			f.setVerifiedHead(t, "verified")
			f.fake.pr.HeadSHA = "newhead"
			tc.mod(f)
			res := f.runAll(t, tc.compare())
			if len(res.Tidied) != 0 {
				t.Fatalf("tidied = %+v, want none", res.Tidied)
			}
			if snap := snapshotReopen(t, f.s, f.cid); snap.ops[0].state != string(OperationStatePending) || len(snap.ops) != 1 {
				t.Errorf("snapshot = %+v, want the pending release untouched", snap)
			}
		})
	}
}

// 履歴が書き換わった（force-push 等）と上流が返したときは、検証し直す必要があるので戻す。
func TestRelease_SweepReopensWhenTheHistoryWasRewritten(t *testing.T) {
	f := newRelFixture(t)
	f.setOpState(t, OperationStatePending)
	f.setVerifiedHead(t, "verified")
	f.fake.pr.HeadSHA = "rebased"
	cmp := newFakeCompare()
	c := ahead(commit("rebased", "x", "m9"))
	c.Status = UpstreamCompareDiverged
	cmp.set("verified", "rebased", c)
	res := f.runAll(t, cmp)
	if len(res.Tidied) != 1 || res.Tidied[0].Action != ReleaseTidyReopened {
		t.Fatalf("tidied = %+v, want reopened", res.Tidied)
	}
}
