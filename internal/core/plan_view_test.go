package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// このファイルは、課題の最新の計画の読み取り（GetPlanView）と、課題の全体像の tasks・統合先を使うか
// （masanami/flywheel#755）を、実ストアで確かめる。端末・API の形は internal/cli・internal/server のテストが持つ。

// 承認の前は、タスクの ID が無く、前提は計画の中の key。問いは通し番号つきで、答えの無い問いの answer は nil。
// 予算の上限は、タスクが全部枠を持つ計画では、タスクの枠の合計（3 個 × 実装 3・レビュー対応 2 = 15）。計画の値（実装の枠 41.5・
// サイズの既定）は読まない。宣言が無くても決められる。
func TestGetPlanView_BeforeApprovalUsesKeysAndHasNoTaskIDs(t *testing.T) {
	f := newPAFixture(t, 3, true)
	agent := &AgentDeclaration{SizeBudgetsUSD: SizeBudgetsUSD{S: SizeBudgetPair{30, 25}, M: SizeBudgetPair{50, 30}, L: SizeBudgetPair{100, 40}}}
	pv, err := f.s.GetPlanView(context.Background(), f.id, agent)
	if err != nil {
		t.Fatal(err)
	}
	if !pv.Structured || pv.Version != 1 || pv.Status != StatusAwaitingPlanApproval {
		t.Fatalf("plan view = %+v", pv)
	}
	if pv.BudgetUSD == nil || *pv.BudgetUSD != 15 {
		t.Errorf("BudgetUSD = %v, want 15 (the sum of the task frames, not the plan's 41.5 + size default)", pv.BudgetUSD)
	}
	noDecl, err := f.s.GetPlanView(context.Background(), f.id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if noDecl.BudgetUSD == nil || *noDecl.BudgetUSD != 15 {
		t.Errorf("BudgetUSD without a declaration = %v, want 15 (task frames need no size default)", noDecl.BudgetUSD)
	}
	if pv.UsesIntegration == nil || !*pv.UsesIntegration {
		t.Errorf("UsesIntegration = %v, want true", pv.UsesIntegration)
	}
	if len(pv.Topics) != 1 || len(pv.Topics[0].Questions) != 2 || pv.Topics[0].Questions[1].Number != 2 || pv.Topics[0].Questions[0].Answer != nil {
		t.Fatalf("topics = %+v", pv.Topics)
	}
	if len(pv.Tasks) != 3 {
		t.Fatalf("tasks = %+v", pv.Tasks)
	}
	for _, tk := range pv.Tasks {
		if tk.ID != "" || tk.Status != "" || tk.Identifier() != tk.Key {
			t.Errorf("task = %+v, want no ID before approval", tk)
		}
	}
	if got := pv.Tasks[1].DependsOn; len(got) != 1 || got[0] != pv.Tasks[0].Key {
		t.Errorf("tasks[1].DependsOn = %v, want [%s]", got, pv.Tasks[0].Key)
	}
}

// 承認の後は、同じ計画の版のタスクの行があるので、ID・状態・前提のタスクの ID で出る。課題の全体像の tasks も同じ行。
func TestGetPlanView_AfterApprovalUsesTaskIDs(t *testing.T) {
	f := newPAFixture(t, 3, true)
	if err := f.approve(t, ttyAtt(t, f.id)); err != nil {
		t.Fatal(err)
	}
	pv, err := f.s.GetPlanView(context.Background(), f.id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.Tasks) != 3 || pv.Tasks[0].ID != "T-1" || pv.Tasks[1].ID != "T-2" || pv.Tasks[1].Status != TaskStatusPending {
		t.Fatalf("tasks = %+v", pv.Tasks)
	}
	if got := pv.Tasks[2].DependsOn; len(got) != 1 || got[0] != "T-2" || pv.Tasks[2].Identifier() != "T-3" {
		t.Errorf("tasks[2] = %+v, want T-3 depending on T-2", pv.Tasks[2])
	}
	// 答えの無い問いは、承認が推奨の答えを記録する。
	if a := pv.Topics[0].Questions[0].Answer; a == nil || !a.Recommended || a.Choice == nil || *a.Choice != 2 || a.Label == nil {
		t.Errorf("Q1 answer = %+v, want the recommended choice 2 with its label", a)
	}

	d := f.detail(t, f.id)
	if len(d.Tasks) != 3 || d.Tasks[2].ID != "T-3" || len(d.Tasks[2].DependsOn) != 1 || d.Tasks[2].DependsOn[0] != "T-2" {
		t.Errorf("detail.Tasks = %+v", d.Tasks)
	}
	if d.UsesIntegration == nil || !*d.UsesIntegration {
		t.Errorf("detail.UsesIntegration = %v, want true", d.UsesIntegration)
	}
}

// タスクが 0 個の課題の全体像は、空のスライス（nil でない）と、未決の統合先（nil）。
func TestChallengeDetail_WithoutTasksHasAnEmptySliceAndNoIntegrationDecision(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "no tasks", "P1")
	d := f.detail(t, ch.ID)
	if d.Tasks == nil || len(d.Tasks) != 0 {
		t.Errorf("Tasks = %#v, want a non-nil empty slice", d.Tasks)
	}
	if d.UsesIntegration != nil {
		t.Errorf("UsesIntegration = %v, want nil", d.UsesIntegration)
	}
}

// 構造化した出力を持たない計画（人が `plan --file` で登録したもの）は、本文だけを持つ。計画が無い課題は ErrValidation、
// 課題が無ければ ErrNotFound。
func TestGetPlanView_LegacyPlanAndMissingPlan(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "legacy", "P1")
	if _, err := f.s.GetPlanView(context.Background(), ch.ID, nil); !errors.Is(err, ErrValidation) {
		t.Errorf("no plan: err = %v, want ErrValidation", err)
	}
	if _, err := f.s.GetPlanView(context.Background(), "C-9999", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("no challenge: err = %v, want ErrNotFound", err)
	}
	if _, _, err := f.s.PlanChallenge(context.Background(), ChannelCLI, ch.ID, PlanInput{Body: "LEGACY-BODY"}); err != nil {
		t.Fatal(err)
	}
	pv, err := f.s.GetPlanView(context.Background(), ch.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Structured || pv.Body != "LEGACY-BODY" || len(pv.Tasks) != 0 || len(pv.Topics) != 0 {
		t.Errorf("plan view = %+v, want the body only", pv)
	}
}

// AC-205: 最新の計画の版に `flywheel budget` の増額（task_plan の枠の上書き）があるとき、予算の上限は、増額を当てた額で出る。
// 実装の枠を 60 USD に置き換えると、レビュー対応の枠はサイズ M の既定（30）のままで、合計は 90。
func TestGetPlanView_BudgetAppliesTheOverrideOfThePlanVersion(t *testing.T) {
	f := newPAFixture(t, 1, false)
	cid, _ := parseChallengeID(f.id)
	if err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE task_plan SET impl_budget_usd = ? WHERE challenge_id = ? AND version = 1`, usdToMicros(60), cid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	agent := &AgentDeclaration{SizeBudgetsUSD: SizeBudgetsUSD{S: SizeBudgetPair{30, 25}, M: SizeBudgetPair{50, 30}, L: SizeBudgetPair{100, 40}}}
	pv, err := f.s.GetPlanView(context.Background(), f.id, agent)
	if err != nil {
		t.Fatal(err)
	}
	if pv.BudgetUSD == nil || *pv.BudgetUSD != 90 {
		t.Errorf("BudgetUSD = %v, want 90 (60 by the override + 30 from the size default)", pv.BudgetUSD)
	}
}
