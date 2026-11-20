package core

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// 反映の段: 反映の依頼（契約の実行の kind が reflect）の起動・続き・取り消しを、実ストア・偽の ContractClient・偽の PR の取得で固定する。
// 受入基準 1 つにつき 1 つの検証（名前に番号を付ける）。

const reflectConnectorsJSON = `{
  "version": 1,
  "human_question_kinds": [],
  "connectors": [
    {"id": "tool", "form": "cli", "permission_mode": "default",
     "commands": {
       "start": ["tool", "start", "--challenge", "{challenge_id}"],
       "status": ["tool", "status", "{run_id}", "--issue", "{issue_number}"],
       "resume": ["tool", "resume", "{run_id}", "--choice", "{choice}", "--note", "{note}", "--approved-ref", "{approved_ref}"],
       "cancel": ["tool", "cancel", "{run_id}"],
       "reflect": ["tool", "reflect", "--artifact", "{artifact_ref}", "--challenge", "{challenge_id}"]
     },
     "contract_version": 1,
     "operations": [{"id": "impl", "interactive": false, "child_may_decide": true, "artifacts": "pr"}]}
  ],
  "repos": [
    {"name": "r", "remote": "o/r", "default_branch": "main", "connector": "tool", "slots": {"provider": "worktree", "base": "wt", "count": 2}}
  ]
}`

type reflFixture struct {
	*relFixture
	conn    *ConnectorsDeclaration
	client  *fakeContractClient
	agent   *AgentDeclaration
	cmp     *fakeCompare
	cycleID string
}

func newReflFixture(t *testing.T) *reflFixture {
	t.Helper()
	f := newRelFixture(t)
	conn, err := parseConnectorsDeclaration([]byte(reflectConnectorsJSON))
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.validate(); err != nil {
		t.Fatal(err)
	}
	return &reflFixture{
		relFixture: f, conn: conn, cmp: newFakeCompare(),
		client: &fakeContractClient{
			startOut: contractOut(ContractStateRunning, nil, nil), last: contractOut(ContractStateRunning, nil, nil),
			resumeOut: contractOut(ContractStateRunning, nil, nil), cancelOut: contractOut(ContractStateCancelled, nil, nil),
		},
		agent: &AgentDeclaration{FailureLimit: 3, MaxParallelRuns: 2, MaxRunBudgetUSD: 5, TimeoutSec: AgentTimeoutSec{Delegate: 60}},
	}
}

func (f *reflFixture) opts() ReleaseExecOptions {
	return ReleaseExecOptions{
		Checks: f.fake, Compare: f.cmp, Connectors: f.conn, Agent: f.agent, Contract: f.client,
		Trigger: ReleaseExecTriggerOpExec, CycleID: f.cycleID, Now: func() time.Time { return f.now },
	}
}

func (f *reflFixture) execIDs(t *testing.T, ids ...string) *ReleaseExecResult {
	t.Helper()
	res, err := f.s.ExecuteReleases(context.Background(), ids, f.opts())
	if err != nil {
		t.Fatalf("ExecuteReleases() error = %v", err)
	}
	f.now = f.now.Add(time.Second)
	return res
}

func (f *reflFixture) exec(t *testing.T) *ReleaseExecResult {
	t.Helper()
	return f.execIDs(t, formatOperationID(f.opID))
}

func (f *reflFixture) one(t *testing.T) ReleaseExecItem {
	t.Helper()
	res := f.exec(t)
	if len(res.Items) != 1 {
		t.Fatalf("Items = %+v, NotStarted = %+v, want 1 item", res.Items, res.NotStarted)
	}
	return res.Items[0]
}

// started は反映の依頼を始める（結果 reflect_started）。
func (f *reflFixture) started(t *testing.T) {
	t.Helper()
	if item := f.one(t); item.Result != ExecutionResultReflectStarted {
		t.Fatalf("item = %+v, want reflect_started", item)
	}
}

func (f *reflFixture) queue(outs ...ContractLaunchOutput) { f.client.queue = outs }

type reflRow struct {
	kind      string
	opID      sql.NullInt64
	taskID    sql.NullInt64
	state     string
	landRef   string
	delivered bool
	ended     bool
	costUSD   int64
}

func (f *reflFixture) rows(t *testing.T) []reflRow {
	t.Helper()
	var out []reflRow
	readForTest(t, f.s, func(tx *sql.Tx) error {
		rs, err := tx.Query(`SELECT kind, operation_id, task_id, state, COALESCE(land_ref, ''), land_delivered, ended_at IS NOT NULL, cost_usd FROM contract_run ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var r reflRow
			if err := rs.Scan(&r.kind, &r.opID, &r.taskID, &r.state, &r.landRef, &r.delivered, &r.ended, &r.costUSD); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rs.Err()
	})
	return out
}

func (f *reflFixture) only(t *testing.T) reflRow {
	t.Helper()
	rows := f.rows(t)
	if len(rows) != 1 {
		t.Fatalf("contract_run rows = %+v, want exactly 1", rows)
	}
	return rows[0]
}

func (f *reflFixture) lastDetail(t *testing.T) string {
	t.Helper()
	var d string
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COALESCE(detail, '') FROM operation_execution WHERE operation_id = ? ORDER BY id DESC LIMIT 1`, f.opID).Scan(&d)
	})
	return d
}

func (f *reflFixture) wantOpState(t *testing.T, want string) {
	t.Helper()
	if got := f.state(t).opState; got != want {
		t.Fatalf("operation state = %s, want %s", got, want)
	}
}

func (f *reflFixture) wantNotStarted(t *testing.T, res *ReleaseExecResult, reason NotStartedReason) {
	t.Helper()
	if len(res.Items) != 0 || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != reason || res.NotStarted[0].OperationID != formatOperationID(f.opID) {
		t.Fatalf("result = %+v, want only not_started %s", res, reason)
	}
}

// moved は PR の head を h にし、承認した中身から h への変わり方が反映先の最新の取り込みだけになるよう比較を用意する。
func (f *reflFixture) movedByMergeIn(h string) {
	f.fake.pr.HeadSHA = h
	f.cmp.set(relHead, h, withFiles(ahead(commit("m1", "main", "m0"), commit(h, "Merge", relHead, "m1")), UpstreamFile{Filename: "a", Patch: "p"}))
	f.cmp.set("main", h, ahead(commit(relHead, "fix", "p0"), commit(h, "Merge", relHead, "m1")))
	f.cmp.set("m1", h, ahead(commit(relHead, "fix", "p0")))
}

// reviewWith は、options と commit の識別を指定したレビュー待ち。
func reviewWith(options []string, commits ...string) ContractLaunchOutput {
	return contractOut(ContractStateWaiting, nil, func(st *ContractStatus) {
		st.RequestedAction = &ContractRequestedAction{Kind: "review", Decider: "any", Text: "レビュー待ち", Options: options}
		st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: relRef}}
		for _, c := range commits {
			st.Artifacts = append(st.Artifacts, ContractArtifact{Kind: "commit", Ref: c})
		}
	})
}

// sendLand は反映の依頼を始め、承認した中身で「入れてよい」を返す（結果 land_sent）。
func (f *reflFixture) sendLand(t *testing.T) {
	t.Helper()
	f.started(t)
	f.queue(reviewFor(relHead))
	if item := f.one(t); item.Result != ExecutionResultLandSent {
		t.Fatalf("item = %+v, want land_sent", item)
	}
}

// insertOpenContractRun は別の課題の、終わっていない契約の実行を足す（同時に走らせる数に数えられる）。
func insertOpenContractRun(t *testing.T, s *Store, cid int64, external string) {
	t.Helper()
	writeForTest(t, s, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO run (challenge_id, kind, challenge_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket)
			VALUES (?, 'contract', 1, 's', 1, 'h', 'x', 'x', 'x', 'succeeded', 1000000, 'impl')`, cid)
		if err != nil {
			return err
		}
		runID, _ := res.LastInsertId()
		_, err = tx.Exec(`INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, max_budget_usd, started_at, observed_at)
			VALUES (?, 'tool', ?, ?, 'running', 's', 1000000, 'x', 'x')`, cid, external, runID)
		return err
	})
}

func (f *reflFixture) secondRelease(t *testing.T, pr string) (cid, opID int64) {
	t.Helper()
	c := createAndAdvance(t, f.s, StatusDone)
	cid = mustParseChallengeID(t, c.ID)
	return cid, coretest.InsertApprovedRelease(t, f.s.workspace, int(cid), pr, relHead, execTS(0))
}

// ---- 起動 ----

func TestReflect_AC219_StartsCommandsReflectWhenTheChallengeHasNoUnfinishedRun(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	if len(f.client.starts) != 1 || f.client.starts[0][1] != "reflect" {
		t.Fatalf("starts = %v, want one commands.reflect call", f.client.starts)
	}
}

func TestReflect_AC220_ArtifactRefIsTheReleaseRef(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	if want := []string{"tool", "reflect", "--artifact", relRef, "--challenge", formatChallengeID(f.cid)}; !reflect.DeepEqual(f.client.starts[0], want) {
		t.Fatalf("command = %v, want %v ({artifact_ref} is the release ref)", f.client.starts[0], want)
	}
}

func TestReflect_AC221_ChallengeIDIsTheChallenge(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	if got := f.client.starts[0][5]; got != formatChallengeID(f.cid) {
		t.Fatalf("{challenge_id} = %q, want %q", got, formatChallengeID(f.cid))
	}
}

func TestReflect_AC222_WorkDirIsTheRepositoryContractWorkDir(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	if got, want := f.client.workDirs["start"], []string{filepath.Join(f.s.workspace, "wt")}; !reflect.DeepEqual(got, want) {
		t.Fatalf("work dirs = %v, want %v", got, want)
	}
}

func TestReflect_AC223_AC224_RunIDReturnedIsReflectStartedWithRouteReflectRequest(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	item := f.one(t)
	if item.Result != ExecutionResultReflectStarted {
		t.Fatalf("result = %s, want reflect_started (AC-223)", item.Result)
	}
	if item.Route != ReleaseRouteReflectRequest || ReleaseRouteReflectRequest != "reflect_request" {
		t.Fatalf("route = %q, want reflect_request (AC-224)", item.Route)
	}
}

func TestReflect_AC225_AC226_AC227_AC228_TheContractRunRecordIsAReflectRunning(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	r := f.only(t)
	if r.kind != "reflect" {
		t.Errorf("kind = %q, want reflect (AC-225)", r.kind)
	}
	if r.state != "running" || r.ended {
		t.Errorf("state = %q ended=%v, want running (AC-226)", r.state, r.ended)
	}
	if !r.opID.Valid || r.opID.Int64 != f.opID {
		t.Errorf("operation_id = %+v, want %d (AC-227)", r.opID, f.opID)
	}
	if r.taskID.Valid {
		t.Errorf("task_id = %+v, want NULL (AC-228)", r.taskID)
	}
}

func TestReflect_AC229_AC282_ADoneChallengeStartsAndTheOperationStaysApproved(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	if st := f.state(t); st.chStatus != "done" || len(f.client.starts) != 1 || st.opState != "approved" {
		t.Fatalf("challenge %s starts %d operation %s, want a done challenge started and still approved", st.chStatus, len(f.client.starts), st.opState)
	}
}

func TestReflect_AC230_AC231_AC232_AFailedReflectIsReflectFailedWithoutARunAndUnapproves(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.client.startOut = ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "boom"}
	item := f.one(t)
	if item.Result != ExecutionResultReflectFailed || item.Route != ReleaseRouteReflectRequest {
		t.Fatalf("item = %+v, want reflect_failed (AC-230)", item)
	}
	if rows := f.rows(t); len(rows) != 0 {
		t.Fatalf("contract_run rows = %+v, want none (AC-231)", rows)
	}
	f.wantOpState(t, "pending") // AC-232
}

func TestReflect_AC233_AC234_AC235_AC236_AC237_AtTheParallelLimitNothingIsStartedAndNoAttemptIsLeft(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.agent.MaxParallelRuns = 1
	other := createAndAdvance(t, f.s, StatusInProgress)
	insertOpenContractRun(t, f.s, mustParseChallengeID(t, other.ID), "other-1")
	res := f.exec(t)
	if len(f.client.starts) != 0 {
		t.Errorf("starts = %v, want none (AC-233)", f.client.starts)
	}
	f.wantNotStarted(t, res, NotStartedSlotUnavailable) // AC-234
	if at := f.attempts(t); len(at) != 0 {
		t.Errorf("attempts = %+v, want no row (AC-235)", at)
	}
	f.wantOpState(t, "approved") // AC-237
	// 枠が空いてから反映の段にかけ直す: 止まった試行として扱われず（interrupted が作られず）、反映の依頼が始まる。
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE contract_run SET ended_at = 'x', state = 'succeeded'`)
		return err
	})
	if item := f.one(t); item.Result != ExecutionResultReflectStarted {
		t.Fatalf("item = %+v, want reflect_started", item)
	}
	at := f.attempts(t)
	if len(at) != 1 || at[0].Result != ExecutionResultReflectStarted {
		t.Errorf("attempts = %+v, want exactly one reflect_started and no interrupted (AC-236)", at)
	}
}

func TestReflect_AC238_AC239_AC240_OverTheCycleBudgetNothingIsStarted(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	c, err := f.s.BeginCycle(context.Background(), BeginCycleInput{Trigger: "run", BudgetUSD: 1, Exclusive: false})
	if err != nil {
		t.Fatal(err)
	}
	f.cycleID = c.ID
	res := f.exec(t)
	if len(f.client.starts) != 0 {
		t.Errorf("starts = %v, want none (AC-238)", f.client.starts)
	}
	f.wantNotStarted(t, res, NotStartedCycleBudget) // AC-239
	if at := f.attempts(t); len(at) != 0 {
		t.Errorf("attempts = %+v, want no row (AC-240)", at)
	}
	if rows := f.rows(t); len(rows) != 0 {
		t.Errorf("contract_run rows = %+v, want none", rows)
	}
	f.wantOpState(t, "approved")
}

func TestReflect_AC241_AnUndecidableWorkDirIsUnsupportedWithoutCallingReflect(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.conn.Repos[0].Slots = ConnectorRepoSlots{Provider: "clone"}
	item := f.one(t)
	if len(f.client.starts) != 0 || item.Result != ExecutionResultUnsupported {
		t.Fatalf("starts %v item %+v, want unsupported without a call", f.client.starts, item)
	}
	if !strings.Contains(f.lastDetail(t), "working directory") {
		t.Fatalf("detail = %q, want the reason", f.lastDetail(t))
	}
}

func TestReflect_AC242_ARunningReflectRequestCountsTowardTheParallelLimit(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.agent.MaxParallelRuns = 1
	f.started(t)
	_, op2 := f.secondRelease(t, "https://github.com/o/r/pull/2")
	res := f.execIDs(t, formatOperationID(op2))
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedSlotUnavailable || len(f.client.starts) != 1 {
		t.Fatalf("result = %+v starts %d, want slot_unavailable by the running reflect request", res, len(f.client.starts))
	}
}

// ---- 続き ----

func TestReflect_AC243_StatusIsReadWithAnEmptySourcePlaceholder(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	f.exec(t)
	if want := []string{"tool", "status", "ext-1", "--issue", ""}; len(f.client.statuses) != 1 || !reflect.DeepEqual(f.client.statuses[0], want) {
		t.Fatalf("statuses = %v, want %v", f.client.statuses, want)
	}
}

func TestReflect_AC244_TheReleaseStageReadsStatusWhateverTheLastReadState(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	f.exec(t) // 最後に読んだ状態は running
	f.queue(reviewFor(relHead))
	f.exec(t) // 最後に読んだ状態は waiting
	f.exec(t)
	if len(f.client.statuses) != 3 {
		t.Fatalf("status calls = %d, want 3 (one per release stage)", len(f.client.statuses))
	}
}

func TestReflect_AC245_AC246_AC247_ALandReviewIsAnsweredWithLandAndTheApprovedRef(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	f.queue(reviewFor(relHead))
	item := f.one(t)
	want := []string{"tool", "resume", "ext-1", "--choice", "land", "--note", "", "--approved-ref", relHead}
	if len(f.client.resumes) != 1 || !reflect.DeepEqual(f.client.resumes[0], want) {
		t.Errorf("resumes = %v, want %v (AC-245)", f.client.resumes, want)
	}
	if item.Result != ExecutionResultLandSent || item.Route != ReleaseRouteReflectRequest {
		t.Errorf("item = %+v, want land_sent with route reflect_request (AC-246)", item)
	}
	if r := f.only(t); r.landRef != relHead || !r.delivered {
		t.Errorf("land_ref=%q delivered=%v, want %q and true (AC-247)", r.landRef, r.delivered, relHead)
	}
}

func TestReflect_AC248_AFailedLandResumeIsLandUnconfirmed(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	f.client.resumeOut = ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "boom"}
	f.queue(reviewFor(relHead))
	if item := f.one(t); item.Result != ExecutionResultLandUnconfirmed {
		t.Fatalf("item = %+v, want land_unconfirmed", item)
	}
}

func TestReflect_AC249_ASameContentReviewAfterADeliveredLandIsLandRefused(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.sendLand(t)
	f.queue(reviewFor(relHead))
	item := f.one(t)
	if item.Result != ExecutionResultLandRefused || item.Route != ReleaseRouteReflectRequest {
		t.Fatalf("item = %+v, want land_refused", item)
	}
	f.wantOpState(t, "pending")
}

func TestReflect_AC250_AMergeInOnlyChangeIsLandedWithTheCurrentContent(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	f.movedByMergeIn("h1")
	f.fake.pr.Checks = []UpstreamCheck{{Status: "completed", Conclusion: "success"}}
	f.queue(reviewFor("h1"))
	item := f.one(t)
	if item.Result != ExecutionResultLandSent || len(f.client.resumes) != 1 || f.client.resumes[0][len(f.client.resumes[0])-1] != "h1" {
		t.Fatalf("item = %+v resumes %v, want land_sent with {approved_ref} h1", item, f.client.resumes)
	}
}

func TestReflect_AC251_AC252_AnUnrelatedChangeIsHeadChangedAndTheRequestIsCancelled(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	f.fake.pr.HeadSHA = "unrelated"
	f.queue(reviewFor("unrelated"))
	if item := f.one(t); item.Result != ExecutionResultHeadChanged {
		t.Fatalf("item = %+v, want head_changed (AC-251)", item)
	}
	if len(f.client.cancels) != 1 || f.client.cancels[0][2] != "ext-1" || len(f.client.resumes) != 0 {
		t.Fatalf("cancels = %v resumes = %v, want one cancel of ext-1 (AC-252)", f.client.cancels, f.client.resumes)
	}
	if r := f.only(t); !r.ended {
		t.Fatalf("run = %+v, want ended by the cancel", r)
	}
}

func TestReflect_AC253_AC254_ReviewWithoutLandOptionIsUnsupportedAndCancelled(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	f.queue(reviewWith([]string{"respond"}, relHead))
	if item := f.one(t); item.Result != ExecutionResultUnsupported || len(f.client.resumes) != 0 {
		t.Fatalf("item = %+v resumes %v, want unsupported without resume (AC-253)", item, f.client.resumes)
	}
	if len(f.client.cancels) != 1 {
		t.Fatalf("cancels = %v, want one (AC-254)", f.client.cancels)
	}
}

func TestReflect_AC255_ReviewWithoutAContentRefIsUnsupported(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	f.queue(reviewWith([]string{"respond", "land"}))
	if item := f.one(t); item.Result != ExecutionResultUnsupported || len(f.client.resumes) != 0 {
		t.Fatalf("item = %+v resumes %v, want unsupported without resume", item, f.client.resumes)
	}
}

func TestReflect_AC256_ReviewWithTwoContentRefsIsUnsupported(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	f.queue(reviewWith([]string{"respond", "land"}, relHead, "other"))
	if item := f.one(t); item.Result != ExecutionResultUnsupported || len(f.client.resumes) != 0 {
		t.Fatalf("item = %+v resumes %v, want unsupported without resume", item, f.client.resumes)
	}
}

func (f *reflFixture) merged() {
	f.fake.pr = UpstreamPullRequestChecks{URL: relRef, State: "merged", Base: "main", HeadSHA: relHead, MergeCommitSHA: "upstream-mc"}
}

func TestReflect_AC257_AC258_AC259_SucceededAndLandedUpstreamIsLandedAndExecuted(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.sendLand(t)
	f.merged()
	f.queue(contractOut(ContractStateSucceeded, nil, nil))
	item := f.one(t)
	if item.Result != ExecutionResultLanded {
		t.Fatalf("item = %+v, want landed (AC-257)", item)
	}
	f.wantOpState(t, "executed") // AC-258
	if mc := f.state(t).mergeCommit; mc.String != "upstream-mc" {
		t.Fatalf("merge_commit = %v, want the merge commit read upstream (AC-259)", mc)
	}
}

func TestReflect_AC260_SucceededWithoutLandingUpstreamIsDelegateEnded(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	f.queue(contractOut(ContractStateSucceeded, nil, nil))
	if item := f.one(t); item.Result != ExecutionResultDelegateEnded {
		t.Fatalf("item = %+v, want delegate_ended", item)
	}
}

func TestReflect_AC261_FailedWithoutLandingUpstreamIsDelegateEnded(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	f.queue(contractOut(ContractStateFailed, nil, nil))
	if item := f.one(t); item.Result != ExecutionResultDelegateEnded {
		t.Fatalf("item = %+v, want delegate_ended", item)
	}
}

func TestReflect_AC262_AnUnfinishedRequestIsNotStartedAgain(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	if item := f.one(t); item.Result != ExecutionResultWaitingDelegate {
		t.Fatalf("item = %+v, want waiting_delegate", item)
	}
	if len(f.client.starts) != 1 {
		t.Fatalf("starts = %d, want commands.reflect called once", len(f.client.starts))
	}
}

func TestReflect_AC263_ACostIncreaseIsRecordedOnTheReadRun(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	cost := 0.5
	f.queue(contractOut(ContractStateRunning, &cost, nil))
	f.one(t)
	var got int64
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT cost_usd FROM run WHERE kind = 'contract' ORDER BY id DESC LIMIT 1`).Scan(&got)
	})
	if got != usdToMicros(0.5) {
		t.Fatalf("cost of the status read run = %d, want %d", got, usdToMicros(0.5))
	}
}

func TestReflect_AC264_ACostOverTheChallengeBudgetDoesNotCancel(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	cost := 1000.0
	f.queue(contractOut(ContractStateRunning, &cost, nil))
	f.one(t)
	if len(f.client.cancels) != 0 {
		t.Fatalf("cancels = %v, want none", f.client.cancels)
	}
}

func TestReflect_AC265_AC266_AC267_BaseChangedCancelsAndAFailedCancelStaysInTheDetail(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	f.fake.pr.Base = "develop"
	f.client.cancelOut = ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "cancel boom"}
	f.queue(reviewFor(relHead))
	item := f.one(t)
	if len(f.client.cancels) != 1 {
		t.Errorf("cancels = %v, want one (AC-265)", f.client.cancels)
	}
	if item.Result != ExecutionResultBaseChanged {
		t.Errorf("result = %s, want base_changed even when the cancel failed (AC-266)", item.Result)
	}
	if d := f.lastDetail(t); !strings.Contains(d, "could not cancel") {
		t.Errorf("detail = %q, want the cancel failure (AC-267)", d)
	}
}

func TestReflect_ALandRefusedCancelsTheRequest(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.sendLand(t)
	f.queue(reviewFor(relHead))
	f.one(t)
	if len(f.client.cancels) != 1 {
		t.Fatalf("cancels = %v, want one", f.client.cancels)
	}
}

func TestReflect_ALimitReachedCancelsTheRequest(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.limit = 1
	f.agent.FailureLimit = 1
	f.started(t)
	f.fake.pr.Checks = []UpstreamCheck{{Status: "completed", Conclusion: "failure"}}
	f.queue(reviewFor(relHead))
	if item := f.one(t); item.Result != ExecutionResultLimitReached {
		t.Fatalf("item = %+v, want limit_reached", item)
	}
	if len(f.client.cancels) != 1 {
		t.Fatalf("cancels = %v, want one", f.client.cancels)
	}
}

func TestReflect_AC276_AWaitingOutsideReleaseStartsARequestOnceTheDeclarationHasAnEntrance(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	reflectCmd := f.conn.Connectors[0].Commands.Reflect
	f.conn.Connectors[0].Commands.Reflect = nil
	if item := f.one(t); item.Result != ExecutionResultWaitingOutside || len(f.client.starts) != 0 {
		t.Fatalf("item = %+v starts %v, want waiting_outside without a call", item, f.client.starts)
	}
	f.conn.Connectors[0].Commands.Reflect = reflectCmd
	if item := f.one(t); item.Result != ExecutionResultReflectStarted || len(f.client.starts) != 1 {
		t.Fatalf("item = %+v starts %v, want reflect_started by commands.reflect", item, f.client.starts)
	}
}

// ---- 別の反映の操作の反映の依頼を待つ ----

func (f *reflFixture) otherOperationRequest(t *testing.T) {
	t.Helper()
	_, op2 := f.secondRelease(t, "https://github.com/o/r/pull/2")
	// 同じ課題の別の反映の操作。
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE operation SET challenge_id = ? WHERE id = ?`, f.cid, op2)
		return err
	})
	if res := f.execIDs(t, formatOperationID(op2)); len(res.Items) != 1 || res.Items[0].Result != ExecutionResultReflectStarted {
		t.Fatalf("second release = %+v, want reflect_started", res)
	}
}

func TestReflect_AC139_AC140_AC141_AnotherOperationsRunningRequestIsWaitedForWithoutCommandsOrRoute(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.otherOperationRequest(t)
	starts, statuses := len(f.client.starts), len(f.client.statuses)
	item := f.one(t)
	if item.Result != ExecutionResultWaitingDelegate {
		t.Errorf("result = %s, want waiting_delegate (AC-139)", item.Result)
	}
	if len(f.client.starts) != starts || len(f.client.statuses) != statuses || len(f.client.resumes) != 0 || len(f.client.cancels) != 0 {
		t.Errorf("commands were called: starts %v statuses %v resumes %v cancels %v (AC-140)", f.client.starts, f.client.statuses, f.client.resumes, f.client.cancels)
	}
	if item.Route != "" {
		t.Errorf("route = %q, want null (AC-141)", item.Route)
	}
}

// ---- 委譲の段は反映の依頼を読まない ----

func (f *reflFixture) poll(t *testing.T) {
	t.Helper()
	if _, err := f.s.PollContractRuns(context.Background(), ContractPollInput{AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client}); err != nil {
		t.Fatalf("PollContractRuns: %v", err)
	}
}

func TestReflect_AC126_TheDelegationStageDoesNotCallStatusOfAReflectRequest(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	f.poll(t)
	if len(f.client.statuses) != 0 {
		t.Fatalf("statuses = %v, want none from the delegation stage", f.client.statuses)
	}
}

func TestReflect_AC322_NoRespondResumeIsSentToAReflectRequest(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	f.queue(reviewFor(relHead))
	f.exec(t) // land
	f.poll(t)
	for _, c := range f.client.resumes {
		for i, a := range c {
			if a == "--choice" && c[i+1] == "respond" {
				t.Fatalf("resume %v answers respond to a reflect request", c)
			}
		}
	}
}

// ---- 完了から戻す前の取り消し ----

// reflectLandFixture は、J5 を通って完了になった課題の、承認済みの反映の操作に、反映の依頼が走っている場面（検証の head がある）。
func newReflectReopenFixture(t *testing.T) *landFixture {
	t.Helper()
	f := newLandFixture(t)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE contract_run SET kind = 'reflect', operation_id = 1, task_id = NULL`)
		return err
	})
	return f
}

func TestReflect_AC311_ReopenCancelsAnUnfinishedReflectRequest(t *testing.T) {
	t.Parallel()
	f := newReflectReopenFixture(t)
	f.cancelsWith()
	res, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, f.reopenInput(f.contentChangedCompare()))
	if err != nil || !res.Reopened {
		t.Fatalf("reopen = %+v err %v, want reopened", res, err)
	}
	if len(f.client.cancels) != 1 {
		t.Fatalf("cancels = %v, want one", f.client.cancels)
	}
}

func TestReflect_AC312_AFailedCancelLeavesTheChallengeDone(t *testing.T) {
	t.Parallel()
	f := newReflectReopenFixture(t)
	f.client.cancelOut = ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "boom"}
	_, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, f.reopenInput(f.contentChangedCompare()))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if got := f.status(t, f.cid); got != StatusDone {
		t.Fatalf("status = %s, want done", got)
	}
}

// 起動の応答がすでに終わった状態なら、終わっていない実行が残らず次の段が始め直すので、始められなかった扱い（reflect_failed）にする。
func TestReflect_AStartThatReturnsAnEndedStateIsReflectFailed(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.client.startOut = contractOut(ContractStateFailed, nil, nil)
	if item := f.one(t); item.Result != ExecutionResultReflectFailed {
		t.Fatalf("item = %+v, want reflect_failed", item)
	}
	f.wantOpState(t, "pending")
}

// 起動の前に分かる unsupported は、契約の実行が無いので route が null。
func TestReflect_UnsupportedBeforeStartingHasNoRoute(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.conn.Repos[0].Slots = ConnectorRepoSlots{Provider: "clone"}
	if item := f.one(t); item.Result != ExecutionResultUnsupported || item.Route != "" {
		t.Fatalf("item = %+v, want unsupported with a null route", item)
	}
}

// 読みと起動の間に承認が取り消されたら、反映の依頼を始めない: commands.reflect は呼ばれず、契約の実行の行も run の行も増えない。
func TestReflect_ApprovalRevokedBetweenTheReadAndTheStartIsConflictAndNothingIsStarted(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	var runsBefore int
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM run`).Scan(&runsBefore)
	})
	revoked := false
	f.fake.onGet = func() {
		if revoked {
			return
		}
		revoked = true
		// 上流の読みの最中に、別の経路が承認を取り消す（版も進む）。
		writeForTest(t, f.s, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE operation SET state = 'pending', version = version + 1 WHERE id = ?`, f.opID)
			return err
		})
	}
	res := f.exec(t)
	if !revoked {
		t.Fatal("the upstream read hook did not run")
	}
	f.wantNotStarted(t, res, NotStartedInProgress)
	if len(f.client.starts) != 0 {
		t.Fatalf("starts = %v, want commands.reflect not called", f.client.starts)
	}
	if rows := f.rows(t); len(rows) != 0 {
		t.Fatalf("contract_run rows = %+v, want none", rows)
	}
	var runsAfter int
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM run`).Scan(&runsAfter)
	})
	if runsAfter != runsBefore {
		t.Fatalf("run rows = %d, want %d (nothing written)", runsAfter, runsBefore)
	}
	f.wantOpState(t, "pending")
}
