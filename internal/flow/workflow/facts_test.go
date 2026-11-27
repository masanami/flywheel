package workflow

import (
	"strings"
	"testing"
)

// 最上位の facts（接続契約の facts に出す値の宣言。docs/harness-runtime-design.md §5.6.2）の読み込みと検査。
// 参照は $steps.<段>.<欄> だけで、段が在り、欄がその段の出力スキーマにある文字列か配列であること。

func withFacts(t *testing.T, facts string) string {
	t.Helper()
	return mutate(t, "limits:\n", "facts:\n"+facts+"\nlimits:\n")
}

func TestFactsAreLoaded(t *testing.T) {
	path, scripts := setup(t, "w", withFacts(t, "  name: [$steps.a.detail.name, $steps.b.detail.name]"))
	wf, err := LoadAndValidate(path, Options{ScriptsDir: scripts})
	if err != nil {
		t.Fatal(err)
	}
	if len(wf.Facts) != 1 || wf.Facts[0].Name != "name" || len(wf.Facts[0].Refs) != 2 {
		t.Fatalf("facts = %+v", wf.Facts)
	}
	if r := wf.Facts[0].Refs[1]; r.Step != "b" || strings.Join(r.Path, ".") != "detail.name" {
		t.Fatalf("ref = %+v", r)
	}
}

func TestFactsAreRejected(t *testing.T) {
	cases := []struct{ name, facts, want string }{
		{"a step that does not exist", "  f: [$steps.nope.detail.name]", "refers to a step that does not exist"},
		{"a field that is not in the output schema", "  f: [$steps.a.nope]", `output schema has no field "nope"`},
		{"AC-122 an object field", "  f: [$steps.a.detail]", "must be a string or array field"},
		{"AC-123 a number field", "  f: [$steps.a.count]", "must be a string or array field"},
		{"an input reference", "  f: [$inputs.s]", "must be a $steps.<step>.<field> reference"},
		{"an edge reference", "  f: [$edge.x]", "must be a $steps.<step>.<field> reference"},
		{"a literal", "  f: [complete]", "must be exactly one reference"},
		{"an empty list", "  f: []", "must be a non-empty list"},
		{"a scalar instead of a list", "  f: $steps.a.detail.name", "must be a non-empty list"},
		{"a bad name", "  Bad-Name: [$steps.a.detail.name]", "must match"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := check(t, withFacts(t, c.facts))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

// AC-121: 出力の型が配列の欄は facts に宣言できる（要素の型は問わない）。
func TestFactsAcceptAnArrayField(t *testing.T) {
	for _, facts := range []string{"  f: [$steps.a.items]", "  f: [$steps.a.anything]"} {
		if err := check(t, withFacts(t, facts)); err != nil {
			t.Errorf("%s: %v", facts, err)
		}
	}
}

// facts は上書きに書けない（上書きできるのは limits と steps の決まったキーだけ）。
func TestOverrideCannotWriteFacts(t *testing.T) {
	_, err := loadTicket(t, ov(LayerArg, "facts:\n  self_review: [$steps.implement.self_review]\n"))
	if err == nil || !strings.Contains(err.Error(), `key "facts" cannot be overridden`) {
		t.Fatalf("err = %v", err)
	}
}

// 同梱の ticket は self_review をレビューのステップ（self-review）の出力から出す。
func TestTicketDeclaresSelfReviewFact(t *testing.T) {
	wf, err := loadTicket(t)
	if err != nil {
		t.Fatal(err)
	}
	if len(wf.Facts) != 1 || wf.Facts[0].Name != "self_review" {
		t.Fatalf("facts = %+v", wf.Facts)
	}
	var steps []string
	for _, r := range wf.Facts[0].Refs {
		steps = append(steps, r.Step+"."+strings.Join(r.Path, "."))
	}
	if got, want := strings.Join(steps, ","), "self-review.self_review"; got != want {
		t.Fatalf("refs = %s, want %s", got, want)
	}
}

// self_review はレビューのステップの出力の必須の欄。無い出力はスキーマが拒み、ある出力は enum の値だけを通す。
// 実装・直し・レビュー対応の出力は self_review を持たない（セルフレビューはレビューのステップだけが行う）。
func TestSelfReviewOutputRequiresSelfReview(t *testing.T) {
	wf, err := loadTicket(t)
	if err != nil {
		t.Fatal(err)
	}
	review := func(outcome, sr string) map[string]any {
		v := map[string]any{"outcome": outcome, "codex_review": "off", "summary": "s", "findings": []any{}, "residual_findings": []any{}, "unverified": []any{}}
		if sr != "" {
			v["self_review"] = sr
		}
		return v
	}
	for _, c := range []struct {
		step string
		ok   []map[string]any
		bad  []map[string]any
	}{
		{"self-review", []map[string]any{review("clean", "complete"), review("incomplete", "incomplete"),
			{"outcome": "findings", "self_review": "complete", "codex_review": "partial", "summary": "s", "residual_findings": []any{}, "unverified": []any{"codex"},
				"findings": []any{map[string]any{"file": "a.go", "line": 3, "severity": "high", "claim": "c", "evidence": "e", "source": "codex"}}}},
			[]map[string]any{review("clean", ""), review("clean", "not_run"), review("done", "complete")}},
		{"fix", []map[string]any{{"outcome": "pass", "summary": "s", "deviation_report": ""}},
			[]map[string]any{{"outcome": "pass", "summary": "s", "deviation_report": "", "self_review": "complete"}}},
		{"respond", []map[string]any{{"outcome": "pushed", "summary": "s", "human_question": ""}},
			[]map[string]any{{"outcome": "pushed", "summary": "s", "human_question": "", "self_review": "not_run"}}},
	} {
		sch := wf.Step(c.step).OutputSchema
		for _, v := range c.ok {
			if err := sch.Validate(v); err != nil {
				t.Errorf("%s %v: %v", c.step, v, err)
			}
		}
		for _, v := range c.bad {
			if err := sch.Validate(v); err == nil {
				t.Errorf("%s %v: accepted", c.step, v)
			}
		}
	}
}
