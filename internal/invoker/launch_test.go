package invoker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

// testRateLimitPrefix は core.IsRateLimited が枠超過と判定する先頭一致文字列
// （internal/core/quota.go の rateLimitPrefix と同じ値。判定規則の正本は
// core 側にあり、ここではその正本を信頼してフィクスチャを組み立てる）。
const testRateLimitPrefix = "You've hit your "

func setFakeClaudePath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir)
}

func newWorkspace(t *testing.T) string {
	t.Helper()
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".flywheel"), 0o755); err != nil {
		t.Fatalf("mkdir .flywheel: %v", err)
	}
	return ws
}

func baseLaunchInput(t *testing.T, ws string) core.JudgmentLaunchInput {
	t.Helper()
	return core.JudgmentLaunchInput{
		SessionID:    "11111111-1111-1111-1111-111111111111",
		Workspace:    ws,
		RunDir:       filepath.Join(ws, ".flywheel", "runs", "R-1"),
		OutputSchema: []byte(`{"type":"object"}`),
		MaxBudgetUSD: 1,
		TimeoutSec:   5,
		Stdin:        []byte("hello"),
	}
}

// AC「PATHにclaudeが無い環境で…invoker_unavailableで終わり」の invoker 側:
// Available は claude が居なければ ErrClaudeNotFound を返す。
func TestLauncher_Available_ClaudeNotFound(t *testing.T) {
	setFakeClaudePath(t, t.TempDir()) // 空のPATH
	l := NewLauncher()
	err := l.Available(context.Background())
	if err == nil {
		t.Fatal("want an error when claude is not on PATH")
	}
}

func TestLauncher_Available_ClaudeFound(t *testing.T) {
	dir := newFakeClaudeDir(t)
	setFakeClaudePath(t, dir)
	l := NewLauncher()
	if err := l.Available(context.Background()); err != nil {
		t.Fatalf("Available() = %v, want nil", err)
	}
}

func TestLauncher_InvokeJudgment_LaunchFailed_NoExecPermission(t *testing.T) {
	dir := t.TempDir()
	writeNoExecScript(t, dir)
	setFakeClaudePath(t, dir)

	ws := newWorkspace(t)
	l := NewLauncher()
	out, err := l.InvokeJudgment(context.Background(), baseLaunchInput(t, ws))
	if err != nil {
		t.Fatalf("InvokeJudgment returned an error: %v", err)
	}
	if out.Result != core.RunResultLaunchFailed {
		t.Errorf("Result = %v, want launch_failed", out.Result)
	}
}

func TestLauncher_InvokeJudgment_TimedOut(t *testing.T) {
	dir := newFakeClaudeDir(t)
	setFakeClaudePath(t, dir)

	ws := newWorkspace(t)
	in := baseLaunchInput(t, ws)
	in.TimeoutSec = 1
	fixturePath := writeFixture(t, fakeClaudeFixture{SleepSeconds: 5, ExitCode: 0, Stdout: `{"is_error":false,"structured_output":{}}`})
	t.Setenv(envFixture, fixturePath)

	l := NewLauncher()
	start := time.Now()
	out, err := l.InvokeJudgment(context.Background(), in)
	if err != nil {
		t.Fatalf("InvokeJudgment returned an error: %v", err)
	}
	if out.Result != core.RunResultTimedOut {
		t.Errorf("Result = %v, want timed_out", out.Result)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("took %s, want to be killed close to the 1s timeout", elapsed)
	}
}

func TestLauncher_InvokeJudgment_Succeeded_ArgsAndStdinRecorded(t *testing.T) {
	dir := newFakeClaudeDir(t)
	setFakeClaudePath(t, dir)

	ws := newWorkspace(t)
	in := baseLaunchInput(t, ws)
	in.Stdin = []byte("課題の本文 UNIQUE-MARKER-123")

	argvLog := filepath.Join(t.TempDir(), "argv.log")
	stdinLog := filepath.Join(t.TempDir(), "stdin.log")
	fixturePath := writeFixture(t, fakeClaudeFixture{
		Stdout:       `{"session_id":"11111111-1111-1111-1111-111111111111","is_error":false,"structured_output":{"ok":true},"total_cost_usd":0.42}`,
		ExitCode:     0,
		ArgvLogPath:  argvLog,
		StdinLogPath: stdinLog,
	})
	t.Setenv(envFixture, fixturePath)

	l := NewLauncher()
	out, err := l.InvokeJudgment(context.Background(), in)
	if err != nil {
		t.Fatalf("InvokeJudgment returned an error: %v", err)
	}
	if out.Result != core.RunResultSucceeded {
		t.Fatalf("Result = %v, want succeeded", out.Result)
	}
	if out.ReportedTotalCostUSD == nil || *out.ReportedTotalCostUSD != 0.42 {
		t.Errorf("ReportedTotalCostUSD = %v, want 0.42", out.ReportedTotalCostUSD)
	}

	// AC「判断の呼び出しの作業ディレクトリは、ワークスペースである」
	logged, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	if len(logged) == 0 {
		t.Fatal("argv log is empty")
	}

	// AC「課題の本文は…標準入力に現れる」
	stdinContent, err := os.ReadFile(stdinLog)
	if err != nil {
		t.Fatalf("read stdin log: %v", err)
	}
	if string(stdinContent) != string(in.Stdin) {
		t.Errorf("stdin passed to claude = %q, want %q", stdinContent, in.Stdin)
	}
}

// AC「判断の呼び出しの標準出力・標準エラー・渡した入力が、
// .flywheel/runs/<runのID>/に保存される」
func TestLauncher_InvokeJudgment_SavesArtifacts(t *testing.T) {
	dir := newFakeClaudeDir(t)
	setFakeClaudePath(t, dir)

	ws := newWorkspace(t)
	in := baseLaunchInput(t, ws)
	// 通常の運用では core.Init が .gitignore を作ってから run が起きる。
	// ここでもその前提を再現する（self-review指摘round2: .gitignoreが
	// 無い場合はensureRunsGitignoreEntryが何もしないよう変更したため）。
	if err := os.WriteFile(filepath.Join(ws, ".flywheel", ".gitignore"), []byte("flywheel.db\n"), 0o644); err != nil {
		t.Fatalf("seed .gitignore: %v", err)
	}
	fixturePath := writeFixture(t, fakeClaudeFixture{
		Stdout:   `{"is_error":false,"structured_output":{}}`,
		Stderr:   "warning: something",
		ExitCode: 0,
	})
	t.Setenv(envFixture, fixturePath)

	l := NewLauncher()
	if _, err := l.InvokeJudgment(context.Background(), in); err != nil {
		t.Fatalf("InvokeJudgment: %v", err)
	}

	stdout, err := os.ReadFile(filepath.Join(in.RunDir, "stdout.json"))
	if err != nil {
		t.Fatalf("read saved stdout: %v", err)
	}
	if string(stdout) != `{"is_error":false,"structured_output":{}}` {
		t.Errorf("saved stdout = %q", stdout)
	}
	stderr, err := os.ReadFile(filepath.Join(in.RunDir, "stderr.txt"))
	if err != nil {
		t.Fatalf("read saved stderr: %v", err)
	}
	if string(stderr) != "warning: something" {
		t.Errorf("saved stderr = %q", stderr)
	}
	stdin, err := os.ReadFile(filepath.Join(in.RunDir, "stdin.txt"))
	if err != nil {
		t.Fatalf("read saved stdin: %v", err)
	}
	if string(stdin) != string(in.Stdin) {
		t.Errorf("saved stdin = %q, want %q", stdin, in.Stdin)
	}

	// AC「runの保存先を作るとき、.flywheel/.gitignoreにrunsを除外する行が
	// 無ければ足す」。この検査は既存の.gitignore（通常の運用ではcore.Init
	// が作る）に行を足すケースを見る。.gitignoreがまったく無いケース
	// （self-review指摘round2: 新規作成するとcore.Initの復元機会を失う）は
	// gitignore_test.goのTestEnsureRunsGitignoreEntry_DoesNothingIfGitignoreMissing
	// が別途固定する。
	giPath := filepath.Join(ws, ".flywheel", ".gitignore")
	gi, err := os.ReadFile(giPath)
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !contains(splitLines(string(gi)), "runs/") {
		t.Errorf(".gitignore = %q, want a runs/ line", gi)
	}
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// AC「判断の呼び出しを1回実行した後、ワークスペースのGitで
// git status --porcelainを実行しても、.flywheel/runs/の下のファイルが
// 現れない」の前提（保存先が.flywheel/.gitignoreのrunsの下にあること）は
// ensureRunsGitignoreEntryのテスト（gitignore_test.go）が担保する。ここでは
// runsディレクトリ自体が.flywheel直下に作られることを確認する。
func TestLauncher_InvokeJudgment_RunDirIsUnderFlywheelRuns(t *testing.T) {
	dir := newFakeClaudeDir(t)
	setFakeClaudePath(t, dir)
	ws := newWorkspace(t)
	in := baseLaunchInput(t, ws)
	fixturePath := writeFixture(t, fakeClaudeFixture{Stdout: `{"is_error":false,"structured_output":{}}`})
	t.Setenv(envFixture, fixturePath)

	l := NewLauncher()
	if _, err := l.InvokeJudgment(context.Background(), in); err != nil {
		t.Fatalf("InvokeJudgment: %v", err)
	}
	wantPrefix := filepath.Join(ws, ".flywheel", "runs")
	if filepath.Dir(in.RunDir) != wantPrefix {
		t.Fatalf("RunDir = %s, want under %s", in.RunDir, wantPrefix)
	}
	if _, err := os.Stat(in.RunDir); err != nil {
		t.Fatalf("RunDir was not created: %v", err)
	}
}

// AC「返り値のsession_idが渡した値と違う結果では、runのsession_idが返り値の
// 値になり、不一致がrunに記録される」の invoker 側: SessionIDReturned を
// そのまま出力に含める（不一致の判定・記録は core.RunJudgment の責務）。
func TestLauncher_InvokeJudgment_ReturnsSessionIDFromResponse(t *testing.T) {
	dir := newFakeClaudeDir(t)
	setFakeClaudePath(t, dir)
	ws := newWorkspace(t)
	in := baseLaunchInput(t, ws)
	fixturePath := writeFixture(t, fakeClaudeFixture{
		Stdout: `{"session_id":"22222222-2222-2222-2222-222222222222","is_error":false,"structured_output":{}}`,
	})
	t.Setenv(envFixture, fixturePath)

	l := NewLauncher()
	out, err := l.InvokeJudgment(context.Background(), in)
	if err != nil {
		t.Fatalf("InvokeJudgment: %v", err)
	}
	if out.SessionIDReturned != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("SessionIDReturned = %q", out.SessionIDReturned)
	}
}

// AC「resultの先頭がYou've hit yourの結果は、runに枠超過が記録される
// （結果の表のsucceeded・erroredの両方で検証する）」
func TestLauncher_InvokeJudgment_RateLimitedOnSucceeded(t *testing.T) {
	dir := newFakeClaudeDir(t)
	setFakeClaudePath(t, dir)
	ws := newWorkspace(t)
	in := baseLaunchInput(t, ws)
	resp := map[string]any{
		"is_error":          false,
		"structured_output": map[string]any{},
		"result":            testRateLimitPrefix + "weekly limit",
	}
	b, _ := json.Marshal(resp)
	fixturePath := writeFixture(t, fakeClaudeFixture{Stdout: string(b)})
	t.Setenv(envFixture, fixturePath)

	l := NewLauncher()
	out, err := l.InvokeJudgment(context.Background(), in)
	if err != nil {
		t.Fatalf("InvokeJudgment: %v", err)
	}
	if out.Result != core.RunResultSucceeded {
		t.Fatalf("Result = %v, want succeeded", out.Result)
	}
	if !out.RateLimited {
		t.Error("RateLimited = false, want true")
	}
}

func TestLauncher_InvokeJudgment_RateLimitedOnErrored(t *testing.T) {
	dir := newFakeClaudeDir(t)
	setFakeClaudePath(t, dir)
	ws := newWorkspace(t)
	in := baseLaunchInput(t, ws)
	resp := map[string]any{
		"is_error": true,
		"subtype":  "error_other",
		"result":   testRateLimitPrefix + "session limit",
	}
	b, _ := json.Marshal(resp)
	fixturePath := writeFixture(t, fakeClaudeFixture{Stdout: string(b)})
	t.Setenv(envFixture, fixturePath)

	l := NewLauncher()
	out, err := l.InvokeJudgment(context.Background(), in)
	if err != nil {
		t.Fatalf("InvokeJudgment: %v", err)
	}
	if out.Result != core.RunResultErrored {
		t.Fatalf("Result = %v, want errored", out.Result)
	}
	if !out.RateLimited {
		t.Error("RateLimited = false, want true")
	}
}

// self-review 指摘（round1, code-reviewer/design-reviewer 双方が CONFIRMED）:
// writeNoExecScript（0o644）は exec.LookPath 自体が非実行ファイルを弾くため
// Available()/InvokeJudgment の早期 LookPath 分岐で launch_failed になり、
// cmd.Start の起動失敗パス（runResult.launchFailed）を一度も通らない。
// 「実行権はあるがインタプリタが存在しないシバン」で、LookPath は通り
// exec.Start が失敗する経路を検証する（AC「偽のclaudeが起動できない…runの
// 結果がlaunch_failedになり、費用が0になる」の本体をこちらで固定する）。
func TestLauncher_InvokeJudgment_LaunchFailed_BadInterpreter(t *testing.T) {
	dir := t.TempDir()
	badClaude := filepath.Join(dir, "claude")
	if err := os.WriteFile(badClaude, []byte("#!/nonexistent/interpreter-that-does-not-exist\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write bad-interpreter claude: %v", err)
	}
	setFakeClaudePath(t, dir)

	ws := newWorkspace(t)
	l := NewLauncher()
	out, err := l.InvokeJudgment(context.Background(), baseLaunchInput(t, ws))
	if err != nil {
		t.Fatalf("InvokeJudgment returned an error: %v", err)
	}
	if out.Result != core.RunResultLaunchFailed {
		t.Fatalf("Result = %v, want launch_failed (exec.LookPath must have found the file; cmd.Start must be what fails)", out.Result)
	}
}

// self-review 指摘（round1, code-reviewer/design-reviewer 双方が CONFIRMED,
// HIGH）: RunDir を作れない（＝claude をまだ起動していない）場合だけが
// launch_failed・費用0の対象であり、起動後の保存失敗はそうではない。
func TestLauncher_InvokeJudgment_RunDirCreationFailureIsLaunchFailedBeforeLaunch(t *testing.T) {
	dir := newFakeClaudeDir(t)
	setFakeClaudePath(t, dir)

	ws := newWorkspace(t)
	in := baseLaunchInput(t, ws)
	// RunDir の親を「ディレクトリではない通常ファイル」にし、MkdirAll を
	// 確実に失敗させる（claude を一度も起動させない）。
	blocker := filepath.Join(ws, ".flywheel", "runs")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("create blocking file: %v", err)
	}
	in.RunDir = filepath.Join(blocker, "R-1")

	argvLog := filepath.Join(t.TempDir(), "argv.log")
	fixturePath := writeFixture(t, fakeClaudeFixture{
		Stdout:      `{"is_error":false,"structured_output":{},"total_cost_usd":9.99}`,
		ArgvLogPath: argvLog,
	})
	t.Setenv(envFixture, fixturePath)

	l := NewLauncher()
	out, err := l.InvokeJudgment(context.Background(), in)
	if err != nil {
		t.Fatalf("InvokeJudgment returned an error: %v", err)
	}
	if out.Result != core.RunResultLaunchFailed {
		t.Fatalf("Result = %v, want launch_failed", out.Result)
	}
	if out.ReportedTotalCostUSD != nil {
		t.Errorf("ReportedTotalCostUSD = %v, want nil (claude was never launched)", out.ReportedTotalCostUSD)
	}
	if _, err := os.Stat(argvLog); err == nil {
		t.Error("the fake claude was invoked (argv log exists) even though RunDir could not be created")
	}
}

// self-review 指摘（round1, code-reviewer/design-reviewer 双方が CONFIRMED,
// HIGH）: claude が実際に起動・成功したのに、その後の出力の保存
// （saveRunArtifacts）が失敗しても、result・費用・structured_output を
// launch_failed/0へすり替えてはならない（§費用の記録の fail-closed は
// 「結果のJSONを得られなかった」ときの規則であり、得られたのに保存できな
// かったときの規則ではない）。
func TestLauncher_InvokeJudgment_ArtifactSaveFailureDoesNotOverrideSucceededResult(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses directory write permission checks")
	}
	dir := newFakeClaudeDir(t)
	setFakeClaudePath(t, dir)

	ws := newWorkspace(t)
	in := baseLaunchInput(t, ws)
	// RunDir を先に作っておき、書き込み不可にする。MkdirAll は既存の
	// ディレクトリに対して権限を見ないため成功するが、その後の
	// os.WriteFile（保存）は失敗する。
	if err := os.MkdirAll(in.RunDir, 0o755); err != nil {
		t.Fatalf("pre-create RunDir: %v", err)
	}
	if err := os.Chmod(in.RunDir, 0o500); err != nil {
		t.Fatalf("chmod RunDir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(in.RunDir, 0o755) })

	fixturePath := writeFixture(t, fakeClaudeFixture{
		Stdout: `{"session_id":"11111111-1111-1111-1111-111111111111","is_error":false,"structured_output":{"ok":true},"total_cost_usd":0.42}`,
	})
	t.Setenv(envFixture, fixturePath)

	l := NewLauncher()
	out, err := l.InvokeJudgment(context.Background(), in)
	if err != nil {
		t.Fatalf("InvokeJudgment returned an error: %v", err)
	}
	if out.Result != core.RunResultSucceeded {
		t.Fatalf("Result = %v, want succeeded (a save failure after a real launch must not change the classification)", out.Result)
	}
	if out.ReportedTotalCostUSD == nil || *out.ReportedTotalCostUSD != 0.42 {
		t.Errorf("ReportedTotalCostUSD = %v, want 0.42 (must not be discarded)", out.ReportedTotalCostUSD)
	}
	if string(out.StructuredOutput) != `{"ok":true}` {
		t.Errorf("StructuredOutput = %s, want it preserved", out.StructuredOutput)
	}
	if out.ErrorSummary == "" {
		t.Error("ErrorSummary is empty, want a note about the save failure")
	}
}
