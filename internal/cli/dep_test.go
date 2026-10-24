package cli

import (
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは M4.5 S2 の `dep add`・`dep rm` を、子プロセスとして起動した CLI で検査する
// （受入基準 §S2: 依存の登録）。

func depWorkspace(t *testing.T, n int) (ws string, ids []string) {
	t.Helper()
	ws = initializedWorkspace(t)
	for i := 0; i < n; i++ {
		ids = append(ids, createForCase(t, ws))
	}
	return ws, ids
}

func TestDepAdd_AddsAllTargetsInOneVersionBumpAndPrintsTheChallenge(t *testing.T) {
	ws, ids := depWorkspace(t, 3)
	out := runJSON(t, ws, "dep", "add", ids[0], ids[1], ids[2])
	c, ok := out["challenge"].(map[string]any)
	if !ok || c["id"] != ids[0] || c["version"] != 2.0 {
		t.Fatalf("output = %#v, want the challenge %s at version 2", out, ids[0])
	}
}

func TestDepRm_RemovesAndBumpsVersionOnce(t *testing.T) {
	ws, ids := depWorkspace(t, 3)
	runJSON(t, ws, "dep", "add", ids[0], ids[1], ids[2])
	out := runJSON(t, ws, "dep", "rm", ids[0], ids[1], ids[2])
	if c := out["challenge"].(map[string]any); c["version"] != 3.0 {
		t.Fatalf("version = %v, want 3", c["version"])
	}
}

func TestDep_IdempotentSuccessKeepsTheVersion(t *testing.T) {
	ws, ids := depWorkspace(t, 2)
	runJSON(t, ws, "dep", "add", ids[0], ids[1])
	out := runJSON(t, ws, "dep", "add", ids[0], ids[1])
	if c := out["challenge"].(map[string]any); c["version"] != 2.0 {
		t.Fatalf("re-add version = %v, want 2", c["version"])
	}
	out = runJSON(t, ws, "dep", "rm", ids[1], ids[0])
	if c := out["challenge"].(map[string]any); c["version"] != 1.0 {
		t.Fatalf("rm of a missing dependency version = %v, want 1", c["version"])
	}
}

func TestDep_RejectionsMapToTheDocumentedCodesAndWriteNothing(t *testing.T) {
	ws, ids := depWorkspace(t, 3)
	runJSON(t, ws, "dep", "add", ids[0], ids[1])
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, ids[2]), "done")

	cases := []struct {
		name string
		args []string
		code ErrorCode
		exit int
	}{
		{"self reference", []string{"dep", "add", ids[0], ids[0]}, CodeUsageError, 2},
		{"missing target add", []string{"dep", "add", ids[0], "C-99"}, CodeNotFound, 1},
		{"missing target rm", []string{"dep", "rm", ids[0], "C-99"}, CodeNotFound, 1},
		{"missing source add", []string{"dep", "add", "C-99", ids[0]}, CodeNotFound, 1},
		{"missing source rm", []string{"dep", "rm", "C-99", ids[0]}, CodeNotFound, 1},
		{"done source add", []string{"dep", "add", ids[2], ids[0]}, CodeTerminalState, 1},
		{"done source rm", []string{"dep", "rm", ids[2], ids[0]}, CodeTerminalState, 1},
		{"cycle", []string{"dep", "add", ids[1], ids[0]}, CodeDependencyCycle, 1},
		{"one rejected target adds none", []string{"dep", "add", ids[1], ids[2], ids[0]}, CodeDependencyCycle, 1},
		{"too few arguments", []string{"dep", "add", ids[0]}, CodeUsageError, 2},
	}
	for _, c := range cases {
		requireErrorCode(t, append(append([]string{}, c.args...), "--workspace", ws), c.exit, c.code)
	}
	// 拒否のあとも、C-2 に依存は 1 つも足されておらず、版も 1 のまま。
	out := runJSON(t, ws, "show", ids[1])
	if v := out["challenge"].(map[string]any)["version"]; v != 1.0 {
		t.Fatalf("%s version = %v, want 1", ids[1], v)
	}
}

// 受入基準: 端末なし（標準入力をパイプ）・CLAUDECODE ありの子プロセスでも実行できる（本人確認を求めない）。
func TestDep_RunsUnderClaudeCodeWithPipedStdin(t *testing.T) {
	ws, ids := depWorkspace(t, 2)
	for _, args := range [][]string{
		{"dep", "add", ids[0], ids[1], "--workspace", ws, "--json"},
		{"dep", "rm", ids[0], ids[1], "--workspace", ws, "--json"},
	} {
		code, stdout, stderr := runPiped(t, args, claudeCodeEnvVar+"=1")
		if code != 0 || !strings.Contains(stdout, `"challenge"`) {
			t.Fatalf("args=%v exit = %d, stdout = %s, stderr = %s, want success", args, code, stdout, stderr)
		}
	}
}
