package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core/coretest"
)

const (
	relRef  = "https://github.com/o/r/pull/1"
	relHead = "a1b2c3"
)

// fakeRelease は UpstreamCheckSource と UpstreamPullRequestMerger の偽の実装（gh は起動しない）。
type fakeRelease struct {
	mu        sync.Mutex
	pr        UpstreamPullRequestChecks
	afterPR   *UpstreamPullRequestChecks // MergePullRequest の後の読み取り。nil なら pr のまま
	getErr    error
	mergeErr  error
	merged    bool
	getCalls  int
	mergeArgs [][]string
	// onMerge が非 nil なら、MergePullRequest の呼び出しの時点で（記録の前に）呼ぶ。
	onMerge func()
	// onGet が非 nil なら、GetPullRequestChecks の呼び出しの時点で呼ぶ。
	onGet func()
	// prSeq が空でなければ、GetPullRequestChecks の n 回目の呼び出しに prSeq[n-1] を返す
	// （呼び出しが prSeq より多ければ最後の要素）。pr・afterPR より優先する。
	prSeq []UpstreamPullRequestChecks
}

func truePtr() *bool { v := true; return &v }

func openPR() UpstreamPullRequestChecks {
	return UpstreamPullRequestChecks{URL: relRef, State: "open", Base: "main", HeadSHA: relHead, Mergeable: truePtr()}
}

func (f *fakeRelease) GetPullRequestChecks(_ context.Context, _ string, _ int) (UpstreamPullRequestChecks, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	if f.onGet != nil {
		f.onGet()
	}
	if f.getErr != nil {
		return UpstreamPullRequestChecks{}, f.getErr
	}
	if len(f.prSeq) > 0 {
		return f.prSeq[min(f.getCalls, len(f.prSeq))-1], nil
	}
	if f.merged && f.afterPR != nil {
		return *f.afterPR, nil
	}
	return f.pr, nil
}

func (f *fakeRelease) MergePullRequest(_ context.Context, prURL, method, head string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mergeArgs = append(f.mergeArgs, []string{prURL, method, head})
	if f.onMerge != nil {
		f.onMerge()
	}
	if f.mergeErr != nil {
		return f.mergeErr
	}
	f.merged = true
	return nil
}

func relConnectors(method string) *ConnectorsDeclaration {
	return &ConnectorsDeclaration{Repos: []ConnectorRepo{{Name: "r", Remote: "o/r", DefaultBranch: "main", MergeMethod: method}}}
}

type relFixture struct {
	s     *Store
	cid   int64
	opID  int64
	fake  *fakeRelease
	now   time.Time
	limit int
}

func newRelFixture(t *testing.T) *relFixture {
	t.Helper()
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusDone)
	cid := mustParseChallengeID(t, c.ID)
	opID := coretest.InsertApprovedRelease(t, s.workspace, int(cid), relRef, relHead, execTS(0))
	return &relFixture{s: s, cid: cid, opID: opID, fake: &fakeRelease{pr: openPR()}, now: execTime(100), limit: 3}
}

func (f *relFixture) opts() ReleaseExecOptions {
	return ReleaseExecOptions{
		Checks: f.fake, Merger: f.fake, Connectors: relConnectors(""),
		Agent: &AgentDeclaration{FailureLimit: f.limit}, Trigger: ReleaseExecTriggerOpExec,
		Now: func() time.Time { return f.now },
	}
}

func (f *relFixture) run(t *testing.T) *ReleaseExecItem {
	t.Helper()
	res, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, f.opts())
	if err != nil {
		t.Fatalf("ExecuteReleases() error = %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("Items = %+v, NotStarted = %+v, want 1 item", res.Items, res.NotStarted)
	}
	f.now = f.now.Add(time.Second)
	return &res.Items[0]
}

type relState struct {
	opState     string
	opVersion   int
	chVersion   int
	chStatus    string
	executedAt  sql.NullString
	mergeCommit sql.NullString
}

func (f *relFixture) state(t *testing.T) relState {
	t.Helper()
	var st relState
	readForTest(t, f.s, func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT state, version, executed_at, merge_commit FROM operation WHERE id = ?`, f.opID).
			Scan(&st.opState, &st.opVersion, &st.executedAt, &st.mergeCommit); err != nil {
			return err
		}
		return tx.QueryRow(`SELECT version, status FROM challenge WHERE id = ?`, f.cid).Scan(&st.chVersion, &st.chStatus)
	})
	return st
}

func (f *relFixture) attempts(t *testing.T) []*operationExecutionRow {
	t.Helper()
	var out []*operationExecutionRow
	readForTest(t, f.s, func(tx *sql.Tx) error {
		var err error
		out, err = listOperationExecutions(context.Background(), tx, f.opID)
		return err
	})
	return out
}

func (f *relFixture) opExecuteLogs(t *testing.T) []Activity {
	t.Helper()
	all, err := f.s.ListActivities(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []Activity
	for _, a := range all {
		if a.Action == ActivityActionOpExecute {
			out = append(out, a)
		}
	}
	return out
}

func relAfter(t *testing.T, a Activity) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(a.After, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// --- 実行済み ---

func TestExecuteReleases_AllConditionsPass_MergesOnceAndMarksExecuted(t *testing.T) {
	f := newRelFixture(t)
	f.fake.afterPR = &UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: relHead, MergeCommitSHA: "m3rg3"}
	before := f.state(t)
	item := f.run(t)
	if item.Result != ExecutionResultMerged || item.MergeCommit != "m3rg3" || item.ObservedHead != relHead || item.ApprovedHead != relHead {
		t.Fatalf("item = %+v", item)
	}
	if len(f.fake.mergeArgs) != 1 || strings.Join(f.fake.mergeArgs[0], " ") != relRef+" squash "+relHead {
		t.Fatalf("merge args = %v, want [url squash head] once", f.fake.mergeArgs)
	}
	st := f.state(t)
	if st.opState != "executed" || !st.executedAt.Valid || st.mergeCommit.String != "m3rg3" {
		t.Errorf("state = %+v", st)
	}
	if st.opVersion != before.opVersion+1 || st.chVersion != before.chVersion+1 {
		t.Errorf("versions op %d→%d ch %d→%d, want +1 each", before.opVersion, st.opVersion, before.chVersion, st.chVersion)
	}
	if st.chStatus != string(StatusDone) {
		t.Errorf("challenge status = %s, want done (execution does not change it)", st.chStatus)
	}
	logs := f.opExecuteLogs(t)
	if len(logs) != 1 || logs[0].Entity != "operation" || logs[0].Channel != "cli" {
		t.Fatalf("logs = %+v", logs)
	}
	a := relAfter(t, logs[0])
	if a["result"] != "merged" || a["approved_head"] != relHead || a["observed_head"] != relHead || a["merge_commit"] != "m3rg3" || a["trigger"] != "op exec" {
		t.Errorf("after = %v", a)
	}
	at := f.attempts(t)
	if len(at) != 1 || at[0].Result != ExecutionResultMerged || at[0].open() {
		t.Errorf("attempts = %+v", at)
	}
}

func TestExecuteReleases_MergeMethodFlagFollowsDeclaration(t *testing.T) {
	for _, m := range []string{"squash", "merge", "rebase"} {
		f := newRelFixture(t)
		o := f.opts()
		o.Connectors = relConnectors(m)
		if _, err := f.s.ExecuteReleases(context.Background(), nil, o); err != nil {
			t.Fatal(err)
		}
		if len(f.fake.mergeArgs) != 1 || f.fake.mergeArgs[0][1] != m {
			t.Errorf("method %s: args = %v", m, f.fake.mergeArgs)
		}
	}
}

func TestExecuteReleases_MergeNotReflected_Unconfirmed_NotCounted(t *testing.T) {
	f := newRelFixture(t)
	item := f.run(t) // 依頼の後の読み取りも open のまま
	if item.Result != ExecutionResultMergeUnconfirmed || item.ConsecutiveFailures != 0 {
		t.Fatalf("item = %+v", item)
	}
	if st := f.state(t); st.opState != "approved" {
		t.Errorf("state = %s, want approved", st.opState)
	}
	if len(f.opExecuteLogs(t)) != 0 {
		t.Error("unconfirmed must not write op_execute")
	}
}

func TestExecuteReleases_MergedWithOtherHeadAfterRequest_MergedUnapproved(t *testing.T) {
	f := newRelFixture(t)
	f.fake.afterPR = &UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: "zzz", MergeCommitSHA: "m"}
	item := f.run(t)
	if item.Result != ExecutionResultMergedUnapproved || item.ObservedHead != "zzz" {
		t.Fatalf("item = %+v", item)
	}
	if st := f.state(t); st.opState != "pending" {
		t.Errorf("state = %s", st.opState)
	}
}

func TestExecuteReleases_MergeRequestFails_MergeFailedCounted(t *testing.T) {
	f := newRelFixture(t)
	f.fake.mergeErr = errors.New("boom")
	item := f.run(t)
	if item.Result != ExecutionResultMergeFailed || item.ConsecutiveFailures != 1 {
		t.Fatalf("item = %+v", item)
	}
	if st := f.state(t); st.opState != "approved" {
		t.Errorf("state = %s", st.opState)
	}
}

func TestExecuteReleases_AlreadyMergedWithApprovedHead_NoMergeCall(t *testing.T) {
	f := newRelFixture(t)
	f.fake.pr = UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "x", HeadSHA: relHead, MergeCommitSHA: "mc"}
	item := f.run(t)
	if item.Result != ExecutionResultAlreadyMerged || item.MergeCommit != "mc" || len(f.fake.mergeArgs) != 0 {
		t.Fatalf("item = %+v args=%v", item, f.fake.mergeArgs)
	}
	if st := f.state(t); st.opState != "executed" || st.mergeCommit.String != "mc" {
		t.Errorf("state = %+v", st)
	}
}

// --- 判定の順 ---

func TestJudgmentOrder(t *testing.T) {
	failedCheck := []UpstreamCheck{{Name: "ci", Status: "completed", Conclusion: "failure"}}
	pendingCheck := []UpstreamCheck{{Name: "ci", Status: "in_progress"}}
	cases := []struct {
		name   string
		mod    func(*relFixture)
		want   ExecutionResult
		state  string
		merges int
	}{
		{"closed", func(f *relFixture) { f.fake.pr.State = "closed" }, ExecutionResultClosed, "pending", 0},
		{"merged other head beats closed/head", func(f *relFixture) {
			f.fake.pr = UpstreamPullRequestChecks{State: "merged", Base: "x", HeadSHA: "zzz", Checks: failedCheck}
		}, ExecutionResultMergedUnapproved, "pending", 0},
		{"closed beats head_changed", func(f *relFixture) { f.fake.pr.State = "closed"; f.fake.pr.HeadSHA = "zzz" }, ExecutionResultClosed, "pending", 0},
		{"base_changed", func(f *relFixture) { f.fake.pr.Base = "dev" }, ExecutionResultBaseChanged, "pending", 0},
		{"base_changed beats head_changed", func(f *relFixture) { f.fake.pr.Base = "dev"; f.fake.pr.HeadSHA = "z" }, ExecutionResultBaseChanged, "pending", 0},
		{"head_changed beats failed check", func(f *relFixture) { f.fake.pr.HeadSHA = "zzz"; f.fake.pr.Checks = failedCheck }, ExecutionResultHeadChanged, "pending", 0},
		{"checks_failed beats pending", func(f *relFixture) {
			f.fake.pr.Checks = append(append([]UpstreamCheck{}, pendingCheck...), failedCheck...)
		}, ExecutionResultChecksFailed, "approved", 0},
		{"waiting_checks", func(f *relFixture) { f.fake.pr.Checks = pendingCheck }, ExecutionResultWaitingChecks, "approved", 0},
		{"mergeable null", func(f *relFixture) { f.fake.pr.Mergeable = nil }, ExecutionResultWaitingChecks, "approved", 0},
		{"mergeable false", func(f *relFixture) { v := false; f.fake.pr.Mergeable = &v }, ExecutionResultNotMergeable, "approved", 0},
		{"read failure", func(f *relFixture) { f.fake.getErr = errors.New("gh down") }, ExecutionResultUpstreamFailed, "approved", 0},
		{"no checks passes", func(f *relFixture) { f.fake.pr.Checks = nil }, ExecutionResultMergeUnconfirmed, "approved", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newRelFixture(t)
			c.mod(f)
			item := f.run(t)
			if item.Result != c.want {
				t.Fatalf("result = %s, want %s", item.Result, c.want)
			}
			if st := f.state(t); st.opState != c.state {
				t.Errorf("state = %s, want %s", st.opState, c.state)
			}
			if len(f.fake.mergeArgs) != c.merges {
				t.Errorf("merge calls = %d, want %d", len(f.fake.mergeArgs), c.merges)
			}
		})
	}
}

func TestCheckClassification(t *testing.T) {
	cases := []struct {
		c    UpstreamCheck
		want checkOutcome
	}{
		{UpstreamCheck{Status: "completed", Conclusion: "success"}, checkPass},
		{UpstreamCheck{Status: "completed", Conclusion: "neutral"}, checkPass},
		{UpstreamCheck{Status: "completed", Conclusion: "skipped"}, checkPass},
		{UpstreamCheck{Status: "completed", Conclusion: "failure"}, checkFail},
		{UpstreamCheck{Status: "completed", Conclusion: "cancelled"}, checkFail},
		{UpstreamCheck{Status: "completed", Conclusion: "timed_out"}, checkFail},
		{UpstreamCheck{Status: "completed", Conclusion: "action_required"}, checkFail},
		{UpstreamCheck{Status: "completed", Conclusion: "stale"}, checkFail},
		{UpstreamCheck{Status: "queued"}, checkPending},
		{UpstreamCheck{Status: "in_progress"}, checkPending},
		{UpstreamCheck{Status: UpstreamCheckStatusLegacy, Conclusion: "success"}, checkPass},
		{UpstreamCheck{Status: UpstreamCheckStatusLegacy, Conclusion: "failure"}, checkFail},
		{UpstreamCheck{Status: UpstreamCheckStatusLegacy, Conclusion: "error"}, checkFail},
		{UpstreamCheck{Status: UpstreamCheckStatusLegacy, Conclusion: "pending"}, checkPending},
		{UpstreamCheck{Status: UpstreamCheckStatusLegacy, Conclusion: "weird"}, checkPending},
	}
	for _, c := range cases {
		if got := classifyCheck(c.c); got != c.want {
			t.Errorf("classifyCheck(%+v) = %d, want %d", c.c, got, c.want)
		}
	}
}

func TestNoApprovedHead_And_Unsupported_CancelWithoutExternalCall(t *testing.T) {
	// head_sha が NULL
	s := newStoreForTest(t)
	fixedActor(t, "alice")
	c := createAndAdvance(t, s, StatusDone)
	cid := mustParseChallengeID(t, c.ID)
	opID := coretest.InsertApprovedRelease(t, s.workspace, int(cid), relRef, "", execTS(0))
	fake := &fakeRelease{pr: openPR()}
	res, err := s.ExecuteReleases(context.Background(), nil, ReleaseExecOptions{Checks: fake, Merger: fake, Connectors: relConnectors(""), Now: func() time.Time { return execTime(5) }})
	if err != nil || len(res.Items) != 1 || res.Items[0].Result != ExecutionResultNoApprovedHead || fake.getCalls != 0 {
		t.Fatalf("res = %+v err = %v calls = %d", res, err, fake.getCalls)
	}
	var st string
	var ov int
	readForTest(t, s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT state, version FROM operation WHERE id = ?`, opID).Scan(&st, &ov)
	})
	if st != "pending" || ov != 3 {
		t.Errorf("no_approved_head: state=%s version=%d, want pending/3", st, ov)
	}
	if logs, _ := s.ListActivities(context.Background(), nil); countAction(logs, ActivityActionOpExecute) != 2 {
		t.Errorf("want 2 op_execute logs")
	}

	// リポジトリが宣言に無い・PR の URL でない
	for _, ref := range []string{"https://github.com/x/y/pull/3", "not a url", "https://example.com/o/r/pull/1"} {
		f := newRelFixture(t)
		writeForTest(t, f.s, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE operation SET ref = ? WHERE id = ?`, ref, f.opID)
			return err
		})
		item := f.run(t)
		if item.Result != ExecutionResultUnsupported || f.fake.getCalls != 0 {
			t.Errorf("ref %q: item = %+v calls = %d", ref, item, f.fake.getCalls)
		}
		if st := f.state(t); st.opState != "pending" {
			t.Errorf("ref %q: state = %s", ref, st.opState)
		}
	}
}

// --- 取り消し ---

func TestCancel_BumpsVersionsKeepsApprovalWritesTwoLogs(t *testing.T) {
	f := newRelFixture(t)
	f.fake.pr.HeadSHA = "zzz"
	before := f.state(t)
	item := f.run(t)
	if item.Result != ExecutionResultHeadChanged || item.ObservedHead != "zzz" {
		t.Fatalf("item = %+v", item)
	}
	st := f.state(t)
	if st.opState != "pending" || st.opVersion != before.opVersion+1 || st.chVersion != before.chVersion+1 || st.chStatus != string(StatusDone) {
		t.Errorf("before %+v after %+v", before, st)
	}
	var n int
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM approval WHERE operation_id = ?`, f.opID).Scan(&n)
	})
	if n != 1 {
		t.Errorf("approval rows = %d, want 1 (kept)", n)
	}
	logs := f.opExecuteLogs(t)
	if len(logs) != 2 || logs[0].Entity != "operation" || logs[1].Entity != "challenge" {
		t.Fatalf("logs = %+v", logs)
	}
	for _, l := range logs {
		if relAfter(t, l)["result"] != "head_changed" {
			t.Errorf("%s after = %s", l.Entity, l.After)
		}
	}
	if relAfter(t, logs[0])["state"] != "pending" {
		t.Errorf("operation after = %s", logs[0].After)
	}
	// 取り消しの前に得た版を添えた承認の ② は conflict
	_, _, err := f.s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: formatOperationID(f.opID), ExpectedVersion: before.opVersion, ExpectedChallengeVersion: before.chVersion,
		Decision: ApprovalDecisionApproved, HeadSHA: "head1"}, verifiedAttestationForTest(t, formatOperationID(f.opID)))
	if !errors.Is(err, ErrConflict) {
		t.Errorf("stale approval after cancel: err = %v, want ErrConflict", err)
	}
	// 新しい版で承認し直せる。
	prev, err := f.s.PrepareOperationApproval(context.Background(), formatOperationID(f.opID))
	if err != nil {
		t.Fatalf("PrepareOperationApproval() error = %v", err)
	}
	if _, _, err := f.s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: prev.OperationID, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion,
		Decision: ApprovalDecisionApproved, HeadSHA: "head1"}, verifiedAttestationForTest(t, prev.OperationID)); err != nil {
		t.Errorf("re-approval error = %v", err)
	}
}

func TestWaitingResults_WriteOnlyAttemptRow_NoLog(t *testing.T) {
	f := newRelFixture(t)
	f.fake.pr.Checks = []UpstreamCheck{{Status: "queued"}}
	for i := 0; i < 4; i++ {
		item := f.run(t)
		if item.Result != ExecutionResultWaitingChecks || item.ConsecutiveFailures != 0 {
			t.Fatalf("item = %+v", item)
		}
	}
	f.fake.getErr = errors.New("x")
	for i := 0; i < 4; i++ {
		if item := f.run(t); item.Result != ExecutionResultUpstreamFailed || item.ConsecutiveFailures != 0 {
			t.Fatalf("item = %+v", item)
		}
	}
	if st := f.state(t); st.opState != "approved" {
		t.Errorf("state = %s", st.opState)
	}
	if len(f.opExecuteLogs(t)) != 0 || len(f.attempts(t)) != 8 {
		t.Errorf("logs = %d attempts = %d", len(f.opExecuteLogs(t)), len(f.attempts(t)))
	}
}

func TestFailureLimit_ReachedCancelsWithLimitReached(t *testing.T) {
	f := newRelFixture(t)
	f.limit = 2
	f.fake.pr.Checks = []UpstreamCheck{{Status: "completed", Conclusion: "failure"}}
	if item := f.run(t); item.Result != ExecutionResultChecksFailed || item.ConsecutiveFailures != 1 {
		t.Fatalf("1st = %+v", item)
	}
	if len(f.opExecuteLogs(t)) != 0 {
		t.Error("counted wait must not log")
	}
	f.fake.pr.Checks = nil
	f.fake.mergeErr = errors.New("rejected") // 種類が違っても連続
	item := f.run(t)
	if item.Result != ExecutionResultLimitReached || item.ConsecutiveFailures != 0 {
		t.Fatalf("2nd = %+v", item)
	}
	at := f.attempts(t)
	if at[1].Detail != string(ExecutionResultMergeFailed) {
		t.Errorf("detail = %q, want original result", at[1].Detail)
	}
	if st := f.state(t); st.opState != "pending" {
		t.Errorf("state = %s", st.opState)
	}
	if len(f.opExecuteLogs(t)) != 2 {
		t.Errorf("logs = %d, want 2", len(f.opExecuteLogs(t)))
	}
}

func TestConsecutiveFailures_WaitingInBetweenAndInterruptedDoNotBreakOrCount(t *testing.T) {
	f := newRelFixture(t)
	f.limit = 3
	f.fake.pr.Checks = []UpstreamCheck{{Status: "completed", Conclusion: "failure"}}
	f.run(t) // checks_failed
	f.fake.pr.Checks = []UpstreamCheck{{Status: "queued"}}
	f.run(t) // waiting_checks
	// interrupted の試行（回収で閉じる）
	coretest.InsertOperationExecution(t, f.s.workspace, f.opID, coretest.ExecutionAttempt{StartedAt: formatTimestamp(f.now), Host: "other", PID: 9})
	f.now = f.now.Add(16 * time.Minute)
	f.fake.pr.Checks = nil
	v := false
	f.fake.pr.Mergeable = &v
	item := f.run(t) // interrupted を閉じてから not_mergeable
	if item.Result != ExecutionResultNotMergeable || item.ConsecutiveFailures != 2 {
		t.Fatalf("item = %+v", item)
	}
}

func TestReapprovalResetsConsecutiveFailures(t *testing.T) {
	f := newRelFixture(t)
	f.limit = 5
	f.fake.pr.Checks = []UpstreamCheck{{Status: "completed", Conclusion: "failure"}}
	f.run(t)
	f.fake.pr.HeadSHA = "zzz"
	f.run(t) // head_changed → pending
	// 実際の承認し直しの経路（取り消しの後の approve <OP-ID>）で approved に戻す。
	prev, err := f.s.PrepareOperationApproval(context.Background(), formatOperationID(f.opID))
	if err != nil {
		t.Fatalf("PrepareOperationApproval() error = %v", err)
	}
	if _, _, err := f.s.ExecuteOperationApproval(context.Background(), OperationApprovalRequest{
		OperationID: prev.OperationID, ExpectedVersion: prev.Version, ExpectedChallengeVersion: prev.ChallengeVersion,
		Decision: ApprovalDecisionApproved, HeadSHA: relHead}, verifiedAttestationForTest(t, prev.OperationID)); err != nil {
		t.Fatalf("re-approval error = %v", err)
	}
	f.fake.pr.HeadSHA = relHead
	if item := f.run(t); item.ConsecutiveFailures != 1 {
		t.Errorf("item = %+v, want counting from 0 again", item)
	}
}

// --- 対象外・回収 ---

func TestNotExecutable_NotReleaseOrNotApproved(t *testing.T) {
	f := newRelFixture(t)
	other := coretest.InsertReleaseOperation(t, f.s.workspace, int(f.cid), relRef, "pending")
	exec := coretest.InsertReleaseOperation(t, f.s.workspace, int(f.cid), relRef, "executed")
	rej := coretest.InsertReleaseOperation(t, f.s.workspace, int(f.cid), relRef, "rejected")
	var del int64
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		r, err := tx.Exec(`INSERT INTO operation (challenge_id, kind, summary, ref, state, version, created_at) VALUES (?, 'delete', 's', 'x', 'approved', 2, ?)`, f.cid, execBase)
		if err != nil {
			return err
		}
		del, _ = r.LastInsertId()
		return nil
	})
	ids := []string{formatOperationID(other), formatOperationID(exec), formatOperationID(rej), formatOperationID(del)}
	res, err := f.s.ExecuteReleases(context.Background(), ids, f.opts())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 0 || len(res.NotStarted) != 4 || f.fake.getCalls != 0 || len(f.fake.mergeArgs) != 0 {
		t.Fatalf("res = %+v calls = %d", res, f.fake.getCalls)
	}
	want := []string{"kind=release state=pending", "kind=release state=executed", "kind=release state=rejected", "kind=delete state=approved"}
	for i, ns := range res.NotStarted {
		if ns.Reason != NotStartedNotExecutable || ns.Detail != want[i] || ns.OperationID != ids[i] || ns.ChallengeID != formatChallengeID(f.cid) {
			t.Errorf("NotStarted[%d] = %+v", i, ns)
		}
	}
	if n := len(f.attempts(t)); n != 0 {
		t.Errorf("attempts = %d", n)
	}
}

// AC-60: 承認済みでも release 以外（external_send・other）は実行せず、操作の state と version を変えない。
func TestNotExecutable_ApprovedNonReleaseKindsAreLeftUntouched(t *testing.T) {
	f := newRelFixture(t)
	ids := map[string]int64{}
	for _, kind := range []string{"external_send", "other"} {
		writeForTest(t, f.s, func(tx *sql.Tx) error {
			r, err := tx.Exec(`INSERT INTO operation (challenge_id, kind, summary, ref, state, version, created_at) VALUES (?, ?, 's', 'x', 'approved', 2, ?)`, f.cid, kind, execBase)
			if err != nil {
				return err
			}
			ids[kind], err = r.LastInsertId()
			return err
		})
	}
	for _, kind := range []string{"external_send", "other"} {
		res, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(ids[kind])}, f.opts())
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Items) != 0 || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedNotExecutable || res.NotStarted[0].Detail != "kind="+kind+" state=approved" {
			t.Errorf("%s: res = %+v", kind, res)
		}
		var state string
		var version int
		readForTest(t, f.s, func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT state, version FROM operation WHERE id = ?`, ids[kind]).Scan(&state, &version)
		})
		if state != "approved" || version != 2 {
			t.Errorf("%s: state/version = %s/%d, want approved/2 (unchanged)", kind, state, version)
		}
	}
	if f.fake.getCalls != 0 || len(f.fake.mergeArgs) != 0 {
		t.Errorf("external calls: get %d merge %v", f.fake.getCalls, f.fake.mergeArgs)
	}
}

func TestUnknownOperationID_NotFound_NothingExecuted(t *testing.T) {
	f := newRelFixture(t)
	for _, id := range []string{"OP-999", "bogus"} {
		_, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID), id}, f.opts())
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", id, err)
		}
	}
	if len(f.attempts(t)) != 0 || f.fake.getCalls != 0 {
		t.Error("nothing should run")
	}
}

func TestExecuteReleases_WithoutIDs_RunsApprovedReleasesInIDOrder(t *testing.T) {
	f := newRelFixture(t)
	second := coretest.InsertApprovedRelease(t, f.s.workspace, int(f.cid), relRef, relHead, execTS(1))
	coretest.InsertReleaseOperation(t, f.s.workspace, int(f.cid), relRef, "pending")
	res, err := f.s.ExecuteReleases(context.Background(), nil, f.opts())
	if err != nil || len(res.Items) != 2 {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if res.Items[0].OperationID != formatOperationID(f.opID) || res.Items[1].OperationID != formatOperationID(second) {
		t.Errorf("order = %v, %v", res.Items[0].OperationID, res.Items[1].OperationID)
	}
}

func TestExecuteReleases_RequiresDependencies(t *testing.T) {
	f := newRelFixture(t)
	o := f.opts()
	o.Merger = nil
	if _, err := f.s.ExecuteReleases(context.Background(), nil, o); !errors.Is(err, ErrValidation) {
		t.Errorf("err = %v", err)
	}
}

func TestReap_OpenAttempt(t *testing.T) {
	host, _ := os.Hostname()
	deadPID := int64(2147480000)
	cases := []struct {
		name      string
		host      string
		pid       int64
		age       time.Duration
		wantReap  bool
		wantStart bool
	}{
		{"same host dead pid", host, deadPID, time.Second, true, true},
		{"same host live pid, within 15m", host, int64(os.Getpid()), 14 * time.Minute, false, false},
		{"same host live pid, over 15m", host, int64(os.Getpid()), 15*time.Minute + time.Second, true, true},
		{"other host exactly 15m", "elsewhere", 1, 15 * time.Minute, false, false},
		{"other host within 15m", "elsewhere", 1, time.Minute, false, false},
		{"other host over 15m", "elsewhere", 1, 15*time.Minute + time.Second, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newRelFixture(t)
			started := execTime(0)
			f.now = started.Add(c.age)
			openID := coretest.InsertOperationExecution(t, f.s.workspace, f.opID, coretest.ExecutionAttempt{StartedAt: formatTimestamp(started), Host: c.host, PID: c.pid})
			res, err := f.s.ExecuteReleases(context.Background(), nil, f.opts())
			if err != nil {
				t.Fatal(err)
			}
			at := f.attempts(t)
			if c.wantReap {
				if at[0].Result != ExecutionResultInterrupted || at[0].open() {
					t.Errorf("first attempt = %+v, want interrupted", at[0])
				}
				if len(res.Items) != 1 || len(at) != 2 {
					t.Errorf("items = %+v attempts = %d", res.Items, len(at))
				}
			} else {
				if !at[0].open() || len(res.Items) != 0 || len(res.NotStarted) != 1 {
					t.Fatalf("attempt open=%v res = %+v", at[0].open(), res)
				}
				ns := res.NotStarted[0]
				if ns.Reason != NotStartedInProgress || ns.Detail != itoa(openID) || ns.OperationID != formatOperationID(f.opID) {
					t.Errorf("ns = %+v", ns)
				}
				if f.fake.getCalls != 0 {
					t.Error("in_progress must not call out")
				}
			}
			if n := len(f.opExecuteLogs(t)); n != 0 {
				t.Errorf("interrupted/unconfirmed must not log, got %d", n)
			}
		})
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestReap_ThenAlreadyMerged(t *testing.T) {
	f := newRelFixture(t)
	coretest.InsertOperationExecution(t, f.s.workspace, f.opID, coretest.ExecutionAttempt{StartedAt: formatTimestamp(execTime(0)), Host: "elsewhere", PID: 1})
	f.now = execTime(0).Add(16 * time.Minute)
	f.fake.pr = UpstreamPullRequestChecks{State: "merged", HeadSHA: relHead, MergeCommitSHA: "mc"}
	item := f.run(t)
	if item.Result != ExecutionResultAlreadyMerged {
		t.Fatalf("item = %+v", item)
	}
}

func TestCycleTriggerRecordedInLog(t *testing.T) {
	f := newRelFixture(t)
	f.fake.pr.HeadSHA = "zzz"
	o := f.opts()
	o.Trigger = ReleaseExecTriggerCycle
	if _, err := f.s.ExecuteReleases(context.Background(), nil, o); err != nil {
		t.Fatal(err)
	}
	logs := f.opExecuteLogs(t)
	if len(logs) != 2 || relAfter(t, logs[0])["trigger"] != "cycle" || logs[0].Channel != "cli" {
		t.Errorf("logs = %+v", logs)
	}
}

func TestWriteFailure_RollsBackEverything(t *testing.T) {
	f := newRelFixture(t)
	f.fake.pr.HeadSHA = "zzz"
	// 課題の版を実行中に変えて楽観ロックを失敗させる代わりに、書き込みの関数を直接失敗させる。
	err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		rec := &activityRecorder{tx: tx, at: execTime(1), actor: "a", channel: ChannelCLI, verification: VerificationNone, insert: func(*sql.Tx, activityRow) error { return errors.New("log fail") }}
		att, err := beginOperationExecution(context.Background(), tx, f.opID, beginOperationExecutionInput{StartedAt: execTime(1), ApprovedHead: relHead, PID: 1, Host: "h"})
		if err != nil {
			return err
		}
		return f.s.cancelReleaseInTx(context.Background(), tx, rec, releaseOutcomeWrite{opID: f.opID, challengeID: f.cid, attemptID: att.ID, version: 2, result: ExecutionResultHeadChanged, trigger: "op exec", at: execTime(2)})
	})
	if err == nil {
		t.Fatal("want error")
	}
	if st := f.state(t); st.opState != "approved" || st.opVersion != 2 {
		t.Errorf("state = %+v, want untouched", st)
	}
	if len(f.attempts(t)) != 0 {
		t.Error("attempt row must roll back too")
	}
}

func countAction(as []Activity, action string) int {
	n := 0
	for _, a := range as {
		if a.Action == action {
			n++
		}
	}
	return n
}

// AC-125: 試行を始めた後に別の呼び出しが操作を変えたとき、not_started の in_progress の detail は
// 終わっていない試行の ID（固定の文字列ではない）である。
func TestExecuteReleases_ChangedAfterAttemptStartedReportsTheAttemptID(t *testing.T) {
	f := newRelFixture(t)
	// マージの依頼が失敗した（依頼していない）場合は、従来どおり not_started になる（M45H18）。
	f.fake.mergeErr = errors.New("merge refused")
	f.fake.onMerge = func() { coretest.SetOperationState(t, f.s.workspace, f.opID, "pending") }
	res, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, f.opts())
	if err != nil {
		t.Fatalf("ExecuteReleases() error = %v", err)
	}
	at := f.attempts(t)
	if len(at) != 1 {
		t.Fatalf("attempts = %+v, want 1", at)
	}
	if len(res.Items) != 0 || len(res.NotStarted) != 1 {
		t.Fatalf("Items = %+v, NotStarted = %+v, want 1 not_started", res.Items, res.NotStarted)
	}
	ns := res.NotStarted[0]
	if ns.Reason != NotStartedInProgress || ns.Detail != fmt.Sprintf("%d", at[0].ID) || ns.OperationID != formatOperationID(f.opID) {
		t.Errorf("not_started = %+v, want in_progress with detail %d", ns, at[0].ID)
	}
	if at[0].open() {
		t.Errorf("attempt %+v is still open, want closed", at[0])
	}
}

// M45H18: マージを依頼した後に試行の記録が衝突しても、release は merge_unconfirmed として出力に残る。
func TestExecuteReleases_ConflictAfterMergeRequestIsMergeUnconfirmed(t *testing.T) {
	f := newRelFixture(t)
	f.fake.afterPR = &UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: relHead, MergeCommitSHA: "m1"}
	f.fake.onMerge = func() { coretest.SetOperationState(t, f.s.workspace, f.opID, "rejected") }
	res, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, f.opts())
	if err != nil {
		t.Fatalf("ExecuteReleases() error = %v", err)
	}
	if len(res.NotStarted) != 0 || len(res.Items) != 1 {
		t.Fatalf("Items = %+v, NotStarted = %+v, want 1 item and no not_started", res.Items, res.NotStarted)
	}
	it := res.Items[0]
	if it.Result != ExecutionResultMergeUnconfirmed || it.OperationID != formatOperationID(f.opID) || it.Detail == "" {
		t.Errorf("item = %+v, want merge_unconfirmed with a detail", it)
	}
}

// #344: マージの依頼の後に release が pending へ取り消されていてマージ済みなら、merged_unapproved として
// 記録し、status の食い違いに出し、承認し直しを拒む。
func TestExecuteReleases_WithdrawnThenMerged_RecordsMergedUnapproved(t *testing.T) {
	f := newRelFixture(t)
	f.fake.afterPR = &UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: relHead, MergeCommitSHA: "m1"}
	f.fake.onMerge = func() { coretest.SetOperationState(t, f.s.workspace, f.opID, "pending") }
	res, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, f.opts())
	if err != nil {
		t.Fatalf("ExecuteReleases() error = %v", err)
	}
	if len(res.NotStarted) != 0 || len(res.Items) != 1 || res.Items[0].Result != ExecutionResultMergedUnapproved {
		t.Fatalf("Items = %+v, NotStarted = %+v, want 1 merged_unapproved item", res.Items, res.NotStarted)
	}
	at := f.attempts(t)
	if last := at[len(at)-1]; last.Result != ExecutionResultMergedUnapproved || last.open() {
		t.Fatalf("last attempt = %+v, want closed merged_unapproved", last)
	}
	ov, err := f.s.GetOverview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range ov.Discrepancies {
		if d.OperationID == formatOperationID(f.opID) {
			for _, k := range d.Kinds {
				found = found || k == DiscrepancyKindMergedUnapproved
			}
		}
	}
	if !found {
		t.Errorf("discrepancies = %+v, want merged_unapproved for the release", ov.Discrepancies)
	}
	forbidden := false
	readForTest(t, f.s, func(tx *sql.Tx) error {
		var e error
		forbidden, e = reapprovalForbidden(context.Background(), tx, f.opID)
		return e
	})
	if !forbidden {
		t.Error("reapproval must be forbidden")
	}
}

// #344: 依頼の後の読み取りが失敗しても、取り消された後の読み取りでマージ済みと分かれば merged_unapproved。
// 依頼した試行そのものを閉じ、試行は 1 行のまま。
func TestExecuteReleases_WithdrawnThenMergedSeenOnSecondRead(t *testing.T) {
	f := newRelFixture(t)
	f.fake.onMerge = func() {
		coretest.SetOperationState(t, f.s.workspace, f.opID, "pending")
		f.fake.pr = UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: relHead, MergeCommitSHA: "m1"}
	}
	res, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, f.opts())
	if err != nil {
		t.Fatalf("ExecuteReleases() error = %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Result != ExecutionResultMergedUnapproved {
		t.Fatalf("Items = %+v, want merged_unapproved", res.Items)
	}
	at := f.attempts(t)
	if len(at) != 1 || at[0].Result != ExecutionResultMergedUnapproved {
		t.Errorf("attempts = %+v, want the single attempt closed as merged_unapproved", at)
	}
}

// #344: 取り消された後で、マージ済みと確かめられないなら記録を足さず merge_unconfirmed のまま。
func TestExecuteReleases_WithdrawnAndNotMerged_StaysMergeUnconfirmed(t *testing.T) {
	f := newRelFixture(t)
	f.fake.onMerge = func() { coretest.SetOperationState(t, f.s.workspace, f.opID, "pending") }
	res, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, f.opts())
	if err != nil {
		t.Fatalf("ExecuteReleases() error = %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Result != ExecutionResultMergeUnconfirmed {
		t.Fatalf("Items = %+v, want merge_unconfirmed", res.Items)
	}
	for _, a := range f.attempts(t) {
		if a.Result == ExecutionResultMergedUnapproved {
			t.Errorf("attempt %+v must not be merged_unapproved", a)
		}
	}
}

// #344: 依頼の後の読み取りではマージ済みと見えず（merge_unconfirmed）、取り消された後の追加の読み取り
// 1 回でマージ済みと分かれば、merged_unapproved の試行が 1 行書かれる。
func TestExecuteReleases_WithdrawnThenMergedSeenOnlyByAdditionalRead(t *testing.T) {
	f := newRelFixture(t)
	merged := UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: relHead, MergeCommitSHA: "m1"}
	f.fake.prSeq = []UpstreamPullRequestChecks{openPR(), openPR(), merged}
	f.fake.onMerge = func() { coretest.SetOperationState(t, f.s.workspace, f.opID, "pending") }
	res, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, f.opts())
	if err != nil {
		t.Fatalf("ExecuteReleases() error = %v", err)
	}
	if f.fake.getCalls != 3 {
		t.Errorf("GetPullRequestChecks calls = %d, want 3 (head check, after merge, additional read)", f.fake.getCalls)
	}
	if len(res.NotStarted) != 0 || len(res.Items) != 1 || res.Items[0].Result != ExecutionResultMergedUnapproved {
		t.Fatalf("Items = %+v, NotStarted = %+v, want 1 merged_unapproved item", res.Items, res.NotStarted)
	}
	n := 0
	for _, a := range f.attempts(t) {
		if a.Result == ExecutionResultMergedUnapproved {
			n++
			if a.open() || a.ObservedHead != relHead {
				t.Errorf("attempt %+v, want closed with observed head %s", a, relHead)
			}
		}
	}
	if n != 1 {
		t.Errorf("merged_unapproved attempts = %d, want 1", n)
	}
}

// #344: 取り消された後の追加の読み取りでもマージ済みと分からなければ、記録を足さず merge_unconfirmed のまま。
func TestExecuteReleases_WithdrawnAndAdditionalReadNotMerged_StaysMergeUnconfirmed(t *testing.T) {
	f := newRelFixture(t)
	f.fake.prSeq = []UpstreamPullRequestChecks{openPR()}
	f.fake.onMerge = func() { coretest.SetOperationState(t, f.s.workspace, f.opID, "pending") }
	res, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, f.opts())
	if err != nil {
		t.Fatalf("ExecuteReleases() error = %v", err)
	}
	if f.fake.getCalls != 3 {
		t.Errorf("GetPullRequestChecks calls = %d, want 3 (head check, after merge, additional read)", f.fake.getCalls)
	}
	if len(res.Items) != 1 || res.Items[0].Result != ExecutionResultMergeUnconfirmed {
		t.Fatalf("Items = %+v, want merge_unconfirmed", res.Items)
	}
	for _, a := range f.attempts(t) {
		if a.Result == ExecutionResultMergedUnapproved {
			t.Errorf("attempt %+v must not be merged_unapproved", a)
		}
	}
}

// M45H18: 別の実行の口が試行を閉じ、release は approved のまま残る衝突では、items に merge_unconfirmed で
// 載り、release は approved のまま、merged_unapproved は足されない。
func TestExecuteReleases_AttemptClosedByOtherExecutorStaysApprovedMergeUnconfirmed(t *testing.T) {
	f := newRelFixture(t)
	f.fake.afterPR = &UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: relHead, MergeCommitSHA: "m1"}
	f.fake.onMerge = func() {
		writeForTest(t, f.s, func(tx *sql.Tx) error {
			last, err := latestOperationExecution(context.Background(), tx, f.opID)
			if err != nil {
				return err
			}
			return closeOperationExecution(context.Background(), tx, last.ID, closeOperationExecutionInput{EndedAt: f.now, Result: ExecutionResultInterrupted, Detail: "closed by another executor"})
		})
	}
	res, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, f.opts())
	if err != nil {
		t.Fatalf("ExecuteReleases() error = %v", err)
	}
	if len(res.NotStarted) != 0 || len(res.Items) != 1 || res.Items[0].Result != ExecutionResultMergeUnconfirmed {
		t.Fatalf("Items = %+v, NotStarted = %+v, want 1 merge_unconfirmed item", res.Items, res.NotStarted)
	}
	if st := f.state(t); st.opState != string(OperationStateApproved) {
		t.Errorf("operation state = %q, want approved", st.opState)
	}
	for _, a := range f.attempts(t) {
		if a.Result == ExecutionResultMergedUnapproved {
			t.Errorf("attempt %+v must not be merged_unapproved", a)
		}
	}
}

// M45H18: マージの依頼の前（先頭の読み取りで merged_unapproved）の衝突は not_started のまま。
func TestExecuteReleases_ConflictBeforeMergeRequestStaysNotStarted(t *testing.T) {
	f := newRelFixture(t)
	f.fake.pr = UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: "other", MergeCommitSHA: "m1"}
	f.fake.onGet = func() { coretest.SetOperationState(t, f.s.workspace, f.opID, "pending") }
	res, err := f.s.ExecuteReleases(context.Background(), []string{formatOperationID(f.opID)}, f.opts())
	if err != nil {
		t.Fatalf("ExecuteReleases() error = %v", err)
	}
	if len(res.Items) != 0 || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedInProgress {
		t.Fatalf("Items = %+v, NotStarted = %+v, want 1 in_progress not_started", res.Items, res.NotStarted)
	}
	if len(f.fake.mergeArgs) != 0 {
		t.Errorf("merge must not be requested: %v", f.fake.mergeArgs)
	}
}

// 2 件目でエラーになっても、1 件目の実行済みの結果は ExecuteReleases の戻り値に残る（op exec が出力に残す）。
func TestExecuteReleases_ErrorOnALaterReleaseKeepsThePartialResult(t *testing.T) {
	f := newRelFixture(t)
	f.fake.afterPR = &UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: relHead, MergeCommitSHA: "m1"}
	c2 := createAndAdvance(t, f.s, StatusDone)
	coretest.InsertApprovedRelease(t, f.s.workspace, int(mustParseChallengeID(t, c2.ID)), relRef, relHead, execTS(0))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := f.opts()
	o.Checks = &cancelOnGet{fakeRelease: f.fake, at: 3, cancel: cancel}
	res, err := f.s.ExecuteReleases(ctx, nil, o)
	if err == nil {
		t.Fatalf("ExecuteReleases() error = nil, want an error from the cancelled context (res %+v)", res)
	}
	if res == nil || len(res.Items) != 1 || res.Items[0].Result != ExecutionResultMerged {
		t.Errorf("partial result = %+v, want the first release merged", res)
	}
}

// cancelOnGet は at 回目の GetPullRequestChecks の呼び出しで ctx を取り消す（2 件目の release の途中で
// 書き込みを失敗させるため）。
type cancelOnGet struct {
	*fakeRelease
	at     int
	n      int
	cancel context.CancelFunc
}

func (c *cancelOnGet) GetPullRequestChecks(ctx context.Context, repo string, n int) (UpstreamPullRequestChecks, error) {
	c.n++
	if c.n == c.at {
		c.cancel()
	}
	return c.fakeRelease.GetPullRequestChecks(ctx, repo, n)
}
