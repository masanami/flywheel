package core

// スーパーバイザーの出力の answers の要素を null（この問いには答えない）にできる型と、委譲先の問いでは null を不備にすることを確かめる
// （区切り S2b-2。止まり方 spec_question の経路そのものは、このファイルの範囲外）。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// svAnswerOutput は、手 hand と、JSON のままの answers を持つ出力を作る。
func svAnswerOutput(hand, answers string) string {
	return `{"hand":"` + hand + `","findings":"f","note":"","basis":[],"recommendation":"r","options":[],"conditions":[],"answers":` + answers + `}`
}

const answeredElementJSON = `{"answer":"x","basis":[{"path":"docs/a.md","locator":"節"}]}`

// AC-7: 出力の読み取りは、手が answer で answers に null を含む出力を、形の誤りにしない。要素の位置はそのまま残る。
func TestParseSVOutput_AC7_AnswerWithNullElementIsNotAShapeError(t *testing.T) {
	t.Parallel()
	offered := SupervisorHandsFor(StopKindSpecQuestion)
	out, err := ParseSVOutput([]byte(svAnswerOutput("answer", `[null,`+answeredElementJSON+`,null]`)), offered)
	if err != nil {
		t.Fatalf("ParseSVOutput = %v, want accepted", err)
	}
	if len(out.Answers) != 3 || out.Answers[0] != nil || out.Answers[1] == nil || out.Answers[1].Answer != "x" || out.Answers[2] != nil {
		t.Errorf("answers = %+v, want [nil, x, nil]", out.Answers)
	}
}

// 手ごとの決まり: answer は null でない要素が 1 件以上。null でない要素の空の答えは不備。escalate の answers は空。
func TestParseSVOutput_AnswerRulesWithNullElements(t *testing.T) {
	t.Parallel()
	offered := SupervisorHandsFor(StopKindSpecQuestion)
	for name, raw := range map[string]string{
		"answer の全部が null":          svAnswerOutput("answer", `[null,null]`),
		"answer の null と空の答え":       svAnswerOutput("answer", `[null,{"answer":" ","basis":[]}]`),
		"escalate の answers が null": svAnswerOutput("escalate", `[null]`),
	} {
		if _, err := ParseSVOutput([]byte(raw), offered); !errors.Is(err, ErrSVOutputInvalid) {
			t.Errorf("%s: err = %v, want ErrSVOutputInvalid", name, err)
		}
	}
	if _, err := ParseSVOutput([]byte(svAnswerOutput("escalate", `[]`)), offered); err != nil {
		t.Errorf("escalate with empty answers = %v, want accepted", err)
	}
}

// AC-8: 出力スキーマの answers の要素の型は、オブジェクトと null の 2 つ。要素がオブジェクトのときの必須は変えない。
func TestSVOutputSchema_AC8_AnswerElementTypeIsObjectOrNull(t *testing.T) {
	t.Parallel()
	var schema struct {
		Properties struct {
			Answers struct {
				Items struct {
					Type     []string `json:"type"`
					Required []string `json:"required"`
				} `json:"items"`
			} `json:"answers"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(SVOutputSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	items := schema.Properties.Answers.Items
	if got := strings.Join(items.Type, ","); got != "object,null" {
		t.Errorf("answers items type = %q, want object,null", got)
	}
	if got := strings.Join(items.Required, ","); got != "answer,basis" {
		t.Errorf("answers items required = %q, want answer,basis", got)
	}
}

// AC-9・AC-10: 委譲先の問いで null を 1 件含む手 answer の出力は不備になり、保留が作られ、判断点の実行の結果は invalid_output になる。
func TestChildReport_AC9_AC10_NullAnswerIsFlawedAndRaisesAHold(t *testing.T) {
	t.Parallel()
	for name, answers := range map[string]string{
		"後ろの問いが null": `[{"answer":"B","basis":[{"path":"docs/a.md","locator":"節"}]},null]`,
		"前の問いが null":  `[null,{"answer":"ANSWER-TWO","basis":[{"path":"docs/a.md","locator":"節"}]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := childReportFixture(t)
			id := f.newInProgress(t, "t", "P1", planSpec(nil))
			f.deleg.result = minorQuestionsOut()
			r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(svAnswerOutput("answer", answers))}}
			f.runWithSV(t, r, id)

			if n := f.countRows(t, `SELECT COUNT(*) FROM hold WHERE stop_kind = 'child_question' AND question LIKE '%QUESTION-ONE%' AND question LIKE '%QUESTION-TWO%'`); n != 1 { // AC-9
				t.Errorf("holds with both questions = %d, want 1", n)
			}
			if got := f.queryString(t, `SELECT result FROM run WHERE judgment = 'SV'`); got != string(RunResultInvalidOutput) { // AC-10
				t.Errorf("SV run result = %q, want %q", got, RunResultInvalidOutput)
			}
			if n := f.countRows(t, `SELECT COUNT(*) FROM supervisor_action WHERE result = 'failed' AND stop_kind = 'child_question'`); n != 1 {
				t.Errorf("failed actions = %d, want 1", n)
			}
			if n := f.countRows(t, `SELECT COUNT(*) FROM supervisor_action WHERE result = 'executed'`); n != 0 {
				t.Errorf("executed actions = %d, want 0 (a partial answer is not recorded)", n)
			}
			if got := len(f.deleg.launched()); got != 1 {
				t.Errorf("launches = %d, want 1 (the child is not resumed)", got)
			}
		})
	}
}

// AC-12: 全部の問いに答えた出力は、保留を作らず、答えとして記録される（判断点の実行は成功のまま）。
func TestChildReport_AC12_AllAnsweredIsRecordedWithoutAHold(t *testing.T) {
	t.Parallel()
	f := childReportFixture(t)
	id := f.newInProgress(t, "t", "P1", planSpec(nil))
	f.deleg.queue = []JudgmentLaunchOutput{minorQuestionsOut(), {Result: RunResultSucceeded, StructuredOutput: reportJSON(nil), ReportedTotalCostUSD: float64Ptr(1)}}
	r := &svRouter{svOut: []JudgmentLaunchOutput{svJSON(answerJSON("B|docs/a.md", "ANSWER-TWO|docs/a.md"))}}
	f.runWithSV(t, r, id)

	if n := f.countRows(t, `SELECT COUNT(*) FROM hold WHERE stop_kind = 'child_question'`); n != 0 {
		t.Errorf("child_question holds = %d, want 0", n)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM supervisor_action WHERE hand = 'answer' AND result = 'executed' AND stop_kind = 'child_question' AND questions LIKE '%"B"%' AND questions LIKE '%ANSWER-TWO%'`); n != 1 {
		t.Errorf("answer actions = %d, want 1", n)
	}
	if got := f.queryString(t, `SELECT result FROM run WHERE judgment = 'SV'`); got != string(RunResultSucceeded) {
		t.Errorf("SV run result = %q, want %q", got, RunResultSucceeded)
	}
}

// 過去の実行の記録の読み取り: null を含む answers は「全部に答えた」と読まない（答えを渡す再開の元にしない）。
func TestJ4StoredAllAnswered_NullElementIsNotAllAnswered(t *testing.T) {
	t.Parallel()
	for _, answers := range []string{`[` + answeredElementJSON + `,null]`, `[null,` + answeredElementJSON + `]`, `[null]`} {
		if ds, ok := j4StoredAllAnswered(svAnswerOutput("answer", answers)); ok {
			t.Errorf("answers %s: decisions = %+v, want not all answered", answers, ds)
		}
	}
	ds, ok := j4StoredAllAnswered(svAnswerOutput("answer", `[`+answeredElementJSON+`]`))
	if !ok || len(ds) != 1 || ds[0].Answer == nil || *ds[0].Answer != "x" {
		t.Errorf("all answered = %+v, %v", ds, ok)
	}
}

// 答えの記録の口は、null を含む出力を記録しない（委譲先の問いは、全部に答えた出力だけを記録する）。
func TestRecordSupervisorAnswer_RejectsANullElement(t *testing.T) {
	t.Parallel()
	c := newLimitCase(t)
	req := supervisorHandRequest{CycleID: c.cycID, Target: supervisorTarget{ChallengeID: 1, StopKind: StopKindChildQuestion},
		Output: SVOutput{Hand: HandAnswer, Findings: "f", Answers: []*SVAnswer{{Answer: "x"}, nil}}}
	if _, _, err := c.s.recordSupervisorAnswer(context.Background(), req, []ChildQuestion{{Question: "q1"}, {Question: "q2"}}); !errors.Is(err, ErrValidation) {
		t.Errorf("err = %v, want ErrValidation", err)
	}
}
