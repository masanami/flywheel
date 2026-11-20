package core

// このファイルは、統合先を使う課題の上げる依頼（完了でない課題の反映の依頼。区切り S3b の受入基準 AC-24・AC-151〜AC-171・AC-188〜AC-191・
// AC-194〜AC-199）を、実ストア・偽の契約のコマンド・偽の PR の取得で検証する。基準 1 件ごとに、基準の文が述べる条件・結果・値を確かめる。

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

const (
	upIntegRef = "feat/integration-1"
	upPRURL    = "https://github.com/o/r/pull/7"
)

// upConnectorsJSON は multiConnectorsJSON に、反映の依頼の口（commands.reflect）を足したもの。
func upConnectorsJSON() string {
	return strings.Replace(multiConnectorsJSON(), `"cancel": ["tool", "cancel", "{run_id}"]`,
		`"cancel": ["tool", "cancel", "{run_id}"],
       "reflect": ["tool", "reflect", "--artifact", "{artifact_ref}", "--challenge", "{challenge_id}"]`, 1)
}

// newUpFixture は、反映の依頼の口を持つ契約の形の接続ツールと、統合先を使う課題（タスク a が済み。統合先の識別を記録済み）を持つ。
func newUpFixture(t *testing.T, connectorsJSON string, tasks ...multiTask) (*contractFixture, string) {
	t.Helper()
	f := newMultiFixture(t, 2)
	conn, err := parseConnectorsDeclaration([]byte(connectorsJSON))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := conn.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	f.conn = conn
	if len(tasks) == 0 {
		tasks = []multiTask{{key: "a"}}
	}
	id := f.newMulti(t, "up", "P1", true, tasks...)
	cid, _ := parseChallengeID(id)
	for _, tk := range tasks {
		setTaskStatusRaw(t, f.s, cid, tk.key, TaskStatusDone)
	}
	f.sqlExec(t, `UPDATE challenge SET integration_ref = ? WHERE id = ?`, upIntegRef, cid)
	return f, id
}

func (f *contractFixture) sqlExec(t *testing.T, q string, args ...any) {
	t.Helper()
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(q, args...)
		return err
	})
}

func (f *contractFixture) reflectStarts() [][]string {
	var out [][]string
	for _, c := range f.client.starts {
		if len(c) > 1 && c[1] == "reflect" {
			out = append(out, c)
		}
	}
	return out
}

func (f *contractFixture) lastReason(t *testing.T, id string) string {
	t.Helper()
	rs := f.holdReasons(t, id)
	if len(rs) == 0 {
		t.Fatalf("%s has no hold", id)
	}
	return rs[len(rs)-1]
}

func (f *contractFixture) statusOf(t *testing.T, id string) Status {
	t.Helper()
	cid, _ := parseChallengeID(id)
	var st string
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT status FROM challenge WHERE id = ?`, cid).Scan(&st)
	})
	return Status(st)
}

// upReviewOut は上げる依頼のレビュー待ち（既定ブランチ宛ての PR と、中身の識別が 1 つ）。
func upReviewOut(options []string, arts ...ContractArtifact) ContractLaunchOutput {
	return contractOut(ContractStateWaiting, nil, func(st *ContractStatus) {
		st.RequestedAction = &ContractRequestedAction{Kind: "review", Decider: "any", Text: "レビュー待ち", Options: options}
		st.Artifacts = arts
	})
}

var upArtsOK = []ContractArtifact{{Kind: "pr", Ref: upPRURL}, reviewContentArtifact}

func (f *contractFixture) setUpPR(base string) {
	f.branches.pulls["o/r#7"] = UpstreamPullRequest{URL: upPRURL, Title: "統合先から反映先へ", State: "open", Base: base}
}

// startUp は委譲の段を 1 回実行して上げる依頼を始め、以降の状態の確認が読む出力を out にする。
func (f *contractFixture) startUp(t *testing.T) {
	t.Helper()
	f.runDelegationStage(t, nil)
	if len(f.reflectStarts()) != 1 {
		t.Fatalf("reflect starts = %v, want 1", f.client.starts)
	}
	f.client.echoRunning = false
}

func (f *contractFixture) readWith(t *testing.T, out ContractLaunchOutput) *DelegateResult {
	t.Helper()
	f.client.last = out
	return f.runDelegationStage(t, nil)
}

func (f *contractFixture) reflectRow(t *testing.T) (kind string, taskID, opID sql.NullInt64, ended bool) {
	t.Helper()
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT kind, task_id, operation_id, ended_at IS NOT NULL FROM contract_run WHERE kind = 'reflect' ORDER BY id DESC LIMIT 1`).Scan(&kind, &taskID, &opID, &ended)
	})
	return
}

// ---- 始める ----

// AC-151・AC-152・AC-153・AC-154
func TestUpRequest_AC151_AC152_AC153_AC154_StartsWhenAllTasksAreDone(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, upConnectorsJSON())
	f.runDelegationStage(t, nil)
	starts := f.reflectStarts()
	if len(starts) != 1 { // AC-151
		t.Fatalf("starts = %v, want one commands.reflect", f.client.starts)
	}
	if got := argAfter(t, starts[0], "--artifact"); got != upIntegRef { // AC-152
		t.Errorf("{artifact_ref} = %q, want %q", got, upIntegRef)
	}
	if got := argAfter(t, starts[0], "--challenge"); got != id { // AC-153
		t.Errorf("{challenge_id} = %q, want %q", got, id)
	}
	kind, taskID, opID, ended := f.reflectRow(t) // AC-154
	if kind != "reflect" || taskID.Valid || opID.Valid || ended {
		t.Errorf("contract run = kind %q task_id %+v operation_id %+v ended %v, want an open reflect run without task and operation", kind, taskID, opID, ended)
	}
}

// AC-155・AC-156: 済みでないタスク（未着手・着手中・検証中）が残る課題では、上げる依頼を始めない。
func TestUpRequest_AC155_AC156_UnfinishedTasksPreventStarting(t *testing.T) {
	t.Parallel()
	for _, st := range []TaskStatus{TaskStatusPending, TaskStatusInProgress, TaskStatusVerifying} {
		f, id := newUpFixture(t, upConnectorsJSON(), multiTask{key: "a"}, multiTask{key: "b"})
		cid, _ := parseChallengeID(id)
		setTaskStatusRaw(t, f.s, cid, "b", st)
		f.runDelegationStage(t, nil)
		if got := f.reflectStarts(); len(got) != 0 {
			t.Errorf("task b %s: reflect starts = %v, want none", st, got)
		}
	}
}

// AC-157・AC-158
func TestUpRequest_AC157_AC158_CancelledTasksAreNotCounted(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, upConnectorsJSON(), multiTask{key: "a"}, multiTask{key: "b"})
	cid, _ := parseChallengeID(id)
	setTaskStatusRaw(t, f.s, cid, "b", TaskStatusCancelled)
	f.runDelegationStage(t, nil)
	if got := f.reflectStarts(); len(got) != 1 { // AC-157
		t.Fatalf("done 1 + cancelled 1: reflect starts = %v, want 1", got)
	}

	g, gid := newUpFixture(t, upConnectorsJSON(), multiTask{key: "a"}, multiTask{key: "b"})
	gcid, _ := parseChallengeID(gid)
	setTaskStatusRaw(t, g.s, gcid, "a", TaskStatusCancelled)
	setTaskStatusRaw(t, g.s, gcid, "b", TaskStatusCancelled)
	g.runDelegationStage(t, nil)
	if got := g.reflectStarts(); len(got) != 0 { // AC-158
		t.Fatalf("all cancelled: reflect starts = %v, want none", got)
	}
}

// AC-159: 統合先を使わない課題では、委譲の段で commands.reflect を呼ばない。
func TestUpRequest_AC159_ChallengeWithoutIntegrationDoesNotStart(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	conn, err := parseConnectorsDeclaration([]byte(upConnectorsJSON()))
	if err != nil {
		t.Fatal(err)
	}
	f.conn = conn
	id := f.newMulti(t, "plain", "P1", false, multiTask{key: "a"})
	cid, _ := parseChallengeID(id)
	setTaskStatusRaw(t, f.s, cid, "a", TaskStatusDone)
	f.runDelegationStage(t, nil)
	if got := f.reflectStarts(); len(got) != 0 {
		t.Fatalf("reflect starts = %v, want none", got)
	}
}

// AC-24: 保留中の課題で、全部のタスクが done でも、commands.reflect は呼ばれない。AC-199: 保留が解けるまで呼ばれない。
func TestUpRequest_AC24_AC199_HeldChallengeDoesNotStartUntilTheHoldIsResolved(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, upConnectorsJSON())
	f.sqlExec(t, `UPDATE challenge SET status = 'awaiting_human'`)
	f.runDelegationStage(t, nil)
	if got := f.reflectStarts(); len(got) != 0 {
		t.Fatalf("held challenge: reflect starts = %v, want none", got)
	}
	f.sqlExec(t, `UPDATE challenge SET status = 'in_progress'`)
	f.runDelegationStage(t, nil)
	if got := f.reflectStarts(); len(got) != 1 {
		t.Fatalf("after the hold is resolved (%s): reflect starts = %v, want 1", id, got)
	}
}

// AC-194: 全部のタスクが done でも、終わっていない契約の実行が 1 本あれば、commands.reflect は呼ばれない。
func TestUpRequest_AC194_AnUnfinishedContractRunPreventsStarting(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, upConnectorsJSON())
	cid, _ := parseChallengeID(id)
	insertOpenContractRun(t, f.s, cid, "ext-9")
	f.runDelegationStage(t, nil)
	if got := f.reflectStarts(); len(got) != 0 {
		t.Fatalf("reflect starts = %v, want none", got)
	}
}

// AC-195: integration_ref が NULL の課題は、commands.reflect が呼ばれず、保留の reason が integration_ref_invalid になる。
func TestUpRequest_AC195_WithoutIntegrationRefTheChallengeIsHeld(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, upConnectorsJSON())
	f.sqlExec(t, `UPDATE challenge SET integration_ref = NULL`)
	f.runDelegationStage(t, nil)
	if got := f.reflectStarts(); len(got) != 0 {
		t.Fatalf("reflect starts = %v, want none", got)
	}
	if got := f.lastReason(t, id); got != string(holdReasonIntegrationRefInvalid) {
		t.Fatalf("reason = %q, want integration_ref_invalid", got)
	}
	if got := f.statusOf(t, id); got != StatusAwaitingHuman {
		t.Fatalf("status = %s", got)
	}
}

// AC-160: 同時に走らせる数が上限に達しているとき、上げる依頼は始まらない（理由 slot_unavailable）。
func TestUpRequest_AC160_AtTheParallelLimitNothingIsStarted(t *testing.T) {
	t.Parallel()
	f, _ := newUpFixture(t, upConnectorsJSON())
	f.agent.MaxParallelRuns = 1
	other := f.newMulti(t, "other", "P2", true, multiTask{key: "o"})
	ocid, _ := parseChallengeID(other)
	insertOpenContractRun(t, f.s, ocid, "ext-9")
	res := f.runDelegationStage(t, nil)
	if got := f.reflectStarts(); len(got) != 0 {
		t.Fatalf("reflect starts = %v, want none", got)
	}
	slot := 0
	for _, n := range notStartedByReason(res, NotStartedSlotUnavailable) {
		if n.ChallengeID != other {
			slot++
		}
	}
	if slot != 1 {
		t.Fatalf("not_started = %+v, want one slot_unavailable for the challenge to be raised", res.NotStarted)
	}
	if n := countRows(t, f.s, `SELECT COUNT(*) FROM hold`); n != 0 {
		t.Fatalf("holds = %d, want none", n)
	}
}

// AC-161: リポジトリに反映の依頼の口が無い → 課題は awaiting_human になり、保留の reason は reflect_unavailable である。
func TestUpRequest_AC161_WithoutTheReflectCommandTheChallengeIsHeld(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, multiConnectorsJSON())
	f.runDelegationStage(t, nil)
	if got := f.reflectStarts(); len(got) != 0 {
		t.Fatalf("reflect starts = %v, want none", got)
	}
	if got := f.statusOf(t, id); got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", got)
	}
	if got := f.lastReason(t, id); got != string(holdReasonReflectUnavailable) {
		t.Fatalf("reason = %q, want reflect_unavailable", got)
	}
	q := f.holdQuestionsOf(t, id)
	if strings.Contains(q, string(holdReasonReflectUnavailable)) || !strings.Contains(q, "commands.reflect") {
		t.Fatalf("question = %q, want a plain-language question that names the missing command", q)
	}
}

func (f *contractFixture) holdQuestionsOf(t *testing.T, id string) string {
	t.Helper()
	cid, _ := parseChallengeID(id)
	var q string
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT question FROM hold WHERE challenge_id = ? ORDER BY id DESC LIMIT 1`, cid).Scan(&q)
	})
	return q
}

// AC-162・AC-163: commands.reflect が失敗した → 保留の reason は reflect_failed で、終わっていない契約の実行の行は残らない。
func TestUpRequest_AC162_AC163_AFailedReflectHoldsWithoutAnOpenRun(t *testing.T) {
	t.Parallel()
	for name, out := range map[string]ContractLaunchOutput{
		"command failure": {Result: RunResultLaunchFailed, ErrorSummary: "boom"},
		"ended state":     contractOut(ContractStateFailed, nil, nil),
	} {
		f, id := newUpFixture(t, upConnectorsJSON())
		f.client.startOut = out
		f.runDelegationStage(t, nil)
		if got := f.lastReason(t, id); got != string(holdReasonReflectFailed) { // AC-162
			t.Errorf("%s: reason = %q, want reflect_failed", name, got)
		}
		if got := f.statusOf(t, id); got != StatusAwaitingHuman {
			t.Errorf("%s: status = %s", name, got)
		}
		if n := countRows(t, f.s, `SELECT COUNT(*) FROM contract_run WHERE ended_at IS NULL`); n != 0 { // AC-163
			t.Errorf("%s: open contract runs = %d, want 0", name, n)
		}
	}
}

// AC-164: 完了でない課題の上げる依頼は、状態の確認で commands.status が呼ばれる。
func TestUpRequest_AC164_TheStatusCommandReadsTheRequest(t *testing.T) {
	t.Parallel()
	f, _ := newUpFixture(t, upConnectorsJSON())
	f.runDelegationStage(t, nil)
	f.runDelegationStage(t, nil)
	if len(f.client.statuses) != 1 || f.client.statuses[0][2] != "ext-1" {
		t.Fatalf("statuses = %v, want one commands.status for the up request", f.client.statuses)
	}
	if len(f.reflectStarts()) != 1 {
		t.Fatalf("reflect starts = %v, want the request is not started again", f.reflectStarts())
	}
}

// ---- レビュー待ちを読む ----

// AC-166・AC-167・AC-168・AC-169
func TestUpRequest_AC166_AC167_AC168_AC169_ReviewCreatesTheOperationAndVerifies(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, upConnectorsJSON())
	f.setUpPR("main")
	f.startUp(t)
	f.readWith(t, upReviewOut([]string{"respond", "land"}, upArtsOK...))
	rels := f.releases(t)
	if len(rels) != 1 || rels[0].ref != upPRURL || rels[0].state != "pending" { // AC-166
		t.Fatalf("releases = %+v, want one pending release of %s", rels, upPRURL)
	}
	_, _, opID, _ := f.reflectRow(t)
	var want int64
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT id FROM operation WHERE kind = 'release'`).Scan(&want)
	})
	if !opID.Valid || opID.Int64 != want { // AC-167
		t.Errorf("operation_id = %+v, want %d", opID, want)
	}
	if got := f.statusOf(t, id); got != StatusVerifying { // AC-168
		t.Errorf("status = %s, want verifying", got)
	}
	cid, _ := parseChallengeID(id)
	if got := taskStatusMap(t, f.s, cid); got["a"] != TaskStatusDone { // AC-169
		t.Errorf("tasks = %v, want a done", got)
	}
}

// AC-170: 既定ブランチ宛ての PR が無い → 保留の reason は content_ref_invalid で、反映の操作は作られない。
func TestUpRequest_AC170_WithoutADefaultBranchPRTheChallengeIsHeld(t *testing.T) {
	t.Parallel()
	for name, setup := range map[string]func(f *contractFixture) []ContractArtifact{
		"pr to another branch": func(f *contractFixture) []ContractArtifact { f.setUpPR("develop"); return upArtsOK },
		"no pr artifact":       func(_ *contractFixture) []ContractArtifact { return []ContractArtifact{reviewContentArtifact} },
	} {
		f, id := newUpFixture(t, upConnectorsJSON())
		arts := setup(f)
		f.startUp(t)
		f.readWith(t, upReviewOut([]string{"respond", "land"}, arts...))
		if got := f.lastReason(t, id); got != string(holdReasonContentRefInvalid) {
			t.Errorf("%s: reason = %q, want content_ref_invalid", name, got)
		}
		if got := f.releases(t); len(got) != 0 {
			t.Errorf("%s: releases = %+v, want none", name, got)
		}
		if got := f.statusOf(t, id); got != StatusAwaitingHuman {
			t.Errorf("%s: status = %s", name, got)
		}
	}
}

// AC-171: options に land が無い → 保留の reason は land_unavailable である。
func TestUpRequest_AC171_WithoutTheLandOptionTheChallengeIsHeld(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, upConnectorsJSON())
	f.setUpPR("main")
	f.startUp(t)
	f.readWith(t, upReviewOut([]string{"respond"}, upArtsOK...))
	if got := f.lastReason(t, id); got != string(holdReasonLandUnavailable) {
		t.Fatalf("reason = %q, want land_unavailable", got)
	}
	if got := f.releases(t); len(got) != 0 {
		t.Fatalf("releases = %+v, want none", got)
	}
}

// ---- 終わりの状態を読む ----

// AC-196・AC-197・AC-198・AC-199: 完了でない課題の上げる依頼の failed を読むと、契約の実行は終わった記録になり、作っていた反映の操作は閉じられ、
// 課題は着手中を経て awaiting_human（reason reflect_failed）になる。その保留が解けるまで、commands.reflect は呼ばれない。
func TestUpRequest_AC196_AC197_AC198_AC199_AFailedRequestEndsTheRecordClosesTheOperationAndHolds(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, upConnectorsJSON())
	f.setUpPR("main")
	f.startUp(t)
	f.readWith(t, upReviewOut([]string{"respond", "land"}, upArtsOK...))
	if got := f.statusOf(t, id); got != StatusVerifying {
		t.Fatalf("precondition: status = %s, want verifying", got)
	}
	// 検証中の課題は、レビュー待ちの上げる依頼を読み直さない。読むのは、作業中（running）の上げる依頼。
	f.sqlExec(t, `UPDATE contract_run SET state = 'running', requested_kind = NULL WHERE kind = 'reflect'`)
	f.readWith(t, contractOut(ContractStateFailed, nil, func(st *ContractStatus) { st.Summary = "委譲先の失敗の要約" }))
	if _, _, _, ended := f.reflectRow(t); !ended { // AC-196
		t.Errorf("the contract run is not ended")
	}
	if got := f.statusOf(t, id); got != StatusAwaitingHuman { // AC-197
		t.Errorf("status = %s, want awaiting_human", got)
	}
	if got := f.lastReason(t, id); got != string(holdReasonReflectFailed) {
		t.Errorf("reason = %q, want reflect_failed", got)
	}
	if q := f.holdQuestionsOf(t, id); !strings.Contains(q, "failed") || !strings.Contains(q, "委譲先の失敗の要約") {
		t.Errorf("question = %q, want the read state and the summary", q)
	}
	rels := f.releases(t)
	if len(rels) != 1 || rels[0].state != "closed" { // AC-198
		t.Errorf("releases = %+v, want one closed release", rels)
	}
	before := len(f.reflectStarts())
	f.runDelegationStage(t, nil)
	if got := len(f.reflectStarts()); got != before { // AC-199
		t.Errorf("reflect starts %d -> %d while the hold is open", before, got)
	}
	// 保留が解けたら（着手中へ戻ったら）、次の委譲の段が上げる依頼を始め直す。
	f.sqlExec(t, `UPDATE challenge SET status = 'in_progress'`)
	f.runDelegationStage(t, nil)
	if got := len(f.reflectStarts()); got != before+1 {
		t.Errorf("reflect starts = %d, want %d once the hold is resolved", got, before+1)
	}
}

// 着手中の課題の上げる依頼が終わりの状態（succeeded を含む）を返したときも、reflect_failed で保留にする（タスクの実行の成功として扱わない）。
func TestUpRequest_ASucceededRequestBeforeLandingIsReflectFailedNotATaskSuccess(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, upConnectorsJSON())
	f.startUp(t)
	f.readWith(t, contractOut(ContractStateSucceeded, nil, nil))
	if got := f.lastReason(t, id); got != string(holdReasonReflectFailed) {
		t.Fatalf("reason = %q, want reflect_failed", got)
	}
	if got := f.statusOf(t, id); got != StatusAwaitingHuman {
		t.Fatalf("status = %s", got)
	}
}

// 申し送り: holdIntegrationSucceeded は契約の実行の種類を見て、反映の依頼の成功を処理しない（偽を返す）。
func TestHoldIntegrationSucceeded_IgnoresTheReflectRequest(t *testing.T) {
	t.Parallel()
	f, id := newUpFixture(t, upConnectorsJSON())
	f.startUp(t)
	handled, err := f.s.holdIntegrationSucceeded(context.Background(),
		&ContractRunReport{ChallengeID: id, ExternalRunID: "ext-1", State: ContractStateSucceeded}, nil)
	if err != nil || handled {
		t.Fatalf("handled = %v err = %v, want false for a reflect request", handled, err)
	}
	if rs := f.holdReasons(t, id); len(rs) != 0 {
		t.Fatalf("holds = %v, want none", rs)
	}
}

// 着手中の課題の上げる依頼が failed・cancelled を返したときは、タスクの実行の停止の保留ではなく、理由 reflect_failed で保留にする。
func TestUpRequest_AFailedOrCancelledRequestOfAnInProgressChallengeIsReflectFailed(t *testing.T) {
	t.Parallel()
	for _, st := range []ContractState{ContractStateFailed, ContractStateCancelled} {
		f, id := newUpFixture(t, upConnectorsJSON())
		f.startUp(t)
		f.readWith(t, contractOut(st, nil, nil))
		if got := f.lastReason(t, id); got != string(holdReasonReflectFailed) {
			t.Errorf("%s: reason = %q, want reflect_failed", st, got)
		}
		if got := f.statusOf(t, id); got != StatusAwaitingHuman {
			t.Errorf("%s: status = %s", st, got)
		}
	}
}

// 上げる依頼の run には、反映の依頼の印（run.reflect）が付く（開始・状態の確認とも）。
func TestUpRequest_RunsOfTheRequestAreMarkedAsReflect(t *testing.T) {
	t.Parallel()
	f, _ := newUpFixture(t, upConnectorsJSON())
	f.startUp(t)
	f.readWith(t, contractOut(ContractStateRunning, nil, nil))
	if n := countRows(t, f.s, `SELECT COUNT(*) FROM run WHERE kind = 'contract' AND reflect = 0`); n != 0 {
		t.Fatalf("contract runs without the reflect mark = %d, want 0", n)
	}
	if n := countRows(t, f.s, `SELECT COUNT(*) FROM run WHERE kind = 'contract' AND reflect = 1`); n != 2 {
		t.Fatalf("contract runs with the reflect mark = %d, want 2 (start and status)", n)
	}
}
