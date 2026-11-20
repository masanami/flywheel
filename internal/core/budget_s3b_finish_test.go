package core

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// 区切り S3b の仕上げ 2: 予算の数え方（項目 1）。

func soleTaskSpec(impl, review float64, integration bool) string {
	return planSpec(func(m map[string]any) {
		tasks := j2TasksOf("a")
		tm := tasks[0].(map[string]any)
		tm["repo"], tm["operation"] = "main-repo", "impl"
		tm["budget_impl_usd"], tm["budget_review_usd"] = impl, review
		m["tasks"] = tasks
		m["uses_integration"] = integration
	})
}

// 統合先を使う課題を計画の作り直しでタスク 1 個にしたとき、前の版のタスクが使った額を新しいタスクの枠から引かない。
func TestTaskRemaining_ReplannedSoleTaskOfAnIntegrationChallengeIsNotChargedForThePreviousVersion(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(30, 10, 40, 10))
	cid, _ := parseChallengeID(id)
	old := taskRows(t, f.s, cid)
	costRun(t, f.s, cid, mustTaskID(t, old[0].ID), "delegate", "", "impl", 20, 0)
	costRun(t, f.s, cid, mustTaskID(t, old[0].ID), "delegate", "", "review", 4, 0)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at, answer, answered_at, answered_by, replan) VALUES (?, 'q', 'in_progress', '2026-09-26T00:00:00.000Z', 'a', '2026-09-26T00:00:01.000Z', 'alice', 1)`, cid); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO task_plan (challenge_id, version, body, created_at, spec) VALUES (?, 2, 'v2', '2026-09-26T00:00:00.000Z', ?)`,
			cid, soleTaskSpec(10, 5, true))
		return err
	})
	nt := seedPlanTasks(t, f.s, cid, 2)
	if len(nt) != 1 {
		t.Fatalf("new tasks = %d, want 1", len(nt))
	}
	if tb := f.taskBudgetOf(t, id, nt[0].ID); tb.ImplRemainingUSD != 10 {
		t.Errorf("remaining = %v, want the full frame 10 (the cancelled tasks' spend is not deducted)", tb.ImplRemainingUSD)
	}
	// 委譲の起動の実際の経路。
	in := f.input(f.cycle(t, 300), &id)
	in.Contract = f.client
	dc, err := f.s.loadDelegationContext(context.Background(), in, f.detail(t, id).Challenge)
	if err != nil {
		t.Fatalf("loadDelegationContext: %v", err)
	}
	if dc.ImplBudgetMicros != usdToMicros(10) || dc.ReviewBudgetMicros != usdToMicros(5) {
		t.Errorf("launch budget = %d / %d, want 10 / 5 USD", dc.ImplBudgetMicros, dc.ReviewBudgetMicros)
	}
}

// タスクごとに枠を書いた計画を、計画を作る判断点の登録の実際の経路で登録する。1 回の実行の上限額（20）より、規模 S の
// 既定（30）が大きい宣言でも、タスクが全部枠（10）を持つので不正な出力にならない（項目 3）。承認の要約・計画の読み取り・
// 計画の本文は、タスクの枠の合計を出す（項目 2）。
func TestPlanWithTaskFrames_RegistersAndShowsTheSumOfTheTaskFrames(t *testing.T) {
	t.Parallel()
	f := newJ2Fixture(t)
	f.agent.MaxRunBudgetUSD = 20
	f.agent.SizeBudgetsUSD.S = SizeBudgetPair{Impl: 30, Review: 10}
	ch := f.newClassified(t, "frames", "P1")
	out := j2OutputFor(false, func(m map[string]any) {
		tasks := j2TasksOf("a", "b")
		for i, tk := range tasks {
			tm := tk.(map[string]any)
			tm["repo"], tm["operation"] = "harness-repo", "brief"
			tm["budget_impl_usd"], tm["budget_review_usd"] = 10.0, float64(2+i)
		}
		tasks[1].(map[string]any)["depends_on"] = []string{"a"}
		m["tasks"] = tasks
		m["uses_integration"] = true
	})
	res, err := f.run(t, succeededInvoker(out), idPtr(ch.ID))
	if err != nil || len(res.Items) != 1 || res.Items[0].Outcome != "plan" {
		t.Fatalf("run: %v %+v", err, res)
	}
	d := f.detail(t, ch.ID)
	if d.Status != StatusAwaitingPlanApproval {
		t.Fatalf("status = %s, want awaiting_plan_approval (the plan was rejected as an invalid output)", d.Status)
	}
	body := d.Plans[len(d.Plans)-1].Body
	for _, want := range []string{"実装枠（タスクの枠の合計）: 20 USD", "レビュー対応枠（タスクの枠の合計）: 5 USD", "枠: 実装 10 USD・レビュー対応 2 USD", "枠: 実装 10 USD・レビュー対応 3 USD"} {
		if !strings.Contains(body, want) {
			t.Errorf("the plan body lacks %q:\n%s", want, body)
		}
	}
	pv, err := f.s.GetPlanView(context.Background(), ch.ID, f.agent)
	if err != nil {
		t.Fatal(err)
	}
	if pv.BudgetUSD == nil || *pv.BudgetUSD != 25 {
		t.Errorf("show --plan budget = %v, want 25 (the task frames), not the size default 40", pv.BudgetUSD)
	}
	if pv.Tasks[0].BudgetImplUSD == nil || *pv.Tasks[0].BudgetImplUSD != 10 || *pv.Tasks[1].BudgetReviewUSD != 3 {
		t.Errorf("per-task frames = %+v", pv.Tasks)
	}
	prev, err := f.s.PrepareApprovalFor(context.Background(), ch.ID, f.agent)
	if err != nil {
		t.Fatal(err)
	}
	if prev.PlanBudgetUSD == nil || *prev.PlanBudgetUSD != 25 {
		t.Errorf("approval summary budget = %v, want 25", prev.PlanBudgetUSD)
	}
}

// タスクが枠を持たない計画（規模の既定を使う）は、見直し前と同じに、規模の既定を 1 回の実行の上限額と比べる。
func TestPlanWithoutTaskFrames_StillComparesTheSizeDefaultWithTheMaxRunBudget(t *testing.T) {
	t.Parallel()
	vc := j2TestContext(t, true)
	vc.Agent.MaxRunBudgetUSD = 20
	vc.Agent.SizeBudgetsUSD.M = SizeBudgetPair{Impl: 30, Review: 10}
	if _, ok := validateJ2Output(j2PlanOutput(func(m map[string]any) { m["size"] = "M" }), vc); ok {
		t.Error("a sole task without frames must still be rejected when the size default exceeds max_run_budget_usd")
	}
}

// 項目 4: 区切り S3b で足した保留の問いの文に、用語集で使わないとした語（接続ツール・run・PR・既定ブランチ）と、
// 内部の値（状態・結果の語）が出ない。問いごとに確かめる。
func TestS3bQuestions_UseGlossaryWordsAndNoInternalValues(t *testing.T) {
	t.Parallel()
	const summary = "委譲先の要約です"
	cases := []struct {
		name string
		text string
	}{
		{"checksFailed", checksFailedQuestion("EXT-1", "abc123")},
		{"landRefused", landRefusedQuestion("EXT-1", "abc123", summary, "操作です")},
		{"landedEnded/other content", landedEndedQuestion("EXT-1", "abc123", true)},
		{"landedEnded/not landed", landedEndedQuestion("EXT-1", "abc123", false)},
		{"integrationLandablePR", integrationLandablePRQuestion("EXT-1", 2)},
		{"integrationIdentityCount", integrationIdentityCountQuestion("EXT-1", 2, 0)},
		{"integrationRefMismatch", integrationRefMismatchQuestion("EXT-1", "a", "b")},
		{"integrationSucceeded/merged", integrationSucceededQuestion("EXT-1", true, false)},
		{"integrationSucceeded/not merged", integrationSucceededQuestion("EXT-1", false, true)},
		{"reflectEnded/failed", reflectEndedQuestion("EXT-1", ContractStateFailed, summary)},
		{"reflectEnded/cancelled", reflectEndedQuestion("EXT-1", ContractStateCancelled, summary)},
		{"reflectEnded/succeeded", reflectEndedQuestion("EXT-1", ContractStateSucceeded, summary)},
		{"reflectNoLandablePR", reflectNoLandablePRQuestion("EXT-1")},
		{"reflectUnavailable", reflectUnavailableQuestion("main-repo")},
		{"integrationRefMissing", integrationRefMissingQuestion()},
		{"reflectFailed/not started", reflectFailedQuestion(reflectNotStartedDetail(&ContractRunReport{Result: RunResultErrored, State: ContractStateFailed, Error: "エラーです"}))},
		{"reflectFailed/timed out", reflectFailedQuestion(reflectNotStartedDetail(&ContractRunReport{Result: RunResultTimedOut, State: ContractStateRunning}))},
		{"reflectFailed/ended", reflectFailedQuestion(reflectNotStartedDetail(&ContractRunReport{Result: RunResultSucceeded, State: ContractStateSucceeded, Summary: summary}))},
		{"reflectFailed/no release op", reflectFailedQuestion(reflectNoReleaseOperationDetail)},
	}
	banned := []string{"接続ツール", "run", "PR", "既定ブランチ", "結果:", "状態:", "failed", "succeeded", "cancelled", "running", "errored", "timed_out", "launch_failed", "invalid_output", "budget_exhausted", "interrupted", "malformed"}
	for _, c := range cases {
		for _, w := range banned {
			if strings.Contains(c.text, w) {
				t.Errorf("%s: the question contains %q:\n%s", c.name, w, c.text)
			}
		}
	}
}

// 承認の後は、計画の読み取りの上限とタスクごとの額が、`flywheel budget --task` の増額を含む実効値になり、
// 済みのタスクを残して作り直した計画の上限は、済みのタスクの枠を含む。
func TestPlanView_AfterApprovalAndReplanTheLimitFollowsTheRecordedFrames(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 4, 5))
	cid, _ := parseChallengeID(id)
	pv, err := f.s.GetPlanView(context.Background(), id, f.agent)
	if err != nil || pv.BudgetUSD == nil || *pv.BudgetUSD != 14 {
		t.Fatalf("approved plan limit = %v (%v), want 14", pv.BudgetUSD, err)
	}
	old := taskRows(t, f.s, cid)
	if _, err := f.raise(t, id, old[0].ID, 8, nil); err != nil {
		t.Fatalf("raise: %v", err)
	}
	pv, _ = f.s.GetPlanView(context.Background(), id, f.agent)
	if *pv.BudgetUSD != 14+5 || *pv.Tasks[0].BudgetImplUSD != 8 {
		t.Errorf("after the raise: limit = %v, task a impl = %v, want 19 and 8", *pv.BudgetUSD, *pv.Tasks[0].BudgetImplUSD)
	}
	// a は済み。b は取りやめ。新しい版は 1 個（10・6）。上限は済みの a（8+2）と新しいタスク（10+6）の合計。
	setBudgetTaskStatus(t, f.s, mustTaskID(t, old[0].ID), "done")
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO task_plan (challenge_id, version, body, created_at, spec) VALUES (?, 2, 'v2', '2026-09-26T00:00:00.000Z', ?)`, cid, soleTaskSpec(10, 6, true))
		return err
	})
	pv, _ = f.s.GetPlanView(context.Background(), id, f.agent) // 承認の前の新しい版
	if *pv.BudgetUSD != 8+2+10+6 {
		t.Errorf("limit before approving the new version = %v, want 26 (the done task's frame included)", *pv.BudgetUSD)
	}
	seedPlanTasks(t, f.s, cid, 2)
	setBudgetTaskStatus(t, f.s, mustTaskID(t, old[0].ID), "done") // seedPlanTasks は未完のタスクだけを取りやめる
	pv, _ = f.s.GetPlanView(context.Background(), id, f.agent)
	if *pv.BudgetUSD != 26 {
		t.Errorf("limit after approving the new version = %v, want 26", *pv.BudgetUSD)
	}
}

// 統合先を使わない課題は、計画を作り直したタスク 1 個の枠から、前の版が使った額を引き続ける（動きを変えない）。
func TestTaskRemaining_ReplannedSoleTaskWithoutIntegrationStillAccumulates(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	id := f.newInProgress(t, "one", "P2", soleTaskSpec(30, 10, false))
	cid, _ := parseChallengeID(id)
	old := taskRows(t, f.s, cid)
	costRun(t, f.s, cid, mustTaskID(t, old[0].ID), "delegate", "", "impl", 20, 0)
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO hold (challenge_id, question, from_status, raised_at, answer, answered_at, answered_by, replan) VALUES (?, 'q', 'in_progress', '2026-09-26T00:00:00.000Z', 'a', '2026-09-26T00:00:01.000Z', 'alice', 1)`, cid); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO task_plan (challenge_id, version, body, created_at, spec) VALUES (?, 2, 'v2', '2026-09-26T00:00:00.000Z', ?)`, cid, soleTaskSpec(30, 10, false))
		return err
	})
	nt := seedPlanTasks(t, f.s, cid, 2)
	if tb := f.taskBudgetOf(t, id, nt[0].ID); tb.ImplRemainingUSD != 10 {
		t.Errorf("remaining = %v, want 30 - 20 (the spend accumulates across the replan)", tb.ImplRemainingUSD)
	}
}
