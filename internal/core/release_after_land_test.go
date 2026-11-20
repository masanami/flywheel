package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// 反映の段が、「入れてよい」を返した後の読み方（landed・land_refused・delegate_ended・返し直し）と、契約の実行の後片付け・
// 完了から戻す前の読みを、実ストア・偽の ContractClient・偽の PR の取得で固定する。

const landedMergeCommit = "upstream-merge-sha"

func (f *landFixture) endedOut(state ContractState) ContractLaunchOutput {
	return contractOut(state, nil, func(st *ContractStatus) {
		st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: reviewPRURL}, {Kind: "commit", Ref: "delegate-says-another-commit"}}
	})
}

func (f *landFixture) merged(head string) {
	f.fake.pr = UpstreamPullRequestChecks{URL: reviewPRURL, State: "merged", Base: "main", HeadSHA: head, MergeCommitSHA: landedMergeCommit}
}

func (f *landFixture) lastDetail(t *testing.T) string {
	t.Helper()
	var detail string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COALESCE(detail, '') FROM operation_execution ORDER BY id DESC LIMIT 1`).Scan(&detail)
	}); err != nil {
		t.Fatal(err)
	}
	return detail
}

func (f *landFixture) mergeCommit(t *testing.T) string {
	t.Helper()
	return f.queryString(t, `SELECT COALESCE(merge_commit, '') FROM operation WHERE id = 1`).String
}

func (f *landFixture) runEnded(t *testing.T) bool {
	t.Helper()
	_, _, _, ended := f.contractRow(t)
	return ended
}

func (f *landFixture) reapprove(t *testing.T, head string) {
	t.Helper()
	prev, err := f.s.PrepareOperationApproval(context.Background(), "OP-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: "OP-1", ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion,
		Decision: ApprovalDecisionApproved, HeadSHA: head,
	}, verifiedAttestationForTest(t, "OP-1")); err != nil {
		t.Fatalf("re-approve: %v", err)
	}
}

// refusedReview は、「入れてよい」を返した後に戻ってくる、同じ中身のレビュー待ち（委譲先が理由を書いている）。
func (f *landFixture) refusedReview() ContractLaunchOutput {
	return contractOut(ContractStateWaiting, nil, func(st *ContractStatus) {
		st.Summary = "ci-is-red-on-main"
		st.RequestedAction = &ContractRequestedAction{Kind: "review", Decider: "any", Text: "main の CI が落ちていて入れられない", Options: []string{"respond", "land"}}
		st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: reviewPRURL}, {Kind: "commit", Ref: f.head}}
	})
}

// sendLand は「入れてよい」を 1 回返して land_sent にする。
func (f *landFixture) sendLand(t *testing.T) {
	t.Helper()
	f.queueReview()
	if item := f.release(t); item.Result != ExecutionResultLandSent {
		t.Fatalf("item = %+v, want land_sent", item)
	}
}

// AC-185・189・190・191・192・193: succeeded で、承認した中身のまま入っていれば landed。反映の操作は executed、課題は done のまま、
// merge_commit は上流の値（委譲先の出力の commit が別の値でも）、契約の実行は終わった記録。
func TestReleaseAfterLand_SucceededAndMergedAsApprovedIsLanded(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.sendLand(t)
	f.merged(f.head)
	f.client.queue = []ContractLaunchOutput{f.endedOut(ContractStateSucceeded)}
	item := f.release(t)
	if item.Result != ExecutionResultLanded || item.Route != ReleaseRouteWaitingRun {
		t.Fatalf("item = %+v, want landed with route waiting_run", item)
	}
	if got := f.opState(t); got != "executed" {
		t.Errorf("operation = %s, want executed", got)
	}
	if got := f.status(t, f.cid); got != StatusDone {
		t.Errorf("challenge = %s, want done", got)
	}
	if got := f.mergeCommit(t); got != landedMergeCommit {
		t.Errorf("merge_commit = %q, want the upstream value %q", got, landedMergeCommit)
	}
	if !f.runEnded(t) {
		t.Error("the contract run is not recorded as ended")
	}
}

// AC-194・288: succeeded でも上流の成果物が入っていなければ delegate_ended。反映の操作は executed にならず pending へ戻る。
func TestReleaseAfterLand_SucceededButNotMergedIsDelegateEndedAndTheOperationGoesBackToPending(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.sendLand(t)
	f.client.queue = []ContractLaunchOutput{f.endedOut(ContractStateSucceeded)}
	item := f.release(t)
	if item.Result != ExecutionResultDelegateEnded {
		t.Fatalf("item = %+v, want delegate_ended", item)
	}
	if got := f.opState(t); got != "pending" {
		t.Errorf("operation = %s, want pending", got)
	}
	if !strings.Contains(f.lastDetail(t), "succeeded") {
		t.Errorf("detail = %q, want the read state", f.lastDetail(t))
	}
}

// AC-195: succeeded で、承認した中身に関係のない変更を足して入っていれば merged_unapproved。
func TestReleaseAfterLand_SucceededAndMergedWithAnUnrelatedChangeIsMergedUnapproved(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.sendLand(t)
	f.merged("unrelated-change")
	f.client.queue = []ContractLaunchOutput{f.endedOut(ContractStateSucceeded)}
	if item := f.release(t); item.Result != ExecutionResultMergedUnapproved {
		t.Fatalf("item = %+v, want merged_unapproved", item)
	}
}

// AC-196・197: succeeded で上流を読めなければ upstream_failed（反映の操作は approved のまま）。次の反映の段で読めて入っていれば executed。
func TestReleaseAfterLand_SucceededWithAnUnreadableUpstreamIsUpstreamFailedAndTheNextStageExecutes(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.sendLand(t)
	f.fake.getErr = errors.New("github is down")
	f.client.queue = []ContractLaunchOutput{f.endedOut(ContractStateSucceeded)}
	if item := f.release(t); item.Result != ExecutionResultUpstreamFailed {
		t.Fatalf("item = %+v, want upstream_failed", item)
	}
	if got := f.opState(t); got != "approved" {
		t.Fatalf("operation = %s, want approved", got)
	}
	f.fake.getErr = nil
	f.merged(f.head)
	if item := f.release(t); item.Result != ExecutionResultAlreadyMerged {
		t.Fatalf("item = %+v, want already_merged (the run was already read as ended)", item)
	}
	if got := f.opState(t); got != "executed" {
		t.Fatalf("operation = %s, want executed", got)
	}
}

// AC-198・199: failed・cancelled で入っていなければ delegate_ended。
func TestReleaseAfterLand_FailedOrCancelledAndNotMergedIsDelegateEnded(t *testing.T) {
	t.Parallel()
	for _, state := range []ContractState{ContractStateFailed, ContractStateCancelled} {
		f := newLandFixture(t)
		f.sendLand(t)
		f.client.queue = []ContractLaunchOutput{f.endedOut(state)}
		if item := f.release(t); item.Result != ExecutionResultDelegateEnded {
			t.Errorf("%s: item = %+v, want delegate_ended", state, item)
		}
	}
}

// AC-200: failed で、承認した中身のまま入っていれば already_merged（反映の操作は executed）。
func TestReleaseAfterLand_FailedButMergedAsApprovedIsAlreadyMergedAndExecuted(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.sendLand(t)
	f.merged(f.head)
	f.client.queue = []ContractLaunchOutput{f.endedOut(ContractStateFailed)}
	if item := f.release(t); item.Result != ExecutionResultAlreadyMerged {
		t.Fatalf("item = %+v, want already_merged", item)
	}
	if got := f.opState(t); got != "executed" {
		t.Fatalf("operation = %s, want executed", got)
	}
}

// AC-202・203・204・205・206・207・309: 「入れてよい」が届いた後に、同じ中身のレビュー待ちが戻れば land_refused。resume は呼ばない。
// 詳細に委譲先の summary と requested_action.text が残り、反映の操作は pending、land_ref は NULL、課題は done のまま。
func TestReleaseAfterLand_TheSameContentAfterADeliveredLandIsLandRefused(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.sendLand(t)
	f.client.queue = []ContractLaunchOutput{f.refusedReview()}
	item := f.release(t)
	if item.Result != ExecutionResultLandRefused {
		t.Fatalf("item = %+v, want land_refused", item)
	}
	if len(f.client.resumes) != 1 {
		t.Errorf("resumes = %v, want only the first land", f.client.resumes)
	}
	detail := f.lastDetail(t)
	if !strings.Contains(detail, "ci-is-red-on-main") {
		t.Errorf("detail = %q, want the delegate's summary", detail)
	}
	if !strings.Contains(detail, "main の CI が落ちていて入れられない") {
		t.Errorf("detail = %q, want the delegate's requested_action.text", detail)
	}
	if got := f.opState(t); got != "pending" {
		t.Errorf("operation = %s, want pending", got)
	}
	if ref, delivered := f.landRow(t); ref != "" || delivered {
		t.Errorf("land_ref=%q delivered=%v, want cleared", ref, delivered)
	}
	if got := f.status(t, f.cid); got != StatusDone {
		t.Errorf("challenge = %s, want done", got)
	}
}

// AC-208: land_refused の後に承認し直すと、次の反映の段で、同じ実行へ {choice} が land の resume が呼ばれる。
func TestReleaseAfterLand_AfterLandRefusedReapprovalReturnsLandAgain(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.sendLand(t)
	f.client.queue = []ContractLaunchOutput{f.refusedReview()}
	f.release(t)
	f.reapprove(t, f.head)
	f.queueReview()
	if item := f.release(t); item.Result != ExecutionResultLandSent {
		t.Fatalf("item = %+v, want land_sent", item)
	}
	if len(f.client.resumes) != 2 || !strings.Contains(strings.Join(f.client.resumes[1], " "), "--choice land") {
		t.Fatalf("resumes = %v, want a second land", f.client.resumes)
	}
}

// AC-209: waiting_delegate と upstream_failed の試行を挟んでも、同じ中身のレビュー待ちは land_refused。
func TestReleaseAfterLand_IntermediateAttemptsDoNotHideLandRefused(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.sendLand(t)
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, nil, nil)}
	if item := f.release(t); item.Result != ExecutionResultWaitingDelegate {
		t.Fatalf("item = %+v, want waiting_delegate", item)
	}
	f.client.queue = []ContractLaunchOutput{{Result: RunResultErrored, ErrorSummary: "down"}}
	if item := f.release(t); item.Result != ExecutionResultUpstreamFailed {
		t.Fatalf("item = %+v, want upstream_failed", item)
	}
	f.client.queue = []ContractLaunchOutput{f.refusedReview()}
	if item := f.release(t); item.Result != ExecutionResultLandRefused {
		t.Fatalf("item = %+v, want land_refused", item)
	}
}

// AC-210: resume が失敗した（land_unconfirmed）後、upstream_failed の試行を挟んでも、同じ中身のレビュー待ちは land_refused にならず、
// 同じ中身の resume がもう一度呼ばれる。
func TestReleaseAfterLand_AfterLandUnconfirmedTheSameContentIsSentAgainEvenAcrossUpstreamFailed(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.queueReview()
	f.client.resumeOut = ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "boom"}
	if item := f.release(t); item.Result != ExecutionResultLandUnconfirmed {
		t.Fatalf("item = %+v, want land_unconfirmed", item)
	}
	f.client.queue = []ContractLaunchOutput{{Result: RunResultErrored, ErrorSummary: "down"}}
	if item := f.release(t); item.Result != ExecutionResultUpstreamFailed {
		t.Fatalf("item = %+v, want upstream_failed", item)
	}
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f.client.queue = []ContractLaunchOutput{f.refusedReview()}
	item := f.release(t)
	if item.Result != ExecutionResultLandSent || len(f.client.resumes) != 2 {
		t.Fatalf("item = %+v resumes %v, want land_sent by a second resume", item, f.client.resumes)
	}
	if got := f.client.resumes[1][len(f.client.resumes[1])-1]; got != f.head {
		t.Fatalf("{approved_ref} = %q, want %q", got, f.head)
	}
}

// AC-211: 直前の試行の結果が land_unconfirmed で、同じ中身のレビュー待ちなら、同じ中身を {approved_ref} にした resume がもう一度呼ばれる。
func TestReleaseAfterLand_RightAfterLandUnconfirmedTheSameContentIsSentAgain(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.queueReview()
	f.client.resumeOut = ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "boom"}
	f.release(t)
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f.client.queue = []ContractLaunchOutput{f.refusedReview()}
	item := f.release(t)
	if item.Result != ExecutionResultLandSent || len(f.client.resumes) != 2 {
		t.Fatalf("item = %+v resumes %v, want land_sent by a second resume", item, f.client.resumes)
	}
	if got := f.client.resumes[1][len(f.client.resumes[1])-1]; got != f.head {
		t.Fatalf("{approved_ref} = %q, want %q", got, f.head)
	}
}

// AC-212: land_ref を書いた後、resume の結果を書く前に止まった試行（interrupted）の後は、同じ中身の resume がもう一度呼ばれる。
func TestReleaseAfterLand_AfterAnInterruptedAttemptTheSameContentIsSentAgain(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	// 止まった試行: land_ref は書いたが、届いた記録は無く、試行は interrupted で閉じられている。
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE contract_run SET land_ref = ?, land_delivered = 0`, f.head); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO operation_execution (operation_id, started_at, ended_at, result, approved_head, pid, host)
			VALUES (1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:01.000Z', 'interrupted', ?, 1, 'h')`, f.head)
		return err
	})
	f.client.queue = []ContractLaunchOutput{f.refusedReview()}
	item := f.release(t)
	if item.Result != ExecutionResultLandSent || len(f.client.resumes) != 1 {
		t.Fatalf("item = %+v resumes %v, want land_sent by a resume", item, f.client.resumes)
	}
}

// movedByMergeIn は PR の head を h にし、承認した中身から h への変わり方が反映先の最新の取り込みだけになるよう、比較を用意する。
func (f *landFixture) movedByMergeIn(cmp *fakeCompare, h string) {
	f.fake.pr.HeadSHA = h
	cmp.set(f.head, h, withFiles(ahead(commit("m1", "main", "m0"), commit(h, "Merge", f.head, "m1")), UpstreamFile{Filename: "a", Patch: "p"}))
	cmp.set("main", h, ahead(commit(f.head, "fix", "p0"), commit(h, "Merge", f.head, "m1")))
	cmp.set("m1", h, ahead(commit(f.head, "fix", "p0")))
}

func (f *landFixture) releaseWith(t *testing.T, cmp *fakeCompare) ReleaseExecItem {
	t.Helper()
	res, err := f.s.ExecuteReleases(context.Background(), []string{"OP-1"}, ReleaseExecOptions{
		Checks: f.fake, Compare: cmp, Connectors: f.conn, Agent: &AgentDeclaration{FailureLimit: 3}, Contract: f.client, Trigger: ReleaseExecTriggerOpExec,
	})
	if err != nil || len(res.Items) != 1 {
		t.Fatalf("ExecuteReleases = %+v err %v", res, err)
	}
	return res.Items[0]
}

func reviewFor(commitRef string) ContractLaunchOutput {
	return contractOut(ContractStateWaiting, nil, func(st *ContractStatus) {
		st.RequestedAction = &ContractRequestedAction{Kind: "review", Decider: "any", Text: "レビュー待ち", Options: []string{"respond", "land"}}
		st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: reviewPRURL}, {Kind: "commit", Ref: commitRef}}
	})
}

// AC-213・214・217: 違う中身のレビュー待ち（求めの文は同じ）で、違いが取り込みだけでチェックが通っていれば、同じ反映の段で新しい中身を
// {approved_ref} にした resume が呼ばれ、land_ref は新しい中身になる。求めの文が同じでも読み飛ばさない。
func TestReleaseAfterLand_AMergeInOnlyChangeAfterADeliveredLandIsSentAgainWithTheNewContent(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.sendLand(t)
	cmp := newFakeCompare()
	f.movedByMergeIn(cmp, "h1")
	f.fake.pr.Checks = []UpstreamCheck{{Status: "completed", Conclusion: "success"}}
	f.client.queue = []ContractLaunchOutput{reviewFor("h1")}
	item := f.releaseWith(t, cmp)
	if item.Result != ExecutionResultLandSent || len(f.client.resumes) != 2 {
		t.Fatalf("item = %+v resumes %v, want land_sent by a second resume", item, f.client.resumes)
	}
	if got := f.client.resumes[1][len(f.client.resumes[1])-1]; got != "h1" {
		t.Errorf("{approved_ref} = %q, want h1", got)
	}
	if ref, _ := f.landRow(t); ref != "h1" {
		t.Errorf("land_ref = %q, want h1", ref)
	}
}

// AC-215: 取り込みだけで、新しい中身のチェックが終わっていなければ waiting_checks（resume は呼ばない）。
func TestReleaseAfterLand_AMergeInOnlyChangeWithUnfinishedChecksIsWaitingChecks(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.sendLand(t)
	cmp := newFakeCompare()
	f.movedByMergeIn(cmp, "h1")
	f.fake.pr.Checks = []UpstreamCheck{{Status: "queued"}}
	f.client.queue = []ContractLaunchOutput{reviewFor("h1")}
	if item := f.releaseWith(t, cmp); item.Result != ExecutionResultWaitingChecks || len(f.client.resumes) != 1 {
		t.Fatalf("item = %+v resumes %v, want waiting_checks without a new resume", item, f.client.resumes)
	}
}

// AC-216: 違いに関係のない変更があれば head_changed。
func TestReleaseAfterLand_AnUnrelatedChangeAfterADeliveredLandIsHeadChanged(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.sendLand(t)
	f.fake.pr.HeadSHA = "unrelated-change"
	f.client.queue = []ContractLaunchOutput{reviewFor("unrelated-change")}
	if item := f.release(t); item.Result != ExecutionResultHeadChanged || len(f.client.resumes) != 1 {
		t.Fatalf("item = %+v resumes %v, want head_changed without a new resume", item, f.client.resumes)
	}
}

// AC-218: 「入れてよい」を land_sent で 4 回続けて返した反映の操作も、5 回目の条件を満たせば resume が呼ばれる（回数の上限が無い）。
func TestReleaseAfterLand_ThereIsNoLimitOnReturningLandAgain(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.fake.pr.Checks = []UpstreamCheck{{Status: "completed", Conclusion: "success"}}
	cmp := newFakeCompare()
	f.queueReview()
	if item := f.releaseWith(t, cmp); item.Result != ExecutionResultLandSent {
		t.Fatalf("1st: item = %+v, want land_sent", item)
	}
	for i := 2; i <= 5; i++ {
		h := fmt.Sprintf("h%d", i)
		f.movedByMergeIn(cmp, h)
		f.client.queue = []ContractLaunchOutput{reviewFor(h)}
		if item := f.releaseWith(t, cmp); item.Result != ExecutionResultLandSent {
			t.Fatalf("attempt %d: item = %+v, want land_sent", i, item)
		}
	}
	if len(f.client.resumes) != 5 {
		t.Fatalf("resumes = %d, want 5", len(f.client.resumes))
	}
}

// waitingRunCancelFixture は、レビュー待ちの実行を残したまま、反映先の側で状態が変わった場面。cancel は取り消し済みを返す。
func (f *landFixture) cancelsWith() {
	f.client.cancelOut = contractOut(ContractStateCancelled, nil, nil)
}

// AC-298: すでに入っている（already_merged）とき、対応する実行が待ちのままなら、その実行へ cancel が呼ばれる（land_ref が無くても）。
func TestReleaseAfterLand_AlreadyMergedCancelsTheOpenRun(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.cancelsWith()
	f.merged(f.head)
	f.queueReview()
	if item := f.release(t); item.Result != ExecutionResultAlreadyMerged {
		t.Fatalf("item = %+v, want already_merged", item)
	}
	if len(f.client.cancels) != 1 || len(f.client.resumes) != 0 {
		t.Fatalf("cancels = %v resumes = %v, want one cancel and no resume", f.client.cancels, f.client.resumes)
	}
	if !f.runEnded(t) {
		t.Error("the contract run is still open")
	}
}

// AC-299: closed のとき、待ちのままの実行へ cancel が呼ばれる。
func TestReleaseAfterLand_ClosedCancelsTheOpenRun(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.cancelsWith()
	f.fake.pr.State = "closed"
	f.queueReview()
	if item := f.release(t); item.Result != ExecutionResultClosed {
		t.Fatalf("item = %+v, want closed", item)
	}
	if len(f.client.cancels) != 1 {
		t.Fatalf("cancels = %v, want one", f.client.cancels)
	}
}

// AC-300: merged_unapproved のとき、待ちのままの実行へ cancel が呼ばれる。
func TestReleaseAfterLand_MergedUnapprovedCancelsTheOpenRun(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.cancelsWith()
	f.merged("unrelated-change")
	f.queueReview()
	if item := f.release(t); item.Result != ExecutionResultMergedUnapproved {
		t.Fatalf("item = %+v, want merged_unapproved", item)
	}
	if len(f.client.cancels) != 1 {
		t.Fatalf("cancels = %v, want one", f.client.cancels)
	}
}

// cancel が失敗しても結果は変えず、失敗を試行の記録の詳細に残す。
func TestReleaseAfterLand_AFailedCancelIsKeptInTheDetailAndDoesNotChangeTheResult(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.client.cancelOut = ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "cannot cancel"}
	f.merged(f.head)
	f.queueReview()
	if item := f.release(t); item.Result != ExecutionResultAlreadyMerged {
		t.Fatalf("item = %+v, want already_merged", item)
	}
	if got := f.lastDetail(t); !strings.Contains(got, "could not cancel") {
		t.Fatalf("detail = %q, want the cancel failure", got)
	}
}

// AC-301: 承認待ちの反映の操作の片付けが閉じたとき、終わっていない対応する実行へ cancel が呼ばれる（status で読んで、終わりでないとき）。
func TestReleaseAfterLand_ClosingAPendingReleaseCancelsTheOpenRunAfterReadingIt(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.cancelsWith()
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE operation SET state = 'pending' WHERE id = 1`)
		return err
	})
	f.merged(f.head)
	f.queueReview()
	statuses := len(f.client.statuses)
	res, err := f.s.ExecuteReleases(context.Background(), nil, ReleaseExecOptions{
		Checks: f.fake, Connectors: f.conn, Agent: &AgentDeclaration{FailureLimit: 3}, Contract: f.client, Trigger: ReleaseExecTriggerOpExec,
	})
	if err != nil || len(res.Tidied) != 1 || res.Tidied[0].Action != ReleaseTidyClosed {
		t.Fatalf("ExecuteReleases = %+v err %v, want one closed release", res, err)
	}
	if len(f.client.statuses)-statuses != 1 || len(f.client.cancels) != 1 {
		t.Fatalf("statuses +%d cancels %v, want one status read and one cancel", len(f.client.statuses)-statuses, f.client.cancels)
	}
}

func (f *landFixture) reopenInput(cmp *fakeCompare) ReopenForReleaseInput {
	return ReopenForReleaseInput{
		OperationID: "OP-1", Reason: ReopenReasonContentChanged,
		Sources:         ReleaseHeadSources{Checks: f.fake, Compare: cmp},
		ContractSources: ReopenContractSources{Contract: f.client, Agent: &AgentDeclaration{FailureLimit: 3}, Connectors: f.conn},
	}
}

func (f *landFixture) contentChangedCompare() *fakeCompare {
	f.fake.pr.HeadSHA = "hc"
	cmp := newFakeCompare()
	cmp.set(f.head, "hc", ahead(commit("hc", "add feature", f.head)))
	cmp.set("main", "hc", ahead(commit(f.head, "fix", "p0"), commit("hc", "add feature", f.head)))
	return cmp
}

// AC-314・315・317相当: 完了から戻す前に、終わっていないタスクの実行の status が呼ばれ、running なら戻さない（課題は done のまま）。
func TestReleaseAfterLand_ReopenReadsTheTaskRunAndDoesNotReopenWhileItIsRunning(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.sendLand(t)
	cmp := f.contentChangedCompare()
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, nil, nil)}
	statuses := len(f.client.statuses)
	_, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, f.reopenInput(cmp))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if len(f.client.statuses)-statuses != 1 {
		t.Errorf("status calls = %d, want 1", len(f.client.statuses)-statuses)
	}
	if got := f.status(t, f.cid); got != StatusDone {
		t.Errorf("challenge = %s, want done", got)
	}
}

// AC-316: status が失敗したら、課題は done のまま。
func TestReleaseAfterLand_ReopenDoesNotReopenWhenTheStatusReadFails(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.sendLand(t)
	cmp := f.contentChangedCompare()
	f.client.queue = []ContractLaunchOutput{{Result: RunResultErrored, ErrorSummary: "down"}}
	if _, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, f.reopenInput(cmp)); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if got := f.status(t, f.cid); got != StatusDone {
		t.Errorf("challenge = %s, want done", got)
	}
}

// 読む口が無い（契約のコマンドの起動の口が渡されない）のに終わっていないタスクの実行があるときも、戻さない。
func TestReleaseAfterLand_ReopenDoesNotReopenWithoutAWayToReadTheRun(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	cmp := f.contentChangedCompare()
	in := f.reopenInput(cmp)
	in.ContractSources = ReopenContractSources{}
	if _, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, in); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if got := f.status(t, f.cid); got != StatusDone {
		t.Errorf("challenge = %s, want done", got)
	}
}

// 待ち（レビュー待ち）なら戻す。実行は待ちのまま残る。
func TestReleaseAfterLand_ReopenProceedsWhenTheTaskRunIsWaiting(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.sendLand(t)
	cmp := f.contentChangedCompare()
	f.client.queue = []ContractLaunchOutput{f.refusedReview()}
	res, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, f.reopenInput(cmp))
	if err != nil || !res.Reopened {
		t.Fatalf("ReopenChallengeForRelease = %+v err %v, want reopened", res, err)
	}
	if f.runEnded(t) {
		t.Error("the waiting contract run was ended")
	}
}

// 終わっていれば、実行を終わった記録にして戻す。
func TestReleaseAfterLand_ReopenRecordsAnEndedRunAndProceeds(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.sendLand(t)
	cmp := f.contentChangedCompare()
	f.client.queue = []ContractLaunchOutput{f.endedOut(ContractStateSucceeded)}
	res, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, f.reopenInput(cmp))
	if err != nil || !res.Reopened {
		t.Fatalf("ReopenChallengeForRelease = %+v err %v, want reopened", res, err)
	}
	if !f.runEnded(t) {
		t.Error("the contract run is not recorded as ended")
	}
}

// AC-318・319: 「入れてよい」を返した実行を持つ課題を完了から戻して land_ref を消し、完了を承認し直して新しい反映の操作を承認した後の
// 最初の反映の段は、land_refused にならず、同じ実行へ {choice} が land の resume が呼ばれる。
func TestReleaseAfterLand_AfterReopeningAndReapprovingTheFirstStageReturnsLandAgain(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.sendLand(t)
	cmp := f.contentChangedCompare()
	f.client.queue = []ContractLaunchOutput{f.refusedReview()}
	res, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, f.reopenInput(cmp))
	if err != nil || !res.Reopened {
		t.Fatalf("ReopenChallengeForRelease = %+v err %v, want reopened", res, err)
	}
	if ref, delivered := f.landRow(t); ref != "" || delivered {
		t.Fatalf("land_ref=%q delivered=%v, want cleared (AC-318)", ref, delivered)
	}
	// 検証し直して、完了を承認し直す（新しい反映の操作も承認される）。
	f.j5.j5.result = j5Out("met", nil, nil)
	if _, err := f.s.VerifyAutoJ5(context.Background(), f.j5.input(f.cycle(t, 301), &f.cid)); err != nil {
		t.Fatal(err)
	}
	prev, err := f.s.PrepareApproval(context.Background(), f.cid)
	if err != nil || len(prev.PendingReleases) != 1 {
		t.Fatalf("PrepareApproval = %+v err %v", prev, err)
	}
	head := f.head
	if _, _, err := f.s.ExecuteApproval(context.Background(), ApprovalRequest{
		ChallengeID: f.cid, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
		ReleaseHeads: []ReleaseHead{{Operation: prev.PendingReleases[0], HeadSHA: &head}},
	}, verifiedAttestationForTest(t, f.cid)); err != nil {
		t.Fatalf("ExecuteApproval: %v", err)
	}
	// 戻した後に PR の head は承認した中身へ戻り、委譲先は戻す前と同じ中身のレビュー待ち。land_ref が残っていれば land_refused になる。
	f.fake.pr.HeadSHA = f.head
	f.client.queue = []ContractLaunchOutput{reviewFor(f.head)}
	resumes := len(f.client.resumes)
	res2, err := f.s.ExecuteReleases(context.Background(), []string{prev.PendingReleases[0].ID}, ReleaseExecOptions{
		Checks: f.fake, Connectors: f.conn, Agent: &AgentDeclaration{FailureLimit: 3}, Contract: f.client, Trigger: ReleaseExecTriggerOpExec,
	})
	if err != nil || len(res2.Items) != 1 || res2.Items[0].Result != ExecutionResultLandSent {
		t.Fatalf("ExecuteReleases = %+v err %v, want land_sent (not land_refused)", res2, err)
	}
	if len(f.client.resumes) != resumes+1 || !strings.Contains(strings.Join(f.client.resumes[resumes], " "), "--choice land") {
		t.Fatalf("resumes = %v, want a new land resume", f.client.resumes)
	}
}

// 片付けが、完了の課題の検証し直しの前に、タスクの実行が running で戻せなければ、その課題を飛ばして結果に残す（課題は done のまま）。
func TestReleaseAfterLand_TidySkipsAReopenWhileTheTaskRunIsRunning(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.sendLand(t)
	f.contentChangedCompare() // head を hc にする
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE operation SET state = 'pending' WHERE id = 1`)
		return err
	})
	cmp := newFakeCompare()
	cmp.set(f.head, "hc", ahead(commit("hc", "add feature", f.head)))
	cmp.set("main", "hc", ahead(commit(f.head, "fix", "p0"), commit("hc", "add feature", f.head)))
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, nil, nil)}
	res, err := f.s.ExecuteReleases(context.Background(), nil, ReleaseExecOptions{
		Checks: f.fake, Compare: cmp, Connectors: f.conn, Agent: &AgentDeclaration{FailureLimit: 3}, Contract: f.client, Trigger: ReleaseExecTriggerOpExec,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tidied) != 1 || res.Tidied[0].Action != ReleaseTidySkipped {
		t.Fatalf("Tidied = %+v, want one skipped", res.Tidied)
	}
	if got := f.status(t, f.cid); got != StatusDone {
		t.Fatalf("challenge = %s, want done", got)
	}
}
