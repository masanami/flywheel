package core

import (
	"strings"
	"testing"
	"time"
)

// 反映の依頼（接続先の予約）の後の読み取りと、待ちの期限の試験。

func requestedPR() UpstreamPullRequestChecks {
	pr := openPR()
	pr.MergeRequested = true
	return pr
}

// 依頼が受け付けられている間は、チェックが 0 件でも未完了でも、依頼し直さず待つ（数えない）。
func TestRelease_WhileRequestedWaitsWithoutRequestingAgain(t *testing.T) {
	pending := []UpstreamCheck{{Name: "check", Status: "in_progress"}}
	for name, checks := range map[string][]UpstreamCheck{"no checks": nil, "pending": pending,
		"all passed but not merged yet": {{Name: "check", Status: "completed", Conclusion: "success"}}} {
		t.Run(name, func(t *testing.T) {
			f := newRelFixture(t)
			if item := f.run(t); item.Result != ExecutionResultMergeRequested {
				t.Fatalf("first run = %s, want merge_requested", item.Result)
			}
			f.fake.pr = requestedPR()
			f.fake.pr.Checks = checks
			f.fake.merged = false
			item := f.run(t)
			if item.Result != ExecutionResultWaitingChecks || item.ConsecutiveFailures != 0 {
				t.Fatalf("item = %+v, want waiting_checks", item)
			}
			if len(f.fake.mergeArgs) != 1 {
				t.Errorf("merge calls = %v, want only the first request", f.fake.mergeArgs)
			}
			if st := f.state(t); st.opState != "approved" {
				t.Errorf("state = %s, want approved", st.opState)
			}
		})
	}
}

// 依頼の後、次の周で反映されていれば反映済み（承認した head のマージ）。
func TestRelease_RequestedThenMergedNextRunIsAlreadyMerged(t *testing.T) {
	f := newRelFixture(t)
	f.run(t)
	f.fake.pr = UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: relHead, MergeCommitSHA: "mc"}
	item := f.run(t)
	if item.Result != ExecutionResultAlreadyMerged || item.MergeCommit != "mc" {
		t.Fatalf("item = %+v, want already_merged", item)
	}
	if st := f.state(t); st.opState != "executed" {
		t.Errorf("state = %s, want executed", st.opState)
	}
}

// 依頼した head と違う head が反映されたときは、反映済みにせず承認なしの反映として残す。
func TestRelease_RequestedThenMergedWithOtherHeadIsMergedUnapproved(t *testing.T) {
	f := newRelFixture(t)
	f.run(t)
	f.fake.pr = UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: "zzz", MergeCommitSHA: "mc"}
	if item := f.run(t); item.Result != ExecutionResultMergedUnapproved {
		t.Fatalf("item = %+v, want merged_unapproved", item)
	}
}

// 依頼したチェックが落ちたときは checks_failed（数える）。上限に達すると理由を写して取り消す（保留）。
func TestRelease_RequestedThenChecksFailedCountsAndHoldsAtLimit(t *testing.T) {
	f := newRelFixture(t)
	f.limit = 2
	f.run(t)
	f.fake.pr = requestedPR()
	f.fake.pr.Checks = []UpstreamCheck{{Name: "check", Status: "completed", Conclusion: "failure"}}
	if item := f.run(t); item.Result != ExecutionResultChecksFailed || item.ConsecutiveFailures != 1 {
		t.Fatalf("item = %+v, want checks_failed (1)", item)
	}
	item := f.run(t)
	if item.Result != ExecutionResultLimitReached {
		t.Fatalf("item = %+v, want limit_reached", item)
	}
	if st := f.state(t); st.opState != "pending" {
		t.Errorf("state = %s, want pending (held)", st.opState)
	}
	at := f.attempts(t)
	if d := at[len(at)-1].Detail; d != string(ExecutionResultChecksFailed) {
		t.Errorf("detail = %q, want the original result checks_failed", d)
	}
}

// 依頼から 90 分ちょうどは待ち、超えたら状態を写して取り消す。
func TestRelease_RequestTimeoutHoldsWithTheObservedState(t *testing.T) {
	f := newRelFixtureAt(t, StatusInProgress)
	requestedAt := f.now
	f.run(t)
	f.fake.pr = requestedPR()
	f.fake.pr.MergeableState = "blocked"
	f.fake.pr.Checks = []UpstreamCheck{{Name: "check", Status: "in_progress"}, {Name: "e2e-real", Status: "completed", Conclusion: "success"}}

	f.now = requestedAt.Add(ReleaseMergeRequestTimeout)
	if item := f.run(t); item.Result != ExecutionResultWaitingChecks {
		t.Fatalf("at exactly the timeout: %s, want waiting_checks", item.Result)
	}
	f.now = requestedAt.Add(ReleaseMergeRequestTimeout + time.Minute)
	item := f.run(t)
	if item.Result != ExecutionResultMergeTimeout {
		t.Fatalf("after the timeout: %s, want merge_timeout", item.Result)
	}
	if st := f.state(t); st.opState != "pending" {
		t.Errorf("state = %s, want pending (held)", st.opState)
	}
	at := f.attempts(t)
	d := at[len(at)-1].Detail
	for _, want := range []string{"success=1", "pending=1", "mergeable_state=blocked"} {
		if !strings.Contains(d, want) {
			t.Errorf("detail %q lacks %q", d, want)
		}
	}
}

// 依頼の後に既定ブランチが進んで遅れたら、チェックの完了を待たず取り込み、依頼が残っていれば依頼し直さない。
func TestRelease_BehindWhileRequestedUpdatesBranchWithoutWaitingForChecks(t *testing.T) {
	f := newRelFixture(t)
	f.run(t)
	f.fake.pr = requestedPR()
	f.fake.pr.MergeableState = UpstreamMergeStateBehind
	f.fake.pr.Checks = []UpstreamCheck{{Name: "check", Status: "in_progress"}}
	bf := &behindFake{fakeRelease: f.fake}
	o := f.opts()
	o.Merger = bf
	if item := f.runWith(t, o); item.Result != ExecutionResultWaitingChecks {
		t.Fatalf("item = %+v, want waiting_checks", item)
	}
	if len(bf.updates) != 1 || bf.updates[0] != relHead {
		t.Errorf("updates = %v, want one on %s", bf.updates, relHead)
	}
	if len(f.fake.mergeArgs) != 1 {
		t.Errorf("merge calls = %v, want only the first request", f.fake.mergeArgs)
	}
}

// 取り込みで依頼が外れたときは、次の周が新しい head で依頼し直す。
func TestRelease_RequestRenewedOnTheNewHeadAfterUpdate(t *testing.T) {
	f, cmp := movedHeadFixture(t)
	f.fake.pr.Checks = nil
	f.fake.pr.MergeRequested = false
	o := f.opts()
	o.Compare = cmp
	if item := f.runWith(t, o); item.Result != ExecutionResultMergeRequested {
		t.Fatalf("item = %+v, want merge_requested", item)
	}
	if got := f.fake.mergeArgs[0][2]; got != f.fake.pr.HeadSHA || got == relHead {
		t.Errorf("requested head = %s, want the moved head %s (not the approved %s)", got, f.fake.pr.HeadSHA, relHead)
	}
}

// 期限を過ぎた後に衝突が出たときは、期限（merge_timeout）より先に not_mergeable（衝突の立て直し）。
func TestRelease_ConflictAfterTimeoutIsNotMergeableNotTimeout(t *testing.T) {
	f := newRelFixtureAt(t, StatusInProgress)
	requestedAt := f.now
	f.run(t)
	f.fake.pr = requestedPR()
	no := false
	f.fake.pr.Mergeable = &no
	f.fake.pr.Checks = []UpstreamCheck{{Name: "check", Status: "in_progress"}}
	f.now = requestedAt.Add(ReleaseMergeRequestTimeout + time.Minute)
	if item := f.run(t); item.Result != ExecutionResultNotMergeable {
		t.Fatalf("item = %+v, want not_mergeable", item)
	}
}

// 依頼の試行が記録に無くても（依頼の後の読み取りに失敗した等）、依頼が残っていれば最初の試行から数えて期限が来る。
func TestRelease_RequestedWithoutRecordedRequestStillTimesOut(t *testing.T) {
	f := newRelFixtureAt(t, StatusInProgress)
	started := f.now
	f.fake.pr = requestedPR() // 人が先に依頼した等。flywheel は依頼しない
	if item := f.run(t); item.Result != ExecutionResultWaitingChecks {
		t.Fatalf("first run = %+v, want waiting_checks", item)
	}
	f.now = started.Add(ReleaseMergeRequestTimeout + time.Minute)
	if item := f.run(t); item.Result != ExecutionResultMergeTimeout {
		t.Fatalf("item = %+v, want merge_timeout", item)
	}
}
