package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// j1NotMineFixture は「担当外」判定の J1 の構造化出力（偽の claude の標準出力）。
const j1NotMineFixture = `{"is_error":false,"structured_output":{"verdict":"not_mine","reason":"CLI test fixture"}}`

func j1MineFixture(priority string) string {
	return `{"is_error":false,"structured_output":{"verdict":"mine","priority":"` + priority + `","reason":"CLI test fixture"}}`
}

// setupWorkspaceWithPosition は初期化済みのワークスペースへ position.md を置き、
// それを指す .flywheel/agent.json を書く（extraJSON を最上位のキーとして
// 追加できる。空文字列なら追加しない）。
func setupWorkspaceWithPosition(t *testing.T) string {
	t.Helper()
	ws := initializedWorkspace(t)
	writePositionFileForTest(t, ws, "このエージェントの担当範囲")
	writeAgentJSONForTest(t, ws, `{"position_file": "position.md"}`)
	return ws
}

// AC-1: position_file だけを持つ agent.json で classify --auto --json を
// 実行すると、既定の周の上限額 300 USD で予算を評価する（#83 が作る周の行の
// 予算の値を runs で観測する）。
func TestClassifyAuto_DefaultCycleBudgetIs300USD(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	id := createForCase(t, ws)
	putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: j1NotMineFixture})

	runJSON(t, ws, "classify", "--auto", id)

	runsDoc := runJSON(t, ws, "runs", id)
	runs := runsDoc["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("runs = %v, want 1", runs)
	}
	got := runs[0].(map[string]any)["cycle_budget_usd"]
	if got != 300.0 {
		t.Errorf("cycle_budget_usd = %v, want 300", got)
	}
}

// AC-3: .flywheel/agent.json が無いワークスペースで classify --auto を実行
// すると、終了コード 2・config_invalid で終わる。
func TestClassifyAuto_MissingAgentJSON_ConfigInvalid(t *testing.T) {
	ws := initializedWorkspace(t)
	id := createForCase(t, ws)
	// AC-14 と同じ理由（position_file が無い＝agent.json が無い）で config_invalid
	// になる。ここでは「agent.json が無い」経路を明示的に固定する。
	putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: j1NotMineFixture})
	requireJSONErrorEnvelope(t, []string{"classify", "--auto", id, "--workspace", ws}, 2, CodeConfigInvalid)
}

// AC-4: 未知のキーを含む agent.json は、終了コード 2・config_invalid で終わり、
// 偽の claude が起動されない。
func TestClassifyAuto_UnknownAgentJSONKey_ConfigInvalidAndClaudeNotInvoked(t *testing.T) {
	ws := initializedWorkspace(t)
	writePositionFileForTest(t, ws, "pos")
	writeAgentJSONForTest(t, ws, `{"position_file": "position.md", "no_such_key": 1}`)
	id := createForCase(t, ws)
	argvLog, _ := putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: j1NotMineFixture})

	requireJSONErrorEnvelope(t, []string{"classify", "--auto", id, "--workspace", ws}, 2, CodeConfigInvalid)

	if _, err := os.Stat(argvLog); err == nil {
		t.Error("the fake claude was invoked even though agent.json is invalid")
	}
}

// AC-14: position_file が無い・指すファイルが無い agent.json は、終了コード
// 2・config_invalid で終わり、偽の claude が起動されない。
func TestClassifyAuto_MissingOrDanglingPositionFile_ConfigInvalidAndClaudeNotInvoked(t *testing.T) {
	cases := []struct {
		name    string
		agent   string
		wantErr ErrorCode
	}{
		{"position_file omitted", `{}`, CodeConfigInvalid},
		{"position_file points to a missing file", `{"position_file": "no-such-file.md"}`, CodeConfigInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ws := initializedWorkspace(t)
			writeAgentJSONForTest(t, ws, c.agent)
			id := createForCase(t, ws)
			argvLog, _ := putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: j1NotMineFixture})

			requireJSONErrorEnvelope(t, []string{"classify", "--auto", id, "--workspace", ws}, 2, c.wantErr)

			if _, err := os.Stat(argvLog); err == nil {
				t.Error("the fake claude was invoked even though position_file is missing/dangling")
			}
		})
	}
}

// AC-16: .flywheel/connectors.json が無いワークスペースでも、classify --auto
// は J1 を起動する。
func TestClassifyAuto_WorksWithoutConnectorsJSON(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	// connectors.json を意図的に作らない。
	if _, err := os.Stat(filepath.Join(ws, ".flywheel", "connectors.json")); err == nil {
		t.Fatal("test setup error: connectors.json unexpectedly exists")
	}
	id := createForCase(t, ws)
	argvLog, _ := putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: j1NotMineFixture})

	runJSON(t, ws, "classify", "--auto", id)

	if _, err := os.Stat(argvLog); err != nil {
		t.Errorf("the fake claude was not invoked: %v", err)
	}
}

// AC-27: PATH に claude が無い環境で classify --auto を実行すると、終了コード
// 2・invoker_unavailable で終わり、ストアが変わらない。
func TestClassifyAuto_ClaudeNotOnPATH_InvokerUnavailableAndStoreUnchanged(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	id := createForCase(t, ws)
	dbPath := filepath.Join(ws, ".flywheel", "flywheel.db")
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read store before: %v", err)
	}

	emptyPATH(t)
	requireJSONErrorEnvelope(t, []string{"classify", "--auto", id, "--workspace", ws}, 2, CodeInvokerUnavailable)

	after, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read store after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("invoker_unavailable must not change the store file")
	}
}

// classify --auto --priority は同時に指定できない（usage_error）。
func TestClassifyAuto_WithPriority_IsUsageError(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	id := createForCase(t, ws)
	requireJSONErrorEnvelope(t, []string{"classify", "--auto", "--priority", "P1", id, "--workspace", ws}, 2, CodeUsageError)
}

// AC-70: 同じ課題に対する2つの classify --auto <C-ID> を並行して実行すると、
// run は1つしか作られず、もう一方は終了コード1・run_in_progress で終わる。
func TestClassifyAuto_ConcurrentSameChallengeID_SecondGetsRunInProgress(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	id := createForCase(t, ws)

	started := filepath.Join(t.TempDir(), "started")
	putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: j1NotMineFixture, SleepSeconds: 3, StartedFile: started})

	var wg sync.WaitGroup
	var code1 int
	var stderr1 bytes.Buffer
	wg.Add(1)
	go func() {
		defer wg.Done()
		var stdout bytes.Buffer
		code1 = run([]string{"classify", "--auto", id, "--workspace", ws, "--json"}, strings.NewReader(""), &stdout, &stderr1, defaultCommands())
	}()

	waitForFileWithTimeout(t, started, 10*time.Second)

	var stdout2, stderr2 bytes.Buffer
	code2 := run([]string{"classify", "--auto", id, "--workspace", ws, "--json"}, strings.NewReader(""), &stdout2, &stderr2, defaultCommands())

	wg.Wait()

	if code1 != 0 {
		t.Fatalf("first call exit = %d, want 0 (stderr=%s)", code1, stderr1.String())
	}
	if code2 != 1 {
		t.Fatalf("second call exit = %d, want 1 (stderr=%s)", code2, stderr2.String())
	}
	var errDoc struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stderr2.Bytes(), &errDoc); err != nil {
		t.Fatalf("second call stderr is not JSON: %v (%q)", err, stderr2.String())
	}
	if errDoc.Error.Code != string(CodeRunInProgress) {
		t.Errorf("second call error.code = %q, want %q", errDoc.Error.Code, CodeRunInProgress)
	}

	runsDoc := runJSON(t, ws, "runs", id)
	if got := len(runsDoc["runs"].([]any)); got != 1 {
		t.Errorf("runs for %s = %d, want exactly 1", id, got)
	}
}

// status.needs_human.triage: 判定 not_mine の後、課題は status の
// needs_human.triage に、理由と run の ID つきで出る（AC-84。CLI からの結線の
// 確認）。
func TestClassifyAuto_NotMine_AppearsInStatusTriage(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	id := createForCase(t, ws)
	putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: j1NotMineFixture})

	classifyDoc := runJSON(t, ws, "classify", "--auto", id)
	phase := classifyDoc["phase"].(map[string]any)
	items := phase["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v, want 1", items)
	}
	item := items[0].(map[string]any)
	if item["outcome"] != "not_mine" || item["status"] != nil {
		t.Fatalf("item = %+v, want outcome=not_mine status=null", item)
	}
	runID := item["run_id"].(string)

	statusDoc := runJSON(t, ws, "status")
	needsHuman := statusDoc["needs_human"].(map[string]any)
	triage := needsHuman["triage"].([]any)
	if len(triage) != 1 {
		t.Fatalf("triage = %v, want 1 item", triage)
	}
	tr := triage[0].(map[string]any)
	if tr["challenge_id"] != id || tr["run_id"] != runID || tr["reason"] != "CLI test fixture" {
		t.Errorf("triage[0] = %+v, want challenge_id=%s run_id=%s reason=CLI test fixture", tr, id, runID)
	}
}

// mine で優先度が付いた J1 の出力は、分類済への遷移を classify --auto の
// JSON にも反映する（結線の smoke テスト）。
func TestClassifyAuto_Mine_ClassifiesChallengeAndReportsStatus(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	id := createForCase(t, ws)
	putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: j1MineFixture("P1")})

	doc := runJSON(t, ws, "classify", "--auto", id)
	item := doc["phase"].(map[string]any)["items"].([]any)[0].(map[string]any)
	if item["outcome"] != "mine" || item["status"] != "classified" {
		t.Fatalf("item = %+v, want outcome=mine status=classified", item)
	}

	show := runJSON(t, ws, "show", id)
	ch := show["challenge"].(map[string]any)
	if ch["status"] != "classified" || ch["priority"] != "P1" {
		t.Errorf("challenge = %+v, want status=classified priority=P1", ch)
	}
}

// ID を指定した classify --auto <C-ID> が周の上限で起動できないときは、
// 終了コード 1・budget_exceeded で終わる（issue #84 の完了条件。AC 自体は
// #83）。
func TestClassifyAuto_ExplicitID_OverCycleBudget_BudgetExceeded(t *testing.T) {
	ws := initializedWorkspace(t)
	writePositionFileForTest(t, ws, "pos")
	// judgment_budget_usd.J1 は既定の 1 USD のまま、周の上限だけを 0.5 USD に
	// 絞る（既消費0＋予約0＋評価額1 > 上限0.5 で必ず超過する）。
	writeAgentJSONForTest(t, ws, `{"position_file": "position.md", "cycle_budget_usd": 0.5}`)
	id := createForCase(t, ws)
	argvLog, _ := putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: j1NotMineFixture})

	requireJSONErrorEnvelope(t, []string{"classify", "--auto", id, "--workspace", ws}, 1, CodeBudgetExceeded)

	if _, err := os.Stat(argvLog); err == nil {
		t.Error("the fake claude was invoked even though the cycle budget was already exceeded")
	}
}

// ID を省略した classify --auto は、未分類の課題すべてを対象にする（スモーク
// テスト。対象の選び方の詳細は internal/core のテストが固定する）。
func TestClassifyAuto_WithoutID_ProcessesAllUnclassifiedChallenges(t *testing.T) {
	ws := setupWorkspaceWithPosition(t)
	id1 := createForCase(t, ws)
	id2 := createForCase(t, ws)
	putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: j1NotMineFixture})

	doc := runJSON(t, ws, "classify", "--auto")
	items := doc["phase"].(map[string]any)["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %v, want 2", items)
	}
	seen := map[string]bool{}
	for _, it := range items {
		seen[it.(map[string]any)["challenge_id"].(string)] = true
	}
	if !seen[id1] || !seen[id2] {
		t.Errorf("items did not cover both challenges: %v", items)
	}
}
