package core

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func j8Prop(layer, target, cause string, ids ...string) string {
	b, _ := json.Marshal(ids)
	return fmt.Sprintf(`{"layer":%q,"target":%q,"cause":%q,"occurrence_ids":%s,"metric":"rejection_rate","expected":"下がる","action":"add","title":"t","done_criteria":"c"}`,
		layer, target, cause, b)
}

func j8Out(props ...string) []byte {
	return []byte(`{"proposals":[` + strings.Join(props, ",") + `]}`)
}

func TestValidateJ8Output_Valid(t *testing.T) {
	t.Parallel()
	got, ok := validateJ8Output(j8Out(
		j8Prop("prompt", " internal/invoker/prompts/j1.md ", "context", "O-1", "O-2"),
		j8Prop("knowledge", "memory/a.md", "permission", "O-3"),
		j8Prop("entry_skill", "run-cycle", "context", "O-4"),
	))
	if !ok || len(got) != 3 {
		t.Fatalf("valid output rejected: ok=%v got=%+v", ok, got)
	}
	if got[0].Target != "internal/invoker/prompts/j1.md" || got[0].Layer != OccurrenceLayerPrompt || got[0].Cause != OccurrenceCauseContext {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[0].Metric != J8MetricRejectionRate || got[0].Action != J8ActionAdd || len(got[0].OccurrenceIDs) != 2 {
		t.Errorf("got[0] = %+v", got[0])
	}
}

func TestValidateJ8Output_EmptyProposalsIsValid(t *testing.T) {
	t.Parallel()
	got, ok := validateJ8Output([]byte(`{"proposals":[]}`))
	if !ok || len(got) != 0 {
		t.Errorf("ok=%v got=%v", ok, got)
	}
}

func TestValidateJ8Output_Invalid(t *testing.T) {
	t.Parallel()
	good := j8Prop("prompt", "a", "context", "O-1")
	replace := func(from, to string) []byte {
		return []byte(strings.Replace(string(j8Out(good)), from, to, 1))
	}
	cases := map[string][]byte{
		"not json":                []byte(`x`),
		"no proposals key":        []byte(`{}`),
		"null proposals":          []byte(`{"proposals":null}`),
		"missing layer":           replace(`"layer":"prompt",`, ``),
		"missing target":          replace(`"target":"a",`, ``),
		"missing cause":           replace(`"cause":"context",`, ``),
		"missing occurrence_ids":  replace(`"occurrence_ids":["O-1"],`, ``),
		"missing metric":          replace(`"metric":"rejection_rate",`, ``),
		"missing expected":        replace(`"expected":"下がる",`, ``),
		"missing action":          replace(`"action":"add",`, ``),
		"missing title":           replace(`"title":"t",`, ``),
		"missing done_criteria":   replace(`,"done_criteria":"c"`, ``),
		"layer out of set":        replace(`"prompt"`, `"x"`),
		"layer none":              replace(`"prompt"`, `"none"`),
		"cause performance":       replace(`"context"`, `"performance"`),
		"cause none":              replace(`"context"`, `"none"`),
		"cause out of set":        replace(`"context"`, `"x"`),
		"metric out of set":       replace(`"rejection_rate"`, `"x"`),
		"action out of set":       replace(`"add"`, `"delete"`),
		"blank title":             replace(`"title":"t"`, `"title":" "`),
		"blank done_criteria":     replace(`"done_criteria":"c"`, `"done_criteria":""`),
		"empty target":            replace(`"target":"a"`, `"target":"  "`),
		"blank expected":          replace(`"expected":"下がる"`, `"expected":" "`),
		"empty occurrence_ids":    replace(`["O-1"]`, `[]`),
		"blank occurrence id":     replace(`["O-1"]`, `[" "]`),
		"same id in one proposal": j8Out(j8Prop("prompt", "a", "context", "O-1", "O-1")),
		"same id in two":          j8Out(j8Prop("prompt", "a", "context", "O-1"), j8Prop("knowledge", "b", "context", "O-1")),
		"same key":                j8Out(j8Prop("prompt", "a", "context", "O-1"), j8Prop("prompt", " a ", "permission", "O-2")),
		"four proposals": j8Out(
			j8Prop("prompt", "a", "context", "O-1"), j8Prop("prompt", "b", "context", "O-2"),
			j8Prop("prompt", "c", "context", "O-3"), j8Prop("prompt", "d", "context", "O-4")),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := validateJ8Output(data); ok {
				t.Errorf("invalid output accepted: %s", data)
			}
		})
	}
}

func TestValidateJ8Output_SameTargetDifferentLayerIsDifferentKey(t *testing.T) {
	t.Parallel()
	if _, ok := validateJ8Output(j8Out(j8Prop("prompt", "a", "context", "O-1"), j8Prop("knowledge", "a", "context", "O-2"))); !ok {
		t.Error("same target in different layers rejected")
	}
}

func TestJ8OutputSchema_HasNoWaitsAndClosedSets(t *testing.T) {
	t.Parallel()
	var schema map[string]any
	if err := json.Unmarshal(j8OutputSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	s := string(j8OutputSchema())
	if strings.Contains(s, "waits") {
		t.Error("schema has waits")
	}
	for _, v := range []string{"connector_skill", "permission", "cost_per_challenge", "reduce"} {
		if !strings.Contains(s, `"`+v+`"`) {
			t.Errorf("schema lacks %q", v)
		}
	}
	for _, v := range []string{`"none"`, `"performance"`} {
		if strings.Contains(s, v) {
			t.Errorf("schema has %s", v)
		}
	}
}

// 検査が空白だけを拒否する文字列の項目は、スキーマでも空を許さない（minLength: 1）。
func TestJ8OutputSchema_NonEmptyStringsMatchValidation(t *testing.T) {
	t.Parallel()
	var schema struct {
		Properties struct {
			Proposals struct {
				Items struct {
					Properties map[string]map[string]any `json:"properties"`
				} `json:"items"`
			} `json:"proposals"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(j8OutputSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	props := schema.Properties.Proposals.Items.Properties
	for _, k := range []string{"target", "expected", "title", "done_criteria"} {
		if got, ok := props[k]["minLength"].(float64); !ok || got != 1 {
			t.Errorf("%s: minLength = %v, want 1", k, props[k]["minLength"])
		}
	}
}

func TestAllJudgmentOutputClosedValues_IncludesJ8(t *testing.T) {
	t.Parallel()
	got := map[string]bool{}
	for _, v := range AllJudgmentOutputClosedValues() {
		got[v] = true
	}
	for _, v := range []string{"rejection_rate", "resume_count", "cost_per_challenge", "approval_count", "add", "reduce"} {
		if !got[v] {
			t.Errorf("missing %q", v)
		}
	}
}

// #571: 任意の見送りの欄（withheld）。正常・欄なし（空を含む）・不正な形の 3 通り。
func TestValidateJ8Withheld_Valid(t *testing.T) {
	t.Parallel()
	got, ok := validateJ8Withheld([]byte(`{"proposals":[],"withheld":[{"layer":"prompt","target":" a.md ","evidence":["C-3 が満たした","a.md の 2 節"]}]}`))
	if !ok || len(got) != 1 || got[0].Layer != OccurrenceLayerPrompt || got[0].Target != "a.md" || len(got[0].Evidence) != 2 {
		t.Fatalf("ok=%v got=%+v", ok, got)
	}
}

func TestValidateJ8Withheld_AbsentOrEmptyIsAccepted(t *testing.T) {
	t.Parallel()
	for _, in := range []string{`{"proposals":[]}`, `{"proposals":[],"withheld":[]}`} {
		got, ok := validateJ8Withheld([]byte(in))
		if !ok || len(got) != 0 {
			t.Errorf("%s: ok=%v got=%v", in, ok, got)
		}
		if _, ok := validateJ8Output([]byte(in)); !ok {
			t.Errorf("%s: proposals rejected", in)
		}
	}
}

func TestValidateJ8Withheld_Invalid(t *testing.T) {
	t.Parallel()
	for name, w := range map[string]string{
		"not an array":     `{}`,
		"missing layer":    `[{"target":"a","evidence":["e"]}]`,
		"layer none":       `[{"layer":"none","target":"a","evidence":["e"]}]`,
		"unknown layer":    `[{"layer":"x","target":"a","evidence":["e"]}]`,
		"blank target":     `[{"layer":"prompt","target":"  ","evidence":["e"]}]`,
		"missing evidence": `[{"layer":"prompt","target":"a"}]`,
		"empty evidence":   `[{"layer":"prompt","target":"a","evidence":[]}]`,
		"blank evidence":   `[{"layer":"prompt","target":"a","evidence":[" "]}]`,
		"too many": `[{"layer":"prompt","target":"a","evidence":["e"]},{"layer":"prompt","target":"b","evidence":["e"]},` +
			`{"layer":"prompt","target":"c","evidence":["e"]},{"layer":"prompt","target":"d","evidence":["e"]}]`,
	} {
		if _, ok := validateJ8Withheld([]byte(`{"proposals":[],"withheld":` + w + `}`)); ok {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestJ8OutputSchema_HasOptionalWithheld(t *testing.T) {
	t.Parallel()
	var sc struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(j8OutputSchema(), &sc); err != nil {
		t.Fatal(err)
	}
	if _, ok := sc.Properties["withheld"]; !ok {
		t.Fatalf("schema lacks withheld: %s", j8OutputSchema())
	}
	for _, r := range sc.Required {
		if r == "withheld" {
			t.Errorf("withheld must be optional")
		}
	}
}
