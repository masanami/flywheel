package workflow

import (
	"strings"
	"testing"
)

// llm のステップの任意のキー tools（使える道具の一覧）の読み込みと検査。

// withLLMStep は base の末尾に、tools の行 line を持つ llm のステップ c を足し、a の bad から c へ遷移させる。
func withLLMStep(t *testing.T, line string) string {
	t.Helper()
	return mutate(t, "      bad: { fail: bad }\n", "      bad: c\n") + `  c:
    kind: llm
    prompt: prompts/p.md
    output: schemas/out.json
    budget_usd: 1
` + line + `    on:
      ok: { done: ok }
      bad: { fail: bad }
`
}

// AC-128
func TestToolsAreLoadedOnAnLLMStep(t *testing.T) {
	path, scripts := setup(t, "w", withLLMStep(t, "    tools: [Read, Edit]\n"))
	wf, err := LoadAndValidate(path, Options{ScriptsDir: scripts})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(wf.Step("c").Tools, ","); got != "Read,Edit" {
		t.Fatalf("tools = %q", got)
	}
}

func TestToolsAreOptional(t *testing.T) {
	path, scripts := setup(t, "w", withLLMStep(t, ""))
	wf, err := LoadAndValidate(path, Options{ScriptsDir: scripts})
	if err != nil {
		t.Fatal(err)
	}
	if wf.Step("c").Tools != nil {
		t.Fatalf("tools = %v, want none", wf.Step("c").Tools)
	}
}

// AC-129 と、起動の引数が崩れる値の拒否。
func TestToolsAreRejected(t *testing.T) {
	cases := []struct{ name, line, want string }{
		{"AC-129 an empty list", "    tools: []\n", "tools must be a non-empty list"},
		{"AC-129 no value", "    tools:\n", "tools must be a non-empty list"},
		{"a scalar", "    tools: Read\n", "tools must be a non-empty list"},
		{"a name with a comma", "    tools: [\"Read,Bash\"]\n", "must match"},
		{"an empty name", "    tools: [\"\"]\n", "must match"},
		{"a name that looks like a flag", "    tools: [--foo]\n", "must match"},
		{"a non-string", "    tools: [1]\n", "must be a string"},
		{"a duplicate", "    tools: [Read, Read]\n", "duplicate value"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := check(t, withLLMStep(t, c.line))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

// AC-130: llm でない種類のステップに tools は書けない。
func TestToolsAreRejectedOutsideLLMSteps(t *testing.T) {
	for _, c := range []struct{ kind, yaml string }{
		{"command", mutate(t, "    run: tool\n    with: { n: $inputs.n }\n", "    run: tool\n    tools: [Read]\n    with: { n: $inputs.n }\n")},
		{"gate", mutate(t, "      bad: { fail: bad }\n", "      bad: g\n") + `  g:
    kind: gate
    type: input
    decider: any
    requested_action: decide
    inputs: [go]
    tools: [Read]
    on:
      go: { done: ok }
`},
		{"select", mutate(t, "      bad: { fail: bad }\n", "      bad: s\n") + `  s:
    kind: select
    value: $steps.a.outcome
    tools: [Read]
    on:
      ok: { done: ok }
      bad: { fail: bad }
`},
	} {
		t.Run(c.kind, func(t *testing.T) {
			err := check(t, c.yaml)
			if err == nil || !strings.Contains(err.Error(), `unknown key "tools"`) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
