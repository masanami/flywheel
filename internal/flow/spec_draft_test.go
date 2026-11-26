package flow

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/masanami/flywheel/internal/flow/assets"
)

// フローを進める仕組みの、下書きづくりのフローが使う分の試験: facts の配列・上限額の引数・上限額が足りないときの結果・
// Git の作業ツリーでないディレクトリからの開始。定義は名前だけを spec-draft にした試験用の代わりを差し込む
// （確かめるのは仕組みで、定義の中身には依らない）。assets.FS を差し替えるので、試験は並列にしない。

const stubSpecDraft = `schema: harness.workflow/v1
id: spec-draft
inputs:
  challenge_id: { type: string, required: true }
limits:
  budget_usd: 12
facts:
  spec_questions: [$steps.critique.questions]
steps:
  write:
    kind: llm
    prompt: prompts/stub-spec-draft.md
    output: schemas/stub-spec-write.json
    budget_usd: 12
    tools: [Read, Write]
    on:
      ok: critique
  critique:
    kind: llm
    prompt: prompts/stub-spec-draft.md
    output: schemas/stub-spec-critique.json
    budget_usd: 12
    tools: [Read]
    on:
      ok: { done: drafted }
`

func useStubSpecDraft(t *testing.T) {
	t.Helper()
	orig := assets.FS
	overlay := fstest.MapFS{}
	names, err := assets.Files(orig)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		data, err := fs.ReadFile(orig, n)
		if err != nil {
			t.Fatal(err)
		}
		overlay[n] = &fstest.MapFile{Data: data}
	}
	for n, body := range map[string]string{
		"workflows/spec-draft.yaml":                 stubSpecDraft,
		"workflows/prompts/stub-spec-draft.md":      "# stub\n",
		"workflows/schemas/stub-spec-write.json":    `{"type":"object","required":["outcome"],"properties":{"outcome":{"type":"string","enum":["ok"]}}}`,
		"workflows/schemas/stub-spec-critique.json": `{"type":"object","required":["outcome","questions"],"properties":{"outcome":{"type":"string","enum":["ok"]},"questions":{"type":"array","items":{"type":"object"}}}}`,
	} {
		overlay[n] = &fstest.MapFile{Data: []byte(body)}
	}
	assets.FS = overlay
	t.Cleanup(func() { assets.FS = orig })
}

type specFixture struct {
	*fixture
	dir string // Git の作業ツリーでない、開始のディレクトリ（<ws>/.flywheel/specs）
}

// newSpecFixture は偽の claude が write → critique（問いは questions をそのまま返す）を答える準備。
func newSpecFixture(t *testing.T, questions string) *specFixture {
	t.Helper()
	f := &specFixture{fixture: newFixture(t, "5.3.0")}
	useStubSpecDraft(t)
	f.dir = filepath.Join(f.ws, ".flywheel", "specs")
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", f.root)
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = f.dir
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("%s must not be inside a git worktree: %s", f.dir, out)
	}
	f.respond(1, claudeStructured(map[string]any{"outcome": "ok"}, 1))
	// 問いは書いた順のキーのまま返させる（map にすると並べ替わる）。
	f.respond(2, strings.Replace(claudeStructured(map[string]any{"outcome": "ok", "questions": "@Q@"}, 1), `"@Q@"`, questions, 1))
	return f
}

// specCall は開始のディレクトリを Git の外にして flow のコマンドを呼び、契約の出力を返す。
func (f *specFixture) specCall(call func(Env) int) contractDoc {
	f.t.Helper()
	env, out, errb := f.env()
	env.Getwd = func() (string, error) { return f.dir, nil }
	if code := call(env); code != ExitOK {
		f.t.Fatalf("exit %d\n%s%s", code, out, errb)
	}
	return decodeDoc(f.t, out.String())
}

// startSpec は spec-draft を始め、start が成功したことを確かめて実行の識別を返す。
func (f *specFixture) startSpec(budget float64) string {
	f.t.Helper()
	d := f.specCall(func(env Env) int {
		return Start(env, StartOptions{Name: SpecDraftFlow, Inputs: []string{"challenge_id=C-1"}, PermissionMode: "auto", DelegatedRun: "R-1", BudgetUSD: budget})
	})
	if d.RunID == nil || d.State == "failed" && budget == 0 {
		f.t.Fatalf("start doc %+v", d)
	}
	return *d.RunID
}

// finished は実行が state で終わり runner が終わるのを待ち、status の契約の出力を返す。
func (f *specFixture) finished(id, state string) contractDoc {
	f.t.Helper()
	f.waitState(id, state)
	f.waitRunnerExit(id)
	return f.specCall(func(env Env) int { return Status(env, id) })
}

// AC-124・AC-136・AC-137: Git の外から始めた実行が成功し、配列の欄は JSON の文字列で facts に載る。
func TestSpecDraftOutsideGitStartsAndReportsArrayFacts(t *testing.T) {
	f := newSpecFixture(t, `[{"a":1},{"a":2}]`)
	id := f.startSpec(0)
	d := f.finished(id, "succeeded")
	if d.State != "succeeded" || len(d.Facts) != 1 || d.Facts["spec_questions"] != `[{"a":1},{"a":2}]` {
		t.Fatalf("contract = state %s facts %v (%s)", d.State, d.Facts, d.Summary)
	}
	if st := f.load(id); st.Cwd != f.dir {
		t.Errorf("cwd = %s, want %s", st.Cwd, f.dir)
	}
	if argv := f.claudeArgv(1); !hasPair(argv, "--tools", "Read,Write") {
		t.Errorf("first launch argv = %v", argv)
	}
}

// AC-125
func TestEmptyArrayFactIsAnEmptyJSONArray(t *testing.T) {
	f := newSpecFixture(t, `[]`)
	d := f.finished(f.startSpec(0), "succeeded")
	if v, ok := d.Facts["spec_questions"]; !ok || v != "[]" {
		t.Fatalf("facts = %v (%s)", d.Facts, d.Summary)
	}
}

// 出力の型が配列だけを許す欄を null・文字列で返すと、型の検査で段が失敗し、契約の出力の facts にその名前が無い。
// この試験は配列の畳み込みを通らない。AC-126・AC-127 の畳み込みは internal/flow/runstate の
// TestArrayFactsHaveNoValueUnlessTheFieldIsAnArray が確かめる。
func TestSchemaRejectedNonArrayAnswerLeavesNoFactInTheContract(t *testing.T) {
	for name, questions := range map[string]string{"null": `null`, "string": `"[]"`} {
		t.Run(name, func(t *testing.T) {
			f := newSpecFixture(t, questions)
			d := f.finished(f.startSpec(0), "failed")
			if _, ok := d.Facts["spec_questions"]; ok {
				t.Fatalf("facts = %v", d.Facts)
			}
		})
	}
}

// AC-139・AC-140: 記録の上限額は、渡せばその額、渡さなければ定義の値。
func TestSpecDraftBudgetIsRecorded(t *testing.T) {
	for _, c := range []struct {
		name         string
		budget, want float64
	}{{"AC-139 given", 8, 8}, {"AC-140 not given", 0, 12}} {
		t.Run(c.name, func(t *testing.T) {
			f := newSpecFixture(t, `[]`)
			id := f.startSpec(c.budget)
			f.finished(id, "succeeded")
			st := f.load(id)
			if st.Limits["budget_usd"] != c.want {
				t.Errorf("limits = %v, want budget_usd %v", st.Limits, c.want)
			}
			if b := st.Unit(mainUnit).Budget; b == nil || b.LimitUSD != c.want {
				t.Errorf("unit budget = %+v, want limit %v", b, c.want)
			}
			// 起動に付与する上限も、記録の上限額を超えない。
			if argv := f.claudeArgv(1); !hasPair(argv, "--max-budget-usd", strconv.FormatFloat(c.want, 'f', -1, 64)) {
				t.Errorf("argv = %v, want --max-budget-usd %v", argv, c.want)
			}
		})
	}
}

// AC-135: 上限額が足りずに起動しなかったステップが、契約の出力の steps の最後の要素で、結果は budget_exhausted。
func TestContractLastStepIsBudgetExhaustedWhenAStepIsNotLaunched(t *testing.T) {
	for _, c := range []struct {
		name, step string
		budget     float64
		launches   string
	}{
		{"the first step", "write", 0.1, ""},
		{"after one step spent the budget", "critique", 1.1, "1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newSpecFixture(t, `[]`)
			d := f.finished(f.startSpec(c.budget), "failed")
			if d.Steps == nil || len(*d.Steps) == 0 {
				t.Fatalf("no steps (%s)", d.Summary)
			}
			last := (*d.Steps)[len(*d.Steps)-1]
			if last.Step != c.step || last.Outcome == nil || *last.Outcome != "budget_exhausted" {
				t.Fatalf("last step = %+v", last)
			}
			n, _ := os.ReadFile(filepath.Join(f.fdir, "count"))
			if got := strings.TrimSpace(string(n)); got != c.launches {
				t.Errorf("claude launches = %q, want %q", got, c.launches)
			}
		})
	}
}

// AC-138: Git の外から始めた実行を、取り消せる。
func TestSpecDraftOutsideGitCanBeCancelled(t *testing.T) {
	f := newSpecFixture(t, `[]`)
	writeFile(t, filepath.Join(f.fdir, "responses", "1.sleep"), "", 0o644)
	id := f.startSpec(0)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(pidsFile(f.fixture)); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("claude was not launched")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if d := f.specCall(func(env Env) int { return Cancel(env, id) }); d.State != "cancelled" {
		t.Fatalf("cancel doc %+v", d)
	}
	f.waitRunnerExit(id)
}

// AC-134: 文字列の欄だけを facts に宣言する ticket の契約の出力と記録は、変わらない。
func TestTicketStringFactsAreUnchanged(t *testing.T) {
	f := newFixture(t, "5.3.0")
	d, errOut, code := f.start()
	if code != ExitOK {
		t.Fatalf("start exit %d\n%s", code, errOut)
	}
	f.waitState(*d.RunID, "waiting")
	f.waitRunnerExit(*d.RunID)
	got := f.status(*d.RunID)
	if len(got.Facts) != 1 || got.Facts["self_review"] != "complete" {
		t.Fatalf("facts = %v", got.Facts)
	}
	st := f.load(*d.RunID)
	if len(st.FactSources["self_review"]) == 0 {
		t.Fatal("no fact sources recorded")
	}
	for _, s := range st.FactSources["self_review"] {
		if s.Array {
			t.Errorf("source %+v is marked as an array", s)
		}
	}
}
