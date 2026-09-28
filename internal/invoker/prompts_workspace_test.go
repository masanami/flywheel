package invoker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// TestInvokeJudgment_UsesEmbeddedInstructions_NotWorkspaceFiles は AC
// 「実行時に指示文をワークスペースから読まない（ワークスペースに同名の
// ファイルを置いても、偽のclaudeの標準入力が埋め込みの指示文のままである
// ことで検証する）」の検査本体。
//
// self-review 指摘（round1, code-reviewer CONFIRMED）: 当初はワークスペースに
// 同名ファイルを置くだけで、テストプロセス自体のカレントディレクトリは
// internal/invoker のままだった。Instructions は embed.FS からしか読まない
// ため、この状態ではワークスペースの汚染ファイルへ到達する経路が実装のどこ
// にも無く、テストは「本来失敗しうる操作」を一度も踏まずに通っていた（相対
// パスでの os.ReadFile へ実装を後退させても、cwd が internal/invoker のまま
// では本物の prompts/j1.md（内容が同じ）を読むだけで検知できない）。
// t.Chdir でテストプロセスの cwd をワークスペース直下へ切り替えることで、
// 「実装が cwd 相対の相対パス読み込みに後退した場合」に汚染ファイルへ到達
// しうる状況を実際に作り、それでも embed の内容が返ることを検証する。
func TestInvokeJudgment_UsesEmbeddedInstructions_NotWorkspaceFiles(t *testing.T) {
	dir := newFakeClaudeDir(t)
	setFakeClaudePath(t, dir)

	ws := newWorkspace(t)
	poisonPaths := []string{
		// Instructions が仮に cwd 相対の "prompts/j1.md" を読む実装に
		// 後退した場合に踏むはずのパス。
		filepath.Join(ws, "prompts", "j1.md"),
		filepath.Join(ws, "prompts", "brief", "decider.md"),
		// ワークスペースの中に誤って本パッケージと同じ相対構造を置いた
		// ケースも想定して置いておく。
		filepath.Join(ws, "internal", "invoker", "prompts", "j1.md"),
	}
	for _, p := range poisonPaths {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir for poison file: %v", err)
		}
		if err := os.WriteFile(p, []byte("WORKSPACE POISONED CONTENT: "+p), 0o644); err != nil {
			t.Fatalf("write poison file: %v", err)
		}
	}

	// テストプロセス自身の cwd をワークスペースへ切り替える（t.Chdir は
	// テスト終了時に元へ戻す）。embed.FS を使う限りこの切り替えは
	// Instructions・BriefFixedSections の戻り値に影響しないはずであり、
	// 影響が無いこと自体がこのテストの主張である。
	t.Chdir(ws)

	embedded, err := Instructions(core.JudgmentJ1)
	if err != nil {
		t.Fatalf("Instructions(J1): %v", err)
	}
	if strings.Contains(embedded, "WORKSPACE POISONED CONTENT") {
		t.Fatalf("Instructions(J1) returned workspace-poisoned content: %q", embedded)
	}

	sections, err := BriefFixedSections()
	if err != nil {
		t.Fatalf("BriefFixedSections(): %v", err)
	}
	for _, s := range sections {
		if strings.Contains(s.Body, "WORKSPACE POISONED CONTENT") {
			t.Fatalf("BriefFixedSections() section %q returned workspace-poisoned content: %q", s.ID, s.Body)
		}
	}

	stdin := BuildStdin(embedded, nil)

	// InvokeJudgment 自体は in.Workspace を子プロセスの作業ディレクトリに
	// 使うだけであり（cwd の切り替えとは別経路）、渡した stdin をそのまま
	// 子へ渡す。ここでは「組み立てた stdin が汚染されていないこと」に加え、
	// 実際に子へ渡る標準入力も embed の内容のままであることを確認する。
	in := baseLaunchInput(t, ws)
	in.Stdin = stdin

	stdinLog := filepath.Join(t.TempDir(), "stdin.log")
	fixturePath := writeFixture(t, fakeClaudeFixture{
		Stdout:       `{"session_id":"11111111-1111-1111-1111-111111111111","is_error":false,"structured_output":{"ok":true}}`,
		ExitCode:     0,
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

	got, err := os.ReadFile(stdinLog)
	if err != nil {
		t.Fatalf("read stdin log: %v", err)
	}
	gotStr := string(got)
	if !strings.Contains(gotStr, embedded) {
		t.Errorf("stdin sent to claude does not contain the embedded instructions")
	}
	if strings.Contains(gotStr, "WORKSPACE POISONED CONTENT") {
		t.Errorf("stdin sent to claude contains workspace-file content instead of the embedded instructions")
	}
}
