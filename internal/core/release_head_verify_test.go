package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// --- 検証の後に動いた head を core が自分で比較し直して確かめる規則（#517）---

// reopenExecPath は、本番反映の段が試行の結果から戻す道（reopenForReleaseInTx）を、公開の入口の検査なしで呼ぶ。
// 戻す書き込みそのもの（T18）の検査に使う。公開の入口 ReopenChallengeForRelease は core が比較して確かめる。
func reopenExecPath(ctx context.Context, s *Store, ch Channel, in ReopenForReleaseInput) (*ReopenForReleaseResult, error) {
	if _, ok := ParseReopenReason(string(in.Reason)); !ok {
		return nil, ErrValidation
	}
	opID, ok := parseOperationID(in.OperationID)
	if !ok {
		return nil, ErrNotFound
	}
	actor, err := s.resolveActor()
	if err != nil {
		return nil, err
	}
	var res *ReopenForReleaseResult
	err = s.mutateAs(ctx, actor, ch, VerificationNone, func(tx *sql.Tx, rec *activityRecorder) error {
		var err error
		res, err = reopenForReleaseInTx(ctx, tx, rec, opID, in.Reason, nil)
		return err
	})
	return res, err
}

// prFake は PR の現在の状態を固定で返す UpstreamCheckSource。
type prFake struct {
	pr  UpstreamPullRequestChecks
	err error
}

func (p *prFake) GetPullRequestChecks(context.Context, string, int) (UpstreamPullRequestChecks, error) {
	return p.pr, p.err
}

func passingChecks() []UpstreamCheck {
	return []UpstreamCheck{{Name: "ci", Status: "completed", Conclusion: "success"}}
}

func openPRAt(head string, checks []UpstreamCheck) *prFake {
	return &prFake{pr: UpstreamPullRequestChecks{State: "open", Base: "main", HeadSHA: head, Checks: checks}}
}

// compareMergeIn は検証の head v から h1（既定ブランチの取り込みだけ）への履歴。
func compareMergeIn() *fakeCompare {
	cmp := newFakeCompare()
	cmp.set("v", "h1", withFiles(ahead(commit("m1", "main", "m0"), commit("h1", "Merge", "v", "m1")), UpstreamFile{Filename: "a", Patch: "p"}))
	cmp.set("main", "h1", ahead(commit("v", "fix", "p0"), commit("h1", "Merge", "v", "m1")))
	cmp.set("m1", "h1", withFiles(ahead(commit("v", "fix", "p0")), UpstreamFile{Filename: "a", Patch: "q"}))
	return cmp
}

// compareContentChanged は v から hc（取り込み以外のコミットが入った）への履歴。
func compareContentChanged() *fakeCompare {
	cmp := newFakeCompare()
	cmp.set("v", "hc", ahead(commit("hc", "add feature", "v")))
	cmp.set("main", "hc", ahead(commit("v", "fix", "p0"), commit("hc", "add feature", "v")))
	return cmp
}

// compareTruncated は v から ht への比較の一覧が打ち切られている（取り直しても変わらない。人間待ち）。
func compareTruncated() *fakeCompare {
	cmp := newFakeCompare()
	c := ahead(commit("ht", "x", "v"))
	c.TotalCommits = 500
	cmp.set("v", "ht", c)
	return cmp
}

func sourcesOf(pr *prFake, cmp UpstreamCompareSource) ReleaseHeadSources {
	return ReleaseHeadSources{Checks: pr, Compare: cmp}
}

func (f *headFixture) approveWith(t *testing.T, heads []ReleaseHead, src ReleaseHeadSources) error {
	t.Helper()
	prev, err := f.s.PrepareApproval(context.Background(), f.c.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: f.c.ID, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
		ReleaseHeads: heads, HeadSources: src,
	}, verifiedAttestationForTest(t, f.c.ID))
	return err
}

func (f *headFixture) approveOpWith(t *testing.T, op *IrreversibleOperation, head string, src ReleaseHeadSources, acceptUndeterminable bool) error {
	t.Helper()
	prev, err := f.s.PrepareOperationApproval(context.Background(), op.ID)
	if err != nil {
		return err
	}
	_, _, err = f.s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: op.ID, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion,
		Decision: ApprovalDecisionApproved, HeadSHA: head, HeadSources: src, AcceptUndeterminable: acceptUndeterminable,
	}, verifiedAttestationForTest(t, op.ID))
	return err
}

// 指摘 2: core は、検証の head と異なる head の承認を、自分で比較した結果（取り込みだけ・CI）で確かめる。
func TestExecuteOperationApproval_CoreVerifiesTheHeadItself(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		head    string
		pr      *prFake
		cmp     UpstreamCompareSource
		wantErr bool
	}{
		{"merge-in only and CI passed", "h1", openPRAt("h1", passingChecks()), compareMergeIn(), false},
		{"content changed is refused", "hc", openPRAt("hc", passingChecks()), compareContentChanged(), true},
		{"merge-in only but CI failed", "h1", openPRAt("h1", []UpstreamCheck{{Name: "ci", Status: "completed", Conclusion: "failure"}}), compareMergeIn(), true},
		{"merge-in only but no checks", "h1", openPRAt("h1", nil), compareMergeIn(), true},
		{"the PR head moved after the head was read", "h1", openPRAt("other", passingChecks()), compareMergeIn(), true},
		{"PR unreadable", "h1", &prFake{err: errors.New("down")}, compareMergeIn(), true},
		{"no compare source", "h1", openPRAt("h1", passingChecks()), nil, true},
		{"undeterminable is refused without the human choice", "ht", openPRAt("ht", passingChecks()), compareTruncated(), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newHeadFixture(t)
			f.verified(t, hdrefA, "v")
			before := f.dump(t)
			err := f.approveOpWith(t, f.relA, tc.head, sourcesOf(tc.pr, tc.cmp), false)
			if tc.wantErr {
				if !errors.Is(err, ErrConflict) {
					t.Fatalf("err = %v, want ErrConflict", err)
				}
				if f.dump(t) != before {
					t.Error("the store changed on a refused approval")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := f.mergeInOf(t, f.relA); got.String != "v" {
				t.Errorf("merge_in_verified_head = %v, want v", got)
			}
		})
	}
}

// 完了の承認と同時に承認する release も、core が自分で確かめる（CLI が取り込みだけと言っても使わない）。
func TestExecuteApproval_CoreVerifiesMergeInItself(t *testing.T) {
	t.Parallel()
	f := newHeadFixture(t)
	f.verified(t, hdrefA, "v")
	heads := []ReleaseHead{f.rh(f.relA, "hc"), f.rh(f.relB, "anything")}
	if err := f.approveWith(t, heads, sourcesOf(openPRAt("hc", passingChecks()), compareContentChanged())); !errors.Is(err, ErrConflict) {
		t.Fatalf("content changed: err = %v, want ErrConflict", err)
	}
	heads = []ReleaseHead{f.rh(f.relA, "h1"), f.rh(f.relB, "anything")}
	if err := f.approveWith(t, heads, ReleaseHeadSources{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("no sources: err = %v, want ErrConflict", err)
	}
	if err := f.approveWith(t, heads, sourcesOf(openPRAt("h1", passingChecks()), compareMergeIn())); err != nil {
		t.Fatalf("merge-in only: %v", err)
	}
	if got := f.mergeInOf(t, f.relA); got.String != "v" {
		t.Errorf("merge_in_verified_head = %v, want v", got)
	}
}

// 指摘 3: 人間待ち（取り込みだけとも中身の変更とも確かめられない）の head は、人が選んだときだけ、core が自分でも人間待ちと
// 確かめた場合に承認でき、理由が作業ログに残る。
func TestExecuteOperationApproval_HumanAcceptsAnUndeterminableHead(t *testing.T) {
	t.Parallel()
	f := newHeadFixture(t)
	f.verified(t, hdrefA, "v")
	src := sourcesOf(openPRAt("ht", passingChecks()), compareTruncated())
	if err := f.approveOpWith(t, f.relA, "ht", src, true); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if h := f.headOf(t, f.relA); h == nil || *h != "ht" {
		t.Errorf("head_sha = %v, want ht", h)
	}
	if got := f.mergeInOf(t, f.relA); got.Valid {
		t.Errorf("merge_in_verified_head = %v, want NULL (not a merge-in judgement)", got)
	}
	var after sql.NullString
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT after FROM activity WHERE entity = 'operation' AND action = 'approve' ORDER BY id DESC LIMIT 1`).Scan(&after)
	})
	if !after.Valid || !strings.Contains(after.String, "undeterminable_head_accepted") || !strings.Contains(after.String, "truncated") {
		t.Errorf("activity after = %v, want the reason of the undeterminable head", after)
	}
}

// 人が選んでも、core の評価が人間待ちでなければ（中身の変更・取得の失敗・口が無い）承認しない。
func TestExecuteOperationApproval_HumanChoiceDoesNotOverrideContentChanged(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		head string
		pr   *prFake
		cmp  UpstreamCompareSource
	}{
		{"content changed", "hc", openPRAt("hc", passingChecks()), compareContentChanged()},
		{"comparison failure", "hx", openPRAt("hx", passingChecks()), newFakeCompare()},
		{"no compare source", "ht", openPRAt("ht", passingChecks()), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newHeadFixture(t)
			f.verified(t, hdrefA, "v")
			before := f.dump(t)
			if err := f.approveOpWith(t, f.relA, tc.head, sourcesOf(tc.pr, tc.cmp), true); !errors.Is(err, ErrConflict) {
				t.Fatalf("err = %v, want ErrConflict", err)
			}
			if f.dump(t) != before {
				t.Error("the store changed on a refused approval")
			}
		})
	}
}

// --- 戻す判定（指摘 1・3）---

func TestAssessReleaseReopen_Verdicts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		mod  func(f *relFixture)
		cmp  UpstreamCompareSource
		want ReleaseReopenVerdict
	}{
		{"content changed", func(f *relFixture) { f.fake.pr.HeadSHA = "hc" }, compareContentChanged(), ReleaseReopenRequired},
		{"history rewritten", func(f *relFixture) { f.fake.pr.HeadSHA = "rb" }, func() UpstreamCompareSource {
			c := ahead(commit("rb", "x", "m9"))
			c.Status = UpstreamCompareDiverged
			cmp := newFakeCompare()
			cmp.set("v", "rb", c)
			return cmp
		}(), ReleaseReopenRequired},
		{"truncated comparison waits for a human", func(f *relFixture) { f.fake.pr.HeadSHA = "ht" }, compareTruncated(), ReleaseReopenNeedsHuman},
		{"no head waits for a human", func(f *relFixture) { f.fake.pr.HeadSHA = "" }, newFakeCompare(), ReleaseReopenNeedsHuman},
		{"merge-in only", func(f *relFixture) { f.fake.pr.HeadSHA = "h1" }, compareMergeIn(), ReleaseReopenNone},
		{"comparison failure is retried later", func(f *relFixture) { f.fake.pr.HeadSHA = "hx" }, newFakeCompare(), ReleaseReopenNone},
		{"no compare source", func(f *relFixture) { f.fake.pr.HeadSHA = "hc" }, nil, ReleaseReopenNone},
		{"head unchanged", func(f *relFixture) { f.fake.pr.HeadSHA = "v" }, newFakeCompare(), ReleaseReopenNone},
		{"challenge not done", func(f *relFixture) { f.fake.pr.HeadSHA = "hc"; setChallengeStatus(t, f.s, f.cid, StatusInProgress) }, compareContentChanged(), ReleaseReopenNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRelFixture(t)
			f.setVerifiedHead(t, "v")
			tc.mod(f)
			as, err := f.s.AssessReleaseReopen(context.Background(), formatOperationID(f.opID), ReleaseHeadSources{Checks: f.fake, Compare: tc.cmp})
			if err != nil {
				t.Fatal(err)
			}
			if as.Verdict != tc.want {
				t.Fatalf("verdict = %q (%s), want %q", as.Verdict, as.Detail, tc.want)
			}
			if tc.want == ReleaseReopenNeedsHuman && as.Detail == "" {
				t.Error("a wait for a human must carry the reason")
			}
		})
	}
	if _, err := newRelFixture(t).s.AssessReleaseReopen(context.Background(), "OP-999", ReleaseHeadSources{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown operation: err = %v, want ErrNotFound", err)
	}
}

// 公開の入口は、戻す理由を core が自分で確かめ、規則に合わなければ何も書かず断る。
func TestReopenChallengeForRelease_CoreChecksTheReason(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		reason  ReopenReason
		head    string
		cmp     UpstreamCompareSource
		wantErr error
	}{
		{"content_changed and the content really changed", ReopenReasonContentChanged, "hc", compareContentChanged(), nil},
		{"content_changed claimed for a merge-in only change", ReopenReasonContentChanged, "h1", compareMergeIn(), ErrConflict},
		{"content_changed claimed for an undeterminable change", ReopenReasonContentChanged, "ht", compareTruncated(), ErrConflict},
		{"content_changed without a compare source", ReopenReasonContentChanged, "hc", nil, ErrConflict},
		{"undeterminable and waiting for a human", ReopenReasonUndeterminable, "ht", compareTruncated(), nil},
		{"undeterminable claimed for changed content", ReopenReasonUndeterminable, "hc", compareContentChanged(), ErrConflict},
		{"undeterminable claimed for a merge-in only change", ReopenReasonUndeterminable, "h1", compareMergeIn(), ErrConflict},
		{"not_mergeable is not accepted from outside", ReopenReasonNotMergeable, "hc", compareContentChanged(), ErrValidation},
		{"limit_reached is not accepted from outside", ReopenReasonLimitReached, "hc", compareContentChanged(), ErrValidation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRelFixture(t)
			f.setVerifiedHead(t, "v")
			f.fake.pr.HeadSHA = tc.head
			before := snapshotReopen(t, f.s, f.cid)
			res, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, ReopenForReleaseInput{
				OperationID: formatOperationID(f.opID), Reason: tc.reason, Sources: ReleaseHeadSources{Checks: f.fake, Compare: tc.cmp},
			})
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				after := snapshotReopen(t, f.s, f.cid)
				if after.chStatus != before.chStatus || len(after.ops) != len(before.ops) || len(after.reopens) != 0 {
					t.Errorf("a refused reopen wrote: %+v", after)
				}
				return
			}
			if err != nil || !res.Reopened {
				t.Fatalf("res = %+v, err = %v, want reopened", res, err)
			}
			if snap := snapshotReopen(t, f.s, f.cid); len(snap.reopens) != 1 || snap.reopens[0].reason != string(tc.reason) {
				t.Errorf("reopens = %+v, want the reason %s", snap.reopens, tc.reason)
			}
		})
	}
}

// 完了でない課題・戻す対象でない操作は、評価せず今までどおり何も書かず Reopened=false。
func TestReopenChallengeForRelease_NotEligibleStillReturnsNotReopened(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	f.setVerifiedHead(t, "v")
	setChallengeStatus(t, f.s, f.cid, StatusInProgress)
	res, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, ReopenForReleaseInput{OperationID: formatOperationID(f.opID), Reason: ReopenReasonContentChanged})
	if err != nil || res.Reopened {
		t.Fatalf("res = %+v, err = %v, want not reopened without error", res, err)
	}
}

// 指摘 3: 人間待ちは片付けの結果に理由つきで出て、何も書かない。
func TestRelease_SweepReportsAHumanWaitWithoutWriting(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		head string
		cmp  UpstreamCompareSource
	}{
		{"truncated comparison", "ht", compareTruncated()},
		{"the PR has no head", "", newFakeCompare()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRelFixture(t)
			f.setOpState(t, OperationStatePending)
			f.setVerifiedHead(t, "v")
			f.fake.pr.HeadSHA = tc.head
			res := f.runAll(t, tc.cmp)
			if len(res.Tidied) != 1 || res.Tidied[0].Action != ReleaseTidyNeedsHuman || res.Tidied[0].WaitReason == "" {
				t.Fatalf("tidied = %+v, want needs_human with a reason", res.Tidied)
			}
			if snap := snapshotReopen(t, f.s, f.cid); snap.chStatus != string(StatusDone) || len(snap.ops) != 1 || snap.ops[0].state != string(OperationStatePending) || len(snap.reopens) != 0 {
				t.Errorf("snapshot = %+v, want nothing written", snap)
			}
		})
	}
}

// 人間待ちの release は、人が承認せず着手中へ戻す方を選べる（待ちのままにならない）。
func TestRelease_HumanWaitCanBeResolvedByReopening(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	f.setOpState(t, OperationStatePending)
	f.setVerifiedHead(t, "v")
	f.fake.pr.HeadSHA = "ht"
	cmp := compareTruncated()
	res, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, ReopenForReleaseInput{
		OperationID: formatOperationID(f.opID), Reason: ReopenReasonUndeterminable, Sources: ReleaseHeadSources{Checks: f.fake, Compare: cmp},
	})
	if err != nil || !res.Reopened {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if snap := snapshotReopen(t, f.s, f.cid); snap.chStatus != string(StatusVerifying) || len(snap.ops) != 2 {
		t.Errorf("snapshot = %+v", snap)
	}
}

// --- PR 側に head が無いときの人間待ち（#517 の完了の検証の差し戻し）---
//
// head が無い release は、人の承認を断る（承認の記録に残す head も、マージの `--match-head-commit` に渡す head も
// 無いため）。人の出口は `approve <OP-ID> --reopen`（ReopenChallengeForRelease の理由 undeterminable）だけである。

// (a) head 無しで人間待ちになった release は、人が「確かめないまま承認する」を選んでも承認できない。
func TestExecuteOperationApproval_HeadlessHumanWaitIsNotApprovable(t *testing.T) {
	t.Parallel()
	headless := &prFake{pr: UpstreamPullRequestChecks{State: "open", Base: "main", HeadSHA: "", Checks: passingChecks()}}
	a := assessReleaseHeadOfPR(context.Background(), newFakeCompare(), "o/r", headless.pr, "v")
	if !a.Change.WaitsForHuman() {
		t.Fatalf("change = %+v, want a wait for a human", a.Change)
	}
	if a.AcceptableByHuman() {
		t.Fatal("AcceptableByHuman = true for a PR without a head, want false")
	}

	f := newHeadFixture(t)
	f.verified(t, hdrefA, "v")
	before := f.dump(t)
	if err := f.approveOpWith(t, f.relA, "", sourcesOf(headless, newFakeCompare()), true); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if f.dump(t) != before {
		t.Error("the store changed on a refused approval")
	}
}

// (b) head 無しで人間待ちになった release は、理由 undeterminable で課題を着手中へ戻せる（人の出口）。
func TestRelease_HeadlessHumanWaitCanBeResolvedByReopening(t *testing.T) {
	t.Parallel()
	for _, state := range []OperationState{OperationStatePending, OperationStateApproved} {
		t.Run(string(state), func(t *testing.T) {
			f := newRelFixture(t)
			f.setOpState(t, state)
			f.setVerifiedHead(t, "v")
			f.fake.pr.HeadSHA = ""
			src := ReleaseHeadSources{Checks: f.fake, Compare: newFakeCompare()}
			as, err := f.s.AssessReleaseReopen(context.Background(), formatOperationID(f.opID), src)
			if err != nil || as.Verdict != ReleaseReopenNeedsHuman || !strings.Contains(as.Detail, "no head") {
				t.Fatalf("assessment = %+v, err = %v, want needs_human (no head)", as, err)
			}
			res, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, ReopenForReleaseInput{
				OperationID: formatOperationID(f.opID), Reason: ReopenReasonUndeterminable, Sources: src,
			})
			if err != nil || !res.Reopened {
				t.Fatalf("res = %+v, err = %v", res, err)
			}
			if snap := snapshotReopen(t, f.s, f.cid); snap.chStatus != string(StatusVerifying) || len(snap.ops) != 2 {
				t.Errorf("snapshot = %+v", snap)
			}
		})
	}
}
