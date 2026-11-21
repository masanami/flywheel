package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
	"github.com/masanami/flywheel/internal/server"
)

// このファイルは docs/features/core-redesign.md の S3c の受入基準のうち、通したコンフリクトの解消の表示（AC-110〜AC-128）を確かめる。
// 作業ログと contract_run の列は試験が直接用意する（J5 側の実装を待たない）。

const fixAt = "2026-11-20T01:02:03.000Z"

func addConflictFixLog(t *testing.T, ws, entity string, entityID int, from, to string, files []string, runID string) {
	t.Helper()
	fs, _ := json.Marshal(files)
	coretest.Exec(t, ws, `INSERT INTO activity (at, actor, channel, verification, entity, entity_id, action, before, after)
		VALUES (?, 'flywheel', 'cli', 'none', ?, ?, 'conflict_fix_passed', ?, ?)`,
		fixAt, entity, entityID, `{"ref":"`+from+`"}`, `{"ref":"`+to+`","files":`+string(fs)+`,"run_id":"`+runID+`"}`)
}

func fixList(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()
	raw, ok := doc["conflict_fixes"]
	if !ok {
		t.Fatal("show --json has no root key conflict_fixes")
	}
	return objectList(t, raw, "conflict_fixes")
}

// AC-110・AC-124: 通したものが無いとき、conflict_fixes は空の配列（null でない）で、テキストの出力に節が出ない。
func TestShow_NoPassedConflictFixes(t *testing.T) {
	t.Parallel()
	ws, id := approvedTasksWorkspace(t)
	doc := runJSON(t, ws, "show", id, "--json")
	if arr, ok := doc["conflict_fixes"].([]any); !ok || len(arr) != 0 {
		t.Errorf("conflict_fixes = %#v, want []", doc["conflict_fixes"])
	}
	if text := runText(t, ws, "show", id); strings.Contains(text, "コンフリクトの解消") {
		t.Errorf("text output has the conflict fix section without any record:\n%s", text)
	}
}

// AC-111〜AC-113・AC-117〜AC-121・AC-123: 統合先宛てで 1 件通した後の要素とテキストの 1 行。
func TestShow_PassedConflictFixToIntegration(t *testing.T) {
	t.Parallel()
	ws, id := approvedTasksWorkspace(t)
	addConflictFixLog(t, ws, "task", 2, "aaa111", "bbb222", []string{"a.go", "b/c.go"}, "R-7")
	fixes := fixList(t, runJSON(t, ws, "show", id, "--json"))
	if len(fixes) != 1 {
		t.Fatalf("conflict_fixes = %v, want 1 element", fixes)
	}
	want := map[string]any{
		"at": fixAt, "target": "integration", "task_id": "T-2", "operation_id": nil,
		"from_ref": "aaa111", "to_ref": "bbb222", "files": []any{"a.go", "b/c.go"}, "run_id": "R-7",
	}
	if !reflect.DeepEqual(fixes[0], want) {
		t.Errorf("conflict_fixes[0] = %v, want %v", fixes[0], want)
	}
	text := runText(t, ws, "show", id)
	var line string
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, "T-2") && strings.Contains(l, "統合先") && strings.Contains(l, "2 件") {
			line = l
		}
	}
	if line == "" || !strings.Contains(line, fixAt) {
		t.Errorf("text output has no one-line conflict fix with the time, the destination, T-2 and the file count:\n%s", text)
	}
}

// AC-114〜AC-116: 反映先宛てで 1 件通した後の要素。
func TestShow_PassedConflictFixToRelease(t *testing.T) {
	t.Parallel()
	ws, id := approvedTasksWorkspace(t)
	op := coretest.InsertReleaseOperation(t, ws, challengeIDToInternalID(t, id), "o/r#1", "approved")
	addConflictFixLog(t, ws, "operation", int(op), "ccc", "ddd", []string{"x.go"}, "R-9")
	fixes := fixList(t, runJSON(t, ws, "show", id, "--json"))
	if len(fixes) != 1 || fixes[0]["target"] != "release" || fixes[0]["operation_id"] != "OP-1" || fixes[0]["task_id"] != nil {
		t.Fatalf("conflict_fixes = %v, want one release element for OP-1 with task_id null", fixes)
	}
	text := runText(t, ws, "show", id)
	found := false
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, "反映先") && strings.Contains(l, "OP-1") && strings.Contains(l, fixAt) && strings.Contains(l, "1 件") {
			found = true
		}
	}
	if !found {
		t.Errorf("text output has no one-line conflict fix with the time, 反映先, OP-1 and the file count:\n%s", text)
	}
}

// 古い順で、別の課題の記録は混ざらない。
func TestShow_ConflictFixesAreOldestFirstAndPerChallenge(t *testing.T) {
	t.Parallel()
	ws, id := approvedTasksWorkspace(t)
	addConflictFixLog(t, ws, "task", 3, "r1", "r2", []string{"a"}, "R-1")
	addConflictFixLog(t, ws, "task", 1, "r2", "r3", []string{"b"}, "R-2")
	other := createForCase(t, ws)
	coretest.InsertApprovedPlan(t, ws, challengeIDToInternalID(t, other), "BODY", showTasksSpec)
	addConflictFixLog(t, ws, "task", 4, "z1", "z2", []string{"z"}, "R-3")
	fixes := fixList(t, runJSON(t, ws, "show", id, "--json"))
	if len(fixes) != 2 || fixes[0]["task_id"] != "T-3" || fixes[1]["task_id"] != "T-1" {
		t.Errorf("conflict_fixes = %v, want T-3 then T-1 (insertion order) only", fixes)
	}
}

// AC-125・AC-126: runs[] の fix_pending_ref は、確かめ待ちの中身を持つ契約の実行を始めた run だけが値を持ち、ほかは null。
func TestShowJSON_RunsCarryFixPendingRef(t *testing.T) {
	t.Parallel()
	ws, id := approvedTasksWorkspace(t)
	cid := challengeIDToInternalID(t, id)
	coretest.InsertContractRun(t, ws, cid, "conn", "ext-1", "running")
	coretest.InsertRun(t, ws, coretest.InsertRunInput{
		ChallengeID: cid, Kind: "judgment", ChallengeVersion: 1, SessionID: "s2", PID: 1, Host: "h",
		HeartbeatAt: "2026-10-22T00:00:00.000Z", StartedAt: "2026-10-22T00:00:00.000Z", EndedAt: "2026-10-22T00:00:01.000Z",
		Result: "succeeded", BudgetBucket: "judgment",
	})
	runs := objectList(t, runJSON(t, ws, "show", id, "--json")["runs"], "runs")
	for _, r := range runs {
		if v, ok := r["fix_pending_ref"]; !ok || v != nil {
			t.Errorf("run %v fix_pending_ref = %#v, want null before any pending ref", r["id"], v)
		}
	}
	coretest.Exec(t, ws, `UPDATE contract_run SET fix_pending_ref = 'pend01' WHERE external_run_id = 'ext-1'`)
	runs = objectList(t, runJSON(t, ws, "show", id, "--json")["runs"], "runs")
	got := map[string]any{}
	for _, r := range runs {
		got[r["id"].(string)] = r["fix_pending_ref"]
	}
	if !reflect.DeepEqual(got, map[string]any{"R-1": "pend01", "R-2": nil}) {
		t.Errorf("fix_pending_ref by run = %v, want R-1=pend01, R-2=null", got)
	}
}

// `flywheel runs --json` の run の形は変えない（fix_pending_ref は show と詳細の API だけ）。
func TestRunsJSON_HasNoFixPendingRef(t *testing.T) {
	t.Parallel()
	ws, id := approvedTasksWorkspace(t)
	coretest.InsertContractRun(t, ws, challengeIDToInternalID(t, id), "conn", "ext-1", "running")
	runs := objectList(t, runJSON(t, ws, "runs", "--json")["runs"], "runs")
	if len(runs) == 0 {
		t.Fatal("no runs")
	}
	if _, ok := runs[0]["fix_pending_ref"]; ok {
		t.Error("runs --json has fix_pending_ref")
	}
}

// AC-122・AC-127: 課題の詳細の API の conflict_fixes と runs[] の fix_pending_ref は、show --json と同じ値である。
func TestChallengeDetailAPI_ConflictFixesAndFixPendingRefMatchShowJSON(t *testing.T) {
	t.Parallel()
	ws, id := approvedTasksWorkspace(t)
	cid := challengeIDToInternalID(t, id)
	coretest.InsertContractRun(t, ws, cid, "conn", "ext-1", "running")
	coretest.Exec(t, ws, `UPDATE contract_run SET fix_pending_ref = 'pend01' WHERE external_run_id = 'ext-1'`)
	addConflictFixLog(t, ws, "task", 1, "a", "b", []string{"f.go"}, "R-1")

	api := getDetailAPI(t, ws, id)
	cli := runJSON(t, ws, "show", id, "--json")
	if !reflect.DeepEqual(api["conflict_fixes"], cli["conflict_fixes"]) || len(fixList(t, api)) != 1 {
		t.Errorf("conflict_fixes differ\napi: %v\ncli: %v", api["conflict_fixes"], cli["conflict_fixes"])
	}
	if !reflect.DeepEqual(api["runs"], cli["runs"]) {
		t.Errorf("runs differ\napi: %v\ncli: %v", api["runs"], cli["runs"])
	}
	if runs := objectList(t, api["runs"], "runs"); runs[0]["fix_pending_ref"] != "pend01" {
		t.Errorf("api runs[0].fix_pending_ref = %v, want pend01", runs[0]["fix_pending_ref"])
	}
}

// AC-128: 通したコンフリクトの解消がある課題があっても、status --json のキーの集合は変わらない。
func TestStatusJSON_KeysUnchangedByPassedConflictFixes(t *testing.T) {
	t.Parallel()
	ws, _ := approvedTasksWorkspace(t)
	collect := func() []string {
		var keys []string
		var walk func(prefix string, v any)
		walk = func(prefix string, v any) {
			switch x := v.(type) {
			case map[string]any:
				for k, c := range x {
					keys = append(keys, prefix+"/"+k)
					walk(prefix+"/"+k, c)
				}
			case []any:
				for _, c := range x {
					walk(prefix+"[]", c)
				}
			}
		}
		walk("", runJSON(t, ws, "status", "--json"))
		sort.Strings(keys)
		return keys
	}
	before := collect()
	addConflictFixLog(t, ws, "task", 1, "a", "b", []string{"f.go"}, "R-1")
	if after := collect(); !reflect.DeepEqual(before, after) {
		t.Errorf("status --json keys changed\nbefore: %v\nafter: %v", before, after)
	}
}

func getDetailAPI(t *testing.T, ws, id string) map[string]any {
	t.Helper()
	ln, err := server.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New(ln, []core.FleetWorkspace{{Name: "w", Path: ws}})
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve() }()
	t.Cleanup(func() {
		_ = srv.Shutdown()
		<-errc
	})
	resp, err := http.Get("http://" + ln.Addr().String() + "/api/v1/workspaces/w/challenges/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("GET = %d %s", resp.StatusCode, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// run_id が数（内部の整数 ID）で書かれていても、課題の詳細は読め、"R-<n>" になる。
func TestShow_ConflictFixRunIDAcceptsANumber(t *testing.T) {
	t.Parallel()
	ws, id := approvedTasksWorkspace(t)
	coretest.Exec(t, ws, `INSERT INTO activity (at, actor, channel, verification, entity, entity_id, action, before, after)
		VALUES (?, 'flywheel', 'cli', 'none', 'task', 1, 'conflict_fix_passed', '{"ref":"a"}', '{"ref":"b","files":[],"run_id":5}')`, fixAt)
	fixes := fixList(t, runJSON(t, ws, "show", id, "--json"))
	if len(fixes) != 1 || fixes[0]["run_id"] != "R-5" {
		t.Errorf("conflict_fixes = %v, want run_id R-5", fixes)
	}
}
