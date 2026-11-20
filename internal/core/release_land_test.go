package core

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
)

// 反映の段が、対応するタスクの実行を先に status で読み、検証と承認を通った中身を「入れてよい」（land）で返すことを、
// 実ストア・偽の ContractClient・偽の PR の取得で固定する。

// landConnectorsJSON は resume に {approved_ref} を渡す宣言。
var landConnectorsJSON = strings.Replace(reviewConnectorsJSON,
	`"--note", "{note}"`, `"--note", "{note}", "--approved-ref", "{approved_ref}"`, 1)

type landFixture struct {
	*reviewFlowFixture
	fake *fakeRelease
	// head は承認した中身で、委譲先のレビュー待ちの中身の識別と同じ。
	head string
}

// newLandFixture は、検証中のタスクの実行（レビュー待ち）を、完了の承認（反映の操作も承認）まで進める。課題は完了になる。
func newLandFixture(t *testing.T) *landFixture {
	t.Helper()
	ctx := context.Background()
	cf := newContractFixtureWith(t, landConnectorsJSON)
	j := &j5Fixture{delegateFixture: cf.delegateFixture, checks: newFakeChecks(), j5: &fakeJudgmentInvoker{result: j5Out("met", nil, nil)}}
	verified := prChecks("open", checkDone)
	verified.HeadSHA = reviewContentArtifact.Ref // 検証した中身は、委譲先が言う中身の識別と同じ
	j.checks.prs["o/r#1"] = verified
	f := &landFixture{reviewFlowFixture: &reviewFlowFixture{contractFixture: cf, j5: j}, head: reviewContentArtifact.Ref}
	f.setPR(1, reviewPRURL, "open", "main")
	f.toVerifying(t)
	if _, err := f.s.VerifyAutoJ5(ctx, f.j5.input(f.cycle(t, 300), &f.cid)); err != nil {
		t.Fatal(err)
	}
	prev, err := f.s.PrepareApproval(ctx, f.cid)
	if err != nil || len(prev.PendingReleases) != 1 {
		t.Fatalf("PrepareApproval = %+v err %v", prev, err)
	}
	head := f.head
	if _, _, err := f.s.ExecuteApproval(ctx, ApprovalRequest{
		ChallengeID: f.cid, ExpectedVersion: prev.Version, Decision: ApprovalDecisionApproved,
		ReleaseHeads: []ReleaseHead{{Operation: prev.PendingReleases[0], HeadSHA: &head}},
	}, verifiedAttestationForTest(t, f.cid)); err != nil {
		t.Fatalf("ExecuteApproval: %v", err)
	}
	open := openPR()
	open.URL = reviewPRURL
	open.HeadSHA = f.head
	f.fake = &fakeRelease{pr: open}
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	return f
}

func (f *landFixture) release(t *testing.T) ReleaseExecItem {
	t.Helper()
	res, err := f.s.ExecuteReleases(context.Background(), []string{"OP-1"}, ReleaseExecOptions{
		Checks: f.fake, Connectors: f.conn, Agent: &AgentDeclaration{FailureLimit: 3}, Contract: f.client, Trigger: ReleaseExecTriggerOpExec,
	})
	if err != nil {
		t.Fatalf("ExecuteReleases: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("ExecuteReleases = %+v, want one item", res)
	}
	return res.Items[0]
}

func (f *landFixture) landRow(t *testing.T) (ref string, delivered bool) {
	t.Helper()
	var r sql.NullString
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT land_ref, land_delivered FROM contract_run`).Scan(&r, &delivered)
	}); err != nil {
		t.Fatal(err)
	}
	return r.String, delivered
}

func (f *landFixture) opState(t *testing.T) string {
	t.Helper()
	var s string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT state FROM operation WHERE id = 1`).Scan(&s)
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *landFixture) queueReview() {
	f.client.queue = []ContractLaunchOutput{reviewWaitOutFor(reviewPRURL)}
}

// 対応するタスクの実行が land を持つレビュー待ちなら、先に status で読み、{choice}=land・{approved_ref}=承認した中身・{note}=空・{answer}=land で resume する。
func TestReleaseLand_ReviewWaitWithLandOptionIsAnsweredWithLandAndTheApprovedRef(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.queueReview()
	statusesBefore := len(f.client.statuses)
	item := f.release(t)
	if len(f.client.statuses)-statusesBefore != 1 {
		t.Fatalf("status calls = %d, want 1 before the upstream read", len(f.client.statuses)-statusesBefore)
	}
	want := []string{"tool", "resume", "ext-1", "--choice", "land", "--note", "", "--approved-ref", f.head}
	if len(f.client.resumes) != 1 || !reflect.DeepEqual(f.client.resumes[0], want) {
		t.Fatalf("resumes = %v, want %v", f.client.resumes, want)
	}
	if item.Result != ExecutionResultLandSent || item.Route != ReleaseRouteWaitingRun {
		t.Fatalf("item = %+v, want land_sent with route waiting_run", item)
	}
	if ref, delivered := f.landRow(t); ref != f.head || !delivered {
		t.Fatalf("land_ref=%q delivered=%v, want %q and true", ref, delivered, f.head)
	}
	if got := f.opState(t); got != "approved" || item.ConsecutiveFailures != 0 {
		t.Fatalf("operation = %s failures %d, want approved and uncounted", got, item.ConsecutiveFailures)
	}
}

// 「入れてよい」の {answer} は land。
func TestReleaseLand_TheAnswerPlaceholderIsLand(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.conn.Connectors[0].Commands.Resume = []string{"tool", "resume", "{run_id}", "--answer", "{answer}"}
	f.queueReview()
	f.release(t)
	if want := []string{"tool", "resume", "ext-1", "--answer", "land"}; len(f.client.resumes) != 1 || !reflect.DeepEqual(f.client.resumes[0], want) {
		t.Fatalf("resumes = %v, want %v", f.client.resumes, want)
	}
}

// resume が失敗したら land_unconfirmed（連続の回数に数える）。land_ref は呼ぶ前に書いてあり、land_delivered は偽のまま。
func TestReleaseLand_AFailedResumeIsLandUnconfirmedAndKeepsTheLandRefUndelivered(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.queueReview()
	f.client.resumeOut = ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "boom"}
	item := f.release(t)
	if item.Result != ExecutionResultLandUnconfirmed || item.ConsecutiveFailures != 1 {
		t.Fatalf("item = %+v, want land_unconfirmed counted once", item)
	}
	if ref, delivered := f.landRow(t); ref != f.head || delivered {
		t.Fatalf("land_ref=%q delivered=%v, want %q and false", ref, delivered, f.head)
	}
	if got := f.opState(t); got != "approved" {
		t.Fatalf("operation = %s, want approved", got)
	}
}

// 委譲先の言う中身が通す中身と違うときは resume を呼ばず content_mismatch（2 つの識別が詳細に残る・数える）。
func TestReleaseLand_AContentRefThatDiffersFromUpstreamIsContentMismatchWithoutResume(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateWaiting, nil, func(st *ContractStatus) {
		st.RequestedAction = &ContractRequestedAction{Kind: "review", Decider: "any", Text: "t", Options: []string{"respond", "land"}}
		st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: reviewPRURL}, {Kind: "commit", Ref: "other-sha"}}
	})}
	item := f.release(t)
	if len(f.client.resumes) != 0 {
		t.Fatalf("resumes = %v, want none", f.client.resumes)
	}
	if item.Result != ExecutionResultContentMismatch || item.Route != ReleaseRouteWaitingRun || item.ConsecutiveFailures != 1 {
		t.Fatalf("item = %+v, want a counted content_mismatch with route waiting_run", item)
	}
	var detail string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COALESCE(detail, '') FROM operation_execution ORDER BY id DESC LIMIT 1`).Scan(&detail)
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "other-sha") || !strings.Contains(detail, f.head) {
		t.Fatalf("detail = %q, want both content refs", detail)
	}
	if got := f.opState(t); got != "approved" {
		t.Fatalf("operation = %s, want approved", got)
	}
}

// 承認済みでない（pending）反映の操作には、land の resume を呼ばない。
func TestReleaseLand_APendingReleaseGetsNoLandResume(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE operation SET state = 'pending' WHERE id = 1`)
		return err
	})
	f.queueReview()
	if _, err := f.s.ExecuteReleases(context.Background(), []string{"OP-1"}, ReleaseExecOptions{
		Checks: f.fake, Connectors: f.conn, Contract: f.client, Trigger: ReleaseExecTriggerOpExec,
	}); err != nil {
		t.Fatal(err)
	}
	if len(f.client.resumes) != 0 || len(f.client.statuses) != 1 {
		t.Fatalf("resumes=%v statuses=%d, want no contract command for a pending release (only the earlier intake read)", f.client.resumes, len(f.client.statuses))
	}
}

// 取り込みだけの変わり方なら、いまの中身を {approved_ref} にする。試行の approved_head は承認した中身、observed_head はいまの中身。
func TestReleaseLand_AMergeInOnlyMoveLandsTheCurrentHeadAndKeepsBothHeadsInTheAttempt(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.fake.pr.HeadSHA = "h1"
	f.fake.pr.Checks = []UpstreamCheck{{Status: "completed", Conclusion: "success"}}
	// 承認した中身から h1 への変わり方は、反映先の最新の取り込みだけ（衝突の解消の差分が無い取り込み）。
	cmp := newFakeCompare()
	cmp.set(f.head, "h1", withFiles(ahead(commit("m1", "main", "m0"), commit("h1", "Merge", f.head, "m1")), UpstreamFile{Filename: "a", Patch: "p"}))
	cmp.set("main", "h1", ahead(commit(f.head, "fix", "p0"), commit("h1", "Merge", f.head, "m1")))
	cmp.set("m1", "h1", ahead(commit(f.head, "fix", "p0"))) // 衝突の解消の差分は無い
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateWaiting, nil, func(st *ContractStatus) {
		st.RequestedAction = &ContractRequestedAction{Kind: "review", Decider: "any", Text: "t", Options: []string{"respond", "land"}}
		st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: reviewPRURL}, {Kind: "commit", Ref: "h1"}}
	})}
	res, err := f.s.ExecuteReleases(context.Background(), []string{"OP-1"}, ReleaseExecOptions{
		Checks: f.fake, Compare: cmp, Connectors: f.conn, Agent: &AgentDeclaration{FailureLimit: 3}, Contract: f.client, Trigger: ReleaseExecTriggerOpExec,
	})
	if err != nil || len(res.Items) != 1 || res.Items[0].Result != ExecutionResultLandSent {
		t.Fatalf("ExecuteReleases = %+v err %v, want land_sent", res, err)
	}
	if got := f.client.resumes[0][len(f.client.resumes[0])-1]; got != "h1" {
		t.Fatalf("{approved_ref} = %q, want the current head", got)
	}
	if it := res.Items[0]; it.ApprovedHead != f.head || it.ObservedHead != "h1" {
		t.Fatalf("heads = %q/%q, want the approved and the current head", it.ApprovedHead, it.ObservedHead)
	}
	if ref, _ := f.landRow(t); ref != "h1" {
		t.Fatalf("land_ref = %q, want h1", ref)
	}
}

// チェックが終わっていない間は resume を呼ばず waiting_checks。
func TestReleaseLand_PendingChecksWaitWithoutResume(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.fake.pr.Checks = []UpstreamCheck{{Status: "queued"}}
	f.queueReview()
	if item := f.release(t); item.Result != ExecutionResultWaitingChecks || len(f.client.resumes) != 0 {
		t.Fatalf("item = %+v resumes %v, want waiting_checks without resume", item, f.client.resumes)
	}
}

// 通す中身のチェックも承認した中身のチェックも 0 件のときは止まらず、「入れてよい」が返る。
func TestReleaseLand_NoChecksOnEitherHeadStillLands(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.fake.pr.Checks = nil
	f.queueReview()
	if item := f.release(t); item.Result != ExecutionResultLandSent {
		t.Fatalf("item = %+v, want land_sent", item)
	}
}

// 取り消す結果（head_changed）では、契約の実行の land_ref を消し、タスクの実行へ cancel を呼ばない。
func TestReleaseLand_ACancellingResultClearsTheLandRefWithoutCancellingTheRun(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.queueReview()
	f.release(t) // land_sent。land_ref が入る。
	f.fake.pr.HeadSHA = "unrelated-change"
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, nil, nil)}
	// 委譲先が running のうちは待つだけ。レビュー待ちへ戻って中身が食い違えば head_changed になる。
	f.release(t)
	f.client.queue = []ContractLaunchOutput{reviewWaitOutFor(reviewPRURL)}
	item := f.release(t)
	if item.Result != ExecutionResultHeadChanged {
		t.Fatalf("item = %+v, want head_changed", item)
	}
	if ref, delivered := f.landRow(t); ref != "" || delivered {
		t.Fatalf("land_ref=%q delivered=%v, want cleared", ref, delivered)
	}
	if len(f.client.cancels) != 0 {
		t.Fatalf("cancels = %v, want none for the task run", f.client.cancels)
	}
	if got := f.opState(t); got != "pending" {
		t.Fatalf("operation = %s, want pending", got)
	}
}

// 状態が running なら、成果物を読む前に waiting_delegate（resume を呼ばない・land_ref があっても同じ・route は waiting_run）。
func TestReleaseLand_ARunningRunIsWaitingDelegateWithoutResumeAndBeforeTheUpstreamRead(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, nil, nil)}
	f.fake.getCalls = 0
	item := f.release(t)
	if item.Result != ExecutionResultWaitingDelegate || item.Route != ReleaseRouteWaitingRun {
		t.Fatalf("item = %+v, want waiting_delegate with route waiting_run", item)
	}
	if len(f.client.resumes) != 0 || f.fake.getCalls != 0 {
		t.Fatalf("resumes=%v upstream reads=%d, want none (the status comes first)", f.client.resumes, f.fake.getCalls)
	}
}

// status が失敗したら upstream_failed（数えない）。
func TestReleaseLand_AFailedStatusIsUpstreamFailed(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.client.queue = []ContractLaunchOutput{{Result: RunResultErrored, ErrorSummary: "down"}}
	item := f.release(t)
	if item.Result != ExecutionResultUpstreamFailed || item.ConsecutiveFailures != 0 || len(f.client.resumes) != 0 {
		t.Fatalf("item = %+v resumes %v, want an uncounted upstream_failed", item, f.client.resumes)
	}
}

// 反映の段が status を読むと契約の実行の記録は更新されるが、成果物の記録（run_artifact）は増えず、費用が上限を超えても cancel しない。
func TestReleaseLand_ReadingUpdatesTheContractRunButNeitherImportsArtifactsNorCancelsOverBudget(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	before := f.count(t, `SELECT COUNT(*) FROM run_artifact`)
	over := 1000.0
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateWaiting, &over, func(st *ContractStatus) {
		st.Summary = "read-by-release"
		st.RequestedAction = &ContractRequestedAction{Kind: "review", Decider: "any", Text: "t", Options: []string{"respond", "land"}}
		st.Artifacts = []ContractArtifact{{Kind: "pr", Ref: reviewPRURL}, {Kind: "commit", Ref: "newer"}}
	})}
	f.release(t)
	if got := f.count(t, `SELECT COUNT(*) FROM run_artifact`); got != before {
		t.Fatalf("run_artifact rows %d → %d, want unchanged", before, got)
	}
	if len(f.client.cancels) != 0 {
		t.Fatalf("cancels = %v, want none", f.client.cancels)
	}
	if _, summary, _, _ := f.contractRow(t); summary != "read-by-release" {
		t.Fatalf("summary = %q, want the value read by the release phase", summary)
	}
	if got := f.queryString(t, `SELECT content_ref FROM contract_run`); got.String != "newer" {
		t.Fatalf("content_ref = %q, want newer", got.String)
	}
}

// 課題に終わっていない実行の記録（例: 学びの判断点）があるときは、契約のコマンドを呼ばず、試行の行を増やさず in_progress で始めない。
func TestReleaseLand_AnUnfinishedRunRecordMeansNoContractCommandAndNoAttemptRow(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO run (challenge_id, kind, judgment, challenge_version, session_id, pid, host, heartbeat_at, started_at, max_budget_usd, budget_bucket)
			VALUES (?, 'judgment', 'J6', 1, 's', 1, 'h', 'x', 'x', 1000000, 'review')`, 1)
		return err
	})
	f.queueReview()
	before := f.count(t, `SELECT COUNT(*) FROM operation_execution`)
	statuses := len(f.client.statuses)
	res, err := f.s.ExecuteReleases(context.Background(), []string{"OP-1"}, ReleaseExecOptions{
		Checks: f.fake, Connectors: f.conn, Agent: &AgentDeclaration{FailureLimit: 3}, Contract: f.client, Trigger: ReleaseExecTriggerOpExec,
	})
	if err != nil || len(res.Items) != 0 || len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedInProgress {
		t.Fatalf("ExecuteReleases = %+v err %v, want in_progress not started", res, err)
	}
	if got := f.count(t, `SELECT COUNT(*) FROM operation_execution`); got != before {
		t.Fatalf("attempt rows %d → %d, want unchanged", before, got)
	}
	if len(f.client.statuses) != statuses || len(f.client.resumes) != 0 {
		t.Fatalf("contract commands were called (status %d→%d, resume %v)", statuses, len(f.client.statuses), f.client.resumes)
	}
}

// 別の成果物の実行がレビュー待ちなら unsupported（詳細に実行の ID）。何も呼ばない。
func TestReleaseLand_ARunForAnotherArtifactThatIsNotRunningIsUnsupportedWithItsID(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE run_artifact SET ref = 'https://github.com/o/r/pull/99' WHERE kind = 'pr'`)
		return err
	})
	statuses := len(f.client.statuses)
	item := f.release(t)
	if item.Result != ExecutionResultUnsupported || item.Route != "" {
		t.Fatalf("item = %+v, want unsupported without a route", item)
	}
	if len(f.client.statuses) != statuses || len(f.client.resumes) != 0 {
		t.Fatalf("commands were called for a run that does not correspond")
	}
	var detail string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COALESCE(detail, '') FROM operation_execution ORDER BY id DESC LIMIT 1`).Scan(&detail)
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "ext-1") {
		t.Fatalf("detail = %q, want the run id", detail)
	}
}

// 別の成果物の実行が running なら waiting_delegate（route は null・何も呼ばない）。
func TestReleaseLand_ARunForAnotherArtifactThatIsRunningIsWaitingDelegateWithoutARoute(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE run_artifact SET ref = 'https://github.com/o/r/pull/99' WHERE kind = 'pr'`); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE contract_run SET state = 'running'`)
		return err
	})
	statuses := len(f.client.statuses)
	item := f.release(t)
	if item.Result != ExecutionResultWaitingDelegate || item.Route != "" || len(f.client.statuses) != statuses {
		t.Fatalf("item = %+v, want waiting_delegate without a route and without commands", item)
	}
}

// 完了から検証中へ戻す（T18）とき、契約の実行の land_ref を消す（承認し直した後に、もう一度「入れてよい」を返す）。
func TestReleaseLand_ReopeningTheChallengeClearsTheLandRef(t *testing.T) {
	t.Parallel()
	f := newLandFixture(t)
	f.queueReview()
	f.release(t) // land_sent
	if ref, _ := f.landRow(t); ref == "" {
		t.Fatal("land_ref was not written")
	}
	f.fake.pr.HeadSHA = "hc"
	cmp := newFakeCompare()
	cmp.set(f.head, "hc", ahead(commit("hc", "add feature", f.head)))
	cmp.set("main", "hc", ahead(commit(f.head, "fix", "p0"), commit("hc", "add feature", f.head)))
	res, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, ReopenForReleaseInput{
		OperationID: "OP-1", Reason: ReopenReasonContentChanged, Sources: ReleaseHeadSources{Checks: f.fake, Compare: cmp},
	})
	if err != nil || !res.Reopened {
		t.Fatalf("ReopenChallengeForRelease = %+v err %v, want reopened", res, err)
	}
	if ref, delivered := f.landRow(t); ref != "" || delivered {
		t.Fatalf("land_ref=%q delivered=%v, want cleared", ref, delivered)
	}
}

// 委譲の段の先頭の状態の確認は、反映の依頼（kind が reflect）の実行を読まない。
func TestPollContractRuns_AReflectRequestIsNotReadByTheDelegationStage(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	if _, err := f.exec(`UPDATE contract_run SET kind = 'reflect'`); err != nil {
		t.Fatal(err)
	}
	f.poll(t)
	if len(f.client.statuses) != 0 || len(f.client.resumes) != 0 {
		t.Fatalf("statuses=%d resumes=%d, want no command for a reflect request", len(f.client.statuses), len(f.client.resumes))
	}
}

// 課題が着手中で、タスクの実行が running のとき、委譲の段の先頭の状態の確認は commands.status を呼ぶ（見直し前と同じ）。
func TestPollContractRuns_ARunningTaskRunOfAnInProgressChallengeIsStillRead(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.poll(t)
	if len(f.client.statuses) != 1 {
		t.Fatalf("statuses = %d, want 1", len(f.client.statuses))
	}
}
