package core

import (
	"encoding/json"
	"strings"
	"testing"
)

const j6ValidOutput = `{
  "classifications": [
    {"occurrence_id": "O-1", "cause": "context", "layer": "prompt", "target": "internal/invoker/prompts/j1.md", "signal": "前提が渡っていない"},
    {"occurrence_id": "O-2", "cause": "none", "layer": "none", "target": "", "signal": "良い結果"}
  ],
  "added": [
    {"outcome": "good", "kind": "good_pattern", "evidence": "run:3", "cause": "none", "layer": "none", "target": "", "signal": "手順が効いた"}
  ],
  "memory": [
    {"mode": "create", "type": "experience", "slug": "s1", "description": "d", "confidence": "high", "sources": ["run:3"], "outcome": "good", "target": "t", "signal": "s", "body": "b"},
    {"mode": "append", "type": "tacit", "slug": "s2", "body": "b2"}
  ],
  "followups": [{"title": "t", "description": "d", "done_criteria": "c"}]
}`

func TestValidateJ6Output_Valid(t *testing.T) {
	got, reason := validateJ6Output([]byte(j6ValidOutput))
	if reason != "" {
		t.Fatalf("valid output rejected: %s", reason)
	}
	if len(got.Classifications) != 2 || got.Classifications[0].Cause != OccurrenceCauseContext || got.Classifications[0].Layer != OccurrenceLayerPrompt {
		t.Errorf("classifications = %+v", got.Classifications)
	}
	if len(got.Added) != 1 || got.Added[0].Outcome != OccurrenceOutcomeGood || got.Added[0].Evidence != "run:3" {
		t.Errorf("added = %+v", got.Added)
	}
	if len(got.Memory) != 2 || got.Memory[0].Mode != MemoryModeCreate || got.Memory[0].Sources[0] != "run:3" || got.Memory[1].Mode != MemoryModeAppend {
		t.Errorf("memory = %+v", got.Memory)
	}
	if len(got.Followups) != 1 || got.Followups[0].DoneCriteria != "c" {
		t.Errorf("followups = %+v", got.Followups)
	}
}

func TestValidateJ6Output_EmptyArraysAreValid(t *testing.T) {
	if _, reason := validateJ6Output([]byte(`{"classifications":[],"added":[],"memory":[],"followups":[]}`)); reason != "" {
		t.Error("empty arrays rejected")
	}
}

// 経験の記憶の追記は outcome・target・signal を要らない（作成だけが必須）。
func TestValidateJ6Output_ExperienceAppendNeedsNoExtras(t *testing.T) {
	in := `{"classifications":[],"added":[],"memory":[{"mode":"append","type":"experience","slug":"s","body":"b"}],"followups":[]}`
	got, reason := validateJ6Output([]byte(in))
	if reason != "" {
		t.Fatalf("experience append rejected: %s", reason)
	}
	if len(got.Memory) != 1 || got.Memory[0].Mode != MemoryModeAppend {
		t.Errorf("memory = %+v", got.Memory)
	}
}

func TestValidateJ6Output_ReasonNamesTheFailedCheck(t *testing.T) {
	in := `{"classifications":[],"added":[],"memory":[{"mode":"create","type":"experience","slug":"s","body":"b"}],"followups":[]}`
	if _, reason := validateJ6Output([]byte(in)); !strings.Contains(reason, "memory[0]") || !strings.Contains(reason, "outcome") {
		t.Errorf("reason = %q", reason)
	}
}

func TestValidateJ6Output_Invalid(t *testing.T) {
	cls := func(id, cause, layer string) string {
		return `{"occurrence_id":"` + id + `","cause":"` + cause + `","layer":"` + layer + `","target":"","signal":"s"}`
	}
	wrap := func(c, a, m, f string) string {
		return `{"classifications":[` + c + `],"added":[` + a + `],"memory":[` + m + `],"followups":[` + f + `]}`
	}
	goodAdded := `{"outcome":"bad","kind":"k","evidence":"e","cause":"context","layer":"prompt","target":"t","signal":"s"}`
	goodMem := `{"mode":"create","type":"tacit","slug":"s","body":"b"}`
	// 土台そのものが受理されること（以降のケースは 1 か所だけ壊す）。
	if _, reason := validateJ6Output([]byte(wrap(cls("O-1", "context", "prompt"), goodAdded, goodMem, `{"title":"t","description":"d","done_criteria":"c"}`))); reason != "" {
		t.Fatal("base fixture must be accepted")
	}
	cases := map[string]string{
		"missing added":             `{"classifications":[],"memory":[],"followups":[]}`,
		"class missing signal":      wrap(`{"occurrence_id":"O-1","cause":"none","layer":"none","target":""}`, "", "", ""),
		"class missing cause":       wrap(`{"occurrence_id":"O-1","layer":"none","target":"","signal":"s"}`, "", "", ""),
		"class missing layer":       wrap(`{"occurrence_id":"O-1","cause":"none","target":"","signal":"s"}`, "", "", ""),
		"added missing kind":        wrap("", strings.Replace(goodAdded, `"kind":"k",`, "", 1), "", ""),
		"added missing target":      wrap("", strings.Replace(goodAdded, `"target":"t",`, "", 1), "", ""),
		"added missing signal":      wrap("", strings.Replace(goodAdded, `,"signal":"s"`, "", 1), "", ""),
		"added missing layer":       wrap("", strings.Replace(goodAdded, `"layer":"prompt",`, "", 1), "", ""),
		"memory missing mode":       wrap("", "", `{"type":"tacit","slug":"s","body":"b"}`, ""),
		"memory missing type":       wrap("", "", `{"mode":"create","slug":"s","body":"b"}`, ""),
		"memory missing slug":       wrap("", "", `{"mode":"create","type":"tacit","body":"b"}`, ""),
		"experience create lacking": wrap("", "", `{"mode":"create","type":"experience","slug":"s","description":"d","confidence":"high","body":"b"}`, ""),
		"followup missing desc":     wrap("", "", "", `{"title":"t","done_criteria":"c"}`),
		"followup missing done":     wrap("", "", "", `{"title":"t","description":"d"}`),
		"not json":                  `nope`,
		"missing classifications":   `{"added":[],"memory":[],"followups":[]}`,
		"missing followups":         `{"classifications":[],"added":[],"memory":[]}`,
		"null memory":               `{"classifications":[],"added":[],"memory":null,"followups":[]}`,
		"unknown cause":             wrap(cls("O-1", "bogus", "prompt"), "", "", ""),
		"unknown layer":             wrap(cls("O-1", "context", "bogus"), "", "", ""),
		"blank occurrence id":       wrap(cls(" ", "context", "prompt"), "", "", ""),
		"duplicate occurrence id":   wrap(cls("O-1", "context", "prompt")+","+cls("O-1", "none", "none"), "", "", ""),
		"missing target":            wrap(`{"occurrence_id":"O-1","cause":"none","layer":"none","signal":"s"}`, "", "", ""),
		"added bad outcome":         wrap("", strings.Replace(goodAdded, `"bad"`, `"meh"`, 1), "", ""),
		"added missing evidence":    wrap("", strings.Replace(goodAdded, `"evidence":"e",`, "", 1), "", ""),
		"added unknown cause":       wrap("", strings.Replace(goodAdded, `"context"`, `"x"`, 1), "", ""),
		"memory overwrite mode":     wrap("", "", strings.Replace(goodMem, `"create"`, `"overwrite"`, 1), ""),
		"memory delete mode":        wrap("", "", strings.Replace(goodMem, `"create"`, `"delete"`, 1), ""),
		"memory unknown type":       wrap("", "", strings.Replace(goodMem, `"tacit"`, `"note"`, 1), ""),
		"memory missing body":       wrap("", "", `{"mode":"create","type":"tacit","slug":"s"}`, ""),
		"experience missing extra":  wrap("", "", `{"mode":"create","type":"experience","slug":"s","body":"b","outcome":"good","target":"t"}`, ""),
		"followup missing title":    wrap("", "", "", `{"description":"d","done_criteria":"c"}`),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, reason := validateJ6Output([]byte(in)); reason == "" {
				t.Errorf("accepted: %s", in)
			}
		})
	}
}

func TestJ6OutputSchema_ClosedSetsMatchDefinitions(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(j6OutputSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	props := schema["properties"].(map[string]any)
	enumOf := func(arr, field string) []string {
		item := props[arr].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)[field].(map[string]any)
		var out []string
		for _, v := range item["enum"].([]any) {
			out = append(out, v.(string))
		}
		return out
	}
	eq := func(name string, got, want []string) {
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s enum = %v, want %v", name, got, want)
		}
	}
	eq("cause", enumOf("classifications", "cause"), []string{"context", "permission", "performance", "none"})
	eq("layer", enumOf("classifications", "layer"), []string{"prompt", "connector_skill", "entry_skill", "knowledge", "none"})
	eq("added cause", enumOf("added", "cause"), []string{"context", "permission", "performance", "none"})
	eq("added layer", enumOf("added", "layer"), []string{"prompt", "connector_skill", "entry_skill", "knowledge", "none"})
	eq("outcome", enumOf("added", "outcome"), []string{"good", "bad"})
	eq("mode", enumOf("memory", "mode"), []string{"create", "append"})
	eq("type", enumOf("memory", "type"), []string{"map", "tacit", "experience", "reference"})
	if len(schema["required"].([]any)) != 4 {
		t.Errorf("required = %v", schema["required"])
	}
}

func TestJ6_BudgetDefaultAndPoint(t *testing.T) {
	if !JudgmentJ6.valid() {
		t.Error("J6 is not a valid judgment point")
	}
	if defaultJudgmentBudgetUSD.J6 != 2 {
		t.Errorf("default J6 budget = %v, want 2", defaultJudgmentBudgetUSD.J6)
	}
	d := &AgentDeclaration{JudgmentBudgetUSD: JudgmentBudgetUSD{J6: 7}}
	if d.JudgmentBudgetFor(JudgmentJ6) != 7 {
		t.Error("JudgmentBudgetFor(J6) wrong")
	}
}

func TestAllJudgmentOutputClosedValues_IncludesJ6(t *testing.T) {
	have := map[string]bool{}
	for _, v := range AllJudgmentOutputClosedValues() {
		have[v] = true
	}
	for _, w := range []string{"context", "permission", "performance", "none", "prompt", "connector_skill", "entry_skill", "knowledge", "create", "append", "map", "tacit", "experience", "reference"} {
		if !have[w] {
			t.Errorf("closed values lack %q", w)
		}
	}
}
