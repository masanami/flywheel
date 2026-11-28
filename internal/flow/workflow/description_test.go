package workflow

import (
	"strings"
	"testing"
)

// 説明（description）の検査は、文字列であることと長さの上限だけ。
func TestDescription_StringWithinTheLimit(t *testing.T) {
	const body = `schema: harness.workflow/v1
id: described
%s
limits:
  budget_usd: 5
steps:
  wait:
    kind: gate
    type: input
    decider: any
    requested_action: go on
    inputs: [go]
    on:
      go: { done: gone }
`
	for name, tc := range map[string]struct {
		line string
		want string // 空なら通る
	}{
		"無い":       {line: ""},
		"空":        {line: `description: ""`},
		"上限ちょうど":   {line: "description: " + strings.Repeat("あ", MaxDescriptionRunes)},
		"上限を 1 超え": {line: "description: " + strings.Repeat("あ", MaxDescriptionRunes+1), want: "description must be at most 500 characters"},
		"文字列でない":   {line: "description: 12", want: "description must be a string"},
	} {
		t.Run(name, func(t *testing.T) {
			got := ""
			if _, err := Parse("described.yaml", []byte(strings.Replace(body, "%s", tc.line, 1))); err != nil {
				got = err.Error()
			}
			if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
				t.Errorf("problems = %q, want %q", got, tc.want)
			}
		})
	}
}
