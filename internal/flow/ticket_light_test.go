package flow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/flow/runstate"
)

// ticket-light をレビュー待ちまで進める試験の準備。偽の claude の応答は、ticket の fixture（分析・実装・コミット）から、
// 分析を除いた実装・レビュー・コミットに並べ直す。タスクの文面のファイルと task_id（T-12）を渡すので、作業ブランチの鍵は task-12。
func newLightFixture(t *testing.T) (*fixture, []string) {
	t.Helper()
	f := newFixture(t, "5.3.0")
	f.branch = "feature/task-12-light"
	f.respond(1, claudeStructured(implementPass, 2.0))
	f.respond(2, claudeStructured(reviewResult("clean"), 0.5))
	f.respond(3, claudeStructured(map[string]any{"outcome": "committed", "commit_sha": "abc", "summary": "committed"}, 0.5))
	if err := os.Remove(filepath.Join(f.fdir, "responses", "4")); err != nil {
		t.Fatal(err)
	}
	taskFile := filepath.Join(f.root, "task.md")
	writeFile(t, taskFile, "# task\nwidget を足す\n", 0o644)
	return f, []string{"task_file=" + taskFile, "task_id=T-12"}
}

func (f *fixture) startLight(extra ...string) string {
	f.t.Helper()
	env, out, errb := f.env()
	code := Start(env, StartOptions{Name: "ticket-light", Inputs: append([]string{"issue=42"}, extra...), PermissionMode: "auto", DelegatedRun: "R-start"})
	d := decodeDoc(f.t, out.String())
	if code != ExitOK || d.RunID == nil || *d.RunID == "" {
		f.t.Fatalf("start exit %d\n%s", code, errb.String())
	}
	return *d.RunID
}

type light struct {
	started     []string          // step_started の順
	transitions map[string]string // from → "<outcome>→<to>"（最後に採った遷移）
	all         []string          // "<from>:<outcome>→<to>" の順
}

func (f *fixture) lightEvents(id string) light {
	f.t.Helper()
	events, _, err := runstate.ReadEvents(filepath.Join(runsDir(f.ws), id, runstate.EventsFile))
	if err != nil {
		f.t.Fatal(err)
	}
	l := light{transitions: map[string]string{}}
	for _, ev := range events {
		switch ev.Type {
		case runstate.EvStepStarted:
			l.started = append(l.started, ev.StepStarted.Step)
		case runstate.EvTransition:
			tr := ev.Transition
			l.transitions[tr.From] = tr.Outcome + "→" + tr.To
			l.all = append(l.all, tr.From+":"+tr.Outcome+"→"+tr.To)
		}
	}
	return l
}

// AC-11・AC-12・AC-13・AC-14・AC-16・AC-18: 鍵が task-12 のとき、作業ツリーの用意へ渡る作業ブランチ名は feature/task-12-light。
// claude を呼ぶ最初のステップは implement（analyze を起動しない）。sync の current は quality へ進み、品質の検査が pass なら publish へ、
// self_review が complete なら commit へ進む。最後は kind review のレビュー待ち。
func TestTicketLightRunsToReviewWithoutAnalysis(t *testing.T) {
	f, extra := newLightFixture(t)
	id := f.startLight(extra...)
	w := f.waitState(id, "waiting")
	if w.RequestedAction == nil || w.RequestedAction.Kind != "review" {
		t.Fatalf("waiting but not at review: %+v (%s)", w.RequestedAction, w.Summary)
	}
	f.waitRunnerExit(id)

	l := f.lightEvents(id)
	for _, s := range l.started {
		if s == "analyze" || s == "e2e" || s == "e2e-route" {
			t.Errorf("step %s was started in ticket-light: %v", s, l.started)
		}
	}
	// claude を起動したステップは、implement・self-review・commit の順（呼び出しの数は 3 回）。
	if b, _ := os.ReadFile(filepath.Join(f.fdir, "count")); strings.TrimSpace(string(b)) != "3" {
		t.Errorf("claude was started %q times, want 3 (implement, self-review and commit)", b)
	}
	if got := l.transitions["resolve"]; got != "ok→integration-route" {
		t.Errorf("resolve: %s", got)
	}
	if got := l.transitions["integration-route"]; got != "none→branch" {
		t.Errorf("integration-route: %s", got)
	}
	if got := l.transitions["branch"]; got != "ok→workspace" {
		t.Errorf("branch: %s", got)
	}
	if got := l.transitions["implement"]; got != "pass→self-review" {
		t.Errorf("implement: %s", got)
	}
	if got := l.transitions["self-review"]; got != "clean→commit" {
		t.Errorf("self-review: %s", got)
	}
	if got := l.transitions["sync"]; got != "current→quality" {
		t.Errorf("sync: %s", got)
	}
	if got := l.transitions["quality"]; got != "pass→publish" {
		t.Errorf("quality: %s", got)
	}
	// 作業ブランチは origin に feature/task-12-light で在る（publish が push する）。
	if fields := strings.Fields(git(t, f.repo, "ls-remote", "--heads", f.origin, "feature/task-12-light")); len(fields) == 0 {
		t.Errorf("branch feature/task-12-light is not on origin")
	}
	// 最初に起動した claude の指示文は implement のもの（簡略なフローの指示文）。
	stdin, _ := os.ReadFile(filepath.Join(f.fdir, "calls", "1.stdin"))
	if !strings.Contains(string(stdin), "ticket-light: 実装") {
		t.Errorf("the first claude call is not the ticket-light implement step:\n%s", stdin)
	}
}

// AC-15: 品質の検査が fail を返すと、fix へ進む。直した後はレビューのステップを通って commit へ進む。
func TestTicketLightQualityFailGoesToFix(t *testing.T) {
	f, extra := newLightFixture(t)
	counter := filepath.Join(f.root, "quality-count")
	writeFile(t, filepath.Join(f.plugin, "scripts", "quality-check-runner.sh"), `#!/bin/bash
if [ ! -e "`+counter+`" ]; then
  : > "`+counter+`"
  printf '%s\n' '{"result":"fail","auto_fix":{},"gates":{"test":{"status":"fail"}}}'
  exit 1
fi
printf '%s\n' '{"result":"pass","auto_fix":{},"gates":{"test":{"status":"pass"}}}'
`, 0o755)
	f.respond(4, claudeStructured(map[string]any{"outcome": "pass", "summary": "fixed", "deviation_report": ""}, 1.0))
	f.respond(5, claudeStructured(reviewResult("clean"), 0.5))
	f.respond(6, claudeStructured(map[string]any{"outcome": "committed", "commit_sha": "abd", "summary": "committed"}, 0.5))
	id := f.startLight(extra...)
	f.waitState(id, "waiting")
	f.waitRunnerExit(id)
	l := f.lightEvents(id)
	joined := strings.Join(l.all, " ")
	for _, want := range []string{"quality:fail→fix", "fix:pass→self-review", "self-review:clean→commit"} {
		if !strings.Contains(joined, want) {
			t.Errorf("transitions lack %q: %s", want, joined)
		}
	}
}

// AC-17: 実装の出力の outcome が deviation のとき、設計の逸脱の待ちに入る。
func TestTicketLightDeviationWaitsAtDesignDeviation(t *testing.T) {
	f, extra := newLightFixture(t)
	f.respond(1, claudeStructured(map[string]any{"outcome": "deviation", "pr_title": "feat: add the widget", "summary": "stopped",
		"behaviors": []map[string]string{}, "unverified": []string{}, "cross_repo_attestation": "", "deviation_report": "範囲を超える"}, 1.0))
	id := f.startLight(extra...)
	f.waitState(id, "waiting")
	f.waitRunnerExit(id)
	if got := f.lightEvents(id).transitions["implement"]; got != "deviation→design-deviation" {
		t.Errorf("implement: %s, want deviation→design-deviation", got)
	}
	if st := f.load(id); len(st.Units) != 1 || st.Units[0].CurrentStep != "design-deviation" {
		t.Errorf("the run is not waiting at design-deviation")
	}
}

// AC-19: レビュー待ちへの land の答えの後、PR を宛先へ入れる gh の呼び出しを 1 回行う。
func TestTicketLightLandMergesOnce(t *testing.T) {
	f, extra := newLightFixture(t)
	id := f.startLight(extra...)
	f.waitState(id, "waiting")
	f.waitRunnerExit(id)
	head := f.originHead()
	f.setPR("OPEN", head, "")
	f.ghFile("merge.then", `{"state":"MERGED","headRefOid":"`+head+`","baseRefName":"main","mergeCommit":{"oid":"squashed0000000000000000000000000000000000"},"isDraft":false}`)
	if _, errOut, code := f.resume(ResumeOptions{ID: id, Unit: "main", Input: "land", HasInput: true, ApprovedRef: head}); code != ExitOK {
		t.Fatalf("resume exit %d\n%s", code, errOut)
	}
	f.waitState(id, "succeeded")
	if n := f.merges(); n != 1 {
		t.Errorf("gh pr merge was called %d times, want exactly 1", n)
	}
}
