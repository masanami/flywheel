package core

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// 委譲へ渡す取り込み元（差し込み）をタスクが対応する子から決めること、タスクが対応する子が閉じた・無いときに
// 起動しないことの試験（#778。S2b）。各試験の名前に対応する受入基準（AC-<番号>）を書く。

const (
	srcRootKey  = "o/r#7"
	srcChildKey = "o/r#12"
)

// useSourceOperations は f の接続ツールの impl-src の invocation を、3 つの差し込みを全部持つ形にする。
func (f *delegateFixture) useSourceOperations(t *testing.T) {
	t.Helper()
	json := strings.Replace(delegateConnectorsJSON, `"/h:impl {issue_number} {challenge_id}"`, `"/h:impl {issue_number} {issue_url} {external_key}"`, 1)
	conn, err := parseConnectorsDeclaration([]byte(json))
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.validate(); err != nil {
		t.Fatal(err)
	}
	f.conn = conn
}

// newSourceChallenge は取り込み元 o/r#7 に対応する着手中の課題を作る（タスクは impl-src の 1 個）。
func (f *delegateFixture) newSourceChallenge(t *testing.T, title string) string {
	t.Helper()
	id := f.newInProgress(t, title, "P1", planSpec(func(m map[string]any) { m["operation"] = "impl-src" }))
	if _, err := createSourceBindingForTest(f.s, id, time.Now(), validBindingInput(srcRootKey)); err != nil {
		t.Fatalf("bind: %v", err)
	}
	th := simpleThread("UP-BODY")
	th.Issue.ExternalKey, th.Issue.Repo, th.Issue.Number, th.Issue.State = srcRootKey, "o/r", 7, "open"
	f.upstream.threads[srcRootKey] = th
	return id
}

func (f *delegateFixture) addSourceChild(t *testing.T, id, key, state string) {
	t.Helper()
	cid, _ := parseChallengeID(id)
	err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO source_child (challenge_id, source_id, external_key, parent_external_key, url, title, body, upstream_state, created_at, updated_at)
			VALUES (?, 'src', ?, ?, ?, 'CHILD', 'BODY', ?, '2026-09-25T00:00:00.000Z', '2026-09-25T00:00:00.000Z')`,
			cid, key, srcRootKey, "https://example.invalid/"+key, state)
		return err
	})
	if err != nil {
		t.Fatalf("addSourceChild: %v", err)
	}
}

// pointTasksAt は課題の全タスクの source_key を key にし、状態を status にする（key が "" なら NULL のまま）。
func (f *delegateFixture) pointTasksAt(t *testing.T, id, key string, status TaskStatus) {
	t.Helper()
	cid, _ := parseChallengeID(id)
	err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		if key != "" {
			if _, err := tx.Exec(`UPDATE task SET source_key = ? WHERE challenge_id = ?`, key, cid); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`UPDATE task SET status = ? WHERE challenge_id = ?`, string(status), cid)
		return err
	})
	if err != nil {
		t.Fatalf("pointTasksAt: %v", err)
	}
}

// launchedInvocation は課題を委譲して、委譲先へ渡した指示文と取り込み元を返す。
func (f *delegateFixture) launchedWithChild(t *testing.T) DelegateLaunchInput {
	t.Helper()
	f.useSourceOperations(t)
	id := f.newSourceChallenge(t, "child")
	f.addSourceChild(t, id, srcChildKey, "open")
	f.pointTasksAt(t, id, srcChildKey, TaskStatusPending)
	f.mustRun(t, nil)
	got := f.deleg.launched()
	if len(got) != 1 {
		t.Fatalf("launched = %d, want 1", len(got))
	}
	return got[0]
}

func TestDelegateSource_AC182_IssueNumberIsTheChildsNumber(t *testing.T) { // #778
	t.Parallel()
	in := newDelegateFixture(t).launchedWithChild(t)
	if !strings.HasPrefix(in.Invocation, "/h:impl 12 ") || in.SourceIssueNumber != 12 {
		t.Errorf("AC-182 #778: invocation = %q source number = %d, want the child's number 12", in.Invocation, in.SourceIssueNumber)
	}
}

func TestDelegateSource_AC183_IssueURLIsTheChildsURL(t *testing.T) { // #778
	t.Parallel()
	in := newDelegateFixture(t).launchedWithChild(t)
	want := "https://example.invalid/" + srcChildKey
	if !strings.Contains(in.Invocation, " "+want+" ") || in.SourceIssueURL != want {
		t.Errorf("AC-183 #778: invocation = %q source url = %q, want the child's URL", in.Invocation, in.SourceIssueURL)
	}
}

func TestDelegateSource_AC184_ExternalKeyIsTheChildsKey(t *testing.T) { // #778
	t.Parallel()
	in := newDelegateFixture(t).launchedWithChild(t)
	if !strings.HasSuffix(in.Invocation, " "+srcChildKey) {
		t.Errorf("AC-184 #778: invocation = %q, want it to end with the child's external_key", in.Invocation)
	}
}

func TestDelegateSource_AC185_TaskWithoutAChildUsesTheChallengesSource(t *testing.T) { // #778
	t.Parallel()
	f := newDelegateFixture(t)
	f.useSourceOperations(t)
	f.newSourceChallenge(t, "root")
	f.mustRun(t, nil)
	got := f.deleg.launched()
	if len(got) != 1 || !strings.HasPrefix(got[0].Invocation, "/h:impl 7 ") || !strings.HasSuffix(got[0].Invocation, " "+srcRootKey) || got[0].SourceIssueNumber != 7 {
		t.Fatalf("AC-185 #778: launched = %+v, want the challenge's source (7) as before", got)
	}
}

// notStartedFor は、タスクが対応する子（state）を持つ課題に委譲の段を実行した結果の起動しなかった理由を返す。
func notStartedFor(t *testing.T, state string, status TaskStatus) (*delegateFixture, *DelegateResult) {
	t.Helper()
	f := newDelegateFixture(t)
	f.useSourceOperations(t)
	id := f.newSourceChallenge(t, "gate")
	f.addSourceChild(t, id, srcChildKey, state)
	f.pointTasksAt(t, id, srcChildKey, status)
	return f, mustRunResult(t, f, nil)
}

func TestDelegateSource_AC186_PendingTaskOfAClosedChildIsNotLaunched(t *testing.T) { // #778
	t.Parallel()
	f, res := notStartedFor(t, "closed", TaskStatusPending)
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedUpstreamClosed || len(f.deleg.launched()) != 0 || len(res.Items) != 0 {
		t.Errorf("AC-186 #778: res = %+v, want not_started upstream_closed and no launch", res)
	}
}

func TestDelegateSource_AC187_PendingTaskOfAMissingChildIsNotLaunched(t *testing.T) { // #778
	t.Parallel()
	f, res := notStartedFor(t, "missing", TaskStatusPending)
	if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedUpstreamMissing || len(f.deleg.launched()) != 0 || len(res.Items) != 0 {
		t.Errorf("AC-187 #778: res = %+v, want not_started upstream_missing and no launch", res)
	}
}

func TestDelegateSource_AC188_TaskWhoseChildRecordIsGoneIsNotLaunched(t *testing.T) { // #778
	t.Parallel()
	for _, status := range []TaskStatus{TaskStatusPending, TaskStatusInProgress} {
		f := newDelegateFixture(t)
		f.useSourceOperations(t)
		id := f.newSourceChallenge(t, "gone")
		f.pointTasksAt(t, id, "o/r#404", status) // 子の記録が無い（unbind の後に別の Issue へ bind した場合と同じ）
		res := mustRunResult(t, f, nil)
		if len(res.NotStarted) != 1 || res.NotStarted[0].Reason != NotStartedUpstreamMissing || len(f.deleg.launched()) != 0 {
			t.Errorf("AC-188 #778: status %s: res = %+v, want not_started upstream_missing (the source is not switched to the challenge's)", status, res)
		}
	}
}

func TestDelegateSource_AC189_InProgressTaskOfAClosedChildIsNotExcludedAsClosed(t *testing.T) { // #778
	t.Parallel()
	f, res := notStartedFor(t, "closed", TaskStatusInProgress)
	for _, ns := range res.NotStarted {
		if ns.Reason == NotStartedUpstreamClosed {
			t.Errorf("AC-189 #778: an in_progress task got upstream_closed: %+v", ns)
		}
	}
	if len(f.deleg.launched()) != 1 {
		t.Errorf("AC-189 #778: the in_progress task should be resumed/redone, launched = %d", len(f.deleg.launched()))
	}
}

// 進められるものの数えが使う選び方（selectDelegationTargets）の「除く理由」に、タスクの分が入る。
func TestDelegateSource_CycleWorkCountExcludesTasksOfClosedAndMissingChildren(t *testing.T) { // #778
	t.Parallel()
	f := newDelegateFixture(t)
	f.useSourceOperations(t)
	closed := f.newSourceChallenge(t, "closed")
	f.addSourceChild(t, closed, srcChildKey, "closed")
	f.pointTasksAt(t, closed, srcChildKey, TaskStatusPending)
	var targets []int64
	var skipped []NotStarted
	err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
		targets, skipped, err = selectDelegationTargets(context.Background(), tx)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 || len(skipped) != 1 || skipped[0].Reason != NotStartedUpstreamClosed {
		t.Errorf("targets = %v skipped = %+v, want the challenge excluded with upstream_closed", targets, skipped)
	}
}

// contractSourceJSON は契約のコマンドが差し込みを全部持つ宣言。
func contractSourceJSON() string {
	r := strings.NewReplacer(
		`"start": ["tool", "start", "--challenge", "{challenge_id}"]`, `"start": ["tool", "start", "{issue_number}", "{issue_url}", "{external_key}"]`,
		`"status": ["tool", "status", "{run_id}"]`, `"status": ["tool", "status", "{run_id}", "{external_key}"]`,
		`"resume": ["tool", "resume", "{run_id}", "--answer", "{answer}"]`, `"resume": ["tool", "resume", "{run_id}", "--answer", "{answer}", "{issue_number}"]`,
		`"cancel": ["tool", "cancel", "{run_id}"]`, `"cancel": ["tool", "cancel", "{run_id}", "{issue_number}"]`,
	)
	return r.Replace(contractConnectorsJSON)
}

// newContractChildFixture は、子 o/r#12 に対応するタスクを持つ契約の形の課題で、契約の run を start した状態にする。
func newContractChildFixture(t *testing.T) *contractFixture {
	t.Helper()
	f := newContractFixtureWith(t, contractSourceJSON())
	if _, err := createSourceBindingForTest(f.s, f.cid, time.Now(), validBindingInput(srcRootKey)); err != nil {
		t.Fatal(err)
	}
	f.addSourceChild(t, f.cid, srcChildKey, "open")
	f.pointTasksAt(t, f.cid, srcChildKey, TaskStatusPending)
	if _, err := f.start(t); err != nil {
		t.Fatalf("start: %v", err)
	}
	return f
}

func TestDelegateSource_ContractStartUsesTheChildsValues(t *testing.T) { // #778
	t.Parallel()
	f := newContractChildFixture(t)
	want := []string{"tool", "start", "12", "https://example.invalid/" + srcChildKey, srcChildKey}
	if len(f.client.starts) != 1 || strings.Join(f.client.starts[0], " ") != strings.Join(want, " ") {
		t.Errorf("start = %v, want %v", f.client.starts, want)
	}
}

func TestDelegateSource_AC190_ContractResumeUsesTheChildsNumber(t *testing.T) { // #778
	t.Parallel()
	f := newContractChildFixture(t)
	f.client.queue = []ContractLaunchOutput{waitingOut(&ContractRequestedAction{Kind: "approve", Decider: "human", Text: "承認して"}, nil)}
	f.poll(t)
	f.answerLatestHold(t, f.cid, "ANSWER-MARKER")
	f.client.resumeOut = contractOut(ContractStateRunning, nil, nil)
	f.poll(t)
	if len(f.client.resumes) != 1 || f.client.resumes[0][len(f.client.resumes[0])-1] != "12" {
		t.Errorf("AC-190 #778: resumes = %v, want the last argument (issue_number) to be the child's 12", f.client.resumes)
	}
}

func TestDelegateSource_AC191_ContractStatusUsesTheChildsExternalKey(t *testing.T) { // #778
	t.Parallel()
	f := newContractChildFixture(t)
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, nil, nil)}
	f.poll(t)
	if len(f.client.statuses) != 1 || f.client.statuses[0][len(f.client.statuses[0])-1] != srcChildKey {
		t.Errorf("AC-191 #778: statuses = %v, want the last argument (external_key) to be the child's %s", f.client.statuses, srcChildKey)
	}
}

func TestDelegateSource_ContractCancelUsesTheChildsNumber(t *testing.T) { // #778
	t.Parallel()
	f := newContractChildFixture(t)
	f.client.cancelOut = contractOut(ContractStateCancelled, nil, nil)
	if _, err := f.s.CancelOpenContractRuns(context.Background(), ContractCancelInput{ChallengeID: f.cid, AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client}); err != nil {
		t.Fatalf("CancelOpenContractRuns: %v", err)
	}
	if len(f.client.cancels) != 1 || f.client.cancels[0][len(f.client.cancels[0])-1] != "12" {
		t.Errorf("cancels = %v, want the last argument to be the child's number 12", f.client.cancels)
	}
}
