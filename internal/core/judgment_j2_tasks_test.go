package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// このファイルは docs/features/core-redesign.md の受入基準「計画を作る判断点（J2）の出力」
// （AC-14〜AC-54）のうち core が持つものを確かめる。基準の番号は、その節の `- [ ]` の上からの序数
// （ストアの節が AC-1〜AC-13）。

// j2TestQuestion は検査を通る問い（選択肢 n 個、推奨は rec）。
func j2TestQuestion(text string, n, rec int) map[string]any {
	opts := make([]any, 0, n)
	for i := 1; i <= n; i++ {
		opts = append(opts, map[string]any{"label": fmt.Sprintf("%s-選択肢%d", text, i), "example": nil})
	}
	return map[string]any{"question": text, "options": opts, "recommended": rec}
}

func j2TestTopic(title string, questions ...any) map[string]any {
	if questions == nil {
		questions = []any{}
	}
	return map[string]any{"title": title, "decision": "決定 " + title, "questions": questions}
}

func j2TasksOf(keys ...string) []any {
	out := make([]any, 0, len(keys))
	for _, k := range keys {
		task := j2TestTask(k, "flywheel", "impl")
		// タスクが 2 個以上の計画は、全部のタスクが枠を持つ（持たない計画は不正）。
		if len(keys) >= 2 {
			task["budget_impl_usd"], task["budget_review_usd"] = 3.0, 2.0
		}
		out = append(out, task)
	}
	return out
}

// 値をタスクの k 番目（0 始まり）の項目へ書く。
func withTask(k int, field string, v any) func(m map[string]any) {
	return func(m map[string]any) {
		tasks := j2TasksOf("a", "b", "c")
		tasks[k].(map[string]any)[field] = v
		m["tasks"] = tasks[:k+1]
	}
}

// --- AC-14〜AC-19: 出力の型 ---

func j2SchemaRoot(t *testing.T) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(j2OutputSchema(), &decoded); err != nil {
		t.Fatalf("j2OutputSchema is not valid JSON: %v", err)
	}
	return decoded["properties"].(map[string]any)
}

// AC-14: verdict の値の集合は plan・uncertain・parent_direct の 3 つ。
func TestJ2OutputSchema_VerdictEnumIsPlanUncertainParentDirect(t *testing.T) {
	t.Parallel()
	enum := j2SchemaRoot(t)["verdict"].(map[string]any)["enum"].([]any)
	got := map[string]bool{}
	for _, v := range enum {
		got[v.(string)] = true
	}
	want := map[string]bool{"plan": true, "uncertain": true, "parent_direct": true}
	if len(got) != 3 || !got["plan"] || !got["uncertain"] || !got["parent_direct"] {
		t.Errorf("verdict enum = %v, want exactly %v", enum, want)
	}
}

// AC-15〜AC-19: 出力の型の根に no_issues_needed・steps・repo・operation・issues のキーが無い。
func TestJ2OutputSchema_RootHasNoRemovedKeys(t *testing.T) {
	t.Parallel()
	props := j2SchemaRoot(t)
	for _, key := range []string{"no_issues_needed", "steps", "repo", "operation", "issues"} {
		if _, ok := props[key]; ok {
			t.Errorf("the schema root still has %q", key)
		}
	}
	if strings.Contains(string(j2OutputSchema()), "no_issues_needed") {
		t.Error("the schema mentions no_issues_needed")
	}
	for _, key := range []string{"summary", "topics", "done_criteria", "expected", "non_goals", "figure", "depends_on",
		"tasks", "uses_integration", "size", "budget_impl_usd", "budget_review_usd", "cross_repo", "related_repos", "question"} {
		if _, ok := props[key]; !ok {
			t.Errorf("the schema root lacks %q", key)
		}
	}
	// タスクが repo・operation を持つ。
	items := props["tasks"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	for _, key := range []string{"key", "title", "body", "done_criteria", "non_goals", "depends_on", "repo", "operation"} {
		if _, ok := items[key]; !ok {
			t.Errorf("task items lack %q", key)
		}
	}
}

// --- AC-20〜AC-34, AC-35〜AC-39: 出力の検査 ---

func TestValidateJ2Output_TaskAndTopicRules(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ac        string
		name      string
		hasSource bool
		mut       func(m map[string]any)
		wantOK    bool
	}{
		{"AC-20", "uses_integration missing", true, func(m map[string]any) { delete(m, "uses_integration") }, false},
		{"AC-20", "uses_integration null", true, func(m map[string]any) { m["uses_integration"] = nil }, false},
		{"AC-21", "tasks empty array", true, func(m map[string]any) { m["tasks"] = []any{} }, false},
		{"AC-21", "tasks missing", true, func(m map[string]any) { m["tasks"] = nil }, false},
		{"AC-22", "13 tasks", true, func(m map[string]any) {
			keys := make([]string, 13)
			for i := range keys {
				keys[i] = fmt.Sprintf("k%d", i)
			}
			m["tasks"] = j2TasksOf(keys...)
			m["uses_integration"] = true
		}, false},
		{"AC-22 boundary", "12 tasks", true, func(m map[string]any) {
			keys := make([]string, 12)
			for i := range keys {
				keys[i] = fmt.Sprintf("k%d", i)
			}
			m["tasks"] = j2TasksOf(keys...)
			m["uses_integration"] = true
		}, true},
		{"AC-23", "duplicate key", true, func(m map[string]any) { m["tasks"] = j2TasksOf("a", "a"); m["uses_integration"] = true }, false},
		{"AC-23 key empty", "empty key", true, func(m map[string]any) { m["tasks"] = j2TasksOf(" ") }, false},
		{"AC-24", "task title empty", true, withTask(0, "title", ""), false},
		{"AC-24", "task title blank", true, withTask(0, "title", " \n"), false},
		{"AC-25", "task body empty", true, withTask(0, "body", ""), false},
		{"AC-26", "task done_criteria empty", true, withTask(0, "done_criteria", ""), false},
		{"AC-27", "depends_on names an unknown key", true, withTask(0, "depends_on", []string{"zzz"}), false},
		{"AC-28", "depends_on names itself", true, withTask(0, "depends_on", []string{"a"}), false},
		{"AC-29", "depends_on cycle", true, func(m map[string]any) {
			tasks := j2TasksOf("a", "b")
			tasks[0].(map[string]any)["depends_on"] = []string{"b"}
			tasks[1].(map[string]any)["depends_on"] = []string{"a"}
			m["tasks"] = tasks
			m["uses_integration"] = true
		}, false},
		{"AC-29 boundary", "depends_on chain without a cycle", true, func(m map[string]any) {
			tasks := j2TasksOf("a", "b")
			tasks[1].(map[string]any)["depends_on"] = []string{"a"}
			m["tasks"] = tasks
			m["uses_integration"] = true
		}, true},
		{"AC-30", "task repo not declared", true, withTask(0, "repo", "no-such-repo"), false},
		{"AC-30", "task repo empty", true, withTask(0, "repo", ""), false},
		{"AC-31", "task operation not in the repo's connector", true, withTask(0, "operation", "brief"), false},
		{"AC-31", "task operation empty", true, withTask(0, "operation", ""), false},
		{"AC-32", "{issue_number} operation without source", false, withTask(0, "operation", "impl"), false},
		{"AC-32", "{issue_url} operation without source", false, withTask(0, "operation", "by-url"), false},
		{"AC-32", "{external_key} operation without source", false, withTask(0, "operation", "by-key"), false},
		{"AC-32 boundary", "the same operations with a source", true, withTask(0, "operation", "by-key"), true},
		{"AC-32 second task", "a later task is checked too", false, func(m map[string]any) {
			tasks := j2TasksOf("a", "b")
			tasks[0].(map[string]any)["repo"], tasks[0].(map[string]any)["operation"] = "harness-repo", "brief"
			m["tasks"] = tasks
			m["uses_integration"] = true
		}, false},
		{"AC-33", "2 tasks and uses_integration false", true, func(m map[string]any) { m["tasks"] = j2TasksOf("a", "b"); m["uses_integration"] = false }, false},
		{"AC-33 boundary", "2 tasks and uses_integration true", true, func(m map[string]any) { m["tasks"] = j2TasksOf("a", "b"); m["uses_integration"] = true }, true},
		{"AC-34", "1 task and uses_integration false", true, func(m map[string]any) { m["uses_integration"] = false }, true},
		{"AC-34", "1 task and uses_integration true", true, func(m map[string]any) { m["uses_integration"] = true }, true},
		{"AC-35", "6 topics", true, func(m map[string]any) {
			m["topics"] = []any{j2TestTopic("1"), j2TestTopic("2"), j2TestTopic("3"), j2TestTopic("4"), j2TestTopic("5"), j2TestTopic("6")}
		}, false},
		{"AC-35 boundary", "5 topics", true, func(m map[string]any) {
			m["topics"] = []any{j2TestTopic("1"), j2TestTopic("2"), j2TestTopic("3"), j2TestTopic("4"), j2TestTopic("5")}
		}, true},
		{"AC-36", "4 questions in a topic", true, func(m map[string]any) {
			q := j2TestQuestion("q", 2, 1)
			m["topics"] = []any{j2TestTopic("t", q, q, q, q)}
		}, false},
		{"AC-36 boundary", "3 questions in a topic", true, func(m map[string]any) {
			q := j2TestQuestion("q", 2, 1)
			m["topics"] = []any{j2TestTopic("t", q, q, q)}
		}, true},
		{"AC-37", "a question with 1 option", true, func(m map[string]any) { m["topics"] = []any{j2TestTopic("t", j2TestQuestion("q", 1, 1))} }, false},
		{"AC-37 boundary", "a question with 2 options", true, func(m map[string]any) { m["topics"] = []any{j2TestTopic("t", j2TestQuestion("q", 2, 2))} }, true},
		{"AC-38", "a question with 6 options", true, func(m map[string]any) { m["topics"] = []any{j2TestTopic("t", j2TestQuestion("q", 6, 1))} }, false},
		{"AC-38 boundary", "a question with 5 options", true, func(m map[string]any) { m["topics"] = []any{j2TestTopic("t", j2TestQuestion("q", 5, 5))} }, true},
		{"AC-39", "recommended above the option count", true, func(m map[string]any) { m["topics"] = []any{j2TestTopic("t", j2TestQuestion("q", 3, 4))} }, false},
		{"AC-39", "recommended zero", true, func(m map[string]any) { m["topics"] = []any{j2TestTopic("t", j2TestQuestion("q", 3, 0))} }, false},
		{"AC-39 boundary", "recommended equal to the option count", true, func(m map[string]any) { m["topics"] = []any{j2TestTopic("t", j2TestQuestion("q", 3, 3))} }, true},
		{"topic", "topic title empty", true, func(m map[string]any) { m["topics"] = []any{j2TestTopic("")} }, false},
		{"old shape", "the old root repo is ignored and tasks are required", true, func(m map[string]any) { delete(m, "tasks"); m["tasks"] = nil }, false},
	}
	for _, c := range cases {
		t.Run(c.ac+"/"+c.name, func(t *testing.T) {
			out := j2PlanOutput(c.mut)
			_, ok := validateJ2Output(out, j2TestContext(t, c.hasSource))
			if ok != c.wantOK {
				t.Errorf("accepted = %v, want %v; output=%s", ok, c.wantOK, out)
			}
		})
	}
}

// 過去の形（tasks が無く、根に repo・operation・steps がある）の出力は検査に落ちる。
func TestValidateJ2Output_OldShapeWithoutTasksIsInvalid(t *testing.T) {
	t.Parallel()
	old := []byte(`{"verdict":"plan","summary":"s","steps":["a"],"repo":"flywheel","operation":"impl","size":"S","done_criteria":"d","cross_repo":false,"related_repos":[]}`)
	if _, ok := validateJ2Output(old, j2TestContext(t, true)); ok {
		t.Error("an output in the old shape must be invalid")
	}
}

// 登録される場合の検査後の値（key・前提・対象リポジトリ・操作・統合先）が出力のまま入る。
func TestValidateJ2Output_ValidatedTasksCarryTheOutput(t *testing.T) {
	t.Parallel()
	out := j2PlanOutput(func(m map[string]any) {
		tasks := j2TasksOf("a", "b")
		tasks[1].(map[string]any)["depends_on"] = []string{"a"}
		tasks[0].(map[string]any)["repo"] = "flywheel-sibling"
		tasks[1].(map[string]any)["repo"], tasks[1].(map[string]any)["operation"] = "flywheel-sibling", "define"
		m["tasks"] = tasks
		m["uses_integration"] = true
	})
	v, ok := validateJ2Output(out, j2TestContext(t, true))
	if !ok || len(v.Tasks) != 2 || !v.UsesIntegration {
		t.Fatalf("ok=%v v=%+v", ok, v)
	}
	if v.Tasks[1].Key != "b" || v.Tasks[1].Repo != "flywheel-sibling" || v.Tasks[1].Operation != "define" || len(v.Tasks[1].DependsOn) != 1 || v.Tasks[1].DependsOn[0] != "a" {
		t.Errorf("task b = %+v", v.Tasks[1])
	}
	if _, single := v.soleTask(); single {
		t.Error("soleTask must be false for 2 tasks")
	}
}

// --- AC-40〜AC-51: 登録 ---

func j2ItemRunCount(t *testing.T, f *j2Fixture, judgment string) int {
	t.Helper()
	var n int
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM run WHERE judgment = ?`, judgment).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// AC-40: size が M・expected が 1 件以上・topics が空の plan は、判断点の呼び出し 1 回（judgment = J2 の実行が 1 件）で
// 計画として登録される。
func TestPlanAutoJ2_SizeMPlanWithoutTopicsRegistersInOneCall(t *testing.T) {
	t.Parallel()
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "m-no-topics", "P1")
	inv := succeededInvoker(j2OutputFor(false, func(m map[string]any) { m["size"] = "M"; m["topics"] = []any{} }))
	calls := 0
	inv.onInvoke = func(JudgmentLaunchInput) { calls++ }
	res, err := f.run(t, inv, idPtr(ch.ID))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(res.Items) != 1 || res.Items[0].Outcome != "plan" || res.Items[0].Result != RunResultSucceeded {
		t.Fatalf("calls=%d items=%+v, want one call and a registered plan", calls, res.Items)
	}
	if n := j2ItemRunCount(t, f, "J2"); n != 1 {
		t.Errorf("J2 runs = %d, want 1", n)
	}
	d := f.detail(t, ch.ID)
	if d.Status != StatusAwaitingPlanApproval || len(d.Plans) != 1 || len(d.Holds) != 0 {
		t.Errorf("status=%s plans=%d holds=%d", d.Status, len(d.Plans), len(d.Holds))
	}
}

// 問い 2 件・論点 2 つの plan を 1 つ登録する。
func j2RegisterPlanWithQuestions(t *testing.T) (*j2Fixture, *ChallengeDetail, int, int) {
	t.Helper()
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "with-questions", "P1")
	holdsBefore := countRowsForTest(t, f.s, "hold")
	tasksBefore := countRowsForTest(t, f.s, "task")
	out := j2OutputFor(false, func(m map[string]any) {
		m["size"] = "M"
		m["budget_impl_usd"] = 41.5
		m["topics"] = []any{
			j2TestTopic("TOPIC-A-題名", j2TestQuestion("QTEXT-1 どちらにするか", 3, 2), j2TestQuestion("QTEXT-2 いつやるか", 2, 1)),
			j2TestTopic("TOPIC-B-題名"),
		}
		tasks := j2TasksOf("a", "b")
		tasks[0].(map[string]any)["repo"], tasks[0].(map[string]any)["operation"] = "harness-repo", "brief"
		tasks[0].(map[string]any)["title"] = "TASK-A-題名"
		tasks[1].(map[string]any)["repo"], tasks[1].(map[string]any)["operation"] = "harness-repo", "brief"
		tasks[1].(map[string]any)["title"] = "TASK-B-題名"
		tasks[1].(map[string]any)["depends_on"] = []string{"a"}
		m["tasks"] = tasks
		m["uses_integration"] = true
	})
	res, err := f.run(t, succeededInvoker(out), idPtr(ch.ID))
	if err != nil || len(res.Items) != 1 || res.Items[0].Outcome != "plan" {
		t.Fatalf("run: %v %+v", err, res)
	}
	return f, f.detail(t, ch.ID), holdsBefore, tasksBefore
}

// AC-41・AC-42: 問いが 2 件ある plan を登録すると、課題の状態は awaiting_plan_approval になり、hold の行は増えない。
func TestPlanAutoJ2_PlanWithQuestionsAwaitsApprovalWithoutHold(t *testing.T) {
	t.Parallel()
	f, d, holdsBefore, _ := j2RegisterPlanWithQuestions(t)
	if d.Status != StatusAwaitingPlanApproval {
		t.Errorf("status = %s, want awaiting_plan_approval", d.Status)
	}
	if got := countRowsForTest(t, f.s, "hold"); got != holdsBefore {
		t.Errorf("hold rows = %d, want %d (a plan with questions makes no hold)", got, holdsBefore)
	}
}

// AC-43〜AC-50: 計画の本文に、論点の題名・問いの文・選択肢のラベル・推奨の印・タスクの題名・対象リポジトリ・操作・実装枠の額がある。
func TestPlanAutoJ2_PlanBodyCarriesTopicsQuestionsTasksAndBudget(t *testing.T) {
	t.Parallel()
	_, d, _, _ := j2RegisterPlanWithQuestions(t)
	body := d.Plans[len(d.Plans)-1].Body
	for _, c := range []struct{ ac, want string }{
		{"AC-43 topic title", "TOPIC-A-題名"},
		{"AC-43 second topic", "TOPIC-B-題名"},
		{"AC-44 question text", "QTEXT-1 どちらにするか"},
		{"AC-44 question text", "QTEXT-2 いつやるか"},
		{"AC-45 option label", "QTEXT-1 どちらにするか-選択肢3"},
		{"AC-45 option label", "QTEXT-2 いつやるか-選択肢2"},
		{"AC-47 task title", "TASK-A-題名"},
		{"AC-47 task title", "TASK-B-題名"},
		{"AC-48 task repo", "harness-repo"},
		{"AC-49 task operation", "brief"},
		{"AC-50 impl budget (sum of the task frames)", "実装枠（この計画のタスクの枠の合計）: 6 USD"},
		{"AC-50 per-task frame", "枠: 実装 3 USD・レビュー対応 2 USD"},
		{"uses_integration", "統合先を使う: はい"},
		{"question numbers", "Q1."},
		{"question numbers", "Q2."},
	} {
		if !strings.Contains(body, c.want) {
			t.Errorf("%s: the plan body lacks %q:\n%s", c.ac, c.want, body)
		}
	}
	// 問いの番号は、論点の並びと問いの並びの順に 1 から振る（論点 B は問いを持たないので Q3 は無い）。
	if strings.Contains(body, "Q3.") {
		t.Errorf("unexpected Q3:\n%s", body)
	}
	q1, q2 := strings.Index(body, "Q1. QTEXT-1"), strings.Index(body, "Q2. QTEXT-2")
	if q1 < 0 || q2 < 0 || q1 > q2 {
		t.Errorf("Q1 must be the first question and Q2 the second:\n%s", body)
	}
	// 前提のタスクが題名で分かる。
	if !strings.Contains(body, "前提: TASK-A-題名") {
		t.Errorf("the dependency of the second task is not shown:\n%s", body)
	}
}

// AC-46: 推奨の選択肢だけに「推奨」の印が付く（選択肢の位置は 1 から数える）。
func TestPlanAutoJ2_PlanBodyMarksOnlyTheRecommendedOption(t *testing.T) {
	t.Parallel()
	_, d, _, _ := j2RegisterPlanWithQuestions(t)
	body := d.Plans[len(d.Plans)-1].Body
	if n := strings.Count(body, "【推奨】"); n != 2 {
		t.Errorf("recommend marks = %d, want 2 (one per question):\n%s", n, body)
	}
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.Contains(line, "QTEXT-1 どちらにするか-選択肢2"):
			if !strings.Contains(line, "【推奨】") || !strings.HasPrefix(strings.TrimSpace(line), "2)") {
				t.Errorf("option 2 of Q1 must be marked: %q", line)
			}
		case strings.Contains(line, "QTEXT-1 どちらにするか-選択肢1"), strings.Contains(line, "QTEXT-1 どちらにするか-選択肢3"):
			if strings.Contains(line, "【推奨】") {
				t.Errorf("a non-recommended option is marked: %q", line)
			}
		case strings.Contains(line, "QTEXT-2 いつやるか-選択肢1"):
			if !strings.Contains(line, "【推奨】") {
				t.Errorf("option 1 of Q2 must be marked: %q", line)
			}
		case strings.Contains(line, "QTEXT-2 いつやるか-選択肢2"):
			if strings.Contains(line, "【推奨】") {
				t.Errorf("option 2 of Q2 must not be marked: %q", line)
			}
		}
	}
}

// AC-51: plan を登録した時点（承認の前）では task の行が増えない。
func TestPlanAutoJ2_RegisteringAPlanCreatesNoTaskRows(t *testing.T) {
	t.Parallel()
	f, d, _, tasksBefore := j2RegisterPlanWithQuestions(t)
	if got := countRowsForTest(t, f.s, "task"); got != tasksBefore {
		t.Errorf("task rows = %d, want %d", got, tasksBefore)
	}
	// 承認の前のツリーは task_plan.spec から読む。
	if d.Plans[len(d.Plans)-1].Spec == nil || !strings.Contains(*d.Plans[len(d.Plans)-1].Spec, `"tasks"`) {
		t.Errorf("the spec must carry the tasks: %+v", d.Plans[len(d.Plans)-1].Spec)
	}
}

// --- AC-52: 入力 ---

// AC-52: 過去の論点の記録（challenge.issues）がある課題でも、J2 の入力に区画「論点と回答」が無い。
func TestPlanAutoJ2_InputHasNoIssuesAndAnswerSectionEvenWithAPastIssuesRecord(t *testing.T) {
	t.Parallel()
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "past-issues", "P1")
	cid, _ := parseChallengeID(ch.ID)
	setIssuesForTest(t, f.s, cid, issuesN(3))
	if d := f.detail(t, ch.ID); len(d.Issues) != 3 {
		t.Fatalf("the past issues record must be readable: %+v", d.Issues)
	}
	var launched JudgmentLaunchInput
	inv := succeededInvoker(j2OutputFor(false, nil))
	inv.onInvoke = func(in JudgmentLaunchInput) { launched = in }
	if _, err := f.run(t, inv, idPtr(ch.ID)); err != nil {
		t.Fatal(err)
	}
	if len(launched.Sections) == 0 {
		t.Fatal("J2 was not launched")
	}
	for _, s := range launched.Sections {
		if s.Label == "論点と回答" || s.Label == "問い直し" {
			t.Errorf("J2 input has the removed section %q", s.Label)
		}
	}
}

// AC-54: core が定義する J2 の判定の値の一覧に issues が無い。
func TestJ2VerdictValues_HaveNoIssues(t *testing.T) {
	t.Parallel()
	for _, v := range J2VerdictValues {
		if string(v) == "issues" {
			t.Error("J2VerdictValues still has issues")
		}
	}
	// verdict が issues の出力は、中身に関わらず不正な出力になる。
	if _, ok := validateJ2Output([]byte(`{"verdict":"issues","issues":[]}`), j2TestContext(t, true)); ok {
		t.Error("a verdict issues output must be invalid")
	}
}

// 登録しない判定: 予想が空のサイズ M は従来どおり保留になり、問いの有無に関わらず論点の記録は要らない。
func TestPlanAutoJ2_SizeMWithoutExpectedStillHolds(t *testing.T) {
	t.Parallel()
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "m-no-expected", "P1")
	out := j2OutputFor(false, func(m map[string]any) {
		m["size"] = "M"
		m["expected"] = nil
		m["topics"] = []any{j2TestTopic("t", j2TestQuestion("q", 2, 1))}
	})
	if _, err := f.run(t, succeededInvoker(out), idPtr(ch.ID)); err != nil {
		t.Fatal(err)
	}
	d := f.detail(t, ch.ID)
	if d.Status != StatusAwaitingHuman || len(d.Plans) != 0 || len(d.Holds) != 1 {
		t.Errorf("status=%s plans=%d holds=%d, want a hold and no plan", d.Status, len(d.Plans), len(d.Holds))
	}
}

// 仕上げ 3 項目 3: 統合先を使う計画のタスクのリポジトリは 1 つ。2 つ以上は不正（統合先を使わない計画の検査は変えない）。
func TestValidateJ2Output_IntegrationPlanTasksMustShareOneRepository(t *testing.T) {
	t.Parallel()
	two := func(m map[string]any) {
		tasks := j2TasksOf("a", "b")
		tasks[1].(map[string]any)["repo"] = "flywheel-sibling"
		m["tasks"] = tasks
		m["uses_integration"] = true
	}
	if _, ok := validateJ2Output(j2PlanOutput(two), j2TestContext(t, true)); ok {
		t.Error("an integration plan across two repositories must be invalid")
	}
	same := func(m map[string]any) { m["tasks"] = j2TasksOf("a", "b"); m["uses_integration"] = true }
	if _, ok := validateJ2Output(j2PlanOutput(same), j2TestContext(t, true)); !ok {
		t.Error("an integration plan in one repository must be valid")
	}
	// 済みのタスクがあれば、済みのタスクのリポジトリと同じでなければならない。
	vc := j2TestContext(t, true)
	vc.HasDoneTasks, vc.DoneRepos = true, []string{"flywheel-sibling"}
	if _, ok := validateJ2Output(j2PlanOutput(same), vc); ok {
		t.Error("a plan in a repository other than the done tasks' repository must be invalid")
	}
	vc.DoneRepos = []string{"flywheel"}
	if _, ok := validateJ2Output(j2PlanOutput(same), vc); !ok {
		t.Error("a plan in the done tasks' repository must be valid")
	}
}
