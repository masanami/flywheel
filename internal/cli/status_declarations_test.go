package cli

import (
	"bytes"
	"strings"
	"testing"
)

const statusConnOp = `{"id": "a", "form": "brief", "permission_mode": "auto", "operations": [{"id": "op"}]}`

func statusRun(t *testing.T, ws string, extra ...string) (int, string, string) {
	t.Helper()
	args := append([]string{"status", "--workspace", ws}, extra...)
	var stdout, stderr bytes.Buffer
	code := run(args, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	return code, stdout.String(), stderr.String()
}

// status は cycle と同じ宣言の検査をし、不備なら config_invalid（終了コード 2）で終わる。
// --json の有無を問わない。
func TestStatus_InvalidDeclarations_ConfigInvalid(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, ws string)
	}{
		{"agent.json unknown key", func(t *testing.T, ws string) {
			writeAgentJSONForTest(t, ws, `{"position_file": "position.md", "no_such_key": 1}`)
		}},
		{"agent.json memory_dir escapes", func(t *testing.T, ws string) {
			writeAgentJSONForTest(t, ws, `{"position_file": "position.md", "memory_dir": "../memory"}`)
		}},
		{"connectors.json absolute slot path", func(t *testing.T, ws string) {
			writeConnectorsJSONForTest(t, ws, `{"version": 1, "connectors": [`+statusConnOp+`], "repos": [{"name": "r", "remote": "o/r", "default_branch": "main", "connector": "a", "slots": {"provider": "clone", "paths": ["/abs/path"]}}]}`)
		}},
		{"connectors.json repo without owner", func(t *testing.T, ws string) {
			writeConnectorsJSONForTest(t, ws, `{"version": 1, "connectors": [`+statusConnOp+`], "repos": [{"name": "r", "remote": "r", "default_branch": "main", "connector": "a"}]}`)
		}},
		{"connectors.json unknown key", func(t *testing.T, ws string) {
			writeConnectorsJSONForTest(t, ws, `{"version": 1, "connectors": [], "repos": [], "no_such_key": 1}`)
		}},
		{"sources.json unknown key", func(t *testing.T, ws string) {
			writeSourcesDeclaration(t, ws, `{"version": 1, "sources": [], "no_such_key": 1}`)
		}},
	}
	for _, c := range cases {
		for _, mode := range []string{"json", "text"} {
			t.Run(c.name+"/"+mode, func(t *testing.T) {
				ws := setupJ2Workspace(t)
				c.setup(t, ws)
				if mode == "json" {
					requireJSONErrorEnvelope(t, []string{"status", "--workspace", ws}, 2, CodeConfigInvalid)
					return
				}
				code, stdout, stderr := statusRun(t, ws)
				if code != 2 || stdout != "" || !strings.Contains(stderr, string(CodeConfigInvalid)) {
					t.Fatalf("exit=%d stdout=%q stderr=%q, want 2/empty/config_invalid", code, stdout, stderr)
				}
			})
		}
	}
}

// 正しい 3 つの宣言でも、宣言が無くても、status は終了コード 0 で動く。
func TestStatus_ValidOrAbsentDeclarations_Exit0(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		ws := setupJ2Workspace(t)
		writeSourcesDeclaration(t, ws, cycleSourcesJSON)
		for _, extra := range [][]string{nil, {"--json"}} {
			if code, _, stderr := statusRun(t, ws, extra...); code != 0 {
				t.Fatalf("exit=%d stderr=%s", code, stderr)
			}
		}
	})
	t.Run("absent", func(t *testing.T) {
		ws := initializedWorkspace(t)
		if code, _, stderr := statusRun(t, ws); code != 0 {
			t.Fatalf("exit=%d stderr=%s", code, stderr)
		}
	})
}
