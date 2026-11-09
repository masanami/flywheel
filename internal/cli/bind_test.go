package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// このファイルは `flywheel bind <C-ID> <owner/repo#番号>`・`flywheel unbind <C-ID>`
// （docs/features/m2-github-issue-ingest.md §課題と Issue の紐づけ）の CLI の配線を、偽の gh で検査する。
// 4 つの条件そのものの網羅は internal/core/bind_test.go が担当する。

const bindDeclaration = `{
  "version": 1,
  "sources": [
    {"id": "src-a", "type": "github-issue", "repos": ["owner/repo-a"], "self_assignees": ["someone"]}
  ]
}`

// bindWorkspace は宣言と、偽の gh（owner/repo-a の #5 が open・#7 が closed・#8 が 404・#9 が 500）を用意し、
// 対応の無い課題を 2 件作る。calls は偽の gh の呼び出しの記録。
func bindWorkspace(t *testing.T) (ws string, ids []string, calls func() []string) {
	t.Helper()
	ws = initializedWorkspace(t)
	writeSourcesDeclaration(t, ws, bindDeclaration)
	issue := func(n int, state string) string {
		return marshalFakeGHIssue(t, fakeGHIssue{Number: n, Title: "UP-TITLE", Body: "UP-BODY", Repo: "owner/repo-a", State: state, Comments: 2})
	}
	calls = withFakeGHRoutesOnPATH(t, []fakeGHRoute{
		fakeGHGetIssueStatusLineRoute("owner/repo-a", 5, "HTTP/2.0 200 OK", issue(5, "open"), 0),
		fakeGHGetIssueStatusLineRoute("owner/repo-a", 6, "HTTP/2.0 200 OK", issue(6, "open"), 0),
		fakeGHGetIssueStatusLineRoute("owner/repo-a", 7, "HTTP/2.0 200 OK", issue(7, "closed"), 0),
		fakeGHGetIssueStatusLineRoute("owner/repo-a", 8, "HTTP/2.0 404 Not Found", `{"message":"Not Found"}`, 1),
		fakeGHGetIssueStatusLineRoute("owner/repo-a", 9, "HTTP/2.0 500 Internal Server Error", `{"message":"boom"}`, 1),
	})
	ids = []string{createForCase(t, ws), createForCase(t, ws)}
	return ws, ids, calls
}

// runBindFailing は args を --json 付きで実行し、失敗の終了コード・error.code・error.message を返す。
func runBindFailing(t *testing.T, ws string, args ...string) (exit int, code, message string) {
	t.Helper()
	full := append(append([]string{}, args...), "--workspace", ws, "--json")
	var stdout, stderr bytes.Buffer
	exit = run(full, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if stdout.Len() != 0 {
		t.Fatalf("args=%v stdout should stay empty on failure, got %q", args, stdout.String())
	}
	var doc struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &doc); err != nil {
		t.Fatalf("args=%v stderr is not JSON: %v (%q)", args, err, stderr.String())
	}
	return exit, doc.Error.Code, doc.Error.Message
}

func logLen(t *testing.T, ws string) int {
	t.Helper()
	return len(runJSON(t, ws, "log")["activities"].([]any))
}

func TestBind_LinksTheChallengeAndShowsTheBinding(t *testing.T) {
	ws, ids, _ := bindWorkspace(t)

	out := runJSON(t, ws, "bind", ids[0], "owner/repo-a#5")
	c, ok := out["challenge"].(map[string]any)
	if !ok || c["id"] != ids[0] || c["version"] != 2.0 || c["title"] != "t" {
		t.Fatalf("output = %#v, want the challenge %s at version 2 with its own title", out, ids[0])
	}

	shown := runJSON(t, ws, "show", ids[0])
	sb, ok := shown["source_binding"].(map[string]any)
	if !ok || sb["external_key"] != "owner/repo-a#5" || sb["source_id"] != "src-a" || sb["upstream_state"] != "open" {
		t.Fatalf("source_binding = %#v, want the bound issue", shown["source_binding"])
	}
	acts := runJSON(t, ws, "log", ids[0])["activities"].([]any)
	last := acts[len(acts)-1].(map[string]any)
	if last["action"] != "bind" || last["channel"] != "cli" || last["verification"] != "none" {
		t.Errorf("last activity = %#v, want bind via cli with no verification", last)
	}
	if got := runText(t, ws, "bind", ids[1], "owner/repo-a#6"); got != ids[1]+"\n" {
		t.Errorf("bind text output = %q, want the challenge id", got)
	}
}

func TestBind_RejectionsMapToCodesAndWriteNothing(t *testing.T) {
	ws, ids, calls := bindWorkspace(t)
	runJSON(t, ws, "bind", ids[0], "owner/repo-a#5")
	before := logLen(t, ws)

	cases := []struct {
		name    string
		args    []string
		exit    int
		code    ErrorCode
		message string // 理由を区別する語
	}{
		{"challenge already bound", []string{"bind", ids[0], "owner/repo-a#6"}, 1, CodeValidationFailed, "already bound to an upstream issue"},
		{"issue bound to another challenge", []string{"bind", ids[1], "owner/repo-a#5"}, 1, CodeValidationFailed, "already bound to another challenge"},
		{"repo not declared", []string{"bind", ids[1], "other/repo#1"}, 1, CodeValidationFailed, "not in any declared source"},
		{"issue closed", []string{"bind", ids[1], "owner/repo-a#7"}, 1, CodeValidationFailed, "not open"},
		{"issue not found upstream", []string{"bind", ids[1], "owner/repo-a#8"}, 1, CodeValidationFailed, "not found"},
		{"fetch fails", []string{"bind", ids[1], "owner/repo-a#9"}, 2, CodeUpstreamUnavailable, "could not fetch"},
		{"malformed external key", []string{"bind", ids[1], "owner/repo-a"}, 1, CodeValidationFailed, "<owner>/<name>#<number>"},
		{"missing challenge", []string{"bind", "C-99", "owner/repo-a#6"}, 1, CodeNotFound, ""},
		{"malformed challenge id", []string{"bind", "nope", "owner/repo-a#6"}, 1, CodeNotFound, ""},
		{"too few arguments", []string{"bind", ids[1]}, 2, CodeUsageError, ""},
		{"too many arguments", []string{"bind", ids[1], "owner/repo-a#6", "x"}, 2, CodeUsageError, ""},
		{"no relayed flag", []string{"bind", ids[1], "owner/repo-a#6", "--relayed", "--session", "s", "--quote", "q"}, 2, CodeUsageError, ""},
	}
	for _, c := range cases {
		exit, code, message := runBindFailing(t, ws, c.args...)
		if exit != c.exit || code != string(c.code) || !strings.Contains(message, c.message) {
			t.Errorf("%s: exit=%d code=%q message=%q, want exit=%d code=%q message containing %q", c.name, exit, code, message, c.exit, c.code, c.message)
		}
	}
	if after := logLen(t, ws); after != before {
		t.Errorf("activities = %d after the rejections, want %d (nothing recorded)", after, before)
	}
	if v := runJSON(t, ws, "show", ids[1])["challenge"].(map[string]any)["version"]; v != 1.0 {
		t.Errorf("version of %s = %v, want 1", ids[1], v)
	}
	if callsContain(calls(), "repos/other/repo") {
		t.Error("gh was called for a repository outside the declaration")
	}
}

func TestBind_DeclarationAndGHProblemsStopBeforeAnyChange(t *testing.T) {
	ws, ids, _ := bindWorkspace(t)

	// 宣言が壊れている
	writeSourcesDeclaration(t, ws, `{"version": 1, "sources": [{"id": "x"}]}`)
	if exit, code, _ := runBindFailing(t, ws, "bind", ids[0], "owner/repo-a#5"); exit != 2 || code != string(CodeConfigInvalid) {
		t.Errorf("invalid declaration: exit=%d code=%q, want 2 config_invalid", exit, code)
	}
	writeSourcesDeclaration(t, ws, bindDeclaration)

	// gh が PATH に無い
	t.Setenv("PATH", t.TempDir())
	if exit, code, _ := runBindFailing(t, ws, "bind", ids[0], "owner/repo-a#5"); exit != 2 || code != string(CodeUpstreamUnavailable) {
		t.Errorf("gh missing: exit=%d code=%q, want 2 upstream_unavailable", exit, code)
	}
	if v := runJSON(t, ws, "show", ids[0])["source_binding"]; v != nil {
		t.Errorf("source_binding = %#v, want none", v)
	}
}

func TestBind_WithoutDeclarationFileIsConfigNotFound(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	if exit, code, _ := runBindFailing(t, ws, "bind", id, "owner/repo-a#5"); exit != 2 || code != string(CodeConfigNotFound) {
		t.Errorf("exit=%d code=%q, want 2 config_not_found", exit, code)
	}
}

func TestUnbind_RemovesTheBindingAndRejectsUnboundOrMissingChallenges(t *testing.T) {
	ws, ids, _ := bindWorkspace(t)
	runJSON(t, ws, "bind", ids[0], "owner/repo-a#5")

	out := runJSON(t, ws, "unbind", ids[0])
	if c, ok := out["challenge"].(map[string]any); !ok || c["id"] != ids[0] || c["version"] != 3.0 {
		t.Fatalf("output = %#v, want the challenge %s at version 3", out, ids[0])
	}
	if v := runJSON(t, ws, "show", ids[0])["source_binding"]; v != nil {
		t.Errorf("source_binding after unbind = %#v, want null", v)
	}
	acts := runJSON(t, ws, "log", ids[0])["activities"].([]any)
	if last := acts[len(acts)-1].(map[string]any); last["action"] != "unbind" || last["verification"] != "none" {
		t.Errorf("last activity = %#v, want unbind with no verification", last)
	}
	before := logLen(t, ws)

	for _, c := range []struct {
		name string
		args []string
		exit int
		code ErrorCode
	}{
		{"not bound", []string{"unbind", ids[0]}, 1, CodeValidationFailed},
		{"missing challenge", []string{"unbind", "C-99"}, 1, CodeNotFound},
		{"malformed id", []string{"unbind", "nope"}, 1, CodeNotFound},
		{"no argument", []string{"unbind"}, 2, CodeUsageError},
		{"too many arguments", []string{"unbind", ids[0], ids[1]}, 2, CodeUsageError},
	} {
		if exit, code, _ := runBindFailing(t, ws, c.args...); exit != c.exit || code != string(c.code) {
			t.Errorf("%s: exit=%d code=%q, want exit=%d code=%q", c.name, exit, code, c.exit, c.code)
		}
	}
	if after := logLen(t, ws); after != before {
		t.Errorf("activities = %d after the rejections, want %d", after, before)
	}
}

func TestUnbind_DoesNotCallGH(t *testing.T) {
	ws, ids, calls := bindWorkspace(t)
	runJSON(t, ws, "bind", ids[0], "owner/repo-a#5")
	n := len(calls())
	runJSON(t, ws, "unbind", ids[0])
	if got := len(calls()); got != n {
		t.Errorf("gh calls = %d after unbind, want %d (unbind never touches the upstream)", got, n)
	}
}

// prepareBindCase は横断の列挙テスト（allcommands_test.go・worklog_allcommands_test.go）の bind・unbind の
// 成功経路の準備: 宣言（example-owner/example-repo）と、その #1 が open の偽の gh。
func prepareBindCase(t *testing.T, ws string) {
	t.Helper()
	writeSourcesDeclaration(t, ws, validSourcesDeclaration)
	withFakeGHRoutesOnPATH(t, []fakeGHRoute{
		fakeGHGetIssueStatusLineRoute("example-owner/example-repo", 1, "HTTP/2.0 200 OK",
			marshalFakeGHIssue(t, fakeGHIssue{Number: 1, Title: "t", Body: "b", Repo: "example-owner/example-repo"}), 0),
	})
}
