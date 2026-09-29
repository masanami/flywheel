package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// このファイルは #86（`flywheel cycle` の CLI レベルのテスト）が使う、判断点ごとに
// 応答を変える偽の `claude` を提供する。fakeclaude_test.go の writeFakeClaude は
// 1 つの固定の応答しか返せないが、`cycle` は J1（分類）と J2（計画）を続けて呼ぶため、
// 起動の引数列（`--json-schema` に渡された出力スキーマ）で応答を選ぶ。

// fakeClaudeRoute は偽の claude の 1 つの応答規則。呼び出しの引数列（"$*"）に Match が
// 部分文字列として含まれる呼び出しに応答する（先に定義した規則が優先。Match が空なら常に
// 一致）。J1 の出力スキーマは `"mine"` を、J2 の出力スキーマは `"plan"` を含む。
type fakeClaudeRoute struct {
	Match string
	// Tag は順序ログの行 "claude <Tag> <課題のタイトル>" の Tag。
	Tag string
	// Stdout は標準出力にそのまま書く JSON（1 行）。
	Stdout string
	// SleepFirstSeconds が正なら、この規則の最初の呼び出しだけ、応答の前にその秒数眠る
	// （実行中の `cycle` を作るために使う。2 回目以降は眠らない）。
	SleepFirstSeconds int
	// StartedFile が非空なら、最初の呼び出しの開始時にこのファイルへ touch する。
	StartedFile string
}

// fakeClaudeJ1Match・fakeClaudeJ2Match は J1・J2 の起動の引数列を見分ける部分文字列
// （invoker が渡す出力スキーマの閉集合の値）。
const (
	fakeClaudeJ1Match = `"mine"`
	fakeClaudeJ2Match = `"plan"`
)

// putRoutedFakeClaudeOnPATH は routes に従って応答する偽の claude を PATH の先頭に足す。
// orderLogPath が非空なら、呼ばれるたびに "claude <Tag> <課題のタイトル>" をそのファイルへ
// 追記する（偽の gh と同じファイルを渡せば、呼び出しの順を検証できる）。どの規則にも
// 一致しない呼び出しは、標準エラーへ理由を書いて終了コード 1 で失敗する。
func putRoutedFakeClaudeOnPATH(t *testing.T, routes []fakeClaudeRoute, orderLogPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("動作環境は macOS と Linux のみ（CLAUDE.md）")
	}
	dir := t.TempDir()
	markerDir := t.TempDir()

	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("ARGS=\"$*\"\n")
	b.WriteString("INPUT=$(cat)\n")
	b.WriteString("TITLE=$(printf '%s\\n' \"$INPUT\" | grep -m1 'タイトル: ' | sed 's/.*タイトル: //')\n")
	b.WriteString("case \"$ARGS\" in\n")
	for i, r := range routes {
		fmt.Fprintf(&b, "*%s*)\n", shellSingleQuote(r.Match))
		if orderLogPath != "" {
			fmt.Fprintf(&b, "printf 'claude %%s %%s\\n' %s \"$TITLE\" >> %s\n", shellSingleQuote(r.Tag), shellSingleQuote(orderLogPath))
		}
		if r.StartedFile != "" {
			fmt.Fprintf(&b, "touch %s\n", shellSingleQuote(r.StartedFile))
		}
		if r.SleepFirstSeconds > 0 {
			marker := filepath.Join(markerDir, fmt.Sprintf("slept-%d", i))
			fmt.Fprintf(&b, "if [ ! -e %s ]; then touch %s; sleep %d; fi\n", shellSingleQuote(marker), shellSingleQuote(marker), r.SleepFirstSeconds)
		}
		delim := fmt.Sprintf("FAKECLAUDE_EOF_%d", i)
		fmt.Fprintf(&b, "cat <<'%s'\n%s\n%s\nexit 0\n;;\n", delim, r.Stdout, delim)
	}
	b.WriteString("*)\nprintf 'fake claude: no route for: %s\\n' \"$ARGS\" >&2\nexit 1\n;;\nesac\n")

	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(b.String()), 0o755); err != nil {
		t.Fatalf("write routed fake claude: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// readOrderLog は偽の gh・偽の claude が追記した順序ログを行ごとに返す（呼ばれた順）。
// ファイルが無ければ（1 回も呼ばれていなければ）nil を返す。
func readOrderLog(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read order log: %v", err)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// orderLogTags は順序ログの各行を "gh" または "claude <Tag>" の形へ縮約する
// （引数列や課題のタイトルを落とす。段の順の検証用）。
func orderLogTags(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		f := strings.Fields(l)
		switch {
		case len(f) == 0:
		case f[0] == "claude" && len(f) >= 2:
			out = append(out, "claude "+f[1])
		default:
			out = append(out, f[0])
		}
	}
	return out
}

// j1RouteMine は J1 に「mine・優先度 priority」を返す規則。
func j1RouteMine(priority string) fakeClaudeRoute {
	return fakeClaudeRoute{Match: fakeClaudeJ1Match, Tag: "J1", Stdout: j1MineFixture(priority)}
}

// j2RoutePlan は J2 に、既定の計画（j2PlanFixture）を返す規則。
func j2RoutePlan(t *testing.T) fakeClaudeRoute {
	t.Helper()
	stdout, _ := j2PlanFixture(t, nil)
	return fakeClaudeRoute{Match: fakeClaudeJ2Match, Tag: "J2", Stdout: stdout}
}
