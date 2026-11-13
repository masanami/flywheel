package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// このファイルは #84（`classify --auto` の CLI レベルのテスト）が使う、
// 偽の `claude` を提供する。ingest_fakegh_test.go の偽の `gh`
// （シェルスクリプトを t.TempDir() に置き、PATH の先頭に足す方式）と同じ
// 作り方を、判断の呼び出し（`-p` と標準入力を受け取り、固定の JSON を
// 標準出力へ返す）向けに用意する。internal/invoker のテストが使う「テスト
// バイナリ自身を再 exec する」方式は internal/invoker パッケージの TestMain に
// 依存しており、internal/cli からは使えない。

// fakeClaudeOpts は writeFakeClaude が作る偽の claude 1 つの挙動。
type fakeClaudeOpts struct {
	// Stdout は標準出力にそのまま書く JSON 文字列（1 行）。
	Stdout string
	// ExitCode はこの偽の claude の終了コード。
	ExitCode int
	// SleepSeconds が正なら、標準出力を書く前にその秒数だけ眠る
	// （時間の上限・並行実行のテストに使う）。
	SleepSeconds int
	// StartedFile が非空なら、標準入力を受け取った直後にこのファイルへ
	// touch する（「起動された」ことをポーリングなしで合図するため）。
	StartedFile string
}

// writeFakeClaude は fakeClaudeOpts の挙動をする「claude」という実行ファイルを
// t.TempDir() に置き、そのディレクトリを返す。渡された引数列・標準入力は
// それぞれ argvLogPath・stdinLogPath へ記録する（空文字列なら記録しない）。
func writeFakeClaude(t *testing.T, opts fakeClaudeOpts, argvLogPath, stdinLogPath string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("動作環境は macOS と Linux のみ（CLAUDE.md）")
	}
	dir := t.TempDir()

	var b []byte
	add := func(s string) { b = append(b, []byte(s)...) }
	add("#!/bin/sh\n")
	if argvLogPath != "" {
		add("printf '%s\\n' \"$*\" >> " + shellSingleQuote(argvLogPath) + "\n")
	}
	if stdinLogPath != "" {
		add("cat > " + shellSingleQuote(stdinLogPath) + "\n")
	} else {
		add("cat > /dev/null\n")
	}
	if opts.StartedFile != "" {
		add("touch " + shellSingleQuote(opts.StartedFile) + "\n")
	}
	if opts.SleepSeconds > 0 {
		add(fmt.Sprintf("sleep %d\n", opts.SleepSeconds))
	}
	delim := "FAKECLAUDE_EOF"
	add("cat <<'" + delim + "'\n")
	add(opts.Stdout)
	add("\n" + delim + "\n")
	add(fmt.Sprintf("exit %d\n", opts.ExitCode))

	if err := os.WriteFile(filepath.Join(dir, "claude"), b, 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return dir
}

// putFakeClaudeOnPATH は writeFakeClaude の偽の claude を PATH の先頭に足す。
func putFakeClaudeOnPATH(t *testing.T, opts fakeClaudeOpts) (argvLogPath, stdinLogPath string) {
	t.Helper()
	dir := t.TempDir()
	argvLogPath = filepath.Join(dir, "argv.log")
	stdinLogPath = filepath.Join(dir, "stdin.log")
	claudeDir := writeFakeClaude(t, opts, argvLogPath, stdinLogPath)
	t.Setenv("PATH", claudeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argvLogPath, stdinLogPath
}

// emptyPATH は PATH を空のディレクトリへ差し替える（PATH に claude が無い
// 環境の再現。invoker_unavailable のテストが使う）。
func emptyPATH(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// waitForFileWithTimeout は path が現れるまで待つ（最大 timeout。100ms 間隔の
// ポーリング）。現れなければ Fatal する（AC-70 の並行実行テストで、一方の
// 偽の claude が本当に起動されたことをタイミング依存にせず確かめるために
// 使う）。
func waitForFileWithTimeout(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s to appear", path)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// writePositionFileForTest はワークスペース ws の下にポジション定義ファイルを
// 置き、.flywheel/agent.json からの相対パスを返す。
func writePositionFileForTest(t *testing.T, ws, content string) string {
	t.Helper()
	const rel = "position.md"
	if err := os.WriteFile(filepath.Join(ws, rel), []byte(content), 0o644); err != nil {
		t.Fatalf("write position file: %v", err)
	}
	return rel
}

// writeAgentJSONForTest はワークスペース ws に .flywheel/agent.json を書く。
func writeAgentJSONForTest(t *testing.T, ws, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ws, ".flywheel", "agent.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write agent.json: %v", err)
	}
}
