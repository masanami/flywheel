package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func j2Expected(n int) []any {
	var out []any
	for i := 0; i < n; i++ {
		out = append(out, map[string]any{"kind": "ui", "scene": "S", "before": "B", "after": "A"})
	}
	return out
}

func TestValidateJ2Output_StageFields(t *testing.T) {
	vc := j2TestContext(t, true)
	longRunes := strings.Repeat("あ", MaxStageRunes+1)
	bad := []struct {
		name string
		mut  func(m map[string]any)
	}{
		{"expected 4 items", func(m map[string]any) { m["expected"] = j2Expected(4) }},
		{"expected bad kind", func(m map[string]any) {
			m["expected"] = []any{map[string]any{"kind": "web", "scene": "S", "before": "B", "after": "A"}}
		}},
		{"expected blank after", func(m map[string]any) {
			m["expected"] = []any{map[string]any{"kind": "ui", "scene": "S", "before": "B", "after": "  "}}
		}},
		{"expected too long", func(m map[string]any) {
			m["expected"] = []any{map[string]any{"kind": "ui", "scene": longRunes, "before": "B", "after": "A"}}
		}},
		{"non_goals 11 items", func(m map[string]any) { m["non_goals"] = strings.Split(strings.Repeat("x,", 11), ",")[:11] }},
		{"non_goals blank", func(m map[string]any) { m["non_goals"] = []string{" "} }},
		{"figure empty", func(m map[string]any) { m["figure"] = "" }},
		{"figure blank", func(m map[string]any) { m["figure"] = " \n" }},
		{"figure too big", func(m map[string]any) { m["figure"] = strings.Repeat("x", MaxFigureBytes+1) }},
	}
	for _, c := range bad {
		if _, ok := validateJ2Output(j2PlanOutput(c.mut), vc); ok {
			t.Errorf("%s: want invalid", c.name)
		}
	}
	for name, mut := range map[string]func(m map[string]any){
		"expected null":    func(m map[string]any) { m["expected"] = nil },
		"expected empty":   func(m map[string]any) { m["expected"] = []any{} },
		"non_goals empty":  func(m map[string]any) { m["non_goals"] = []string{} },
		"figure null":      func(m map[string]any) { m["figure"] = nil },
		"fields absent":    func(m map[string]any) { delete(m, "expected"); delete(m, "non_goals"); delete(m, "figure") },
		"figure at limit":  func(m map[string]any) { m["figure"] = strings.Repeat("x", MaxFigureBytes) },
		"expected 3 items": func(m map[string]any) { m["expected"] = j2Expected(3) },
	} {
		if _, ok := validateJ2Output(j2PlanOutput(mut), vc); !ok {
			t.Errorf("%s: want valid", name)
		}
	}
	v, _ := validateJ2Output(j2PlanOutput(func(m map[string]any) { m["expected"] = []any{}; m["non_goals"] = []string{} }), vc)
	if v.Expected != nil || v.NonGoals != nil {
		t.Errorf("empty arrays must normalize to nil: %+v %+v", v.Expected, v.NonGoals)
	}
}

func TestJ2OutputSchema_StageFields(t *testing.T) {
	var decoded map[string]any
	if err := json.Unmarshal(j2OutputSchema(), &decoded); err != nil {
		t.Fatal(err)
	}
	props := decoded["properties"].(map[string]any)
	if got := props["expected"].(map[string]any)["maxItems"]; got != float64(MaxExpected) {
		t.Errorf("expected maxItems = %v", got)
	}
	if got := props["non_goals"].(map[string]any)["maxItems"]; got != float64(MaxNonGoals) {
		t.Errorf("non_goals maxItems = %v", got)
	}
}

func TestPlanAutoJ2_Plan_WritesStageFieldsAndActivity(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "stages", "P1")
	// 前の実物・やらないこと・図は plan で置き換わる／消える。
	actual := `[{"scene":"x","after":"y"}]`
	nonGoals := `["old"]`
	if _, err := f.s.EditChallenge(context.Background(), ChannelCLI, ch.ID, EditInput{Actual: &actual, NonGoals: &nonGoals}); err != nil {
		t.Fatal(err)
	}
	out := j2OutputFor(false, nil)
	if _, err := f.run(t, succeededInvoker(out), idPtr(ch.ID)); err != nil {
		t.Fatal(err)
	}
	d := f.detail(t, ch.ID)
	if d.Status != StatusAwaitingPlanApproval {
		t.Fatalf("status = %s", d.Status)
	}
	wantExp := []StageExpected{{Kind: ExpectedKindCLI, Scene: "SCENE-MARKER", Before: "BEFORE-MARKER", After: "AFTER-MARKER"}}
	if !reflect.DeepEqual(d.Expected, wantExp) {
		t.Errorf("expected = %+v", d.Expected)
	}
	if !reflect.DeepEqual(d.NonGoals, []string{"NONGOAL-MARKER"}) {
		t.Errorf("non_goals = %+v", d.NonGoals)
	}
	if d.Figure == nil || *d.Figure != "graph TD; A-->B" {
		t.Errorf("figure = %v", d.Figure)
	}
	if d.Actual != nil {
		t.Errorf("actual = %+v, want nil", d.Actual)
	}
	// 作業ログの after に変わった欄が載る。
	acts, err := f.s.ListActivities(context.Background(), &ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := acts[len(acts)-1]
	var after, before map[string]any
	if err := json.Unmarshal(last.After, &after); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(last.Before, &before); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"expected", "non_goals", "figure", "actual"} {
		if _, ok := after[k]; !ok {
			t.Errorf("activity after lacks %q: %s", k, last.After)
		}
	}
	if _, ok := before["actual"]; !ok {
		t.Errorf("activity before lacks actual: %s", last.Before)
	}
}

func TestPlanAutoJ2_Plan_OmittedFieldsClearNonGoalsFigureAndKeepExpected(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "keep", "P1")
	exp := `[{"kind":"ui","scene":"KEEP","before":"b","after":"a"}]`
	ng := `["old"]`
	fig := "graph LR; X-->Y"
	if _, err := f.s.EditChallenge(context.Background(), ChannelCLI, ch.ID, EditInput{Expected: &exp, NonGoals: &ng, Figure: &fig}); err != nil {
		t.Fatal(err)
	}
	out := j2OutputFor(false, func(m map[string]any) {
		m["expected"] = []any{}
		delete(m, "non_goals")
		m["figure"] = nil
	})
	if _, err := f.run(t, succeededInvoker(out), idPtr(ch.ID)); err != nil {
		t.Fatal(err)
	}
	d := f.detail(t, ch.ID)
	if d.Status != StatusAwaitingPlanApproval {
		t.Fatalf("status = %s", d.Status)
	}
	if len(d.Expected) != 1 || d.Expected[0].Scene != "KEEP" {
		t.Errorf("expected = %+v, want the current value kept", d.Expected)
	}
	if d.NonGoals != nil || d.Figure != nil {
		t.Errorf("non_goals=%v figure=%v, want NULL", d.NonGoals, d.Figure)
	}
}

// AC-301・AC-302: 予想が NULL の課題で、サイズ S の plan は expected が [] でも null でも
// 計画として登録され、課題の expected は NULL のまま（[] を書かない）。
func TestPlanAutoJ2_Plan_SizeSAllowsEmptyExpectedAndNoIssues(t *testing.T) {
	cases := []struct {
		name     string
		expected any
	}{
		{"empty array", []any{}},
		{"null", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newJ2Fixture(t)
			ch := f.newClassified(t, "small", "P1")
			out := j2OutputFor(false, func(m map[string]any) { m["expected"] = tc.expected })
			if _, err := f.run(t, succeededInvoker(out), idPtr(ch.ID)); err != nil {
				t.Fatal(err)
			}
			if d := f.detail(t, ch.ID); d.Status != StatusAwaitingPlanApproval || d.Expected != nil {
				t.Errorf("status=%s expected=%#v, want awaiting_plan_approval and nil", d.Status, d.Expected)
			}
			cid, _ := parseChallengeID(ch.ID)
			var raw sql.NullString
			if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
				return tx.QueryRow(`SELECT expected FROM challenge WHERE id = ?`, cid).Scan(&raw)
			}); err != nil {
				t.Fatal(err)
			}
			if raw.Valid {
				t.Errorf("challenge.expected = %q, want NULL", raw.String)
			}
		})
	}
}

func TestPlanAutoJ2_Plan_SizeMLWithoutIssuesRecordIsInvalidOutput(t *testing.T) {
	for _, size := range []string{"M", "L"} {
		f := newJ2Fixture(t)
		ch := f.newClassified(t, "nolog", "P1")
		before := f.detail(t, ch.ID)
		out := j2OutputFor(false, func(m map[string]any) { m["size"] = size })
		res, err := f.run(t, succeededInvoker(out), idPtr(ch.ID))
		if err != nil {
			t.Fatal(err)
		}
		if res.Items[0].Result != RunResultInvalidOutput {
			t.Errorf("%s: result = %v, want invalid_output", size, res.Items[0].Result)
		}
		after := f.detail(t, ch.ID)
		if after.Status != StatusClassified || after.Version != before.Version || len(after.Plans) != 0 || after.Expected != nil || len(after.Holds) != 0 {
			t.Errorf("%s: challenge changed: %+v", size, after.Challenge)
		}
	}
}

func TestPlanAutoJ2_Plan_IssuesRecordedViaEditCountsEvenWithoutHoldID(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "edit-issues", "P1")
	f.recordIssues(t, ch.ID)
	if d := f.detail(t, ch.ID); d.IssuesHoldID != nil {
		t.Fatalf("issues_hold_id = %v, want NULL", d.IssuesHoldID)
	}
	out := j2OutputFor(false, func(m map[string]any) { m["size"] = "L" })
	if _, err := f.run(t, succeededInvoker(out), idPtr(ch.ID)); err != nil {
		t.Fatal(err)
	}
	if d := f.detail(t, ch.ID); d.Status != StatusAwaitingPlanApproval {
		t.Errorf("status = %s", d.Status)
	}
}

// AC-298: サイズ M・L × expected の []・null の 4 通りのどれも、計画を登録せず人間対応待ちになる。
func TestPlanAutoJ2_Plan_SizeMLWithEmptyExpectedHoldsWithoutPlan(t *testing.T) {
	for _, size := range []string{"M", "L"} {
		for _, exp := range []struct {
			name string
			val  any
		}{{"empty array", []any{}}, {"null", nil}} {
			t.Run(size+"/"+exp.name, func(t *testing.T) {
				f := newJ2Fixture(t)
				ch := f.newClassified(t, "noexp", "P1")
				f.recordIssues(t, ch.ID)
				before := f.detail(t, ch.ID)
				out := j2OutputFor(false, func(m map[string]any) {
					m["size"] = size
					m["expected"] = exp.val
					m["question"] = "QUESTION-MARKER どう見えますか"
				})
				res, err := f.run(t, succeededInvoker(out), idPtr(ch.ID))
				if err != nil {
					t.Fatal(err)
				}
				item := res.Items[0]
				if item.Result != RunResultSucceeded || item.Status == nil || *item.Status != string(StatusAwaitingHuman) {
					t.Fatalf("item = %+v", item)
				}
				if item.Outcome != "uncertain" {
					t.Errorf("outcome = %q, want uncertain", item.Outcome)
				}
				d := f.detail(t, ch.ID)
				if d.Status != StatusAwaitingHuman || len(d.Plans) != 0 || d.Version != before.Version+1 {
					t.Fatalf("status=%s plans=%d version=%d", d.Status, len(d.Plans), d.Version)
				}
				last := d.Holds[len(d.Holds)-1]
				if last.Question != ExpectedEmptyHoldPreamble+" QUESTION-MARKER どう見えますか" || last.FromStatus != StatusClassified {
					t.Errorf("hold = %+v", last)
				}
				// 計画を登録しないので、非空の non_goals・figure も書かない。
				if d.NonGoals != nil || d.Figure != nil || d.Expected != nil {
					t.Errorf("stage fields written: %+v", d.StageFields)
				}
			})
		}
	}
}

func TestPlanAutoJ2_Plan_SizeMLUsesCurrentExpectedWhenOutputHasNone(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "cur", "P1")
	f.recordIssues(t, ch.ID)
	exp := `[{"kind":"behavior","scene":"CUR","before":"b","after":"a"}]`
	if _, err := f.s.EditChallenge(context.Background(), ChannelCLI, ch.ID, EditInput{Expected: &exp}); err != nil {
		t.Fatal(err)
	}
	out := j2OutputFor(false, func(m map[string]any) { m["size"] = "M"; m["expected"] = nil })
	if _, err := f.run(t, succeededInvoker(out), idPtr(ch.ID)); err != nil {
		t.Fatal(err)
	}
	d := f.detail(t, ch.ID)
	if d.Status != StatusAwaitingPlanApproval || len(d.Expected) != 1 || d.Expected[0].Scene != "CUR" {
		t.Errorf("status=%s expected=%+v", d.Status, d.Expected)
	}
}

func TestPlanAutoJ2_Issues_IgnoresStageFields(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "iss", "P1")
	out := j2IssuesOutput(func(m map[string]any) {
		m["expected"] = []any{map[string]any{"kind": "bad"}}
		m["figure"] = ""
	})
	if _, err := f.run(t, succeededInvoker(out), idPtr(ch.ID)); err != nil {
		t.Fatal(err)
	}
	d := f.detail(t, ch.ID)
	if d.Status != StatusAwaitingHuman || d.Expected != nil || d.Figure != nil {
		t.Errorf("status=%s fields=%+v", d.Status, d.StageFields)
	}
}

func TestPlanAutoJ2_InputCarriesCurrentStages(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "in", "P1")
	var launched JudgmentLaunchInput
	inv := succeededInvoker(j2OutputFor(false, nil))
	inv.onInvoke = func(in JudgmentLaunchInput) { launched = in }
	if _, err := f.run(t, inv, idPtr(ch.ID)); err != nil {
		t.Fatal(err)
	}
	for _, sec := range launched.Sections {
		if strings.Contains(sec.Label, "予想") {
			t.Fatalf("section present without any stage value: %q", sec.Label)
		}
	}

	exp := `[{"kind":"ui","scene":"SCENE-IN","before":"BEFORE-IN","after":"AFTER-IN"}]`
	ng := `["NG-IN"]`
	fig := "graph TD; FIG-IN"
	ch2 := f.newClassified(t, "in2", "P1")
	if _, err := f.s.EditChallenge(context.Background(), ChannelCLI, ch2.ID, EditInput{Expected: &exp, NonGoals: &ng, Figure: &fig}); err != nil {
		t.Fatal(err)
	}
	launched = JudgmentLaunchInput{}
	if _, err := f.run(t, inv, idPtr(ch2.ID)); err != nil {
		t.Fatal(err)
	}
	var content string
	for _, sec := range launched.Sections {
		if sec.Label == "いまの予想・やらないこと・図" {
			content = sec.Content
		}
	}
	for _, want := range []string{"SCENE-IN", "BEFORE-IN", "AFTER-IN", "NG-IN", "FIG-IN"} {
		if !strings.Contains(content, want) {
			t.Errorf("stages section lacks %q:\n%s", want, content)
		}
	}
}
