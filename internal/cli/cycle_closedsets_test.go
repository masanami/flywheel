package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは #86 の AC-163・AC-164（閉集合の双方向の照合。AC-163 は #108 で S2 の集合へ広げた）を検証する。
//   - 表 ⊇ 実装: 実際の出力に現れる値は、すべて仕様の閉集合に含まれる
//   - 表 ⊆ 実装: 仕様の閉集合の全ての値が、実際の出力に少なくとも 1 回は現れる
//   - 仕様の列挙・core の定義・実際の出力の 3 者が一致する
// ingest_result_closed_set_test.go の AC-95 と同じ形。

var backtickSpanRe = regexp.MustCompile("`([^`]+)`")

// m3SpecLine は m3-invoker-delegation.md の、contains を全て含む最初の行を返す。
func m3SpecLine(t *testing.T, contains ...string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "features", "m3-invoker-delegation.md"))
	if err != nil {
		t.Fatalf("read m3 spec: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		ok := true
		for _, c := range contains {
			if !strings.Contains(line, c) {
				ok = false
				break
			}
		}
		if ok {
			return line
		}
	}
	t.Fatalf("m3 spec has no line containing %q", contains)
	return ""
}

// pipeSet は "a | b | c" を集合（ソート済み）へ分ける。
func pipeSet(s string) []string {
	var out []string
	for _, part := range strings.Split(s, "|") {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// runResultsOf は `runs --json` の全 run の result（終了していなければ ""）を返す。
func runResultsOf(t *testing.T, ws string) []string {
	t.Helper()
	var out []string
	for _, r := range runJSON(t, ws, "runs")["runs"].([]any) {
		v, _ := r.(map[string]any)["result"].(string)
		out = append(out, v)
	}
	return out
}

// AC-163・AC-367・AC-368: cycle --json の not_started[].reason の値は、S2 では cycle_budget |
// rate_limited | run_budget | slot_unavailable | failure_limit | rework_limit |
// upstream_fetch_failed | serialized | waiting_external の閉集合に限られる（仕様・core の定義・実際の
// 出力の 3 者を双方向に照合する）。
//
// 実際の出力（cycle --json）で観測できるものはここで観測する。連続失敗・差し戻しの履歴や、他の
// 課題の終了していない run を要る failure_limit・rework_limit・serialized は、core のテストが
// 同じ値を実際の周の対象の選択で観測している（coreObservedReasons に、その根拠のテストを置く）。
func TestCycle_NotStartedReasonsAreTheClosedSetInBothDirections(t *testing.T) {
	line := m3SpecLine(t, "`not_started[].reason` は", "S1 は ")
	spans := backtickSpanRe.FindAllStringSubmatch(line, -1)
	if len(spans) < 2 {
		t.Fatalf("expected the field name and the closed set as backtick spans in %q", line)
	}
	// 行の最初の span は `not_started[].reason` 自身、2 番目が S2 の全体の閉集合。
	spec := pipeSet(spans[1][1])

	var impl []string
	for _, r := range core.NotStartedReasonValues() {
		impl = append(impl, string(r))
	}
	if !equalStringSlices(spec, sortedStrings(impl)) {
		t.Fatalf("core.NotStartedReasonValues() = %v, spec set = %v", sortedStrings(impl), spec)
	}

	// core のテストが、実際の周・委譲・検証の対象の選択で観測している値。
	coreObservedReasons := map[string]string{
		"failure_limit": "TestFailureLimit_ReachedHoldsWithKindAndCountAndReportsNotStarted (internal/core/delegate_resume_test.go)",
		"rework_limit":  "TestRework_LimitIsDetectedInTheNextCycle_TwoStillLaunch (internal/core/judgment_j5_test.go)",
		"serialized":    "TestPlan_RunningInSameGroup_Serialized_OtherGroupStarts (internal/core/delegate_plan_test.go)",
	}
	observed := map[string]bool{}
	for r, ref := range coreObservedReasons {
		if !containsString(spec, r) {
			t.Errorf("coreObservedReasons has %q which is not in the spec closed set", r)
		}
		// 根拠のテストが実在し、その値の定数を参照していることを確かめる（改名・削除で気付けるように）。
		testName, file, _ := strings.Cut(ref, " (")
		src, err := os.ReadFile(filepath.Join(repoRoot(t), strings.TrimSuffix(file, ")")))
		if err != nil || !strings.Contains(string(src), "func "+testName+"(") || !strings.Contains(string(src), notStartedConstFor(r)) {
			t.Errorf("coreObservedReasons[%q]: %s must exist and reference %s (err=%v)", r, ref, notStartedConstFor(r), err)
		}
		observed[r] = true
	}
	observe := func(doc map[string]any) {
		assertDocumentedCycle(t, loadDocumentedJSON(t), doc)
		for _, p := range doc["phases"].([]any) {
			ns, _ := p.(map[string]any)["not_started"].([]any)
			for _, n := range ns {
				observed[n.(map[string]any)["reason"].(string)] = true
			}
		}
	}

	t.Run("cycle_budget", func(t *testing.T) {
		ws := initializedWorkspace(t)
		writePositionFileForTest(t, ws, "pos")
		// 上限 1.5・J1 の上限 1（既定）・1 回 0.8 の費用: 2 件目は 0.8 + 1 > 1.5 で起動できない。
		writeAgentJSONForTest(t, ws, `{"position_file": "position.md", "cycle_budget_usd": 1.5}`)
		createTitled(t, ws, "a")
		createTitled(t, ws, "b")
		putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{{Match: fakeClaudeJ1Match, Tag: "J1",
			Stdout: `{"is_error":false,"total_cost_usd":0.8,"structured_output":{"verdict":"not_mine","reason":"r"}}`}}, "")
		observe(cycleDoc(t, ws))
	})
	t.Run("rate_limited", func(t *testing.T) {
		ws := setupWorkspaceWithPosition(t)
		createTitled(t, ws, "a")
		createTitled(t, ws, "b")
		putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{{Match: fakeClaudeJ1Match, Tag: "J1",
			Stdout: `{"is_error":true,"result":"You've hit your limit"}`}}, "")
		doc := cycleDoc(t, ws)
		if doc["rate_limited"] != true {
			t.Errorf("rate_limited = %v, want true", doc["rate_limited"])
		}
		observe(doc)
	})
	t.Run("upstream_fetch_failed", func(t *testing.T) {
		ws := setupJ2Workspace(t)
		id := newClassifiedForCase(t, ws)
		bindChallengeToIssue(t, ws, id, 7, 2, "2026-09-25T09:00:00.000Z")
		withFakeGHRoutesOnPATH(t, nil) // どの取得も失敗する
		putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j2RoutePlan(t)}, "")
		observe(cycleDoc(t, ws))
	})

	t.Run("slot_unavailable", func(t *testing.T) {
		ws := setupRunWorkspace(t)
		newInProgressForRun(t, ws, nil)
		if err := os.WriteFile(filepath.Join(ws, "slot-d", "dirty.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j3Route("b"), delegateRoute("completed")}, "")
		observe(cycleDoc(t, ws))
	})
	t.Run("run_budget", func(t *testing.T) {
		ws := setupRunWorkspace(t)
		// 実装枠が起動の最小額（1 USD）に満たない計画。
		newInProgressForRun(t, ws, func(m map[string]any) { m["budget_impl_usd"] = 0.5 })
		putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j3Route("b"), delegateRoute("completed")}, "")
		observe(cycleDoc(t, ws))
	})
	t.Run("waiting_external", func(t *testing.T) {
		ws := setupRunWorkspace(t)
		newVerifyingForCase(t, ws, true)
		withFakeGHRoutesOnPATH(t, ghChecksRoutes("open", checkRunsPending))
		putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j5Route("met", nil, nil)}, "")
		observe(cycleDoc(t, ws))
	})

	var got []string
	for r := range observed {
		got = append(got, r)
	}
	if !equalStringSlices(sortedStrings(got), spec) {
		t.Errorf("observed not_started reasons = %v, want exactly the S2 closed set %v (each value must appear, and no other)", sortedStrings(got), spec)
	}
}

// AC-164: run の結果の値は launch_failed | timed_out | malformed | budget_exhausted | errored |
// invalid_output | succeeded | interrupted の閉集合に限られる（仕様・core の定義・実際の run の
// 結果を双方向に照合する）。
func TestRunResults_AreTheDocumentedClosedSetInBothDirections(t *testing.T) {
	line := m3SpecLine(t, "run の結果の値は `", "閉集合に限られる")
	m := backtickSpanRe.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("no backtick span in %q", line)
	}
	spec := pipeSet(m[1])

	var impl []string
	for _, r := range core.RunResultValues() {
		impl = append(impl, string(r))
	}
	if !equalStringSlices(spec, sortedStrings(impl)) {
		t.Fatalf("core.RunResultValues() = %v, spec set = %v", sortedStrings(impl), spec)
	}

	observed := map[string]bool{}
	// scenario は J1 の応答（偽の claude の標準出力）を変えた cycle を 1 回実行し、runs に現れた
	// result を観測する。agentJSON は agent.json の内容（空なら position_file だけ）。
	scenario := func(name, stdout, agentJSON string, sleepFirst int) {
		t.Run(name, func(t *testing.T) {
			ws := setupWorkspaceWithPosition(t)
			if agentJSON != "" {
				writeAgentJSONForTest(t, ws, agentJSON)
			}
			createTitled(t, ws, "a")
			putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{{Match: fakeClaudeJ1Match, Tag: "J1", Stdout: stdout, SleepFirstSeconds: sleepFirst}}, "")
			cycleDoc(t, ws) // 周の中の失敗でも終了コード 0
			results := runResultsOf(t, ws)
			if len(results) != 1 || results[0] != name {
				t.Fatalf("run results = %v, want exactly [%s]", results, name)
			}
			observed[results[0]] = true
		})
	}
	scenario("succeeded", j1MineFixture("P1"), "", 0)
	scenario("errored", `{"is_error":true,"result":"boom"}`, "", 0)
	scenario("malformed", `this is not a json object`, "", 0)
	scenario("budget_exhausted", `{"subtype":"error_max_budget_usd","is_error":true}`, "", 0)
	scenario("invalid_output", `{"is_error":false,"structured_output":{"verdict":"bogus","reason":"r"}}`, "", 0)
	scenario("timed_out", j1MineFixture("P1"), `{"position_file": "position.md", "timeout_sec": {"judgment": 1}}`, 3)

	t.Run("launch_failed", func(t *testing.T) {
		ws := setupWorkspaceWithPosition(t)
		createTitled(t, ws, "a")
		// PATH 上に実行ビットのある claude はあるが、解釈系が無く起動できない。
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/nonexistent/interpreter\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		cycleDoc(t, ws)
		results := runResultsOf(t, ws)
		if len(results) != 1 || results[0] != "launch_failed" {
			t.Fatalf("run results = %v, want exactly [launch_failed]", results)
		}
		observed["launch_failed"] = true
	})
	t.Run("interrupted", func(t *testing.T) {
		ws := setupWorkspaceWithPosition(t)
		id := createTitled(t, ws, "a")
		// そのホストで生きていない保持者の、heartbeat の古い終了していない run は、次にストアを
		// 開いたコマンドが interrupted で閉じる。
		insertRunFixture(t, ws, id, func(in *coretest.InsertRunInput) {
			in.PID = deadPID(t)
			in.Host = currentHost(t)
		})
		results := runResultsOf(t, ws)
		if len(results) != 1 || results[0] != "interrupted" {
			t.Fatalf("run results = %v, want exactly [interrupted]", results)
		}
		observed["interrupted"] = true
	})

	var got []string
	for r := range observed {
		got = append(got, r)
	}
	if !equalStringSlices(sortedStrings(got), spec) {
		t.Errorf("observed run results = %v, want exactly the closed set %v", sortedStrings(got), spec)
	}
}

// notStartedConstFor は not_started の理由の値に対応する core の定数名。
func notStartedConstFor(reason string) string {
	switch reason {
	case "failure_limit":
		return "NotStartedFailureLimit"
	case "rework_limit":
		return "NotStartedReworkLimit"
	case "serialized":
		return "NotStartedSerialized"
	}
	return "?"
}
