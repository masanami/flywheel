package flow

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/flow/workflow"
)

// 組み込みの reflect を統合ブランチの名前で始めると、links の段が取り込み元の Issue の本文を読み、作る昇格の PR の本文は、
// タスクの PR の本文に参照が無くても Closes #N を持つ。取り込み元が無い・本文が Closes: なし なら、Issue の参照を持たない。
func TestReflectOpensTheIntegrationPullRequestClosingTheSourceIssue(t *testing.T) {
	for _, c := range []struct {
		name, issueBody string
		inputs          []string
		closes          bool
	}{
		{name: "source issue", issueBody: "背景\n", inputs: []string{"challenge_issue=42"}, closes: true},
		{name: "Closes: なし", issueBody: "Closes: なし\n", inputs: []string{"challenge_issue=42"}},
		{name: "no source issue", issueBody: "背景\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, "5.3.0")
			git(t, f.repo, "checkout", "-q", "-b", "integration/c-1")
			writeFile(t, filepath.Join(f.repo, "widget.txt"), "w\n", 0o644)
			git(t, f.repo, "add", ".")
			git(t, f.repo, "commit", "-q", "-m", "widget")
			git(t, f.repo, "push", "-q", "origin", "integration/c-1")
			git(t, f.repo, "checkout", "-q", "main")
			f.ghFile("issue.json", `{"number":42,"title":"t","body":`+quoteJSON(c.issueBody)+`,"state":"OPEN"}`)
			f.ghFile("pr-list-merged", `[{"number":3,"title":"feat: task","url":"https://github.com/o/r/pull/3","body":"no reference\n"}]`)
			f.respond(1, claudeStructured(map[string]any{"outcome": "ok", "pr_title": "feat: widget", "summary": "widget"}, 0.1))

			env, out, errb := f.env()
			code := Start(env, StartOptions{Name: "reflect", PermissionMode: "auto", DelegatedRun: "R-1",
				Inputs: append([]string{"artifact_ref=integration/c-1"}, c.inputs...)})
			d := decodeDoc(t, out.String())
			if code != ExitOK || d.RunID == nil {
				t.Fatalf("start exit %d: %s\n%s", code, out.String(), errb.String())
			}
			// PR を作った後の段（作業ツリー以降）は、この試験の対象でない。本文が書かれるまで待ち、run を取り消す。
			bodyPath := filepath.Join(f.ghDir, "body.1.md")
			waitFile(t, f, *d.RunID, bodyPath)
			body, err := os.ReadFile(bodyPath)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(body), "\n## Issue 参照\n\nCloses #42\n\n"); got != c.closes {
				t.Errorf("Closes #42 = %v, want %v:\n%s", got, c.closes, body)
			}
			if !c.closes && strings.Contains(string(body), "#42") {
				t.Errorf("the body refers to the issue:\n%s", body)
			}
			cenv, _, _ := f.env()
			_ = Cancel(cenv, *d.RunID)
		})
	}
}

// 入力 challenge_issue を宣言していない定義（足す前に書き出したワークスペースの定義）は、渡されても読み飛ばして始める。
// ほかの宣言していない入力は、いままでどおり誤り。組み込みの 3 つの定義は宣言している。
func TestParseInputsAndTheChallengeIssueInput(t *testing.T) {
	wf := &workflow.Workflow{ID: "old"}
	got, err := parseInputs(wf, []string{"challenge_issue=42"})
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v err %v, want the input to be skipped", got, err)
	}
	if _, err := parseInputs(wf, []string{"other=1"}); err == nil {
		t.Fatal("an undeclared input must still be refused")
	}
}

// ワークスペースの reflect が challenge_issue を宣言していなければ（読めなければ）、タスクのフローにも渡さない。宣言していれば渡す。
func TestTicketGetsTheChallengeIssueOnlyWhenReflectDeclaresIt(t *testing.T) {
	const old = "schema: harness.workflow/v1\nid: reflect\ninputs:\n  artifact_ref: { type: string, required: true }\n"
	for _, c := range []struct {
		name, reflect string
		passed        bool
	}{
		{name: "built-in reflect", passed: true},
		{name: "old workspace reflect", reflect: old + "steps:\n  a: { kind: select, value: $inputs.artifact_ref, on: { x: { done: ok } } }\n"},
		{name: "unreadable workspace reflect", reflect: "{"},
		{name: "workspace reflect that declares it", reflect: old + "  challenge_issue: { type: integer }\nsteps:\n  a: { kind: select, value: $inputs.artifact_ref, on: { x: { done: ok } } }\n", passed: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ws := t.TempDir()
			if c.reflect != "" {
				writeFile(t, filepath.Join(FlowsDir(ws), "reflect.yaml"), c.reflect, 0o644)
			}
			ticket := &workflow.Workflow{ID: "ticket", Inputs: []*workflow.Input{{Name: inputChallengeIssue}}}
			var errb bytes.Buffer
			got := challengeIssueInputs(Env{Workspace: ws, Stderr: &errb}, ticket, "ticket", []string{"issue=", "challenge_issue=42"})
			want := []string{"issue="}
			if c.passed {
				want = append(want, "challenge_issue=42")
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("inputs = %q, want %q", got, want)
			}
			if warned := strings.Contains(errb.String(), inputChallengeIssue); warned == c.passed {
				t.Errorf("stderr = %q, want a warning only when the input is not passed", errb.String())
			}
		})
	}
}

// flow start も同じ判断をする: 古い reflect があるワークスペースで始めた ticket の実行は、入力 challenge_issue を持たない。
func TestStartDropsTheChallengeIssueForAnOldReflect(t *testing.T) {
	f := newFixture(t, "5.3.0")
	writeFile(t, filepath.Join(FlowsDir(f.ws), "reflect.yaml"), "schema: harness.workflow/v1\nid: reflect\n", 0o644)
	d, errOut, code := f.start("challenge_issue=42")
	if code != ExitOK || d.RunID == nil {
		t.Fatalf("start exit %d: %+v\n%s", code, d, errOut)
	}
	if _, ok := f.load(*d.RunID).Inputs[inputChallengeIssue]; ok {
		t.Error("the run has the input challenge_issue")
	}
	if !strings.Contains(errOut, inputChallengeIssue) {
		t.Errorf("stderr does not say why: %s", errOut)
	}
	cenv, _, _ := f.env()
	_ = Cancel(cenv, *d.RunID)
}

// 定義が challenge_issue を宣言していなければ、読み飛ばすことを標準エラーに出す。
func TestSkippingTheUndeclaredChallengeIssueIsReported(t *testing.T) {
	var errb bytes.Buffer
	challengeIssueInputs(Env{Workspace: t.TempDir(), Stderr: &errb}, &workflow.Workflow{ID: "old"}, "reflect", []string{"challenge_issue=42"})
	if !strings.Contains(errb.String(), "読み飛ばす") {
		t.Fatalf("stderr = %q", errb.String())
	}
}

// 組み込みの ticket・ticket-light・reflect は入力 challenge_issue を宣言し、取り込み元を読む段へ渡す。reflect は links の結果を open へ渡す。
func TestEmbeddedDefinitionsPassTheChallengeIssue(t *testing.T) {
	binding := func(t *testing.T, wf *workflow.Workflow, step, name string) string {
		t.Helper()
		s := wf.Step(step)
		if s == nil {
			t.Fatalf("%s has no step %s", wf.ID, step)
		}
		for _, b := range s.With {
			if b.Name == name && b.Value.Ref != nil {
				return b.Value.Ref.Raw
			}
		}
		return ""
	}
	for id, step := range map[string]string{"ticket": "resolve", "ticket-light": "resolve", "reflect": "links"} {
		t.Run(id, func(t *testing.T) {
			wf, err := workflow.Load(filepath.Join("assets", "files", "workflows", id+".yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if in := wf.Input(inputChallengeIssue); in == nil || in.Required {
				t.Fatalf("input %s = %+v, want an optional input", inputChallengeIssue, in)
			}
			if got := binding(t, wf, step, inputChallengeIssue); got != "$inputs.challenge_issue" {
				t.Errorf("%s.with.challenge_issue = %q", step, got)
			}
			if id == reflectFlowName {
				if got := binding(t, wf, "open", "closes"); got != "$steps.links.closes" {
					t.Errorf("open.with.closes = %q", got)
				}
			}
		})
	}
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// waitFile は path が現れるまで待つ。
func waitFile(t *testing.T, f *fixture, runID, path string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			f.dumpRun(runID)
			t.Fatalf("%s did not appear: %+v", path, f.status(runID))
		}
		time.Sleep(50 * time.Millisecond)
	}
}
