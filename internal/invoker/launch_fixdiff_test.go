package invoker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
)

// コンフリクトの解消の確かめ（J5）の入力は、実際の起動（Launcher.InvokeJudgment が偽の claude を起動する）でも、標準入力のデータの区画
// 「コンフリクトの解消の差分」の内側に、直したファイルの差分が入る。区画の名前は core の入力の組み立て（fixCheckSections）と同じ文字列だが、
// invoker からは core の定数を読めないので、名前を固定するのは core 側の試験（TestJ5Fix_InputCarriesTheConflictFixDiffSection）。
func TestLauncher_InvokeJudgment_J5FixCheckStdinCarriesTheDiffInsideItsDataSection(t *testing.T) {
	setFakeClaudePath(t, newFakeClaudeDir(t))
	ws := newWorkspace(t)
	in := baseLaunchInput(t, ws)
	in.Stdin = nil
	in.Judgment = core.JudgmentJ5
	const diff = "a.go\n@@ -1 +1 @@\n-old\n+RESOLVED-LINE-XYZZY"
	in.Sections = []core.JudgmentDataSection{
		{Label: "コンフリクトの解消の差分", Content: diff},
	}
	stdinLog := filepath.Join(t.TempDir(), "stdin.log")
	t.Setenv(envFixture, writeFixture(t, fakeClaudeFixture{
		Stdout:       `{"session_id":"11111111-1111-1111-1111-111111111111","is_error":false,"structured_output":{"ok":true},"total_cost_usd":0.1}`,
		StdinLogPath: stdinLog,
	}))

	out, err := NewLauncher().InvokeJudgment(context.Background(), in)
	if err != nil || out.Result != core.RunResultSucceeded {
		t.Fatalf("InvokeJudgment = %+v, %v, want succeeded", out, err)
	}
	b, err := os.ReadFile(stdinLog)
	if err != nil {
		t.Fatal(err)
	}
	stdin := string(b)
	begin := strings.Index(stdin, dataSectionBeginPrefix+"コンフリクトの解消の差分"+dataSectionBeginSuffix)
	end := strings.Index(stdin[max(begin, 0):], dataSectionEnd)
	at := strings.Index(stdin, "RESOLVED-LINE-XYZZY")
	if begin < 0 || end < 0 || at < begin || at > begin+end {
		t.Fatalf("the diff is not inside the data section (begin=%d end=%d at=%d):\n%s", begin, end, at, stdin)
	}
	instructions, err := Instructions(core.JudgmentJ5)
	if err != nil || !strings.HasPrefix(stdin, instructions) {
		t.Errorf("stdin must start with the embedded J5 instructions (err=%v)", err)
	}
	// 渡した入力は、実行の記録（RunDir）にも残る。
	if saved, err := os.ReadFile(filepath.Join(in.RunDir, "stdin.txt")); err != nil || !strings.Contains(string(saved), "RESOLVED-LINE-XYZZY") {
		t.Errorf("the saved input lacks the diff (err=%v): %s", err, saved)
	}
}
