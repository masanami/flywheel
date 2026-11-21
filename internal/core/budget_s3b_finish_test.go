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
	for _, want := range []string{"実装枠（この計画のタスクの枠の合計）: 20 USD", "レビュー対応枠（この計画のタスクの枠の合計）: 5 USD", "枠: 実装 10 USD・レビュー対応 2 USD", "枠: 実装 10 USD・レビュー対応 3 USD"} {
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
		{"reflectFailed/no release op", reflectNoReleaseOperationQuestion("C-3", "https://github.com/o/r/pull/12")},
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

// 仕上げ 3 項目 5: 統合先を使う課題で済みのタスクがあるとき、枠を書かない 1 個のタスクの計画の上限は、規模の既定で引いた枠と、済みのタスクの枠の合計
// （承認の後の課題の上限と同じ額）。
func TestPlanView_SoleTaskWithoutFramesAddsTheDoneTasksFrames(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 4, 5))
	cid, _ := parseChallengeID(id)
	old := taskRows(t, f.s, cid)
	setBudgetTaskStatus(t, f.s, mustTaskID(t, old[0].ID), "done") // 済みのタスクの枠は 3 + 2
	noFrames := planSpec(func(m map[string]any) {
		tasks := j2TasksOf("a")
		tm := tasks[0].(map[string]any)
		tm["repo"], tm["operation"] = "main-repo", "impl"
		m["tasks"] = tasks
		m["uses_integration"] = true
	})
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO task_plan (challenge_id, version, body, created_at, spec) VALUES (?, 2, 'v2', '2026-09-26T00:00:00.000Z', ?)`, cid, noFrames)
		return err
	})
	impl, review, ok := resolveSpecBudgets(noFrames, f.agent)
	if !ok {
		t.Fatal("the size default cannot be resolved")
	}
	want := 3.0 + 2.0 + impl + review
	pv, err := f.s.GetPlanView(context.Background(), id, f.agent) // 承認の前の新しい版
	if err != nil || pv.BudgetUSD == nil || *pv.BudgetUSD != want {
		t.Fatalf("limit before approving = %v (%v), want %v (the size default plus the done task's frame)", pv.BudgetUSD, err, want)
	}
	// 承認の後（fillSoleTaskBudgets が規模の既定をタスクの枠へ入れる）の課題の上限と同じ額。
	seedPlanTasks(t, f.s, cid, 2)
	setBudgetTaskStatus(t, f.s, mustTaskID(t, old[0].ID), "done")
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task SET budget_impl_micros = ?, budget_review_micros = ? WHERE challenge_id = ? AND plan_version = 2`, usdToMicros(impl), usdToMicros(review), cid)
		return err
	})
	var bs []taskBudget
	readForTest(t, f.s, func(tx *sql.Tx) (err error) {
		bs, err = loadTaskBudgets(context.Background(), tx, cid, f.agent)
		return err
	})
	if sum, ok := sumTaskBudgets(bs); !ok || microsToUSD(sum.Impl+sum.Review) != want {
		t.Errorf("limit after approving = %+v, want %v", sum, want)
	}
}

// タスクが 1 個で枠を省いた計画の承認の要約（承認の前）は、core が当てる既定の枠をタスクの枠として出す。
func TestPlanView_SoleTaskWithoutFramesShowsTheDefaultFrame(t *testing.T) {
	t.Parallel()
	f := newContractFixture(t)
	id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 4, 5))
	cid, _ := parseChallengeID(id)
	noFrames := planSpec(func(m map[string]any) {
		tasks := j2TasksOf("a")
		tm := tasks[0].(map[string]any)
		tm["repo"], tm["operation"] = "main-repo", "impl"
		m["tasks"] = tasks
	})
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO task_plan (challenge_id, version, body, created_at, spec) VALUES (?, 2, 'v2', '2026-09-26T00:00:00.000Z', ?)`, cid, noFrames)
		return err
	})
	impl, review, ok := resolveSpecBudgets(noFrames, f.agent)
	if !ok {
		t.Fatal("the size default cannot be resolved")
	}
	pv, err := f.s.GetPlanView(context.Background(), id, f.agent)
	if err != nil || len(pv.Tasks) != 1 {
		t.Fatalf("GetPlanView: %v tasks=%d", err, len(pv.Tasks))
	}
	tv := pv.Tasks[0]
	if tv.BudgetImplUSD == nil || tv.BudgetReviewUSD == nil || *tv.BudgetImplUSD != impl || *tv.BudgetReviewUSD != review {
		t.Errorf("task frame = %v/%v, want the size default %v/%v", tv.BudgetImplUSD, tv.BudgetReviewUSD, impl, review)
	}
}

// タスクが 1 個の計画の承認の要約（承認の前）は、枠を片方だけ書いた計画でも、承認でタスクへ記録される枠（fillSoleTaskBudgets）と同じ額を出す
// （書いた枠はそのまま、省いた枠だけ既定で埋める）。
func TestPlanView_SoleTaskShowsTheFramesRecordedAtApproval(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		impl, review *float64
	}{
		{"only the impl frame is written", ptrFloat(7), nil},
		{"only the review frame is written", nil, ptrFloat(2.5)},
		{"neither frame is written", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newContractFixture(t)
			id := f.newInProgress(t, "two", "P2", twoTaskSpec(3, 2, 4, 5))
			cid, _ := parseChallengeID(id)
			spec := planSpec(func(m map[string]any) {
				tasks := j2TasksOf("a")
				tm := tasks[0].(map[string]any)
				tm["repo"], tm["operation"] = "main-repo", "impl"
				if c.impl != nil {
					tm["budget_impl_usd"] = *c.impl
				}
				if c.review != nil {
					tm["budget_review_usd"] = *c.review
				}
				m["tasks"] = tasks
			})
			writeForTest(t, f.s, func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO task_plan (challenge_id, version, body, created_at, spec) VALUES (?, 2, 'v2', '2026-09-26T00:00:00.000Z', ?)`, cid, spec)
				return err
			})
			// 承認でタスクへ記録される枠。
			tasks := parsePlanSpec(&spec).Tasks
			readForTest(t, f.s, func(tx *sql.Tx) error {
				return fillSoleTaskBudgets(context.Background(), tx, cid, 2, tasks, f.agent)
			})
			if tasks[0].BudgetImplMicros == nil || tasks[0].BudgetReviewMicros == nil {
				t.Fatalf("fillSoleTaskBudgets left a frame empty: %+v", tasks[0])
			}
			pv, err := f.s.GetPlanView(context.Background(), id, f.agent)
			if err != nil || len(pv.Tasks) != 1 {
				t.Fatalf("GetPlanView: %v tasks=%d", err, len(pv.Tasks))
			}
			tv := pv.Tasks[0]
			if tv.BudgetImplUSD == nil || tv.BudgetReviewUSD == nil {
				t.Fatalf("task frame = %v/%v, want both frames", tv.BudgetImplUSD, tv.BudgetReviewUSD)
			}
			if usdToMicros(*tv.BudgetImplUSD) != *tasks[0].BudgetImplMicros || usdToMicros(*tv.BudgetReviewUSD) != *tasks[0].BudgetReviewMicros {
				t.Errorf("view frame = %v/%v USD, recorded at approval = %d/%d micros", *tv.BudgetImplUSD, *tv.BudgetReviewUSD, *tasks[0].BudgetImplMicros, *tasks[0].BudgetReviewMicros)
			}
			// 承認の後は、タスクの記録の値（`flywheel budget --task` で置き換えた額を含む）を出す。
			seedPlanTasks(t, f.s, cid, 2)
			wantImpl, wantReview := *tasks[0].BudgetImplMicros+usdToMicros(1), *tasks[0].BudgetReviewMicros+usdToMicros(1)
			writeForTest(t, f.s, func(tx *sql.Tx) error {
				_, err := tx.Exec(`UPDATE task SET budget_impl_micros = ?, budget_review_micros = ? WHERE challenge_id = ? AND plan_version = 2`, wantImpl, wantReview, cid)
				return err
			})
			pv, err = f.s.GetPlanView(context.Background(), id, f.agent)
			if err != nil || len(pv.Tasks) != 1 || pv.Tasks[0].BudgetImplUSD == nil || pv.Tasks[0].BudgetReviewUSD == nil {
				t.Fatalf("GetPlanView after approval: %v %+v", err, pv.Tasks)
			}
			if usdToMicros(*pv.Tasks[0].BudgetImplUSD) != wantImpl || usdToMicros(*pv.Tasks[0].BudgetReviewUSD) != wantReview {
				t.Errorf("view frame after approval = %v/%v USD, recorded = %d/%d micros", *pv.Tasks[0].BudgetImplUSD, *pv.Tasks[0].BudgetReviewUSD, wantImpl, wantReview)
			}
		})
	}
}

func ptrFloat(v float64) *float64 { return &v }
