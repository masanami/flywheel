package cli

import (
	"strings"
	"testing"
	"time"
)

// 受け入れの通し（M4 S3 #258）: 端末の無い子プロセス（標準入力はパイプ）から、LLM の口
// （--relayed）で承認を記録し、人間用の口（--relayed なし）は従来どおり tty_required、
// （委譲の環境の拒否は、子プロセスの TestMain が目印を外すため
// TestRelayed_DelegatedEnvRejectsEvenWithRelayed が in-process で検査する。）

func runPiped(t *testing.T, args []string, extraEnv ...string) (code int, stdout, stderr string) {
	t.Helper()
	cmd, out, errb := newChildCmd(args, extraEnv...)
	cmd.Stdin = strings.NewReader("")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	code = waitChild(t, cmd, 20*time.Second)
	return code, out.String(), errb.String()
}

func TestRelayedE2E_ProcessLevelFlow(t *testing.T) {
	ws := initializedWorkspace(t)
	id := planPendingChallenge(t, ws)

	// 人間用の口: 端末が無ければ tty_required のまま（何も書かない）。
	code, _, stderr := runPiped(t, []string{"approve", id, "--workspace", ws, "--json"})
	if code != 1 || readErrorCode(t, stderr) != string(CodeTTYRequired) {
		t.Fatalf("approve w/o --relayed: code=%d stderr=%s", code, stderr)
	}

	// 手がかりの欠落: usage_error・何も書かない。
	code, _, stderr = runPiped(t, []string{"approve", id, "--relayed", "--session", "s", "--workspace", ws, "--json"})
	if code != 2 || readErrorCode(t, stderr) != string(CodeUsageError) {
		t.Fatalf("missing quote: code=%d stderr=%s", code, stderr)
	}

	// LLM の口: CLAUDECODE が設定されていても通り、手がかりが作業ログに残る。
	code, _, stderr = runPiped(t, []string{"approve", id, "--relayed", "--session", "sess-e2e", "--quote", "計画で進めてください", "--workspace", ws, "--json"},
		claudeCodeEnvVar+"=1")
	if code != 0 {
		t.Fatalf("relayed approve: code=%d stderr=%s", code, stderr)
	}
	if got := workStatus(t, ws, id); got == "awaiting_plan_approval" {
		t.Errorf("status = %q, want advanced", got)
	}
	requireRelayedRecorded(t, ws, "sess-e2e", "計画で進めてください")
}
