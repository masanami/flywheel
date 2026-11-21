package core

// このファイルは、実行中の述語と状態の確認の読みをタスクの単位にする変更（区切り S3b。docs/features/core-redesign.md の
// 受入基準のうち、保留中の課題の別のタスクの契約の実行の読み・レビュー待ちだけを持つタスクの扱い・タスクの検証の実行が開いている
// タスクの契約の実行の読みの飛ばし方）を、実ストア・偽の契約のコマンドで検証する。

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// twoTaskFixture は、タスク 1 を契約の start で始めた課題に、タスク 2 とその終わっていない契約の実行（ext-2）を足したもの。
type twoTaskFixture struct {
	*contractFixture
	cidInt       int64
	task1, task2 int64
	run1, run2   int64
}

func newTwoTaskFixture(t *testing.T) *twoTaskFixture {
	t.Helper()
	f := &twoTaskFixture{contractFixture: newContractFixture(t)}
	if _, err := f.start(t); err != nil {
		t.Fatal(err)
	}
	f.cidInt, _ = parseChallengeID(f.cid)
	f.run1 = f.insertRowFor(t, `SELECT id FROM contract_run`)
	f.task1 = f.insertRowFor(t, `SELECT task_id FROM contract_run`)
	f.task2 = f.insertRow(t, `INSERT INTO task (challenge_id, plan_version, key, title, body, done_criteria, repo, operation, status, created_at, updated_at)
		SELECT challenge_id, plan_version, 'second', title, body, done_criteria, repo, operation, 'in_progress', created_at, updated_at FROM task WHERE id = ?`, f.task1)
	f.run2 = f.insertRow(t, `INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, cost_usd, max_budget_usd,
			started_at, observed_at, task_id)
		SELECT challenge_id, connector_id, 'ext-2', start_run_id, 'running', 'old', 0, max_budget_usd, started_at, observed_at, ? FROM contract_run WHERE id = ?`, f.task2, f.run1)
	return f
}

func (f *twoTaskFixture) insertRowFor(t *testing.T, q string) int64 {
	t.Helper()
	return f.count(t, q)
}

func (f *twoTaskFixture) setRun(t *testing.T, runID int64, state, requestedKind string) {
	t.Helper()
	if _, err := f.exec(`UPDATE contract_run SET state = ?, requested_kind = ? WHERE id = ?`, state, nullableTextColumn(requestedKind), runID); err != nil {
		t.Fatal(err)
	}
}

func (f *twoTaskFixture) taskActive(t *testing.T, taskID int64) bool {
	t.Helper()
	var got bool
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
		got, err = taskHasActiveRun(context.Background(), tx, f.cidInt, taskID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

func (f *twoTaskFixture) challengeActive(t *testing.T) bool {
	t.Helper()
	var got bool
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
		got, err = challengeHasActiveRun(context.Background(), tx, f.cidInt)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

func (f *twoTaskFixture) beginStart(t *testing.T, taskID int64) error {
	t.Helper()
	sid, _ := newLowercaseUUIDv4()
	_, err := f.s.beginContractRunForTask(context.Background(), f.cidInt, nil, nil, sid, usdToMicros(5), true, &taskID, nil)
	return err
}

// statusCalledFor は、状態の確認（status）が呼ばれた外部の実行の ID を呼ばれた順に返す。
func (f *twoTaskFixture) statusCalledFor() []string {
	var out []string
	for _, c := range f.client.statuses {
		out = append(out, c[len(c)-1])
	}
	return out
}

// AC-30: in_progress の課題の、レビュー待ちの契約の実行だけを持つタスクは「実行中」に数えられず、同じ課題の別のタスクが起動される。
func TestTaskActive_ReviewWaitOnlyTaskIsNotRunningAndAnotherTaskStarts(t *testing.T) {
	t.Parallel()
	f := newTwoTaskFixture(t)
	f.exec1(t, `UPDATE contract_run SET ended_at = '2026-11-01T00:00:00.000Z', state = 'succeeded' WHERE id = ?`, f.run2) // タスク 2 の実行は無い状態にする
	f.setRun(t, f.run1, "running", "")
	if !f.taskActive(t, f.task1) || !f.challengeActive(t) {
		t.Fatal("a running contract run must make the task and the challenge running")
	}
	if err := f.beginStart(t, f.task2); err != nil {
		t.Fatalf("starting another task while task 1 is working: %v", err)
	}
	f.exec1(t, `DELETE FROM run WHERE task_id = ? AND result IS NULL`, f.task2) // 起動の記録だけを消す
	if err := f.beginStart(t, f.task1); !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("starting task 1 again while it is working: %v, want ErrRunInProgress", err)
	}

	f.setRun(t, f.run1, "waiting", "review")
	if got := f.status(t, f.cid); got != StatusInProgress {
		t.Fatalf("status = %s, want in_progress", got)
	}
	if f.taskActive(t, f.task1) {
		t.Error("a task with only a review-waiting contract run must not count as running while the challenge is in_progress")
	}
	if f.challengeActive(t) {
		t.Error("the challenge must not count as running when only a task has a review-waiting contract run")
	}
	if err := f.beginStart(t, f.task2); err != nil {
		t.Fatalf("another task of the same challenge must start: %v", err)
	}
	// レビュー待ちが開いたままのタスクは、実行中でなくても、もう一度 start しない（外部の実行を二重に作らない）。
	if err := f.beginStart(t, f.task1); !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("starting task 1 again while its review-waiting contract run is open: %v, want ErrRunInProgress", err)
	}
	// 述語の対照: タスク 2 の契約の実行が作業中なら、タスク 2 は実行中で、課題も実行中。
	f.setRun(t, f.run2, "running", "")
	f.exec1(t, `UPDATE contract_run SET ended_at = NULL, state = 'running' WHERE id = ?`, f.run2)
	if !f.taskActive(t, f.task2) || f.taskActive(t, f.task1) {
		t.Error("only task 2 must be running")
	}
}

// タスクの単位の述語は、オーナーへの問いの待ち（保留の課題の waiting）も、ほかのタスクの実行も数えない。
func TestTaskActive_IsScopedToTheTask(t *testing.T) {
	t.Parallel()
	f := newTwoTaskFixture(t)
	f.setRun(t, f.run1, "running", "")
	if !f.taskActive(t, f.task1) {
		t.Error("task 1 has a running contract run")
	}
	if !f.taskActive(t, f.task2) { // タスク 2 の契約の実行も running
		t.Error("task 2 has a running contract run")
	}
	f.setRun(t, f.run2, "waiting", "answer")
	f.setStatus(t, StatusAwaitingHuman)
	if f.taskActive(t, f.task2) {
		t.Error("an owner-question wait must not count as running")
	}
	if !f.taskActive(t, f.task1) {
		t.Error("task 1 still has a running contract run")
	}
	// 実行の記録（run）は、そのタスクだけを実行中にする。
	f.exec1(t, `UPDATE contract_run SET ended_at = '2026-11-01T00:00:00.000Z', state = 'succeeded' WHERE id IN (?, ?)`, f.run1, f.run2)
	f.setStatus(t, StatusInProgress)
	if f.taskActive(t, f.task1) || f.taskActive(t, f.task2) || f.challengeActive(t) {
		t.Fatal("nothing should be running")
	}
	in := validInsertRunInput()
	in.TaskID = int64Ptr(f.task2)
	if _, err := insertRunForTest(t, f.s, f.cidInt, in); err != nil {
		t.Fatal(err)
	}
	if f.taskActive(t, f.task1) || !f.taskActive(t, f.task2) || !f.challengeActive(t) {
		t.Fatal("an open run record of task 2 must make only task 2 (and the challenge) running")
	}
}

func (f *twoTaskFixture) exec1(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// AC-31・AC-32: タスクの検証の実行の記録が開いているタスクの契約の実行には status を呼ばず（エラーの報告も作らない）、
// 同じ課題の別のタスクの契約の実行には status を呼ぶ。閉じれば、次の読みで呼ぶ。
func TestPollContractRuns_SkipsOnlyTheTaskWithAnOpenJudgmentRun(t *testing.T) {
	t.Parallel()
	f := newTwoTaskFixture(t)
	f.client.queue = []ContractLaunchOutput{contractOut(ContractStateRunning, nil, func(st *ContractStatus) { st.RunID = "ext-2" })}
	in := validInsertRunInput()
	in.Judgment = judgmentJ5
	in.TaskID = int64Ptr(f.task1)
	row, err := insertRunForTest(t, f.s, f.cidInt, in)
	if err != nil {
		t.Fatal(err)
	}
	reps := f.poll(t)
	got := f.statusCalledFor()
	if len(got) != 1 || got[0] != "ext-2" {
		t.Fatalf("status called for %v, want only ext-2 (AC-31: not ext-1 of the task under verification; AC-32: ext-2 of the other task)", got)
	}
	for _, r := range reps {
		if r.Error != "" || strings.Contains(r.ExternalRunID, "ext-1") {
			t.Errorf("unexpected report for the skipped task: %+v", r)
		}
	}
	id, _ := parseRunID(row.ID)
	f.exec1(t, `UPDATE run SET result = 'succeeded', ended_at = '2026-11-01T00:00:00.000Z' WHERE id = ?`, id)
	f.client.statuses = nil
	f.client.queue = []ContractLaunchOutput{
		contractOut(ContractStateRunning, nil, nil),
		contractOut(ContractStateRunning, nil, func(st *ContractStatus) { st.RunID = "ext-2" }),
	}
	f.poll(t)
	if got := f.statusCalledFor(); len(got) != 2 || got[0] != "ext-1" || got[1] != "ext-2" {
		t.Fatalf("status called for %v after the verification run closed, want both runs", got)
	}
}

// AC-14: 保留中の課題の、ほかのタスクの契約の実行は、状態の確認で読まれ、契約の実行の記録の状態が更新される。
func TestPollContractRuns_ReadsOtherTasksRunWhileTheChallengeIsHeld(t *testing.T) {
	t.Parallel()
	f := newTwoTaskFixture(t)
	f.setStatus(t, StatusAwaitingHuman)
	f.client.queue = []ContractLaunchOutput{
		contractOut(ContractStateRunning, nil, nil),
		contractOut(ContractStateRunning, nil, func(st *ContractStatus) { st.RunID = "ext-2"; st.Summary = "progress-2"; st.CostUSD = nil }),
	}
	f.exec1(t, `UPDATE contract_run SET state = 'pending' WHERE id = ?`, f.run2)
	f.poll(t)
	called := f.statusCalledFor()
	if len(called) != 2 {
		t.Fatalf("status called for %v, want both runs read while held", called)
	}
	var state, summary string
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT state, summary FROM contract_run WHERE id = ?`, f.run2).Scan(&state, &summary)
	}); err != nil {
		t.Fatal(err)
	}
	if state != "running" || summary != "progress-2" { // 記録の前は pending だった
		t.Fatalf("contract_run of the other task = (%s, %s), want the state read while held to be recorded", state, summary)
	}
	if got := f.status(t, f.cid); got != StatusAwaitingHuman {
		t.Fatalf("challenge status = %s, want awaiting_human unchanged", got)
	}
}

// 反映の段は、統合先を使う課題のタスクの実行を扱わない（反映の依頼だけを読む）。統合先を使わない課題の実行は、これまでどおり読む。
func TestLoadOpenRunInTx_IgnoresTaskRunsOfIntegrationChallenges(t *testing.T) {
	t.Parallel()
	f := newTwoTaskFixture(t)
	load := func() *releaseRun {
		var out *releaseRun
		if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
			out, err = loadOpenRunInTx(context.Background(), tx, f.cidInt)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if r := load(); r == nil || r.row.ID != f.run1 {
		t.Fatalf("a challenge without an integration must keep reading its task run, got %+v", r)
	}
	f.exec1(t, `UPDATE challenge SET uses_integration = 1 WHERE id = ?`, f.cidInt)
	if r := load(); r != nil {
		t.Fatalf("a task run of an integration challenge must not be read by the release stage, got %+v", r)
	}
	f.exec1(t, `UPDATE contract_run SET ended_at = '2026-11-01T00:00:00.000Z', state = 'succeeded' WHERE id IN (?, ?)`, f.run1, f.run2)
	reflect := f.insertRow(t, `INSERT INTO contract_run (challenge_id, connector_id, external_run_id, start_run_id, state, summary, cost_usd, max_budget_usd,
			started_at, observed_at, kind)
		SELECT challenge_id, connector_id, 'ext-3', start_run_id, 'running', 'r', 0, max_budget_usd, started_at, observed_at, 'reflect' FROM contract_run WHERE id = ?`, f.run1)
	// 完了でない課題の反映の依頼（上げる依頼）は、委譲の段の先頭の状態の確認が読むので、反映の段は読まない。完了の課題のものは読む。
	if r := load(); r != nil {
		t.Fatalf("a reflect request of an undone challenge must not be read by the release stage, got %+v", r)
	}
	f.exec1(t, `UPDATE challenge SET status = 'done' WHERE id = ?`, f.cidInt)
	if r := load(); r == nil || r.row.ID != reflect {
		t.Fatalf("a reflect request of a done integration challenge must be read, got %+v", r)
	}
}
