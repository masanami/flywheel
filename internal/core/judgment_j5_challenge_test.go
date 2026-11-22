package core

// このファイルは、課題の単位の確かめ（統合先を使う、検証中の課題を対象にする J5。区切り S3b の受入基準 AC-172〜AC-187・AC-192・AC-193・
// AC-200〜AC-207）を、実ストア・偽の判断の呼び出し・偽の契約のコマンドで固定する。基準 1 件ごとに、基準の文が述べる条件・結果・値を確かめる。

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// chkFixture は、統合先を使う課題（タスク a が済み・タスク b が取りやめ）が、上げる依頼のレビュー待ちを読んで検証中になった場面
// （反映の操作は未承認・上げる依頼は開いている）。
type chkFixture struct {
	*contractFixture
	j5 *j5Fixture
	id string
}

func newChkFixture(t *testing.T) *chkFixture {
	t.Helper()
	return newChkFixtureFacts(t, nil)
}

// newChkFixtureFacts は、偽の委譲先が、上げる依頼の状態の読みで facts を返す版（facts が nil なら返さない）。
func newChkFixtureFacts(t *testing.T, facts map[string]string) *chkFixture {
	t.Helper()
	f, id := newUpFixture(t, upConnectorsJSON(), multiTask{key: "a"}, multiTask{key: "b", deps: []string{"a"}})
	cid, _ := parseChallengeID(id)
	setTaskStatusRaw(t, f.s, cid, "b", TaskStatusCancelled)
	f.setUpPR("main")
	f.startUp(t)
	out := upReviewOut([]string{"respond", "land"}, upArtsOK...)
	if facts != nil {
		out.Status.Facts = facts
	}
	f.readWith(t, out)
	if got := f.statusOf(t, id); got != StatusVerifying {
		t.Fatalf("status = %s, want verifying", got)
	}
	j := &j5Fixture{delegateFixture: f.delegateFixture, checks: newFakeChecks(), j5: &fakeJudgmentInvoker{result: j5Out("met", nil, nil)}}
	pc := prChecks("open", checkDone)
	pc.URL = upPRURL
	j.checks.prs["o/r#7"] = pc
	j.j5.onInvoke = func(in JudgmentLaunchInput) { j.j5Inputs = append(j.j5Inputs, in) }
	return &chkFixture{contractFixture: f, j5: j, id: id}
}

func (f *chkFixture) verify(t *testing.T) *JudgmentAutoResult {
	t.Helper()
	res, err := f.j5.verify(t, &f.id)
	if err != nil {
		t.Fatalf("VerifyAutoJ5: %v", err)
	}
	return res
}

func (f *chkFixture) lastInput(t *testing.T) string {
	t.Helper()
	if len(f.j5.j5Inputs) == 0 {
		t.Fatal("J5 was not invoked")
	}
	return sectionsText(f.j5.j5Inputs[len(f.j5.j5Inputs)-1].Sections)
}

// hold は課題の最後の保留の、理由・戻る先・タスク・問い。
func (f *chkFixture) hold(t *testing.T) (reason, from string, taskID sql.NullInt64, question string) {
	t.Helper()
	cid, _ := parseChallengeID(f.id)
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COALESCE(reason, ''), from_status, task_id, question FROM hold WHERE challenge_id = ? ORDER BY id DESC LIMIT 1`, cid).Scan(&reason, &from, &taskID, &question)
	})
	return
}

func (f *chkFixture) j5RunTaskIDs(t *testing.T) []sql.NullInt64 {
	t.Helper()
	return (&reviewFlowFixture{contractFixture: f.contractFixture, j5: f.j5}).j5RunTaskIDs(t)
}

func (f *chkFixture) reflectEnded(t *testing.T) bool {
	t.Helper()
	_, _, _, ended := f.reflectRow(t)
	return ended
}

func (f *chkFixture) count(t *testing.T, q string, args ...any) (n int) {
	t.Helper()
	readForTest(t, f.s, func(tx *sql.Tx) error { return tx.QueryRow(q, args...).Scan(&n) })
	return n
}

// ---- 入力・実行の記録 ----

// AC-172・AC-173・AC-174・AC-175・AC-176
func TestChallengeCheck_InputHasTheTasksTheExpectedSceneThePRAndTheFactsAndTheRunHasNoTask(t *testing.T) {
	t.Parallel()
	// AC-175: 偽の委譲先が、状態の読みで facts を返す（ストアへ記録される経路を通す）。
	f := newChkFixtureFacts(t, map[string]string{"self_review": "complete", "zeta": "Z", "alpha": "A"})
	cid, _ := parseChallengeID(f.id)
	f.sqlExec(t, `UPDATE challenge SET expected = ? WHERE id = ?`, `[{"kind":"ui","scene":"EXPECTED-SCENE","before":"BEFORE-X","after":"AFTER-X"}]`, cid)
	f.j5.j5.result = j5Out("not_met", "FEEDBACK-TEXT", nil)
	f.verify(t)
	in := f.lastInput(t)
	secs := f.j5.j5Inputs[0].Sections
	section := func(label string) string {
		sec, ok := sectionByLabel(secs, label)
		if !ok {
			t.Fatalf("J5 input has no section %q:\n%s", label, in)
		}
		return sec.Content
	}
	// AC-172: 全部のタスクの題名（済み・取りやめの別と完了条件つき）。区画の中で確かめる。
	tasks := section(labelAllTasksSection)
	for _, want := range []string{"- [済み] タスク a\n  完了条件: 完了条件 a", "- [取りやめ] タスク b\n  完了条件: 完了条件 b"} {
		if !strings.Contains(tasks, want) {
			t.Errorf("the all-tasks section lacks %q:\n%s", want, tasks)
		}
	}
	// AC-173: 完了したら見えるもの（計画の背景の区画の中）。
	bg := section(labelPlanBackgroundSection)
	for _, want := range []string{"EXPECTED-SCENE", "BEFORE-X", "AFTER-X"} {
		if !strings.Contains(bg, want) {
			t.Errorf("the plan-background section lacks %q:\n%s", want, bg)
		}
	}
	// AC-174: 上げる依頼の成果物（PR）の参照。
	if !strings.Contains(in, upPRURL) {
		t.Errorf("J5 input lacks the PR %q:\n%s", upPRURL, in)
	}
	// AC-175: facts の名前と値（名前の辞書順）。
	facts := section(labelReflectFactsSection)
	for _, want := range []string{"- alpha: A", "- self_review: complete", "- zeta: Z"} {
		if !strings.Contains(facts, want) {
			t.Errorf("the facts section lacks %q:\n%s", want, facts)
		}
	}
	if strings.Index(facts, "- alpha: A") > strings.Index(facts, "- zeta: Z") {
		t.Error("the facts are not in name order")
	}
	if sec, ok := sectionByLabel(f.j5.j5Inputs[0].Sections, "達成条件"); !ok || !strings.Contains(sec.Content, j5ChallengeCheckNote) {
		t.Errorf("the criteria section lacks the challenge-check note: %+v", sec)
	}
	ids := f.j5RunTaskIDs(t)
	if len(ids) != 1 || ids[0].Valid { // AC-176
		t.Errorf("J5 run task_ids = %v, want one NULL", ids)
	}
}

// 統合先を使わない課題の J5 の入力には、課題の単位の確かめの区画が入らない。
func TestChallengeCheck_ANonIntegrationChallengeHasNoChallengeCheckSections(t *testing.T) {
	t.Parallel()
	f := newJ5Fixture(t, "impl")
	f.delegateToVerifying(t, "C-1")
	if _, err := f.verify(t, nil); err != nil {
		t.Fatal(err)
	}
	text := sectionsText(f.j5Inputs[0].Sections)
	for _, no := range []string{labelAllTasksSection, labelPlanBackgroundSection, j5ChallengeCheckNote} {
		if strings.Contains(text, no) {
			t.Errorf("a challenge without an integration target has %q in the J5 input", no)
		}
	}
}

// ---- 結果 ----

// AC-178
func TestChallengeCheck_MetMovesTheChallengeToAwaitingCompletionApproval(t *testing.T) {
	t.Parallel()
	f := newChkFixture(t)
	res := f.verify(t)
	if len(res.Items) != 1 || res.Items[0].Outcome != "met" {
		t.Fatalf("items = %+v, want met", res.Items)
	}
	if got := f.statusOf(t, f.id); got != StatusAwaitingCompletionApproval {
		t.Errorf("status = %s, want awaiting_completion_approval", got)
	}
}

// AC-179・AC-180・AC-181・AC-182
func TestChallengeCheck_NotMetHoldsAsChallengeCheckNotMetFromInProgressAndClosesTheOperation(t *testing.T) {
	t.Parallel()
	f := newChkFixture(t)
	f.j5.j5.result = j5Out("not_met", "FEEDBACK-TEXT", nil)
	res := f.verify(t)
	if len(res.Items) != 1 || res.Items[0].Outcome != "not_met" || res.Items[0].Status == nil || *res.Items[0].Status != string(StatusAwaitingHuman) {
		t.Fatalf("items = %+v, want not_met with awaiting_human", res.Items)
	}
	if got := f.statusOf(t, f.id); got != StatusAwaitingHuman { // AC-179
		t.Errorf("status = %s, want awaiting_human", got)
	}
	reason, from, taskID, question := f.hold(t)
	if reason != "challenge_check_not_met" { // AC-179
		t.Errorf("hold reason = %q, want challenge_check_not_met", reason)
	}
	wantLastHoldStop(t, f.s, stopNone)    // AC-17: 課題の単位の確かめの「満たしていない」は対象にしない
	if from != string(StatusInProgress) { // AC-180
		t.Errorf("hold from_status = %q, want in_progress", from)
	}
	if taskID.Valid {
		t.Errorf("hold task_id = %v, want NULL", taskID)
	}
	if strings.Contains(question, "challenge_check_not_met") { // AC-237: 問いの文に理由の値の語は出ない
		t.Errorf("the question names the reason value: %q", question)
	}
	if !strings.Contains(question, "FEEDBACK-TEXT") { // AC-181
		t.Errorf("the question lacks the J5 feedback: %q", question)
	}
	if !strings.Contains(question, "--replan") {
		t.Errorf("the question does not say that a replan answer can add tasks: %q", question)
	}
	for _, r := range f.releases(t) { // AC-182
		if r.state == "pending" || r.state == "approved" {
			t.Errorf("release %+v is still waiting", r)
		}
	}
	if rels := f.releases(t); len(rels) != 1 || rels[0].state != "closed" {
		t.Errorf("releases = %+v, want the one release closed", rels)
	}
	cid, _ := parseChallengeID(f.id)
	if got := taskStatusMap(t, f.s, cid); got["a"] != TaskStatusDone {
		t.Errorf("tasks = %v, want a done", got)
	}
	// 課題の単位の確かめの失敗は、確かめの失敗の発生に数える。
	if n := f.count(t, `SELECT COUNT(*) FROM occurrence WHERE kind = ?`, string(OccurrenceKindVerificationNotMet)); n != 1 {
		t.Errorf("verification_not_met occurrences = %d, want 1", n)
	}
}

// AC-200
func TestChallengeCheck_UncertainHoldsWithoutATask(t *testing.T) {
	t.Parallel()
	f := newChkFixture(t)
	f.j5.j5.result = j5Out("uncertain", nil, "QUESTION-TEXT")
	f.verify(t)
	if got := f.statusOf(t, f.id); got != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", got)
	}
	reason, from, taskID, question := f.hold(t)
	if taskID.Valid {
		t.Errorf("hold task_id = %v, want NULL", taskID)
	}
	if reason == "challenge_check_not_met" || from != string(StatusVerifying) || question != "QUESTION-TEXT" {
		t.Errorf("hold = (%q, %q, %q), want the ordinary uncertain hold from verifying", reason, from, question)
	}
}

// ---- 上げる依頼の取り消し ----

// AC-183
func TestChallengeCheck_NotMetCancelsTheRequestAtTheNextStatusCheckWhileHeld(t *testing.T) {
	t.Parallel()
	f := newChkFixture(t)
	f.j5.j5.result = j5Out("not_met", "FEEDBACK-TEXT", nil)
	f.verify(t)
	if f.reflectEnded(t) {
		t.Fatal("the reflect request must stay open until the next status check")
	}
	statuses := len(f.client.statuses)
	f.client.cancelOut = contractOut(ContractStateCancelled, nil, nil)
	f.runDelegationStage(t, nil)
	if len(f.client.cancels) != 1 || !reflect.DeepEqual(f.client.cancels[0], []string{"tool", "cancel", "ext-1"}) {
		t.Fatalf("cancels = %v, want one commands.cancel for the reflect request ext-1", f.client.cancels)
	}
	if len(f.client.statuses) != statuses {
		t.Errorf("commands.status was called %d times for the cancelled request", len(f.client.statuses)-statuses)
	}
	if got := f.statusOf(t, f.id); got != StatusAwaitingHuman {
		t.Errorf("status = %s, want awaiting_human (unchanged)", got)
	}
	if !f.reflectEnded(t) {
		t.Error("the reflect request is still open after it was cancelled")
	}
	if n := len(f.reflectStarts()); n != 1 {
		t.Errorf("commands.reflect calls = %d, want 1 (no new request while held)", n)
	}
}

// AC-184
func TestChallengeCheck_AFailedCancelPreventsANewRequestEvenAfterTheHoldIsAnswered(t *testing.T) {
	t.Parallel()
	f := newChkFixture(t)
	f.j5.j5.result = j5Out("not_met", "FEEDBACK-TEXT", nil)
	f.verify(t)
	f.client.cancelOut = ContractLaunchOutput{Result: RunResultErrored, ErrorSummary: "exit 1"}
	f.runDelegationStage(t, nil)
	if len(f.client.cancels) != 1 || f.reflectEnded(t) {
		t.Fatalf("cancels = %v, ended = %v; want one failed cancel and the record left open", f.client.cancels, f.reflectEnded(t))
	}
	f.answerLatestHold(t, f.id, "直した")
	f.runDelegationStage(t, nil)
	if n := len(f.reflectStarts()); n != 1 {
		t.Errorf("commands.reflect calls = %d, want 1 (no new request while the cancel is failing)", n)
	}
	if len(f.client.cancels) != 2 {
		t.Errorf("cancels = %d, want the cancel retried at the next status check", len(f.client.cancels))
	}
	// 取り消せたら、次の委譲の段が始め直す。
	f.client.cancelOut = contractOut(ContractStateCancelled, nil, nil)
	f.runDelegationStage(t, nil)
	f.runDelegationStage(t, nil)
	if n := len(f.reflectStarts()); n != 2 {
		t.Errorf("commands.reflect calls = %d, want 2 after the cancel succeeded", n)
	}
}

// AC-185・AC-186・AC-187
func TestChallengeCheck_AnAnswerWithoutReplanResumesAndTheCheckReadsTheAnswer(t *testing.T) {
	t.Parallel()
	f := newChkFixture(t)
	f.j5.j5.result = j5Out("not_met", "FEEDBACK-TEXT", nil)
	f.verify(t)
	f.client.cancelOut = contractOut(ContractStateCancelled, nil, nil)
	f.runDelegationStage(t, nil) // 取り消す
	f.answerLatestHold(t, f.id, "ANSWER-TEXT-統合先を直した")
	if got := f.statusOf(t, f.id); got != StatusInProgress { // AC-185
		t.Fatalf("status = %s, want in_progress", got)
	}
	f.client.uniqueRunIDs = true
	f.runDelegationStage(t, nil) // AC-186: 前の上げる依頼が終わっていれば、もう一度 commands.reflect
	if n := len(f.reflectStarts()); n != 2 {
		t.Fatalf("commands.reflect calls = %d, want 2", n)
	}
	f.client.echoRunning = false
	second := upReviewOut([]string{"respond", "land"}, upArtsOK...)
	second.Status.RunID = "ext-2"
	f.readWith(t, second)
	if got := f.statusOf(t, f.id); got != StatusVerifying {
		t.Fatalf("status = %s, want verifying", got)
	}
	f.j5.j5.result = j5Out("met", nil, nil)
	f.verify(t)
	if sec, ok := sectionByLabel(f.j5.j5Inputs[len(f.j5.j5Inputs)-1].Sections, labelChallengeCheckAnswerSection); !ok || !strings.Contains(sec.Content, "ANSWER-TEXT-統合先を直した") { // AC-187
		t.Errorf("the J5 input lacks the answer text:\n%s", f.lastInput(t))
	}
	// 確かめが済んだ後の確かめには、同じ回答を渡さない。
	if got := f.statusOf(t, f.id); got != StatusAwaitingCompletionApproval {
		t.Fatalf("status = %s, want awaiting_completion_approval", got)
	}
	cid, _ := parseChallengeID(f.id)
	f.sqlExec(t, `UPDATE challenge SET status = 'verifying' WHERE id = ?`, cid)
	f.sqlExec(t, `UPDATE run SET started_at = '2099-01-01T00:00:00.000Z' WHERE judgment = 'J5'`) // 回答より後に始まった確かめ
	f.verify(t)
	if _, ok := sectionByLabel(f.j5.j5Inputs[len(f.j5.j5Inputs)-1].Sections, labelChallengeCheckAnswerSection); ok {
		t.Errorf("the answer was given again to a check after the one that read it:\n%s", f.lastInput(t))
	}
}

// 計画を作り直す回答（--replan）・別の理由の保留への回答は、確かめの入力に入らない。
func TestChallengeCheck_TheAnswerIsOnlyReadForANotMetHoldWithoutReplan(t *testing.T) {
	t.Parallel()
	f := newChkFixture(t)
	f.sqlExec(t, `INSERT INTO hold (challenge_id, question, from_status, raised_at, answer, answered_at, answered_by, reason, replan)
		VALUES (1, 'Q', 'in_progress', '2026-11-01T00:00:00.000Z', 'REPLAN-ANSWER', '2026-11-01T00:00:01.000Z', 'alice', 'challenge_check_not_met', 1)`)
	f.verify(t)
	if _, ok := sectionByLabel(f.j5.j5Inputs[0].Sections, labelChallengeCheckAnswerSection); ok {
		t.Errorf("a replan answer is in the J5 input:\n%s", f.lastInput(t))
	}
}

// ---- 状態の確認 ----

// AC-201
func TestChallengeCheck_WhileTheCheckRunIsOpenTheRequestIsNotRead(t *testing.T) {
	t.Parallel()
	f := newChkFixture(t)
	cid, _ := parseChallengeID(f.id)
	var runID int64
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		now := formatTimestamp(f.s.currentTime())
		res, err := tx.Exec(`INSERT INTO run (challenge_id, kind, judgment, challenge_version, session_id, pid, host, heartbeat_at, started_at, max_budget_usd, budget_bucket)
			VALUES (?, 'judgment', 'J5', 1, '55555555-5555-4555-8555-555555555555', 1, 'h', ?, ?, 1, 'judgment')`, cid, now, now)
		if err != nil {
			return err
		}
		runID, err = res.LastInsertId()
		return err
	})
	_ = runID
	f.sqlExec(t, `UPDATE contract_run SET state = 'running', requested_kind = '' WHERE kind = 'reflect'`)
	statuses := len(f.client.statuses)
	f.client.echoRunning = true
	f.runDelegationStage(t, nil)
	if len(f.client.statuses) != statuses {
		t.Errorf("commands.status was called %d times while the check run is open", len(f.client.statuses)-statuses)
	}
	// 開いた記録が閉じれば、次の状態の確認は読む。
	f.sqlExec(t, `UPDATE run SET result = 'succeeded', ended_at = started_at WHERE judgment = 'J5'`)
	f.runDelegationStage(t, nil)
	if len(f.client.statuses) == statuses {
		t.Error("commands.status was not called after the check run ended")
	}
}

// ---- 完了から戻す（T18） ----

// AC-202: 完了から戻した後（上げる依頼は取り消されて終わっている）の J5 の入力に、反映の操作の成果物（PR）の参照があり、上げる依頼の facts は無い。
func TestChallengeCheck_AfterReopeningTheInputHasTheOperationsPRAndNoFacts(t *testing.T) {
	t.Parallel()
	f := newChkFixture(t)
	f.sqlExec(t, `UPDATE contract_run SET facts = '{"k":"FACT-VALUE"}' WHERE kind = 'reflect'`)
	f.sqlExec(t, `UPDATE contract_run SET ended_at = observed_at, state = 'cancelled' WHERE kind = 'reflect'`) // 完了から戻す前に取り消された
	f.verify(t)
	in := f.lastInput(t)
	if !strings.Contains(in, upPRURL) {
		t.Errorf("the J5 input lacks the PR of the operation:\n%s", in)
	}
	if strings.Contains(in, "FACT-VALUE") || strings.Contains(in, labelReflectFactsSection) {
		t.Errorf("the J5 input has facts of an ended request:\n%s", in)
	}
	if !strings.Contains(in, j5ChallengeCheckNote) {
		t.Errorf("the check after reopening is not the challenge-level check:\n%s", in)
	}
}

// AC-192・AC-193
func TestChallengeCheck_ReopenKeepsTheTasksDoneOnlyForAnIntegrationChallenge(t *testing.T) {
	t.Parallel()
	for _, integ := range []bool{true, false} {
		f := newReflectReopenFixture(t)
		f.cancelsWith()
		cid, _ := parseChallengeID(f.cid)
		if integ {
			writeForTest(t, f.s, func(tx *sql.Tx) error {
				_, err := tx.Exec(`UPDATE challenge SET uses_integration = 1 WHERE id = ?`, cid)
				return err
			})
		}
		before := taskStatusMap(t, f.s, cid)
		for k, v := range before {
			if v != TaskStatusDone {
				t.Fatalf("fixture task %s = %s, want done", k, v)
			}
		}
		res, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, f.reopenInput(f.contentChangedCompare()))
		if err != nil || !res.Reopened {
			t.Fatalf("integ=%v: reopen = %+v err %v, want reopened", integ, res, err)
		}
		if got := f.status(t, f.cid); got != StatusVerifying {
			t.Errorf("integ=%v: status = %s, want verifying", integ, got)
		}
		want := TaskStatusVerifying // AC-193: S3a と同じ
		if integ {
			want = TaskStatusDone // AC-192
		}
		for k, v := range taskStatusMap(t, f.s, cid) {
			if v != want {
				t.Errorf("integ=%v: task %s = %s, want %s", integ, k, v, want)
			}
		}
	}
}

// ---- 完了の差し戻し（T15） ----

func (f *chkFixture) rejectCompletion(t *testing.T, reason string) {
	t.Helper()
	ctx := context.Background()
	f.j5.j5.result = j5Out("met", nil, nil)
	f.verify(t)
	if got := f.statusOf(t, f.id); got != StatusAwaitingCompletionApproval {
		t.Fatalf("status = %s, want awaiting_completion_approval", got)
	}
	prev, err := f.s.PrepareRejection(ctx, f.id, reason)
	if err != nil {
		t.Fatalf("PrepareRejection: %v", err)
	}
	if _, _, err := f.s.ExecuteApproval(ctx, ApprovalRequest{ChallengeID: f.id, ExpectedVersion: prev.Version,
		Decision: ApprovalDecisionRejected, Reason: &reason}, ttyAtt(t, f.id)); err != nil {
		t.Fatalf("ExecuteApproval(reject): %v", err)
	}
}

// AC-203・AC-204・AC-205・AC-206
func TestChallengeRejection_IntegrationKeepsTheTasksClosesTheOperationFeedsJ2AndCancelsTheRequest(t *testing.T) {
	t.Parallel()
	f := newChkFixture(t)
	cid, _ := parseChallengeID(f.id)
	f.rejectCompletion(t, "REJECT-REASON-差し戻し")
	if got := f.statusOf(t, f.id); got != StatusClassified {
		t.Errorf("status = %s, want classified (the replan path)", got)
	}
	if got := taskStatusMap(t, f.s, cid); got["a"] != TaskStatusDone || got["b"] != TaskStatusCancelled { // AC-203
		t.Errorf("tasks = %v, want a done and b cancelled", got)
	}
	if rels := f.releases(t); len(rels) != 1 || rels[0].state != "closed" { // AC-204
		t.Errorf("releases = %+v, want the one release closed", rels)
	}
	// AC-205: その後の J2 の入力に、差し戻しの理由がある。
	f.agent.PositionFile = "pos.md"
	if err := os.WriteFile(filepath.Join(f.s.workspace, f.agent.PositionFile), []byte("POSITION"), 0o644); err != nil {
		t.Fatal(err)
	}
	ch := f.detail(t, f.id).Challenge
	prepared, ns, err := f.s.prepareJ2(context.Background(), ch, J2AutoInput{AgentDecl: f.agent, Upstream: f.upstream, ConnDecl: f.conn})
	if err != nil || ns != nil {
		t.Fatalf("prepareJ2: ns %+v err %v", ns, err)
	}
	sec, ok := sectionByLabel(prepared.Sections, CompletionRejectionsForReplanSectionLabel)
	if !ok || !strings.Contains(sec.Content, "REJECT-REASON-差し戻し") || !strings.Contains(sec.Content, "済みのタスクはやり直さず") {
		t.Errorf("the J2 input lacks the rejection reason: %+v\n%s", sec, sectionsText(prepared.Sections))
	}
	// AC-206: その後の状態の確認で、上げる依頼に commands.cancel が呼ばれる（課題は分類済のまま）。
	f.client.cancelOut = contractOut(ContractStateCancelled, nil, nil)
	f.runDelegationStage(t, nil)
	if len(f.client.cancels) != 1 {
		t.Fatalf("cancels = %v, want one commands.cancel", f.client.cancels)
	}
	if !f.reflectEnded(t) {
		t.Error("the reflect request is still open")
	}
	if got := f.statusOf(t, f.id); got != StatusClassified {
		t.Errorf("status = %s, want classified (unchanged)", got)
	}
}

// 作り直した計画で、前の完了の差し戻しの理由は、次の J2 の入力に残さない（最新の計画より前の差し戻しは渡さない）。
func TestChallengeRejection_ARejectionBeforeTheLatestPlanIsNotPassedToJ2(t *testing.T) {
	t.Parallel()
	f := newChkFixture(t)
	f.rejectCompletion(t, "OLD-REASON")
	f.sqlExec(t, `UPDATE approval SET decided_at = '2099-01-01T00:00:00.000Z' WHERE kind = 'plan'`)
	f.agent.PositionFile = "pos.md"
	if err := os.WriteFile(filepath.Join(f.s.workspace, f.agent.PositionFile), []byte("POSITION"), 0o644); err != nil {
		t.Fatal(err)
	}
	ch := f.detail(t, f.id).Challenge
	prepared, _, err := f.s.prepareJ2(context.Background(), ch, J2AutoInput{AgentDecl: f.agent, Upstream: f.upstream, ConnDecl: f.conn})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sectionByLabel(prepared.Sections, CompletionRejectionsForReplanSectionLabel); ok {
		t.Errorf("a rejection older than the latest plan is in the J2 input:\n%s", sectionsText(prepared.Sections))
	}
}

// AC-207: 統合先を使わない課題の完了の差し戻しは、S3a と同じに、タスクを in_progress にし、課題は着手中へ戻る。
func TestChallengeRejection_NonIntegrationReturnsTheTaskToInProgress(t *testing.T) {
	t.Parallel()
	f := newJ5Fixture(t, "impl")
	f.delegateAndVerifyMet(t, "C-1")
	f.rejectCompletionForTest(t, "C-1", "REASON", rejDay(2026, 11, 4), ttyAtt)
	cid, _ := parseChallengeID("C-1")
	for k, v := range taskStatusMap(t, f.s, cid) {
		if v != TaskStatusInProgress {
			t.Errorf("task %s = %s, want in_progress", k, v)
		}
	}
	if got := f.detail(t, "C-1").Status; got != StatusInProgress {
		t.Errorf("status = %s, want in_progress", got)
	}
}

// 課題の単位の確かめが読んだ後に、課題が検証中でなくなっていたら（保留・取り下げなど）、結果は書かない（保留も、反映の操作を閉じることも起きない）。
func TestChallengeCheck_NotMetIsNotWrittenWhenTheChallengeIsNoLongerVerifying(t *testing.T) {
	t.Parallel()
	f := newChkFixture(t)
	f.j5.j5.result = j5Out("not_met", "FEEDBACK-TEXT", nil)
	cid, _ := parseChallengeID(f.id)
	inner := f.j5.j5.onInvoke
	f.j5.j5.onInvoke = func(in JudgmentLaunchInput) {
		inner(in)
		f.sqlExec(t, `UPDATE challenge SET status = 'in_progress' WHERE id = ?`, cid)
	}
	res := f.verify(t)
	if len(res.Items) != 1 || res.Items[0].Outcome != "not_met" || res.Items[0].Status != nil {
		t.Fatalf("items = %+v, want the verdict recorded but no state written", res.Items)
	}
	if got := f.statusOf(t, f.id); got != StatusInProgress {
		t.Errorf("status = %s, want in_progress (unchanged)", got)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM hold WHERE challenge_id = ?`, cid); n != 0 {
		t.Errorf("holds = %d, want none", n)
	}
	if rels := f.releases(t); len(rels) != 1 || rels[0].state != "pending" {
		t.Errorf("releases = %+v, want the one release still pending", rels)
	}
}

// 返さない facts は、区画ごと出さない。
func TestChallengeCheck_NoFactsReturnedMeansNoFactsSection(t *testing.T) {
	t.Parallel()
	f := newChkFixture(t)
	f.verify(t)
	if _, ok := sectionByLabel(f.j5.j5Inputs[0].Sections, labelReflectFactsSection); ok {
		t.Errorf("the facts section is in the J5 input although the delegate returned no facts:\n%s", f.lastInput(t))
	}
}

// 別の理由の保留への回答は、確かめの入力に入らない。
func TestChallengeCheck_TheAnswerOfAnotherHoldReasonIsNotRead(t *testing.T) {
	t.Parallel()
	f := newChkFixture(t)
	f.sqlExec(t, `INSERT INTO hold (challenge_id, question, from_status, raised_at, answer, answered_at, answered_by, reason, replan)
		VALUES (1, 'Q', 'in_progress', '2026-11-01T00:00:00.000Z', 'OTHER-ANSWER', '2026-11-01T00:00:01.000Z', 'alice', 'reflect_failed', 0)`)
	f.verify(t)
	if _, ok := sectionByLabel(f.j5.j5Inputs[0].Sections, labelChallengeCheckAnswerSection); ok {
		t.Errorf("the answer of another hold reason is in the J5 input:\n%s", f.lastInput(t))
	}
}

// AC-202（完了から戻す操作を通す）: 完了から戻した（上げる依頼は戻す前に取り消された）、統合先を使う課題の J5 は課題の単位の確かめで、入力に反映の操作の成果物（PR）の
// 参照があり、タスクごとの検証はやり直さない（タスクは済みのまま）。そこで「満たしていない」なら、置き換えて作った新しい反映の操作を閉じて保留にする。
func TestChallengeCheck_AfterARealReopenTheCheckReadsThePRAndNotMetClosesTheNewOperation(t *testing.T) {
	t.Parallel()
	f := newReflectReopenFixture(t)
	f.cancelsWith()
	cid, _ := parseChallengeID(f.cid)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE challenge SET uses_integration = 1 WHERE id = ?`, cid); err != nil {
			return err
		}
		// 上げる依頼の取り込み済みの成果物（上げる依頼の実行の印）。
		_, err := tx.Exec(`UPDATE run SET reflect = 1 WHERE id IN (SELECT run_id FROM run_artifact WHERE kind = 'pr')`)
		return err
	})
	res, err := f.s.ReopenChallengeForRelease(context.Background(), ChannelCLI, f.reopenInput(f.contentChangedCompare()))
	if err != nil || !res.Reopened {
		t.Fatalf("reopen = %+v err %v, want reopened", res, err)
	}
	if len(f.client.cancels) != 1 {
		t.Fatalf("cancels = %v, want the reflect request cancelled before reopening", f.client.cancels)
	}
	f.j5.j5.onInvoke = func(in JudgmentLaunchInput) { f.j5.j5Inputs = append(f.j5.j5Inputs, in) }
	f.j5.j5.result = j5Out("not_met", "FEEDBACK-TEXT", nil)
	if _, err := f.j5.verify(t, &f.cid); err != nil {
		t.Fatalf("VerifyAutoJ5: %v", err)
	}
	if len(f.j5.j5Inputs) != 1 {
		t.Fatalf("J5 inputs = %d, want 1", len(f.j5.j5Inputs))
	}
	in := sectionsText(f.j5.j5Inputs[0].Sections)
	if !strings.Contains(in, reviewPRURL) || !strings.Contains(in, j5ChallengeCheckNote) {
		t.Errorf("the check after reopening lacks the PR %q or is not the challenge-level check:\n%s", reviewPRURL, in)
	}
	for k, v := range taskStatusMap(t, f.s, cid) {
		if v != TaskStatusDone {
			t.Errorf("task %s = %s, want done", k, v)
		}
	}
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Errorf("status = %s, want awaiting_human", got)
	}
	var waiting int
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM operation WHERE challenge_id = ? AND state IN ('pending', 'approved')`, cid).Scan(&waiting)
	})
	if waiting != 0 {
		t.Errorf("%d release operations still waiting after the check was not met", waiting)
	}
}

// AC-176（確かめの対象がちょうど 1 個の検証中のタスクを持つ場合でも）: 課題の単位の確かめの実行の記録は、どのタスクにも結び付かない。
func TestChallengeCheck_TheRunHasNoTaskEvenWithASingleVerifyingTask(t *testing.T) {
	t.Parallel()
	f := newChkFixture(t)
	cid, _ := parseChallengeID(f.id)
	setTaskStatusRaw(t, f.s, cid, "a", TaskStatusVerifying)
	f.verify(t)
	ids := f.j5RunTaskIDs(t)
	if len(ids) != 1 || ids[0].Valid {
		t.Errorf("J5 run task_ids = %v, want one NULL", ids)
	}
	if _, ok := sectionByLabel(f.j5.j5Inputs[0].Sections, labelAllTasksSection); !ok {
		t.Errorf("the check is not the challenge-level check:\n%s", f.lastInput(t))
	}
}
