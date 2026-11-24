package workflow

import (
	"strings"
	"testing"
)

// effortsBase は efforts を定義し、llm のステップ a が変数を指す、検査を通る定義。
const effortsBase = `schema: harness.workflow/v1
id: w
limits:
  budget_usd: 5
efforts:
  main: high
  light: low
steps:
  a:
    kind: llm
    prompt: prompts/p.md
    effort: $efforts.light
    output: schemas/out.json
    budget_usd: 1
    on:
      ok: b
      bad: { fail: bad }
  b:
    kind: command
    run: tool
    output: schemas/out.json
    on:
      ok: { done: ok }
      bad: { fail: bad }
`

func effortsMutate(t *testing.T, old, repl string) string {
	t.Helper()
	if n := strings.Count(effortsBase, old); n != 1 {
		t.Fatalf("fragment %q occurs %d times in effortsBase; a mutation must change exactly one place", old, n)
	}
	return strings.Replace(effortsBase, old, repl, 1)
}

// AC-86: efforts に変数を定義し、llm のステップの effort に $efforts.<名前> を書いた定義は検査を通り、読み取った値を持つ。
func TestEffortsDefinitionIsValidAndRead(t *testing.T) {
	path, scripts := setup(t, "w", effortsBase)
	wf, err := LoadAndValidate(path, Options{ScriptsDir: scripts})
	if err != nil {
		t.Fatalf("a definition with efforts and a variable reference must be valid: %v", err)
	}
	if len(wf.Efforts) != 2 || wf.Effort("main").Value != "high" || wf.Effort("light").Value != "low" {
		t.Errorf("efforts = %+v", wf.Efforts)
	}
	if got := wf.Step("a").Effort; got != "light" {
		t.Errorf("step a effort = %q, want light", got)
	}
	if got := wf.Step("b").Effort; got != "" {
		t.Errorf("step b (no effort) = %q, want empty", got)
	}
	if got := ResolveEfforts(wf); len(got) != 2 || got["main"] != "high" || got["light"] != "low" {
		t.Errorf("ResolveEfforts = %v", got)
	}
}

// 5 つの値はどれも使える。
func TestEffortsAcceptsEveryValue(t *testing.T) {
	for _, v := range EffortValues {
		if err := check(t, effortsMutate(t, "main: high", "main: "+v)); err != nil {
			t.Errorf("effort %q rejected: %v", v, err)
		}
	}
}

// efforts を持たない定義は nil。
func TestResolveEffortsWithoutEfforts(t *testing.T) {
	plain, scripts := setup(t, "w", base)
	bare, err := LoadAndValidate(plain, Options{ScriptsDir: scripts})
	if err != nil {
		t.Fatal(err)
	}
	if got := ResolveEfforts(bare); got != nil {
		t.Errorf("no efforts: %v", got)
	}
}

// AC-87〜AC-91: 誤りになる書き方。
func TestEffortsRejects(t *testing.T) {
	for _, c := range []struct {
		name     string
		old, new string
		want     string
	}{
		{"AC-87 value written directly", "effort: $efforts.light", "effort: low", "must be an effort variable reference"},
		{"AC-87 reference of another root", "effort: $efforts.light", "effort: $models.light", "must be an effort variable reference"},
		{"AC-87 empty variable in the reference", "effort: $efforts.light", "effort: $efforts.", "must be an effort variable reference"},
		{"AC-88 undefined variable", "effort: $efforts.light", "effort: $efforts.heavy", "$efforts.heavy, which efforts does not define"},
		{"AC-89 effort on a command step", "    run: tool\n", "    run: tool\n    effort: $efforts.main\n", "effort is only allowed on an llm step"},
		{"AC-90 value outside the five", "main: high", "main: extreme", "efforts.main must be one of low, medium, high, xhigh, max"},
		{"AC-90 empty value", "main: high", `main: ""`, "efforts.main must be one of"},
		{"AC-91 integer value", "main: high", "main: 5", "efforts.main must be a string"},
		{"AC-91 list value", "main: high", "main: [high]", "efforts.main must be a string"},
		{"AC-91 null value", "main: high", "main:", "efforts.main must be a string"},
		{"efforts is not a mapping", "efforts:\n  main: high\n  light: low\n", "efforts: [a]\n", "efforts must be a mapping"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := check(t, effortsMutate(t, c.old, c.new))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
			if strings.Contains(c.name, "command step") && strings.Contains(err.Error(), "unknown key") {
				t.Errorf("the effort key is reported twice: %v", err)
			}
		})
	}
}
