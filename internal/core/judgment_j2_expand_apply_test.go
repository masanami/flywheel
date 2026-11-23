package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

// 区切り S4c-1 の 6: 「足す」の形の出力の検査と、足す処理（none・add〔上限以下〕・不正な出力）。実ストアと偽の判断の IF で確かめる。

// expandAddOutput は、タスクを足す出力を作る（mut で各タスクの項目を直せる）。
func expandAddOutput(mut func(tasks []map[string]any)) string {
	mk := func(key string) map[string]any {
		return map[string]any{
			"key": key, "title": "続き " + key, "body": "本文", "done_criteria": "完了", "non_goals": []string{},
			"depends_on": []string{}, "repo": "harness-repo", "operation": "brief",
			"budget_impl_usd": 2.0, "budget_review_usd": 1.0,
		}
	}
	tasks := []map[string]any{mk("c")}
	if mut != nil {
		mut(tasks)
	}
	b, _ := json.Marshal(map[string]any{"verdict": "add", "tasks": tasks, "question": nil})
	return string(b)
}

// expandDoneFixture は、a が済みで、足す依頼がまだ無い課題。
func expandDoneFixture(t *testing.T) *paFixture {
	t.Helper()
	f := newExpandFixture(t)
	f.setTaskStatus(t, "a", TaskStatusDone)
	return f
}

func (f *paFixture) taskByKey(t *testing.T, key string) *Task {
	t.Helper()
	for _, tk := range taskRows(t, f.s, f.cid()) {
		if tk.Key == key {
			c := tk
			return &c
		}
	}
	return nil
}

func TestExpandApply_NoneClosesTheAdditionAndAddsNothing(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	f.expand(t, &expandRecorder{out: []byte(`{"verdict":"none"}`)})
	adds := f.additions(t)
	if len(adds) != 1 || adds[0].State != TaskAdditionNone || adds[0].DecidedAt == nil {
		t.Fatalf("additions = %+v, want one closed as none", adds)
	}
	if n := len(taskRows(t, f.s, f.cid())); n != 2 {
		t.Errorf("tasks = %d, want 2 (nothing added)", n)
	}
	if st := f.detail(t, f.id).Status; st != StatusInProgress {
		t.Errorf("status = %s, want in_progress", st)
	}
}

func TestExpandApply_AddUnderTheLimitCreatesPendingTasksAndClosesTheAddition(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	res := f.expand(t, &expandRecorder{out: []byte(expandAddOutput(func(ts []map[string]any) {
		ts[0]["depends_on"] = []string{"a", "b"}
	}))})
	if len(res.Items) != 1 || res.Items[0].Result != RunResultSucceeded || res.Items[0].Outcome != "add" {
		t.Fatalf("items = %+v", res.Items)
	}
	c := f.taskByKey(t, "c")
	if c == nil || c.Status != TaskStatusPending || c.Title != "続き c" {
		t.Fatalf("task c = %+v, want pending with the output's title", c)
	}
	a, b := f.taskByKey(t, "a"), f.taskByKey(t, "b")
	if len(c.DependsOn) != 2 || c.DependsOn[0] != a.ID || c.DependsOn[1] != b.ID {
		t.Errorf("c.DependsOn = %v, want [%s %s]", c.DependsOn, a.ID, b.ID)
	}
	if c.PlanVersion != a.PlanVersion {
		t.Errorf("plan version = %d, want the approved one %d", c.PlanVersion, a.PlanVersion)
	}
	adds := f.additions(t)
	if len(adds) != 1 || adds[0].State != TaskAdditionApplied || len(adds[0].TaskIDs) != 1 || adds[0].TaskIDs[0] != c.ID {
		t.Fatalf("additions = %+v, want applied with the added task", adds)
	}
	if adds[0].TotalAfterUSD == nil || *adds[0].TotalAfterUSD != 13 || adds[0].LimitUSD == nil || *adds[0].LimitUSD != 14 {
		t.Errorf("total/limit = %v/%v, want 13/14", adds[0].TotalAfterUSD, adds[0].LimitUSD)
	}
	d := f.detail(t, f.id)
	if d.Status != StatusInProgress {
		t.Errorf("status = %s, want in_progress (no owner approval, no hold)", d.Status)
	}
	// 作業ログに、足す依頼・足したタスク・足した後の見込み・上限を残す。
	var after string
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT after FROM activity WHERE entity = 'task_addition' AND action = 'add_tasks'`).Scan(&after)
	})
	for _, want := range []string{adds[0].ID, c.ID, "13", "14"} {
		if !strings.Contains(after, want) {
			t.Errorf("activity after = %s, want it to carry %q", after, want)
		}
	}
	// 判断点の費用は課題の見込みに数えない（見込みは枠の合計だけ）。
	if d.Budget.EstimateUSD == nil || *d.Budget.EstimateUSD != 13 {
		t.Errorf("estimate = %v, want 13", d.Budget.EstimateUSD)
	}
}

func TestExpandApply_AddedTaskIsLaunchedLikeTheOthers(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	f.expand(t, &expandRecorder{out: []byte(expandAddOutput(nil))})
	// 足したタスクは、委譲の段が見る対象（承認済みの版の未着手のタスク）に入る。
	var n int
	readForTest(t, f.s, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM task t WHERE t.challenge_id = ? AND t.status = 'pending' AND t.addition_id IS NOT NULL
			AND t.plan_version = (SELECT plan_version FROM approval WHERE challenge_id = t.challenge_id AND kind = 'plan' AND decision = 'approved' ORDER BY id DESC LIMIT 1)`, f.cid()).Scan(&n)
	})
	if n != 1 {
		t.Errorf("pending added tasks of the approved version = %d, want 1", n)
	}
}

func TestExpandApply_AddExactlyAtTheLimitIsApplied(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	f.expand(t, &expandRecorder{out: []byte(expandAddOutput(func(ts []map[string]any) {
		ts[0]["budget_impl_usd"], ts[0]["budget_review_usd"] = 3.0, 1.0 // 10 + 4 = 上限 14
	}))})
	if adds := f.additions(t); len(adds) != 1 || adds[0].State != TaskAdditionApplied {
		t.Fatalf("additions = %+v, want applied at the limit", adds)
	}
}

// expandTaskMap は検査に通る足すタスク 1 件（項目を mut で直す）。
func expandTaskMap(key string, mut func(m map[string]any)) map[string]any {
	m := map[string]any{
		"key": key, "title": "t", "body": "b", "done_criteria": "d", "non_goals": []string{},
		"depends_on": []string{}, "repo": "harness-repo", "operation": "brief", "budget_impl_usd": 0.1, "budget_review_usd": 0.1,
	}
	if mut != nil {
		mut(m)
	}
	return m
}

func expandAddOf(tasks ...map[string]any) string {
	b, _ := json.Marshal(map[string]any{"verdict": "add", "tasks": tasks})
	return string(b)
}

func TestExpandApply_InvalidOutputsLeaveEverythingAndCountAsFailures(t *testing.T) {
	t.Parallel()
	set := func(field string, v any) string {
		return expandAddOf(expandTaskMap("c", func(m map[string]any) { m[field] = v }))
	}
	thirteen := make([]map[string]any, 0, 13)
	for i := 0; i < 13; i++ {
		thirteen = append(thirteen, expandTaskMap("k"+string(rune('a'+i)), nil))
	}
	outputs := map[string]string{
		"key of an existing task":      set("key", "a"),
		"empty title":                  set("title", " "),
		"empty body":                   set("body", ""),
		"empty done criteria":          set("done_criteria", ""),
		"dependency on an unknown key": set("depends_on", []string{"zzz"}),
		"dependency on itself":         set("depends_on", []string{"c"}),
		"another repo":                 set("repo", "flywheel"),
		"unknown operation":            set("operation", "nope"),
		"no review budget":             expandAddOf(expandTaskMap("c", func(m map[string]any) { delete(m, "budget_review_usd") })),
		"zero tasks":                   `{"verdict":"add","tasks":[]}`,
		"thirteen tasks":               expandAddOf(thirteen...),
		"cycle between added tasks": expandAddOf(
			expandTaskMap("x", func(m map[string]any) { m["depends_on"] = []string{"y"} }),
			expandTaskMap("y", func(m map[string]any) { m["depends_on"] = []string{"x"} })),
		"two added tasks sharing a key": expandAddOf(expandTaskMap("x", nil), expandTaskMap("x", nil)),
		"verdict outside the set":       `{"verdict":"plan"}`,
		"ask without a question":        `{"verdict":"ask","question":" "}`,
		"not json":                      `not json`,
	}
	for name, out := range outputs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := expandDoneFixture(t)
			res := f.expand(t, &expandRecorder{out: []byte(out)})
			if len(res.Items) != 1 || res.Items[0].Result != RunResultInvalidOutput {
				t.Fatalf("items = %+v, want invalid_output", res.Items)
			}
			if adds := f.additions(t); len(adds) != 1 || adds[0].State != TaskAdditionPending {
				t.Errorf("additions = %+v, want pending kept", adds)
			}
			if n := len(taskRows(t, f.s, f.cid())); n != 2 {
				t.Errorf("tasks = %d, want 2 (nothing added)", n)
			}
			// flywheel#1210: どの検査で落ちたかを、実行の記録の誤りの欄に残す（内部の前置きは入れない）。
			runs, err := f.s.ListRuns(context.Background(), RunListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var runErr string
			for _, r := range runs {
				if r.ID == res.Items[0].RunID {
					runErr = r.Error
				}
			}
			if !strings.HasPrefix(runErr, "invalid J2 expand output: ") || len(runErr) == len("invalid J2 expand output: ") || strings.Contains(runErr, ErrValidation.Error()) {
				t.Errorf("run error = %q, want the expand prefix and a reason without the internal prefix", runErr)
			}
		})
	}
}

func TestExpandApply_ThirteenTasksFitTheSchemaLimitOnlyUpToTwelve(t *testing.T) {
	t.Parallel()
	twelve := make([]map[string]any, 0, 12)
	for i := 0; i < 12; i++ {
		twelve = append(twelve, expandTaskMap("k"+string(rune('a'+i)), nil))
	}
	f := expandDoneFixture(t)
	f.expand(t, &expandRecorder{out: []byte(expandAddOf(twelve...))})
	// 12 件の枠は 0.2 × 12 = 2.4 USD で、上限 14 に収まる（件数の上限は 12）。
	if n := len(taskRows(t, f.s, f.cid())); n != 14 {
		t.Errorf("tasks = %d, want 14 (12 added)", n)
	}
}

func TestExpandApply_DependencyOnACancelledOrPreviousVersionTaskIsInvalid(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		setup func(f *paFixture, t *testing.T)
		mut   func(ts []map[string]any)
	}{
		"dependency on a cancelled task": {
			setup: func(f *paFixture, t *testing.T) { f.setTaskStatus(t, "b", TaskStatusCancelled) },
			mut:   func(ts []map[string]any) { ts[0]["depends_on"] = []string{"b"} },
		},
		"key of a cancelled task": {
			setup: func(f *paFixture, t *testing.T) { f.setTaskStatus(t, "b", TaskStatusCancelled) },
			mut:   func(ts []map[string]any) { ts[0]["key"] = "b" },
		},
		"dependency on a key only the previous version has": {
			setup: func(f *paFixture, t *testing.T) {
				writeForTest(t, f.s, func(tx *sql.Tx) error {
					_, err := tx.Exec(`INSERT INTO task (challenge_id, plan_version, key, title, body, done_criteria, repo, operation, status, created_at, updated_at, budget_impl_micros, budget_review_micros)
						VALUES (?, 99, 'old', 't', 'b', 'd', 'harness-repo', 'brief', 'pending', '2026-11-22T00:00:00.000Z', '2026-11-22T00:00:00.000Z', 1000000, 1000000)`, f.cid())
					return err
				})
			},
			mut: func(ts []map[string]any) { ts[0]["depends_on"] = []string{"old"} },
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := expandDoneFixture(t)
			c.setup(f, t)
			res := f.expand(t, &expandRecorder{out: []byte(expandAddOutput(c.mut))})
			if len(res.Items) != 1 || res.Items[0].Result != RunResultInvalidOutput {
				t.Fatalf("items = %+v, want invalid_output", res.Items)
			}
		})
	}
}

func TestExpandApply_ConsecutiveInvalidOutputsHoldTheInProgressChallengeAndAnswerResumesIt(t *testing.T) {
	t.Parallel()
	f := expandDoneFixture(t)
	inv := &expandRecorder{out: []byte(`not json`)}
	limit := judgmentFailureLimit(f.agent)
	var held bool
	for i := 0; i < limit+1 && !held; i++ {
		res := f.expand(t, inv)
		for _, ns := range res.NotStarted {
			held = held || ns.Reason == NotStartedFailureLimit
		}
	}
	if !held {
		t.Fatalf("no failure_limit hold after %d invalid outputs", limit)
	}
	if inv.count() != limit {
		t.Errorf("J2 calls = %d, want %d (the limit)", inv.count(), limit)
	}
	d := f.detail(t, f.id)
	if d.Status != StatusAwaitingHuman {
		t.Fatalf("status = %s, want awaiting_human", d.Status)
	}
	if adds := f.additions(t); len(adds) != 1 || adds[0].State != TaskAdditionPending {
		t.Errorf("additions = %+v, want pending kept", adds)
	}
	// 保留に回答すると、課題は着手中へ戻る。
	if _, _, err := f.s.ExecuteAnswer(context.Background(), AnswerRequest{ChallengeID: f.id, ExpectedVersion: d.Version, Answer: "続けて"}, verifiedAttestationForTest(t, f.id)); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if st := f.detail(t, f.id).Status; st != StatusInProgress {
		t.Errorf("status after the answer = %s, want in_progress", st)
	}
}

func TestExpandApply_ThePlanShapeFilterIsNotAppliedToTheExpandShape(t *testing.T) {
	t.Parallel()
	// 計画の形の検査（計画の判定・分類済みの課題だけ）と、足す形の検査は別: 計画の形の出力は足す形では不正、逆も同じ。
	if _, ok := validateJ2ExpandOutput([]byte(`{"verdict":"plan"}`), j2ExpandValidationContext{}); ok {
		t.Error("a plan-shape verdict must be invalid in the expand shape")
	}
	if _, ok := validateJ2Output([]byte(`{"verdict":"add"}`), j2ValidationContext{}); ok {
		t.Error("an expand-shape verdict must be invalid in the plan shape")
	}
}
