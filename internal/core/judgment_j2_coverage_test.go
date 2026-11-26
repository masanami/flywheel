package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// このファイルは、計画を作る判断点の出力の受入基準の受け持ち（covers・deferred_criteria）の検査と、
// 計画への書き足し・タスクの記録・計画の本文（docs/features/request-to-plan.md）を確かめる。

// 受け持ちの試験の基準。長さは計画の本文の抜き出し（80 文字）の境目に合わせる。
var (
	covCrit80 = strings.Repeat("八", 79) + "末"
	covCrit81 = strings.Repeat("九", 80) + "超"
	covCritA  = "基準 A の文"
	covDoc    = "# 仕様\n\n## 受入基準\n\n- [ ] " + covCritA + "\n- [ ] " + covCrit80 + "\n- [ ] " + covCrit81 + "\n"

	covIDA  = acceptanceCriterionID(covCritA)
	covID80 = acceptanceCriterionID(covCrit80)
	covID81 = acceptanceCriterionID(covCrit81)
)

const covUnknownID = "AC-00000000"

func covCriteria() *planAcceptanceCriteria {
	return &planAcceptanceCriteria{Path: criteriaDocPath, Items: []acceptanceCriterion{
		{ID: covIDA, Text: covCritA}, {ID: covID80, Text: covCrit80}, {ID: covID81, Text: covCrit81},
	}}
}

func covDeferred(id string, reason any) map[string]any {
	return map[string]any{"id": id, "reason": reason}
}

// covTasks は covers が与えられた通りのタスク 2 件。nil の要素は covers を書かない。
func covTasks(repo, operation string, covers ...[]string) []any {
	tasks := []any{}
	for i, c := range covers {
		task := j2TestTask([]string{"a", "b", "c"}[i], repo, operation)
		task["budget_impl_usd"], task["budget_review_usd"] = 1.0, 1.0
		if c != nil {
			task["covers"] = c
		}
		tasks = append(tasks, task)
	}
	return tasks
}

func covPlan(deferred []any, covers ...[]string) func(m map[string]any) {
	return func(m map[string]any) {
		m["tasks"] = covTasks("harness-repo", "brief", covers...)
		m["uses_integration"] = true
		if deferred != nil {
			m["deferred_criteria"] = deferred
		}
	}
}

func TestValidateJ2Output_Coverage(t *testing.T) {
	t.Parallel()
	all := []string{covIDA, covID80, covID81}
	rest := []string{covID80, covID81}
	for _, tc := range []struct {
		name     string
		criteria *planAcceptanceCriteria
		mut      func(map[string]any)
		// want は理由に含む語（空は、検査を通る）。
		want string
	}{
		{"AC-43 すべて covers にある", covCriteria(), covPlan(nil, all, []string{}), ""},
		{"AC-44 漏れ", covCriteria(), covPlan(nil, []string{covIDA}, []string{covID80}), "criteria not covered"},
		{"AC-45 covers に無い識別", covCriteria(), covPlan(nil, all, []string{covUnknownID}), "unknown criterion"},
		{"AC-45 covers に識別でない文", covCriteria(), covPlan(nil, all, []string{covCritA}), "unknown criterion"},
		{"AC-46 deferred_criteria に無い識別", covCriteria(), covPlan([]any{covDeferred(covUnknownID, "理由")}, all), "unknown criterion"},
		{"AC-47 1 つのタスクで重なる", covCriteria(), covPlan(nil, []string{covIDA, covIDA, covID80, covID81}), "duplicate criterion"},
		{"AC-48 deferred と covers が重なる", covCriteria(), covPlan([]any{covDeferred(covIDA, "理由")}, all), "duplicate criterion"},
		{"AC-49 理由が無い", covCriteria(), covPlan([]any{map[string]any{"id": covIDA}}, rest), "deferred_criteria[0].reason"},
		{"AC-49 理由が null", covCriteria(), covPlan([]any{covDeferred(covIDA, nil)}, rest), "deferred_criteria[0].reason"},
		{"AC-50 理由が空白だけ", covCriteria(), covPlan([]any{covDeferred(covIDA, " \n\t　")}, rest), "deferred_criteria[0].reason"},
		{"AC-51 理由が 2001 文字", covCriteria(), covPlan([]any{covDeferred(covIDA, strings.Repeat("あ", 2001))}, rest), "deferred_criteria[0].reason"},
		{"AC-51 理由が 2000 文字", covCriteria(), covPlan([]any{covDeferred(covIDA, strings.Repeat("あ", 2000))}, rest), ""},
		{"AC-52 残りが理由つきで deferred", covCriteria(), covPlan([]any{covDeferred(covIDA, "後で"), covDeferred(covID80, "後で")}, []string{covID81}), ""},
		{"AC-53 2 つのタスクが同じ基準", covCriteria(), covPlan(nil, all, []string{covIDA}), ""},
		{"AC-54 covers が空・省略・null のタスク", covCriteria(), func(m map[string]any) {
			covPlan(nil, all, []string{}, nil)(m)
			m["tasks"].([]any)[1].(map[string]any)["covers"] = nil
			m["deferred_criteria"] = nil
		}, ""},
		{"AC-55 区画なしで covers に識別", nil, covPlan(nil, []string{covIDA}), "unknown criterion"},
		{"区画なしで deferred_criteria に識別", nil, covPlan([]any{covDeferred(covIDA, "理由")}, nil), "unknown criterion"},
		{"AC-56 区画なしで省略", nil, covPlan(nil, nil, nil), ""},
		{"AC-56 区画なしで空", nil, covPlan([]any{}, []string{}), ""},
		{"判断点は一覧を書けない", nil, func(m map[string]any) {
			covPlan(nil, nil)(m)
			m["acceptance_criteria"] = map[string]any{"path": "x", "items": []any{}}
		}, "acceptance_criteria"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			vc := j2TestContext(t, false)
			vc.CheckCriteria, vc.Criteria = true, tc.criteria
			_, reason := validateJ2OutputReason(j2PlanOutput(tc.mut), vc)
			switch {
			case tc.want == "" && reason != "":
				t.Fatalf("want valid, got %q", reason)
			case tc.want != "" && !strings.Contains(reason, tc.want):
				t.Fatalf("reason = %q, want it to contain %q", reason, tc.want)
			}
			// AC-61: 理由に基準の文を写さない。
			for _, text := range []string{covCritA, covCrit80, covCrit81} {
				if strings.Contains(reason, text) {
					t.Errorf("reason carries a criterion text: %q", reason)
				}
			}
		})
	}
}

// uncertain の出力には受け持ちの検査を掛けない。
func TestValidateJ2Output_CoverageIsNotCheckedForUncertain(t *testing.T) {
	t.Parallel()
	vc := j2TestContext(t, false)
	vc.CheckCriteria, vc.Criteria = true, covCriteria()
	if _, reason := validateJ2OutputReason([]byte(`{"verdict":"uncertain","question":"q"}`), vc); reason != "" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestJ2OutputSchema_CoversAndDeferredCriteria(t *testing.T) {
	t.Parallel()
	nullableArray := []any{"array", "null"}
	for name, schema := range map[string][]byte{
		"plan":            j2OutputSchema(),
		"expand":          j2ExpandOutputSchema(),
		"expand followup": j2ExpandOutputSchemaFor(TaskAdditionOriginFollowup),
	} {
		covers, ok := j2SchemaTaskProps(t, schema)["covers"].(map[string]any)
		if !ok {
			t.Fatalf("%s: tasks lack covers", name)
		}
		if !reflect.DeepEqual(covers["type"], nullableArray) || covers["items"].(map[string]any)["type"] != "string" {
			t.Errorf("%s: covers = %v", name, covers)
		}
		var decoded map[string]any
		if err := json.Unmarshal(schema, &decoded); err != nil {
			t.Fatal(err)
		}
		items := decoded["properties"].(map[string]any)["tasks"].(map[string]any)["items"].(map[string]any)
		for _, r := range items["required"].([]any) {
			if r == "covers" {
				t.Errorf("%s: covers must not be required", name)
			}
		}
		if _, has := decoded["properties"].(map[string]any)["deferred_criteria"]; has != (name == "plan") {
			t.Errorf("%s: has deferred_criteria = %v", name, has)
		}
	}
	root := j2SchemaRoot(t)
	deferred := root["deferred_criteria"].(map[string]any)
	if !reflect.DeepEqual(deferred["type"], nullableArray) {
		t.Errorf("deferred_criteria type = %v", deferred["type"])
	}
	item := deferred["items"].(map[string]any)
	if item["additionalProperties"] != false || !reflect.DeepEqual(item["required"], []any{"id", "reason"}) {
		t.Errorf("deferred_criteria item = %v", item)
	}
	props := item["properties"].(map[string]any)
	if props["id"].(map[string]any)["type"] != "string" || props["reason"].(map[string]any)["maxLength"] != float64(2000) {
		t.Errorf("deferred_criteria properties = %v", props)
	}
}

// covRegister は仕様の文書（withSpec のとき）を持つ分類済の課題に、出力 out の計画の呼び出しを 1 回行う。
func covRegister(t *testing.T, withSpec bool, out []byte) (*j2Fixture, string, J2AutoItem) {
	t.Helper()
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "coverage", "P1")
	if withSpec {
		writeWorkspaceFile(t, f.s.workspace, criteriaDocPath, covDoc)
		f.setSpec(t, ch.ID, criteriaDocPath)
	}
	res, err := f.run(t, succeededInvoker(out), idPtr(ch.ID))
	if err != nil || len(res.Items) != 1 {
		t.Fatalf("PlanAutoJ2: %+v %v", res, err)
	}
	return f, ch.ID, res.Items[0]
}

func covLatestPlan(t *testing.T, f *j2Fixture, id string) (body string, spec map[string]json.RawMessage) {
	t.Helper()
	d := f.detail(t, id)
	if len(d.Plans) != 1 || d.Plans[0].Spec == nil {
		t.Fatalf("plans = %+v", d.Plans)
	}
	if err := json.Unmarshal([]byte(*d.Plans[0].Spec), &spec); err != nil {
		t.Fatal(err)
	}
	return d.Plans[0].Body, spec
}

// AC-57〜AC-61: 不正な出力の理由は実行の記録に残り、基準の文を含まない。計画は登録しない。
func TestPlanAutoJ2_CoverageFailureIsRecordedOnTheRun(t *testing.T) {
	t.Parallel()
	all := []string{covIDA, covID80, covID81}
	for _, tc := range []struct {
		name, want string
		out        []byte
	}{
		{"AC-57 漏れ", "criteria not covered", j2OutputFor(false, covPlan(nil, []string{covIDA}))},
		{"AC-58 無い識別", "unknown criterion", j2OutputFor(false, covPlan(nil, all, []string{covUnknownID}))},
		{"AC-59 タスク内の重なり", "duplicate criterion", j2OutputFor(false, covPlan(nil, append([]string{covIDA}, all...)))},
		{"AC-60 deferred との重なり", "duplicate criterion", j2OutputFor(false, covPlan([]any{covDeferred(covIDA, "理由")}, all))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, id, item := covRegister(t, true, tc.out)
			if item.Result != RunResultInvalidOutput {
				t.Fatalf("result = %v, want invalid_output", item.Result)
			}
			if d := f.detail(t, id); d.Status != StatusClassified || len(d.Plans) != 0 {
				t.Errorf("status = %s, plans = %d", d.Status, len(d.Plans))
			}
			runs, err := f.s.ListRuns(context.Background(), RunListOptions{ChallengeID: &id})
			if err != nil || len(runs) != 1 {
				t.Fatalf("runs = %+v, %v", runs, err)
			}
			got := runs[0].Error
			if !strings.HasPrefix(got, "invalid J2 output: ") || !strings.Contains(got, tc.want) {
				t.Errorf("run error = %q, want %q", got, tc.want)
			}
			for _, text := range []string{covCritA, covCrit80, covCrit81} {
				if strings.Contains(got, text) {
					t.Errorf("run error carries a criterion text: %q", got)
				}
			}
		})
	}
}

// AC-62・AC-64・AC-65: 一覧の書き足し、承認で作るタスクの記録、課題の詳細。
func TestPlanAutoJ2_CoverageIsRecordedOnThePlanAndTheTasks(t *testing.T) {
	t.Parallel()
	f, id, item := covRegister(t, true, j2OutputFor(false, covPlan(nil, []string{covID81, covIDA, covID80}, nil)))
	if item.Result != RunResultSucceeded || item.Outcome != "plan" {
		t.Fatalf("item = %+v", item)
	}
	_, spec := covLatestPlan(t, f, id)
	var list planAcceptanceCriteria
	if err := json.Unmarshal(spec["acceptance_criteria"], &list); err != nil {
		t.Fatalf("acceptance_criteria: %v (%s)", err, spec["acceptance_criteria"])
	}
	if !reflect.DeepEqual(&list, covCriteria()) {
		t.Errorf("acceptance_criteria = %+v, want %+v", list, *covCriteria())
	}

	// 保存した出力は、受入基準の検査を求めない読み直しを通る。
	vc := j2TestContext(t, false)
	vc.SkipFlowReason = true
	if _, reason := validateJ2OutputReason([]byte(*f.detail(t, id).Plans[0].Spec), vc); reason != "" {
		t.Errorf("re-validation of the stored spec: %q", reason)
	}

	if err := approvePlanForTest(t, f.s, id, ttyAtt(t, id)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	tasks := f.detail(t, id).Tasks
	if len(tasks) != 2 {
		t.Fatalf("tasks = %+v", tasks)
	}
	if want := []string{covID81, covIDA, covID80}; !reflect.DeepEqual(tasks[0].Covers, want) {
		t.Errorf("tasks[0].Covers = %v, want %v", tasks[0].Covers, want)
	}
	if len(tasks[1].Covers) != 0 {
		t.Errorf("tasks[1].Covers = %v, want none", tasks[1].Covers)
	}
	var nulls int
	cid, _ := parseChallengeID(id)
	if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM task WHERE challenge_id = ? AND covers IS NULL`, cid).Scan(&nulls)
	}); err != nil {
		t.Fatal(err)
	}
	if nulls != 1 {
		t.Errorf("tasks with NULL covers = %d, want 1", nulls)
	}
}

// AC-56・AC-63・AC-73: 仕様の文書の場所を持たない課題の計画は、一覧を書き足さず、本文に検査していない旨を出す。
func TestPlanAutoJ2_NoSpecPathPlanHasNoListAndSaysItIsNotChecked(t *testing.T) {
	t.Parallel()
	f, id, item := covRegister(t, false, j2OutputFor(false, covPlan(nil, nil, []string{})))
	if item.Result != RunResultSucceeded || item.Outcome != "plan" {
		t.Fatalf("item = %+v", item)
	}
	body, spec := covLatestPlan(t, f, id)
	if v, has := spec["acceptance_criteria"]; has {
		t.Errorf("unexpected acceptance_criteria: %s", v)
	}
	requireLines(t, body, "## 受入基準の受け持ち", "- 仕様の文書が無いので、受入基準の網羅は検査していない", "## 完了条件")
	if strings.Contains(body, "仕様の文書: ") {
		t.Errorf("body:\n%s", body)
	}
}

// AC-66〜AC-71: 計画の本文のタスクごとの件数と、節「受入基準の受け持ち」。
func TestPlanAutoJ2_PlanBodyShowsTheCoverage(t *testing.T) {
	t.Parallel()
	f, id, _ := covRegister(t, true, j2OutputFor(false, covPlan(
		[]any{covDeferred(covID81, "続きのタスクで\n受け持つ"), covDeferred(covID80, "後の区切り")},
		[]string{covIDA}, []string{},
	)))
	body, _ := covLatestPlan(t, f, id)
	requireLines(t, body,
		"## タスク",
		"1. タスク a",
		"   - 受け持つ受入基準: 1 件",
		"2. タスク b",
		"   - 受け持つ受入基準: 0 件",
		"## 受入基準の受け持ち",
		"- 仕様の文書: "+criteriaDocPath,
		"- 受入基準: 3 件",
		"- 受け持たない基準:",
		// 受け持たない基準は、文書に出てくる順。80 文字はそのまま、81 文字は切って「…」を付ける。
		"  - "+covCrit80+"（"+covID80+"）: 後の区切り",
		"  - "+strings.Repeat("九", 80)+"…（"+covID81+"）: 続きのタスクで 受け持つ",
		"## 方針",
	)
}

// AC-72
func TestPlanAutoJ2_PlanBodySaysThereIsNoDeferredCriterion(t *testing.T) {
	t.Parallel()
	f, id, _ := covRegister(t, true, j2OutputFor(false, covPlan(nil, []string{covIDA, covID80, covID81})))
	body, _ := covLatestPlan(t, f, id)
	requireLines(t, body, "## 受入基準の受け持ち", "- 受入基準: 3 件", "- 受け持たない基準: なし")
}
