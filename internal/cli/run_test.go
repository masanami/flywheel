package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

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
	// 直列化グループ: 候補 1 件だけのグループ。取り込み元の対応が無い候補は予測できない（not_predictable）。
	wantGroups := []any{map[string]any{"repo": "direct-repo", "challenges": []any{id}, "reasons": []any{"not_predictable"}, "prediction_head_sha": nil}}
	if !reflect.DeepEqual(phase["serial_groups"], wantGroups) {
		t.Errorf("serial_groups = %#v, want %#v", phase["serial_groups"], wantGroups)
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

// 先に起動した課題は取り込み元の対応が無く Issue 番号を渡せないため、後の呼び出しは同じ直列化グループ
// （fail-closed）に入り、委譲を起動せず serialized（終了コード 1）で終わる（AC-327・366）。
func TestRun_SingleSlot_TwoProcessesRaceForIt_OneLaunchesTheOtherIsSerialized(t *testing.T) {
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
		code := run([]string{"run", id, "--workspace", ws, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
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
	if results[1].code != 1 || !strings.Contains(results[1].stderr, `"serialized"`) {
		t.Errorf("second: exit=%d stderr=%s, want exit 1 serialized", results[1].code, results[1].stderr)
	}
	if n := len(delegateRunsOf(t, ws)); n != 1 {
		t.Errorf("delegate runs = %d, want 1", n)
	}
}

func TestRun_DirtySlot_NeedsAttentionAndSlotUnavailable(t *testing.T) {
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
	ws := setupRunWorkspace(t)
	id := newInProgressForRun(t, ws, func(m map[string]any) { m["repo"] = "flywheel-repo"; m["operation"] = "impl-op" })
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

// 衝突の予測の口（宣言の command に置いた偽の実行ファイル）が呼ばれ、結果から直列化グループができる
// （AC-297・301・334・335・347・351 の CLI 側）。`flywheel run`（ID の省略）も cycle の委譲の段と同じ
// core の処理を通る。
func TestRun_PredictionHook_GroupsFromSharedFiles_AndRecordsAPredictRun(t *testing.T) {
	ws := setupRunWorkspace(t)
	argvLog := filepath.Join(t.TempDir(), "predict.argv")
	script := filepath.Join(t.TempDir(), "predict.sh")
	// PATH が偽の実行ファイルの置き場だけでも動くよう、シェルの組み込みだけを使う。
	body := "#!/bin/sh\necho \"$@\" >> " + shellSingleQuote(argvLog) + "\n" +
		"printf '%s' '{\"schema\":\"harness.conflict-prediction/v1\",\"complete\":true,\"error\":null,\"head_sha\":\"cafe01\",\"cost_usd\":0.1,\"unknown_cost_count\":0," +
		"\"issues\":[{\"issue\":7,\"status\":\"predicted\"},{\"issue\":9,\"status\":\"predicted\"}]," +
		"\"pairs\":[{\"issues\":[7,9],\"status\":\"predicted\",\"shared_files\":[{\"path\":\"a.go\",\"merge_friendly\":false,\"ignored\":false}],\"dependency\":null}]}'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	conn := strings.Replace(runConnectorsFixture, `{"id": "harness", "form": "plugin", "permission_mode": "acceptEdits", "operations"`,
		`{"id": "harness", "form": "plugin", "permission_mode": "acceptEdits", "conflict_prediction": {"command": ["`+script+`"], "schema": "harness.conflict-prediction/v1"}, "operations"`, 1)
	// 取り込み元の Issue（o/r）が対象リポジトリの Issue であるときだけ、番号を予測の口へ渡せる。
	conn = strings.Replace(conn, `"remote": "o/flywheel"`, `"remote": "o/r"`, 1)
	slotGit(t, filepath.Join(ws, "slot-f"), "remote", "set-url", "origin", "https://github.com/o/r.git")
	writeConnectorsJSONForTest(t, ws, conn)
	mut := func(m map[string]any) { m["repo"] = "flywheel-repo"; m["operation"] = "impl-op" }
	a := newInProgressForRun(t, ws, mut)
	b := newInProgressForRun(t, ws, mut)
	bindChallengeToIssue(t, ws, a, 7, 2, "2026-09-25T09:00:00Z")
	bindChallengeToIssue(t, ws, b, 9, 0, "2026-09-25T09:00:00Z")
	withFakeGHRoutesOnPATH(t, append(j2GHRoutes(t), fakeGHRoute{match: "repos/o/r/issues/9/comments?per_page=100&page=1", stdout: "[]"}))
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{delegateRoute("completed"), j3Route("b")}, "")

	phase := phaseOf(t, runJSON(t, ws, "run"))
	groups, _ := phase["serial_groups"].([]any)
	want := []any{map[string]any{"repo": "flywheel-repo", "challenges": []any{a, b}, "reasons": []any{"shared_files"}, "prediction_head_sha": "cafe01"}}
	if !reflect.DeepEqual(groups, want) {
		t.Errorf("serial_groups = %#v, want %#v", groups, want)
	}
	if raw, _ := os.ReadFile(argvLog); strings.TrimSpace(string(raw)) != "--max-budget-usd 2 7 9" {
		t.Errorf("prediction argv = %q, want \"--max-budget-usd 2 7 9\"", raw)
	}
	var predict []map[string]any
	for _, r := range runJSON(t, ws, "runs")["runs"].([]any) {
		if m := r.(map[string]any); m["kind"] == "predict" {
			predict = append(predict, m)
		}
	}
	if len(predict) != 1 || predict[0]["challenge_id"] != nil || predict[0]["result"] != "succeeded" {
		t.Errorf("predict runs = %v", predict)
	}
	if n := len(delegateRunsOf(t, ws)); n != 2 {
		t.Errorf("delegate runs = %d, want both candidates launched one after the other", n)
	}
}
