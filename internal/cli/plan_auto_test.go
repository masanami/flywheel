package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは #85（親要件チケット #77・docs/features/m3-invoker-delegation.md
// §J2 計画）の `flywheel plan --auto [<C-ID>]` の CLI レベルのテスト。偽の `claude`
// （PATH の先頭）と偽の `gh` を使い、本物の `claude`・`gh` は起動しない。

const j2ConnectorsFixture = `{
  "version": 1,
  "connectors": [
    {"id": "harness", "form": "plugin", "permission_mode": "auto", "operations": [
      {"id": "impl-op", "invocation": "/h:impl {issue_number}", "interactive": false, "child_may_decide": true, "artifacts": "pr"}
    ]},
    {"id": "direct", "form": "brief", "permission_mode": "auto", "operations": [
      {"id": "brief-op", "interactive": false, "child_may_decide": true, "artifacts": "pr"}
    ]}
  ],
  "repos": [
    {"name": "flywheel-repo", "remote": "o/flywheel", "default_branch": "main", "connector": "harness"},
    {"name": "direct-repo", "remote": "o/direct", "default_branch": "main", "connector": "direct"}
  ]
}`

func writeConnectorsJSONForTest(t *testing.T, ws, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ws, ".flywheel", "connectors.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write connectors.json: %v", err)
	}
}

// j2PlanFixture は偽の claude が返す J2 の構造化出力（1 行の JSON）。
func j2PlanFixture(t *testing.T, mut func(m map[string]any)) (stdout string, output map[string]any) {
	t.Helper()
	output = map[string]any{
		"verdict":           "plan",
		"summary":           "SUMMARY-CLI-MARKER",
		"steps":             []string{"step one", "step two"},
		"repo":              "direct-repo",
		"operation":         "brief-op",
		"size":              "M",
		"done_criteria":     "DONE-CLI-MARKER",
		"budget_impl_usd":   nil,
		"budget_review_usd": nil,
		"cross_repo":        false,
		"related_repos":     []string{},
		"question":          nil,
	}
	if mut != nil {
		mut(output)
	}
	b, err := json.Marshal(map[string]any{"is_error": false, "structured_output": output})
	if err != nil {
		t.Fatal(err)
	}
	return string(b), output
}

// setupJ2Workspace は position.md・agent.json・connectors.json を置いた
// ワークスペースを返す。
func setupJ2Workspace(t *testing.T) string {
	t.Helper()
	ws := setupWorkspaceWithPosition(t)
	writeConnectorsJSONForTest(t, ws, j2ConnectorsFixture)
	return ws
}

// newClassifiedForCase は分類済の課題（P1）を作って ID を返す。
func newClassifiedForCase(t *testing.T, ws string) string {
	t.Helper()
	id := createForCase(t, ws)
	runJSON(t, ws, "classify", id, "--priority", "P1")
	return id
}

// bindChallengeToIssue は課題を o/r#<number> の Issue と対応づける（取り込み済みの
// 課題の形）。
func bindChallengeToIssue(t *testing.T, ws, id string, number int, comments int, updatedAt string) {
	t.Helper()
	key := "o/r#" + itoa(number)
	coretest.InsertSourceBinding(t, ws, challengeIDToInternalID(t, id), "src", key, "https://github.com/o/r/issues/"+itoa(number),
		"1:abc", "open", "in_policy", "2026-09-26T00:00:00.000Z")
	coretest.SetSourceBindingObservation(t, ws, challengeIDToInternalID(t, id), comments, updatedAt, 0, "")
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// j2GHRoutes は o/r#7（本文・コメント 2 件）と、その本文が参照する o/r#9 を返す偽の gh の規則。
func j2GHRoutes(t *testing.T) []fakeGHRoute {
	t.Helper()
	issue7 := marshalFakeGHIssue(t, fakeGHIssue{Number: 7, Title: "UP-TITLE", Body: "UP-BODY-CLI-MARKER see #9", Repo: "o/r", Comments: 2, UpdatedAt: "2026-09-25T09:00:00Z"})
	issue9 := marshalFakeGHIssue(t, fakeGHIssue{Number: 9, Title: "REF-TITLE", Body: "REF-BODY-CLI-MARKER", Repo: "o/r"})
	comments := `[{"body":"UP-COMMENT-1-CLI","user":{"login":"alice"},"created_at":"2026-09-25T01:00:00Z","html_url":"https://github.com/o/r/issues/7#c1"},` +
		`{"body":"UP-COMMENT-2-CLI","user":{"login":"bob"},"created_at":"2026-09-25T02:00:00Z","html_url":"https://github.com/o/r/issues/7#c2"}]`
	return []fakeGHRoute{
		fakeGHGetIssueStatusLineRoute("o/r", 7, "HTTP/2.0 200 OK", issue7, 0),
		fakeGHGetIssueStatusLineRoute("o/r", 9, "HTTP/2.0 200 OK", issue9, 0),
		{match: "repos/o/r/issues/7/comments?per_page=100&page=1", stdout: comments},
	}
}

func phaseOf(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	phase, ok := doc["phase"].(map[string]any)
	if !ok {
		t.Fatalf("output has no `phase` object: %#v", doc)
	}
	return phase
}

// --- 宣言・引数（AC-15） ---

// AC-15: connectors.json が無いワークスペースで plan --auto を実行すると、終了コード 2・
// config_not_found で終わり、偽の claude・偽の gh が起動されず、ストアが変わらない。
func TestPlanAuto_MissingConnectorsJSON_ConfigNotFound(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	id := newClassifiedForCase(t, ws)
	argvLog, _ := putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: `{"is_error":false}`})
	ghCalls := withFakeGHRoutesOnPATH(t, nil)
	before := storeBytes(t, ws)

	requireJSONErrorEnvelope(t, []string{"plan", "--auto", id, "--workspace", ws}, 2, CodeConfigNotFound)

	if got := string(storeBytes(t, ws)); got != string(before) {
		t.Error("config_not_found must not change the store")
	}
	if _, err := os.Stat(argvLog); err == nil {
		t.Error("the fake claude was invoked although connectors.json is missing")
	}
	if got := ghCalls(); len(got) != 0 {
		t.Errorf("gh was invoked although connectors.json is missing: %v", got)
	}
}

func TestPlanAuto_InvalidConnectorsJSON_ConfigInvalid(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	id := newClassifiedForCase(t, ws)
	writeConnectorsJSONForTest(t, ws, `{"version": 1, "connectors": [], "repos": [], "no_such_key": 1}`)
	argvLog, _ := putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: `{"is_error":false}`})

	requireJSONErrorEnvelope(t, []string{"plan", "--auto", id, "--workspace", ws}, 2, CodeConfigInvalid)
	if _, err := os.Stat(argvLog); err == nil {
		t.Error("the fake claude was invoked although connectors.json is invalid")
	}
}

func TestPlanAuto_MissingOrDanglingAgentJSON_ConfigInvalidAndClaudeNotInvoked(t *testing.T) {
	cases := map[string]string{
		"no agent.json":                 "",
		"position_file omitted":         `{}`,
		"position_file file is missing": `{"position_file": "no-such-file.md"}`,
	}
	for name, agent := range cases {
		t.Run(name, func(t *testing.T) {
			ws := initializedWorkspace(t)
			writeConnectorsJSONForTest(t, ws, j2ConnectorsFixture)
			if agent != "" {
				writeAgentJSONForTest(t, ws, agent)
			}
			id := newClassifiedForCase(t, ws)
			argvLog, _ := putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: `{"is_error":false}`})
			requireJSONErrorEnvelope(t, []string{"plan", "--auto", id, "--workspace", ws}, 2, CodeConfigInvalid)
			if _, err := os.Stat(argvLog); err == nil {
				t.Error("the fake claude was invoked with a broken agent declaration")
			}
		})
	}
}

// AC-27 の plan 版: PATH に claude が無ければ、終了コード 2・invoker_unavailable で終わり、
// ストアが変わらず、gh も呼ばれない。
func TestPlanAuto_ClaudeNotOnPATH_InvokerUnavailableAndStoreUnchanged(t *testing.T) {
	ws := setupJ2Workspace(t)
	id := newClassifiedForCase(t, ws)
	before := storeBytes(t, ws)
	emptyPATH(t)

	requireJSONErrorEnvelope(t, []string{"plan", "--auto", id, "--workspace", ws}, 2, CodeInvokerUnavailable)
	if string(storeBytes(t, ws)) != string(before) {
		t.Error("invoker_unavailable must not change the store")
	}
}

// AC-1 の plan 版・#83: 個別の操作も周を作り、既定の周の上限額 300 で評価する。
func TestPlanAuto_DefaultCycleBudgetIs300USD(t *testing.T) {
	ws := setupJ2Workspace(t)
	id := newClassifiedForCase(t, ws)
	stdout, _ := j2PlanFixture(t, nil)
	putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: stdout})

	runJSON(t, ws, "plan", "--auto", id)

	runs := runJSON(t, ws, "runs", id)["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("runs = %v, want 1", runs)
	}
	run := runs[0].(map[string]any)
	if run["cycle_budget_usd"] != 300.0 || run["judgment"] != "J2" {
		t.Errorf("run = %v, want judgment J2 with cycle_budget_usd 300", run)
	}
}

// --- 計画の登録・spec・入力（AC-87 の結線・AC-91・94・95・102） ---

func TestPlanAuto_MappedChallenge_RegistersPlanWithSpecAndFeedsUpstreamToJ2(t *testing.T) {
	ws := setupJ2Workspace(t)
	id := newClassifiedForCase(t, ws)
	bindChallengeToIssue(t, ws, id, 7, 0, "2026-09-20T00:00:00Z")
	ghCalls := withFakeGHRoutesOnPATH(t, j2GHRoutes(t))
	stdout, output := j2PlanFixture(t, func(m map[string]any) {
		m["repo"] = "flywheel-repo"
		m["operation"] = "impl-op"
		m["budget_impl_usd"] = 41.5
	})
	argvLog, stdinLog := putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: stdout})

	doc := runJSON(t, ws, "plan", "--auto", id)

	// --json は {"phase": {…}} の形（§IF / API）。
	phase := phaseOf(t, doc)
	if phase["phase"] != "plan" || phase["skipped"] != false {
		t.Errorf("phase = %v, want phase=plan skipped=false", phase)
	}
	items := phase["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v, want 1", items)
	}
	item := items[0].(map[string]any)
	if item["challenge_id"] != id || item["result"] != "succeeded" || item["outcome"] != "plan" || item["status"] != "awaiting_plan_approval" {
		t.Errorf("item = %v", item)
	}
	if ns := phase["not_started"].([]any); len(ns) != 0 {
		t.Errorf("not_started = %v, want []", ns)
	}

	// AC-95・AC-102: 計画が 1 版登録され、show の plans の要素の spec が構造化した出力と一致する。
	show := runJSON(t, ws, "show", id)
	if st := show["challenge"].(map[string]any)["status"]; st != "awaiting_plan_approval" {
		t.Errorf("status = %v, want awaiting_plan_approval", st)
	}
	plans := show["plans"].([]any)
	if len(plans) != 1 {
		t.Fatalf("plans = %v, want 1", plans)
	}
	plan := plans[0].(map[string]any)
	body := plan["body"].(string)
	for _, want := range []string{"flywheel-repo", "impl-op", "サイズ: M", "DONE-CLI-MARKER", "実装枠: 41.5 USD", "レビュー対応枠: 30 USD"} {
		if !strings.Contains(body, want) {
			t.Errorf("plan body does not contain %q:\n%s", want, body)
		}
	}
	if !reflect.DeepEqual(plan["spec"], normalizeJSONValue(t, output)) {
		t.Errorf("plan.spec = %v, want the structured output %v", plan["spec"], output)
	}

	// 読んだ記録: J2 の直前の取得の値（コメント 2 件・2026-09-25T09:00:00Z）。ストアの観測値は
	// (0, 2026-09-20…) のまま。
	sb := show["source_binding"].(map[string]any)
	if sb["read_comments_count"] != 2.0 || sb["read_upstream_updated_at"] != "2026-09-25T09:00:00Z" {
		t.Errorf("read record = (%v, %v), want (2, 2026-09-25T09:00:00Z)", sb["read_comments_count"], sb["read_upstream_updated_at"])
	}
	if sb["comments_count"] != 0.0 || sb["upstream_updated_at"] != "2026-09-20T00:00:00Z" {
		t.Errorf("observation = (%v, %v), want it untouched", sb["comments_count"], sb["upstream_updated_at"])
	}

	// AC-87（結線側）・AC-94: J2 の標準入力に本文・コメント・参照先・宣言の id が含まれる。
	stdin, err := os.ReadFile(stdinLog)
	if err != nil {
		t.Fatalf("read stdin log: %v", err)
	}
	for _, want := range []string{
		"UP-BODY-CLI-MARKER", "UP-COMMENT-1-CLI", "UP-COMMENT-2-CLI", "REF-BODY-CLI-MARKER",
		"flywheel-repo", "direct-repo", "impl-op", "brief-op",
	} {
		if !strings.Contains(string(stdin), want) {
			t.Errorf("J2 stdin does not contain %q", want)
		}
	}

	// J2 の呼び出しの引数: 判断点の上限額（既定 5）・出力スキーマ・読み取り専用の道具。
	argv, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	for _, want := range []string{"--max-budget-usd 5", "--json-schema", "--allowedTools Read Grep Glob"} {
		if !strings.Contains(string(argv), want) {
			t.Errorf("claude argv does not contain %q: %s", want, argv)
		}
	}

	// 上流の取得はすべて GET（gh api への書き込みの指定が無い）。
	calls := ghCalls()
	if len(calls) == 0 {
		t.Fatal("gh was never called for a challenge with a source binding")
	}
	for _, c := range calls {
		for _, forbidden := range []string{"-X", "--method", "-f ", "-F ", "--field", "--raw-field", "--input"} {
			if strings.Contains(c, forbidden) {
				t.Errorf("gh call %q looks like a write (%s)", c, forbidden)
			}
		}
	}
}

func normalizeJSONValue(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// 人が登録した計画の spec は null（show の plans の要素）。
func TestShow_PlansSpecIsNullForAHumanPlan(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	runJSON(t, ws, "classify", id, "--priority", "P1")
	runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, "human plan"))

	plans := runJSON(t, ws, "show", id)["plans"].([]any)
	if len(plans) != 1 {
		t.Fatalf("plans = %v", plans)
	}
	if spec, ok := plans[0].(map[string]any)["spec"]; !ok || spec != nil {
		t.Errorf("spec = %v (present=%v), want an explicit null", spec, ok)
	}
}

// AC-93: 取り込み元の対応の無い課題の J2 は gh を呼ばない。
func TestPlanAuto_ChallengeWithoutSource_NeverCallsGH(t *testing.T) {
	ws := setupJ2Workspace(t)
	id := newClassifiedForCase(t, ws)
	ghCalls := withFakeGHRoutesOnPATH(t, j2GHRoutes(t))
	stdout, _ := j2PlanFixture(t, nil)
	putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: stdout})

	doc := runJSON(t, ws, "plan", "--auto", id)

	if got := ghCalls(); len(got) != 0 {
		t.Errorf("gh was called for a challenge without a source binding: %v", got)
	}
	if item := phaseOf(t, doc)["items"].([]any)[0].(map[string]any); item["outcome"] != "plan" {
		t.Errorf("item = %v, want the plan registered", item)
	}
}

// AC-91: 上流の取得に失敗した課題は J2 が起動されず、状態と版が変わらず、
// not_started に upstream_fetch_failed で出る。
func TestPlanAuto_UpstreamFetchFailure_NotStartedAndNothingChanges(t *testing.T) {
	ws := setupJ2Workspace(t)
	id := newClassifiedForCase(t, ws)
	bindChallengeToIssue(t, ws, id, 7, 0, "2026-09-20T00:00:00Z")
	withFakeGHRoutesOnPATH(t, []fakeGHRoute{
		fakeGHGetIssueStatusLineRoute("o/r", 7, "HTTP/2.0 500 Internal Server Error", `{"message":"boom"}`, 1),
	})
	stdout, _ := j2PlanFixture(t, nil)
	argvLog, _ := putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: stdout})
	before := runJSON(t, ws, "show", id)["challenge"].(map[string]any)

	for _, args := range [][]string{{"plan", "--auto", id}, {"plan", "--auto"}} {
		doc := runJSON(t, ws, args...)
		phase := phaseOf(t, doc)
		notStarted := phase["not_started"].([]any)
		if len(notStarted) != 1 {
			t.Fatalf("%v: not_started = %v, want 1", args, notStarted)
		}
		ns := notStarted[0].(map[string]any)
		if ns["challenge_id"] != id || ns["reason"] != "upstream_fetch_failed" || len(ns) != 2 {
			t.Errorf("%v: not_started[0] = %v, want exactly {challenge_id, reason: upstream_fetch_failed}", args, ns)
		}
		if items := phase["items"].([]any); len(items) != 0 {
			t.Errorf("%v: items = %v, want []", args, items)
		}
	}

	if _, err := os.Stat(argvLog); err == nil {
		t.Error("J2 was started although the upstream fetch failed")
	}
	after := runJSON(t, ws, "show", id)["challenge"].(map[string]any)
	if after["status"] != "classified" || after["version"] != before["version"] {
		t.Errorf("challenge changed: before=%v after=%v", before, after)
	}
	if runs := runJSON(t, ws, "runs", id)["runs"].([]any); len(runs) != 0 {
		t.Errorf("runs = %v, want none", runs)
	}
}

// gh が PATH に無い環境の上流の取得は、呼ばれたときに失敗を返す（対応の無い課題は
// gh を要さない）。
func TestNewUpstreamThreadSource_WithoutGH_FailsOnUseOnly(t *testing.T) {
	emptyPATH(t)
	src := newUpstreamThreadSource()
	if _, err := src.GetIssueThread(context.Background(), "o/r", 1); err == nil {
		t.Error("GetIssueThread succeeded although gh is not on PATH")
	}
	if _, err := src.GetReferencedIssue(context.Background(), "o/r", 1); err == nil {
		t.Error("GetReferencedIssue succeeded although gh is not on PATH")
	}
}

// --- 判定・不正な出力・対象 ---

func TestPlanAuto_Uncertain_PutsChallengeAwaitingHumanWithQuestion(t *testing.T) {
	ws := setupJ2Workspace(t)
	id := newClassifiedForCase(t, ws)
	b, _ := json.Marshal(map[string]any{"is_error": false, "structured_output": map[string]any{"verdict": "uncertain", "question": "UNIQUE-CLI-QUESTION"}})
	putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: string(b)})

	item := phaseOf(t, runJSON(t, ws, "plan", "--auto", id))["items"].([]any)[0].(map[string]any)
	if item["outcome"] != "uncertain" || item["status"] != "awaiting_human" {
		t.Errorf("item = %v", item)
	}
	show := runJSON(t, ws, "show", id)
	holds := show["holds"].([]any)
	if len(holds) != 1 || holds[0].(map[string]any)["question"] != "UNIQUE-CLI-QUESTION" {
		t.Errorf("holds = %v", holds)
	}
}

func TestPlanAuto_InvalidOutput_LeavesChallengeClassified(t *testing.T) {
	ws := setupJ2Workspace(t)
	id := newClassifiedForCase(t, ws)
	stdout, _ := j2PlanFixture(t, func(m map[string]any) { m["repo"] = "no-such-repo" })
	putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: stdout})

	item := phaseOf(t, runJSON(t, ws, "plan", "--auto", id))["items"].([]any)[0].(map[string]any)
	if item["result"] != "invalid_output" || item["outcome"] != nil || item["status"] != nil {
		t.Errorf("item = %v, want invalid_output with null outcome and status", item)
	}
	show := runJSON(t, ws, "show", id)
	if show["challenge"].(map[string]any)["status"] != "classified" || len(show["plans"].([]any)) != 0 {
		t.Errorf("challenge changed: %v", show)
	}
}

func TestPlanAuto_AllTargets_OnlyClassifiedChallengesAreProcessed(t *testing.T) {
	ws := setupJ2Workspace(t)
	classified := newClassifiedForCase(t, ws)
	unclassified := createForCase(t, ws)
	stdout, _ := j2PlanFixture(t, nil)
	putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: stdout})

	items := phaseOf(t, runJSON(t, ws, "plan", "--auto"))["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["challenge_id"] != classified {
		t.Errorf("items = %v, want only %s", items, classified)
	}
	if st := runJSON(t, ws, "show", unclassified)["challenge"].(map[string]any)["status"]; st != "unclassified" {
		t.Errorf("unclassified challenge status = %v", st)
	}
}

func TestPlanAuto_ByID_NonClassifiedIsInvalidTransition(t *testing.T) {
	ws := setupJ2Workspace(t)
	id := createForCase(t, ws) // 未分類
	stdout, _ := j2PlanFixture(t, nil)
	argvLog, _ := putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: stdout})

	requireJSONErrorEnvelope(t, []string{"plan", "--auto", id, "--workspace", ws}, 1, CodeInvalidTransition)
	if _, err := os.Stat(argvLog); err == nil {
		t.Error("J2 was started for an unclassified challenge")
	}
	requireJSONErrorEnvelope(t, []string{"plan", "--auto", "C-999", "--workspace", ws}, 1, CodeNotFound)
}

// #83 の CLI の形: ID を指定した plan --auto が周の上限で起動できなければ、終了コード 1・
// budget_exceeded で終わる。
func TestPlanAuto_ByID_CycleBudgetExceeded(t *testing.T) {
	ws := initializedWorkspace(t)
	writePositionFileForTest(t, ws, "pos")
	writeAgentJSONForTest(t, ws, `{"position_file": "position.md", "cycle_budget_usd": 0.5}`)
	writeConnectorsJSONForTest(t, ws, j2ConnectorsFixture)
	id := newClassifiedForCase(t, ws)
	stdout, _ := j2PlanFixture(t, nil)
	argvLog, _ := putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: stdout})

	requireJSONErrorEnvelope(t, []string{"plan", "--auto", id, "--workspace", ws}, 1, CodeBudgetExceeded)
	if _, err := os.Stat(argvLog); err == nil {
		t.Error("J2 was started beyond the cycle budget")
	}
}

// --- 引数（AC-…の CLI 共通: 手動の引数との同時指定） ---

func TestPlanAuto_WithManualArguments_UsageError(t *testing.T) {
	ws := setupJ2Workspace(t)
	id := newClassifiedForCase(t, ws)
	planFile := writePlanFileForTest(t, "manual body")
	argvLog, _ := putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: `{"is_error":false}`})
	before := storeBytes(t, ws)

	for _, args := range [][]string{
		{"plan", "--auto", "--file", planFile},
		{"plan", id, "--auto", "--file", planFile},
		{"plan", id, "--auto", "--stdin"},
		{"plan", id}, // どれも指定しない
	} {
		requireJSONErrorEnvelope(t, append(append([]string{}, args...), "--workspace", ws), 2, CodeUsageError)
	}
	if string(storeBytes(t, ws)) != string(before) {
		t.Error("usage errors must not change the store")
	}
	if _, err := os.Stat(argvLog); err == nil {
		t.Error("claude was invoked on a usage error")
	}
}

// 手動の plan（--file）は従来どおり動く。
func TestPlan_ManualFileStillRegistersAPlan(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	runJSON(t, ws, "classify", id, "--priority", "P0")
	doc := runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, "manual"))
	plan := doc["plan"].(map[string]any)
	if plan["version"] != 1.0 || plan["body"] != "manual" {
		t.Errorf("plan = %v", plan)
	}
	if _, ok := plan["spec"]; ok {
		t.Errorf("the single `plan` output object must not gain a spec key: %v", plan)
	}

}
