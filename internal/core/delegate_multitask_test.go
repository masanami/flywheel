package core

// このファイルは、委譲の段の起動の判定・計画・同時に走らせる数をタスクの単位にする変更（区切り S3b。
// docs/features/core-redesign.md の受入基準 AC-1〜AC-13・AC-20〜AC-23・AC-33〜AC-35・AC-69〜AC-72・AC-76・AC-236）を、
// 実ストア・偽の契約のコマンドで検証する。受入基準 1 件ごとに、基準の文が述べる条件・結果・値を確かめる。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// multiConnectorsJSON は、start に {task_id}・{uses_integration} を書く form: cli の接続ツールと、2 つのリポジトリ。
func multiConnectorsJSON() string {
	s := strings.Replace(contractConnectorsJSON,
		`"start": ["tool", "start", "--challenge", "{challenge_id}"]`,
		`"start": ["tool", "start", "--challenge", "{challenge_id}", "--task", "{task_id}", "--integration", "{uses_integration}"]`, 1)
	return strings.Replace(s, `"paths": ["slot1"]}}`,
		`"paths": ["slot1"]}},
    {"name": "other-repo", "remote": "o/s", "default_branch": "main", "connector": "tool", "slots": {"provider": "clone", "paths": ["slot-s"]}}`, 1)
}

// newMultiFixture は契約の形の接続ツールの宣言を持つ（課題は作らない）。同時に走らせる数の上限は maxParallel。
func newMultiFixture(t *testing.T, maxParallel int) *contractFixture {
	t.Helper()
	f := newDelegateFixture(t)
	conn, err := parseConnectorsDeclaration([]byte(multiConnectorsJSON()))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := conn.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	f.conn = conn
	f.agent.MaxParallelRuns = maxParallel
	return &contractFixture{delegateFixture: f, client: &fakeContractClient{
		startOut: contractOut(ContractStateRunning, nil, nil), uniqueRunIDs: true, echoRunning: true}}
}

// multiTask は multiPlan の 1 タスク（key と、前提のタスクの key）。
type multiTask struct {
	key  string
	deps []string
}

// multiPlan は、tasks を持ち、統合先を使うかが integ の計画の構造化した出力（リポジトリは main-repo）。
func multiPlan(integ bool, tasks ...multiTask) string {
	return planSpec(func(m map[string]any) {
		var ts []any
		for _, tk := range tasks {
			t := j2TestTask(tk.key, "main-repo", "impl")
			if tk.deps != nil {
				t["depends_on"] = tk.deps
			}
			if len(tasks) >= 2 { // タスクが 2 個以上の計画は、タスクごとの見込みを持つ
				t["budget_impl_usd"], t["budget_review_usd"] = 2.0, 1.0
			}
			ts = append(ts, t)
		}
		m["tasks"] = ts
		m["uses_integration"] = integ || len(tasks) >= 2 // 2 個以上のタスクの計画は、統合先を使う（検査）
	})
}

func (f *contractFixture) newMulti(t *testing.T, title, priority string, integ bool, tasks ...multiTask) string {
	t.Helper()
	plan := multiPlan(integ, tasks...)
	if _, c := f.conn.findConnectorRepo("main-repo"); c != nil && c.Form == ConnectorFormFlow {
		plan = withFlowReasons(t, plan) // flow の形の操作を選んだタスクは flow_reason が要る
	}
	id := f.newInProgress(t, title, priority, plan)
	if cid, _ := parseChallengeID(id); !integ {
		setUsesIntegrationForTest(t, f.s, cid, false) // 統合先を使わない課題（見直し前の記録）にする
	}
	return id
}

func (f *contractFixture) taskID(t *testing.T, id, key string) string {
	t.Helper()
	cid, _ := parseChallengeID(id)
	for _, tk := range taskRows(t, f.s, cid) {
		if tk.Key == key {
			return tk.ID
		}
	}
	t.Fatalf("task %q not found in %s", key, id)
	return ""
}

// startedTasks は契約の start に渡された --task の値（昇順）。
func (f *contractFixture) startedTasks(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, args := range f.client.starts {
		out = append(out, argAfter(t, args, "--task"))
	}
	sort.Strings(out)
	return out
}

func notStartedByReason(res *DelegateResult, r NotStartedReason) []NotStarted {
	var out []NotStarted
	for _, n := range res.NotStarted {
		if n.Reason == r {
			out = append(out, n)
		}
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (f *contractFixture) holdReasons(t *testing.T, id string) []string {
	t.Helper()
	cid, _ := parseChallengeID(id)
	var out []string
	err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT COALESCE(reason, '') FROM hold WHERE challenge_id = ? ORDER BY id`, cid)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var r string
			if err := rows.Scan(&r); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// AC-1・AC-2: 統合先を使う、タスクが 1 個の課題の未着手のタスクは起動され（start が呼ばれる）、{uses_integration} は true である。
func TestMultiTask_AC1_AC2_IntegrationOneTaskStartsWithIntegrationTrue(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "one", "P1", true, multiTask{key: "a"})
	res := f.runDelegationStage(t, nil)
	if len(f.client.starts) != 1 {
		t.Fatalf("starts = %v, want 1 (not_started = %+v)", f.client.starts, res.NotStarted)
	}
	if got := argAfter(t, f.client.starts[0], "--integration"); got != "true" {
		t.Errorf("{uses_integration} = %q, want true", got)
	}
	if got := argAfter(t, f.client.starts[0], "--task"); got != f.taskID(t, id, "a") {
		t.Errorf("{task_id} = %q, want %q", got, f.taskID(t, id, "a"))
	}
	if got := taskStatusOf(t, f.s, id, "a"); got != TaskStatusInProgress {
		t.Errorf("task status = %s, want in_progress", got)
	}
}

// AC-3: 統合先を使う、タスクが 2 個（前提の線が無い）の課題は、上限が 2 のとき、1 回の委譲の段で 2 個とも起動される。
func TestMultiTask_AC3_TwoIndependentTasksStartInOneStage(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "two", "P1", true, multiTask{key: "a"}, multiTask{key: "b"})
	f.runDelegationStage(t, nil)
	want := []string{f.taskID(t, id, "a"), f.taskID(t, id, "b")}
	if got := f.startedTasks(t); !equalStrings(got, want) {
		t.Fatalf("started tasks = %v, want %v", got, want)
	}
	for _, k := range []string{"a", "b"} {
		if got := taskStatusOf(t, f.s, id, k); got != TaskStatusInProgress {
			t.Errorf("task %s status = %s, want in_progress", k, got)
		}
	}
	if n := countRows(t, f.s, `SELECT COUNT(*) FROM contract_run WHERE task_id IS NOT NULL`); n != 2 {
		t.Errorf("contract runs of tasks = %d, want 2", n)
	}
}

// AC-4: 前提のタスクが済みでないタスクは、起動されない。
func TestMultiTask_AC4_TaskWhosePrerequisiteIsNotDoneIsNotStarted(t *testing.T) {
	t.Parallel()
	for _, prereq := range []TaskStatus{TaskStatusVerifying, TaskStatusPending} {
		f := newMultiFixture(t, 2)
		id := f.newMulti(t, "chain", "P1", true, multiTask{key: "a"}, multiTask{key: "b", deps: []string{"a"}})
		cid, _ := parseChallengeID(id)
		setTaskStatusRaw(t, f.s, cid, "a", prereq)
		f.runDelegationStage(t, nil)
		got := f.startedTasks(t)
		if prereq == TaskStatusVerifying && len(got) != 0 {
			t.Errorf("prerequisite %s: started = %v, want none", prereq, got)
		}
		if prereq == TaskStatusPending && !equalStrings(got, []string{f.taskID(t, id, "a")}) {
			t.Errorf("prerequisite %s: started = %v, want only the prerequisite task a", prereq, got)
		}
		if st := taskStatusOf(t, f.s, id, "b"); st != TaskStatusPending {
			t.Errorf("prerequisite %s: task b = %s, want pending", prereq, st)
		}
	}
}

// AC-5: 前提のタスクが、すべて済みになったタスクは、次の委譲の段で起動される。
func TestMultiTask_AC5_TaskIsStartedOnceItsPrerequisitesAreDone(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "chain", "P1", true, multiTask{key: "a"}, multiTask{key: "b"}, multiTask{key: "c", deps: []string{"a", "b"}})
	cid, _ := parseChallengeID(id)
	setTaskStatusRaw(t, f.s, cid, "a", TaskStatusDone)
	f.runDelegationStage(t, nil)
	if got := f.startedTasks(t); !equalStrings(got, []string{f.taskID(t, id, "b")}) {
		t.Fatalf("started = %v, want only b (c waits for b)", got)
	}
	setTaskStatusRaw(t, f.s, cid, "b", TaskStatusDone)
	f.runDelegationStage(t, nil)
	want := []string{f.taskID(t, id, "b"), f.taskID(t, id, "c")}
	if got := f.startedTasks(t); !equalStrings(got, want) {
		t.Fatalf("started = %v, want %v (c starts at the next stage)", got, want)
	}
}

// AC-6: 前提に、取りやめのタスクがあるタスクは、起動されない（ほかの起動できるタスクは起動される）。
func TestMultiTask_AC6_TaskWithACancelledPrerequisiteIsNotStarted(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "cancelled", "P1", true, multiTask{key: "a"}, multiTask{key: "b", deps: []string{"a"}}, multiTask{key: "c"})
	cid, _ := parseChallengeID(id)
	setTaskStatusRaw(t, f.s, cid, "a", TaskStatusCancelled)
	f.runDelegationStage(t, nil)
	if got := f.startedTasks(t); !equalStrings(got, []string{f.taskID(t, id, "c")}) {
		t.Fatalf("started = %v, want only c", got)
	}
	if st := taskStatusOf(t, f.s, id, "b"); st != TaskStatusPending {
		t.Errorf("task b = %s, want pending", st)
	}
	if got := f.detail(t, id).Status; got != StatusInProgress {
		t.Errorf("challenge status = %s, want in_progress (c can still go on)", got)
	}
}

// AC-7: 統合先を使わない、起動できるタスクが 2 個の課題は、起動されない（理由 integration_unavailable）。
func TestMultiTask_AC7_NonIntegrationTwoTasksAreNotStarted(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "two", "P1", false, multiTask{key: "a"}, multiTask{key: "b"})
	for _, target := range []*string{nil, &id} {
		res := f.runDelegationStage(t, target)
		got := notStartedByReason(res, NotStartedIntegrationUnavailable)
		if len(got) != 1 || got[0].ChallengeID != id {
			t.Fatalf("not_started = %+v, want one integration_unavailable for %s", res.NotStarted, id)
		}
	}
	if len(f.client.starts) != 0 {
		t.Errorf("starts = %v, want none", f.client.starts)
	}
}

// AC-8・AC-236: 統合先を使う課題で、取りやめでないタスク（済みを含む）のリポジトリが 2 つあるものは、起動されず、保留になる（仕上げ 3 項目 3）。
// 問いの文に、語「リポジトリ」がある。
func TestMultiTask_AC8_AC236_TwoRepositoriesAreNotStarted(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "repos", "P1", true, multiTask{key: "a"}, multiTask{key: "b"})
	cid, _ := parseChallengeID(id)
	if err := f.delegateFixture.exec(`UPDATE task SET repo = 'other-repo' WHERE challenge_id = ? AND key = 'b'`, cid); err != nil {
		t.Fatal(err)
	}
	res := f.runDelegationStage(t, nil)
	if len(res.NotStarted) != 1 || res.NotStarted[0].ChallengeID != id {
		t.Fatalf("not_started = %+v, want one entry for %s", res.NotStarted, id)
	}
	if f.status(t, id) != StatusAwaitingHuman || !strings.Contains(f.lastHoldQuestionOf(t, cid), "リポジトリ") {
		t.Errorf("status = %s, question = %q, want a hold that names the repositories", f.status(t, id), f.lastHoldQuestionOf(t, cid))
	}
	if len(f.client.starts) != 0 {
		t.Errorf("starts = %v, want none", f.client.starts)
	}
}

// AC-9: 同じ課題の 2 個のタスクの契約の実行が、どちらも running のとき、同時に走らせる数は 2 と数えられる
// （上限が 2 なら、ほかの課題のタスクは、理由 slot_unavailable で起動されない）。
func TestMultiTask_AC9_TwoRunningTasksOfOneChallengeCountAsTwo(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "two", "P1", true, multiTask{key: "a"}, multiTask{key: "b"})
	f.runDelegationStage(t, nil)
	if len(f.client.starts) != 2 {
		t.Fatalf("starts = %v, want 2", f.client.starts)
	}
	var n int
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
		n, err = activeDelegationCount(context.Background(), tx)
		return err
	}); err != nil || n != 2 {
		t.Fatalf("active delegation count = %d err = %v, want 2", n, err)
	}
	other := f.newMulti(t, "other", "P0", false, multiTask{key: "x"})
	res := f.runDelegationStage(t, nil)
	got := notStartedByReason(res, NotStartedSlotUnavailable)
	if len(got) != 1 || got[0].ChallengeID != other {
		t.Fatalf("not_started = %+v, want slot_unavailable for %s", res.NotStarted, other)
	}
	if len(f.client.starts) != 2 {
		t.Errorf("starts = %d, want still 2 (the other challenge's task is not started)", len(f.client.starts))
	}
	_ = id
}

// AC-10: レビュー待ちの契約の実行は、同時に走らせる数に数えられない。
func TestMultiTask_AC10_ReviewWaitingContractRunsAreNotCounted(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "two", "P1", true, multiTask{key: "a"}, multiTask{key: "b"})
	f.runDelegationStage(t, nil)
	count := func() int {
		var n int
		if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
			n, err = activeDelegationCount(context.Background(), tx)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	cid, _ := parseChallengeID(id)
	_ = cid
	for i, key := range []string{"a", "b"} {
		if err := f.delegateFixture.exec(`UPDATE contract_run SET state = 'waiting', requested_kind = 'review' WHERE task_id = ?`, mustTaskInt(f.taskID(t, id, key))); err != nil {
			t.Fatal(err)
		}
		if got, want := count(), 1-i; got != want {
			t.Errorf("after %d review-waiting runs: count = %d, want %d", i+1, got, want)
		}
	}
}

// レビュー待ちの契約の実行（終わっていない）だけを持つ着手中のタスクは、起動できるタスクの候補に入らない（beginContractRunForTask が
// ErrRunInProgress で断る経路へ進まない）。課題を保留にもしない（レビュー待ちは成果物の受け取りが扱う）。ほかの起動できる
// タスクは、そのまま起動される。
func TestMultiTask_ReviewWaitingContractRunTaskIsNotALaunchCandidate(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 3)
	id := f.newMulti(t, "two", "P1", true, multiTask{key: "a"}, multiTask{key: "b"})
	cid, _ := parseChallengeID(id)
	setTaskStatusRaw(t, f.s, cid, "b", TaskStatusVerifying) // 先に a だけを起動する
	f.runDelegationStage(t, nil)
	setTaskStatusRaw(t, f.s, cid, "b", TaskStatusPending)
	taskA, taskB := f.taskID(t, id, "a"), f.taskID(t, id, "b")
	if err := f.delegateFixture.exec(`UPDATE contract_run SET state = 'waiting', requested_kind = 'review' WHERE task_id = ?`, mustTaskInt(taskA)); err != nil {
		t.Fatal(err)
	}
	var sel *launchSelection
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
		sel, err = selectLaunchTasks(context.Background(), tx, "", f.conn, id, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var cands []string
	for _, tk := range sel.Tasks {
		cands = append(cands, tk.ID)
	}
	if !equalStrings(cands, []string{taskB}) {
		t.Fatalf("launch candidates = %v, want only %s (task %s has a review-waiting contract run)", cands, taskB, taskA)
	}
	if sel.Blocked != "" {
		t.Errorf("Blocked = %q, want none (a review wait is not a block)", sel.Blocked)
	}
	// 委譲の段は、a の起動を試みず（ErrRunInProgress にならず）、b を起動する。
	f.runDelegationStage(t, nil)
	if got := f.startedTasks(t); !equalStrings(got, []string{taskA, taskB}) {
		t.Fatalf("started tasks = %v, want a once and b once", got)
	}
	if got := f.holdReasons(t, id); len(got) != 0 {
		t.Errorf("holds = %v, want none", got)
	}
}

// レビュー待ちの契約の実行のタスクしか残っていない課題は、起動するタスクが無い（待つ）。保留にしない。
func TestMultiTask_OnlyReviewWaitingContractRunTaskRemainsWaitsWithoutHold(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "one", "P1", true, multiTask{key: "a"})
	cid, _ := parseChallengeID(id)
	f.runDelegationStage(t, nil)
	if err := f.delegateFixture.exec(`UPDATE contract_run SET state = 'waiting', requested_kind = 'review' WHERE task_id = ?`, mustTaskInt(f.taskID(t, id, "a"))); err != nil {
		t.Fatal(err)
	}
	var sel *launchSelection
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
		sel, err = selectLaunchTasks(context.Background(), tx, "", f.conn, id, cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(sel.Tasks) != 0 || sel.Blocked != "" || sel.Busy {
		t.Fatalf("selection = %+v, want no tasks, not blocked, not busy", sel)
	}
	res := f.runDelegationStage(t, nil)
	if len(f.client.starts) != 1 {
		t.Errorf("starts = %d, want still 1 (no second start of the review-waiting task)", len(f.client.starts))
	}
	for _, n := range res.NotStarted {
		if n.Reason == NotStartedInProgress {
			t.Errorf("not_started = %+v, want no in_progress refusal", res.NotStarted)
		}
	}
	if got := f.holdReasons(t, id); len(got) != 0 {
		t.Errorf("holds = %v, want none", got)
	}
}

// 1 つの委譲の段で 2 個のタスクを起動するとき、契約の実行の上限額は、それぞれのタスクの枠の残り（実装の枠 + レビュー対応の枠。
// 同じ課題のほかのタスクの枠・使った額は混ぜない）になる。
func TestMultiTask_TwoTasksInOneStageEachGetTheirOwnBudgetRemainingAsTheCap(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	spec := planSpec(func(m map[string]any) {
		tasks := j2TasksOf("a", "b")
		for i, tk := range tasks {
			tm := tk.(map[string]any)
			tm["repo"], tm["operation"] = "main-repo", "impl"
			vals := [][2]float64{{10, 4}, {20, 6}}[i]
			tm["budget_impl_usd"], tm["budget_review_usd"] = vals[0], vals[1]
		}
		m["tasks"] = tasks
		m["uses_integration"] = true
		m["budget_impl_usd"] = 99.0
		m["budget_review_usd"] = 88.0
	})
	id := f.newInProgress(t, "two", "P1", spec)
	f.runDelegationStage(t, nil)
	want := []string{f.taskID(t, id, "a"), f.taskID(t, id, "b")}
	if got := f.startedTasks(t); !equalStrings(got, want) {
		t.Fatalf("started tasks = %v, want %v", got, want)
	}
	for taskKey, usd := range map[string]float64{"a": 14, "b": 26} {
		got := microsOf(t, f.s, `SELECT max_budget_usd FROM contract_run WHERE task_id = ?`, mustTaskInt(f.taskID(t, id, taskKey)))
		if got.Int64 != usdToMicros(usd) {
			t.Errorf("task %s: contract_run.max_budget_usd = %d, want %v USD (its own impl + review frame)", taskKey, got.Int64, usd)
		}
	}
}

// AC-11: 委譲の計画の候補は、課題の優先度、課題の ID、タスクの ID の順に並ぶ。
func TestMultiTask_AC11_CandidatesAreOrderedByPriorityChallengeThenTask(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 4)
	low := f.newMulti(t, "low", "P2", true, multiTask{key: "a"}, multiTask{key: "b"})
	high := f.newMulti(t, "high", "P1", true, multiTask{key: "a"}, multiTask{key: "b"})
	in := f.input(f.cycle(t, 300), nil)
	var picked delegationTargets
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
		picked, err = selectDelegationTargetsFor(context.Background(), tx, "", f.conn)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := f.s.planDelegation(context.Background(), in, picked.Targets)
	if err != nil {
		t.Fatal(err)
	}
	var got, want []string
	for _, pc := range plan.candidates {
		got = append(got, pc.Cand.ID+"/"+formatTaskID(pc.Cand.TaskSeq))
	}
	for _, id := range []string{high, low} {
		for _, k := range []string{"a", "b"} {
			want = append(want, id+"/"+f.taskID(t, id, k))
		}
	}
	if !equalStrings(got, want) {
		t.Errorf("candidates = %v, want %v", got, want)
	}
}

// AC-11（優先度が同じ課題）: 優先度が同じ 2 つの課題は、課題の ID の小さい方のタスクが先に並ぶ。タスクの ID の大小が課題の ID の大小と逆でも、
// 課題の ID が先に効く（タスクの ID は、同じ課題の中だけの並びを決める）。
func TestMultiTask_AC11_SamePriorityChallengeIDBeatsTaskID(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 4)
	first := f.newMulti(t, "first", "P1", true, multiTask{key: "a"}, multiTask{key: "b"})
	second := f.newMulti(t, "second", "P1", true, multiTask{key: "a"}, multiTask{key: "b"})
	firstCID, _ := parseChallengeID(first)
	// first（課題の ID が小さい）のタスクの ID を、second のタスクの ID より大きくする。
	if err := f.delegateFixture.exec(`UPDATE task SET id = id + 1000 WHERE challenge_id = ?`, firstCID); err != nil {
		t.Fatal(err)
	}
	secondCID, _ := parseChallengeID(second)
	if firstCID >= secondCID {
		t.Fatalf("challenge ids = %d, %d, want first < second", firstCID, secondCID)
	}
	if f.taskIDInt(t, first, "a") <= f.taskIDInt(t, second, "b") {
		t.Fatalf("setup: first's task ids must be larger than second's")
	}
	in := f.input(f.cycle(t, 300), nil)
	var picked delegationTargets
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) (err error) {
		picked, err = selectDelegationTargetsFor(context.Background(), tx, "", f.conn)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := f.s.planDelegation(context.Background(), in, picked.Targets)
	if err != nil {
		t.Fatal(err)
	}
	var got, want []string
	for _, pc := range plan.candidates {
		got = append(got, pc.Cand.ID+"/"+formatTaskID(pc.Cand.TaskSeq))
	}
	for _, id := range []string{first, second} {
		for _, k := range []string{"a", "b"} {
			want = append(want, id+"/"+f.taskID(t, id, k))
		}
	}
	if !equalStrings(got, want) {
		t.Errorf("candidates = %v, want %v (the smaller challenge id first although its task ids are larger)", got, want)
	}
}

func (f *contractFixture) taskIDInt(t *testing.T, id, key string) int64 {
	t.Helper()
	return mustTaskInt(f.taskID(t, id, key))
}

// AC-12: 同じ課題の 2 個のタスクのうち、1 個が、対応する子が上流で閉じているために起動されないとき、もう 1 個は起動される。
func TestMultiTask_AC12_ClosedChildExcludesOnlyItsTask(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "children", "P1", true, multiTask{key: "a"}, multiTask{key: "b"})
	cid, _ := parseChallengeID(id)
	f.addSourceChild(t, id, "o/r#12", "closed")
	f.addSourceChild(t, id, "o/r#13", "open")
	if err := f.delegateFixture.exec(`UPDATE task SET source_key = CASE key WHEN 'a' THEN 'o/r#12' ELSE 'o/r#13' END WHERE challenge_id = ?`, cid); err != nil {
		t.Fatal(err)
	}
	res := f.runDelegationStage(t, nil)
	if got := f.startedTasks(t); !equalStrings(got, []string{f.taskID(t, id, "b")}) {
		t.Fatalf("started = %v, want only b (a's child is closed)", got)
	}
	if got := notStartedByReason(res, NotStartedUpstreamClosed); len(got) != 1 {
		t.Errorf("not_started = %+v, want one upstream_closed", res.NotStarted)
	}
}

// AC-13: 保留中（awaiting_human）の課題の、未着手のタスクは、起動されない。
func TestMultiTask_AC13_HeldChallengeStartsNoNewTask(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "held", "P1", true, multiTask{key: "a"}, multiTask{key: "b"})
	cid, _ := parseChallengeID(id)
	if err := f.delegateFixture.exec(`UPDATE challenge SET status = 'awaiting_human' WHERE id = ?`, cid); err != nil {
		t.Fatal(err)
	}
	f.runDelegationStage(t, nil)
	if len(f.client.starts) != 0 {
		t.Errorf("starts = %v, want none", f.client.starts)
	}
	for _, k := range []string{"a", "b"} {
		if got := taskStatusOf(t, f.s, id, k); got != TaskStatusPending {
			t.Errorf("task %s = %s, want pending", k, got)
		}
	}
}

// AC-20: 起動できるタスクが 2 個ある課題で、1 個のタスクの契約の実行が、オーナーへの問いの待ちになったとき、作られる保留の
// task_id は、そのタスクである。
func TestMultiTask_AC20_OwnerWaitHoldIsTiedToThatTask(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "two", "P1", true, multiTask{key: "a"}, multiTask{key: "b"})
	f.runDelegationStage(t, nil)
	taskB := f.taskID(t, id, "b")
	runInt, err := strconv.ParseInt(f.delegateFixture.queryString(t, `SELECT id FROM run WHERE kind = 'contract' AND task_id = ? ORDER BY id LIMIT 1`, mustTaskInt(taskB)), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	ext := f.delegateFixture.queryString(t, `SELECT external_run_id FROM contract_run WHERE task_id = ?`, mustTaskInt(taskB))
	held, err := f.s.holdContractWait(context.Background(), &ContractRunReport{RunID: formatRunID(runInt), ChallengeID: id, ExternalRunID: ext}, "どうしますか")
	if err != nil || !held {
		t.Fatalf("holdContractWait = %v, %v, want held", held, err)
	}
	if got, want := f.delegateFixture.queryString(t, `SELECT task_id FROM hold ORDER BY id DESC LIMIT 1`), strconv.FormatInt(mustTaskInt(taskB), 10); got != want {
		t.Errorf("hold.task_id = %q, want %q (task %s)", got, want, taskB)
	}
}

// AC-21: 統合先を使う課題の、form: plugin の委譲先のタスクは、起動されない（理由 integration_unavailable。詳細に、委譲の形が
// 契約の形でない旨）。form: brief も同じ。
func TestMultiTask_AC21_NonContractFormIsNotStartedForAnIntegrationChallenge(t *testing.T) {
	t.Parallel()
	for name, spec := range map[string]string{
		"plugin": planSpec(func(m map[string]any) { m["uses_integration"] = true }),
		"brief":  briefSpec(func(m map[string]any) { m["uses_integration"] = true }),
	} {
		f := newDelegateFixture(t)
		id := f.newInProgress(t, name, "P1", spec)
		res := mustRunResult(t, f, nil)
		got := notStartedByReason(res, NotStartedIntegrationUnavailable)
		if len(got) != 1 || got[0].ChallengeID != id || !strings.Contains(got[0].Detail, "契約の形") {
			t.Fatalf("%s: not_started = %+v, want one integration_unavailable saying the form is not the contract form", name, res.NotStarted)
		}
		if len(f.deleg.launched()) != 0 || len(f.j3Inputs) != 0 {
			t.Errorf("%s: launched %d, J3 %d, want none", name, len(f.deleg.launched()), len(f.j3Inputs))
		}
	}
}

// AC-22: 統合先を使う課題への `flywheel submit` は、validation_failed で終わる（ストアは変わらない）。
func TestMultiTask_AC22_SubmitOfAnIntegrationChallengeIsRejected(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "integ", "P1", true, multiTask{key: "a"})
	before := f.detail(t, id)
	rows := countRows(t, f.s, `SELECT COUNT(*) FROM activity`)
	if _, err := f.s.SubmitChallenge(context.Background(), ChannelCLI, id); !errors.Is(err, ErrValidation) {
		t.Fatalf("SubmitChallenge err = %v, want ErrValidation", err)
	}
	after := f.detail(t, id)
	if after.Status != before.Status || after.Version != before.Version {
		t.Errorf("challenge = %s v%d, want unchanged %s v%d", after.Status, after.Version, before.Status, before.Version)
	}
	if got := taskStatusOf(t, f.s, id, "a"); got != TaskStatusPending {
		t.Errorf("task = %s, want pending", got)
	}
	if n := countRows(t, f.s, `SELECT COUNT(*) FROM activity`); n != rows {
		t.Errorf("activities = %d, want unchanged %d", n, rows)
	}
}

// AC-23: 統合先を使う課題への `flywheel verify --result` は、validation_failed で終わる（ストアは変わらない）。
func TestMultiTask_AC23_VerifyOfAnIntegrationChallengeIsRejected(t *testing.T) {
	t.Parallel()
	for _, result := range []string{"met", "not_met", "uncertain"} {
		f := newMultiFixture(t, 2)
		id := f.newMulti(t, "integ", "P1", true, multiTask{key: "a"})
		cid, _ := parseChallengeID(id)
		setUsesIntegrationForTest(t, f.s, cid, false)
		if _, err := f.s.SubmitChallenge(context.Background(), ChannelCLI, id); err != nil {
			t.Fatal(err)
		}
		setUsesIntegrationForTest(t, f.s, cid, true)
		before := f.detail(t, id)
		rows := countRows(t, f.s, `SELECT COUNT(*) FROM activity`)
		var q *string
		if result == "uncertain" {
			s := "どうする"
			q = &s
		}
		if _, err := f.s.VerifyChallenge(context.Background(), ChannelCLI, id, VerifyInput{Result: result, Question: q}); !errors.Is(err, ErrValidation) {
			t.Fatalf("%s: VerifyChallenge err = %v, want ErrValidation", result, err)
		}
		after := f.detail(t, id)
		if after.Status != StatusVerifying || after.Version != before.Version {
			t.Errorf("%s: challenge = %s v%d, want unchanged verifying v%d", result, after.Status, after.Version, before.Version)
		}
		if n := countRows(t, f.s, `SELECT COUNT(*) FROM activity`); n != rows {
			t.Errorf("%s: activities = %d, want unchanged %d", result, n, rows)
		}
		if n := countRows(t, f.s, `SELECT COUNT(*) FROM hold`); n != 0 {
			t.Errorf("%s: holds = %d, want 0", result, n)
		}
	}
}

// AC-33: 未着手のタスクが、前提に取りやめのタスクを持つものだけで、実行中・検証中のタスクが無い課題は、awaiting_human になり、
// 保留の reason は tasks_blocked である。
func TestMultiTask_AC33_BlockedByCancelledPrerequisiteHoldsTheChallenge(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "blocked", "P1", true, multiTask{key: "a"}, multiTask{key: "b", deps: []string{"a"}}, multiTask{key: "c", deps: []string{"b"}})
	cid, _ := parseChallengeID(id)
	setTaskStatusRaw(t, f.s, cid, "a", TaskStatusCancelled)
	f.runDelegationStage(t, nil)
	if got := f.detail(t, id).Status; got != StatusAwaitingHuman {
		t.Fatalf("challenge status = %s, want awaiting_human", got)
	}
	if got := f.holdReasons(t, id); len(got) != 1 || got[0] != "tasks_blocked" {
		t.Errorf("hold reasons = %v, want [tasks_blocked]", got)
	}
	wantLastHoldStop(t, f.s, stopNone) // AC-25: tasks_blocked は対象にしない
	q := f.delegateFixture.queryString(t, `SELECT question FROM hold WHERE challenge_id = ?`, cid)
	if strings.Contains(q, "tasks_blocked") { // AC-237: 問いの文に理由の値の語は出ない
		t.Errorf("question names the reason value: %q", q)
	}
	for _, want := range []string{"タスク b", "タスク c", "取りやめ"} {
		if !strings.Contains(q, want) {
			t.Errorf("question = %q, want it to contain %q (blocked tasks and the reason)", q, want)
		}
	}
	if !strings.Contains(q, "計画を作り直す") || !strings.Contains(q, "取り下げ") {
		t.Errorf("question = %q, want it to say how to go on (re-plan or withdraw)", q)
	}
	if got := f.delegateFixture.queryString(t, `SELECT COUNT(*) FROM hold WHERE challenge_id = ? AND task_id IS NOT NULL`, cid); got != "0" {
		t.Errorf("holds tied to a task = %s, want 0", got)
	}
	if len(f.client.starts) != 0 {
		t.Errorf("starts = %v, want none", f.client.starts)
	}
}

// AC-33（実行中・検証中のタスクがあるときは保留にしない）。
func TestMultiTask_AC33_NotHeldWhileATaskIsVerifyingOrRunning(t *testing.T) {
	t.Parallel()
	for _, st := range []TaskStatus{TaskStatusVerifying, TaskStatusInProgress} {
		f := newMultiFixture(t, 2)
		id := f.newMulti(t, "blocked", "P1", true, multiTask{key: "a"}, multiTask{key: "b", deps: []string{"a"}}, multiTask{key: "c"})
		cid, _ := parseChallengeID(id)
		setTaskStatusRaw(t, f.s, cid, "a", TaskStatusCancelled)
		setTaskStatusRaw(t, f.s, cid, "c", st)
		// 作業中のタスクは、終わっていない実行を持たなければ、再開のため起動される（保留にはならない）。
		f.runDelegationStage(t, nil)
		if got := f.detail(t, id).Status; got != StatusInProgress {
			t.Errorf("task c %s: challenge status = %s, want in_progress", st, got)
		}
		if got := f.holdReasons(t, id); len(got) != 0 {
			t.Errorf("task c %s: holds = %v, want none", st, got)
		}
	}
}

// AC-34: 全部のタスクが cancelled の、統合先を使う、in_progress の課題は、保留の reason が tasks_blocked で保留になる。
func TestMultiTask_AC34_AllCancelledHoldsTheChallenge(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "all-cancelled", "P1", true, multiTask{key: "a"}, multiTask{key: "b"})
	cid, _ := parseChallengeID(id)
	setAllTasksStatus(t, f.s, cid, TaskStatusPending, TaskStatusCancelled)
	f.runDelegationStage(t, nil)
	if got := f.detail(t, id).Status; got != StatusAwaitingHuman {
		t.Fatalf("challenge status = %s, want awaiting_human", got)
	}
	if got := f.holdReasons(t, id); len(got) != 1 || got[0] != "tasks_blocked" {
		t.Errorf("hold reasons = %v, want [tasks_blocked]", got)
	}
}

// AC-35: 未着手のタスクが、対応する子が上流で閉じているために起動されないだけの課題は、tasks_blocked の保留にならない。
func TestMultiTask_AC35_ClosedChildAloneDoesNotHoldTheChallenge(t *testing.T) {
	t.Parallel()
	f := newMultiFixture(t, 2)
	id := f.newMulti(t, "closed", "P1", true, multiTask{key: "a"}, multiTask{key: "b", deps: []string{"a"}})
	cid, _ := parseChallengeID(id)
	f.addSourceChild(t, id, "o/r#12", "closed")
	if err := f.delegateFixture.exec(`UPDATE task SET source_key = 'o/r#12' WHERE challenge_id = ? AND key = 'a'`, cid); err != nil {
		t.Fatal(err)
	}
	res := f.runDelegationStage(t, nil)
	if got := f.detail(t, id).Status; got != StatusInProgress {
		t.Errorf("challenge status = %s, want in_progress", got)
	}
	if got := f.holdReasons(t, id); len(got) != 0 {
		t.Errorf("holds = %v, want none", got)
	}
	if got := notStartedByReason(res, NotStartedUpstreamClosed); len(got) != 1 {
		t.Errorf("not_started = %+v, want upstream_closed", res.NotStarted)
	}
}

// --- 子に対応するタスクの文面 ---

const childTitle, childURL, childBody = "子の題名 CHILD-TITLE", "https://example.invalid/o/r/issues/12", "子の本文 CHILD-ONLY-BODY\n- 箇条"

func addChildRecord(t *testing.T, f *delegateFixture, id, key string) {
	t.Helper()
	cid, _ := parseChallengeID(id)
	if err := f.exec(`INSERT INTO source_child (challenge_id, source_id, external_key, parent_external_key, url, title, body, upstream_state, created_at, updated_at)
		VALUES (?, 'src', ?, ?, ?, ?, ?, 'open', '2026-09-25T00:00:00.000Z', '2026-09-25T00:00:00.000Z')`,
		cid, key, srcRootKey, childURL, childTitle, childBody); err != nil {
		t.Fatal(err)
	}
}

func taskDocOf(t *testing.T, f *delegateFixture, id, key string) string {
	t.Helper()
	cid, _ := parseChallengeID(id)
	var doc string
	tasks := taskRows(t, f.s, cid)
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		plan, _, err := loadApprovedPlan(context.Background(), tx, cid)
		if err != nil {
			return err
		}
		for _, tk := range tasks {
			if tk.Key == key {
				doc, err = loadTaskDocument(context.Background(), tx, cid, plan, tk)
				return err
			}
		}
		return errors.New("task not found")
	}); err != nil {
		t.Fatal(err)
	}
	return doc
}

// AC-69〜AC-72: 子に対応するタスクの文面の「このタスク」の節に、子の題名・URL・本文が「対応する取り込み元」としてある。
// 子に対応しないタスクの文面に、その見出しは無い。
func TestTaskDocument_AC69_AC72_SourceChildSection(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "children", "P1", twoTaskPlanSpec())
	cid, _ := parseChallengeID(id)
	addChildRecord(t, f, id, srcChildKey)
	if err := f.exec(`UPDATE task SET source_key = ? WHERE challenge_id = ? AND key = 'a'`, srcChildKey, cid); err != nil {
		t.Fatal(err)
	}
	doc := taskDocOf(t, f, id, "a")
	start, end := strings.Index(doc, "# このタスク"), strings.Index(doc, "# ほかのタスク")
	section := strings.Index(doc, "対応する取り込み元")
	if start < 0 || end < 0 || section < start || section > end {
		t.Fatalf("「対応する取り込み元」が「このタスク」の節の中に無い:\n%s", doc)
	}
	inTask := doc[start:end]
	for name, want := range map[string]string{"AC-69 題名": childTitle, "AC-70 URL": childURL, "AC-71 本文": childBody} {
		if !strings.Contains(inTask, want) {
			t.Errorf("%s: 「このタスク」の節に %q が無い:\n%s", name, want, inTask)
		}
	}
	if other := taskDocOf(t, f, id, "b"); strings.Contains(other, "対応する取り込み元") || strings.Contains(other, childBody) {
		t.Errorf("AC-72: 子に対応しないタスクの文面に「対応する取り込み元」がある:\n%s", other)
	}
}

// AC-76: 子に対応するタスクを起動したとき、上流の本文の区画は、課題の取り込み元の本文である（子の本文ではない）。
// J3 の入力の区画（form: brief）で確かめる。form: plugin の起動では、タスクの文面（Brief）に子の本文が入る。
func TestTaskDocument_AC76_UpstreamBodySectionStaysTheChallenges(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "brief", "P1", briefSpec(nil))
	if _, err := createSourceBindingForTest(f.s, id, time.Now(), validBindingInput(srcRootKey)); err != nil {
		t.Fatalf("bind: %v", err)
	}
	th := simpleThread("UP-BODY")
	th.Issue.ExternalKey, th.Issue.Repo, th.Issue.Number, th.Issue.State = srcRootKey, "o/r", 7, "open"
	f.upstream.threads[srcRootKey] = th
	addChildRecord(t, f, id, srcChildKey)
	f.pointTasksAt(t, id, srcChildKey, TaskStatusPending)
	f.mustRun(t, nil)
	if len(f.j3Inputs) != 1 {
		t.Fatalf("J3 inputs = %d, want 1", len(f.j3Inputs))
	}
	text := sectionsText(f.j3Inputs[0].Sections)
	if !strings.Contains(text, "UP-BODY") {
		t.Errorf("J3 input lacks the challenge's upstream body UP-BODY")
	}
	if strings.Contains(text, "CHILD-ONLY-BODY") {
		t.Errorf("J3 input contains the child's body; the upstream body section must stay the challenge's")
	}
}

func TestTaskDocument_PluginLaunchCarriesTheChildInTheTaskDocument(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	f.useSourceOperations(t)
	id := f.newSourceChallenge(t, "plugin")
	addChildRecord(t, f, id, srcChildKey)
	f.pointTasksAt(t, id, srcChildKey, TaskStatusPending)
	f.mustRun(t, nil)
	l := f.lastLaunch(t)
	if !strings.Contains(l.Brief, "対応する取り込み元") || !strings.Contains(l.Brief, "CHILD-ONLY-BODY") {
		t.Errorf("Brief = %q, want the task document with the source child", l.Brief)
	}
}

// 同じ課題の多数のタスクの契約の実行を、1 回の委譲の段で同時に起動しても、上限の範囲内の数だけ、取りこぼさずに起動される。
// 起動を許可する数え（run の記録の前後の二重計上）が、並行に始めた別のタスクの許可を誤って断らないことを、
// 繰り返して確かめる。
func TestMultiTask_ConcurrentLaunchesAreNotRefusedByDoubleCounting(t *testing.T) {
	t.Parallel()
	const n = 6
	for i := 0; i < 20; i++ {
		f := newMultiFixture(t, n)
		var tasks []multiTask
		for j := 0; j < n; j++ {
			tasks = append(tasks, multiTask{key: "t" + strconv.Itoa(j)})
		}
		f.newMulti(t, "many", "P1", true, tasks...)
		res := f.runDelegationStage(t, nil)
		if len(f.client.starts) != n {
			t.Fatalf("iteration %d: starts = %d, want %d (not_started = %+v)", i, len(f.client.starts), n, res.NotStarted)
		}
	}
}

// withFlowReasons は計画の構造化した出力 plan の全タスクへ flow_reason を足す。
func withFlowReasons(t *testing.T, plan string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(plan), &m); err != nil {
		t.Fatal(err)
	}
	ts, _ := m["tasks"].([]any)
	for _, x := range ts {
		x.(map[string]any)["flow_reason"] = "標準の実装で進める"
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
