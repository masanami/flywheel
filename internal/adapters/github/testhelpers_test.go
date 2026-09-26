package github

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeExecutableScript は path に実行可能なシェルスクリプト content を書く
// テストヘルパー（偽の gh の設置に使う）。
func writeExecutableScript(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write script %s: %v", path, err)
	}
}

// sleepSeconds は fakeGHSleep から呼ぶ薄いラッパー（テストヘルパーとして
// 同居させ、本体の import を最小にする）。
func sleepSeconds(n int) {
	time.Sleep(time.Duration(n) * time.Second)
}

// sharedFakeGHDir・sharedFakeGHDirOnce・sharedFakeGHDirErr は、偽の gh の
// 「gh」スクリプト（このテストバイナリ自身を FLYWHEEL_FAKE_GH=1 付きで
// exec するだけの薄いラッパー）をテストバイナリのプロセス全体で 1 度だけ
// 作り、以後の全テストで使い回す。
//
// 当初はテストごとに t.TempDir() へ新規のスクリプトファイルを書いていたが、
// -race ビルドでは「初めて実行されるまっさらな実行ファイル」の起動が
// 数百 ms〜数秒単位で遅れることがあり（macOS のファイルシステム・
// スキャン等に起因すると見られる）、タイムアウトを検証するテスト
// （200ms で打ち切るはずが、プロセスの起動自体がそれより遅れる）が
// 稀に flaky になった（self-review 実測: go test -race -count=5 で
// 2/5 回失敗）。スクリプトを使い回し、かつ初回だけ同期的に 1 回
// 実行して「暖機」しておくことで、タイミングに依存するテストの区間から
// この初回起動コストを追い出す。
var (
	sharedFakeGHDirOnce sync.Once
	sharedFakeGHDir     string
	sharedFakeGHDirErr  error
)

// newFakeGHDir は sharedFakeGHDir を返す（テストごとの引数は不要。
// 呼び出し側は t.Setenv で挙動を切り替える）。
func newFakeGHDir(t *testing.T) string {
	t.Helper()
	sharedFakeGHDirOnce.Do(func() {
		sharedFakeGHDir, sharedFakeGHDirErr = createAndWarmFakeGHDir()
	})
	if sharedFakeGHDirErr != nil {
		t.Fatalf("newFakeGHDir: %v", sharedFakeGHDirErr)
	}
	return sharedFakeGHDir
}

func createAndWarmFakeGHDir() (string, error) {
	testBinary, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("os.Executable: %w", err)
	}
	dir, err := os.MkdirTemp("", "flywheel-fake-gh-")
	if err != nil {
		return "", fmt.Errorf("mkdir temp: %w", err)
	}
	scriptPath := filepath.Join(dir, "gh")
	script := fmt.Sprintf("#!/bin/sh\n%s=1 exec %s \"$@\"\n", envFakeGHMode, shellQuote(testBinary))
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("write fake gh script: %w", err)
	}

	// 暖機: シナリオ未指定（=default ケースで即座に終了コード 1）で 1 回
	// 同期実行し、初回起動コストをここで払っておく。
	// 呼び出したテストの FAKE_GH_* を引き継がないよう、空の環境で起動する。
	warmup := exec.Command(scriptPath)
	warmup.Env = []string{}
	_ = warmup.Run()

	return dir, nil
}

// shellQuote は s を POSIX sh のシングルクォート文字列として安全に埋め込む
// （s 自体にシングルクォートが含まれる場合も壊れない形にする）。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// newFakeGHLogPath は AC-22（GET 専用の検証）用の呼び出しログの置き場を
// 返す。
func newFakeGHLogPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "invocations.log")
}

// readInvocationLog は newFakeGHLogPath が返すログ（1 行 1 呼び出し、JSON
// 配列）を読み、各呼び出しの argv のスライスとして返す。
func readInvocationLog(t *testing.T, logPath string) [][]string {
	t.Helper()
	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("open invocation log %s: %v", logPath, err)
	}
	defer func() { _ = f.Close() }()

	var calls [][]string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var argv []string
		if err := json.Unmarshal([]byte(line), &argv); err != nil {
			t.Fatalf("decode invocation log line %q: %v", line, err)
		}
		calls = append(calls, argv)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan invocation log: %v", err)
	}
	return calls
}

// requireOnlyGETCalls は AC-22 を検証する: logPath に記録された全呼び出しが
// 「api」で始まり、-X／--method に GET 以外が無く、
// -f／-F／--field／--raw-field／--input が無いことを確かめる。
func requireOnlyGETCalls(t *testing.T, logPath string) {
	t.Helper()
	calls := readInvocationLog(t, logPath)
	if len(calls) == 0 {
		t.Fatal("requireOnlyGETCalls: no invocations recorded")
	}
	forbidden := map[string]bool{
		"-f": true, "-F": true, "--field": true, "--raw-field": true, "--input": true,
	}
	for _, argv := range calls {
		if len(argv) == 0 || argv[0] != "api" {
			t.Errorf("invocation %v does not start with %q", argv, "api")
			continue
		}
		for i, a := range argv {
			if forbidden[a] {
				t.Errorf("invocation %v contains forbidden flag %q", argv, a)
			}
			// pflag が受け付ける連結形（--field=k=v・-fk=v・-XPOST 等）も拒む。
			if isJoinedWriteFlag(a) {
				t.Errorf("invocation %v contains forbidden flag form %q", argv, a)
			}
			if strings.HasPrefix(a, "-X") && a != "-X" && a != "-XGET" && a != "-X=GET" {
				t.Errorf("invocation %v uses %s", argv, a)
			}
			if a == "-X" || a == "--method" {
				if i+1 >= len(argv) || argv[i+1] != "GET" {
					t.Errorf("invocation %v uses %s with a non-GET value", argv, a)
				}
			}
			if strings.HasPrefix(a, "--method=") && a != "--method=GET" {
				t.Errorf("invocation %v uses %s", argv, a)
			}
		}
	}
}

// isJoinedWriteFlag は、値を連結した形の書き込み系フラグ（--field=k=v・
// --raw-field=k=v・--input=file・-fk=v・-Fk=v）かどうかを判定する。
func isJoinedWriteFlag(a string) bool {
	for _, p := range []string{"--field=", "--raw-field=", "--input="} {
		if strings.HasPrefix(a, p) {
			return true
		}
	}
	return !strings.HasPrefix(a, "--") && len(a) > 2 && (strings.HasPrefix(a, "-f") || strings.HasPrefix(a, "-F"))
}
