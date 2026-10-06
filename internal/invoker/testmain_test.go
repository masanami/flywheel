package invoker

// 偽の claude の実装。テストが PATH の先頭に置く実行ファイル「claude」は、
// このテストバイナリ自身を FLYWHEEL_FAKE_CLAUDE=1 付きで exec するだけの
// 薄いシェルスクリプトであり（newFakeClaudeDir 参照）、TestMain がその環境
// 変数を見て「偽の claude として振る舞うモード」へ分岐する。挙動は
// FAKE_CLAUDE_FIXTURE が指す JSON フィクスチャで指定する（M2 の偽の gh と
// 同じ方式。internal/adapters/github/testmain_test.go 参照）。

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	envFakeClaudeMode = "FLYWHEEL_FAKE_CLAUDE"
	envFixture        = "FAKE_CLAUDE_FIXTURE"
)

func TestMain(m *testing.M) {
	if os.Getenv(envFakeClaudeMode) == "1" {
		os.Exit(runFakeClaude())
	}
	// 委譲の子が flywheel 自身のリポジトリでテストを回しても落ちないよう、委譲の目印を外す（AC-219d）。
	_ = os.Unsetenv("FLYWHEEL_DELEGATED_RUN")
	// AC「テストを除くGoのコードに、claudeの絶対パスを含む環境固有の絶対パスが
	// 含まれない」「go test ./...は、PATHに本物のclaudeがあっても、それを
	// 起動しない」を裏付ける: 既定のPATHを空のディレクトリにし、テストが
	// 明示的に偽のclaudeを置かない限りexec.LookPath("claude")が本物へ到達
	// しないようにする（internal/adapters/github/testmain_test.goと同じ方針）。
	sandbox, err := os.MkdirTemp("", "flywheel-empty-path-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: mkdir temp: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("PATH", sandbox); err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: setenv PATH: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(sandbox)
	if sharedFakeClaudeDir != "" {
		_ = os.RemoveAll(sharedFakeClaudeDir)
	}
	os.Exit(code)
}

// fakeClaudeFixture は偽の claude の1回の起動の挙動を記述する（テストが
// t.TempDir() へ書き、FAKE_CLAUDE_FIXTURE で渡す）。
type fakeClaudeFixture struct {
	Stdout       string `json:"stdout"`
	Stderr       string `json:"stderr"`
	ExitCode     int    `json:"exit_code"`
	SleepSeconds int    `json:"sleep_seconds"`
	// ArgvLogPath が非空なら、受け取った argv（os.Args[1:]）をJSON配列の
	// 1行としてこのファイルへ追記する。
	ArgvLogPath string `json:"argv_log_path"`
	// StdinLogPath が非空なら、受け取った標準入力をそのまま書き出す。
	StdinLogPath string `json:"stdin_log_path"`
	// CwdLogPath が非空なら、起動時の作業ディレクトリをこのファイルへ書く。
	CwdLogPath string `json:"cwd_log_path"`
	// EnvLogPath が非空なら、委譲の目印の環境変数（FLYWHEEL_DELEGATED_RUN）の有無と値を
	// 「present=<true|false> value=<値>」の1行でこのファイルへ書く。
	EnvLogPath string `json:"env_log_path"`
}

func runFakeClaude() int {
	fixturePath := os.Getenv(envFixture)
	if fixturePath == "" {
		fmt.Fprintln(os.Stderr, "fake claude: no fixture (FAKE_CLAUDE_FIXTURE not set)")
		return 1
	}
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake claude: read fixture: %v\n", err)
		return 1
	}
	var fx fakeClaudeFixture
	if err := json.Unmarshal(data, &fx); err != nil {
		fmt.Fprintf(os.Stderr, "fake claude: decode fixture: %v\n", err)
		return 1
	}

	if fx.ArgvLogPath != "" {
		appendArgvLog(fx.ArgvLogPath, os.Args[1:])
	}
	if fx.CwdLogPath != "" {
		wd, _ := os.Getwd()
		_ = os.WriteFile(fx.CwdLogPath, []byte(wd), 0o644)
	}
	if fx.EnvLogPath != "" {
		v, ok := os.LookupEnv("FLYWHEEL_DELEGATED_RUN")
		_ = os.WriteFile(fx.EnvLogPath, []byte(fmt.Sprintf("present=%t value=%s", ok, v)), 0o644)
	}
	if fx.StdinLogPath != "" {
		b, _ := io.ReadAll(os.Stdin)
		_ = os.WriteFile(fx.StdinLogPath, b, 0o644)
	} else {
		_, _ = io.Copy(io.Discard, os.Stdin)
	}

	if fx.SleepSeconds > 0 {
		time.Sleep(time.Duration(fx.SleepSeconds) * time.Second)
	}

	fmt.Print(fx.Stdout)
	fmt.Fprint(os.Stderr, fx.Stderr)
	return fx.ExitCode
}

func appendArgvLog(path string, argv []string) {
	b, err := json.Marshal(argv)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "%s\n", b)
}

// --- 偽の claude の PATH への設置 ---

var (
	sharedFakeClaudeDirOnce sync.Once
	sharedFakeClaudeDir     string
	sharedFakeClaudeDirErr  error
)

// newFakeClaudeDir は「claude」という名前の実行ファイル（このテストバイナリを
// 再execするだけの薄いラッパー）を1度だけ作ったディレクトリを返す
// （internal/adapters/github.newFakeGHDir と同じ、暖機つきの使い回し方式）。
func newFakeClaudeDir(t *testing.T) string {
	t.Helper()
	sharedFakeClaudeDirOnce.Do(func() {
		sharedFakeClaudeDir, sharedFakeClaudeDirErr = createAndWarmFakeClaudeDir()
	})
	if sharedFakeClaudeDirErr != nil {
		t.Fatalf("newFakeClaudeDir: %v", sharedFakeClaudeDirErr)
	}
	return sharedFakeClaudeDir
}

func createAndWarmFakeClaudeDir() (string, error) {
	testBinary, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("os.Executable: %w", err)
	}
	dir, err := os.MkdirTemp("", "flywheel-fake-claude-")
	if err != nil {
		return "", fmt.Errorf("mkdir temp: %w", err)
	}
	scriptPath := filepath.Join(dir, "claude")
	script := fmt.Sprintf("#!/bin/sh\n%s=1 exec %s \"$@\"\n", envFakeClaudeMode, shellQuote(testBinary))
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("write fake claude script: %w", err)
	}

	warmup := exec.Command(scriptPath)
	warmup.Env = []string{}
	_ = warmup.Run()

	return dir, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writeFixture は fx を JSON として t.TempDir() 配下に書き、パスを返す。
func writeFixture(t *testing.T, fx fakeClaudeFixture) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.json")
	b, err := json.Marshal(fx)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// writeNoExecScript は path に「実行権の無い」ファイルを置く（起動失敗の
// フィクスチャ）。
func writeNoExecScript(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatalf("write no-exec claude: %v", err)
	}
	return dir
}
