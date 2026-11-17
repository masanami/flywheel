package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateJ9Output(t *testing.T) {
	t.Parallel()
	ok := map[string]J9Verdict{
		`{"verdict":"strong","reason":"r"}`:    J9VerdictStrong,
		`{"verdict":"weak","reason":" r "}`:    J9VerdictWeak,
		`{"verdict":"uncertain","reason":"r"}`: J9VerdictUncertain,
	}
	for in, want := range ok {
		v, r, good := validateJ9Output([]byte(in))
		if !good || v != want || r != strings.TrimSpace(r) || r != "r" {
			t.Errorf("validateJ9Output(%s) = %v %q %v", in, v, r, good)
		}
	}
	for _, bad := range []string{``, `[]`, `{}`, `{"verdict":"weak"}`, `{"reason":"r"}`, `{"verdict":"maybe","reason":"r"}`, `{"verdict":"weak","reason":""}`, `{"verdict":null,"reason":"r"}`} {
		if _, _, good := validateJ9Output([]byte(bad)); good {
			t.Errorf("validateJ9Output(%s) = ok, want invalid", bad)
		}
	}
	var schema map[string]any
	if err := json.Unmarshal(j9OutputSchema(), &schema); err != nil || schema["additionalProperties"] != false {
		t.Errorf("schema = %s, err %v", j9OutputSchema(), err)
	}
}

func TestJ9_ConfigDeclarations(t *testing.T) {
	t.Parallel()
	conn, err := parseConnectorsDeclaration([]byte(strings.Replace(planConnectorsJSON, `"name": "multi",`, `"name": "multi", "serial_judgment": true,`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range conn.Repos {
		if r.SerialJudgment != (r.Name == "multi") {
			t.Errorf("repo %s SerialJudgment = %v", r.Name, r.SerialJudgment)
		}
	}
	for _, bad := range []string{`null`, `"yes"`, `1`} {
		if _, err := parseConnectorsDeclaration([]byte(strings.Replace(planConnectorsJSON, `"name": "multi",`, `"name": "multi", "serial_judgment": `+bad+`,`, 1))); err == nil {
			t.Errorf("serial_judgment %s was accepted", bad)
		}
	}
	d := defaultAgentDeclaration()
	if d.JudgmentBudgetFor(JudgmentJ9) != 0.5 || d.SerialJudgmentCycleBudgetUSD != 3 {
		t.Errorf("defaults = %v / %v, want 0.5 / 3", d.JudgmentBudgetFor(JudgmentJ9), d.SerialJudgmentCycleBudgetUSD)
	}
	for _, bad := range []string{`{"judgment_budget_usd": {"J9": 0}}`, `{"serial_judgment_cycle_budget_usd": 0}`, `{"serial_judgment_cycle_budget_usd": -1}`} {
		if _, err := LoadAgentDeclaration(writeAgentJSON(t, bad)); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}
