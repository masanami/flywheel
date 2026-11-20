package core

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 反映の段: 終わっていない契約の実行が無い承認済みの反映の操作は、flywheel の外で反映先へ入るのを待つ。

// insertContractRunForRelease は課題に契約の実行を 1 本足す（ended が真なら終わった記録）。
func (f *relFixture) insertContractRunForRelease(t *testing.T, state string, ended bool) {
	t.Helper()
	endedAt := "NULL"
	if ended {
		endedAt = "'x'"
	}
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO run (challenge_id, kind, challenge_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket)
			VALUES (?, 'contract', 1, 's', 1, 'h', 'x', 'x', 'x', 'succeeded', 1000000, 'impl')`, f.cid)
		if err != nil {
			return err
		}
		runID, _ := res.LastInsertId()
		_, err = tx.Exec(`INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, max_budget_usd, started_at, observed_at, ended_at)
			VALUES (?, 'tool', 'ext-1', ?, ?, 's', 1000000, 'x', 'x', `+endedAt+`)`, f.cid, runID, state)
		return err
	})
}

func (f *relFixture) runResult(t *testing.T) *ReleaseExecResult {
	t.Helper()
	res, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, f.opts())
	if err != nil {
		t.Fatalf("ExecuteReleases() error = %v", err)
	}
	f.now = f.now.Add(1e9)
	return res
}

func TestReleaseOutside_NoContractRun_ResultIsWaitingOutside(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	item := f.run(t)
	if item.Result != ExecutionResultWaitingOutside {
		t.Fatalf("result = %s, want waiting_outside", item.Result)
	}
	if at := f.attempts(t); len(at) != 1 || at[0].Result != ExecutionResultWaitingOutside || at[0].open() {
		t.Fatalf("attempts = %+v, want one closed waiting_outside attempt", at)
	}
}

func TestReleaseOutside_RouteIsOutside(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	if item := f.run(t); item.Route != ReleaseRouteOutside || ReleaseRouteOutside != "outside" {
		t.Fatalf("route = %q, want outside", item.Route)
	}
}

func TestReleaseOutside_OperationStaysApproved(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	f.run(t)
	if st := f.state(t); st.opState != "approved" {
		t.Fatalf("state = %s, want approved", st.opState)
	}
}

func TestReleaseOutside_WritesNoOperationLog(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	f.run(t)
	if n := len(f.opExecuteLogs(t)); n != 0 {
		t.Fatalf("op_execute logs = %d, want 0", n)
	}
}

func TestReleaseOutside_SecondTimeAddsNoAttemptRow(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	f.run(t)
	res := f.runResult(t)
	if len(res.Items) != 0 {
		t.Fatalf("items = %+v, want none", res.Items)
	}
	if at := f.attempts(t); len(at) != 1 {
		t.Fatalf("attempts = %d, want 1 (no new row while waiting)", len(at))
	}
	f.runResult(t)
	if at := f.attempts(t); len(at) != 1 {
		t.Fatalf("attempts after a third time = %d, want 1", len(at))
	}
}

func TestReleaseOutside_SecondTimeReturnsNotStartedWaitingOutside(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	f.run(t)
	res := f.runResult(t)
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedWaitingOutside ||
		res.NotStarted[0].OperationID != formatOperationID(f.opID) || res.NotStarted[0].ChallengeID != formatChallengeID(f.cid) {
		t.Fatalf("not_started = %+v, want waiting_outside for the operation", res.NotStarted)
	}
	if got := string(NotStartedWaitingOutside); got != "waiting_outside" {
		t.Fatalf("reason word = %q", got)
	}
}

func TestReleaseOutside_NotStartedReasonIsInTheClosedSet(t *testing.T) {
	t.Parallel()
	for _, r := range NotStartedReasonValues() {
		if r == NotStartedWaitingOutside {
			return
		}
	}
	t.Fatal("NotStartedReasonValues() lacks waiting_outside")
}

func TestReleaseOutside_SecondTimeLeavesNoOpenAttempt(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	f.run(t)
	f.runResult(t)
	at := f.attempts(t)
	if len(at) != 1 || at[0].open() || at[0].Result != ExecutionResultWaitingOutside {
		t.Fatalf("attempts = %+v, want one closed waiting_outside attempt", at)
	}
}

func TestReleaseOutside_EnteredWithApprovedHead_BecomesExecuted(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	f.run(t)
	f.fake.pr = UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: relHead, MergeCommitSHA: "mc"}
	if item := f.run(t); item.Result != ExecutionResultAlreadyMerged {
		t.Fatalf("result = %s, want already_merged", item.Result)
	}
	if st := f.state(t); st.opState != "executed" || st.mergeCommit.String != "mc" {
		t.Fatalf("state = %+v, want executed with the merge commit read from upstream", st)
	}
}

func TestReleaseOutside_EnteredWithApprovedHead_LeavesAnAlreadyMergedAttempt(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	f.run(t)
	f.fake.pr = UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: relHead, MergeCommitSHA: "mc"}
	f.run(t)
	at := f.attempts(t)
	if len(at) != 2 || at[1].Result != ExecutionResultAlreadyMerged {
		t.Fatalf("attempts = %+v, want waiting_outside then already_merged", at)
	}
}

func TestReleaseOutside_ChecksFailedAfterWaiting_IsNotHiddenByTheWait(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	f.run(t)
	f.fake.pr.Checks = []UpstreamCheck{{Status: "completed", Conclusion: "failure"}}
	if item := f.run(t); item.Result != ExecutionResultChecksFailed {
		t.Fatalf("result = %s, want checks_failed", item.Result)
	}
}

func TestReleaseOutside_WaitingAfterAChangeInBetweenWritesANewRow(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	f.run(t)
	f.fake.pr.Checks = []UpstreamCheck{{Status: "queued"}}
	f.run(t) // waiting_checks
	f.fake.pr.Checks = nil
	if item := f.run(t); item.Result != ExecutionResultWaitingOutside {
		t.Fatalf("result = %s, want waiting_outside again as a new row", item.Result)
	}
	if n := len(f.attempts(t)); n != 3 {
		t.Fatalf("attempts = %d, want 3 (the wait is only deduplicated against the immediately preceding attempt)", n)
	}
}

func TestReleaseOutside_UnfinishedContractRun_ResultIsWaitingDelegate(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	f.insertContractRunForRelease(t, "running", false)
	item := f.run(t)
	if item.Result != ExecutionResultWaitingDelegate || item.Route != "" {
		t.Fatalf("item = %+v, want waiting_delegate without a route", item)
	}
	if st := f.state(t); st.opState != "approved" {
		t.Fatalf("state = %s, want approved", st.opState)
	}
}

func TestReleaseOutside_FinishedContractRunIsNotWaitedFor(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	f.insertContractRunForRelease(t, "succeeded", true)
	if item := f.run(t); item.Result != ExecutionResultWaitingOutside {
		t.Fatalf("result = %s, want waiting_outside", item.Result)
	}
}

func TestReleaseOutside_WaitingDelegateAddsARowEachTime(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	f.insertContractRunForRelease(t, "running", false)
	f.run(t)
	f.run(t)
	if n := len(f.attempts(t)); n != 2 {
		t.Fatalf("attempts = %d, want 2", n)
	}
}

func TestReleaseOutside_WaitingDelegateIsNotCounted(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	f.limit = 1
	f.insertContractRunForRelease(t, "running", false)
	for i := 0; i < 3; i++ {
		if item := f.run(t); item.Result != ExecutionResultWaitingDelegate || item.ConsecutiveFailures != 0 {
			t.Fatalf("item = %+v, want an uncounted waiting_delegate", item)
		}
	}
	if st := f.state(t); st.opState != "approved" {
		t.Fatalf("state = %s, want approved", st.opState)
	}
}

func TestReleaseOutside_ChecksFailedCountsAndReachesTheLimit(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	f.limit = 2
	f.fake.pr.Checks = []UpstreamCheck{{Status: "completed", Conclusion: "failure"}}
	if item := f.run(t); item.Result != ExecutionResultChecksFailed || item.ConsecutiveFailures != 1 {
		t.Fatalf("first = %+v", item)
	}
	if st := f.state(t); st.opState != "approved" {
		t.Fatalf("state below the limit = %s, want approved", st.opState)
	}
	if item := f.run(t); item.Result != ExecutionResultLimitReached {
		t.Fatalf("second = %+v, want limit_reached", item)
	}
	if st := f.state(t); st.opState != "pending" {
		t.Fatalf("state at the limit = %s, want pending", st.opState)
	}
}

func TestReleaseOutside_HeadChangedDoesNotReopenTheDoneChallenge(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	f.fake.pr.HeadSHA = "zzz"
	if item := f.run(t); item.Result != ExecutionResultHeadChanged {
		t.Fatalf("item = %+v", item)
	}
	st := f.state(t)
	if st.opState != "pending" || st.chStatus != string(StatusDone) {
		t.Fatalf("state = %+v, want the operation pending and the challenge still done", st)
	}
}

func TestReleaseOutside_DoesNotReadMergeability(t *testing.T) {
	t.Parallel()
	f := newRelFixture(t)
	no := false
	f.fake.pr.Mergeable = &no
	f.fake.pr.MergeableState = UpstreamMergeStateBehind
	if item := f.run(t); item.Result != ExecutionResultWaitingOutside {
		t.Fatalf("result = %s, want waiting_outside regardless of mergeable", item.Result)
	}
}

// --- 反映先へ入れる口が core・adapter に無いこと ---

func readGoSources(t *testing.T, dir string, includeTests bool) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || (!includeTests && strings.HasSuffix(n, "_test.go")) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
	}
	return b.String()
}

func TestSource_CoreHasNoMergeRequestInterfaces(t *testing.T) {
	t.Parallel()
	src := readGoSources(t, ".", false)
	for _, id := range []string{"UpstreamPullRequestMerger", "UpstreamPullRequestBranchUpdater"} {
		if strings.Contains(src, id) {
			t.Errorf("internal/core still mentions %s", id)
		}
	}
}

func TestSource_GitHubAdapterHasNoMergeOrUpdateBranchCall(t *testing.T) {
	t.Parallel()
	src := readGoSources(t, filepath.Join("..", "adapters", "github"), false)
	if strings.Contains(src, "update-branch") {
		t.Error("internal/adapters/github still mentions update-branch")
	}
	for _, call := range []string{`"pr", "merge"`, `"merge",`, `, "merge")`} {
		if strings.Contains(src, call) {
			t.Errorf("internal/adapters/github still passes the gh argument merge (%s)", call)
		}
	}
}
