package flow

// 埋め込んだ定義 spec-draft を偽の claude で実行し、契約の出力の facts を確かめる。
// 仕組みの試験（spec_draft_test.go）は代わりの定義を差し込むので、定義の facts はここで確かめる。

import (
	"os"
	"path/filepath"
	"testing"
)

// runEmbeddedSpecDraft は定義 spec-draft を始め、state で終わった実行の契約の出力を返す。
// answers は起動の順（write → critique）の、claude の型付きの出力。
func runEmbeddedSpecDraft(t *testing.T, state string, answers ...any) contractDoc {
	t.Helper()
	f := &specFixture{fixture: newFixture(t, "5.3.0")}
	f.dir = filepath.Join(f.ws, ".flywheel", "specs")
	briefDir := filepath.Join(f.ws, ".flywheel", "briefs")
	for _, dir := range []string{f.dir, briefDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", f.root)
	for i, a := range answers {
		f.respond(i+1, claudeStructured(a, 1))
	}
	d := f.specCall(func(env Env) int {
		return Start(env, StartOptions{Name: SpecDraftFlow, PermissionMode: "auto", DelegatedRun: "R-1", Inputs: []string{
			"draft_file=C-1.md", "brief_file=" + filepath.Join(briefDir, "C-1.md"), "brief_dir=" + briefDir,
			"repo_dir=" + f.ws, "challenge_id=C-1",
		}})
	})
	if d.RunID == nil {
		t.Fatalf("start doc %+v", d)
	}
	return f.finished(*d.RunID, state)
}

func specWriteAnswer(outcome, summary string) map[string]any {
	return map[string]any{"outcome": outcome, "summary": summary}
}

func specCritiqueAnswer(outcome, summary string, questions ...any) map[string]any {
	return map[string]any{"outcome": outcome, "summary": summary, "questions": append([]any{}, questions...)}
}

func assertStepSummaryFact(t *testing.T, d contractDoc, want string) {
	t.Helper()
	if got, ok := d.Facts["spec_step_summary"]; !ok || got != want {
		t.Fatalf("facts = %v, want spec_step_summary %q (%s)", d.Facts, want, d.Summary)
	}
}

// S2b-2 の AC-380
func TestSpecDraftStepSummaryFactIsWriteSummaryWhenWriteFails(t *testing.T) {
	assertStepSummaryFact(t, runEmbeddedSpecDraft(t, "failed", specWriteAnswer("failure", "X")), "X")
}

// S2b-2 の AC-381
func TestSpecDraftStepSummaryFactIsCritiqueSummaryWhenCritiqueFails(t *testing.T) {
	d := runEmbeddedSpecDraft(t, "failed", specWriteAnswer("ok", "X"), specCritiqueAnswer("failure", "Y"))
	assertStepSummaryFact(t, d, "Y")
}

// S2b-2 の AC-382
func TestSpecDraftStepSummaryFactIsCritiqueSummaryWhenBothSucceed(t *testing.T) {
	d := runEmbeddedSpecDraft(t, "succeeded", specWriteAnswer("ok", "X"), specCritiqueAnswer("ok", "Y"))
	assertStepSummaryFact(t, d, "Y")
}

// S2b-2 の AC-383: spec_questions は、問いの配列の JSON の文字列のまま。
func TestSpecDraftSpecQuestionsFactIsUnchanged(t *testing.T) {
	question := map[string]any{
		"question": "どうするか", "options": []any{map[string]any{"label": "A"}, map[string]any{"label": "B"}},
		"recommended": 1, "reason": "理由",
	}
	d := runEmbeddedSpecDraft(t, "succeeded", specWriteAnswer("ok", "X"), specCritiqueAnswer("ok", "Y", question))
	// キーの順は偽の claude の応答（encoding/json が辞書順に書く）のまま。
	const want = `[{"options":[{"label":"A"},{"label":"B"}],"question":"どうするか","reason":"理由","recommended":1}]`
	if got := d.Facts["spec_questions"]; got != want {
		t.Fatalf("spec_questions = %s, want %s (%s)", got, want, d.Summary)
	}
}
