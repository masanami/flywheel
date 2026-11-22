package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// J4（子の問いへの応答。docs/features/m3-invoker-delegation.md §J4）の検証。実ストア・偽の委譲の IF・偽の契約の口を使う。

func j4Out(decisions ...map[string]any) JudgmentLaunchOutput {
	b, _ := json.Marshal(map[string]any{"decisions": decisions})
	return JudgmentLaunchOutput{Result: RunResultSucceeded, StructuredOutput: b, ReportedTotalCostUSD: float64Ptr(0.5)}
}

func dec(i int, decision string, option, answer any) map[string]any {
	return map[string]any{"question_index": i, "decision": decision, "option": option, "answer": answer}
}

// minorQuestionsOut は、人へ上げる種類でない 2 つの問い（1: 選択肢あり・2: 選択肢なし）で止まる報告。
func minorQuestionsOut() JudgmentLaunchOutput {
	return JudgmentLaunchOutput{Result: RunResultSucceeded, ReportedTotalCostUSD: float64Ptr(1), StructuredOutput: reportJSON(func(m map[string]any) {
		m["outcome"] = "questions"
		m["branch"] = "feat/x"
		m["questions"] = []any{
			map[string]any{"kind": "minor", "text": "QUESTION-ONE", "options": []string{"A", "B"}, "recommendation": "A"},
			map[string]any{"kind": "requirements", "text": "QUESTION-TWO", "options": []string{}, "recommendation": nil},
		}
	})}
}

func answerAll() JudgmentLaunchOutput {
	return j4Out(dec(1, "answer", "B", nil), dec(2, "answer", nil, "ANSWER-TWO"))
}

func (f *delegateFixture) countRows(t *testing.T, q string) int {
	t.Helper()
	n := 0
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(q).Scan(&n) }); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestValidateJ4Output(t *testing.T) {
	t.Parallel()
	qs := []j4Question{{Kind: "minor", Text: "q1", Options: []string{"A", "B"}}, {Text: "q2"}}
	ok := func(s string) bool { _, v := validateJ4Output([]byte(s), qs); return v }
	if !ok(`{"decisions":[{"question_index":1,"decision":"answer","option":"A","answer":null},{"question_index":2,"decision":"answer","option":null,"answer":"x"}]}`) {
		t.Error("a valid all-answer output must pass")
	}
	if !ok(`{"decisions":[{"question_index":2,"decision":"escalate","option":null,"answer":null},{"question_index":1,"decision":"escalate","option":null,"answer":"reason"}]}`) {
		t.Error("escalate needs no option or answer, and the order is free")
	}
	bad := map[string]string{
		"option not among the options": `{"decisions":[{"question_index":1,"decision":"answer","option":"C","answer":null},{"question_index":2,"decision":"escalate","option":null,"answer":null}]}`,
		"option missing":               `{"decisions":[{"question_index":1,"decision":"answer","option":null,"answer":"A"},{"question_index":2,"decision":"escalate","option":null,"answer":null}]}`,
		"no options and blank answer":  `{"decisions":[{"question_index":1,"decision":"escalate","option":null,"answer":null},{"question_index":2,"decision":"answer","option":null,"answer":"  "}]}`,
		"duplicate index":              `{"decisions":[{"question_index":1,"decision":"escalate","option":null,"answer":null},{"question_index":1,"decision":"escalate","option":null,"answer":null}]}`,
		"wrong count":                  `{"decisions":[{"question_index":1,"decision":"escalate","option":null,"answer":null}]}`,
		"index out of range":           `{"decisions":[{"question_index":1,"decision":"escalate","option":null,"answer":null},{"question_index":3,"decision":"escalate","option":null,"answer":null}]}`,
		"unknown decision":             `{"decisions":[{"question_index":1,"decision":"maybe","option":null,"answer":null},{"question_index":2,"decision":"escalate","option":null,"answer":null}]}`,
		"not json":                     `x`,
	}
	for name, s := range bad {
		if ok(s) {
			t.Errorf("%s: must be rejected", name)
		}
	}
}

func TestJ4Schema_EnumComesFromTheDecisionValues(t *testing.T) {
	t.Parallel()
	var m map[string]any
	if err := json.Unmarshal(j4OutputSchema(), &m); err != nil {
		t.Fatal(err)
	}
	enum := m["properties"].(map[string]any)["decisions"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["decision"].(map[string]any)["enum"]
	if !reflect.DeepEqual(enum, []any{"answer", "escalate"}) {
		t.Errorf("enum = %v", enum)
	}
}

// 同じ周で再開できなくても、回答は run に残り、次の起動が同じ回答を渡して再開する（新しいセッションで始めない）。
func TestJ4_PendingAnswerIsResumedByTheNextLaunchDecision(t *testing.T) {
	t.Parallel()
	pv := int64(1)
	deleg := runRow{ID: "R-1", Kind: runKindDelegate, SessionID: "sess", Result: RunResultSucceeded, PlanVersion: &pv,
		Output: string(minorQuestionsOut().StructuredOutput)}
	j4 := runRow{ID: "R-2", Kind: runKindJudgment, Judgment: judgmentJ4, Result: RunResultSucceeded, Output: string(answerAll().StructuredOutput)}
	h := &launchHistory{Runs: []runRow{deleg}, J4Runs: []runRow{j4}}
	resume, limit := h.decideLaunch(1, 2)
	if limit != nil || resume == nil || resume.Kind != ResumeKindAnswer || resume.Target.ID != "R-1" ||
		!strings.Contains(resume.Answer, "ANSWER-TWO") || !strings.Contains(resume.Question, "QUESTION-ONE") {
		t.Fatalf("resume = %+v limit = %+v", resume, limit)
	}
	// 回答を渡す再開が済んだ（後の委譲の run がある）なら、同じ回答を渡し直さない。
	later := runRow{ID: "R-3", Kind: runKindDelegate, SessionID: "sess", Result: RunResultSucceeded, PlanVersion: &pv, Output: `{"outcome":"completed"}`}
	h.Runs = append(h.Runs, later)
	if resume, _ := h.decideLaunch(1, 2); resume != nil {
		t.Errorf("resume = %+v, want none after the answered resume ran", resume)
	}
	// 人へ上げた J4（全問 answer でない）は再開に使わない。
	h = &launchHistory{Runs: []runRow{deleg}, J4Runs: []runRow{{ID: "R-2", Kind: runKindJudgment, Judgment: judgmentJ4, Result: RunResultSucceeded,
		Output: string(j4Out(dec(1, "answer", "A", nil), dec(2, "escalate", nil, nil)).StructuredOutput)}}}
	if resume, _ := h.decideLaunch(1, 2); resume != nil {
		t.Errorf("resume = %+v, want none for an escalating J4", resume)
	}
}

func TestJ4_DeclaredHumanKindIsNeverGivenToJ4(t *testing.T) {
	t.Parallel()
	qs := []DelegationQuestion{{Kind: "minor", Text: "x"}, {Kind: "my_kind", Text: "y"}}
	if !j4RuleEscalates(qs, []HumanQuestionKind{{ID: "my_kind", Label: "L"}}) {
		t.Error("a declared human question kind must escalate")
	}
	if j4RuleEscalates(qs, nil) {
		t.Error("undeclared minor kinds must not escalate by rule")
	}
}
