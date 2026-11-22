package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

// 予算をタスクごとの見込みにする規則（区切り S3b。AC-37〜68・231・249）を、実ストアで確かめる。

// twoTaskSpec は、タスク a・b（b は a の後）を持つ計画の構造化した出力。計画の実装の枠（99）はタスクの合計と違う値にする。
func twoTaskSpec(implA, reviewA, implB, reviewB float64) string {
	return planSpec(func(m map[string]any) {
		tasks := j2TasksOf("a", "b")
		for i, tk := range tasks {
			tm := tk.(map[string]any)
			tm["repo"], tm["operation"] = "main-repo", "impl"
			vals := [][2]float64{{implA, reviewA}, {implB, reviewB}}[i]
			tm["budget_impl_usd"], tm["budget_review_usd"] = vals[0], vals[1]
		}
		tasks[1].(map[string]any)["depends_on"] = []string{"a"}
		m["tasks"] = tasks
		m["uses_integration"] = true
		m["budget_impl_usd"] = 99.0
		m["budget_review_usd"] = 88.0
	})
}

func microsOf(t *testing.T, s *Store, q string, args ...any) sql.NullInt64 {
	t.Helper()
	var v sql.NullInt64
	readForTest(t, s, func(tx *sql.Tx) error { return tx.QueryRow(q, args...).Scan(&v) })
	return v
}

// costRun は終わった run を直に記録する（費用 costUSD。taskID が 0 ならタスクに結び付けない）。
func costRun(t *testing.T, s *Store, cid, taskID int64, kind, judgment, bucket string, costUSD float64, reflectMark int) {
	t.Helper()
	var task, j any
	if taskID != 0 {
		task = taskID
	}
	if judgment != "" {
		j = judgment
	}
	writeForTest(t, s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO run (challenge_id, kind, judgment, challenge_version, plan_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket, cost_usd, task_id, reflect)
			VALUES (?, ?, ?, 1, 1, 's', 1, 'h', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:01.000Z', 'succeeded', 1000000, ?, ?, ?, ?)`,
			cid, kind, j, bucket, usdToMicros(costUSD), task, reflectMark)
		return err
	})
}

func (f *delegateFixture) budgetOf(t *testing.T, id string) *ChallengeBudget {
	t.Helper()
	b, err := f.s.GetChallengeBudget(context.Background(), id, f.agent)
	if err != nil {
		t.Fatalf("GetChallengeBudget: %v", err)
	}
	return b
}

func (f *delegateFixture) taskBudgetOf(t *testing.T, id, taskID string) TaskBudget {
	t.Helper()
	for _, tb := range f.budgetOf(t, id).Tasks {
		if tb.TaskID == taskID {
			return tb
		}
	}
	t.Fatalf("task %s not found in the budget of %s", taskID, id)
	return TaskBudget{}
}

// raise は `flywheel budget` の①と②を通す。
func (f *delegateFixture) raise(t *testing.T, id, taskID string, impl float64, review *float64) (*Approval, error) {
	t.Helper()
	ctx := context.Background()
	prev, err := f.s.PrepareBudgetForTask(ctx, id, taskID, f.agent, impl, review)
	if err != nil {
		return nil, err
	}
	return f.s.ExecuteBudget(ctx, BudgetRequest{ChallengeID: id, ExpectedVersion: prev.Version, PlanVersion: prev.PlanVersion,
		TaskID: prev.TaskID, ImplUSD: impl, ReviewUSD: review}, f.budgetAttestation(t, id))
}

// --- 計画の検査（AC-37〜41） ---

func j2TwoTaskOutput(mut func(tasks []any)) []byte {
	return j2PlanOutput(func(m map[string]any) {
		tasks := j2TasksOf("a", "b")
		if mut != nil {
			mut(tasks)
		}
		m["tasks"] = tasks
		m["uses_integration"] = true
	})
}

func TestJ2TaskBudgets_PlanValidation(t *testing.T) {
	t.Parallel()
	maxRun := defaultAgentDeclaration().MaxRunBudgetUSD
	cases := []struct {
		name string
		mut  func(tasks []any)
		ok   bool
	}{
		{"AC-37: both tasks carry both budgets", nil, true},
		{"AC-38: a task without budget_impl_usd", func(ts []any) { delete(ts[1].(map[string]any), "budget_impl_usd") }, false},
		{"AC-38: a task whose budget_impl_usd is null", func(ts []any) { ts[1].(map[string]any)["budget_impl_usd"] = nil }, false},
		{"AC-39: a task without budget_review_usd", func(ts []any) { delete(ts[0].(map[string]any), "budget_review_usd") }, false},
		{"AC-40: budget_impl_usd of zero", func(ts []any) { ts[0].(map[string]any)["budget_impl_usd"] = 0.0 }, false},
		{"AC-40: budget_impl_usd below zero", func(ts []any) { ts[0].(map[string]any)["budget_impl_usd"] = -1.0 }, false},
		{"AC-40: budget_review_usd of zero", func(ts []any) { ts[0].(map[string]any)["budget_review_usd"] = 0.0 }, false},
		{"AC-41: budget_impl_usd above max_run_budget_usd", func(ts []any) { ts[1].(map[string]any)["budget_impl_usd"] = maxRun + 0.01 }, false},
		{"AC-41: budget_impl_usd equal to max_run_budget_usd", func(ts []any) { ts[1].(map[string]any)["budget_impl_usd"] = maxRun }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, ok := validateJ2Output(j2TwoTaskOutput(c.mut), j2TestContext(t, true))
			if ok != c.ok {
				t.Fatalf("valid = %v, want %v", ok, c.ok)
			}
			if ok && (v.Tasks[0].BudgetImplUSD == nil || *v.Tasks[0].BudgetImplUSD != 3) {
				t.Errorf("task budgets are not carried into the validated output: %+v", v.Tasks[0])
			}
		})
	}
}

func TestJ2TaskBudgets_SingleTaskPlanMayOmitTheKeys(t *testing.T) {
	t.Parallel()
	one := func(mut func(tk map[string]any)) []byte {
		return j2PlanOutput(func(m map[string]any) {
			tk := j2TestTask("a", "flywheel", "impl")
			if mut != nil {
				mut(tk)
			}
			m["tasks"] = []any{tk}
		})
	}
	if _, ok := validateJ2Output(one(nil), j2TestContext(t, true)); !ok {
		t.Error("a single task without budget keys must be valid (it takes the pre-S3b decision)")
	}
	if _, ok := validateJ2Output(one(func(tk map[string]any) { tk["budget_impl_usd"] = 0.0 }), j2TestContext(t, true)); ok {
		t.Error("a single task whose budget_impl_usd is 0 must be invalid")
	}
}

// --- 承認でタスクの記録へ入れる（AC-42・43・249） ---

func approveWithAgent(t *testing.T, f *paFixture, agent *AgentDeclaration) {
	t.Helper()
	prev, err := f.s.PrepareApproval(context.Background(), f.id)
	if err != nil {
		t.Fatalf("PrepareApproval: %v", err)
	}
	if _, _, err := f.s.ExecuteApproval(context.Background(), ApprovalRequest{ChallengeID: f.id, ExpectedVersion: prev.Version,
		Decision: ApprovalDecisionApproved, Agent: agent}, ttyAtt(t, f.id)); err != nil {
		t.Fatalf("ExecuteApproval: %v", err)
	}
}

func TestPlanApproval_SoleTaskWithoutKeysTakesThePreS3bDecision_AC42(t *testing.T) {
	t.Parallel()
	f := newPAFixture(t, 1, false) // 計画の実装の枠は 41.5、レビュー対応の枠は書かない（サイズ M の既定）
	agent := defaultAgentDeclaration()
	approveWithAgent(t, f, agent)
	pair, _ := agent.SizeBudgetFor(JudgmentSize("M"))
	if got := microsOf(t, f.s, `SELECT budget_impl_micros FROM task`); !got.Valid || got.Int64 != usdToMicros(41.5) {
		t.Errorf("budget_impl_micros = %+v, want the plan's 41.5 USD", got)
	}
	if got := microsOf(t, f.s, `SELECT budget_review_micros FROM task`); !got.Valid || got.Int64 != usdToMicros(pair.Review) {
		t.Errorf("budget_review_micros = %+v, want the size default %v USD", got, pair.Review)
	}
}

func TestPlanApproval_SoleTaskWithoutKeysAndWithoutADeclarationIsReadAsThePreS3bDecision_AC249(t *testing.T) {
	t.Parallel()
	f := newPAFixture(t, 1, false)
	approveWithAgent(t, f, nil) // 宣言が読めなければ、タスクの記録は空のまま残る
	if got := microsOf(t, f.s, `SELECT budget_impl_micros FROM task`); got.Valid {
		t.Fatalf("budget_impl_micros = %+v, want NULL without a declaration", got)
	}
	agent := defaultAgentDeclaration()
	pair, _ := agent.SizeBudgetFor(JudgmentSize("M"))
	got, err := f.s.GetChallengeBudget(context.Background(), f.id, agent)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tasks) != 1 || !got.Tasks[0].Known || got.Tasks[0].ImplUSD != 41.5 || got.Tasks[0].ReviewUSD != pair.Review {
		t.Fatalf("tasks = %+v, want impl 41.5 (the plan) and review %v (the size default)", got.Tasks, pair.Review)
	}
}

func TestPlanApproval_TasksWithKeysRecordTheirValues_AC43(t *testing.T) {
	t.Parallel()
	f := newPAFixture(t, 2, true) // 2 個のタスクが 3 USD・2 USD を持つ
	approveWithAgent(t, f, defaultAgentDeclaration())
	for _, col := range []struct {
		name string
		want int64
	}{{"budget_impl_micros", usdToMicros(3)}, {"budget_review_micros", usdToMicros(2)}} {
		var got []int64
		readForTest(t, f.s, func(tx *sql.Tx) error {
			rows, err := tx.Query(`SELECT ` + col.name + ` FROM task ORDER BY id`)
			if err != nil {
				return err
			}
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var v int64
				if err := rows.Scan(&v); err != nil {
					return err
				}
				got = append(got, v)
			}
			return rows.Err()
		})
		if !reflect.DeepEqual(got, []int64{col.want, col.want}) {
			t.Errorf("%s = %v, want both tasks %d", col.name, got, col.want)
		}
	}
}

// --- 課題の上限・使った額（AC-44〜48） ---

func TestChallengeBudget_LimitIsTheSumOfTheTaskBudgets_AC44_AC45(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 4, 5))
	b := f.budgetOf(t, id)
	if !b.EstimateKnown || b.ImplEstimateUSD != 7 {
		t.Errorf("impl estimate = %v (known %v), want 3+4 = 7, not the plan's 99", b.ImplEstimateUSD, b.EstimateKnown)
	}
	if b.ReviewEstimateUSD != 7 {
		t.Errorf("review estimate = %v, want 2+5 = 7, not the plan's 88", b.ReviewEstimateUSD)
	}
}

func TestChallengeBudget_AfterReplanDoneTasksAndNewTasksAreSummed_AC46_AC47(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 4, 5))
	cid, _ := parseChallengeID(id)
	old := taskRows(t, f.s, cid) // a・b
	// a は済み。b は計画の作り直しで取りやめになる。
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task SET status = 'done' WHERE id = ?`, mustTaskID(t, old[0].ID))
		return err
	})
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO task_plan (challenge_id, version, body, created_at, spec) VALUES (?, 2, 'v2', '2026-09-26T00:00:00.000Z', ?)`,
			cid, twoTaskSpec(10, 6, 20, 7))
		return err
	})
	seedPlanTasks(t, f.s, cid, 2)
	b := f.budgetOf(t, id)
	// 済みの a（3）＋新しい版の 2 個（10・20）。取りやめの b（4）は入らない。
	if b.ImplEstimateUSD != 3+10+20 {
		t.Errorf("AC-46/47: impl estimate = %v, want 3 (done) + 10 + 20 (new version), without the cancelled task's 4", b.ImplEstimateUSD)
	}
	if b.ReviewEstimateUSD != 2+6+7 {
		t.Errorf("review estimate = %v, want 2 + 6 + 7", b.ReviewEstimateUSD)
	}
}

func setBudgetTaskStatus(t *testing.T, s *Store, id int64, status string) {
	t.Helper()
	writeForTest(t, s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task SET status = ? WHERE id = ?`, status, id)
		return err
	})
}

func mustTaskID(t *testing.T, s string) int64 {
	t.Helper()
	id, ok := parseTaskID(s)
	if !ok {
		t.Fatalf("malformed task id %q", s)
	}
	return id
}

func TestChallengeBudget_CancelledTasksSpendCountsTowardTheChallengeButNotTheNewTasks_AC48(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(30, 10, 40, 10))
	cid, _ := parseChallengeID(id)
	old := taskRows(t, f.s, cid)
	costRun(t, f.s, cid, mustTaskID(t, old[1].ID), "delegate", "", "impl", 6, 0)
	costRun(t, f.s, cid, mustTaskID(t, old[1].ID), "delegate", "", "review", 1, 0)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO task_plan (challenge_id, version, body, created_at, spec) VALUES (?, 2, 'v2', '2026-09-26T00:00:00.000Z', ?)`,
			cid, twoTaskSpec(30, 10, 40, 10)); err != nil {
			return err
		}
		return nil
	})
	seedPlanTasks(t, f.s, cid, 2) // a・b は取りやめになる
	b := f.budgetOf(t, id)
	if b.ImplSpentUSD != 6 || b.ReviewSpentUSD != 1 {
		t.Errorf("AC-48: challenge spent = %v impl / %v review, want the cancelled task's 6 / 1 included", b.ImplSpentUSD, b.ReviewSpentUSD)
	}
	if b.ImplEstimateUSD != 70 {
		t.Errorf("impl estimate = %v, want the new version's 30 + 40 only", b.ImplEstimateUSD)
	}
	for _, tb := range b.Tasks {
		if tb.Status == TaskStatusPending && tb.ImplRemainingUSD != tb.ImplUSD {
			t.Errorf("new task %s remaining = %v, want its full frame %v (the cancelled task's spend is not deducted)", tb.TaskID, tb.ImplRemainingUSD, tb.ImplUSD)
		}
	}
}

// --- 実行の上限額（AC-49・50） ---

func TestTaskRemaining_OtherTasksSpendIsNotDeducted_AC49_AC50(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(10, 4, 20, 6))
	cid, _ := parseChallengeID(id)
	tasks := taskRows(t, f.s, cid)
	a, b := mustTaskID(t, tasks[0].ID), mustTaskID(t, tasks[1].ID)
	setBudgetTaskStatus(t, f.s, b, "verifying")           // 起動できるタスクを a だけにする
	costRun(t, f.s, cid, a, "delegate", "", "impl", 2, 0) // a 自身が使った額は引く
	costRun(t, f.s, cid, b, "delegate", "", "impl", 5, 0) // b が使った額は a の枠から引かない
	costRun(t, f.s, cid, b, "delegate", "", "review", 3, 0)

	in := f.input(f.cycle(t, 300), &id)
	in.Contract = f.client
	dc, err := f.s.loadDelegationContext(context.Background(), in, f.detail(t, id).Challenge)
	if err != nil {
		t.Fatalf("loadDelegationContext: %v", err)
	}
	// a の実装の枠の残り 10-2 = 8 と、レビュー対応の枠の残り 4。
	if dc.ImplBudgetMicros != usdToMicros(8) || dc.ReviewBudgetMicros != usdToMicros(4) {
		t.Errorf("AC-50: remaining = %d / %d, want 8 / 4 USD (task b's spend is not deducted)", dc.ImplBudgetMicros, dc.ReviewBudgetMicros)
	}
	if _, ns, err := f.s.delegateViaContract(context.Background(), in, nil, dc, nil); err != nil || ns != nil {
		t.Fatalf("delegateViaContract: %v %+v", err, ns)
	}
	if got := microsOf(t, f.s, `SELECT max_budget_usd FROM contract_run WHERE challenge_id = ?`, cid); got.Int64 != usdToMicros(12) {
		t.Errorf("AC-49: contract_run.max_budget_usd = %d, want 12 USD (8 + 4)", got.Int64)
	}
	if tb := f.taskBudgetOf(t, id, tasks[1].ID); tb.ImplRemainingUSD != 15 {
		t.Errorf("task b remaining = %v, want 20 - 5", tb.ImplRemainingUSD)
	}
}

// --- 上限を超えたタスクだけを取り消す（AC-51〜53） ---

func TestOverBudget_OnlyTheExceedingTasksRunIsCancelledAndHeldForThatTask_AC51_AC52_AC53(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 3, 2))
	cid, _ := parseChallengeID(id)
	tasks := taskRows(t, f.s, cid)
	a, b := mustTaskID(t, tasks[0].ID), mustTaskID(t, tasks[1].ID)
	setBudgetTaskStatus(t, f.s, b, "verifying") // 起動できるタスクを a だけにする
	// a は start で起動する（b は a の後なので、b の実行は直に足す）。
	if _, err := f.s.StartContractDelegation(context.Background(), ContractStartInput{
		ChallengeID: id, ConnectorID: "tool", AgentDecl: f.agent, ConnDecl: f.conn, Client: f.client,
		CycleID: f.cycle(t, 300), MaxBudgetUSD: 5, Reconcile: f.branches,
	}); err != nil {
		t.Fatalf("StartContractDelegation: %v", err)
	}
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT name FROM pragma_table_info('contract_run') WHERE name <> 'id'`)
		if err != nil {
			return err
		}
		var cols []string
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				return err
			}
			cols = append(cols, n)
		}
		_ = rows.Close()
		sel := make([]string, len(cols))
		for i, c := range cols {
			switch c {
			case "external_run_id":
				sel[i] = "'ext-b'"
			case "task_id":
				sel[i] = fmt.Sprint(b)
			default:
				sel[i] = c
			}
		}
		_, err = tx.Exec(fmt.Sprintf(`INSERT INTO contract_run (%s) SELECT %s FROM contract_run WHERE external_run_id = 'ext-1'`,
			strings.Join(cols, ","), strings.Join(sel, ",")))
		return err
	})
	if got := f.count(t, `SELECT COUNT(*) FROM contract_run WHERE challenge_id = `+fmt.Sprint(cid)+` AND ended_at IS NULL`); got != 2 {
		t.Fatalf("open contract runs = %d, want 2 (one per task)", got)
	}
	// a の実行の費用の累計だけが上限額（3 + 2 = 5 USD）を超える。
	f.client.cancelOut = contractOut(ContractStateCancelled, float64Ptr(6), nil)
	f.client.queue = []ContractLaunchOutput{
		contractOut(ContractStateRunning, float64Ptr(6), nil),
		contractOut(ContractStateRunning, float64Ptr(1), nil),
	}
	f.poll(t)
	if want := [][]string{{"tool", "cancel", "ext-1"}}; !reflect.DeepEqual(f.client.cancels, want) {
		t.Fatalf("AC-51/52: cancels = %v, want only the exceeding task's run %v", f.client.cancels, want)
	}
	if got := f.count(t, `SELECT COUNT(*) FROM contract_run WHERE external_run_id = 'ext-b' AND ended_at IS NULL`); got != 1 {
		t.Errorf("the other task's run must stay open, open = %d", got)
	}
	var holdTask sql.NullInt64
	readForTest(t, f.s, func(tx *sql.Tx) error { return tx.QueryRow(`SELECT task_id FROM hold`).Scan(&holdTask) })
	if !holdTask.Valid || holdTask.Int64 != a {
		t.Errorf("AC-53: hold.task_id = %+v, want the exceeding task %d", holdTask, a)
	}
}

// --- flywheel budget（AC-54〜63・231） ---

func TestBudgetTask_RaisesOnlyThatTasksFrameAndRecordsTheTask_AC54_AC55_AC56_AC231(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 4, 5))
	cid, _ := parseChallengeID(id)
	tasks := taskRows(t, f.s, cid)
	if _, err := f.raise(t, id, tasks[0].ID, 30, nil); err != nil {
		t.Fatalf("raise: %v", err)
	}
	a, b := f.taskBudgetOf(t, id, tasks[0].ID), f.taskBudgetOf(t, id, tasks[1].ID)
	if a.ImplUSD != 30 {
		t.Errorf("AC-54/231: task a impl frame = %v, want 30", a.ImplUSD)
	}
	if b.ImplUSD != 4 || b.ReviewUSD != 5 {
		t.Errorf("AC-56: task b frames = %v / %v, want unchanged 4 / 5", b.ImplUSD, b.ReviewUSD)
	}
	if got := microsOf(t, f.s, `SELECT task_id FROM approval WHERE kind = 'budget'`); !got.Valid || got.Int64 != mustTaskID(t, tasks[0].ID) {
		t.Errorf("AC-55: approval.task_id = %+v, want %s", got, tasks[0].ID)
	}
	if a.ReviewUSD != 2 {
		t.Errorf("AC-62: review frame = %v, want unchanged 2 when --review-usd is omitted", a.ReviewUSD)
	}
	if got := f.budgetOf(t, id); got.ImplEstimateUSD != 34 {
		t.Errorf("impl estimate = %v, want 30 + 4", got.ImplEstimateUSD)
	}
}

func TestBudgetTask_ReviewFrameAndRepeatedRaiseReplace_AC61_AC63(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 4, 5))
	cid, _ := parseChallengeID(id)
	tasks := taskRows(t, f.s, cid)
	review := 9.0
	if _, err := f.raise(t, id, tasks[0].ID, 30, &review); err != nil {
		t.Fatal(err)
	}
	if a := f.taskBudgetOf(t, id, tasks[0].ID); a.ReviewUSD != 9 {
		t.Errorf("AC-61: review frame = %v, want 9", a.ReviewUSD)
	}
	if _, err := f.raise(t, id, tasks[0].ID, 12, nil); err != nil {
		t.Fatal(err)
	}
	if a := f.taskBudgetOf(t, id, tasks[0].ID); a.ImplUSD != 12 || a.ReviewUSD != 9 {
		t.Errorf("AC-63: frames = %v / %v after two raises, want the later 12 (not 42) and the review 9 kept", a.ImplUSD, a.ReviewUSD)
	}
}

func TestBudgetTask_WithoutTaskFlag_AC57_AC58(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	two := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 4, 5))
	_, err := f.s.PrepareBudgetForTask(context.Background(), two, "", f.agent, 10, nil)
	if !errors.Is(err, ErrBudgetTaskRequired) {
		t.Fatalf("AC-57: err = %v, want ErrBudgetTaskRequired (usage_error, exit code 2)", err)
	}
	// 未完のタスクが 1 個（もう 1 個は済み）なら、そのタスクの枠を改める。
	cid, _ := parseChallengeID(two)
	tasks := taskRows(t, f.s, cid)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task SET status = 'done' WHERE id = ?`, mustTaskID(t, tasks[0].ID))
		return err
	})
	if _, err := f.raise(t, two, "", 11, nil); err != nil {
		t.Fatalf("AC-58: %v", err)
	}
	if b := f.taskBudgetOf(t, two, tasks[1].ID); b.ImplUSD != 11 {
		t.Errorf("AC-58: the only unfinished task's frame = %v, want 11", b.ImplUSD)
	}
	if a := f.taskBudgetOf(t, two, tasks[0].ID); a.ImplUSD != 3 {
		t.Errorf("the finished task's frame = %v, want unchanged 3", a.ImplUSD)
	}
}

func TestBudgetTask_ATaskOfAnotherChallengeFailsAndLeavesTheStore_AC59(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	one := f.newInProgress(t, "one", "P2", twoTaskSpec(3, 2, 4, 5))
	other := f.newInProgress(t, "other", "P2", twoTaskSpec(6, 2, 7, 5))
	ocid, _ := parseChallengeID(other)
	foreign := taskRows(t, f.s, ocid)[0].ID
	before := f.budgetOf(t, one)
	beforeApprovals := f.count0(t, `SELECT COUNT(*) FROM approval WHERE kind = 'budget'`)
	for _, bad := range []string{foreign, "T-9999", "x"} {
		if _, err := f.raise(t, one, bad, 50, nil); err == nil {
			t.Errorf("task %q: want a failure", bad)
		}
	}
	if !reflect.DeepEqual(before, f.budgetOf(t, one)) || !reflect.DeepEqual(f.budgetOf(t, other).Tasks[0].ImplUSD, 6.0) {
		t.Error("the store changed after a rejected request")
	}
	if got := f.count0(t, `SELECT COUNT(*) FROM approval WHERE kind = 'budget'`); got != beforeApprovals {
		t.Errorf("budget approvals = %d, want %d", got, beforeApprovals)
	}
}

func (f *delegateFixture) count0(t *testing.T, q string) int64 {
	t.Helper()
	return microsOf(t, f.s, q).Int64
}

// --- 使った額に数えないもの（AC-60・64・65） ---

func TestTaskSpent_DoesNotCountJudgmentCostsOrReflectRuns_AC60_AC64_AC65(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(30, 10, 40, 10))
	cid, _ := parseChallengeID(id)
	tasks := taskRows(t, f.s, cid)
	a := mustTaskID(t, tasks[0].ID)
	costRun(t, f.s, cid, a, "delegate", "", "impl", 2, 0)
	costRun(t, f.s, cid, a, "judgment", "J5", "judgment", 7, 0) // タスクの検証（J5）
	costRun(t, f.s, cid, 0, "judgment", "J5", "judgment", 5, 0) // 課題の単位の確かめ（J5）
	costRun(t, f.s, cid, a, "judgment", "J4", "judgment", 3, 0) // 上げる依頼（J4）
	costRun(t, f.s, cid, a, "contract", "", "impl", 11, 1)      // 反映の依頼の run
	b := f.budgetOf(t, id)
	if got := f.taskBudgetOf(t, id, tasks[0].ID).SpentUSD; got != 2 {
		t.Errorf("AC-60/64: task a spent_usd = %v, want 2 (judgment and reflect costs excluded)", got)
	}
	if got := f.taskBudgetOf(t, id, tasks[1].ID).SpentUSD; got != 0 {
		t.Errorf("AC-65: task b spent_usd = %v, want 0 (the challenge-level J5 cost is no task's)", got)
	}
	if b.ImplSpentUSD != 2 || b.ReviewSpentUSD != 0 {
		t.Errorf("challenge spent = %v / %v, want 2 / 0", b.ImplSpentUSD, b.ReviewSpentUSD)
	}
}

// --- 反映の印（AC-66・67） ---

func TestRunReflectMark_ReflectRequestIsOneAndTaskRunIsZero_AC66_AC67(t *testing.T) {
	t.Parallel()
	f := newReflFixture(t)
	f.started(t)
	if got := microsOf(t, f.s, `SELECT reflect FROM run WHERE operation_id IS NOT NULL ORDER BY id DESC LIMIT 1`); !got.Valid || got.Int64 != 1 {
		t.Errorf("AC-66: reflect of the reflect request's run = %+v, want 1", got)
	}
	cf := newContractFixture(t)
	if _, err := cf.start(t); err != nil {
		t.Fatal(err)
	}
	if got := microsOf(t, cf.s, `SELECT reflect FROM run WHERE kind = 'contract' AND task_id IS NOT NULL`); !got.Valid || got.Int64 != 0 {
		t.Errorf("AC-67: reflect of the task's run = %+v, want 0", got)
	}
}

// --- 予算切れの一覧（AC-68） ---

func TestStatus_BudgetExhaustedNamesTheTask_AC68(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 40, 5))
	cid, _ := parseChallengeID(id)
	tasks := taskRows(t, f.s, cid)
	costRun(t, f.s, cid, mustTaskID(t, tasks[0].ID), "delegate", "", "impl", 3, 0) // a の実装の枠が尽きる
	got := f.overview(t).NeedsHumanBudgetExhausted
	if len(got) != 1 || got[0].ChallengeID != id || got[0].TaskID != tasks[0].ID || got[0].ImplRemainingUSD != 0 {
		t.Fatalf("budget_exhausted = %+v, want exactly task %s of %s", got, tasks[0].ID, id)
	}
}

func TestStatus_ImplStoppedIsJudgedPerTask(t *testing.T) {
	t.Parallel()
	f := newDelegateFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(30, 2, 40, 5))
	cid, _ := parseChallengeID(id)
	tasks := taskRows(t, f.s, cid)
	a, b := mustTaskID(t, tasks[0].ID), mustTaskID(t, tasks[1].ID)
	stop := func(task int64) {
		writeForTest(t, f.s, func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO run (challenge_id, kind, challenge_version, plan_version, session_id, pid, host, heartbeat_at, started_at, ended_at, result, max_budget_usd, budget_bucket, task_id)
				VALUES (?, 'contract', 1, 1, 's', 1, 'h', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:01.000Z', 'budget_exhausted', 1000000, 'impl', ?)`, cid, task)
			return err
		})
	}
	stop(a)
	if got := f.overview(t).NeedsHumanBudgetExhausted; len(got) != 1 || got[0].TaskID != tasks[0].ID {
		t.Fatalf("budget_exhausted = %+v, want only the stopped task %s", got, tasks[0].ID)
	}
	// a の枠を置き換えると a は消え、b の打ち切りは b だけを巻き込む。
	if _, err := f.raise(t, id, tasks[0].ID, 30, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.overview(t).NeedsHumanBudgetExhausted; len(got) != 0 {
		t.Fatalf("budget_exhausted = %+v, want none after raising task a", got)
	}
	stop(b)
	if got := f.overview(t).NeedsHumanBudgetExhausted; len(got) != 1 || got[0].TaskID != tasks[1].ID {
		t.Fatalf("budget_exhausted = %+v, want only task %s (task a's raise does not cover task b)", got, tasks[1].ID)
	}
}

func TestMicrosNeverNaN(t *testing.T) { // 枠の額の検査が NaN を通さない
	t.Parallel()
	if validBudgetAmount(math.NaN()) {
		t.Fatal("NaN must not be a valid amount")
	}
}
