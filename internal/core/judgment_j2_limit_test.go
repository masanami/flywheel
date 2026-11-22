package core

import (
	"context"
	"database/sql"
	"testing"
)

// 区切り S4c-1 の 2: 計画の上限（budget_limit_usd・budget_limit_reason）と、続きを決めるタスクの印（expand_after）。
// 出力の検査・計画の承認での写し・課題の詳細の予算を、実ストアで確かめる。

// limitPlan は 2 個のタスク（枠は 3+2 USD ずつ。見込みは 10 USD）の統合先を使う計画の出力（mut で上限などを足す）。
func limitPlan(mut func(m map[string]any)) []byte {
	return j2PlanOutput(func(m map[string]any) {
		m["tasks"] = j2TasksOf("a", "b")
		m["uses_integration"] = true
		if mut != nil {
			mut(m)
		}
	})
}

func TestValidateJ2Output_BudgetLimit(t *testing.T) {
	t.Parallel()
	one := func(m map[string]any) { m["tasks"] = []any{j2TestTask("a", "flywheel", "impl")} }
	pair, _ := defaultAgentDeclaration().SizeBudgetFor(JudgmentSizeM)
	sole := pair.Impl + pair.Review // 枠を書かない 1 個のタスクの計画の見込みは、計画の額（サイズの既定）
	kept := []taskBudget{{TaskID: 9, PlanVersion: 1, Status: TaskStatusDone, Impl: usdToMicros(4), Review: usdToMicros(3), Known: true}}
	cases := []struct {
		name  string
		mut   func(m map[string]any)
		kept  []taskBudget
		valid bool
	}{
		{"上限を省く", nil, nil, true},
		{"上限が見込みと同額で理由が無い", func(m map[string]any) { m["budget_limit_usd"] = 10.0 }, nil, true},
		{"上限が見込みより小さい", func(m map[string]any) { m["budget_limit_usd"] = 9.99; m["budget_limit_reason"] = "理由" }, nil, false},
		{"上限が見込みより大きく理由が無い", func(m map[string]any) { m["budget_limit_usd"] = 10.01 }, nil, false},
		{"上限が見込みより大きく理由が null", func(m map[string]any) { m["budget_limit_usd"] = 11.0; m["budget_limit_reason"] = nil }, nil, false},
		{"上限が見込みより大きく理由が空白だけ", func(m map[string]any) { m["budget_limit_usd"] = 11.0; m["budget_limit_reason"] = " \n" }, nil, false},
		{"上限が見込みより大きく理由がある", func(m map[string]any) { m["budget_limit_usd"] = 11.0; m["budget_limit_reason"] = "続きを足す分" }, nil, true},
		{"上限が 0", func(m map[string]any) { m["budget_limit_usd"] = 0.0; m["budget_limit_reason"] = "理由" }, nil, false},
		{"上限が負", func(m map[string]any) { m["budget_limit_usd"] = -1.0; m["budget_limit_reason"] = "理由" }, nil, false},
		{"作り直し: 新しい版のタスクの枠の合計以上でも、残る済みのタスクを含めた見込みより小さい", func(m map[string]any) { m["budget_limit_usd"] = 16.0; m["budget_limit_reason"] = "理由" }, kept, false},
		{"作り直し: 残る済みのタスクを含めた見込みと同額", func(m map[string]any) { m["budget_limit_usd"] = 17.0 }, kept, true},
		{"作り直し: 見込みより大きく理由がある", func(m map[string]any) { m["budget_limit_usd"] = 18.0; m["budget_limit_reason"] = "理由" }, kept, true},
		{"取りやめのタスクの枠は見込みに入らない", func(m map[string]any) { m["budget_limit_usd"] = 10.0 }, []taskBudget{{TaskID: 9, PlanVersion: 1, Status: TaskStatusCancelled, Impl: usdToMicros(40), Review: usdToMicros(40), Known: true}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vc := j2TestContext(t, true)
			vc.KeptBudgets, vc.CheckLimit = c.kept, true
			v, ok := validateJ2Output(limitPlan(c.mut), vc)
			if ok != c.valid {
				t.Fatalf("valid = %v, want %v", ok, c.valid)
			}
			if ok && c.mut == nil && v.BudgetLimitUSD != nil {
				t.Errorf("BudgetLimitUSD = %v, want nil when omitted", *v.BudgetLimitUSD)
			}
		})
	}
	t.Run("枠を書かない 1 個のタスクは、計画の額（サイズの既定）が見込み", func(t *testing.T) {
		mk := func(limit float64) []byte {
			return j2PlanOutput(func(m map[string]any) { one(m); m["budget_limit_usd"] = limit; m["budget_limit_reason"] = "理由" })
		}
		if _, ok := validateJ2Output(mk(sole-0.01), limitCtx(t)); ok {
			t.Error("a limit below the estimate must be invalid")
		}
		if _, ok := validateJ2Output(mk(sole), limitCtx(t)); !ok {
			t.Error("a limit equal to the estimate must be valid")
		}
	})
	t.Run("検査後の値は上限と、空白を除いた理由", func(t *testing.T) {
		v, ok := validateJ2Output(limitPlan(func(m map[string]any) { m["budget_limit_usd"] = 12.5; m["budget_limit_reason"] = "  理由  " }), j2TestContext(t, true))
		if !ok || v.BudgetLimitUSD == nil || *v.BudgetLimitUSD != 12.5 || v.BudgetLimitReason == nil || *v.BudgetLimitReason != "理由" {
			t.Fatalf("ok=%v v=%+v", ok, v)
		}
	})
}

func TestJ2OutputSchema_HasBudgetLimitAndExpandAfter(t *testing.T) {
	t.Parallel()
	root := j2SchemaRoot(t)
	for _, k := range []string{"budget_limit_usd", "budget_limit_reason"} {
		if _, ok := root[k]; !ok {
			t.Errorf("schema lacks %q", k)
		}
	}
	items := root["tasks"].(map[string]any)["items"].(map[string]any)
	if _, ok := items["properties"].(map[string]any)["expand_after"]; !ok {
		t.Error("task items lack expand_after")
	}
	for _, r := range items["required"].([]any) {
		if r.(string) == "expand_after" {
			t.Error("expand_after must be optional")
		}
	}
}

func TestValidateJ2Output_ExpandAfter(t *testing.T) {
	t.Parallel()
	with := func(integ bool, n int, v any) []byte {
		return j2PlanOutput(func(m map[string]any) {
			tasks := j2TasksOf("a", "b", "c")[:n]
			tasks[0].(map[string]any)["expand_after"] = v
			m["tasks"] = tasks
			m["uses_integration"] = integ || n > 1
		})
	}
	if _, ok := validateJ2Output(with(false, 1, true), j2TestContext(t, true)); ok {
		t.Error("expand_after = true in a plan that does not use the integration branch must be invalid")
	}
	for _, v := range []any{false, nil} {
		if _, ok := validateJ2Output(with(false, 1, v), j2TestContext(t, true)); !ok {
			t.Errorf("expand_after = %v without the integration branch must be valid", v)
		}
	}
	got, ok := validateJ2Output(with(true, 2, true), j2TestContext(t, true))
	if !ok || !got.Tasks[0].ExpandAfter || got.Tasks[1].ExpandAfter {
		t.Fatalf("ok=%v tasks=%+v, want expand_after carried on the first task only", ok, got)
	}
}

// --- 計画の承認 ---

func newLimitFixture(t *testing.T, mut func(m map[string]any)) *paFixture {
	t.Helper()
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "limit", "P1")
	out := j2OutputFor(false, func(m map[string]any) {
		m["size"] = "M"
		tasks := j2TasksOf("a", "b")
		for _, tk := range tasks {
			tm := tk.(map[string]any)
			tm["repo"], tm["operation"] = "harness-repo", "brief"
		}
		tasks[0].(map[string]any)["expand_after"] = true
		m["tasks"] = tasks
		m["uses_integration"] = true
		if mut != nil {
			mut(m)
		}
	})
	res, err := f.run(t, succeededInvoker(out), idPtr(ch.ID))
	if err != nil || len(res.Items) != 1 || res.Items[0].Outcome != "plan" {
		t.Fatalf("plan: %v %+v", err, res)
	}
	return &paFixture{j2Fixture: f, id: ch.ID}
}

func (f *paFixture) limitColumns(t *testing.T) (limit sql.NullInt64, reason sql.NullString) {
	t.Helper()
	cid, _ := parseChallengeID(f.id)
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT budget_limit_micros, budget_limit_reason FROM challenge WHERE id = ?`, cid).Scan(&limit, &reason)
	})
	return limit, reason
}

func TestPlanApproval_RecordsTheLimitAndExpandAfter(t *testing.T) {
	t.Parallel()
	f := newLimitFixture(t, func(m map[string]any) { m["budget_limit_usd"] = 14.0; m["budget_limit_reason"] = "続きを足す分" })

	// 承認の前は、上限の記録は NULL のまま。課題の詳細は、承認を待っている版の見込みと上限・理由を返す。
	if l, r := f.limitColumns(t); l.Valid || r.Valid {
		t.Fatalf("before approval: limit=%+v reason=%+v, want NULL", l, r)
	}
	before := f.detail(t, f.id).Budget
	if before.EstimateUSD == nil || *before.EstimateUSD != 10 || before.LimitUSD == nil || *before.LimitUSD != 14 || before.LimitReason == nil || *before.LimitReason != "続きを足す分" {
		t.Fatalf("budget before approval = %+v, want estimate 10 / limit 14 / the reason", before)
	}

	if err := f.approve(t, ttyAtt(t, f.id)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	l, r := f.limitColumns(t)
	if !l.Valid || l.Int64 != usdToMicros(14) || !r.Valid || r.String != "続きを足す分" {
		t.Errorf("after approval: limit=%+v reason=%+v, want 14 USD and the reason", l, r)
	}
	cid, _ := parseChallengeID(f.id)
	tasks := taskRows(t, f.s, cid)
	if len(tasks) != 2 || !tasks[0].ExpandAfter || tasks[1].ExpandAfter {
		t.Errorf("tasks = %+v, want expand_after on the first task only", tasks)
	}
	after := f.detail(t, f.id).Budget
	if after.EstimateUSD == nil || *after.EstimateUSD != *before.EstimateUSD || after.LimitUSD == nil || *after.LimitUSD != 14 || after.LimitReason == nil || *after.LimitReason != "続きを足す分" {
		t.Errorf("budget after approval = %+v, want the same estimate as before (%v) and the recorded limit", after, *before.EstimateUSD)
	}
}

func TestPlanApproval_OmittedLimitIsRecordedAsTheEstimate(t *testing.T) {
	t.Parallel()
	f := newLimitFixture(t, nil)
	before := f.detail(t, f.id).Budget
	if before.EstimateUSD == nil || before.LimitUSD == nil || *before.LimitUSD != *before.EstimateUSD || before.LimitReason != nil {
		t.Fatalf("budget before approval = %+v, want limit = estimate without a reason", before)
	}
	if err := f.approve(t, ttyAtt(t, f.id)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if l, r := f.limitColumns(t); !l.Valid || microsToUSD(l.Int64) != *before.EstimateUSD || r.Valid {
		t.Errorf("limit=%+v reason=%+v, want the estimate at approval without a reason", l, r)
	}
	after := f.detail(t, f.id).Budget
	if after.LimitUSD == nil || *after.LimitUSD != *before.EstimateUSD {
		t.Errorf("budget after approval = %+v, want limit = estimate", after)
	}
}

func TestPlanApproval_ReplacesThePreviousLimitAndSupersedesOpenAdditions(t *testing.T) {
	t.Parallel()
	f := newLimitFixture(t, nil) // 計画は上限を省く
	cid, _ := parseChallengeID(f.id)
	// 前の版の承認で記録した上限が残っている課題と、閉じていない足す依頼・閉じた足す依頼を用意する。
	writeForTest(t, f.s, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE challenge SET budget_limit_micros = 99000000, budget_limit_reason = '前の版の理由' WHERE id = ?`, cid); err != nil {
			return err
		}
		for _, st := range []string{"pending", "awaiting_owner", "applied", "none"} {
			if _, err := tx.Exec(`INSERT INTO task_addition (challenge_id, origin, state, created_at) VALUES (?, 'expand', ?, '2026-11-01T00:00:00.000Z')`, cid, st); err != nil {
				return err
			}
		}
		return nil
	})
	estimateBefore := f.detail(t, f.id).Budget.EstimateUSD
	if estimateBefore == nil {
		t.Fatal("estimate before approval is unknown")
	}
	if err := f.approve(t, ttyAtt(t, f.id)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if l, r := f.limitColumns(t); !l.Valid || microsToUSD(l.Int64) != *estimateBefore || r.Valid {
		t.Errorf("limit=%+v reason=%+v, want the previous values replaced by the estimate without a reason", l, r)
	}
	adds, err := f.s.ListTaskAdditions(context.Background(), f.id)
	if err != nil || len(adds) != 4 {
		t.Fatalf("ListTaskAdditions: %v %+v", err, adds)
	}
	want := []TaskAdditionState{TaskAdditionSuperseded, TaskAdditionSuperseded, TaskAdditionApplied, TaskAdditionNone}
	for i, a := range adds {
		if a.State != want[i] {
			t.Errorf("addition %d state = %s, want %s", i, a.State, want[i])
		}
		if (i < 2) != (a.DecidedAt != nil) && i < 2 {
			t.Errorf("addition %d: decided_at must be recorded when superseded", i)
		}
	}
}

func limitCtx(t *testing.T) j2ValidationContext {
	vc := j2TestContext(t, true)
	vc.CheckLimit = true
	return vc
}

// 承認済みの計画を委譲の段で読み直す検査（CheckLimit なし）は、見込みとの比べをしない（済みのタスクの枠を持たず、見込みは承認の後に動く）。
func TestValidateJ2Output_LimitIsNotComparedWithTheEstimateOutsideJ2(t *testing.T) {
	t.Parallel()
	out := limitPlan(func(m map[string]any) { m["budget_limit_usd"] = 17.0 }) // 済みのタスクを含めた見込みと同額で理由なし（J2 では通る）
	if _, ok := validateJ2Output(out, j2TestContext(t, true)); !ok {
		t.Error("the re-read at delegation must not reject a limit that J2 accepted")
	}
	bad := limitPlan(func(m map[string]any) { m["budget_limit_usd"] = -1.0 })
	if _, ok := validateJ2Output(bad, j2TestContext(t, true)); ok {
		t.Error("a malformed limit must still be invalid at delegation")
	}
}
