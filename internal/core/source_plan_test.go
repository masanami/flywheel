package core

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// 計画を作る判断点（J2）の入力の区画「取り込み元の子」・出力の source の検査・計画の本文・承認での task.source_key の
// 記録の試験（#778。S2b）。各試験の名前に対応する受入基準（AC-<番号>）を書く。上流は偽、ストアは実物。

// addChild は課題 ch の取り込み元の子を 1 件記録する（state は open・closed・missing）。
func (f *j2Fixture) addChild(t *testing.T, ch *Challenge, key, parent, title, body, state string) {
	t.Helper()
	cid, _ := parseChallengeID(ch.ID)
	err := f.s.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO source_child (challenge_id, source_id, external_key, parent_external_key, url, title, body, upstream_state, created_at, updated_at)
			VALUES (?, 'src', ?, ?, ?, ?, ?, ?, '2026-09-25T00:00:00.000Z', '2026-09-25T00:00:00.000Z')`,
			cid, key, parent, "https://example.invalid/"+key, title, body, state)
		return err
	})
	if err != nil {
		t.Fatalf("addChild: %v", err)
	}
}

// childPlanFix は取り込み元 o/r#7 に対応し、子 o/r#8（開）・孫 o/r#9（開）・閉じた子 o/r#10 を持つ課題。
type childPlanFix struct {
	*j2Fixture
	ch *Challenge
}

func newChildPlanFix(t *testing.T) *childPlanFix {
	t.Helper()
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "children", "P1")
	f.bind(t, ch, 7, simpleThread("UP-BODY"))
	f.addChild(t, ch, "o/r#8", "o/r#7", "CHILD-TITLE-8", "CHILD-BODY-8", "open")
	f.addChild(t, ch, "o/r#9", "o/r#8", "GRAND-TITLE-9", "GRAND-BODY-9", "open")
	f.addChild(t, ch, "o/r#10", "o/r#7", "CLOSED-TITLE-10", "CLOSED-BODY-10", "closed")
	return &childPlanFix{j2Fixture: f, ch: ch}
}

// srcTasks は source が sources[i]（"" は無し）のタスクを並べる。2 件以上なら統合先を使う。
func srcTasks(m map[string]any, sources ...string) {
	keys := []string{"a", "b", "c", "d"}[:len(sources)]
	tasks := j2TasksOf(keys...)
	for i, s := range sources {
		if s != "" {
			tasks[i].(map[string]any)["source"] = s
		}
	}
	m["tasks"] = tasks
	m["uses_integration"] = len(sources) >= 2
}

// plan は mut を掛けた出力で J2 を 1 回走らせ、起動の入力・結果の項目を返す。
func (f *childPlanFix) plan(t *testing.T, mut func(m map[string]any)) (JudgmentLaunchInput, J2AutoItem) {
	t.Helper()
	var launched JudgmentLaunchInput
	inv := succeededInvoker(j2OutputFor(true, mut))
	inv.onInvoke = func(in JudgmentLaunchInput) { launched = in }
	res, err := f.run(t, inv, idPtr(f.ch.ID))
	if err != nil || len(res.Items) != 1 {
		t.Fatalf("PlanAutoJ2: %v %+v", err, res)
	}
	return launched, res.Items[0]
}

func (f *childPlanFix) childrenSection(t *testing.T) (string, bool) {
	t.Helper()
	launched, _ := f.plan(t, func(m map[string]any) { srcTasks(m, "") })
	s, ok := sectionByLabel(launched.Sections, SourceChildrenSectionLabel)
	return s.Content, ok
}

func (f *childPlanFix) assertRegistered(t *testing.T, item J2AutoItem, want bool) {
	t.Helper()
	d := f.detail(t, f.ch.ID)
	if want {
		if item.Result != RunResultSucceeded || item.Outcome != "plan" || len(d.Plans) != 1 || d.Status != StatusAwaitingPlanApproval {
			t.Fatalf("item = %+v plans = %d status = %s, want the plan registered", item, len(d.Plans), d.Status)
		}
		return
	}
	if item.Result != RunResultInvalidOutput || len(d.Plans) != 0 || d.Status != StatusClassified {
		t.Fatalf("item = %+v plans = %d status = %s, want invalid_output and no plan", item, len(d.Plans), d.Status)
	}
}

func TestSourcePlan_AC165_InputHasTheChildrenSection(t *testing.T) { // #778
	t.Parallel()
	f := newChildPlanFix(t)
	if _, ok := f.childrenSection(t); !ok {
		t.Fatal(`AC-165 #778: the J2 input has no section "取り込み元の子"`)
	}
}

func TestSourcePlan_AC166_SectionHasTheExternalKey(t *testing.T) { // #778
	t.Parallel()
	f := newChildPlanFix(t)
	if c, _ := f.childrenSection(t); !strings.Contains(c, "o/r#8") {
		t.Errorf("AC-166 #778: section lacks the external_key o/r#8:\n%s", c)
	}
}

func TestSourcePlan_AC167_SectionHasTheTitle(t *testing.T) { // #778
	t.Parallel()
	f := newChildPlanFix(t)
	if c, _ := f.childrenSection(t); !strings.Contains(c, "CHILD-TITLE-8") {
		t.Errorf("AC-167 #778: section lacks the title:\n%s", c)
	}
}

func TestSourcePlan_AC168_SectionHasTheBody(t *testing.T) { // #778
	t.Parallel()
	f := newChildPlanFix(t)
	if c, _ := f.childrenSection(t); !strings.Contains(c, "CHILD-BODY-8") {
		t.Errorf("AC-168 #778: section lacks the body:\n%s", c)
	}
}

func TestSourcePlan_AC169_GrandchildCarriesTheDirectParentKey(t *testing.T) { // #778
	t.Parallel()
	f := newChildPlanFix(t)
	c, _ := f.childrenSection(t)
	i := strings.Index(c, "o/r#9")
	if i < 0 || !strings.Contains(c[i:], "直の親: o/r#8") {
		t.Errorf("AC-169 #778: the grandchild o/r#9 lacks 直の親: o/r#8:\n%s", c)
	}
}

func TestSourcePlan_AC170_NoSectionWithoutChildren(t *testing.T) { // #778
	t.Parallel()
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "no children", "P1")
	f.bind(t, ch, 7, simpleThread("UP-BODY"))
	var launched JudgmentLaunchInput
	inv := succeededInvoker(j2OutputFor(true, nil))
	inv.onInvoke = func(in JudgmentLaunchInput) { launched = in }
	if _, err := f.run(t, inv, idPtr(ch.ID)); err != nil {
		t.Fatal(err)
	}
	if _, ok := sectionByLabel(launched.Sections, SourceChildrenSectionLabel); ok {
		t.Error("AC-170 #778: a challenge without children must not get the section")
	}
}

func TestSourcePlan_AC171_ClosedChildIsNotInTheSection(t *testing.T) { // #778
	t.Parallel()
	f := newChildPlanFix(t)
	c, _ := f.childrenSection(t)
	if strings.Contains(c, "o/r#10") || strings.Contains(c, "CLOSED-TITLE-10") {
		t.Errorf("AC-171 #778: the closed child is in the section:\n%s", c)
	}
}

func TestSourcePlan_AC172_SchemaTaskHasSource(t *testing.T) { // #778
	t.Parallel()
	items := j2SchemaRoot(t)["tasks"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	if _, ok := items["source"]; !ok {
		t.Error("AC-172 #778: the task items of the output schema lack source")
	}
}

func TestSourcePlan_AC173_SourceOfAnOpenChildIsRegistered(t *testing.T) { // #778
	t.Parallel()
	f := newChildPlanFix(t)
	_, item := f.plan(t, func(m map[string]any) { srcTasks(m, "o/r#8") })
	f.assertRegistered(t, item, true)
}

func TestSourcePlan_AC174_SourceNotInTheChildrenIsInvalid(t *testing.T) { // #778
	t.Parallel()
	f := newChildPlanFix(t)
	_, item := f.plan(t, func(m map[string]any) { srcTasks(m, "o/r#99") })
	f.assertRegistered(t, item, false)
}

func TestSourcePlan_AC175_SourceOfAClosedChildIsInvalid(t *testing.T) { // #778
	t.Parallel()
	f := newChildPlanFix(t)
	_, item := f.plan(t, func(m map[string]any) { srcTasks(m, "o/r#10") })
	f.assertRegistered(t, item, false)
}

func TestSourcePlan_AC176_SameSourceInTwoTasksIsInvalid(t *testing.T) { // #778
	t.Parallel()
	f := newChildPlanFix(t)
	_, item := f.plan(t, func(m map[string]any) { srcTasks(m, "o/r#8", "o/r#8") })
	f.assertRegistered(t, item, false)
}

func TestSourcePlan_AC177_UntaskedChildDoesNotBlockThePlan(t *testing.T) { // #778
	t.Parallel()
	f := newChildPlanFix(t)
	_, item := f.plan(t, func(m map[string]any) { srcTasks(m, "o/r#8") }) // o/r#9 はどのタスクも指さない
	f.assertRegistered(t, item, true)
}

func TestSourcePlan_AC178_BodyHasTheChildKeyOfATask(t *testing.T) { // #778
	t.Parallel()
	f := newChildPlanFix(t)
	f.plan(t, func(m map[string]any) { srcTasks(m, "o/r#8", "o/r#9") })
	body := f.detail(t, f.ch.ID).Plans[0].Body
	if !strings.Contains(body, "取り込み元の子: o/r#8") || !strings.Contains(body, "取り込み元の子: o/r#9") {
		t.Errorf("AC-178 #778: the plan body lacks the children of the tasks:\n%s", body)
	}
	if strings.Contains(body, UntaskedChildrenHeading) {
		t.Errorf("a plan whose tasks point at every child has no untasked heading:\n%s", body)
	}
}

func TestSourcePlan_AC179_BodyListsTheUntaskedChildren(t *testing.T) { // #778
	t.Parallel()
	f := newChildPlanFix(t)
	f.plan(t, func(m map[string]any) { srcTasks(m, "o/r#8") })
	body := f.detail(t, f.ch.ID).Plans[0].Body
	i := strings.Index(body, "## タスクにしなかった取り込み元の子")
	if i < 0 || !strings.Contains(body[i:], "- o/r#9") {
		t.Errorf("AC-179 #778: the plan body lacks the heading and the untasked child o/r#9:\n%s", body)
	}
	if strings.Contains(body[i:], "o/r#8") || strings.Contains(body, "o/r#10") {
		t.Errorf("AC-179 #778: only the untasked open children are listed:\n%s", body)
	}
}

func TestSourcePlan_AC180_ApprovalRecordsTheSourceKey(t *testing.T) { // #778
	t.Parallel()
	f := newChildPlanFix(t)
	f.plan(t, func(m map[string]any) { srcTasks(m, "o/r#8", "") })
	if err := approvePlanForTest(t, f.s, f.ch.ID, ttyAtt(t, f.ch.ID)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	cid, _ := parseChallengeID(f.ch.ID)
	tasks := taskRows(t, f.s, cid)
	if len(tasks) != 2 || tasks[0].Key != "a" || tasks[0].SourceKey != "o/r#8" {
		t.Errorf("AC-180 #778: tasks = %+v, want the first task's source_key o/r#8", tasks)
	}
	if got := countRows(t, f.s, `SELECT COUNT(*) FROM task WHERE source_key = 'o/r#8'`); got != 1 {
		t.Errorf("tasks with source_key o/r#8 = %d, want 1", got)
	}
}

func TestSourcePlan_AC181_TaskWithoutSourceHasNullSourceKey(t *testing.T) { // #778
	t.Parallel()
	f := newChildPlanFix(t)
	f.plan(t, func(m map[string]any) { srcTasks(m, "o/r#8", "") })
	if err := approvePlanForTest(t, f.s, f.ch.ID, ttyAtt(t, f.ch.ID)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if got := countRows(t, f.s, `SELECT COUNT(*) FROM task WHERE key = 'b' AND source_key IS NULL`); got != 1 {
		t.Errorf("AC-181 #778: tasks with a NULL source_key and no source = %d, want 1", got)
	}
}

// 委譲の段が計画を読み直すとき、その後に子が閉じていても計画は不正にならない（source の突き合わせは登録のときだけ）。
func TestSourcePlan_RevalidationAtDelegationDoesNotCheckSourceAgainstTheChildren(t *testing.T) { // #778
	t.Parallel()
	f := newChildPlanFix(t)
	_, item := f.plan(t, func(m map[string]any) { srcTasks(m, "o/r#8") })
	f.assertRegistered(t, item, true)
	spec := *f.detail(t, f.ch.ID).Plans[0].Spec
	vc := j2TestContext(t, true)
	if _, ok := validateJ2Output([]byte(spec), vc); !ok {
		t.Error("a registered plan must stay valid when the source is not checked against the children")
	}
	vc.CheckSource, vc.OpenChildKeys = true, nil
	if _, ok := validateJ2Output([]byte(spec), vc); ok {
		t.Error("with the check on and no open children, a source is invalid")
	}
}
