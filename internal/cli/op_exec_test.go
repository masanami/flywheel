package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは `flywheel op exec` と `cycle` の最初の段 `release`（M4.5 S1 #310）の CLI のテスト。
// 偽の gh（状態を持つ）と偽の claude を PATH の先頭に置き、本物は起動しない。

const (
	execHead   = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	execHead2  = "b2c3d4e5f60718293a4b5c6d7e8f901234567890"
	execMerged = "deadbeef00000000000000000000000000000000"
)

// releaseGH は PR o/r#1 を扱う、状態を持つ偽の gh。`gh pr merge` が成功すると PR は merged になる。
type releaseGH struct{ dir string }

func newReleaseGH(t *testing.T, head string) *releaseGH {
	t.Helper()
	dir := t.TempDir()
	g := &releaseGH{dir: dir}
	g.write(t, "head", head)
	g.write(t, "base", "main")
	script := fmt.Sprintf(`#!/bin/sh
D=%s
printf 'CALL %%s\n' "$*" >> "$D/calls.log"
case "$*" in
*"pr merge "*)
  if [ -f "$D/merge_fail" ]; then echo "merge refused" >&2; exit 1; fi
  : > "$D/merged"
  exit 0;;
*repos/o/r/pulls/1)
  HEAD=$(cat "$D/head")
  BASE=$(cat "$D/base")
  if [ -f "$D/merged" ]; then
    printf '{"html_url":"https://github.com/o/r/pull/1","title":"PR","state":"closed","merged_at":"2026-10-23T00:00:00Z","base":{"ref":"%%s"},"head":{"sha":"%%s"},"mergeable":null,"merge_commit_sha":"%s"}' "$BASE" "$HEAD"
  else
    printf '{"html_url":"https://github.com/o/r/pull/1","title":"PR","state":"open","merged_at":null,"base":{"ref":"%%s"},"head":{"sha":"%%s"},"mergeable":true}' "$BASE" "$HEAD"
  fi
  exit 0;;
*check-runs*) echo '{"total_count":0,"check_runs":[]}'; exit 0;;
*status*) echo '{"state":"success","total_count":0,"statuses":[]}'; exit 0;;
*) echo "fake gh: no route for: $*" >&2; exit 1;;
esac
`, shellSingleQuote(dir), execMerged)
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return g
}

func (g *releaseGH) write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(g.dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (g *releaseGH) calls(t *testing.T) []string {
	return readFakeGHCallLog(t, filepath.Join(g.dir, "calls.log"))
}

func (g *releaseGH) mergeCalls(t *testing.T) []string {
	var out []string
	for _, c := range g.calls(t) {
		if strings.Contains(c, "pr merge") {
			out = append(out, c)
		}
	}
	return out
}

// execWorkspace は宣言（agent.json・connectors.json）を置いたワークスペースに、状態 done の課題と
// 承認済みの release（PR o/r#1・承認した head は execHead）を 1 件作る。
func execWorkspace(t *testing.T) (ws, challengeID, opID string) {
	t.Helper()
	ws = setupWorkspaceWithPosition(t)
	challengeID, opID = prepareExecWorkspace(t, ws)
	return ws, challengeID, opID
}

// prepareExecWorkspace は初期化済みの ws に execWorkspace と同じ内容を置く（agent.json も書く）。
func prepareExecWorkspace(t *testing.T, ws string) (challengeID, opID string) {
	t.Helper()
	writePositionFileForTest(t, ws, "pos")
	writeAgentJSONForTest(t, ws, `{"position_file": "position.md"}`)
	declareReleaseRepo(t, ws)
	challengeID = createWithStatusForCase(t, ws, "done")
	op := coretest.InsertApprovedRelease(t, ws, challengeIDToInternalID(t, challengeID), releasePRURL(1), execHead, "2026-10-22T00:00:00.000Z")
	return challengeID, fmt.Sprintf("OP-%d", op)
}

func execItems(t *testing.T, doc map[string]any) (phase map[string]any, items, notStarted []any) {
	t.Helper()
	phase, ok := doc["phase"].(map[string]any)
	if !ok {
		t.Fatalf("doc has no phase object: %#v", doc)
	}
	items, _ = phase["items"].([]any)
	notStarted, _ = phase["not_started"].([]any)
	return phase, items, notStarted
}

func opExecEnvelope(t *testing.T, ws string, wantExit int, wantCode ErrorCode, args ...string) {
	t.Helper()
	requireJSONErrorEnvelope(t, append(append([]string{"op", "exec"}, args...), "--workspace", ws), wantExit, wantCode)
}

// AC-38・AC-46・AC-123: 承認した head と一致する open の PR は gh pr merge を 1 回呼んでマージされ、show にマージの
// コミットが出る。出力は {"phase": {…}} の形で、items は 7 つのキーを持つ。claude は起動しない。
func TestOpExec_MergesApprovedReleaseAndShowsTheMergeCommit(t *testing.T) {
	ws, cid, op := execWorkspace(t)
	gh := newReleaseGH(t, execHead)
	// 生きている別の保持者が排他ロックを持っていても、op exec は取らないので実行できる。
	holder := coretest.InsertCycle(t, ws, "other-cycle", 300_000_000, nowStamp())
	coretest.InsertLock(t, ws, holder, int64(os.Getpid()), currentHost(t), nowStamp(), nowStamp())
	claudeLog := filepath.Join(t.TempDir(), "claude.log")
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{{Match: "", Tag: "ANY", Stdout: `{}`}}, claudeLog)

	doc := runJSON(t, ws, "op", "exec")

	phase, items, notStarted := execItems(t, doc)
	if len(doc) != 1 || phase["phase"] != "release" || phase["skipped"] != false {
		t.Errorf("doc = %#v, want only {phase: {phase: release, skipped: false, …}}", doc)
	}
	if len(items) != 1 || len(notStarted) != 0 {
		t.Fatalf("items = %v, not_started = %v, want one item", items, notStarted)
	}
	it := items[0].(map[string]any)
	if len(it) != 7 {
		t.Errorf("item keys = %v, want the 7 documented keys", it)
	}
	want := map[string]any{"operation_id": op, "challenge_id": cid, "result": "merged", "approved_head": execHead,
		"observed_head": execHead, "merge_commit": execMerged, "consecutive_failures": float64(0)}
	for k, v := range want {
		if it[k] != v {
			t.Errorf("item[%s] = %v, want %v", k, it[k], v)
		}
	}
	merges := gh.mergeCalls(t)
	if len(merges) != 1 || !strings.Contains(merges[0], "--squash") || !strings.HasSuffix(merges[0], "--match-head-commit "+execHead) {
		t.Errorf("gh pr merge calls = %v, want one squash call with --match-head-commit %s", merges, execHead)
	}
	if strings.Contains(strings.Join(merges, "\n"), "--admin") || strings.Contains(strings.Join(merges, "\n"), "--auto") {
		t.Errorf("gh pr merge must not carry --admin/--auto: %v", merges)
	}

	shown := runJSON(t, ws, "show", cid)["operations"].([]any)[0].(map[string]any)
	if shown["state"] != "executed" || shown["merge_commit"] != execMerged {
		t.Errorf("show operation = %v, want state executed and merge_commit %s", shown, execMerged)
	}
	if got := readOrderLog(t, claudeLog); len(got) != 0 {
		t.Errorf("claude was invoked by op exec: %v", got)
	}
	triggers, locks := coretest.CycleTriggers(t, ws)
	if len(triggers) != 2 || triggers[1] != "op exec" || locks != 1 {
		t.Errorf("cycle triggers = %v, locks = %d, want the holder's row plus one 'op exec' row and the holder's lock untouched", triggers, locks)
	}
}

// 受入基準: CLAUDECODE を設定し標準入力をパイプにした子プロセスとして起動しても実行する。
func TestOpExec_RunsUnderClaudeCodeWithPipedStdin(t *testing.T) {
	ws, _, _ := execWorkspace(t)
	gh := newReleaseGH(t, execHead)

	code, stdout, stderr := runPiped(t, []string{"op", "exec", "--workspace", ws, "--json"}, claudeCodeEnvVar+"=1")

	if code != 0 || !strings.Contains(stdout, `"result":"merged"`) {
		t.Fatalf("exit = %d, stdout = %s, stderr = %s, want a merge", code, stdout, stderr)
	}
	if len(gh.mergeCalls(t)) != 1 {
		t.Errorf("merge calls = %v, want 1", gh.mergeCalls(t))
	}
}

// 委譲の目印のある環境は verification_rejected で終わり、gh を呼ばず、試行の行も周の行も作らない。
func TestOpExec_DelegatedEnvIsRejectedBeforeAnythingRuns(t *testing.T) {
	ws, _, _ := execWorkspace(t)
	gh := newReleaseGH(t, execHead)
	t.Setenv(core.DelegatedRunEnvVar, "R-7")

	var stdout, stderr bytes.Buffer
	code := run([]string{"op", "exec", "--workspace", ws, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands())

	if code != 1 || !strings.Contains(stderr.String(), `"code":"verification_rejected"`) {
		t.Errorf("exit = %d, stderr = %s, want 1 / verification_rejected", code, stderr.String())
	}
	if calls := gh.calls(t); len(calls) != 0 {
		t.Errorf("gh was called: %v", calls)
	}
	if triggers, _ := coretest.CycleTriggers(t, ws); len(triggers) != 0 {
		t.Errorf("cycle rows = %v, want none", triggers)
	}
	if n := coretest.CountOperationExecutions(t, ws); n != 0 {
		t.Errorf("operation_execution rows = %d, want 0", n)
	}
}

// AC-42・AC-63: connectors.json が無ければ config_not_found、merge_method が閉集合の外なら外部を呼ばず config_invalid。
func TestOpExec_ConnectorsMissingOrMergeMethodInvalid(t *testing.T) {
	t.Run("config_not_found", func(t *testing.T) {
		ws := setupWorkspaceWithPosition(t)
		gh := newReleaseGH(t, execHead)
		opExecEnvelope(t, ws, 2, CodeConfigNotFound)
		if len(gh.calls(t)) != 0 {
			t.Errorf("gh was called: %v", gh.calls(t))
		}
	})
	t.Run("config_invalid", func(t *testing.T) {
		ws, _, _ := execWorkspace(t)
		writeConnectorsJSONForTest(t, ws, strings.Replace(releaseConnectorsFixture,
			`"default_branch": "main",`, `"default_branch": "main", "merge_method": "fast-forward",`, 1))
		gh := newReleaseGH(t, execHead)
		opExecEnvelope(t, ws, 2, CodeConfigInvalid)
		if len(gh.calls(t)) != 0 {
			t.Errorf("gh was called: %v", gh.calls(t))
		}
		if triggers, _ := coretest.CycleTriggers(t, ws); len(triggers) != 0 {
			t.Errorf("cycle rows = %v, want none", triggers)
		}
	})
}

// 存在しない <OP-ID> は終了コード 1・not_found（外部を呼ばない）。
func TestOpExec_UnknownOperationIsNotFound(t *testing.T) {
	ws, _, _ := execWorkspace(t)
	gh := newReleaseGH(t, execHead)
	opExecEnvelope(t, ws, 1, CodeNotFound, "OP-99")
	if len(gh.mergeCalls(t)) != 0 {
		t.Errorf("gh pr merge was called: %v", gh.mergeCalls(t))
	}
}

// release 以外・approved でない操作は、外部を呼ばず not_started に not_executable と `kind=… state=…` で示し、終了コード 0。
func TestOpExec_NotExecutableOperationsAreReportedWithDetail(t *testing.T) {
	ws, cid, _ := execWorkspace(t)
	gh := newReleaseGH(t, execHead)
	del := fmt.Sprintf("OP-%d", coretest.InsertReleaseOperation(t, ws, challengeIDToInternalID(t, cid), releasePRURL(1), "pending"))
	other := runJSON(t, ws, "op", "add", createForCase(t, ws), "--kind", "delete", "--summary", "d")["operation"].(map[string]any)["id"].(string)

	doc := runJSON(t, ws, "op", "exec", del)
	_, items, ns := execItems(t, doc)
	if len(items) != 0 || len(ns) != 1 {
		t.Fatalf("items = %v, not_started = %v", items, ns)
	}
	n := ns[0].(map[string]any)
	if n["reason"] != "not_executable" || n["operation_id"] != del || n["detail"] != "kind=release state=pending" || n["challenge_id"] != cid {
		t.Errorf("not_started[0] = %v", n)
	}

	doc = runJSON(t, ws, "op", "exec", other)
	_, _, ns = execItems(t, doc)
	if n := ns[0].(map[string]any); n["reason"] != "not_executable" || n["detail"] != "kind=delete state=pending" {
		t.Errorf("not_started[0] = %v, want not_executable kind=delete state=pending", n)
	}

	// AC-60: 承認済みでも release 以外は実行しない。
	coretest.SetOperationState(t, ws, int64(mustAtoi(t, strings.TrimPrefix(other, "OP-"))), "approved")
	doc = runJSON(t, ws, "op", "exec", other)
	_, items, ns = execItems(t, doc)
	if len(items) != 0 || len(ns) != 1 {
		t.Fatalf("items = %v, not_started = %v, want only one not_started", items, ns)
	}
	if n := ns[0].(map[string]any); n["reason"] != "not_executable" || n["detail"] != "kind=delete state=approved" {
		t.Errorf("not_started[0] = %v, want not_executable kind=delete state=approved", n)
	}
	if n := coretest.CountOperationExecutions(t, ws); n != 0 {
		t.Errorf("operation_execution rows = %d, want 0", n)
	}
	if len(gh.calls(t)) != 0 {
		t.Errorf("gh was called for a non-executable operation: %v", gh.calls(t))
	}
}

// 終わっていない生きた試行があれば、理由 in_progress と試行の ID を detail に出して飛ばす。
func TestOpExec_OpenLiveAttemptIsInProgress(t *testing.T) {
	ws, _, op := execWorkspace(t)
	gh := newReleaseGH(t, execHead)
	attempt := coretest.InsertOperationExecution(t, ws, int64(mustAtoi(t, strings.TrimPrefix(op, "OP-"))), coretest.ExecutionAttempt{
		StartedAt: nowStamp(), ApprovedHead: execHead, PID: int64(os.Getpid()), Host: currentHost(t),
	})

	doc := runJSON(t, ws, "op", "exec")

	_, items, ns := execItems(t, doc)
	if len(items) != 0 || len(ns) != 1 {
		t.Fatalf("items = %v, not_started = %v", items, ns)
	}
	n := ns[0].(map[string]any)
	if n["reason"] != "in_progress" || n["operation_id"] != op || n["detail"] != fmt.Sprintf("%d", attempt) {
		t.Errorf("not_started[0] = %v, want in_progress with the attempt ID %d", n, attempt)
	}
	if len(gh.mergeCalls(t)) != 0 {
		t.Errorf("merge calls = %v", gh.mergeCalls(t))
	}
}

// ID を省くと承認済みで未実行の release を ID の昇順に試す。待つ結果だけで終わっても終了コード 0。
func TestOpExec_WithoutIDTriesEveryApprovedReleaseInIDOrder(t *testing.T) {
	ws, cid, op1 := execWorkspace(t)
	op2 := fmt.Sprintf("OP-%d", coretest.InsertApprovedRelease(t, ws, challengeIDToInternalID(t, cid), releasePRURL(1), execHead, "2026-10-22T00:00:01.000Z"))
	newReleaseGH(t, execHead)

	doc := runJSON(t, ws, "op", "exec")

	_, items, _ := execItems(t, doc)
	if len(items) != 2 || items[0].(map[string]any)["operation_id"] != op1 || items[1].(map[string]any)["operation_id"] != op2 {
		t.Fatalf("items = %v, want %s then %s", items, op1, op2)
	}
	// 1 件目でマージされ、2 件目は PR が承認した head でマージ済みなので already_merged。
	if items[0].(map[string]any)["result"] != "merged" || items[1].(map[string]any)["result"] != "already_merged" {
		t.Errorf("results = %v, %v", items[0].(map[string]any)["result"], items[1].(map[string]any)["result"])
	}
}

// 待つ・失敗の結果（merge_failed）だけで終わった op exec も終了コード 0 で、release は approved のまま残る。
func TestOpExec_MergeFailureStillExitsZero(t *testing.T) {
	ws, cid, _ := execWorkspace(t)
	gh := newReleaseGH(t, execHead)
	gh.write(t, "merge_fail", "1")

	doc := runJSON(t, ws, "op", "exec")

	_, items, _ := execItems(t, doc)
	if len(items) != 1 || items[0].(map[string]any)["result"] != "merge_failed" || items[0].(map[string]any)["consecutive_failures"] != float64(1) {
		t.Fatalf("items = %v, want one merge_failed with consecutive_failures 1", items)
	}
	if got := runJSON(t, ws, "show", cid)["operations"].([]any)[0].(map[string]any)["state"]; got != "approved" {
		t.Errorf("operation state = %v, want approved (retry later)", got)
	}
}

// AC-79: 承認の後に push された release は、マージせずに取り消され（status に承認待ちで出る）、承認し直すと
// 次の op exec で実行の対象になる。承認し直しの head は GitHub が返す新しい head で、--match-head-commit にもそれを渡す。
func TestOpExec_CancelledReleaseReapprovedIsExecutedByTheNextRun(t *testing.T) {
	ws, _, op := execWorkspace(t)
	gh := newReleaseGH(t, execHead2) // 承認の後に push されて head が変わっている

	doc := runJSON(t, ws, "op", "exec")

	_, items, _ := execItems(t, doc)
	if len(items) != 1 || items[0].(map[string]any)["result"] != "head_changed" || items[0].(map[string]any)["observed_head"] != execHead2 {
		t.Fatalf("items = %v, want one head_changed observing %s", items, execHead2)
	}
	if len(gh.mergeCalls(t)) != 0 {
		t.Fatalf("gh pr merge was called for a changed head: %v", gh.mergeCalls(t))
	}
	st := runJSON(t, ws, "status")
	if !strings.Contains(fmt.Sprint(st["needs_human"]), op) {
		t.Errorf("status.needs_human = %v, want %s listed as awaiting approval", st["needs_human"], op)
	}
	if got := runJSON(t, ws, "op", "exec"); len(got) == 0 {
		t.Fatal("empty doc")
	} else if _, items, _ := execItems(t, got); len(items) != 0 {
		t.Errorf("a cancelled (pending) release was executed again: %v", items)
	}

	// 承認し直す（LLM の口。承認の ① が GitHub の現在の head を記録する）。
	code, _, stderr := runPiped(t, []string{"approve", op, "--relayed", "--session", "s", "--quote", "マージしてください", "--workspace", ws, "--json"})
	if code != 0 {
		t.Fatalf("re-approve: exit = %d, stderr = %s", code, stderr)
	}
	doc = runJSON(t, ws, "op", "exec")
	_, items, _ = execItems(t, doc)
	if len(items) != 1 || items[0].(map[string]any)["result"] != "merged" || items[0].(map[string]any)["approved_head"] != execHead2 {
		t.Fatalf("items = %v, want merged with approved_head %s", items, execHead2)
	}
	merges := gh.mergeCalls(t)
	if len(merges) != 1 || !strings.HasSuffix(merges[0], "--match-head-commit "+execHead2) {
		t.Errorf("merge calls = %v, want one with --match-head-commit %s", merges, execHead2)
	}
}

// cycle の最初の段は release で、取り込みより前。接続ツールの宣言が無ければ skipped。
func TestCycle_ReleasePhaseIsFirstAndMergesInsideTheCycle(t *testing.T) {
	ws, _, op := execWorkspace(t)
	gh := newReleaseGH(t, execHead)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1")}, "")

	doc := cycleDoc(t, ws)

	phases := doc["phases"].([]any)
	if phases[0].(map[string]any)["phase"] != "release" || phases[1].(map[string]any)["phase"] != "ingest" {
		t.Fatalf("phases = %v, want release first and ingest second", phaseNames(phases))
	}
	rel := phases[0].(map[string]any)
	items := rel["items"].([]any)
	if rel["skipped"] != false || len(items) != 1 || items[0].(map[string]any)["operation_id"] != op || items[0].(map[string]any)["result"] != "merged" {
		t.Errorf("release phase = %v", rel)
	}
	if len(gh.mergeCalls(t)) != 1 {
		t.Errorf("merge calls = %v, want 1", gh.mergeCalls(t))
	}
	if triggers, _ := coretest.CycleTriggers(t, ws); len(triggers) != 1 || triggers[0] != "manual" {
		t.Errorf("cycle triggers = %v", triggers)
	}
}

// M3 の AC-146（M4.5 の序数ではない）: 承認済みの release があっても、生きている保持者が排他を持っている間の cycle は locked で
// 終わり、gh pr merge を呼ばず、試行の行も作らない（release の段は cycle のロックの中で動く）。
func TestCycle_LiveHolder_ReleaseIsNotExecuted(t *testing.T) {
	ws, _, _ := execWorkspace(t)
	gh := newReleaseGH(t, execHead)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1")}, "")
	holder := coretest.InsertCycle(t, ws, "other-cycle", 300_000_000, nowStamp())
	coretest.InsertLock(t, ws, holder, int64(os.Getpid()), currentHost(t), nowStamp(), nowStamp())

	requireJSONErrorEnvelope(t, []string{"cycle", "--workspace", ws}, 1, CodeLocked)

	if calls := gh.calls(t); len(calls) != 0 {
		t.Errorf("gh was called while the exclusive lock was held: %v", calls)
	}
	if n := coretest.CountOperationExecutions(t, ws); n != 0 {
		t.Errorf("operation_execution rows = %d, want 0", n)
	}
	if h, found := coretest.CycleLockHolder(t, ws); !found || h != holder {
		t.Errorf("lock holder = %d (found=%v), want the original holder %d", h, found, holder)
	}
}

func TestCycle_ReleasePhaseIsSkippedWithoutConnectors(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1")}, "")

	doc := cycleDoc(t, ws)

	rel := cyclePhase(t, doc, "release")
	if rel["skipped"] != true || len(rel["items"].([]any)) != 0 || len(rel["not_started"].([]any)) != 0 {
		t.Errorf("release phase = %v, want skipped with no items", rel)
	}
}

// merge_method が閉集合の外なら cycle は周を始める前に config_invalid で終わる（周の行も gh の呼び出しも無い）。
func TestCycle_InvalidMergeMethodFailsBeforeTheCycleStarts(t *testing.T) {
	ws, _, _ := execWorkspace(t)
	writeConnectorsJSONForTest(t, ws, strings.Replace(releaseConnectorsFixture,
		`"default_branch": "main",`, `"default_branch": "main", "merge_method": "bogus",`, 1))
	gh := newReleaseGH(t, execHead)
	requireJSONErrorEnvelope(t, []string{"cycle", "--workspace", ws}, 2, CodeConfigInvalid)
	if len(gh.calls(t)) != 0 {
		t.Errorf("gh was called: %v", gh.calls(t))
	}
	if triggers, _ := coretest.CycleTriggers(t, ws); len(triggers) != 0 {
		t.Errorf("cycle rows = %v, want none", triggers)
	}
}

// release の段の not_started は operation_id を常に持ち、それ以外の段の not_started は持たない。
func TestCycle_NotStartedOperationIDOnlyInTheReleasePhase(t *testing.T) {
	ws, _, op := execWorkspace(t)
	newReleaseGH(t, execHead)
	coretest.InsertOperationExecution(t, ws, int64(mustAtoi(t, strings.TrimPrefix(op, "OP-"))), coretest.ExecutionAttempt{
		StartedAt: nowStamp(), ApprovedHead: execHead, PID: int64(os.Getpid()), Host: currentHost(t),
	})
	// 周の上限 1.5・1 回 0.8 の費用: 2 件目の分類は cycle_budget で起動できず、release 以外の段にも not_started ができる。
	writeAgentJSONForTest(t, ws, `{"position_file": "position.md", "cycle_budget_usd": 1.5}`)
	createTitled(t, ws, "a")
	createTitled(t, ws, "b")
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{{Match: fakeClaudeJ1Match, Tag: "J1",
		Stdout: `{"is_error":false,"total_cost_usd":0.8,"structured_output":{"verdict":"not_mine","reason":"r"}}`}}, "")

	doc := cycleDoc(t, ws)

	sawRelease, sawOther := false, false
	for _, p := range doc["phases"].([]any) {
		pm := p.(map[string]any)
		nsList, _ := pm["not_started"].([]any)
		if pm["phase"] == "release" {
			sawRelease = len(nsList) == 1
		}
		if pm["phase"] != "release" && len(nsList) > 0 {
			sawOther = true
		}
		for _, n := range nsList {
			_, has := n.(map[string]any)["operation_id"]
			if pm["phase"] == "release" && !has || pm["phase"] != "release" && has {
				t.Errorf("phase %v not_started %v: operation_id presence = %v", pm["phase"], n, has)
			}
		}
	}
	if !sawRelease {
		t.Errorf("release phase did not report the in_progress release: %v", doc["phases"])
	}
	if !sawOther {
		t.Errorf("no not_started element outside the release phase was produced: %v", doc["phases"])
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		t.Fatalf("atoi(%q): %v", s, err)
	}
	return n
}

func phaseNames(phases []any) []string {
	var out []string
	for _, p := range phases {
		out = append(out, fmt.Sprint(p.(map[string]any)["phase"]))
	}
	return out
}
