package core

// このファイルは、区切り S4c-3 のうち「タスクを止める保留だけが開いている課題で、保留が結び付くタスク以外を進める・結び付くタスクを止める」を、
// 実ストア・偽の契約のコマンドで検証する。タスク a（保留が立つ）とタスク b（別のタスク）は、どちらも統合先を使う課題の検証中のタスクで、
// 契約の実行はそれぞれ ext-1・ext-2 のレビュー待ち。受入基準の番号（AC-n）は、親の Issue の本文の「## 受入基準」の並びの順。

import (
	"context"
	"database/sql"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// byRunClient は、状態の確認（status）を、読む実行 ID ごとの出力で返す偽の契約のコマンド。
type byRunClient struct {
	*fakeContractClient
	outs map[string]ContractLaunchOutput
}

func (c byRunClient) Status(ctx context.Context, in ContractLaunchInput) (ContractLaunchOutput, error) {
	if len(in.Command) > 2 {
		if out, ok := c.outs[in.Command[2]]; ok {
			c.mu.Lock()
			c.statuses = append(c.statuses, in.Command)
			c.mu.Unlock()
			return out, nil
		}
	}
	return c.fakeContractClient.Status(ctx, in)
}

type proceedFixture struct {
	*igLandFixture
	taskA, taskB int64
}

const (
	extA = "ext-1"
	extB = "ext-2"
)

func newProceedFixture(t *testing.T) *proceedFixture {
	t.Helper()
	f := newIgLandFixture(t)
	f.addSecondVerifyingTask(t, "")
	pf := &proceedFixture{igLandFixture: f}
	pf.taskA = f.count(t, `SELECT id FROM task WHERE key <> 'second'`)
	pf.taskB = f.count(t, `SELECT id FROM task WHERE key = 'second'`)
	return pf
}

func runningOf(ext string) ContractLaunchOutput {
	return contractOut(ContractStateRunning, nil, func(st *ContractStatus) { st.RunID = ext })
}

func reviewOf(ext, content string) ContractLaunchOutput {
	return contractOut(ContractStateWaiting, nil, func(st *ContractStatus) {
		st.RunID = ext
		st.RequestedAction = &ContractRequestedAction{Kind: "review", Decider: "any", Text: "レビュー待ち", Options: integOptsWithLd}
		st.Artifacts = []ContractArtifact{{Kind: "commit", Ref: content}, integBranchArt, integCommitArt}
	})
}

func observeOf(ext string) ContractLaunchOutput {
	return contractOut(ContractStateWaiting, nil, func(st *ContractStatus) {
		st.RunID = ext
		st.RequestedAction = &ContractRequestedAction{Kind: "observe", Decider: "any", Text: "見ている"}
	})
}

// pollBy は、実行 ID ごとの出力で契約の実行を 1 回読む。
func (f *proceedFixture) pollBy(t *testing.T, outs map[string]ContractLaunchOutput) {
	t.Helper()
	if _, err := f.s.PollContractRuns(context.Background(), ContractPollInput{
		AgentDecl: f.agent, ConnDecl: f.conn, Client: byRunClient{fakeContractClient: f.client, outs: outs}, CycleID: f.cycle(t, 300),
		Reconcile: f.branches, Checks: f.checks, Compare: f.compare,
	}); err != nil {
		t.Fatalf("PollContractRuns: %v", err)
	}
}

// holdTaskA はタスク a を止める保留を立てる（課題は人間対応待ちになる）。
func (f *proceedFixture) holdTaskA(t *testing.T) {
	t.Helper()
	f.holdTask(t, f.taskA)
}

func (f *proceedFixture) holdTask(t *testing.T, task int64) {
	t.Helper()
	if _, err := f.s.transition(context.Background(), ChannelCLI, f.cid, OpHold, func(ctx context.Context, tc *transitionCtx) error {
		tc.taskID = &task
		_, err := insertHoldWithOptions(ctx, tc, "q", HoldKindHuman, tc.current.Status, tc.taskID, HoldOptionsInput{Scope: HoldScopeTask})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Fatalf("precondition: status = %s, want awaiting_human", got)
	}
}

// holdChallenge は課題を止める保留を立てる。
func (f *proceedFixture) holdChallenge(t *testing.T) {
	t.Helper()
	if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, f.cid, HoldInput{Question: "課題を止める"}); err != nil {
		t.Fatal(err)
	}
}

func (f *proceedFixture) taskStatusOf(t *testing.T, task int64) string {
	t.Helper()
	return f.text(t, `SELECT status FROM task WHERE id = `+strconvI(task))
}

func strconvI(n int64) string { return strconv.FormatInt(n, 10) }

func (f *proceedFixture) statusReads() []string {
	var out []string
	for _, c := range f.client.statuses {
		out = append(out, c[2])
	}
	sort.Strings(out)
	return out
}

func (f *proceedFixture) resumeRuns() []string {
	var out []string
	for _, c := range f.client.resumes {
		out = append(out, c[2])
	}
	sort.Strings(out)
	return out
}

// 両方のタスクを、まだ成果物を受け取っていない（着手中）の状態に戻す。
func (f *proceedFixture) resetToInProgress(t *testing.T) {
	t.Helper()
	f.sql(t, `UPDATE task SET status = 'in_progress'`)
	f.sql(t, `UPDATE contract_run SET content_ref = NULL, verified_ref = NULL`)
}

// タスク a を止める保留が開いている間、タスク b の走っている実行の状態の確認は行われる（AC-24）。タスク a の状態の確認も行われる（AC-27）。
func TestHoldTaskProceed_AC24_AC27_StatusIsReadForBothTasks(t *testing.T) {
	t.Parallel()
	f := newProceedFixture(t)
	f.holdTaskA(t)
	f.pollBy(t, map[string]ContractLaunchOutput{extA: runningOf(extA), extB: runningOf(extB)})
	if got := f.statusReads(); strings.Join(got, ",") != extA+","+extB {
		t.Errorf("status reads = %v, want both %s and %s", got, extA, extB)
	}
}

// タスク a を止める保留が開いている間、タスク b のレビュー待ちは受け取られる（AC-20）。タスク a のレビュー待ちは受け取られない（AC-28）。
func TestHoldTaskProceed_AC20_AC28_ReviewWaitIsReceivedOnlyForTheTaskWithoutAHold(t *testing.T) {
	t.Parallel()
	f := newProceedFixture(t)
	f.resetToInProgress(t)
	f.holdTaskA(t)
	f.pollBy(t, map[string]ContractLaunchOutput{extA: reviewOf(extA, landContent), extB: reviewOf(extB, "beef000")})
	if got := f.taskStatusOf(t, f.taskB); got != "verifying" {
		t.Errorf("task b = %s, want verifying（レビュー待ちを受け取る。AC-20）", got)
	}
	if got := f.taskStatusOf(t, f.taskA); got != "in_progress" {
		t.Errorf("task a = %s, want in_progress（レビュー待ちを受け取らない。AC-28）", got)
	}
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Errorf("status = %s, want awaiting_human（保留は a のまま）", got)
	}
}

// 課題を止める保留が開いている間は、タスク b のレビュー待ちも受け取られない（AC-32）。
func TestHoldTaskProceed_AC32_ChallengeHoldReceivesNoReviewWait(t *testing.T) {
	t.Parallel()
	f := newProceedFixture(t)
	f.resetToInProgress(t)
	f.holdChallenge(t)
	f.pollBy(t, map[string]ContractLaunchOutput{extA: reviewOf(extA, landContent), extB: reviewOf(extB, "beef000")})
	if got := f.taskStatusOf(t, f.taskB); got != "in_progress" {
		t.Errorf("task b = %s, want in_progress", got)
	}
	if got := f.taskStatusOf(t, f.taskA); got != "in_progress" {
		t.Errorf("task a = %s, want in_progress", got)
	}
}

// タスク a を止める保留が開いている間、タスク b のタスクの検証は走る（AC-21）。タスク a のタスクの検証は走らない（AC-29）。
func TestHoldTaskProceed_AC21_AC29_TaskVerificationRunsOnlyForTheTaskWithoutAHold(t *testing.T) {
	t.Parallel()
	f := newProceedFixture(t)
	f.sql(t, `UPDATE contract_run SET verified_ref = NULL`)
	f.holdTaskA(t)
	f.setVerdict("met")
	if _, err := f.verifyTasks(t, nil); err != nil {
		t.Fatal(err)
	}
	ids := f.j5RunTaskIDs(t)
	if len(ids) != 1 || !ids[0].Valid || ids[0].Int64 != f.taskB {
		t.Fatalf("J5 run task_ids = %v, want only task b (%d)", ids, f.taskB)
	}
	if got := f.text(t, `SELECT COALESCE(verified_ref, '<null>') FROM contract_run WHERE external_run_id = 'ext-1'`); got != "<null>" {
		t.Errorf("task a's verified_ref = %q, want none（検証は走らない。AC-29）", got)
	}
	if got := f.text(t, `SELECT COALESCE(verified_ref, '<null>') FROM contract_run WHERE external_run_id = 'ext-2'`); got != "beef000" {
		t.Errorf("task b's verified_ref = %q, want beef000（検証は走る。AC-21）", got)
	}
}

// 課題を止める保留が開いている間は、どのタスクの検証も走らない。
func TestHoldTaskProceed_ChallengeHoldRunsNoTaskVerification(t *testing.T) {
	t.Parallel()
	f := newProceedFixture(t)
	f.sql(t, `UPDATE contract_run SET verified_ref = NULL`)
	f.holdChallenge(t)
	f.setVerdict("met")
	if _, err := f.verifyTasks(t, nil); err != nil {
		t.Fatal(err)
	}
	if ids := f.j5RunTaskIDs(t); len(ids) != 0 {
		t.Errorf("J5 run task_ids = %v, want none", ids)
	}
}

// タスク a を止める保留が開いている間、検証を通ったタスク b に「入れてよい」が返る（AC-22）。タスク a には返らない。
func TestHoldTaskProceed_AC22_LandIsReturnedOnlyForTheTaskWithoutAHold(t *testing.T) {
	t.Parallel()
	f := newProceedFixture(t)
	f.checks.commits[landContent] = igPassingChecks
	f.checks.commits["beef000"] = igPassingChecks
	f.sql(t, `UPDATE contract_run SET verified_ref = 'beef000' WHERE external_run_id = 'ext-2'`)
	f.holdTaskA(t)
	f.client.resumeOut = runningOf(extB)
	f.pollBy(t, map[string]ContractLaunchOutput{extA: reviewOf(extA, landContent), extB: reviewOf(extB, "beef000")})
	if got := f.resumeRuns(); strings.Join(got, ",") != extB {
		t.Fatalf("resumes = %v, want only %s", got, extB)
	}
	if cmd := strings.Join(f.client.resumes[0], " "); !strings.Contains(cmd, "--choice land") || !strings.Contains(cmd, "--approved beef000") {
		t.Errorf("resume command = %q", cmd)
	}
	if got := f.text(t, `SELECT COALESCE(land_ref, '') FROM contract_run WHERE external_run_id = 'ext-1'`); got != "" {
		t.Errorf("task a's land_ref = %q, want none", got)
	}
}

// 課題を止める保留が開いている間は、検証を通ったタスクにも「入れてよい」を返さない。
func TestHoldTaskProceed_ChallengeHoldReturnsNoLand(t *testing.T) {
	t.Parallel()
	f := newProceedFixture(t)
	f.checks.commits["beef000"] = igPassingChecks
	f.sql(t, `UPDATE contract_run SET verified_ref = 'beef000' WHERE external_run_id = 'ext-2'`)
	f.holdChallenge(t)
	f.client.resumeOut = runningOf(extB)
	f.pollBy(t, map[string]ContractLaunchOutput{extA: runningOf(extA), extB: reviewOf(extB, "beef000")})
	if got := f.resumeRuns(); len(got) != 0 {
		t.Errorf("resumes = %v, want none", got)
	}
}

// タスク a を止める保留が開いている間、統合先に入ったタスク b は済みになる（AC-23）。
func TestHoldTaskProceed_AC23_LandedTaskIsDoneWhileAnotherTaskIsHeld(t *testing.T) {
	t.Parallel()
	f := newProceedFixture(t)
	f.sql(t, `UPDATE contract_run SET verified_ref = 'beef000', land_ref = 'beef000', land_delivered = 1 WHERE external_run_id = 'ext-2'`)
	f.checks.prs["o/r#7"] = UpstreamPullRequestChecks{URL: landPRURL, State: "merged", Base: integBranch, HeadSHA: "beef000"}
	f.setPR(7, landPRURL, "merged", integBranch)
	f.holdTaskA(t)
	f.pollBy(t, map[string]ContractLaunchOutput{
		extA: runningOf(extA),
		extB: contractOut(ContractStateSucceeded, nil, func(st *ContractStatus) { st.RunID = extB; st.Artifacts = landedArts }),
	})
	if got := f.taskStatusOf(t, f.taskB); got != "done" {
		t.Errorf("task b = %s, want done", got)
	}
	if got := f.taskStatusOf(t, f.taskA); got != "verifying" {
		t.Errorf("task a = %s, want verifying（変わらない）", got)
	}
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Errorf("status = %s, want awaiting_human", got)
	}
}

// タスク a を止める保留が開いている間、タスク b の観測の待ちは再開される（AC-25）。タスク a の観測の待ちは再開されない。
func TestHoldTaskProceed_AC25_ObserveWaitIsResumedOnlyForTheTaskWithoutAHold(t *testing.T) {
	t.Parallel()
	f := newProceedFixture(t)
	f.sql(t, `UPDATE contract_run SET requested_kind = 'observe', state = 'waiting'`)
	f.holdTaskA(t)
	f.client.resumeOut = runningOf(extB)
	f.pollBy(t, map[string]ContractLaunchOutput{extA: observeOf(extA), extB: observeOf(extB)})
	if got := f.resumeRuns(); strings.Join(got, ",") != extB {
		t.Errorf("resumes = %v, want only %s", got, extB)
	}
}

// 課題を止める保留が開いている間は、観測の待ちも再開しない（いまと同じ）。
func TestHoldTaskProceed_ChallengeHoldResumesNoObservation(t *testing.T) {
	t.Parallel()
	f := newProceedFixture(t)
	f.sql(t, `UPDATE contract_run SET requested_kind = 'observe', state = 'waiting'`)
	f.holdChallenge(t)
	f.client.resumeOut = runningOf(extB)
	f.pollBy(t, map[string]ContractLaunchOutput{extA: observeOf(extA), extB: observeOf(extB)})
	if got := f.resumeRuns(); len(got) != 0 {
		t.Errorf("resumes = %v, want none", got)
	}
}

// 保留が結び付くタスクの契約の待ちへの答えは、その保留が回答済みになった後の回りで委譲先へ返る（課題が着手中へ戻るのを待たない）。
func TestHoldTaskProceed_AnswerToAContractWaitIsReturnedWhileAnotherHoldIsOpen(t *testing.T) {
	t.Parallel()
	f := newHoldScopeFixture(t)
	f.waitHold(t, f.runB)
	f.waitHold(t, f.runA)
	// 最新の保留（a）へ答える。b の保留は開いたまま。
	att := verifiedAttestationForTest(t, f.id)
	if _, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: f.id, ExpectedVersion: f.version(t), Answer: "進めて"}, att); err != nil {
		t.Fatal(err)
	}
	if got := f.challengeStatus(t); got != "awaiting_human" {
		t.Fatalf("status = %s, want awaiting_human（b の保留が開いている）", got)
	}
	if err := f.delegateFixture.exec(`UPDATE contract_run SET state = 'waiting'`); err != nil {
		t.Fatal(err)
	}
	row, err := f.s.loadContractRunRow(context.Background(), mustRowIDOf(t, f, f.taskA))
	if err != nil {
		t.Fatal(err)
	}
	answer, resume, err := f.s.loadContractAnswer(context.Background(), row, nil)
	if err != nil || !resume || answer != "進めて" {
		t.Fatalf("loadContractAnswer = %q, %v, %v, want the answer to be returned", answer, resume, err)
	}
}

func mustRowIDOf(t *testing.T, f *holdScopeFixture, task int64) int64 {
	t.Helper()
	return f.count(t, `SELECT id FROM contract_run WHERE task_id = `+strconvI(task))
}

// タスクの単位の数え（連続の失敗・差し戻し・再開の対象）の境は、そのタスクの保留だけで決まる（AC-94・AC-95・AC-99）。
// タスク b の保留が開いている間にタスク a の保留に答えた後も、a の数えは a の回答を境に数え直される。
func TestHoldTaskProceed_AC94_AC95_AC99_CountBoundaryIsTheTasksOwnHold(t *testing.T) {
	t.Parallel()
	pv := int64(1)
	at := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	runOf := func(n int, task string, result RunResult) runRow {
		tk := task
		return runRow{ID: formatRunID(int64(n)), PlanVersion: &pv, TaskID: &tk, Result: result, SessionID: "11111111-1111-4111-8111-11111111111" + string(rune('0'+n%10)),
			StartedAt: at.Add(time.Duration(n) * time.Minute)}
	}
	ans := "続けて"
	aHoldRun := int64(13)
	answeredAt := at.Add(20 * time.Minute)
	taskA, taskB := "T-1", "T-2"
	h := &launchHistory{
		Runs: []runRow{
			runOf(11, taskA, RunResultErrored), runOf(12, taskA, RunResultErrored), runOf(13, taskA, RunResultErrored),
		},
		Holds: []holdRef{
			{ID: 1, RunID: &aHoldRun, TaskID: &taskA, Answer: &ans, AnsweredAt: &answeredAt, RaisedAt: at.Add(15 * time.Minute)},
			{ID: 2, TaskID: &taskB, RaisedAt: at.Add(18 * time.Minute)}, // タスク b の、開いたままの保留（あとに立った）
		},
	}
	// 課題の単位で「最後の保留」を読むと、b の開いている保留が境になり、a の回答が境にならない（連続の失敗が上限に達してしまう）。
	if _, limit := h.decideLaunch(1, 3); limit == nil {
		t.Fatal("precondition: reading the challenge-wide last hold must count the failures before a's answer")
	}
	th := h.forTask(taskA)
	resume, limit := th.decideLaunch(1, 3)
	if limit != nil {
		t.Errorf("AC-95: failure limit hit = %+v, want the count restarted at task a's answer", limit)
	}
	if resume == nil || resume.Kind != ResumeKindAnswer || resume.Target.ID != formatRunID(13) || resume.Answer != ans {
		t.Errorf("AC-99: resume = %+v, want an answer resume of R-13 with the answer", resume)
	}
	// 差し戻しの数え: a の J5 の「満たしていない」3 回は、a の回答より前なので数えない。
	notMet := string(j5Out("not_met", "FB", nil).StructuredOutput)
	for i, id := range []int{7, 8, 9} {
		v := runOf(id, taskA, RunResultSucceeded)
		v.Output = notMet
		v.StartedAt = at.Add(time.Duration(i) * time.Minute)
		h.Verifications = append(h.Verifications, v)
	}
	if _, limit := h.forTask(taskA).decideRework(1, 3); limit != nil {
		t.Errorf("AC-94: rework limit hit = %+v, want the count restarted at task a's answer", limit)
	}
}

// 実装の枠が尽きたタスクの数えは、進めてよいタスクだけを数える。タスク a を止める保留だけが開いている間は、タスク b の枠だけが数えに入る。
// 課題を止める保留が開いている間は、どちらも入らない（課題の全部が止まる）。
func TestHoldTaskProceed_BudgetExhaustedCountsOnlyTasksThatMayProceed(t *testing.T) {
	f := newProceedFixture(t)
	f.sql(t, `UPDATE task SET budget_impl_micros = 500000, budget_review_micros = 30000000, status = 'in_progress'`)
	exhausted := func() []BudgetExhausted {
		var out []BudgetExhausted
		if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
			var err error
			out, err = listBudgetExhausted(context.Background(), tx, nil)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := exhausted(); len(got) != 2 {
		t.Fatalf("precondition: exhausted = %+v, want both tasks (in_progress challenge)", got)
	}
	f.holdTaskA(t)
	got := exhausted()
	if len(got) != 1 || got[0].TaskID != formatTaskID(f.taskB) {
		t.Fatalf("exhausted = %+v, want only task b %s", got, formatTaskID(f.taskB))
	}
}

// 課題を止める保留が開いている間は、枠が尽きたタスクはどれも数えに入らない。
func TestHoldTaskProceed_BudgetExhaustedCountsNothingWhileAChallengeHoldIsOpen(t *testing.T) {
	f := newProceedFixture(t)
	f.sql(t, `UPDATE task SET budget_impl_micros = 500000, budget_review_micros = 30000000, status = 'in_progress'`)
	f.holdChallenge(t)
	var out []BudgetExhausted
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		out, err = listBudgetExhausted(context.Background(), tx, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("exhausted = %+v, want none", out)
	}
}

// 述語 taskMayProceed と SQL の式 taskMayProceedSQL は、同じ状態で同じ答えを返す。
func TestHoldTaskProceed_SQLPredicateAgreesWithGoPredicate(t *testing.T) {
	check := func(f *proceedFixture, label string) {
		for _, task := range []int64{f.taskA, f.taskB} {
			var goAns bool
			var sqlAns int
			if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
				var err error
				cid, _ := parseChallengeID(f.cid)
				goAns, err = taskMayProceed(context.Background(), tx, cid, task)
				if err != nil {
					return err
				}
				return tx.QueryRowContext(context.Background(), `SELECT `+taskMayProceedSQL("t")+` FROM (SELECT challenge_id, id AS task_id FROM task WHERE id = ?) t`, task).Scan(&sqlAns)
			}); err != nil {
				t.Fatal(err)
			}
			if goAns != (sqlAns == 1) {
				t.Errorf("%s task %d: go=%v sql=%d", label, task, goAns, sqlAns)
			}
		}
	}
	f := newProceedFixture(t)
	check(f, "in_progress/no hold")
	f.holdTaskA(t)
	check(f, "task hold on a")
	g := newProceedFixture(t)
	g.holdChallenge(t)
	check(g, "challenge hold")
}
