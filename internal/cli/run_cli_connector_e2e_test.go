package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `form: cli` の接続ツール（案 B。harness の汎用契約）の委譲を、`flywheel run` の入口から、実ストア・
// 実物の git リポジトリ（スロット）・PATH に置いた偽の `harness` で検証する。本物の harness・claude は
// 起動しない（claude は PATH に置かず、呼ばれたら起動不能で失敗する）。

const cliConnectorFixture = `{
  "version": 1,
  "connectors": [
    {"id": "ct", "form": "cli", "permission_mode": "default", "contract_version": 1,
     "commands": {"start": ["harness", "contract", "start", "ticket", "--input", "challenge={challenge_id}"],
                  "status": ["harness", "contract", "status", "{run_id}"],
                  "resume": ["harness", "contract", "resume", "--input", "{input}", "{run_id}"],
                  "cancel": ["harness", "contract", "cancel", "{run_id}"]},
     "operations": [{"id": "cli-op", "interactive": false, "child_may_decide": true, "artifacts": "none"}]}
  ],
  "repos": [
    {"name": "direct-repo", "remote": "o/direct", "default_branch": "main", "connector": "ct", "slots": {"provider": "clone", "paths": ["slot-d"]}}
  ]
}`

// putFakeHarnessOnPATH は引数を記録して固定の JSON を出す偽の harness を PATH の先頭に置き、記録先を返す。
func putFakeHarnessOnPATH(t *testing.T, doc string) (argvLog string) {
	t.Helper()
	dir := t.TempDir()
	argvLog = filepath.Join(dir, "argv.log")
	script := "#!/bin/sh\necho \"$*\" >> " + shellSingleQuote(argvLog) + "\ncat <<'JSON'\n" + doc + "\nJSON\n"
	if err := os.WriteFile(filepath.Join(dir, "harness"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argvLog
}

func setupCLIConnectorWorkspace(t *testing.T) (ws, id string) {
	t.Helper()
	ws = setupWorkspaceWithPosition(t)
	writeConnectorsJSONForTest(t, ws, cliConnectorFixture)
	slotClone(t, filepath.Join(ws, "slot-d"), "git@github.com:o/direct.git")
	id = newInProgressForRun(t, ws, func(m map[string]any) { m["repo"] = "direct-repo"; m["operation"] = "cli-op" })
	return ws, id
}

func TestRun_E2E_CLIConnector_SucceededContractCompletesWithTheConnectorsCostAndRunID(t *testing.T) {
	ws, id := setupCLIConnectorWorkspace(t)
	argvLog := putFakeHarnessOnPATH(t, `{"contract_version":1,"run_id":"HRUN-1","state":"succeeded","summary":"done","requested_action":null,"artifacts":[],"cost_usd":21.5}`)
	withFakeGHRoutesOnPATH(t, nil)

	phase := phaseOf(t, runJSON(t, ws, "run"))
	items := phase["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", items)
	}
	it := items[0].(map[string]any)
	if it["challenge_id"] != id || it["result"] != "succeeded" || it["outcome"] != "completed" || it["status"] != "verifying" {
		t.Errorf("item = %v", it)
	}
	if got, _ := os.ReadFile(argvLog); strings.TrimSpace(string(got)) != "contract start ticket --input challenge="+id {
		t.Errorf("harness argv = %q", got)
	}
	runs := delegateRunsOf(t, ws)
	if len(runs) != 1 || runs[0]["cost_usd"] != 21.5 {
		t.Fatalf("delegate runs = %v, want cost_usd 21.5 from the contract document", runs)
	}
}

func TestRun_E2E_CLIConnector_WaitingContractBecomesAGenericHold(t *testing.T) {
	ws, id := setupCLIConnectorWorkspace(t)
	putFakeHarnessOnPATH(t, `{"contract_version":1,"run_id":"HRUN-2","state":"waiting","summary":"stopped","requested_action":{"kind":"approve","decider":"human","text":"ASK-TEXT"},"artifacts":[],"cost_usd":3}`)
	withFakeGHRoutesOnPATH(t, nil)

	it := phaseOf(t, runJSON(t, ws, "run"))["items"].([]any)[0].(map[string]any)
	if it["challenge_id"] != id || it["outcome"] != "blocked" || it["status"] != "awaiting_human" {
		t.Fatalf("item = %v, want a hold (awaiting_human)", it)
	}
	if show := runJSON(t, ws, "show", id); !strings.Contains(fmt.Sprint(show), "ASK-TEXT") {
		t.Errorf("show does not carry the requested action text: %v", show)
	}
}
