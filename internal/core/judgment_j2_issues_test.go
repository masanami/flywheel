package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func j2IssuesOutput(mut func(m map[string]any)) []byte {
	return j2IssuesOutputN(3, mut)
}

// j2IssuesOutputN は論点が n 個の issues の出力を作る（問いは q1〜qn）。
func j2IssuesOutputN(n int, mut func(m map[string]any)) []byte {
	mkIssue := func(q string) map[string]any {
		return map[string]any{
			"question": q,
			"options": []any{
				map[string]any{"label": "案A-" + q, "example": "例-" + q},
				map[string]any{"label": "案B-" + q, "example": nil},
			},
			"recommended": 2,
		}
	}
	var issues []any
	for i := 1; i <= n; i++ {
		issues = append(issues, mkIssue("q"+strconv.Itoa(i)))
	}
	m := map[string]any{
		"verdict": "issues",
		"issues":  issues,
	}
	if mut != nil {
		mut(m)
	}
	b, _ := json.Marshal(m)
	return b
}

func TestJ2VerdictValues_IncludesIssuesAndSchemaEnum(t *testing.T) {
	var decoded map[string]any
	if err := json.Unmarshal(j2OutputSchema(), &decoded); err != nil {
		t.Fatal(err)
	}
	enum := decoded["properties"].(map[string]any)["verdict"].(map[string]any)["enum"].([]any)
	found := false
	for _, v := range enum {
		if v == "issues" {
			found = true
		}
	}
	if !found {
		t.Errorf("verdict enum = %v, want issues included", enum)
	}
}

func TestValidateJ2Output_Issues(t *testing.T) {
	vc := j2TestContext(t, true)
	issue := func(n int, rec int) map[string]any {
		var opts []any
		for i := 0; i < n; i++ {
			opts = append(opts, map[string]any{"label": "L", "example": nil})
		}
		return map[string]any{"question": "Q", "options": opts, "recommended": rec}
	}
	many := func(n int) []any {
		var out []any
		for i := 0; i < n; i++ {
			out = append(out, issue(2, 1))
		}
		return out
	}
	ok := []struct {
		name string
		out  []byte
	}{
		{"3 issues", j2IssuesOutput(nil)},
		{"5 issues", j2IssuesOutput(func(m map[string]any) { m["issues"] = many(5) })},
		{"5 options", j2IssuesOutput(func(m map[string]any) { m["issues"] = []any{issue(5, 5), issue(2, 1), issue(2, 1)} })},
		{"plan fields are ignored even if wrong", j2IssuesOutput(func(m map[string]any) {
			m["size"] = "XL"
			m["repo"] = "ghost"
			m["budget_impl_usd"] = -1
			m["cross_repo"] = nil
			m["done_criteria"] = ""
		})},
		{"2000 code points", j2IssuesOutput(func(m map[string]any) {
			is := issue(2, 1)
			is["question"] = strings.Repeat("あ", 2000)
			m["issues"] = []any{is, issue(2, 1), issue(2, 1)}
		})},
	}
	for _, c := range ok {
		t.Run("ok/"+c.name, func(t *testing.T) {
			v, accepted := validateJ2Output(c.out, vc)
			if !accepted || v.Verdict != J2VerdictIssues || len(v.Issues) < 3 {
				t.Fatalf("accepted=%v v=%+v", accepted, v)
			}
			var in struct{ Issues []StageIssue }
			_ = json.Unmarshal(c.out, &in)
			if !reflect.DeepEqual(v.Issues, in.Issues) {
				t.Errorf("issues changed: %+v vs %+v", v.Issues, in.Issues)
			}
		})
	}
	bad := []struct {
		name string
		out  []byte
	}{
		{"no issues", j2IssuesOutput(func(m map[string]any) { delete(m, "issues") })},
		{"empty issues", j2IssuesOutput(func(m map[string]any) { m["issues"] = []any{} })},
		{"2 issues", j2IssuesOutput(func(m map[string]any) { m["issues"] = many(2) })},
		{"6 issues", j2IssuesOutput(func(m map[string]any) { m["issues"] = many(6) })},
		{"1 option", j2IssuesOutput(func(m map[string]any) { m["issues"] = []any{issue(1, 1), issue(2, 1), issue(2, 1)} })},
		{"6 options", j2IssuesOutput(func(m map[string]any) { m["issues"] = []any{issue(6, 1), issue(2, 1), issue(2, 1)} })},
		{"recommended 0", j2IssuesOutput(func(m map[string]any) { m["issues"] = []any{issue(2, 0), issue(2, 1), issue(2, 1)} })},
		{"recommended above options", j2IssuesOutput(func(m map[string]any) { m["issues"] = []any{issue(2, 3), issue(2, 1), issue(2, 1)} })},
		{"blank question", j2IssuesOutput(func(m map[string]any) {
			is := issue(2, 1)
			is["question"] = " \n"
			m["issues"] = []any{is, issue(2, 1), issue(2, 1)}
		})},
		{"blank label", j2IssuesOutput(func(m map[string]any) {
			is := issue(2, 1)
			is["options"] = []any{map[string]any{"label": " "}, map[string]any{"label": "x"}}
			m["issues"] = []any{is, issue(2, 1), issue(2, 1)}
		})},
		{"blank example", j2IssuesOutput(func(m map[string]any) {
			is := issue(2, 1)
			is["options"] = []any{map[string]any{"label": "a", "example": " "}, map[string]any{"label": "x"}}
			m["issues"] = []any{is, issue(2, 1), issue(2, 1)}
		})},
		{"2001 code points", j2IssuesOutput(func(m map[string]any) {
			is := issue(2, 1)
			is["question"] = strings.Repeat("あ", 2001)
			m["issues"] = []any{is, issue(2, 1), issue(2, 1)}
		})},
	}
	for _, c := range bad {
		t.Run("bad/"+c.name, func(t *testing.T) {
			if _, accepted := validateJ2Output(c.out, vc); accepted {
				t.Fatal("want rejected")
			}
		})
	}
}

// AC-278: 論点 3・4・5 個のどれも、人間対応待ちになる。
func TestPlanAutoJ2_Issues_HoldsWithQuestionIssuesAndHoldID(t *testing.T) {
	for _, n := range []int{3, 4, 5} {
		t.Run(strconv.Itoa(n)+" issues", func(t *testing.T) {
			f := newJ2Fixture(t)
			ch := f.newClassified(t, "big", "P1")
			before := f.detail(t, ch.ID)
			res, err := f.run(t, succeededInvoker(j2IssuesOutputN(n, nil)), idPtr(ch.ID))
			if err != nil {
				t.Fatalf("PlanAutoJ2: %v", err)
			}
			item := res.Items[0]
			if item.Outcome != "issues" || item.Status == nil || *item.Status != string(StatusAwaitingHuman) || item.Result != RunResultSucceeded {
				t.Fatalf("item = %+v", item)
			}
			d := f.detail(t, ch.ID)
			if d.Status != StatusAwaitingHuman || d.Version != before.Version+1 || len(d.Plans) != 0 {
				t.Fatalf("detail status=%s version=%d plans=%v", d.Status, d.Version, d.Plans)
			}
			if len(d.Holds) != 1 || d.Holds[0].FromStatus != StatusClassified || d.Holds[0].RunID == nil {
				t.Fatalf("holds = %+v", d.Holds)
			}
			want := IssuesQuestionPreamble + "\n"
			for i := 1; i <= n; i++ {
				q := "q" + strconv.Itoa(i)
				want += "\nQ" + strconv.Itoa(i) + ". " + q + "\n  1) 案A-" + q + "（例: 例-" + q + "）\n  2) 案B-" + q + "\n  推奨: 2\n"
			}
			if d.Holds[0].Question != want {
				t.Errorf("question = %q, want %q", d.Holds[0].Question, want)
			}
			if len(d.Issues) != n || d.Issues[0].Question != "q1" || d.IssuesHoldID == nil {
				t.Fatalf("issues = %+v holdID = %v", d.Issues, d.IssuesHoldID)
			}
			var holdID int64
			if err := f.s.db.Read(context.Background(), func(tx *sql.Tx) error {
				return tx.QueryRow(`SELECT id FROM hold WHERE challenge_id = ?`, mustChallengeInternalID(t, ch.ID)).Scan(&holdID)
			}); err != nil {
				t.Fatal(err)
			}
			if *d.IssuesHoldID != holdID {
				t.Errorf("issues_hold_id = %d, want %d", *d.IssuesHoldID, holdID)
			}
			// J2 のサイズは課題へ書かない（plan が無く、段階の欄は論点だけ）。
			if d.Expected != nil || d.NonGoals != nil || d.Figure != nil {
				t.Errorf("unexpected stage fields: %+v", d.StageFields)
			}
		})
	}
}

func TestPlanAutoJ2_Issues_AcceptedEvenWithSmallSizeAndIgnoresPlanFields(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "small", "P1")
	out := j2IssuesOutput(func(m map[string]any) { m["size"] = "S"; m["repo"] = "ghost" })
	if _, err := f.run(t, succeededInvoker(out), idPtr(ch.ID)); err != nil {
		t.Fatal(err)
	}
	if d := f.detail(t, ch.ID); d.Status != StatusAwaitingHuman || len(d.Issues) != 3 {
		t.Errorf("status=%s issues=%d", d.Status, len(d.Issues))
	}
}

func TestPlanAutoJ2_Issues_InvalidLeavesChallengeUnchanged(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "bad", "P1")
	before := f.detail(t, ch.ID)
	res, err := f.run(t, succeededInvoker(j2IssuesOutput(func(m map[string]any) { m["issues"] = []any{} })), idPtr(ch.ID))
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].Result != RunResultInvalidOutput {
		t.Errorf("result = %v", res.Items[0].Result)
	}
	after := f.detail(t, ch.ID)
	if after.Status != StatusClassified || after.Version != before.Version || after.Issues != nil || len(after.Holds) != 0 {
		t.Errorf("challenge changed: %+v", after.Challenge)
	}
}

func TestPlanAutoJ2_Issues_AnswerReturnsToClassifiedAndSecondIssuesIsInvalid(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "twice", "P1")
	if _, err := f.run(t, succeededInvoker(j2IssuesOutput(nil)), idPtr(ch.ID)); err != nil {
		t.Fatal(err)
	}
	answerHold(t, f.s, ch.ID, "ANSWER-MARKER 1: 2, 2: 1, 3: 1")
	d := f.detail(t, ch.ID)
	if d.Status != StatusClassified || d.IssuesAnswer == nil || *d.IssuesAnswer != "ANSWER-MARKER 1: 2, 2: 1, 3: 1" {
		t.Fatalf("status=%s answer=%v", d.Status, d.IssuesAnswer)
	}

	// 次の J2 の入力には論点と回答が入る。
	var launched JudgmentLaunchInput
	inv := succeededInvoker(j2IssuesOutput(nil))
	inv.onInvoke = func(in JudgmentLaunchInput) { launched = in }
	before := f.detail(t, ch.ID)
	res, err := f.run(t, inv, idPtr(ch.ID))
	if err != nil {
		t.Fatal(err)
	}
	var all string
	for _, sec := range launched.Sections {
		if sec.Label == "論点と回答" {
			all = sec.Content
		}
	}
	for _, want := range []string{"Q1. q1", "案A-q1", "推奨: 2", "ANSWER-MARKER"} {
		if !strings.Contains(all, want) {
			t.Errorf("J2 input lacks %q:\n%s", want, all)
		}
	}
	// 2 回目の issues は invalid_output で、課題は変わらない。
	if res.Items[0].Result != RunResultInvalidOutput {
		t.Errorf("result = %v, want invalid_output", res.Items[0].Result)
	}
	after := f.detail(t, ch.ID)
	if after.Status != StatusClassified || after.Version != before.Version || len(after.Holds) != 1 {
		t.Errorf("challenge changed: %+v holds=%d", after.Challenge, len(after.Holds))
	}
}

func TestPlanAutoJ2_Issues_NoIssuesSectionWithoutRecord(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "none", "P1")
	var launched JudgmentLaunchInput
	inv := succeededInvoker(j2OutputFor(false, nil))
	inv.onInvoke = func(in JudgmentLaunchInput) { launched = in }
	if _, err := f.run(t, inv, idPtr(ch.ID)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(allSectionsText(launched.Sections), "論点と回答") {
		t.Error("section 論点と回答 must not appear without an issues record")
	}
}

func TestPlanAutoJ2_Issues_ChallengeLeftClassifiedDuringJ2_WritesNothing(t *testing.T) {
	f := newJ2Fixture(t)
	ch := f.newClassified(t, "moved", "P1")
	inv := succeededInvoker(j2IssuesOutput(nil))
	inv.onInvoke = func(JudgmentLaunchInput) {
		if _, err := f.s.HoldChallenge(context.Background(), ChannelCLI, ch.ID, HoldInput{Question: "HUMAN-FIRST"}); err != nil {
			t.Errorf("HoldChallenge: %v", err)
		}
	}
	if _, err := f.run(t, inv, idPtr(ch.ID)); err != nil {
		t.Fatal(err)
	}
	d := f.detail(t, ch.ID)
	if d.Issues != nil || d.IssuesHoldID != nil || len(d.Holds) != 1 || d.Holds[0].Question != "HUMAN-FIRST" {
		t.Errorf("detail = issues %v holds %+v", d.Issues, d.Holds)
	}
}
