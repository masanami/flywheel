package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは #102（親要件チケット #98 §J3 ブリーフと委譲の起動）の `flywheel run` を、実ストア・
// 実物の git リポジトリ（スロット）・偽の claude・偽の gh で検証する（AC-190〜217・281・361・365 の
// CLI 側）。本物の claude・gh・harness は起動しない。

const runConnectorsFixture = `{
  "version": 1,
  "connectors": [
    {"id": "harness", "form": "plugin", "permission_mode": "acceptEdits", "operations": [
      {"id": "impl-op", "invocation": "/h:impl {issue_number}", "interactive": false, "child_may_decide": true, "artifacts": "pr"}
    ]},
    {"id": "direct", "form": "brief", "permission_mode": "auto", "operations": [
      {"id": "brief-op", "interactive": false, "child_may_decide": true, "artifacts": "pr"}
    ]}
  ],
  "repos": [
    {"name": "flywheel-repo", "remote": "o/flywheel", "default_branch": "main", "connector": "harness", "slots": {"provider": "clone", "paths": ["slot-f"]}},
    {"name": "direct-repo", "remote": "o/direct", "default_branch": "main", "connector": "direct", "slots": {"provider": "clone", "paths": ["slot-d"]}}
  ]
}`

const (
	fakeClaudeJ3Match       = `"brief"`
	fakeClaudeDelegateMatch = `"pr_urls"`
)

func j3Route(brief string) fakeClaudeRoute {
	return fakeClaudeRoute{Match: fakeClaudeJ3Match, Tag: "J3",
		Stdout: `{"session_id":"s","is_error":false,"total_cost_usd":0.4,"structured_output":{"brief":"` + brief + `"}}`}
}

func delegateRoute(outcome string) fakeClaudeRoute {
	return fakeClaudeRoute{Match: fakeClaudeDelegateMatch, Tag: "DELEGATE",
		Stdout: `{"session_id":"s","is_error":false,"total_cost_usd":2.5,"structured_output":{"outcome":"` + outcome + `","summary":"s","branch":null,"pr_urls":[],"commits":[],"quality_gate":null,"assumptions":[],"unverified":[],"questions":[]}}`}
}

// setupRunWorkspace は宣言（agent.json・connectors.json）と、クリーンなスロット（実物の git
// リポジトリ。origin は宣言の remote）を持つワークスペースを作る。
func setupRunWorkspace(t *testing.T) string {
	t.Helper()
	ws := setupWorkspaceWithPosition(t)
	writeConnectorsJSONForTest(t, ws, runConnectorsFixture)
	slotClone(t, filepath.Join(ws, "slot-f"), "https://github.com/o/flywheel.git")
	slotClone(t, filepath.Join(ws, "slot-d"), "git@github.com:o/direct.git")
	return ws
}

// newInProgressForRun は着手中で承認済みの計画（repo・operation を mut で選べる）を持つ課題を作る。
func newInProgressForRun(t *testing.T, ws string, mut func(m map[string]any)) string {
	t.Helper()
	id := createForCase(t, ws)
	_, output := j2PlanFixture(t, mut)
	spec, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	coretest.InsertApprovedPlan(t, ws, challengeIDToInternalID(t, id), "PLAN-BODY-CLI", string(spec))
	return id
}

func delegateRunsOf(t *testing.T, ws string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range runJSON(t, ws, "runs")["runs"].([]any) {
		m := r.(map[string]any)
		if m["kind"] == "delegate" {
			out = append(out, m)
		}
	}
	return out
}

// 成功: {"phase": {...}} の形・委譲の run の記録・固定の節つきの標準入力の保存。
func TestRun_DelegatesAndReturnsAPhaseObject(t *testing.T) {
	parallelTools(t, false)
	ws := setupRunWorkspace(t)
	id := newInProgressForRun(t, ws, nil)
	orderLog := filepath.Join(t.TempDir(), "order.log")
	child := delegateRoute("completed")
	// 子がスロットで今回のブランチを切る（割り当て時と異なる現在のブランチが照合先になる）。
	child.ShellBefore = "git -C " + shellSingleQuote(filepath.Join(ws, "slot-d")) + " checkout -q -b feat/child"
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{child, j3Route("BRIEF-CLI-MARKER")}, orderLog)
	// 委譲の後の照合（報告にブランチが無いので、スロットの現在のブランチ feat/child で調べる）。
	ghCalls := withFakeGHRoutesOnPATH(t, []fakeGHRoute{
		{match: "repos/o/direct/branches/feat/child", stdout: "HTTP/2.0 200 OK\r\n\r\n{\"name\":\"feat/child\"}", exit: 0},
		{match: "repos/o/direct/pulls?head=o%3Afeat%2Fchild&page=1&per_page=100&state=all", stdout: `[{"html_url":"https://github.com/o/flywheel/pull/9","title":"t","state":"open","merged_at":null,"base":{"ref":"develop"}}]`, exit: 0},
	})

	out := runJSON(t, ws, "run")
	if len(out) != 1 {
		t.Fatalf("top-level keys = %v, want only phase", keysOf(out))
	}
	phase := phaseOf(t, out)
	if phase["phase"] != "run" || phase["skipped"] != false {
		t.Errorf("phase = %v", phase)
	}
	items := phase["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", items)
	}
	if _, ok := phase["serial_groups"]; ok {
		t.Errorf("phase has a serial_groups key: %v", phase)
	}
	it := items[0].(map[string]any)
	if it["challenge_id"] != id || it["result"] != "succeeded" || it["outcome"] != "completed" || it["status"] != "verifying" {
		t.Errorf("item = %v", it)
	}
	if got := orderLogTags(readOrderLog(t, orderLog)); strings.Join(got, ",") != "claude J3,claude DELEGATE" {
		t.Errorf("claude calls = %v, want J3 then the delegation", got)
	}

	runs := delegateRunsOf(t, ws)
	if len(runs) != 1 || runs[0]["result"] != "succeeded" || runs[0]["challenge_id"] != id {
		t.Fatalf("delegate runs = %v", runs)
	}
	stdin, err := os.ReadFile(filepath.Join(ws, ".flywheel", "runs", runs[0]["id"].(string), "stdin.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"BRIEF-CLI-MARKER", "意思決定者: child", "該当した行: 4", "Closes #<Issue 番号>"} {
		if !strings.Contains(string(stdin), want) {
			t.Errorf("delegation stdin lacks %q", want)
		}
	}
	// 照合の gh はすべて GET（api サブコマンドで、書き込みのフラグを持たない）。
	calls := ghCalls()
	if len(calls) != 2 {
		t.Fatalf("gh calls = %v, want the branch and the pull request lookups", calls)
	}
	for _, c := range calls {
		c = strings.TrimPrefix(c, "CALL ")
		if !strings.HasPrefix(c, "api ") || strings.Contains(c, " -X") || strings.Contains(c, "--method") || strings.Contains(c, " -f ") || strings.Contains(c, " -F ") {
			t.Errorf("gh call is not a plain GET: %q", c)
		}
	}
	// 委譲の後、スロットは idle に戻る。
	status := runJSON(t, ws, "status")
	if nh, _ := status["needs_human"].(map[string]any); nh != nil && len(nh["slots"].([]any)) != 0 {
		t.Errorf("needs_human.slots = %v", nh["slots"])
	}
}

// 委譲の引数: 権限モードは宣言の値・--disallowedTools・セッション ID 等（3 値のそれぞれ）。
func TestRun_ArgvOfTheDelegationPerPermissionMode(t *testing.T) {
	parallelTools(t, false)
	for _, mode := range []string{"default", "acceptEdits", "auto"} {
		t.Run(mode, func(t *testing.T) {
			ws := setupRunWorkspace(t)
			conn := strings.Replace(runConnectorsFixture, `"form": "brief", "permission_mode": "auto"`, `"form": "brief", "permission_mode": "`+mode+`"`, 1)
			writeConnectorsJSONForTest(t, ws, conn)
			newInProgressForRun(t, ws, nil)
			argvLog := filepath.Join(t.TempDir(), "argv.log")
			routes := []fakeClaudeRoute{delegateRoute("completed"), j3Route("b")}
			for i := range routes {
				routes[i].ArgvLogPath = argvLog
			}
			putRoutedFakeClaudeOnPATH(t, routes, "")

			runJSON(t, ws, "run")

			var delegateLine, j3Line string
			for _, l := range readOrderLog(t, argvLog) {
				switch {
				case strings.Contains(l, fakeClaudeDelegateMatch):
					delegateLine = l
				case strings.Contains(l, fakeClaudeJ3Match):
					j3Line = l
				}
			}
			if delegateLine == "" || j3Line == "" {
				t.Fatalf("argv log = %v", readOrderLog(t, argvLog))
			}
			for _, want := range []string{"-p ", "--session-id ", "--output-format json", "--json-schema ", "--permission-mode " + mode, "--disallowedTools Bash(flywheel:*)", "--max-budget-usd 50"} {
				if !strings.Contains(delegateLine, want) {
					t.Errorf("delegation argv lacks %q: %s", want, delegateLine)
				}
			}
			if strings.Contains(j3Line, "Bash(flywheel:*)") || !strings.Contains(j3Line, "--permission-mode default") {
				t.Errorf("J3 must use the read-only judgment arguments: %s", j3Line)
			}
		})
	}
}

func TestRun_NonTargetsAreNotStarted(t *testing.T) {
	parallelTools(t, false)
	ws := setupRunWorkspace(t)
	createForCase(t, ws) // 未分類
	newClassifiedForCase(t, ws)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{delegateRoute("completed"), j3Route("b")}, "")
	phase := phaseOf(t, runJSON(t, ws, "run"))
	if len(phase["items"].([]any)) != 0 || len(phase["not_started"].([]any)) != 0 {
		t.Errorf("phase = %v, want nothing to start", phase)
	}
	if len(delegateRunsOf(t, ws)) != 0 {
		t.Error("no delegation may be recorded")
	}
}

func TestRun_RunInProgress_ExitOneAndNothingStarted(t *testing.T) {
	parallelTools(t, false)
	ws := setupRunWorkspace(t)
	id := newInProgressForRun(t, ws, nil)
	host, _ := os.Hostname()
	coretest.InsertRun(t, ws, coretest.InsertRunInput{
		ChallengeID: challengeIDToInternalID(t, id), Kind: "judgment", Judgment: "J5", ChallengeVersion: 1,
		SessionID: "11111111-1111-4111-8111-111111111111", PID: int64(os.Getpid()), Host: host,
		HeartbeatAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), StartedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		MaxBudgetUSD: 1_000_000, BudgetBucket: "judgment",
	})
	orderLog := filepath.Join(t.TempDir(), "order.log")
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{delegateRoute("completed"), j3Route("b")}, orderLog)

	requireJSONErrorEnvelope(t, []string{"run", id, "--workspace", ws}, 1, CodeRunInProgress)
	if got := readOrderLog(t, orderLog); len(got) != 0 {
		t.Errorf("claude was started: %v", got)
	}
	// ID を省略した run は、終了していない run を持つ課題を対象にしない。
	if phase := phaseOf(t, runJSON(t, ws, "run")); len(phase["items"].([]any)) != 0 {
		t.Errorf("phase = %v", phase)
	}
}

// 先に起動した run が動いている間の 2 つ目の run は、サイクルの排他で locked（終了コード 1）になり、
// 委譲を起動しない。下の _OtherIsLocked との違いはスロットの宣言: こちらは標準の宣言（clone のスロット 1 本）、
// 下は worktree のスロット 1 本を同じリポジトリの 2 課題が取り合う宣言。どちらでも結果は locked になる。
func TestRun_SingleCloneSlot_SecondRunWhileFirstRuns_IsLocked(t *testing.T) {
	parallelTools(t, false)
	ws := setupRunWorkspace(t)
	a := newInProgressForRun(t, ws, nil)
	b := newInProgressForRun(t, ws, nil)
	started := filepath.Join(t.TempDir(), "started")
	route := delegateRoute("completed")
	route.SleepFirstSeconds = 2
	route.StartedFile = started
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{route, j3Route("b")}, "")

	type result struct {
		code   int
		stderr string
	}
	runOne := func(id string) result {
		var stdout, stderr bytes.Buffer
		code := runWithLookPath([]string{"run", id, "--workspace", ws, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands(), lookPathFor(t))
		return result{code, stderr.String()}
	}
	var wg sync.WaitGroup
	results := make([]result, 2)
	wg.Add(1)
	go func() { defer wg.Done(); results[0] = runOne(a) }()
	waitForFileWithTimeout(t, started, 30*time.Second)
	results[1] = runOne(b) // 最初の委譲がスロットを持っている間に起動する
	wg.Wait()

	if results[0].code != 0 {
		t.Errorf("first: exit=%d stderr=%s", results[0].code, results[0].stderr)
	}
	// run はサイクルと同じ排他を取るので、先の run が動いている間の 2 つ目の run は、スロットの割り当てに
	// 進む前に locked で断られる。
	if results[1].code != 1 || !strings.Contains(results[1].stderr, `"locked"`) {
		t.Errorf("second: exit=%d stderr=%s, want exit 1 locked", results[1].code, results[1].stderr)
	}
	if n := len(delegateRunsOf(t, ws)); n != 1 {
		t.Errorf("delegate runs = %d, want 1", n)
	}
}

// 同じリポジトリの 2 課題が worktree のスロット 1 本を取り合う宣言でも、動いている run があれば 2 つ目の run は
// サイクルの排他で locked になる（スロットの割り当てには進まない）。上の _SecondRunWhileFirstRuns_IsLocked との違いは
// スロットの宣言（こちらは worktree の 1 本）だけで、残す意図は宣言の種類によらず排他が先に効くこと。
func TestRun_SingleWorktreeSlot_SameRepoSecondRun_IsLocked(t *testing.T) {
	parallelTools(t, false)
	ws := setupRunWorkspace(t)
	conn := runConnectorsFixture
	conn = strings.Replace(conn, `"remote": "o/flywheel"`, `"remote": "o/r"`, 1)
	conn = strings.Replace(conn, `"slots": {"provider": "clone", "paths": ["slot-f"]}`, `"slots": {"provider": "worktree", "base": "slot-f", "count": 1}`, 1)
	slotGit(t, filepath.Join(ws, "slot-f"), "remote", "set-url", "origin", "https://github.com/o/r.git")
	writeConnectorsJSONForTest(t, ws, conn)
	mut := func(m map[string]any) { m["repo"] = "flywheel-repo"; m["operation"] = "impl-op" }
	a := newInProgressForRun(t, ws, mut)
	b := newInProgressForRun(t, ws, mut)
	bindChallengeToIssue(t, ws, a, 7, 2, "2026-09-25T09:00:00Z")
	bindChallengeToIssue(t, ws, b, 9, 0, "2026-09-25T09:00:00Z")
	withFakeGHRoutesOnPATH(t, append(j2GHRoutes(t), fakeGHRoute{match: "repos/o/r/issues/9/comments?per_page=100&page=1", stdout: "[]"}))
	started := filepath.Join(t.TempDir(), "started")
	route := delegateRoute("completed")
	route.SleepFirstSeconds = 6 // 後続が gh を通る間も、先行がスロットを持ち続ける余裕
	route.StartedFile = started
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{route, j3Route("b")}, "")

	type result struct {
		code   int
		stderr string
	}
	runOne := func(id string) result {
		var stdout, stderr bytes.Buffer
		code := runWithLookPath([]string{"run", id, "--workspace", ws, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands(), lookPathFor(t))
		return result{code, stderr.String()}
	}
	var wg sync.WaitGroup
	results := make([]result, 2)
	wg.Add(1)
	go func() { defer wg.Done(); results[0] = runOne(a) }()
	waitForFileWithTimeout(t, started, 30*time.Second)
	results[1] = runOne(b) // 最初の委譲がスロットを持っている間に起動する
	wg.Wait()

	if results[0].code != 0 {
		t.Errorf("first: exit=%d stderr=%s", results[0].code, results[0].stderr)
	}
	if results[1].code != 1 || !strings.Contains(results[1].stderr, `"locked"`) {
		t.Errorf("second: exit=%d stderr=%s, want exit 1 locked", results[1].code, results[1].stderr)
	}
	if n := len(delegateRunsOf(t, ws)); n != 1 {
		t.Errorf("delegate runs = %d, want 1", n)
	}
}

// 契約の接続ツール（form: cli）の宣言。start はすぐ戻り、実行は排他の外で走り続ける。clone のスロットは 2 本（空きは 2 件分）。
const runContractConnectorsFixture = `{
  "version": 1,
  "connectors": [
    {"id": "tool", "form": "cli", "permission_mode": "default",
     "commands": {
       "start": ["tool", "start", "--challenge", "{challenge_id}"],
       "status": ["tool", "status", "{run_id}"],
       "resume": ["tool", "resume", "{run_id}", "--answer", "{answer}"],
       "cancel": ["tool", "cancel", "{run_id}"]
     },
     "contract_version": 1,
     "operations": [{"id": "impl", "interactive": false, "child_may_decide": true, "artifacts": "pr"}]}
  ],
  "repos": [
    {"name": "tool-repo", "remote": "o/r", "default_branch": "main", "connector": "tool", "slots": {"provider": "clone", "paths": ["slot-a", "slot-b"]}}
  ]
}`

// putFakeContractToolOnPATH は契約の偽の接続ツール tool を PATH の先頭に足す。start は終わらない実行（running）を
// すぐ返し（run_id は引数の課題の ID から "ext-<課題の ID>"）、status も running を返す。呼ばれた引数列は logPath へ 1 行ずつ追記する。
func putFakeContractToolOnPATH(t *testing.T, logPath string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellSingleQuote(logPath) + "\n" +
		"case \"$1\" in\n" +
		"start) printf '{\"contract_version\":1,\"run_id\":\"ext-%s\",\"state\":\"running\",\"summary\":\"s\"}\\n' \"$3\";;\n" +
		"status) printf '{\"contract_version\":1,\"run_id\":\"%s\",\"state\":\"running\",\"summary\":\"s\"}\\n' \"$2\";;\n" +
		"*) exit 1;;\n" +
		"esac\n"
	dir := t.TempDir()
	if err := writeExecutable(filepath.Join(dir, "tool"), []byte(script)); err != nil {
		t.Fatalf("write fake contract tool: %v", err)
	}
	addToolDir(t, dir)
}

// 出どころ: core の見直しの区切り S1（docs/features/core-redesign.md。親は masanami/flywheel#732）の受入基準の
// 並び順の AC-3。同じリポジトリに委譲先のフローを通る委譲（form: cli の契約の実行）が走っているとき、別の
// `flywheel run <課題の ID>` は、同時に走らせる数の上限とスロットに空きがあれば、その課題を起動する。
// 契約の start はすぐ戻り、実行は排他の外で走り続けるので、1 件目の run が終わればサイクルの排他は外れている。
// 同期で走る形の委譲（指示文を直接渡す形・スキルを呼ぶ形）が走っている間は locked になる（上の 2 つのテスト。変えない）。
func TestRun_AC3_ContractRunRunning_SecondRunOfSameRepoLaunches(t *testing.T) {
	parallelTools(t, false)
	ws := setupWorkspaceWithPosition(t)
	writeAgentJSONForTest(t, ws, `{"position_file": "position.md", "max_parallel_runs": 2}`)
	writeConnectorsJSONForTest(t, ws, runContractConnectorsFixture)
	slotClone(t, filepath.Join(ws, "slot-a"), "https://github.com/o/r.git")
	slotClone(t, filepath.Join(ws, "slot-b"), "https://github.com/o/r.git")
	mut := func(m map[string]any) { m["repo"] = "tool-repo"; m["operation"] = "impl" }
	a := newInProgressForRun(t, ws, mut)
	b := newInProgressForRun(t, ws, mut)
	toolLog := filepath.Join(t.TempDir(), "tool.log")
	putFakeContractToolOnPATH(t, toolLog)
	// run は claude の有無を確かめるが、form: cli の委譲は claude を起動しない（どの呼び出しにも応じない偽の claude）。
	putRoutedFakeClaudeOnPATH(t, nil, "")

	runOne := func(id string) (code int, stdout, stderr string) {
		var o, e bytes.Buffer
		code = runWithLookPath([]string{"run", id, "--workspace", ws, "--json"}, strings.NewReader(""), &o, &e, defaultCommands(), lookPathFor(t))
		return code, o.String(), e.String()
	}
	unfinishedContractRuns := func() int {
		n := 0
		for _, line := range strings.Split(coretest.DumpTables(t, ws, "contract_run"), "\n") {
			if strings.HasPrefix(line, "[") && strings.Contains(line, " running ") {
				n++
			}
		}
		return n
	}

	if code, _, stderr := runOne(a); code != 0 {
		t.Fatalf("first: exit=%d stderr=%s", code, stderr)
	}
	if n := unfinishedContractRuns(); n != 1 {
		t.Fatalf("unfinished contract runs after the first run = %d, want 1", n)
	}

	code, stdout, stderr := runOne(b)
	if code != 0 {
		t.Fatalf("second: exit=%d stderr=%s, want 0 (not locked, serialized or slot_unavailable)", code, stderr)
	}
	for _, bad := range []string{`"locked"`, `"serialized"`, `"slot_unavailable"`} {
		if strings.Contains(stdout+stderr, bad) {
			t.Errorf("second: output contains %s: stdout=%s stderr=%s", bad, stdout, stderr)
		}
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("second: stdout is not JSON: %v (%q)", err, stdout)
	}
	items := phaseOf(t, doc)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["challenge_id"] != b {
		t.Errorf("second: items = %v, want %s launched", items, b)
	}
	if n := unfinishedContractRuns(); n != 2 {
		t.Errorf("unfinished contract runs after the second run = %d, want 2", n)
	}
	starts := 0
	for _, line := range readOrderLog(t, toolLog) {
		if strings.HasPrefix(line, "start ") {
			starts++
		}
	}
	if starts != 2 {
		t.Errorf("tool start calls = %d, want 2 (log: %v)", starts, readOrderLog(t, toolLog))
	}
}

func TestRun_DirtySlot_NeedsAttentionAndSlotUnavailable(t *testing.T) {
	parallelTools(t, false)
	ws := setupRunWorkspace(t)
	id := newInProgressForRun(t, ws, nil)
	if err := os.WriteFile(filepath.Join(ws, "slot-d", "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{delegateRoute("completed"), j3Route("b")}, "")
	requireJSONErrorEnvelope(t, []string{"run", id, "--workspace", ws}, 1, CodeSlotUnavailable)
	status := runJSON(t, ws, "status")
	slots := status["needs_human"].(map[string]any)["slots"].([]any)
	if len(slots) != 1 {
		t.Errorf("needs_human.slots = %v, want the dirty slot", slots)
	}
}

func TestRun_J3InvalidOutput_NoDelegation(t *testing.T) {
	parallelTools(t, false)
	ws := setupRunWorkspace(t)
	newInProgressForRun(t, ws, nil)
	bad := fakeClaudeRoute{Match: fakeClaudeJ3Match, Tag: "J3", Stdout: `{"is_error":false,"structured_output":{"brief":"b","extra":1}}`}
	orderLog := filepath.Join(t.TempDir(), "order.log")
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{delegateRoute("completed"), bad}, orderLog)

	phase := phaseOf(t, runJSON(t, ws, "run"))
	it := phase["items"].([]any)[0].(map[string]any)
	if it["result"] != "invalid_output" || it["outcome"] != nil {
		t.Errorf("item = %v", it)
	}
	if got := orderLogTags(readOrderLog(t, orderLog)); strings.Join(got, ",") != "claude J3" {
		t.Errorf("claude calls = %v, want only J3", got)
	}
	if len(delegateRunsOf(t, ws)) != 0 {
		t.Error("no delegation run may be recorded")
	}
}

func TestRun_ConfigAndEnvironmentFailuresStartNothing(t *testing.T) {
	parallelTools(t, false)
	t.Run("connectors.json missing", func(t *testing.T) {
		ws := setupWorkspaceWithPosition(t)
		argvLog, _ := putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: `{"is_error":false}`})
		before := storeBytes(t, ws)
		requireJSONErrorEnvelope(t, []string{"run", "--workspace", ws}, 2, CodeConfigNotFound)
		if string(storeBytes(t, ws)) != string(before) {
			t.Error("the store changed")
		}
		if _, err := os.Stat(argvLog); err == nil {
			t.Error("claude was invoked")
		}
	})
	t.Run("claude not on PATH", func(t *testing.T) {
		ws := setupRunWorkspace(t)
		newInProgressForRun(t, ws, nil)
		before := storeBytes(t, ws)
		emptyPATH(t)
		requireJSONErrorEnvelope(t, []string{"run", "--workspace", ws}, 2, CodeInvokerUnavailable)
		if string(storeBytes(t, ws)) != string(before) {
			t.Error("the store changed")
		}
	})
	t.Run("two positional arguments", func(t *testing.T) {
		ws := setupRunWorkspace(t)
		requireJSONErrorEnvelope(t, []string{"run", "C-1", "C-2", "--workspace", ws}, 2, CodeUsageError)
	})
}

func TestRun_SourceBound_J3GetsUpstreamViaGET_AndReadValuesStay(t *testing.T) {
	parallelTools(t, false)
	ws := setupRunWorkspace(t)
	// form: plugin は J3 を呼ばないので、J3 を呼ぶ形態（brief）の操作で確かめる。
	id := newInProgressForRun(t, ws, func(m map[string]any) { m["repo"] = "direct-repo"; m["operation"] = "brief-op" })
	bindChallengeToIssue(t, ws, id, 7, 2, "2026-09-25T09:00:00Z")
	ghCalls := withFakeGHRoutesOnPATH(t, j2GHRoutes(t))
	stdinLog := filepath.Join(t.TempDir(), "j3.stdin")
	route := j3Route("b")
	route.StdinLogPath = stdinLog
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{delegateRoute("completed"), route}, "")

	runJSON(t, ws, "run", id)

	stdin, err := os.ReadFile(stdinLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"UP-BODY-CLI-MARKER", "UP-COMMENT-1-CLI", "UP-COMMENT-2-CLI", "PLAN-BODY-CLI"} {
		if !strings.Contains(string(stdin), want) {
			t.Errorf("J3 stdin lacks %q", want)
		}
	}
	for _, c := range ghCalls() {
		if strings.Contains(c, "-X") || strings.Contains(c, "POST") || strings.Contains(c, "PATCH") {
			t.Errorf("gh call is not a GET: %s", c)
		}
	}
	sb, _ := runJSON(t, ws, "show", id)["source_binding"].(map[string]any)
	if sb == nil || sb["read_comments_count"] != float64(0) {
		t.Errorf("source_binding = %v, the read values must not change", sb)
	}
}

// AC-217: run が作った周の ID を、委譲の失敗の発生が値で持つ。
func TestRun_DelegationFailureOccurrenceCarriesTheCycleID(t *testing.T) {
	parallelTools(t, false)
	ws := setupRunWorkspace(t)
	newInProgressForRun(t, ws, nil)
	failing := fakeClaudeRoute{Match: fakeClaudeDelegateMatch, Tag: "DELEGATE", Stdout: `{"session_id":"s","is_error":true,"result":"boom"}`}
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{failing, j3Route("b")}, filepath.Join(t.TempDir(), "order.log"))

	runJSON(t, ws, "run")

	ids := coretest.OccurrenceCycleIDs(t, ws)
	cycles := coretest.CycleIDsByTrigger(t, ws, "run")
	if len(ids) != 1 || len(cycles) != 1 || ids[0] != cycles[0] {
		t.Fatalf("occurrence cycle ids = %v, want the 'run' cycle %v", ids, cycles)
	}
}

// run は周と同じ排他を取る: 周が動いている（生きた保持者がいる）間の run は、待たず locked（終了コード 1）で
// 断られ、claude を起動せず、スロットの作業ツリー（HEAD・状態）にも触れない。保持者のロックは残る。
func TestRun_CycleHoldsTheLock_LockedAndSlotUntouched(t *testing.T) {
	ws := setupRunWorkspace(t)
	id := newInProgressForRun(t, ws, nil)
	slotHead := func(dir string) string { return slotGit(t, dir, "rev-parse", "HEAD") }
	headBefore := slotHead(filepath.Join(ws, "slot-d"))
	holder := coretest.InsertCycle(t, ws, "other-cycle", 300_000_000, nowStamp())
	coretest.InsertLock(t, ws, holder, int64(os.Getpid()), currentHost(t), nowStamp(), nowStamp())
	orderLog := filepath.Join(t.TempDir(), "order.log")
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{delegateRoute("completed"), j3Route("b")}, orderLog)
	// ストア上のスロット（空き・割り当て中・要確認）を用意し、呼ぶ前の状態と割り当て先を控える。
	coretest.InsertSlot(t, ws, "direct-repo", "clone", filepath.Join(ws, "slot-d"), "idle", "")
	coretest.InsertSlot(t, ws, "flywheel-repo", "clone", filepath.Join(ws, "slot-f"), "needs_attention", "dirty")
	slotsBefore := listSlotsForTest(t, ws)

	requireJSONErrorEnvelope(t, []string{"run", id, "--workspace", ws}, 1, CodeLocked)
	// 断ったときの表示に、理由（周が実行中）と次にすること（周の後に打ち直す・周に任せる）が出る。
	for _, args := range [][]string{{"run", id, "--workspace", ws, "--json"}, {"run", id, "--workspace", ws}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, strings.NewReader(""), &stdout, &stderr, defaultCommands()); code != 1 {
			t.Fatalf("args=%v exit=%d, want 1 (stderr=%s)", args, code, stderr.String())
		}
		for _, want := range []string{"実行中", "周が終わってから", "打ち直す", "周に任せて"} {
			if !strings.Contains(stderr.String(), want) {
				t.Errorf("args=%v stderr lacks %q: %s", args, want, stderr.String())
			}
		}
	}
	if got := listSlotsForTest(t, ws); !reflect.DeepEqual(got, slotsBefore) {
		t.Errorf("slots in the store changed: %+v -> %+v", slotsBefore, got)
	}

	if got := readOrderLog(t, orderLog); len(got) != 0 {
		t.Errorf("claude was started: %v", got)
	}
	if got := slotHead(filepath.Join(ws, "slot-d")); got != headBefore {
		t.Errorf("slot HEAD changed: %s -> %s", headBefore, got)
	}
	if st := slotGit(t, filepath.Join(ws, "slot-d"), "status", "--porcelain"); st != "" {
		t.Errorf("slot worktree touched: %q", st)
	}
	if n := len(delegateRunsOf(t, ws)); n != 0 {
		t.Errorf("delegate runs = %d, want 0", n)
	}
	if h, found := coretest.CycleLockHolder(t, ws); !found || h != holder {
		t.Errorf("lock holder = %d (found=%v), want the original %d", h, found, holder)
	}
}

// run は取った排他を解放する: 動いている間は trigger "run" の周が保持者で、終わった後は排他が残らず、
// 続く cycle が locked にならない。
func TestRun_HoldsTheLockWhileRunningAndReleasesAfterwards(t *testing.T) {
	ws := setupRunWorkspace(t)
	newInProgressForRun(t, ws, nil)
	started := filepath.Join(t.TempDir(), "started")
	route := delegateRoute("completed")
	route.SleepFirstSeconds = 3
	route.StartedFile = started
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{route, j3Route("b")}, "")

	done := make(chan struct{})
	go func() {
		defer close(done)
		var stdout, stderr bytes.Buffer
		run([]string{"run", "--workspace", ws, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	}()
	waitForFileWithTimeout(t, started, 30*time.Second)
	if _, found := coretest.CycleLockHolder(t, ws); !found {
		t.Error("the lock is not held while run is delegating")
	}
	// run がロックを持っている間は、周（排他を取る BeginCycle）が locked で断られる。
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginCycle(context.Background(), core.BeginCycleInput{Trigger: "manual", BudgetUSD: 1, Exclusive: true}); !errors.Is(err, core.ErrLocked) {
		t.Errorf("BeginCycle while run holds the lock: err = %v, want core.ErrLocked", err)
	}
	_ = st.Close()
	<-done
	if _, found := coretest.CycleLockHolder(t, ws); found {
		t.Error("the lock is still held after run finished")
	}
}

// listSlotsForTest はストア上のスロットの一覧（状態・割り当て先の run を含む）を返す。
func listSlotsForTest(t *testing.T, ws string) []core.Slot {
	t.Helper()
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	slots, err := st.ListSlots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return slots
}
