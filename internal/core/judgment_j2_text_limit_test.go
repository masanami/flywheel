package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// j2PlanWithTaskField は、唯一のタスクの欄 field を value にした plan の出力を返す。
func j2PlanWithTaskField(field, value string) []byte {
	return j2PlanOutput(func(m map[string]any) {
		task := j2TestTask("t1", "flywheel", "impl")
		task[field] = value
		m["tasks"] = []any{task}
	})
}

// flywheel#1210: タスクの本文の上限は 6,000 文字（ちょうどは通り、1 文字超えると不正）。文字は Unicode のコードポイントで数える。
func TestValidateJ2Output_TaskBodyLimitIs6000Runes(t *testing.T) {
	t.Parallel()
	vc := j2TestContext(t, true)
	if MaxTaskBodyRunes != 6000 {
		t.Fatalf("MaxTaskBodyRunes = %d, want 6000", MaxTaskBodyRunes)
	}
	if _, reason := validateJ2OutputReason(j2PlanWithTaskField("body", strings.Repeat("あ", 6000)), vc); reason != "" {
		t.Errorf("body of 6000 runes: want valid, got %q", reason)
	}
	_, reason := validateJ2OutputReason(j2PlanWithTaskField("body", strings.Repeat("あ", 6001)), vc)
	if reason != "tasks[0].body exceeds 6000 characters" {
		t.Errorf("body of 6001 runes: reason = %q, want the field and the limit only", reason)
	}
}

// flywheel#1210: 本文のほかの欄（鍵・題名・達成条件）の上限は 2,000 文字のまま。
func TestValidateJ2Output_OtherTaskTextLimitsStayAt2000Runes(t *testing.T) {
	t.Parallel()
	vc := j2TestContext(t, true)
	for _, field := range []string{"key", "title", "done_criteria"} {
		if _, reason := validateJ2OutputReason(j2PlanWithTaskField(field, strings.Repeat("あ", 2000)), vc); reason != "" {
			t.Errorf("%s of 2000 runes: want valid, got %q", field, reason)
		}
		_, reason := validateJ2OutputReason(j2PlanWithTaskField(field, strings.Repeat("あ", 2001)), vc)
		if reason != "tasks[0]."+field+" exceeds 2000 characters" {
			t.Errorf("%s of 2001 runes: reason = %q, want the field and the limit", field, reason)
		}
	}
}

// j2ExpandAddWithBody は、本文が body のタスク 1 件を足す「足す」の形の出力を返す。
func j2ExpandAddWithBody(t *testing.T, body string) []byte {
	t.Helper()
	task := j2TestTask("added", "flywheel", "impl")
	task["body"] = body
	task["budget_impl_usd"], task["budget_review_usd"] = 1.0, 1.0
	out, err := json.Marshal(map[string]any{"verdict": "add", "tasks": []any{task}})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// flywheel#1210: タスクを足す形の出力でも、タスクの本文の上限は 6,000 文字。
func TestValidateJ2ExpandOutput_TaskBodyLimitIs6000Runes(t *testing.T) {
	t.Parallel()
	vc := j2ExpandValidationContext{Agent: defaultAgentDeclaration(), Conn: j2TestConnectors(t), HasSource: true, Repos: []string{"flywheel"}}
	if _, ok := validateJ2ExpandOutput(j2ExpandAddWithBody(t, strings.Repeat("あ", 6000)), vc); !ok {
		t.Error("body of 6000 runes: want valid")
	}
	if _, reason := validateJ2ExpandOutputReason(j2ExpandAddWithBody(t, strings.Repeat("あ", 6001)), vc); reason != "tasks[0].body exceeds 6000 characters" {
		t.Errorf("body of 6001 runes: reason = %q, want the field and the limit only", reason)
	}
}

// j2SchemaTaskProps は、スキーマ schema の tasks の要素の properties を返す。
func j2SchemaTaskProps(t *testing.T, schema []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(schema, &decoded); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	tasks := decoded["properties"].(map[string]any)["tasks"].(map[string]any)
	return tasks["items"].(map[string]any)["properties"].(map[string]any)
}

// flywheel#1210: 型は、core の検査と同じ文字数の上限を持つ（型で通って検査で落ちる隙間を作らない）。
func TestJ2OutputSchema_TaskTextsCarryTheSameLimitsAsTheValidation(t *testing.T) {
	t.Parallel()
	want := map[string]float64{"key": 2000, "title": 2000, "body": 6000, "done_criteria": 2000}
	for name, schema := range map[string][]byte{
		"plan":            j2OutputSchema(),
		"expand":          j2ExpandOutputSchema(),
		"expand followup": j2ExpandOutputSchemaFor(TaskAdditionOriginFollowup),
	} {
		props := j2SchemaTaskProps(t, schema)
		for field, limit := range want {
			if got := props[field].(map[string]any)["maxLength"]; got != limit {
				t.Errorf("%s: tasks.%s maxLength = %v, want %v", name, field, got, limit)
			}
		}
	}
}

// flywheel#1210: 計画の出力が検査で不正になったとき、どの検査で落ちたかを、実行の記録の誤りの欄に残す。
func TestPlanAutoJ2_InvalidOutputRecordsWhichCheckFailed(t *testing.T) {
	t.Parallel()
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "target", "P1")
	// 取り込み元の無い課題で起動できる操作のタスクにし、本文の長さだけを変える（6,000 文字なら通ることを先に確かめる）。
	withBody := func(n int) []byte {
		return j2OutputFor(false, func(m map[string]any) {
			task := j2TestTask("t1", "harness-repo", "brief")
			task["body"] = strings.Repeat("あ", n)
			m["tasks"] = []any{task}
		})
	}
	okCh := f.newClassified(t, "at the limit", "P1")
	if res, err := f.run(t, succeededInvoker(withBody(6000)), idPtr(okCh.ID)); err != nil || res.Items[0].Result != RunResultSucceeded {
		t.Fatalf("body of 6000 runes: result = %+v, err = %v, want succeeded", res, err)
	}
	res, err := f.run(t, succeededInvoker(withBody(6001)), idPtr(ch.ID))
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].Result != RunResultInvalidOutput {
		t.Fatalf("result = %v, want invalid_output", res.Items[0].Result)
	}
	runs, err := f.s.ListRuns(context.Background(), RunListOptions{ChallengeID: &ch.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	// 理由は、判断点の名前と落ちた検査だけ（内部の前置きも、判断点の出力の中身も入らない）。
	if got, want := runs[0].Error, "invalid J2 output: tasks[0].body exceeds 6000 characters"; got != want {
		t.Errorf("run error = %q, want %q", got, want)
	}
}

// flywheel#1210: core が文字数を検査するほかの文字列（論点・問い・予想・やらないこと・上限の理由）も、型に同じ上限を持つ。
func TestJ2OutputSchema_OtherCheckedStringsCarryTheStageLimit(t *testing.T) {
	t.Parallel()
	root := j2SchemaRoot(t)
	at := func(path ...string) map[string]any {
		cur := root
		for _, k := range path {
			cur = cur[k].(map[string]any)
		}
		return cur
	}
	topic := []string{"topics", "items", "properties"}
	question := append(append([]string{}, topic...), "questions", "items", "properties")
	option := append(append([]string{}, question...), "options", "items", "properties")
	expected := []string{"expected", "items", "properties"}
	for name, node := range map[string]map[string]any{
		"topics.title":             at(append(append([]string{}, topic...), "title")...),
		"topics.decision":          at(append(append([]string{}, topic...), "decision")...),
		"question":                 at(append(append([]string{}, question...), "question")...),
		"question.options.label":   at(append(append([]string{}, option...), "label")...),
		"question.options.example": at(append(append([]string{}, option...), "example")...),
		"expected.scene":           at(append(append([]string{}, expected...), "scene")...),
		"expected.before":          at(append(append([]string{}, expected...), "before")...),
		"expected.after":           at(append(append([]string{}, expected...), "after")...),
		"non_goals":                at("non_goals", "items"),
		"tasks.non_goals":          at("tasks", "items", "properties", "non_goals", "items"),
		"budget_limit_reason":      at("budget_limit_reason"),
	} {
		if got := node["maxLength"]; got != float64(MaxStageRunes) {
			t.Errorf("%s maxLength = %v, want %d", name, got, MaxStageRunes)
		}
	}
	var expand map[string]any
	if err := json.Unmarshal(j2ExpandOutputSchema(), &expand); err != nil {
		t.Fatal(err)
	}
	if got := expand["properties"].(map[string]any)["question"].(map[string]any)["maxLength"]; got != float64(MaxStageRunes) {
		t.Errorf("expand question maxLength = %v, want %d", got, MaxStageRunes)
	}
}
