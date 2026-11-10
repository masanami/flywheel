package invoker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// 形態 cli の委譲の起動（Launcher.InvokeDelegation の cli 経路）を、PATH に置いた偽の `harness`
// （引数を記録して固定の出力を返すシェルスクリプト）で検証する。

func fakeHarness(t *testing.T, script string) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "argv.log")
	body := "#!/bin/sh\necho \"$*|cwd=$(pwd)|marker=${FLYWHEEL_DELEGATED_RUN-unset}\" >> '" + logPath + "'\n" + script + "\n"
	if err := os.WriteFile(filepath.Join(dir, "harness"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/bin:/usr/bin")
	return logPath
}

func cliInput(t *testing.T, resume bool) core.DelegateLaunchInput {
	t.Helper()
	ws := t.TempDir()
	work := t.TempDir()
	in := core.DelegateLaunchInput{
		Form: core.ConnectorFormCLI, Workspace: ws, WorkDir: work, RunID: "R-7", TimeoutSec: 5,
		RunDir:    filepath.Join(ws, ".flywheel", "runs", "R-7"),
		CLIStart:  []string{"harness", "contract", "start", "ticket"},
		CLIStatus: []string{"harness", "contract", "status", "RID"},
		CLICancel: []string{"harness", "contract", "cancel", "RID"},
	}
	if resume {
		in.IsResume = true
		in.SessionID = "RID"
		in.CLIResume = []string{"harness", "contract", "resume", "RID"}
	}
	return in
}

func readLog(t *testing.T, p string) []string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestInvokeDelegation_CLI_StartPassesStdoutThroughInTheWorkDirWithTheDelegatedMarker(t *testing.T) {
	logPath := fakeHarness(t, `echo '{"contract_version":1}'`)
	in := cliInput(t, false)
	out, err := (&Launcher{}).InvokeDelegation(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != core.RunResultSucceeded || strings.TrimSpace(string(out.ContractOutput)) != `{"contract_version":1}` {
		t.Fatalf("out = %+v", out)
	}
	log := readLog(t, logPath)
	wantCwd, _ := filepath.EvalSymlinks(in.WorkDir)
	if len(log) != 1 || !strings.HasPrefix(log[0], "contract start ticket|") || !strings.Contains(log[0], "marker=R-7") {
		t.Fatalf("log = %v", log)
	}
	gotCwd, _ := filepath.EvalSymlinks(strings.TrimPrefix(strings.Split(log[0], "|")[1], "cwd="))
	if gotCwd != wantCwd {
		t.Errorf("cwd = %q, want %q", gotCwd, wantCwd)
	}
	if _, err := os.Stat(filepath.Join(in.RunDir, "stdout.json")); err != nil {
		t.Errorf("stdout was not saved under the run dir: %v", err)
	}
}

func TestInvokeDelegation_CLI_StartFailuresAfterTheConnectorRanAreNotLaunchFailedSoTheirCostIsCounted(t *testing.T) {
	// 実行ファイルが無い。
	t.Setenv("PATH", t.TempDir())
	out, _ := (&Launcher{}).InvokeDelegation(t.Context(), cliInput(t, false))
	if out.Result != core.RunResultLaunchFailed {
		t.Errorf("no harness: result = %s", out.Result)
	}
	// 非 0 終了（契約は「JSON を出力できたか」だけを終了コードで表す）。
	fakeHarness(t, `echo oops >&2; exit 3`)
	out, _ = (&Launcher{}).InvokeDelegation(t.Context(), cliInput(t, false))
	if out.Result != core.RunResultErrored || !strings.Contains(out.ErrorSummary, "oops") || out.ContractOutput != nil {
		t.Errorf("exit 3: %+v", out)
	}
	// 時間切れ。
	fakeHarness(t, `sleep 5`)
	in := cliInput(t, false)
	in.TimeoutSec = 1
	out, _ = (&Launcher{}).InvokeDelegation(t.Context(), in)
	if out.Result != core.RunResultTimedOut || !strings.Contains(out.ErrorSummary, "timed out") {
		t.Errorf("timeout: %+v", out)
	}
}

func TestInvokeDelegation_CLI_ResumeTimeoutCancelsTheConnectorRun(t *testing.T) {
	logPath := fakeHarness(t, `case "$2" in resume) sleep 5;; *) echo '{}';; esac`)
	in := cliInput(t, true)
	in.TimeoutSec = 1
	out, _ := (&Launcher{}).InvokeDelegation(t.Context(), in)
	if out.Result != core.RunResultTimedOut || !strings.Contains(out.ErrorSummary, "cancelled") {
		t.Fatalf("out = %+v", out)
	}
	log := readLog(t, logPath)
	if len(log) != 2 || !strings.HasPrefix(log[1], "contract cancel RID|") {
		t.Errorf("log = %v, want resume then cancel RID", log)
	}
}

func TestInvokeDelegation_CLI_ResumeWithoutOutputAsksForTheStatusOnce(t *testing.T) {
	logPath := fakeHarness(t, `case "$2" in resume) exit 1;; status) echo '{"contract_version":1,"state":"succeeded"}';; esac`)
	out, _ := (&Launcher{}).InvokeDelegation(t.Context(), cliInput(t, true))
	if out.Result != core.RunResultSucceeded || !strings.Contains(string(out.ContractOutput), `"succeeded"`) {
		t.Fatalf("out = %+v", out)
	}
	if log := readLog(t, logPath); len(log) != 2 || !strings.HasPrefix(log[1], "contract status RID|") {
		t.Errorf("log = %v", log)
	}

	fakeHarness(t, `exit 0`) // 終了コード 0 でも標準出力が空なら出力なし
	out, _ = (&Launcher{}).InvokeDelegation(t.Context(), cliInput(t, true))
	if out.Result != core.RunResultErrored || out.ContractOutput != nil {
		t.Errorf("no status either: %+v", out)
	}
}
