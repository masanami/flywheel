package core

import (
	"context"
	"testing"
)

// 区切り S4b-2: artifact_missing の reverify（成果物の照合のやり直し）と、手 retry。
// 実ストアと偽の委譲先・偽の判断（決めた出力を返す）で検証する。

// superviseHand は、スーパーバイザーの段を 1 回回し、判断点が hand を返したことにする。
func superviseHand(t *testing.T, s *Store, decl *AgentDeclaration, cycleID, hand string) CyclePhaseResult {
	t.Helper()
	inv := &fakeJudgmentInvoker{result: svResult(map[string]any{"hand": hand, "findings": "FINDINGS", "note": "NOTE"})}
	res, err := s.superviseStage(context.Background(), CycleRunInput{AgentDecl: decl, Invoker: inv}, cycleID, nil)
	if err != nil {
		t.Fatalf("superviseStage: %v", err)
	}
	return res
}

// reconcileMark は課題 C-1 の「照合をやり直す」印と、照合した実行（NULL は ""）。
func (f *delegateFixture) reconcileMark(t *testing.T) (mark, runID string) {
	t.Helper()
	return f.queryString(t, `SELECT reconcile_supervisor_action_id FROM challenge WHERE id = 1`),
		f.queryString(t, `SELECT reconcile_fetch_run_id FROM challenge WHERE id = 1`)
}

// artifactMissingPending は、成果物が見つからず artifact_missing のスーパーバイザー待ちの保留ができた状態を作る。
func artifactMissingPending(t *testing.T) *delegateFixture {
	t.Helper()
	f := newReconFixture(t, "impl")
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	wantLastHoldStop(t, f.s, "artifact_missing/pending")
	return f
}

// 保留を reverify で閉じると、課題は着手中へ戻り、印にその対処の記録の ID が入り、照合する実行は保留の原因の実行になる。
func TestSupervisorReverify_ArtifactMissingMarksTheChallengeForReconciliation(t *testing.T) {
	t.Parallel()
	f := artifactMissingPending(t)
	holdRun := f.queryString(t, `SELECT run_id FROM hold WHERE challenge_id = 1`)
	res := superviseHand(t, f.s, f.agent, f.cycle(t, 300), "reverify")
	if res.SuperviseHands[HandReverify] != 1 {
		t.Fatalf("hands = %v, want reverify:1", res.SuperviseHands)
	}
	if got := f.status(t); got != StatusInProgress {
		t.Fatalf("status = %s, want in_progress", got)
	}
	actionID := f.queryString(t, `SELECT id FROM supervisor_action WHERE hand = 'reverify' AND result = 'executed'`)
	if mark, run := f.reconcileMark(t); mark != actionID || run != holdRun {
		t.Errorf("mark/run = %s/%s, want %s/%s", mark, run, actionID, holdRun)
	}
	if got := f.queryString(t, `SELECT supervisor_action_id FROM hold WHERE challenge_id = 1`); got != actionID {
		t.Errorf("hold.supervisor_action_id = %s, want %s", got, actionID)
	}
}

// 次の回りは、新しい委譲を起動せず保留の原因の実行の成果物だけを照合し、見つかれば検証中へ進み、印は NULL に戻る。
func TestSupervisorReverify_ArtifactMissingThenFoundGoesToVerifyingWithoutANewDelegation(t *testing.T) {
	t.Parallel()
	f := artifactMissingPending(t)
	superviseHand(t, f.s, f.agent, f.cycle(t, 300), "reverify")
	before := len(f.branches.callLog())
	f.branches.prs["feat/x"] = []UpstreamPullRequest{mkPR(2, "open", "develop")}
	res, err := f.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.status(t); got != StatusVerifying {
		t.Fatalf("status = %s, want verifying", got)
	}
	if n := len(f.delegateRuns(t)); n != 1 {
		t.Errorf("delegate runs = %d, want 1 (no new delegation)", n)
	}
	if len(f.branches.callLog()) <= before {
		t.Error("the artifacts of the cause run were not looked up")
	}
	if len(res.Items) != 1 || res.Items[0].RunID != "R-2" {
		t.Errorf("items = %+v, want the reconciliation of R-2", res.Items)
	}
	if mark, _ := f.reconcileMark(t); mark != "" {
		t.Errorf("mark = %q, want NULL", mark)
	}
	if prs := f.prArtifacts(t); len(prs) != 1 {
		t.Errorf("pr artifacts = %+v, want the found PR", prs)
	}
}

// 照合でまた見つからなければ、artifact_missing の保留が新しく 1 件できる（印は NULL に戻る）。
func TestSupervisorReverify_ArtifactMissingThenStillNotFoundHoldsAgain(t *testing.T) {
	t.Parallel()
	f := artifactMissingPending(t)
	superviseHand(t, f.s, f.agent, f.cycle(t, 300), "reverify")
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t); got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", got)
	}
	if got := f.queryString(t, `SELECT COUNT(*) FROM hold WHERE stop_kind = 'artifact_missing'`); got != "2" {
		t.Errorf("artifact_missing holds = %s, want 2", got)
	}
	wantLastHoldStop(t, f.s, "artifact_missing/pending")
	if n := len(f.delegateRuns(t)); n != 1 {
		t.Errorf("delegate runs = %d, want 1", n)
	}
	if mark, _ := f.reconcileMark(t); mark != "" {
		t.Errorf("mark = %q, want NULL", mark)
	}
}

// 取得の失敗なら、いまの取得の失敗の扱い（連続の数え）へ入り、課題は着手中のまま、印は NULL に戻る。
func TestSupervisorReverify_ArtifactMissingThenFetchFailureCountsAsAFetchFailure(t *testing.T) {
	t.Parallel()
	f := artifactMissingPending(t)
	superviseHand(t, f.s, f.agent, f.cycle(t, 300), "reverify")
	f.branches.err = errFakeGH
	res, err := f.run(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertUnchangedAfterFetchFailure(t, f, res, "1")
	if mark, _ := f.reconcileMark(t); mark != "" {
		t.Errorf("mark = %q, want NULL", mark)
	}
	if n := len(f.delegateRuns(t)); n != 1 {
		t.Errorf("delegate runs = %d, want 1", n)
	}
	// 次の回りは、いまの取得の失敗のやり直し（印なし・数え 1）で、同じ実行の照合を続ける。
	f.branches.err = nil
	f.branches.prs["feat/x"] = []UpstreamPullRequest{mkPR(2, "open", "develop")}
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t); got != StatusVerifying {
		t.Errorf("status = %s, want verifying", got)
	}
}

// 「見つからない」で取得の失敗の数え・ブランチが消えていても、最初の照合と同じに、報告のブランチで調べ直す。
func TestSupervisorReverify_ArtifactMissingLooksUpTheReportedBranchAgain(t *testing.T) {
	t.Parallel()
	f := artifactMissingPending(t)
	if n, b := f.fetchFailures(t); n != "0" || b != "" {
		t.Fatalf("precondition: failures/branch = %s/%q, want 0/NULL", n, b)
	}
	superviseHand(t, f.s, f.agent, f.cycle(t, 300), "reverify")
	f.branches.mu.Lock()
	f.branches.calls = nil
	f.branches.mu.Unlock()
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	log := f.branches.callLog()
	hasBranch, hasPR := false, false
	for _, c := range log {
		hasBranch = hasBranch || c == "exists o/r feat/x"
		hasPR = hasPR || c == "pr o/r 1"
	}
	if !hasBranch || !hasPR {
		t.Errorf("lookups = %v, want the reported branch and pr_urls", log)
	}
}

// 印が無く取得の失敗の数えも 0 の課題は、照合のやり直しの対象にならない（retry は印を付けず、新しい委譲の実行を 1 件起動する）。
func TestSupervisorRetry_ArtifactMissingLaunchesANewDelegationAndLeavesNoMark(t *testing.T) {
	t.Parallel()
	f := artifactMissingPending(t)
	res := superviseHand(t, f.s, f.agent, f.cycle(t, 300), "retry")
	if res.SuperviseHands[HandRetry] != 1 {
		t.Fatalf("hands = %v, want retry:1", res.SuperviseHands)
	}
	if got := f.status(t); got != StatusInProgress {
		t.Fatalf("status = %s, want in_progress", got)
	}
	if mark, _ := f.reconcileMark(t); mark != "" {
		t.Errorf("mark = %q, want NULL", mark)
	}
	if _, err := f.run(t, nil); err != nil {
		t.Fatal(err)
	}
	if n := len(f.delegateRuns(t)); n != 2 {
		t.Errorf("delegate runs = %d, want 2 (one new delegation)", n)
	}
}

// retry で閉じた保留は、回答した者 supervisor・経路 invoker・本人確認 none の記録になる。
func TestSupervisorRetry_ClosesTheHoldAsTheSupervisor(t *testing.T) {
	t.Parallel()
	f := artifactMissingPending(t)
	superviseHand(t, f.s, f.agent, f.cycle(t, 300), "retry")
	if got := f.queryString(t, `SELECT answered_by || '/' || answered_channel || '/' || answered_verification || '/' || supervisor_state FROM hold WHERE challenge_id = 1`); got != "supervisor/invoker/none/handled" {
		t.Errorf("hold = %s, want supervisor/invoker/none/handled", got)
	}
}

// 委譲の連続の失敗の上限の保留を retry で閉じると、数えは上限 − 1 になり、次の 1 回の失敗で同じ上限の保留になる。
func TestSupervisorRetry_DelegateStoppedCountsFromLimitMinusOne(t *testing.T) {
	t.Parallel()
	f := newResumeFixture(t)
	f.agent.FailureLimit = 3 // 上限 − 1 が 2 になり、「1 を足す」実装と区別できる
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.queue = []JudgmentLaunchOutput{failOut(RunResultErrored), failOut(RunResultErrored), failOut(RunResultErrored)}
	for i := 0; i < 3; i++ {
		f.topUpImplBudget(t, id)
		f.runDelegation(t, id)
	}
	f.runDelegation(t, id) // 上限: 起動せず保留
	wantLastHoldStop(t, f.s, "delegate_stopped/pending")

	superviseHand(t, f.s, f.agent, f.cycle(t, 300), "retry")
	if got := f.detail(t, id).Status; got != StatusInProgress {
		t.Fatalf("status = %s, want in_progress", got)
	}
	n := len(f.deleg.launched())
	f.deleg.queue = []JudgmentLaunchOutput{failOut(RunResultErrored)}
	f.topUpImplBudget(t, id)
	f.runDelegation(t, id)
	f.topUpImplBudget(t, id)
	if len(f.deleg.launched()) != n+1 {
		t.Fatalf("launched = %d, want %d (the retry launches the same task again)", len(f.deleg.launched()), n+1)
	}
	// 1 回の失敗だけで、同じ上限の保留になる（起動しない）。
	res := f.runDelegation(t, id)
	if len(f.deleg.launched()) != n+1 {
		t.Error("a delegation was launched after one failure; the count must start at limit - 1")
	}
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedFailureLimit {
		t.Errorf("notStarted = %+v, want failure_limit", res.NotStarted)
	}
	wantLastHoldStop(t, f.s, "delegate_stopped/pending")
}

// オーナーの回答で閉じた上限の保留は、いまと同じに 0 から数え直す（retry の「上限 − 1」を持ち込まない）。
func TestOwnerAnswerAfterDelegateStoppedStillCountsFromZero(t *testing.T) {
	t.Parallel()
	f := newResumeFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.queue = []JudgmentLaunchOutput{failOut(RunResultErrored), failOut(RunResultErrored)}
	f.runDelegation(t, id)
	f.runDelegation(t, id)
	f.runDelegation(t, id)
	f.answerLatestHold(t, id, "go on")
	n := len(f.deleg.launched())
	f.deleg.queue = []JudgmentLaunchOutput{failOut(RunResultErrored)}
	f.topUpImplBudget(t, id)
	f.runDelegation(t, id)
	f.topUpImplBudget(t, id)
	f.runDelegation(t, id)
	if len(f.deleg.launched()) != n+2 {
		t.Errorf("launched = %d, want %d (one failure after an owner's answer does not reach the limit)", len(f.deleg.launched()), n+2)
	}
}

// 判断点の連続失敗の上限の保留を retry で閉じると、その判断点の数えは上限 − 1 になる。
func TestSupervisorRetry_JudgmentFailedCountsFromLimitMinusOne(t *testing.T) {
	t.Parallel()
	s := newStoreForTest(t)
	decl := newAgentDeclForJ1Test(t, s, "pos")
	decl.FailureLimit = 3 // 上限 − 1 が 2 になり、「1 を足す」実装と区別できる
	ch, err := s.CreateChallenge(context.Background(), ChannelCLI, CreateInput{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	inv := &fakeJudgmentInvoker{result: JudgmentLaunchOutput{Result: RunResultErrored}, onInvoke: func(JudgmentLaunchInput) { calls++ }}
	for i := 0; i < decl.FailureLimit; i++ {
		autoJ1Once(t, s, decl, inv)
	}
	autoJ1Once(t, s, decl, inv) // 上限: 起動せず保留
	wantLastHoldStop(t, s, "judgment_failed/pending")
	calls = 0

	superviseHand(t, s, decl, beginJ1Cycle(t, s, 300), "retry")
	if d, _ := s.GetChallenge(context.Background(), ch.ID); d.Status != StatusUnclassified {
		t.Fatalf("status = %s, want unclassified (the status before the hold)", d.Status)
	}
	autoJ1Once(t, s, decl, inv) // 起動する（1 回の失敗）
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (the judgment is called again)", calls)
	}
	res := autoJ1Once(t, s, decl, inv) // 1 回の失敗で、同じ上限の保留
	if calls != 1 || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedFailureLimit {
		t.Errorf("calls=%d NotStarted=%+v, want held without invoking after one failure", calls, res.NotStarted)
	}
}
