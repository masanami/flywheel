package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは #86（親要件チケット #77・docs/features/m3-invoker-delegation.md
// §一括の操作（サイクル）・受入基準「一括の操作（サイクル）」）の `flywheel cycle` を、
// 偽の `claude`（fakeclaude_router_test.go）と偽の `gh`（ingest_fakegh_test.go）で検証する。
// 本物の `claude`・`gh` は起動しない。AC-nn は同ファイル ## 受入基準 配下の項目の通し序数。

// cycleSourcesJSON は o/r を取り込み元にする sources.json（self_assignees を明示して
// `gh api user` を呼ばせない）。
const cycleSourcesJSON = `{
  "version": 1,
  "sources": [
    {"id": "src", "type": "github-issue", "repos": ["o/r"], "self_assignees": ["someone"]}
  ]
}`

// cycleFullAgentJSON は全キーを明示した agent.json（既定値を 1 つも使わない）。
const cycleFullAgentJSON = `{
  "version": 1,
  "position_file": "position.md",
  "cycle_budget_usd": 300,
  "size_budgets_usd": {"S": {"impl": 30, "review": 25}, "M": {"impl": 50, "review": 30}, "L": {"impl": 100, "review": 40}},
  "max_run_budget_usd": 200,
  "judgment_budget_usd": {"J1": 1, "J2": 5, "J3": 3, "J4": 2, "J5": 5},
  "timeout_sec": {"judgment": 900, "delegate": 14400},
  "max_parallel_runs": 2,
  "rework_limit": 3,
  "failure_limit": 2
}`

// cycleDoc は `cycle --json` の出力の最上位（cycle・config_defaults_used・phases・rate_limited）。
func cycleDoc(t *testing.T, ws string, args ...string) map[string]any {
	t.Helper()
	return runJSON(t, ws, append([]string{"cycle"}, args...)...)
}

// cyclePhase は phases から phase 名が name の要素を返す。
func cyclePhase(t *testing.T, doc map[string]any, name string) map[string]any {
	t.Helper()
	phases, ok := doc["phases"].([]any)
	if !ok {
		t.Fatalf("doc has no phases array: %#v", doc)
	}
	for _, p := range phases {
		pm := p.(map[string]any)
		if pm["phase"] == name {
			return pm
		}
	}
	t.Fatalf("no phase %q in %v", name, doc["phases"])
	return nil
}

func phaseItems(t *testing.T, phase map[string]any) []map[string]any {
	t.Helper()
	raw, ok := phase["items"].([]any)
	if !ok {
		t.Fatalf("phase %v has no items array", phase["phase"])
	}
	out := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		out = append(out, it.(map[string]any))
	}
	return out
}

func phaseNotStarted(t *testing.T, phase map[string]any) []map[string]any {
	t.Helper()
	raw, ok := phase["not_started"].([]any)
	if !ok {
		t.Fatalf("phase %v has no not_started array", phase["phase"])
	}
	out := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		out = append(out, it.(map[string]any))
	}
	return out
}

func challengeStatusOf(t *testing.T, ws, id string) string {
	t.Helper()
	return runJSON(t, ws, "show", id)["challenge"].(map[string]any)["status"].(string)
}

func createTitled(t *testing.T, ws, title string) string {
	t.Helper()
	return runJSON(t, ws, "create", "--title", title, "--done-criteria", "d")["challenge"].(map[string]any)["id"].(string)
}

func classifyManually(t *testing.T, ws, id, priority string) {
	t.Helper()
	runJSON(t, ws, "classify", id, "--priority", priority)
}

// orderLogTitles は順序ログのうち "claude <tag> <title>" の行から、tag の呼び出しの
// 課題のタイトルを呼ばれた順に返す。
func orderLogTitles(lines []string, tag string) []string {
	var out []string
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) >= 3 && f[0] == "claude" && f[1] == tag {
			out = append(out, f[2])
		}
	}
	return out
}

func collapseConsecutive(in []string) []string {
	var out []string
	for _, s := range in {
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}

// --- AC-2 ---

// AC-2: position_file だけを持つ agent.json のワークスペースでの cycle --json の
// config_defaults_used は agent.json を含む。全キーを明示した agent.json では含まない。
func TestCycle_PositionFileOnlyAgentJSON_ConfigDefaultsUsedIncludesAgentJSON(t *testing.T) {
	ws := setupWorkspaceWithPosition(t) // agent.json は {"position_file": "position.md"} だけ
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1")}, "")

	doc := cycleDoc(t, ws)

	got, ok := doc["config_defaults_used"].([]any)
	if !ok || !reflect.DeepEqual(got, []any{"agent.json"}) {
		t.Fatalf("config_defaults_used = %#v, want [agent.json]", doc["config_defaults_used"])
	}
}

func TestCycle_FullyExplicitAgentJSON_ConfigDefaultsUsedIsEmptyArray(t *testing.T) {
	ws := initializedWorkspace(t)
	writePositionFileForTest(t, ws, "pos")
	writeAgentJSONForTest(t, ws, cycleFullAgentJSON)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1")}, "")

	doc := cycleDoc(t, ws)

	got, ok := doc["config_defaults_used"].([]any)
	if !ok || len(got) != 0 {
		t.Fatalf("config_defaults_used = %#v, want an empty array (not null)", doc["config_defaults_used"])
	}
}

// --- AC-132〜136: 段の順・skipped ---

// AC-132: sources.json のあるワークスペースの cycle は、取り込み・分類・計画の順に段を
// 実行する（偽の gh と偽の claude の呼び出しの順で検証する）。取り込みで作られた課題が
// 同じ周で分類・計画される。
func TestCycle_WithSources_RunsIngestClassifyPlanInOrder(t *testing.T) {
	ws := setupJ2Workspace(t)
	writeSourcesDeclaration(t, ws, cycleSourcesJSON)
	order := filepath.Join(t.TempDir(), "order.log")
	issue := marshalFakeGHIssue(t, fakeGHIssue{Number: 1, Title: "ingested-issue", Body: "body", Repo: "o/r", UpdatedAt: "2026-09-25T09:00:00Z"})
	withFakeGHRoutesOrderedOnPATH(t, []fakeGHRoute{
		fakeGHListRoute("o/r", 1, "["+issue+"]", 0),
		fakeGHGetIssueStatusLineRoute("o/r", 1, "HTTP/2.0 200 OK", issue, 0),
		{match: "repos/o/r/issues/1/comments?per_page=100&page=1", stdout: "[]"},
	}, order)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1"), j2RoutePlan(t)}, order)

	doc := cycleDoc(t, ws)

	var names []string
	for _, p := range doc["phases"].([]any) {
		names = append(names, p.(map[string]any)["phase"].(string))
	}
	if !reflect.DeepEqual(names, []string{"ingest", "classify", "plan"}) {
		t.Errorf("phases = %v, want [ingest classify plan]", names)
	}
	got := collapseConsecutive(orderLogTags(readOrderLog(t, order)))
	want := []string{"gh", "claude J1", "gh", "claude J2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("call order = %v, want %v (ingest's gh, J1, J2's upstream gh, J2)", got, want)
	}
	if got := challengeStatusOf(t, ws, "C-1"); got != "awaiting_plan_approval" {
		t.Errorf("C-1 status = %q, want awaiting_plan_approval (ingested, classified and planned in this cycle)", got)
	}
}

// AC-133: sources.json の無いワークスペースの cycle の出力で、ingest の段は skipped: true・
// result: null で、gh は呼ばれない。
func TestCycle_WithoutSources_IngestPhaseIsSkipped(t *testing.T) {
	ws := setupJ2Workspace(t)
	ghCalls := withFakeGHRoutesOnPATH(t, nil)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1"), j2RoutePlan(t)}, "")

	doc := cycleDoc(t, ws)

	ing := cyclePhase(t, doc, "ingest")
	if ing["skipped"] != true {
		t.Errorf("ingest.skipped = %v, want true", ing["skipped"])
	}
	if v, present := ing["result"]; !present || v != nil {
		t.Errorf("ingest.result = %v (present=%v), want an explicit null", v, present)
	}
	if calls := ghCalls(); len(calls) != 0 {
		t.Errorf("gh was invoked although there is no sources.json: %v", calls)
	}
}

// AC-134・AC-135・AC-136: connectors.json の無いワークスペースの cycle は、終了コード 0 で
// 終わり、plan の段は skipped: true（items・not_started は空）で J2 を起動せず、分類の段は
// J1 を起動する。
func TestCycle_WithoutConnectors_PlanSkippedExitZeroClassifyStillRuns(t *testing.T) {
	ws := setupWorkspaceWithPosition(t) // connectors.json は置かない
	id := createTitled(t, ws, "only-classify")
	order := filepath.Join(t.TempDir(), "order.log")
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1"), j2RoutePlan(t)}, order)

	doc := cycleDoc(t, ws) // runJSON は終了コード 0 を要求する（AC-134）

	plan := cyclePhase(t, doc, "plan")
	if plan["skipped"] != true || len(phaseItems(t, plan)) != 0 || len(phaseNotStarted(t, plan)) != 0 {
		t.Errorf("plan phase = %v, want skipped=true with empty items and not_started (AC-135)", plan)
	}
	tags := orderLogTags(readOrderLog(t, order))
	if !reflect.DeepEqual(tags, []string{"claude J1"}) {
		t.Errorf("claude calls = %v, want only J1 (AC-135 J2 not invoked / AC-136 J1 invoked)", tags)
	}
	cls := cyclePhase(t, doc, "classify")
	if cls["skipped"] != false || len(phaseItems(t, cls)) != 1 {
		t.Errorf("classify phase = %v, want executed with 1 item (AC-136)", cls)
	}
	if got := challengeStatusOf(t, ws, id); got != "classified" {
		t.Errorf("status = %q, want classified", got)
	}
}

// --- AC-137〜140: 対象 ---

// AC-137: 同じ周の分類の段で分類済になった課題は、同じ周の計画の段で J2 の対象になる。
func TestCycle_ClassifiedInThisCycleIsPlannedInThisCycle(t *testing.T) {
	ws := setupJ2Workspace(t)
	id := createTitled(t, ws, "one-shot")
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1"), j2RoutePlan(t)}, "")

	doc := cycleDoc(t, ws)

	cls := phaseItems(t, cyclePhase(t, doc, "classify"))
	if len(cls) != 1 || cls[0]["challenge_id"] != id || cls[0]["outcome"] != "mine" || cls[0]["status"] != "classified" {
		t.Fatalf("classify items = %v, want %s mine -> classified", cls, id)
	}
	plan := phaseItems(t, cyclePhase(t, doc, "plan"))
	if len(plan) != 1 || plan[0]["challenge_id"] != id || plan[0]["outcome"] != "plan" || plan[0]["status"] != "awaiting_plan_approval" {
		t.Fatalf("plan items = %v, want %s planned in the same cycle", plan, id)
	}
}

// AC-138: 各段の対象は、優先度 P0・P1・P2・未設定の順、同じ優先度では ID の昇順で処理される。
// 計画の段（P0→P1→P2 の順）と、優先度未設定の分類の段（ID の昇順）で確かめる。
func TestCycle_TargetsAreProcessedByPriorityThenID(t *testing.T) {
	ws := setupJ2Workspace(t)
	p2a := createTitled(t, ws, "p2-a") // C-1
	p0a := createTitled(t, ws, "p0-a") // C-2
	p1a := createTitled(t, ws, "p1-a") // C-3
	p0b := createTitled(t, ws, "p0-b") // C-4
	classifyManually(t, ws, p2a, "P2")
	classifyManually(t, ws, p0a, "P0")
	classifyManually(t, ws, p1a, "P1")
	classifyManually(t, ws, p0b, "P0")
	createTitled(t, ws, "u-a") // C-5（未分類。J1 の優先度は P0 にする）
	createTitled(t, ws, "u-b") // C-6
	order := filepath.Join(t.TempDir(), "order.log")
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P0"), j2RoutePlan(t)}, order)

	cycleDoc(t, ws)

	lines := readOrderLog(t, order)
	if got, want := orderLogTitles(lines, "J1"), []string{"u-a", "u-b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("J1 order = %v, want %v (ID ascending)", got, want)
	}
	// P0: C-2, C-4, C-5, C-6（ID 昇順。C-5・C-6 は同じ周の分類で P0 になった）→ P1: C-3 → P2: C-1。
	if got, want := orderLogTitles(lines, "J2"), []string{"p0-a", "p0-b", "u-a", "u-b", "p1-a", "p2-a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("J2 order = %v, want %v (P0 by ID, then P1, then P2)", got, want)
	}
}

// AC-139: 計画承認待ち・完了確認待ち・人間対応待ちの課題は、cycle のどの段でも判断の呼び出しの
// 対象にならない。
func TestCycle_DoesNotInvokeJudgmentsForChallengesWaitingForAHuman(t *testing.T) {
	ws := setupJ2Workspace(t)
	awaitingPlan := createPlannedForCase(t, ws)                                          // C-1: 計画承認待ち
	awaitingCompletion := createWithStatusForCase(t, ws, "awaiting_completion_approval") // C-2
	awaitingHuman := createForCase(t, ws)                                                // C-3
	runJSON(t, ws, "hold", awaitingHuman, "--question", "q")
	target := createTitled(t, ws, "only-target") // C-4
	order := filepath.Join(t.TempDir(), "order.log")
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1"), j2RoutePlan(t)}, order)

	cycleDoc(t, ws)

	got := readOrderLog(t, order)
	want := []string{"claude J1 only-target", "claude J2 only-target"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("judgment calls = %v, want %v (only %s is a target)", got, want, target)
	}
	for id, want := range map[string]string{awaitingPlan: "awaiting_plan_approval", awaitingCompletion: "awaiting_completion_approval", awaitingHuman: "awaiting_human"} {
		if got := challengeStatusOf(t, ws, id); got != want {
			t.Errorf("%s status = %q, want %q (unchanged)", id, got, want)
		}
	}
}

// AC-140: cycle は、承認・差し戻し・保留への回答の作業ログを残さない（残るのは経路 invoker の
// 分類・計画の遷移だけ）。
func TestCycle_RecordsNoApprovalRejectionOrAnswerActivity(t *testing.T) {
	ws := setupJ2Workspace(t)
	createPlannedForCase(t, ws)
	held := createForCase(t, ws)
	runJSON(t, ws, "hold", held, "--question", "q")
	createTitled(t, ws, "target")
	before := len(allActivities(t, ws))
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1"), j2RoutePlan(t)}, "")

	cycleDoc(t, ws)

	added := allActivities(t, ws)[before:]
	if len(added) == 0 {
		t.Fatal("cycle recorded no activity at all (the classify/plan transitions must be recorded)")
	}
	for _, a := range added {
		switch a["action"] {
		case "approve", "reject", "answer":
			t.Errorf("activity %v recorded by cycle: approve/reject/answer must never be written", a)
		case "classify", "plan":
			if a["channel"] != "invoker" || a["verification"] != "none" {
				t.Errorf("activity %v: want channel=invoker verification=none", a)
			}
		default:
			t.Errorf("unexpected activity action %v recorded by cycle", a["action"])
		}
	}
}

// --- AC-141〜145 ---

// AC-141・AC-142: --trigger の値が周の記録の契機になる。省略すると manual。
func TestCycle_TriggerIsRecorded(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"cron", []string{"--trigger", "cron"}, "cron"}, // AC-141
		{"omitted", nil, "manual"},                      // AC-142
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ws := setupWorkspaceWithPosition(t)
			putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1")}, "")

			doc := cycleDoc(t, ws, c.args...)

			cyc := doc["cycle"].(map[string]any)
			if cyc["trigger"] != c.want {
				t.Errorf("cycle.trigger = %v, want %q", cyc["trigger"], c.want)
			}
			if cyc["result"] != "completed" {
				t.Errorf("cycle.result = %v, want completed", cyc["result"])
			}
		})
	}
}

// --trigger を空文字列で明示すると、省略の manual に化けず validation_failed で終わる
// （ingest の --source と同じく、指定の有無を区別する）。周は作られない。
func TestCycle_EmptyTrigger_IsValidationFailedAndCreatesNoCycle(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1")}, "")
	requireJSONErrorEnvelope(t, []string{"cycle", "--trigger", "", "--workspace", ws}, 1, CodeValidationFailed)
	if _, found := coretest.CycleLockHolder(t, ws); found {
		t.Error("a lock row remains after a rejected cycle")
	}
}

// AC-143: cycle --json は標準出力に JSON を 1 つだけ出力する（形は jsondoc_test.go の
// 文書との照合が allCommandSuccessCases["cycle"] 経由で検証する）。
func TestCycle_JSONIsASingleDocumentOnStdout(t *testing.T) {
	ws := setupJ2Workspace(t)
	createTitled(t, ws, "doc-shape")
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1"), j2RoutePlan(t)}, "")

	var stdout, stderr bytes.Buffer
	code := run([]string{"cycle", "--workspace", ws, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q, want 0 and empty", code, stderr.String())
	}
	doc := requireSingleJSONObject(t, "cycle", stdout.String())
	for _, key := range []string{"cycle", "config_defaults_used", "phases", "rate_limited"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("cycle --json lacks %q", key)
		}
	}
}

// AC-144・AC-145: 周の中の判断の呼び出しが失敗（errored）しても、cycle は終了コード 0 で終わり、
// その課題の要素の result が errored である（課題の状態は変わらない）。
func TestCycle_ErroredRunIsShownInTheResultAndExitCodeIsZero(t *testing.T) {
	ws := setupJ2Workspace(t)
	id := createTitled(t, ws, "will-error")
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{
		{Match: fakeClaudeJ1Match, Tag: "J1", Stdout: `{"is_error":true,"result":"boom"}`},
	}, "")

	doc := cycleDoc(t, ws) // 終了コード 0（AC-144）

	items := phaseItems(t, cyclePhase(t, doc, "classify"))
	if len(items) != 1 || items[0]["challenge_id"] != id || items[0]["result"] != "errored" {
		t.Fatalf("classify items = %v, want %s with result errored (AC-145)", items, id)
	}
	if items[0]["outcome"] != nil || items[0]["status"] != nil {
		t.Errorf("item = %v, want outcome and status null for a failed run", items[0])
	}
	if got := challengeStatusOf(t, ws, id); got != "unclassified" {
		t.Errorf("status = %q, want unchanged", got)
	}
}

// AC-127 の CLI の形: cycle --json の spent_usd は、終了した run の費用の合計である。
func TestCycle_SpentUSDIsTheSumOfRunCosts(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	createTitled(t, ws, "a")
	createTitled(t, ws, "b")
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{
		{Match: fakeClaudeJ1Match, Tag: "J1", Stdout: `{"is_error":false,"total_cost_usd":0.4,"structured_output":{"verdict":"not_mine","reason":"r"}}`},
	}, "")

	doc := cycleDoc(t, ws)

	spent := doc["cycle"].(map[string]any)["spent_usd"].(float64)
	if spent < 0.7999 || spent > 0.8001 {
		t.Errorf("cycle.spent_usd = %v, want 0.8 (2 runs x 0.4)", spent)
	}
	if budget := doc["cycle"].(map[string]any)["budget_usd"]; budget != float64(300) {
		t.Errorf("cycle.budget_usd = %v, want 300", budget)
	}
}

// --- 宣言・環境の不備は周を始めない ---

func TestCycle_InvalidDeclarations_ConfigInvalidAndNothingInvoked(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, ws string)
	}{
		{"no agent.json (position_file missing)", func(t *testing.T, ws string) {
			// setupJ2Workspace が置いた agent.json を消す。
			if err := os.Remove(filepath.Join(ws, ".flywheel", "agent.json")); err != nil {
				t.Fatal(err)
			}
		}},
		{"unknown agent.json key", func(t *testing.T, ws string) {
			writeAgentJSONForTest(t, ws, `{"position_file": "position.md", "no_such_key": 1}`)
		}},
		{"position_file points to a missing file", func(t *testing.T, ws string) {
			writeAgentJSONForTest(t, ws, `{"position_file": "no-such.md"}`)
		}},
		{"invalid connectors.json", func(t *testing.T, ws string) {
			writeConnectorsJSONForTest(t, ws, `{"version": 1, "connectors": [], "repos": [], "no_such_key": 1}`)
		}},
		{"invalid sources.json", func(t *testing.T, ws string) {
			writeSourcesDeclaration(t, ws, `{"version": 1, "sources": [], "no_such_key": 1}`)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ws := setupJ2Workspace(t)
			createTitled(t, ws, "t")
			c.setup(t, ws)
			order := filepath.Join(t.TempDir(), "order.log")
			withFakeGHRoutesOrderedOnPATH(t, nil, order)
			putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1"), j2RoutePlan(t)}, order)
			before := storeBytes(t, ws)

			requireJSONErrorEnvelope(t, []string{"cycle", "--workspace", ws}, 2, CodeConfigInvalid)

			if got := readOrderLog(t, order); len(got) != 0 {
				t.Errorf("something was invoked despite the invalid declaration: %v", got)
			}
			if string(storeBytes(t, ws)) != string(before) {
				t.Error("config_invalid must not change the store")
			}
		})
	}
}

// PATH に claude が無い環境の cycle は、終了コード 2・invoker_unavailable で終わり、周も排他も
// 作らず、ストアを変えない。
func TestCycle_ClaudeNotOnPATH_InvokerUnavailableAndStoreUnchanged(t *testing.T) {
	ws := setupJ2Workspace(t)
	createTitled(t, ws, "t")
	before := storeBytes(t, ws)
	emptyPATH(t)

	requireJSONErrorEnvelope(t, []string{"cycle", "--workspace", ws}, 2, CodeInvokerUnavailable)

	if string(storeBytes(t, ws)) != string(before) {
		t.Error("invoker_unavailable must not change the store")
	}
}

// sources.json があるのに gh が PATH に無い cycle は、ingest と同じく upstream_unavailable
// （終了コード 2）で、周を始めない。
func TestCycle_SourcesWithoutGH_UpstreamUnavailable(t *testing.T) {
	ws := setupJ2Workspace(t)
	writeSourcesDeclaration(t, ws, cycleSourcesJSON)
	createTitled(t, ws, "t")
	order := filepath.Join(t.TempDir(), "order.log")
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1")}, order)
	// PATH の先頭に置いた偽の claude のディレクトリだけを残し、gh を見つからなくする。
	t.Setenv("PATH", strings.SplitN(os.Getenv("PATH"), string(os.PathListSeparator), 2)[0])
	before := storeBytes(t, ws)

	requireJSONErrorEnvelope(t, []string{"cycle", "--workspace", ws}, 2, CodeUpstreamUnavailable)

	if got := readOrderLog(t, order); len(got) != 0 {
		t.Errorf("claude was invoked although gh is unavailable: %v", got)
	}
	if string(storeBytes(t, ws)) != string(before) {
		t.Error("upstream_unavailable must not change the store")
	}
}

// --- AC-146〜150: サイクルの排他 ---

func nowStamp() string { return FormatTimestamp(time.Now().UTC()) }

func currentHost(t *testing.T) string {
	t.Helper()
	h, err := os.Hostname()
	if err != nil {
		t.Fatalf("hostname: %v", err)
	}
	return h
}

// AC-146: 生きている保持者が排他を持っている間の cycle は、終了コード 1・locked で終わり、
// 偽の gh・偽の claude が呼ばれない。保持者のロックは残る。
func TestCycle_LiveHolder_LockedAndNothingInvoked(t *testing.T) {
	ws := setupJ2Workspace(t)
	writeSourcesDeclaration(t, ws, cycleSourcesJSON)
	createTitled(t, ws, "t")
	holder := coretest.InsertCycle(t, ws, "other-cycle", 300_000_000, nowStamp())
	coretest.InsertLock(t, ws, holder, int64(os.Getpid()), currentHost(t), nowStamp(), nowStamp())
	order := filepath.Join(t.TempDir(), "order.log")
	withFakeGHRoutesOrderedOnPATH(t, nil, order)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1"), j2RoutePlan(t)}, order)

	requireJSONErrorEnvelope(t, []string{"cycle", "--workspace", ws}, 1, CodeLocked)

	if got := readOrderLog(t, order); len(got) != 0 {
		t.Errorf("gh/claude were invoked while the exclusive lock was held: %v", got)
	}
	if h, found := coretest.CycleLockHolder(t, ws); !found || h != holder {
		t.Errorf("lock holder = %d (found=%v), want the original holder %d to keep it", h, found, holder)
	}
}

// deadPID は終了済みの子プロセスの pid を返す（そのホストで生きていない保持者の再現）。
func deadPID(t *testing.T) int64 {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run true: %v", err)
	}
	return int64(cmd.Process.Pid)
}

// AC-147: heartbeat が 300 秒より古く、保持者のプロセスが生きていない排他は、次の cycle が
// 回収して取得し、古い周を interrupted で閉じる。終了後は解放される。
func TestCycle_StaleHolder_IsReclaimedAndReleased(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	createTitled(t, ws, "t")
	holder := coretest.InsertCycle(t, ws, "dead-cycle", 300_000_000, "2026-09-28T00:00:00.000Z")
	coretest.InsertLock(t, ws, holder, deadPID(t), currentHost(t), "2026-09-28T00:00:00.000Z", "2026-09-28T00:00:00.000Z")
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1")}, "")

	doc := cycleDoc(t, ws) // 終了コード 0

	if doc["cycle"].(map[string]any)["result"] != "completed" {
		t.Errorf("cycle.result = %v, want completed", doc["cycle"].(map[string]any)["result"])
	}
	if res, ended := coretest.CycleResultOf(t, ws, holder); res != "interrupted" || !ended {
		t.Errorf("the reclaimed cycle result = %q ended=%v, want interrupted and ended", res, ended)
	}
	if _, found := coretest.CycleLockHolder(t, ws); found {
		t.Error("the lock is still held after the cycle finished")
	}
}

// AC-148: cycle の終了後、排他が解放されている（続けて実行した cycle が locked にならない）。
func TestCycle_ReleasesTheLock_SecondCycleIsNotLocked(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	createTitled(t, ws, "t")
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1")}, "")

	cycleDoc(t, ws)
	if _, found := coretest.CycleLockHolder(t, ws); found {
		t.Fatal("the lock row remains after the first cycle")
	}
	doc := cycleDoc(t, ws) // 2 回目も終了コード 0

	if doc["cycle"].(map[string]any)["id"] != "Y-2" {
		t.Errorf("second cycle id = %v, want Y-2", doc["cycle"].(map[string]any)["id"])
	}
}

// AC-150: cycle の実行中も、ID を指定した classify --auto <C-ID> は排他を待たずに実行できる
// （cycle の最初の J1 が眠っている間に、別の課題への classify --auto が完了する）。
func TestCycle_RunningCycleDoesNotBlockClassifyAutoByID(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	first := createTitled(t, ws, "first")   // cycle の J1 が最初に処理して眠る
	second := createTitled(t, ws, "second") // 実行中の cycle と並行して classify --auto <C-ID> する
	started := filepath.Join(t.TempDir(), "started")
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{
		{Match: fakeClaudeJ1Match, Tag: "J1", Stdout: j1MineFixture("P1"), SleepFirstSeconds: 4, StartedFile: started},
	}, "")

	cycleDone := make(chan struct{})
	var cycleExit int
	var cycleStderr bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(cycleDone)
		var stdout bytes.Buffer
		cycleExit = run([]string{"cycle", "--workspace", ws, "--json"}, strings.NewReader(""), &stdout, &cycleStderr, defaultCommands())
	}()
	waitForFileWithTimeout(t, started, 10*time.Second)

	// この時点で cycle は排他を保持したまま、first の J1 の応答を待っている。
	if _, found := coretest.CycleLockHolder(t, ws); !found {
		t.Fatal("the running cycle does not hold the exclusive lock")
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"classify", "--auto", second, "--workspace", ws, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	select {
	case <-cycleDone:
		t.Fatal("cycle finished before classify --auto returned: the test cannot tell whether classify --auto waited for the lock")
	default:
	}
	if code != 0 {
		t.Fatalf("classify --auto %s exit = %d, want 0 while a cycle holds the lock (stderr=%s)", second, code, stderr.String())
	}
	wg.Wait()
	if cycleExit != 0 {
		t.Fatalf("cycle exit = %d (stderr=%s), want 0", cycleExit, cycleStderr.String())
	}
	for _, id := range []string{first, second} {
		if got := challengeStatusOf(t, ws, id); got != "classified" {
			t.Errorf("%s status = %q, want classified", id, got)
		}
	}
}
