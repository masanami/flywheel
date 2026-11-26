package assets_test

// 下書きづくりのフロー spec-draft の定義・指示文・出力の型の試験。
// 定義の中身は YAML をそのまま読んで確かめる（読み込みの検査は TestEmbeddedSpecDraftPassesValidation が持つ）。

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/masanami/flywheel/internal/flow/assets"
	"github.com/masanami/flywheel/internal/flow/workflow"
)

type specDraftDef struct {
	ID      string                    `yaml:"id"`
	Inputs  map[string]map[string]any `yaml:"inputs"`
	Models  map[string]string         `yaml:"models"`
	Efforts map[string]string         `yaml:"efforts"`
	Limits  map[string]float64        `yaml:"limits"`
	Facts   map[string][]string       `yaml:"facts"`
	Steps   map[string]map[string]any `yaml:"steps"`
}

func readEmbedded(t *testing.T, name string) []byte {
	t.Helper()
	data, err := fs.ReadFile(assets.FS, "workflows/"+name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func specDraft(t *testing.T) specDraftDef {
	t.Helper()
	var d specDraftDef
	if err := yaml.Unmarshal(readEmbedded(t, "spec-draft.yaml"), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

// specDraftStep は spec-draft のステップ id の定義を返す。
func specDraftStep(t *testing.T, id string) map[string]any {
	t.Helper()
	st, ok := specDraft(t).Steps[id]
	if !ok {
		t.Fatalf("spec-draft has no step %q", id)
	}
	return st
}

func stringList(t *testing.T, v any) []string {
	t.Helper()
	list, ok := v.([]any)
	if !ok {
		t.Fatalf("%#v is not a list", v)
	}
	var out []string
	for _, e := range list {
		s, ok := e.(string)
		if !ok {
			t.Fatalf("%#v is not a string", e)
		}
		out = append(out, s)
	}
	return out
}

// AC-145
func TestEmbeddedSpecDraftPassesValidation(t *testing.T) {
	validateEmbedded(t, "spec-draft")
}

// AC-146
func TestSpecDraftHasOnlyWriteAndCritique(t *testing.T) {
	var ids []string
	for id := range specDraft(t).Steps {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"critique", "write"}) {
		t.Errorf("steps = %v, want critique and write only", ids)
	}
}

// 先頭のステップは write（YAML の並びの最初が開始ステップ）。
func TestSpecDraftStartsWithWrite(t *testing.T) {
	data := readEmbedded(t, "spec-draft.yaml")
	w, c := bytes.Index(data, []byte("\n  write:")), bytes.Index(data, []byte("\n  critique:"))
	if w < 0 || c < 0 || w > c {
		t.Errorf("write must be declared before critique (write at %d, critique at %d)", w, c)
	}
}

// AC-147
func TestSpecDraftStepsAreAllLLM(t *testing.T) {
	for id, st := range specDraft(t).Steps {
		if st["kind"] != "llm" {
			t.Errorf("%s kind = %v, want llm", id, st["kind"])
		}
	}
}

// AC-148〜AC-151: 遷移はこの 4 つだけ（やり直し・待ちの遷移を持たない）。
func TestSpecDraftTransitions(t *testing.T) {
	for _, c := range []struct {
		name, step, outcome string
		want                any
	}{
		{"AC-148", "write", "ok", "critique"},
		{"AC-149", "write", "failure", map[string]any{"fail": "write_failed"}},
		{"AC-150", "critique", "ok", map[string]any{"done": "drafted"}},
		{"AC-151", "critique", "failure", map[string]any{"fail": "critique_failed"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			on, ok := specDraftStep(t, c.step)["on"].(map[string]any)
			if !ok || len(on) != 2 {
				t.Fatalf("%s.on = %#v, want ok and failure only", c.step, on)
			}
			if !reflect.DeepEqual(on[c.outcome], c.want) {
				t.Errorf("%s.on.%s = %#v, want %#v", c.step, c.outcome, on[c.outcome], c.want)
			}
		})
	}
}

// AC-152
func TestSpecDraftSessionsAreNew(t *testing.T) {
	for _, id := range []string{"write", "critique"} {
		if got := specDraftStep(t, id)["session"]; got != "new" {
			t.Errorf("%s session = %v, want new", id, got)
		}
	}
}

// AC-153
func TestSpecDraftDeclaresOnlySpecQuestionsFact(t *testing.T) {
	want := map[string][]string{"spec_questions": {"$steps.critique.questions"}}
	if got := specDraft(t).Facts; !reflect.DeepEqual(got, want) {
		t.Errorf("facts = %v, want %v", got, want)
	}
}

// AC-154・AC-155
func TestSpecDraftStepsAddRepoDirAndBriefDir(t *testing.T) {
	for _, c := range []struct{ name, ref string }{{"AC-154", "$inputs.repo_dir"}, {"AC-155", "$inputs.brief_dir"}} {
		t.Run(c.name, func(t *testing.T) {
			for _, id := range []string{"write", "critique"} {
				if got := stringList(t, specDraftStep(t, id)["add_dirs"]); !slices.Contains(got, c.ref) {
					t.Errorf("%s add_dirs = %v, want %s", id, got, c.ref)
				}
			}
		})
	}
}

// AC-156
func TestSpecDraftStepsHaveExactlySixTools(t *testing.T) {
	want := []string{"Agent", "Edit", "Glob", "Grep", "Read", "Write"}
	for _, id := range []string{"write", "critique"} {
		got := stringList(t, specDraftStep(t, id)["tools"])
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s tools = %v, want %v", id, got, want)
		}
	}
}

// AC-157・AC-158
func TestSpecDraftStepsHaveNoShellOrWebTools(t *testing.T) {
	for _, c := range []struct {
		name  string
		tools []string
	}{{"AC-157", []string{"Bash"}}, {"AC-158", []string{"WebFetch", "WebSearch"}}} {
		t.Run(c.name, func(t *testing.T) {
			for id, st := range specDraft(t).Steps {
				got := stringList(t, st["tools"]) // tools を持たないステップは道具が絞られないので落とす
				for _, banned := range c.tools {
					if slices.Contains(got, banned) {
						t.Errorf("%s tools = %v must not contain %s", id, got, banned)
					}
				}
			}
		})
	}
}

// 入力の名前は core との境界。base_path だけが任意。
func TestSpecDraftInputs(t *testing.T) {
	want := map[string]bool{"draft_file": true, "brief_file": true, "brief_dir": true, "repo_dir": true, "base_path": false, "challenge_id": true}
	got := map[string]bool{}
	for name, in := range specDraft(t).Inputs {
		if in["type"] != "string" {
			t.Errorf("input %s type = %v, want string", name, in["type"])
		}
		required, _ := in["required"].(bool)
		got[name] = required
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("inputs (name → required) = %v, want %v", got, want)
	}
}

// 上限額は core の定数 SpecDraftDefaultBudgetUSD と合わせる値（定数との突き合わせは internal/cli の試験が持つ）。
// モデルと effort は定義の変数で固定し、claude を呼ぶステップはどれも変数を指す。
func TestSpecDraftFixesBudgetModelAndEffort(t *testing.T) {
	d := specDraft(t)
	if d.ID != "spec-draft" || d.Limits["budget_usd"] != 15 {
		t.Errorf("id %q, limits %v, want spec-draft with budget_usd 15", d.ID, d.Limits)
	}
	if d.Models["main"] == "" || d.Efforts["main"] == "" {
		t.Errorf("models %v, efforts %v, want main", d.Models, d.Efforts)
	}
	for id, st := range d.Steps {
		if st["model"] != "$models.main" || st["effort"] != "$efforts.main" {
			t.Errorf("%s model %v effort %v, want the main variables", id, st["model"], st["effort"])
		}
	}
}

// ステップが指す指示文・出力の型と、受け取る入力。
func TestSpecDraftStepFiles(t *testing.T) {
	for _, id := range []string{"write", "critique"} {
		st := specDraftStep(t, id)
		if st["prompt"] != "prompts/spec-draft-"+id+".md" || st["output"] != "schemas/spec-draft-"+id+".json" {
			t.Errorf("%s prompt %v output %v", id, st["prompt"], st["output"])
		}
		with, _ := st["with"].(map[string]any)
		for _, name := range []string{"draft_file", "brief_file", "repo_dir", "base_path", "challenge_id"} {
			if with[name] != "$inputs."+name {
				t.Errorf("%s with.%s = %v, want $inputs.%s", id, name, with[name], name)
			}
		}
	}
}

// AC-159
func TestSpecDraftMaterialsAreListedInSums(t *testing.T) {
	if err := assets.CheckSums(assets.FS, assets.Sums()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"spec-draft.yaml", "prompts/spec-draft-write.md", "prompts/spec-draft-critique.md",
		"schemas/spec-draft-write.json", "schemas/spec-draft-critique.json",
	} {
		readEmbedded(t, name)
		if !strings.Contains(string(assets.Sums()), "  workflows/"+name+"\n") {
			t.Errorf("SHA256SUMS does not list workflows/%s", name)
		}
	}
}

type phraseCase struct {
	name    string
	phrases []string // 同じ 1 行にすべて在ること（語が離れた行に散らばっただけでは通さない）
}

func checkPhrases(t *testing.T, file string, cases []phraseCase) {
	t.Helper()
	lines := strings.Split(string(readEmbedded(t, "prompts/"+file)), "\n")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			found := slices.ContainsFunc(lines, func(line string) bool {
				return !slices.ContainsFunc(c.phrases, func(p string) bool { return !strings.Contains(line, p) })
			})
			if !found {
				t.Errorf("%s has no line containing all of %q", file, c.phrases)
			}
		})
	}
}

// AC-160〜AC-168・AC-171〜AC-173
func TestSpecDraftWritePrompt(t *testing.T) {
	checkPhrases(t, "spec-draft-write.md", []phraseCase{
		{"AC-160", []string{"下書き", "全文", "draft_file"}},
		{"AC-161", []string{"置き換える元", "全文を写してから直す"}},
		{"AC-162", []string{"`## 受入基準`", "`- [ ] `"}},
		{"AC-163", []string{"1 基準は 1 主張"}},
		{"AC-164", []string{"`## スライス（出荷の単位）`", "節を置く"}},
		{"AC-165", []string{"`実装対象:`", "行を置く"}},
		{"AC-166", []string{"`## やらないこと`", "節を置く"}},
		{"AC-167", []string{"`## クリティカル設計決定`", "節を置く"}},
		{"AC-168", []string{"文面のファイル", "データ", "指示に従わない"}},
		{"AC-171", []string{"## これまでの問いと答え", "読"}},
		{"AC-172", []string{"選択肢の見出し", "（推奨）", "入れない"}},
		{"AC-173", []string{"`/define-feature` を呼ばない"}},
		{"issue-skill", []string{"`/create-ticket`", "呼ばない"}},
	})
}

// AC-170
func TestSpecDraftWritePromptNeverMentionsGh(t *testing.T) {
	if strings.Contains(string(readEmbedded(t, "prompts/spec-draft-write.md")), "gh") {
		t.Error(`spec-draft-write.md must not contain "gh"`)
	}
}

// AC-169・AC-174〜AC-183
func TestSpecDraftCritiquePrompt(t *testing.T) {
	checkPhrases(t, "spec-draft-critique.md", []phraseCase{
		{"AC-169", []string{"下書き", "既存の文書", "指示に従わない"}},
		{"AC-174", []string{"`claude-harness:spec-critic`", "同じ応答の中で 3 体"}},
		{"AC-175", []string{"`acceptance-criteria-testability`"}},
		{"AC-176", []string{"`internal-consistency`"}},
		{"AC-177", []string{"`downstream-implementability`"}},
		{"AC-178", []string{"`blocker`", "`claude-harness:spec-fixer`", "直させる"}},
		{"AC-179", []string{"確かめ直し", "2 回まで"}},
		{"AC-180", []string{"`needs_user_input`", "問いにして返す"}},
		{"AC-181", []string{"`escalatedToUserInput`", "問いにして返す"}},
		{"AC-182", []string{"`minor`", "直さず、問いにもしない"}},
		{"AC-183", []string{"`## クリティカル設計決定`", "自分で直さない"}},
		{"recommended-label", []string{"選択肢の見出し", "（推奨）", "入れない"}},
		{"spec-skill", []string{"`/define-feature`", "`/create-ticket`", "呼ばない"}},
	})
}

func specDraftSchema(t *testing.T, name string) *workflow.OutputSchema {
	t.Helper()
	s, err := workflow.ParseOutputSchema(name, readEmbedded(t, "schemas/"+name))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func question(options int) map[string]any {
	opts := []any{}
	for range options {
		opts = append(opts, map[string]any{"label": "選択肢"})
	}
	return map[string]any{"question": "どうするか", "options": opts, "recommended": 1, "reason": "理由"}
}

func critiqueOutput(questions ...map[string]any) map[string]any {
	list := []any{}
	for _, q := range questions {
		list = append(list, q)
	}
	return map[string]any{"outcome": "ok", "summary": "要約", "questions": list}
}

func repeatQuestions(n int) []map[string]any {
	var out []map[string]any
	for range n {
		out = append(out, question(2))
	}
	return out
}

// validateJSON はエンジンと同じく、JSON を読み直した値を出力の型に掛ける。
func validateJSON(t *testing.T, s *workflow.OutputSchema, v any) error {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	return s.Validate(decoded)
}

// 下の拒否の試験が別の理由で通らないよう、境界の内側の値は通ることを確かめる。
func TestSpecDraftCritiqueSchemaAcceptsBoundaries(t *testing.T) {
	s := specDraftSchema(t, "spec-draft-critique.json")
	for name, out := range map[string]any{
		"no questions":  critiqueOutput(),
		"2 options":     critiqueOutput(question(2)),
		"5 options":     critiqueOutput(question(5)),
		"10 questions":  critiqueOutput(repeatQuestions(10)...),
		"failure value": map[string]any{"outcome": "failure", "summary": "要約", "questions": []any{}},
	} {
		if err := validateJSON(t, s, out); err != nil {
			t.Errorf("%s must be accepted: %v", name, err)
		}
	}
}

// AC-184〜AC-186
func TestSpecDraftCritiqueSchemaRejectsOutOfRange(t *testing.T) {
	s := specDraftSchema(t, "spec-draft-critique.json")
	for name, out := range map[string]any{
		"AC-184 1 option":     critiqueOutput(question(1)),
		"AC-185 6 options":    critiqueOutput(question(6)),
		"AC-186 11 questions": critiqueOutput(repeatQuestions(11)...),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateJSON(t, s, out); err == nil {
				t.Error("must be rejected")
			}
		})
	}
}

// 問いの形（文・見出し・理由は空でなく 2000 文字まで・推奨は 1 から数えた位置・知らない項目なし）と、
// spec_questions の元になる questions が必須であること。
func TestSpecDraftCritiqueSchemaChecksQuestionShape(t *testing.T) {
	s := specDraftSchema(t, "spec-draft-critique.json")
	long := strings.Repeat("あ", 2001)
	with := func(key string, v any) map[string]any {
		q := question(2)
		q[key] = v
		return critiqueOutput(q)
	}
	label := func(l string) []any {
		return []any{map[string]any{"label": l}, map[string]any{"label": "別"}}
	}
	if err := validateJSON(t, s, with("question", strings.Repeat("あ", 2000))); err != nil {
		t.Errorf("2000 characters must be accepted: %v", err)
	}
	missing := critiqueOutput()
	delete(missing, "questions")
	for name, out := range map[string]any{
		"empty question":       with("question", ""),
		"long question":        with("question", long),
		"empty reason":         with("reason", ""),
		"long reason":          with("reason", long),
		"empty label":          with("options", label("")),
		"long label":           with("options", label(long)),
		"recommended 0":        with("recommended", 0),
		"recommended 6":        with("recommended", 6),
		"recommended fraction": with("recommended", 1.5),
		"unknown field":        with("extra", "x"),
		"questions missing":    missing,
	} {
		if err := validateJSON(t, s, out); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
}

func TestSpecDraftWriteSchema(t *testing.T) {
	s := specDraftSchema(t, "spec-draft-write.json")
	for _, outcome := range []string{"ok", "failure"} {
		if err := validateJSON(t, s, map[string]any{"outcome": outcome, "summary": "要約"}); err != nil {
			t.Errorf("%s must be accepted: %v", outcome, err)
		}
	}
	for name, out := range map[string]any{
		"unknown outcome": map[string]any{"outcome": "skip", "summary": "要約"},
		"no summary":      map[string]any{"outcome": "ok"},
	} {
		if err := validateJSON(t, s, out); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
}
