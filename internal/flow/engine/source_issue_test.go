package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// このファイルは、課題の取り込み元の Issue の参照が、昇格の PR と、タスクの PR へ届くことの試験（flywheel#1424 の 1 点目）。

// issueRefSection は PR の本文の「## Issue 参照」の節の行（無ければ空）。
func issueRefSection(body string) string {
	_, rest, ok := strings.Cut(body, "## Issue 参照\n\n")
	if !ok {
		return ""
	}
	section, _, _ := strings.Cut(rest, "\n\n")
	return section
}

// open は、タスクの PR の本文に参照が無くても、渡された closes（取り込み元の Issue）の Closes の行を昇格の PR の本文に書く。
// 同じ番号がタスクの PR の Refs の行にもあれば Closes だけにする。closes が無いか 0 なら、本文はいまのまま。
func TestOpenWritesTheClosesOfTheSourceIssue(t *testing.T) {
	cases := []struct {
		name    string
		prBody  string
		extra   map[string]any
		section string
	}{
		{name: "no reference in any task pull request", prBody: "## 概要\n\nx\n", extra: map[string]any{"closes": 1419}, section: "Closes #1419"},
		{name: "the task pull requests only refer to it", prBody: "Refs #1419\nRefs #7\n", extra: map[string]any{"closes": 1419}, section: "Closes #1419\nRefs #7"},
		{name: "a task pull request also closes it", prBody: "Closes #1419\nCloses #5\n", extra: map[string]any{"closes": 1419}, section: "Closes #5\nCloses #1419"},
		{name: "closes 0 keeps the copied lines only", prBody: "Refs #7\n", extra: map[string]any{"closes": 0}, section: "Refs #7"},
		{name: "closes 0 and no reference: no section", prBody: "x\n", extra: map[string]any{"closes": 0}},
		{name: "closes not given and no reference: no section", prBody: "x\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newPREnv(t)
			p.tl.write(p.tl.gh, "pr-list-merged", `[{"number":11,"title":"t","url":"u","body":`+jsonQuote(c.prBody)+`}]`)
			st, _ := p.open(c.extra)
			if st.Reason != "opened" {
				t.Fatalf("status %s reason %s", st.Status, st.Reason)
			}
			body := p.tl.read(p.tl.gh, "body.1.md")
			if got := issueRefSection(body); got != c.section {
				t.Fatalf("Issue 参照 = %q, want %q\n%s", got, c.section, body)
			}
			if c.section == "" && strings.Contains(body, "## Issue 参照") {
				t.Fatalf("unexpected section:\n%s", body)
			}
		})
	}
}

// closes を渡さないときと 0 のときの本文は、1 バイトも変わらない。
func TestRenderIntegrationPRBodyWithoutASourceIssueIsUnchanged(t *testing.T) {
	prs := []mergedPR{{Number: 11, Title: "t", URL: "u", Body: "Refs #7\n"}}
	want := "## 概要\n\ns\n\n## 入っているタスクの PR（全 1 件）\n\n| 番号 | 題名 | URL |\n|---|---|---|\n| #11 | t | u |\n\n## Issue 参照\n\nRefs #7\n\n---\n\n" +
		"この PR は harness runtime（run `R`）が作成した。課題: `C-1`。題名と概要は LLM が書き、タスクの PR の表と Issue 参照の行は、統合ブランチへ入ったタスクの PR から runtime が転記した。\n"
	if got := renderIntegrationPRBody("s", prs, 0, "R", "C-1"); got != want {
		t.Fatalf("body = %q\nwant   %q", got, want)
	}
}

// resolve-ticket の --challenge_issue（課題の取り込み元の Issue）: 統合先を使う実行のタスクの PR は、その番号を Closes でなく Refs で書く。
// 取り込み元が Closes: なし のとき・統合先を使わないとき・渡されないときは、出力はいまのまま。
func TestResolveTicketChallengeIssue(t *testing.T) {
	dir := t.TempDir()
	taskFile := filepath.Join(dir, "task.md")
	if err := os.WriteFile(taskFile, []byte("# タスク\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	task := []string{"--task_file", taskFile, "--task_id", "T-1"}
	integ := []string{"--uses_integration", "true", "--challenge_id", "C-9"}
	args := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	cases := []struct {
		name    string
		body    string
		args    []string
		outcome string
		closes  int
		refs    []int
	}{
		{name: "multi-task: refers to the source issue", args: args(task, integ, []string{"--challenge_issue", "42"}), outcome: "ok", closes: 0, refs: []int{42}},
		{name: "one task with the issue input: Closes becomes Refs", args: args([]string{"--issue", "42"}, task, integ, []string{"--challenge_issue", "42"}), outcome: "ok", closes: 0, refs: []int{42}},
		{name: "a task of a child issue still closes the child", args: args([]string{"--issue", "42"}, task, integ, []string{"--challenge_issue", "40"}), outcome: "ok", closes: 42, refs: []int{40}},
		{name: "the Refs line of the issue is kept and not repeated", body: "Refs: #42, #7\n", args: args([]string{"--issue", "42"}, task, integ, []string{"--challenge_issue", "42"}), outcome: "ok", closes: 0, refs: []int{42, 7}},
		{name: "Closes: なし with the issue input", body: "Closes: なし\n", args: args([]string{"--issue", "42"}, task, integ, []string{"--challenge_issue", "42"}), outcome: "ok", closes: 0},
		{name: "Closes: なし without the issue input", body: "Closes: なし\n", args: args(task, integ, []string{"--challenge_issue", "42"}), outcome: "ok", closes: 0},
		{name: "an unreadable Closes line of the source issue that is not the input: no stop, no reference", body: "Closes: #1\n", args: args(task, integ, []string{"--challenge_issue", "42"}), outcome: "ok", closes: 0},
		{name: "an unreadable Closes line of the input issue still stops", body: "Closes: #1\n", args: args([]string{"--issue", "42"}, task, integ, []string{"--challenge_issue", "42"}), outcome: "links_invalid", closes: 42},
		{name: "without an integration branch it is ignored", args: args([]string{"--issue", "42"}, task, []string{"--uses_integration", "false", "--challenge_issue", "42"}), outcome: "ok", closes: 42},
		{name: "no source issue: no reference", args: args(task, integ), outcome: "ok", closes: 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, code, stderr := resolveWith(t, newTools(t), c.body, c.args...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if r.Outcome != c.outcome || r.Closes == nil || *r.Closes != c.closes {
				t.Fatalf("got %+v", r)
			}
			if c.outcome == "ok" && !reflect.DeepEqual(append([]int{}, r.Refs...), append([]int{}, c.refs...)) {
				t.Fatalf("refs = %v, want %v", r.Refs, c.refs)
			}
		})
	}
}

// タスクの Issue（子。--issue 42）と課題の取り込み元（親。--challenge_issue 40）の番号が違うときは、取り込み元の本文を番号を指して読み、
// それぞれの本文の Closes: 行は、その Issue にだけ効く。取り込み元を読めなくても止めない。
func TestResolveTicketReadsTheSourceIssueByItsNumber(t *testing.T) {
	taskFile := filepath.Join(t.TempDir(), "task.md")
	if err := os.WriteFile(taskFile, []byte("# タスク\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, child, parent string
		parentFails         bool
		closes              int
		refs                []int
	}{
		{name: "neither says Closes: なし", child: "x\n", parent: "y\n", closes: 42, refs: []int{40}},
		{name: "only the source issue says Closes: なし", child: "x\n", parent: "Closes: なし\n", closes: 42},
		{name: "only the task issue says Closes: なし", child: "Closes: なし\n", parent: "y\n", closes: 0, refs: []int{40}},
		{name: "the Refs line of the source issue is not copied", child: "x\n", parent: "Refs: #7\n", closes: 42, refs: []int{40}},
		{name: "the source issue has an unreadable Closes line", child: "x\n", parent: "Closes: #1\n", closes: 42},
		{name: "the source issue cannot be read", child: "x\n", parentFails: true, closes: 42},
	} {
		t.Run(c.name, func(t *testing.T) {
			tl := newTools(t)
			if c.parentFails {
				tl.write(tl.gh, "issue.40.fail", "")
			} else {
				tl.write(tl.gh, "issue.40.json", `{"number":40,"title":"parent","body":`+jsonQuote(c.parent)+`,"state":"OPEN"}`)
			}
			// resolveWith は c.child を issue.json（番号 42 が読むもの）に書く。
			r, code, stderr := resolveWith(t, tl, c.child, "--issue", "42", "--task_file", taskFile, "--task_id", "T-1",
				"--uses_integration", "true", "--challenge_id", "C-9", "--challenge_issue", "40")
			if code != 0 || r.Outcome != "ok" || r.Closes == nil || *r.Closes != c.closes {
				t.Fatalf("exit %d: %s %+v", code, stderr, r)
			}
			if !reflect.DeepEqual(append([]int{}, r.Refs...), append([]int{}, c.refs...)) {
				t.Fatalf("refs = %v, want %v", r.Refs, c.refs)
			}
			calls := tl.read(tl.gh, "calls")
			for _, want := range []string{"issue\tview\t42\t", "issue\tview\t40\t"} {
				if strings.Count(calls, want) != 1 {
					t.Errorf("gh calls lack exactly one %q:\n%s", want, calls)
				}
			}
		})
	}
}

// links の段は、取り込み元を読めなければ失敗で終わる（Closes の無い昇格の PR を作らない）。読むのは渡された番号の Issue。
func TestResolveTicketLinksOnlyReadsTheSourceIssueByItsNumber(t *testing.T) {
	tl := newTools(t)
	tl.write(tl.gh, "issue.40.json", `{"body":"Closes: なし\n"}`)
	r, code, stderr := resolveWith(t, tl, "x\n", "--links_only", "true", "--challenge_issue", "40")
	if code != 0 || r.Closes == nil || *r.Closes != 0 || !strings.Contains(tl.read(tl.gh, "calls"), "issue\tview\t40\t") {
		t.Fatalf("exit %d: %s %+v\n%s", code, stderr, r, tl.read(tl.gh, "calls"))
	}
	failing := newTools(t)
	failing.write(failing.gh, "issue.40.fail", "")
	if _, code, _ := resolveWith(t, failing, "x\n", "--links_only", "true", "--challenge_issue", "40"); code == 0 {
		t.Fatal("links must fail when the source issue cannot be read")
	}
}

// 統合先を使わない 1 タスクの課題（--issue に取り込み元が入る）では、main へ向かうタスクの PR の本文に Closes #N が入る。
// Closes: なし の Issue では入らない。
func TestTaskPullRequestWithoutIntegrationClosesTheSourceIssue(t *testing.T) {
	taskFile := filepath.Join(t.TempDir(), "task.md")
	if err := os.WriteFile(taskFile, []byte("# タスク\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		body string
		want bool
	}{{body: "x\n", want: true}, {body: "Closes: なし\n"}} {
		r, code, stderr := resolveWith(t, newTools(t), c.body, "--issue", "42", "--task_file", taskFile, "--task_id", "T-1", "--uses_integration", "false")
		if code != 0 || r.Outcome != "ok" || r.Base != "main" || r.Closes == nil {
			t.Fatalf("exit %d: %s %+v", code, stderr, r)
		}
		pr := RenderPRBody(PRInput{RunID: "R", Base: r.Base, Closes: int64(*r.Closes), Title: "t", Summary: "s"})
		if got := strings.Contains(pr, "\nCloses #42\n"); got != c.want {
			t.Fatalf("body %q: Closes #42 = %v, want %v\n%s", c.body, got, c.want, pr)
		}
	}
}

// reflect の links の段（resolve-ticket.sh --links_only true）は、取り込み元の Issue の本文から昇格の PR の closes を決める。
func TestResolveTicketLinksOnly(t *testing.T) {
	cases := []struct {
		name, body, outcome string
		args                []string
		closes              int
		ghCalls             bool
	}{
		{name: "closes the source issue", body: "Base: feat/x\nRefs: #7\n", args: []string{"--challenge_issue", "42"}, outcome: "ok", closes: 42, ghCalls: true},
		{name: "Closes: なし", body: "Closes: なし\n", args: []string{"--challenge_issue", "42"}, outcome: "ok", closes: 0, ghCalls: true},
		{name: "Closes: none with a full-width colon", body: "Closes：none\n", args: []string{"--challenge_issue", "42"}, outcome: "ok", closes: 0, ghCalls: true},
		{name: "Closes: なし in a code block is not read", body: "```\nCloses: なし\n```\n", args: []string{"--challenge_issue", "42"}, outcome: "ok", closes: 42, ghCalls: true},
		{name: "an unreadable Closes line: no Closes, with the reason", body: "Closes: #3\n", args: []string{"--challenge_issue", "42"}, outcome: "ok", closes: 0, ghCalls: true},
		{name: "no source issue: no gh call", args: nil, outcome: "ok", closes: 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tl := newTools(t)
			r, code, stderr := resolveWith(t, tl, c.body, append([]string{"--links_only", "true"}, c.args...)...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if r.Outcome != c.outcome || r.Closes == nil || *r.Closes != c.closes {
				t.Fatalf("got %+v", r)
			}
			if got := tl.read(tl.gh, "calls") != ""; got != c.ghCalls {
				t.Fatalf("gh called = %v, want %v: %s", got, c.ghCalls, tl.read(tl.gh, "calls"))
			}
			if strings.HasPrefix(c.name, "an unreadable") != strings.Contains(r.Message, "Issue #42") {
				t.Errorf("message %q", r.Message)
			}
		})
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
