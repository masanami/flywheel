package workflow

import (
	"strings"
	"testing"
)

// modelsBase は models を定義し、llm のステップ a が変数を指す、検査を通る定義。
const modelsBase = `schema: harness.workflow/v1
id: w
limits:
  budget_usd: 5
models:
  main: model-main
  light: model-light
steps:
  a:
    kind: llm
    prompt: prompts/p.md
    model: $models.light
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

func modelsMutate(t *testing.T, old, repl string) string {
	t.Helper()
	if n := strings.Count(modelsBase, old); n != 1 {
		t.Fatalf("fragment %q occurs %d times in modelsBase; a mutation must change exactly one place", old, n)
	}
	return strings.Replace(modelsBase, old, repl, 1)
}

// AC-120: models に変数を定義し、llm のステップの model に $models.<名前> を書いた定義は検査を通り、読み取った値を持つ。
func TestModelsDefinitionIsValidAndRead(t *testing.T) {
	path, scripts := setup(t, "w", modelsBase)
	wf, err := LoadAndValidate(path, Options{ScriptsDir: scripts})
	if err != nil {
		t.Fatalf("a definition with models and a variable reference must be valid: %v", err)
	}
	if len(wf.Models) != 2 || wf.Model("main").Value != "model-main" || wf.Model("light").Value != "model-light" {
		t.Errorf("models = %+v", wf.Models)
	}
	if got := wf.Step("a").Model; got != "light" {
		t.Errorf("step a model = %q, want light", got)
	}
	if got := wf.Step("b").Model; got != "" {
		t.Errorf("step b (no model) = %q, want empty", got)
	}
}

// AC-121〜AC-125: 誤りになる書き方。
func TestModelsRejects(t *testing.T) {
	for _, c := range []struct {
		name     string
		old, new string
		want     string
	}{
		{"AC-121 model name written directly", "model: $models.light", "model: claude-sonnet-5-5", "must be a model variable reference"},
		{"AC-121 reference of another root", "model: $models.light", "model: $inputs.light", "must be a model variable reference"},
		{"AC-121 empty variable in the reference", "model: $models.light", "model: $models.", "must be a model variable reference"},
		{"AC-122 undefined variable", "model: $models.light", "model: $models.heavy", "$models.heavy, which models does not define"},
		{"AC-123 model on a command step", "    run: tool\n", "    run: tool\n    model: $models.main\n", "model is only allowed on an llm step"},
		{"AC-124 empty value", "main: model-main", `main: ""`, "models.main must not be an empty string"},
		{"AC-125 integer value", "main: model-main", "main: 5", "models.main must be a string"},
		{"AC-125 list value", "main: model-main", "main: [a]", "models.main must be a string"},
		{"AC-125 null value", "main: model-main", "main:", "models.main must be a string"},
		{"models is not a mapping", "models:\n  main: model-main\n  light: model-light\n", "models: [a]\n", "models must be a mapping"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := check(t, modelsMutate(t, c.old, c.new))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
			if c.name == "AC-123 model on a command step" && strings.Contains(err.Error(), "unknown key") {
				t.Errorf("the model key is reported twice: %v", err)
			}
		})
	}
}

// ResolveModels は定義の models をそのまま写像にする。models を持たない定義は nil。
func TestResolveModels(t *testing.T) {
	path, scripts := setup(t, "w", modelsBase)
	wf, err := LoadAndValidate(path, Options{ScriptsDir: scripts})
	if err != nil {
		t.Fatal(err)
	}
	got := ResolveModels(wf)
	if got["main"] != "model-main" || got["light"] != "model-light" || len(got) != 2 {
		t.Errorf("defaults = %v", got)
	}
	plain, scripts2 := setup(t, "w", base)
	bare, err := LoadAndValidate(plain, Options{ScriptsDir: scripts2})
	if err != nil {
		t.Fatal(err)
	}
	if got := ResolveModels(bare); got != nil {
		t.Errorf("no models: %v", got)
	}
}
