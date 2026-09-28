package core

import "testing"

// TestJ1VerdictValues_Are3ClosedValues は J1 の判定の閉集合が
// mine/not_mine/uncertain の3値であることを固定する
// （docs/features/m3-invoker-delegation.md §IF / API「判断点の出力」）。
func TestJ1VerdictValues_Are3ClosedValues(t *testing.T) {
	want := []J1Verdict{J1VerdictMine, J1VerdictNotMine, J1VerdictUncertain}
	if len(J1VerdictValues) != len(want) {
		t.Fatalf("len(J1VerdictValues) = %d, want %d", len(J1VerdictValues), len(want))
	}
	for i, v := range want {
		if J1VerdictValues[i] != v {
			t.Errorf("J1VerdictValues[%d] = %q, want %q", i, J1VerdictValues[i], v)
		}
	}
	if string(J1VerdictMine) != "mine" || string(J1VerdictNotMine) != "not_mine" || string(J1VerdictUncertain) != "uncertain" {
		t.Errorf("J1Verdict string values do not match the spec literally")
	}
}

// TestJ2VerdictValues_Are2ClosedValues は J2 の判定の閉集合が plan/uncertain の
// 2値であることを固定する。
func TestJ2VerdictValues_Are2ClosedValues(t *testing.T) {
	want := []J2Verdict{J2VerdictPlan, J2VerdictUncertain}
	if len(J2VerdictValues) != len(want) {
		t.Fatalf("len(J2VerdictValues) = %d, want %d", len(J2VerdictValues), len(want))
	}
	for i, v := range want {
		if J2VerdictValues[i] != v {
			t.Errorf("J2VerdictValues[%d] = %q, want %q", i, J2VerdictValues[i], v)
		}
	}
}

// TestJudgmentSizeValues_AreSML は J1・J2 のサイズ案の閉集合が S/M/L である
// ことを固定する。
func TestJudgmentSizeValues_AreSML(t *testing.T) {
	want := []JudgmentSize{JudgmentSizeS, JudgmentSizeM, JudgmentSizeL}
	if len(JudgmentSizeValues) != len(want) {
		t.Fatalf("len(JudgmentSizeValues) = %d, want %d", len(JudgmentSizeValues), len(want))
	}
	for i, v := range want {
		if JudgmentSizeValues[i] != v {
			t.Errorf("JudgmentSizeValues[%d] = %q, want %q", i, JudgmentSizeValues[i], v)
		}
	}
	if string(JudgmentSizeS) != "S" || string(JudgmentSizeM) != "M" || string(JudgmentSizeL) != "L" {
		t.Errorf("JudgmentSize string values do not match the spec literally")
	}
}

// TestJ4DecisionValues_AreAnswerEscalate は J4 の問いごとの判定の閉集合が
// answer/escalate であることを固定する。
func TestJ4DecisionValues_AreAnswerEscalate(t *testing.T) {
	want := []J4Decision{J4DecisionAnswer, J4DecisionEscalate}
	if len(J4DecisionValues) != len(want) {
		t.Fatalf("len(J4DecisionValues) = %d, want %d", len(J4DecisionValues), len(want))
	}
	for i, v := range want {
		if J4DecisionValues[i] != v {
			t.Errorf("J4DecisionValues[%d] = %q, want %q", i, J4DecisionValues[i], v)
		}
	}
}

// TestJ5VerdictValues_Are3ClosedValues は J5 の判定の閉集合が
// met/not_met/uncertain の3値であることを固定する。
func TestJ5VerdictValues_Are3ClosedValues(t *testing.T) {
	want := []J5Verdict{J5VerdictMet, J5VerdictNotMet, J5VerdictUncertain}
	if len(J5VerdictValues) != len(want) {
		t.Fatalf("len(J5VerdictValues) = %d, want %d", len(J5VerdictValues), len(want))
	}
	for i, v := range want {
		if J5VerdictValues[i] != v {
			t.Errorf("J5VerdictValues[%d] = %q, want %q", i, J5VerdictValues[i], v)
		}
	}
}

// TestDelegationOutcomeValues_Are3ClosedValues は委譲の報告の outcome の
// 閉集合が completed/questions/blocked であることを固定する。
func TestDelegationOutcomeValues_Are3ClosedValues(t *testing.T) {
	want := []DelegationOutcome{DelegationOutcomeCompleted, DelegationOutcomeQuestions, DelegationOutcomeBlocked}
	if len(DelegationOutcomeValues) != len(want) {
		t.Fatalf("len(DelegationOutcomeValues) = %d, want %d", len(DelegationOutcomeValues), len(want))
	}
	for i, v := range want {
		if DelegationOutcomeValues[i] != v {
			t.Errorf("DelegationOutcomeValues[%d] = %q, want %q", i, DelegationOutcomeValues[i], v)
		}
	}
}

// TestQuestionKindValues_Are8BuiltInValues は問いの種類の組み込みの閉集合が
// §IF / API・§J3 と逐語で一致する8値であることを固定する
// （宣言の human_question_kinds はこれとは別に動的な id を持つため、ここには
// 含めない）。
func TestQuestionKindValues_Are8BuiltInValues(t *testing.T) {
	want := []QuestionKind{
		QuestionKindRequirements,
		QuestionKindCriticalDesign,
		QuestionKindAcceptanceCriteria,
		QuestionKindSafetyTradeoff,
		QuestionKindCrossRepo,
		QuestionKindScope,
		QuestionKindProductPolicy,
		QuestionKindMinor,
	}
	if len(QuestionKindValues) != len(want) {
		t.Fatalf("len(QuestionKindValues) = %d, want %d", len(QuestionKindValues), len(want))
	}
	for i, v := range want {
		if QuestionKindValues[i] != v {
			t.Errorf("QuestionKindValues[%d] = %q, want %q", i, QuestionKindValues[i], v)
		}
	}
	wantLiterals := []string{
		"requirements", "critical_design", "acceptance_criteria", "safety_tradeoff",
		"cross_repo", "scope", "product_policy", "minor",
	}
	for i, w := range wantLiterals {
		if string(want[i]) != w {
			t.Errorf("QuestionKind literal[%d] = %q, want %q", i, want[i], w)
		}
	}
}

// TestAllJudgmentOutputClosedValues_ContainsEveryEnum は
// AllJudgmentOutputClosedValues が全ての閉集合の値を1つの一覧へ合成する
// ことを検証する（internal/invoker の禁止語生成の入力になる正本）。
func TestAllJudgmentOutputClosedValues_ContainsEveryEnum(t *testing.T) {
	got := AllJudgmentOutputClosedValues()
	mustContain := []string{
		"P0", "P1", "P2",
		"mine", "not_mine", "uncertain", "plan", "S", "M", "L",
		"answer", "escalate", "met", "not_met",
		"completed", "questions", "blocked",
		"requirements", "critical_design", "acceptance_criteria", "safety_tradeoff",
		"cross_repo", "scope", "product_policy", "minor",
	}
	set := map[string]bool{}
	for _, v := range got {
		set[v] = true
	}
	for _, w := range mustContain {
		if !set[w] {
			t.Errorf("AllJudgmentOutputClosedValues() is missing %q", w)
		}
	}
}
