package core

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは #440 の「自分の本番反映（委譲の PR のマージ）で取り込み元が閉じた課題を、周の対象から
// 外さない」と「取り込み元の食い違いで外した課題を not_started に種類ごとの理由で出す」を固定する。

// setBindingStateForTest は課題の取り込み元の対応の上流の状態・ポリシーの状態を直接書き換える。
func setBindingStateForTest(t *testing.T, s *Store, id string, upstream upstreamState, policy policyState) {
	t.Helper()
	cid := mustChallengeInternalID(t, id)
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE source_binding SET upstream_state = ?, policy_state = ? WHERE challenge_id = ?`,
			string(upstream), string(policy), cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// insertReleaseForTest は課題に release の不可逆操作を 1 件置く。state が executed なら、実行の書き込み
// （executeReleaseInTx）と同じく executed_at と merge_commit（空なら NULL）を記録する。
func insertReleaseForTest(t *testing.T, s *Store, id, ref string, state OperationState, mergeCommit string) int64 {
	t.Helper()
	cid := mustChallengeInternalID(t, id)
	op := coretest.InsertReleaseOperation(t, s.workspace, int(cid), ref, string(state))
	if state == OperationStateExecuted {
		var mc any
		if mergeCommit != "" {
			mc = mergeCommit
		}
		if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE operation SET executed_at = ?, merge_commit = ? WHERE id = ?`,
				formatTimestamp(time.Now()), mc, op)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	return op
}

// insertUpstreamStateChangeForTest は取り込みが記録する upstream_state_change の作業ログを、日時 at で 1 件置く。
func insertUpstreamStateChangeForTest(t *testing.T, s *Store, id, at string, to upstreamState) {
	t.Helper()
	cid := mustChallengeInternalID(t, id)
	if err := s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO activity (at, actor, channel, verification, entity, entity_id, action, before, after)
			VALUES (?, 'alice', 'cli', 'none', 'challenge', ?, 'upstream_state_change', '{"upstream_state":"closed"}', ?)`,
			at, cid, fmt.Sprintf(`{"upstream_state":%q,"version":99}`, string(to)))
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// newClosedVerifyingFixture は C-1 を委譲して検証中にし、取り込み元 o/r#42 の対応を付ける（上流は open）。
func newClosedVerifyingFixture(t *testing.T) *j5Fixture {
	t.Helper()
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	f.attachSource(t, "C-1", "o/r#42", "o/r", 42, "UP-BODY")
	return f
}

// (a) 本番反映が実行済みで PR のマージが記録され、取り込み元が closed → 完了の検証の対象に入り、
// not_started に出ない。後続の完了の承認・学びの対象選びでも外れない。
func TestAutoExclusion_ClosedByOwnRelease_IsVerifiedThenApprovedThenLearned(t *testing.T) {
	f := newClosedVerifyingFixture(t)
	insertReleaseForTest(t, f.s, "C-1", relRef, OperationStateExecuted, "mc1")
	setBindingStateForTest(t, f.s, "C-1", upstreamStateClosed, policyStateInPolicy)

	res, err := f.verify(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NotStarted) != 0 || len(res.Items) != 1 || res.Items[0].Outcome != "met" {
		t.Fatalf("verify result = %+v, want J5 met on C-1 and nothing not started", res)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingCompletionApproval {
		t.Fatalf("status = %s, want awaiting completion approval", got)
	}

	// 完了の承認は ID を指定した個別の操作で、取り込み元が閉じていても成立する。
	prev, err := f.s.PrepareApproval(context.Background(), "C-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.ExecuteApproval(context.Background(), ApprovalRequest{ChallengeID: "C-1", ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved},
		verifiedAttestationForTest(t, "C-1")); err != nil {
		t.Fatalf("ExecuteApproval: %v", err)
	}
	if got := f.detail(t, "C-1").Status; got != StatusDone {
		t.Fatalf("status = %s, want done", got)
	}

	// 学びの段の対象（完了の承認の発生を持つ完了の課題）からも外れない。
	var learn []int64
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		learn, err = selectJ6AutoTargets(context.Background(), tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(learn) != "[1]" {
		t.Errorf("learn targets = %v, want [1]", learn)
	}
}

// (a) の続き: 自身の本番反映で閉じた課題が完了の検証で not_met になり着手中へ戻っても、委譲（手直し）の
// 対象から外れない（完了までの経路の段すべてに当てる）。
func TestAutoExclusion_ClosedByOwnRelease_ReworkDelegationIsNotExcluded(t *testing.T) {
	f := newClosedVerifyingFixture(t)
	insertReleaseForTest(t, f.s, "C-1", relRef, OperationStateExecuted, "mc1")
	setBindingStateForTest(t, f.s, "C-1", upstreamStateClosed, policyStateInPolicy)
	f.j5.result = j5Out("not_met", "FIX-IT", nil)
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.detail(t, "C-1").Status; got != StatusInProgress {
		t.Fatalf("status = %s, want in progress", got)
	}
	var ids []int64
	var skipped []NotStarted
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		ids, skipped, err = selectDelegationTargets(context.Background(), tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(ids) != "[1]" || len(skipped) != 0 {
		t.Errorf("delegation targets = %v, skipped = %+v, want [1] and none skipped", ids, skipped)
	}
}

// (b) 本番反映の記録が無い・足りないまま closed、missing、範囲外 → 完了の検証の対象から外れ、
// not_started に種類ごとの理由（Detail は取り込み元の URL）で出る。J5 は起動しない。
func TestAutoExclusion_VerifyExcludesDiscrepanciesWithAReasonPerKind(t *testing.T) {
	cases := []struct {
		name     string
		upstream upstreamState
		policy   policyState
		release  func(t *testing.T, s *Store)
		want     NotStartedReason
	}{
		{name: "closed without any release (closed outside, or the PR merged on GitHub without an executed release)", upstream: upstreamStateClosed, policy: policyStateInPolicy, want: NotStartedUpstreamClosed},
		{name: "closed with the release approved but not executed", upstream: upstreamStateClosed, policy: policyStateInPolicy,
			release: func(t *testing.T, s *Store) { insertReleaseForTest(t, s, "C-1", relRef, OperationStateApproved, "") }, want: NotStartedUpstreamClosed},
		{name: "closed with an executed release that has no merge commit", upstream: upstreamStateClosed, policy: policyStateInPolicy,
			release: func(t *testing.T, s *Store) { insertReleaseForTest(t, s, "C-1", relRef, OperationStateExecuted, "") }, want: NotStartedUpstreamClosed},
		{name: "closed with an executed release whose ref is not a PR", upstream: upstreamStateClosed, policy: policyStateInPolicy,
			release: func(t *testing.T, s *Store) {
				insertReleaseForTest(t, s, "C-1", "https://github.com/o/r/issues/1", OperationStateExecuted, "mc1")
			}, want: NotStartedUpstreamClosed},
		{name: "closed with an executed release whose merge commit is blank", upstream: upstreamStateClosed, policy: policyStateInPolicy,
			release: func(t *testing.T, s *Store) { insertReleaseForTest(t, s, "C-1", relRef, OperationStateExecuted, "  ") }, want: NotStartedUpstreamClosed},
		{name: "closed again after a reopen observed after the executed release", upstream: upstreamStateClosed, policy: policyStateInPolicy,
			release: func(t *testing.T, s *Store) {
				insertReleaseForTest(t, s, "C-1", relRef, OperationStateExecuted, "mc1")
				insertUpstreamStateChangeForTest(t, s, "C-1", "2999-01-01T00:00:00.000Z", upstreamStateOpen)
			}, want: NotStartedUpstreamClosed},
		{name: "missing even with an executed release", upstream: upstreamStateMissing, policy: policyStateInPolicy,
			release: func(t *testing.T, s *Store) { insertReleaseForTest(t, s, "C-1", relRef, OperationStateExecuted, "mc1") }, want: NotStartedUpstreamMissing},
		{name: "out of policy", upstream: upstreamStateOpen, policy: policyStateOutOfPolicy, want: NotStartedOutOfPolicy},
		{name: "closed by its own release but out of policy", upstream: upstreamStateClosed, policy: policyStateOutOfPolicy,
			release: func(t *testing.T, s *Store) { insertReleaseForTest(t, s, "C-1", relRef, OperationStateExecuted, "mc1") }, want: NotStartedOutOfPolicy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newClosedVerifyingFixture(t)
			if tc.release != nil {
				tc.release(t, f.s)
			}
			setBindingStateForTest(t, f.s, "C-1", tc.upstream, tc.policy)
			res, err := f.verify(t, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Items) != 0 || len(f.j5Runs(t)) != 0 {
				t.Fatalf("verify items = %+v, J5 runs = %d, want J5 not started", res.Items, len(f.j5Runs(t)))
			}
			want := NotStarted{ChallengeID: "C-1", Reason: tc.want, Detail: f.detail(t, "C-1").SourceBinding.URL}
			if len(res.NotStarted) != 1 || res.NotStarted[0] != want {
				t.Fatalf("not_started = %+v, want [%+v]", res.NotStarted, want)
			}
			if got := f.detail(t, "C-1").Status; got != StatusVerifying {
				t.Errorf("status = %s, want verifying (unchanged)", got)
			}
		})
	}
}

// 分類・計画・委譲の段でも、食い違いで外した課題を not_started に理由つきで出す（対象には入れない）。
func TestAutoExclusion_OtherStagesReportTheExcludedChallenges(t *testing.T) {
	f := newDelegateFixture(t)
	ctx := context.Background()
	unclassified, err := f.s.CreateChallenge(ctx, ChannelCLI, CreateInput{Title: "u", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	classified, err := f.s.CreateChallenge(ctx, ChannelCLI, CreateInput{Title: "c", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ClassifyChallenge(ctx, ChannelCLI, classified.ID, ClassifyInput{Priority: "P1"}); err != nil {
		t.Fatal(err)
	}
	inProgress := f.newInProgress(t, "i", "P1", planSpec(nil))
	bindSourceForTest(t, f.s, unclassified.ID, "closed", "in_policy")
	bindSourceForTest(t, f.s, classified.ID, "missing", "in_policy")
	bindSourceForTest(t, f.s, inProgress, "open", "out_of_policy")

	type sel func(context.Context, *sql.Tx) ([]int64, []NotStarted, error)
	for _, tc := range []struct {
		name string
		sel  sel
		id   string
		want NotStartedReason
	}{
		{"classify", selectJ1AutoTargets, unclassified.ID, NotStartedUpstreamClosed},
		{"plan", selectJ2AutoTargets, classified.ID, NotStartedUpstreamMissing},
		{"run", selectDelegationTargets, inProgress, NotStartedOutOfPolicy},
	} {
		var ids []int64
		var ex []NotStarted
		if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
			var err error
			ids, ex, err = tc.sel(context.Background(), tx)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		cid := mustChallengeInternalID(t, tc.id)
		want := NotStarted{ChallengeID: tc.id, Reason: tc.want, Detail: "https://example.com/issues/" + fmt.Sprint(cid)}
		if len(ids) != 0 || len(ex) != 1 || ex[0] != want {
			t.Errorf("%s: targets = %v, excluded = %+v, want none and [%+v]", tc.name, ids, ex, want)
		}
	}

	// 分類の段（classify --auto）の結果の not_started にも出る（J1 は起動しない）。
	res, err := f.s.ClassifyAutoJ1(context.Background(), J1AutoInput{AgentDecl: f.agent, Invoker: f.j3, CycleID: f.cycle(t, 300)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 0 || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedUpstreamClosed || res.NotStarted[0].ChallengeID != unclassified.ID {
		t.Errorf("classify result = %+v, want only upstream_closed for %s", res, unclassified.ID)
	}
}

// (c) Y-10 の再現: 完了の検証の問い（保留）への回答と、委譲の PR の本番反映の承認が同じ発言で届き、
// 次の 1 周の release の段が PR をマージして取り込み元が閉じる。同じ周の取り込みの段が閉じたことを
// 記録し、続く検証の段が J5 を起動して完了の承認待ちまで進む（not_started は空）。
func TestRunCycle_Y10_ReleaseClosesTheSourceAndVerifyStillRunsInTheSameCycle(t *testing.T) {
	f := newClosedVerifyingFixture(t)
	// 1 回目の J5 が uncertain で問いを上げ、課題は人間対応待ちになる。
	f.j5.result = j5Out("uncertain", nil, "QUESTION")
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting human", got)
	}
	// 同じ発言の回答（検証中へ戻る）と、委譲の PR の release の承認。
	prev, err := f.s.PrepareAnswer(context.Background(), "C-1", "ANSWER")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: "C-1", ExpectedVersion: prev.Version, Answer: "ANSWER"},
		verifiedAttestationForTest(t, "C-1")); err != nil {
		t.Fatal(err)
	}
	opID := coretest.InsertApprovedRelease(t, f.s.workspace, 1, relRef, relHead, execTS(0))

	// release の段のマージで PR は merged になる（J5 の前の PR のチェックの取得も同じ偽物が答える）。
	rel := &fakeRelease{pr: openPR(), afterPR: &UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: relHead, MergeCommitSHA: "mc1"}}
	// 取り込みの段: o/r#42 は open の一覧に現れず、個別の取得で closed（`Closes #42` で閉じた）。
	up := &fakeUpstream{
		issues:   map[string][]UpstreamIssue{"o/r": {}},
		getIssue: map[string]UpstreamIssue{"o/r#42": {ExternalKey: "o/r#42", State: "closed"}},
	}
	var calls []JudgmentPoint
	in := f.cycleInput(cycleS2Invoker("met", &calls))
	in.Checks, in.Merger = rel, rel
	in.Ingest = &CycleIngestInput{Sources: []SourceEntry{excludeOthersSource("s", []string{"o/r"}, []string{"masanami"})}, Upstream: up, Channel: ChannelCLI}
	res, err := f.s.RunCycle(context.Background(), in)
	if err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	relPhase := phaseOfResult(t, res, CyclePhaseRelease)
	if len(relPhase.ReleaseItems) != 1 || relPhase.ReleaseItems[0].OperationID != formatOperationID(opID) || relPhase.ReleaseItems[0].Result != ExecutionResultMerged {
		t.Fatalf("release phase = %+v, want OP merged", relPhase)
	}
	if sb := f.detail(t, "C-1").SourceBinding; sb == nil || sb.UpstreamState != "closed" {
		t.Fatalf("source binding = %+v, want upstream closed by the ingest phase of the same cycle", sb)
	}
	verify := phaseOfResult(t, res, CyclePhaseVerify)
	if len(verify.NotStarted) != 0 || len(verify.Items) != 1 || verify.Items[0].ChallengeID != "C-1" || verify.Items[0].Outcome != "met" {
		t.Fatalf("verify phase = %+v, want J5 met on C-1 in the same cycle", verify)
	}
	if got := f.detail(t, "C-1").Status; got != StatusAwaitingCompletionApproval {
		t.Errorf("status = %s, want awaiting completion approval", got)
	}
}

// 述語の境界: 本番反映の前に記録された reopen は妨げない。release が複数あるときは、条件を満たすものが
// 1 つあれば足りる（条件を満たさない release が先にあっても）。
func TestClosedByOwnRelease_ReopenBeforeTheReleaseAndSeveralReleases(t *testing.T) {
	check := func(t *testing.T, f *j5Fixture, want bool) {
		t.Helper()
		var got bool
		if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
			var err error
			got, err = closedByOwnRelease(context.Background(), tx, 1)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("closedByOwnRelease = %v, want %v", got, want)
		}
	}
	t.Run("reopen recorded before the release", func(t *testing.T) {
		f := newClosedVerifyingFixture(t)
		insertUpstreamStateChangeForTest(t, f.s, "C-1", "2000-01-01T00:00:00.000Z", upstreamStateOpen)
		insertReleaseForTest(t, f.s, "C-1", relRef, OperationStateExecuted, "mc1")
		check(t, f, true)
	})
	t.Run("a close (not a reopen) recorded after the release", func(t *testing.T) {
		f := newClosedVerifyingFixture(t)
		insertReleaseForTest(t, f.s, "C-1", relRef, OperationStateExecuted, "mc1")
		insertUpstreamStateChangeForTest(t, f.s, "C-1", "2999-01-01T00:00:00.000Z", upstreamStateClosed)
		check(t, f, true)
	})
	t.Run("an unqualified release first, then a qualified one", func(t *testing.T) {
		f := newClosedVerifyingFixture(t)
		insertReleaseForTest(t, f.s, "C-1", relRef, OperationStateExecuted, "")
		insertReleaseForTest(t, f.s, "C-1", "https://github.com/o/r/issues/9", OperationStateExecuted, "mc0")
		insertReleaseForTest(t, f.s, "C-1", "https://github.com/o/r/pull/2", OperationStateExecuted, "mc2")
		check(t, f, true)
	})
	t.Run("only unqualified releases", func(t *testing.T) {
		f := newClosedVerifyingFixture(t)
		insertReleaseForTest(t, f.s, "C-1", relRef, OperationStateExecuted, "")
		insertReleaseForTest(t, f.s, "C-1", relRef, OperationStatePending, "")
		insertReleaseForTest(t, f.s, "C-1", relRef, OperationStateRejected, "")
		check(t, f, false)
	})
}
