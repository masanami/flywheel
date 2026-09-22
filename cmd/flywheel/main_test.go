package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// newChildCmd は bin を args で実行する *exec.Cmd を、FLYWHEEL_WORKSPACE を
// 明示的に空へ中和した環境で組み立てる。開発者・CI の実行環境に
// FLYWHEEL_WORKSPACE がたまたま設定されていても、--workspace を明示するケース
// （env より優先されるので影響しない）・cwd 起点の探索に頼るケース（env が
// 設定されていると親へ遡らず即座に別の場所を見てしまう）のどちらもこの汚染を
// 受けないようにする（レビュー指摘 item 8(e)）。
func newChildCmd(bin string, args ...string) *exec.Cmd {
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "FLYWHEEL_WORKSPACE=")
	return cmd
}

// buildBinary は ./cmd/flywheel を CGO_ENABLED=0 でビルドし、生成物のパスを返す。
// AC-78・AC-74・AC-77 の一部は、実際にビルドしたバイナリへの子プロセス起動で検証する。
func buildBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "flywheel")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build ./cmd/flywheel failed: %v\n%s", err, out)
	}
	return bin
}

func TestBinary_HelpExitsZeroWithUsageOnStdout(t *testing.T) {
	bin := buildBinary(t)
	cmd := newChildCmd(bin, "--help")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("--help failed: %v (stderr=%s)", err, stderr.String())
	}
	if stdout.Len() == 0 {
		t.Fatal("--help produced no stdout")
	}
	if stderr.Len() != 0 {
		t.Fatalf("--help produced stderr: %q", stderr.String())
	}
}

func TestBinary_UnknownFlagExitsTwoWithJSONUsageError(t *testing.T) {
	bin := buildBinary(t)
	cmd := newChildCmd(bin, "create", "--nope", "x", "--json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *exec.ExitError, got %v", err)
	}
	if exitErr.ExitCode() != 2 {
		t.Fatalf("exit code = %d, want 2", exitErr.ExitCode())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout should be empty on failure, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), `"code":"usage_error"`) {
		t.Fatalf("stderr = %q, want usage_error envelope", stderr.String())
	}
}

// runOK は bin を args で実行し、終了コード 0 でなければテストを失敗させる。
func runOK(t *testing.T, bin string, args ...string) {
	t.Helper()
	cmd := newChildCmd(bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %v failed: %v (stdout=%s stderr=%s)", bin, args, err, stdout.String(), stderr.String())
	}
}

// TestBinary_InitCreatesStoreAndStatusSeesIt は init → status を実バイナリの
// 子プロセスとして通しで実行し、AC「flywheel init を実行すると…終了コード 0」と
// 「init 以外のコマンドは…上位の .flywheel/flywheel.db を使う」の一部を、
// CGO_ENABLED=0 のビルド成果物で検証する。
func TestBinary_InitCreatesStoreAndStatusSeesIt(t *testing.T) {
	bin := buildBinary(t)
	ws := t.TempDir()
	runOK(t, bin, "init", "--workspace", ws, "--json")
	if _, err := os.Stat(filepath.Join(ws, ".flywheel", "flywheel.db")); err != nil {
		t.Fatalf("store not created by the built binary: %v", err)
	}

	child := filepath.Join(ws, "sub", "dir")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cmd := newChildCmd(bin, "status", "--json")
	cmd.Dir = child
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("status from a subdirectory failed: %v (stdout=%s stderr=%s)", err, stdout.String(), stderr.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("status stdout is not JSON: %v (stdout=%s)", err, stdout.String())
	}
	for _, key := range []string{"needs_human", "actionable", "approved"} {
		if _, ok := doc[key]; !ok {
			t.Fatalf("status output missing %q (store found upward): %s", key, stdout.String())
		}
	}
}

// resolveActorLikeCore は internal/core.resolveActor と同じ優先順位
// （os/user.Current().Username → $USER → $LOGNAME）で、このテストプロセス自身の
// actor を解決する。userCurrentOK は os/user.Current() 自体が成功したかを表す
// （CGO_ENABLED=0 でビルドした対象バイナリと、この go test バイナリの cgo 設定が
// 異なりうるため、この戻り値はあくまで参考値であり完全な代替ではない。既知の
// 限界として実装依頼の返却に明記する）。
func resolveActorLikeCore(t *testing.T) (actor string, userCurrentOK bool) {
	t.Helper()
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username, true
	}
	if v := os.Getenv("USER"); v != "" {
		return v, false
	}
	if v := os.Getenv("LOGNAME"); v != "" {
		return v, false
	}
	t.Fatal("could not resolve an actor for the test process (no user.Current, USER, LOGNAME)")
	return "", false
}

// createReporter は bin（CGO_ENABLED=0 でビルド済み）で init 済みの ws に対して
// `create --title x --json` を実行し、返った reporter を返す。
func createReporter(t *testing.T, bin, ws string, extraEnv ...string) string {
	t.Helper()
	cmd := newChildCmd(bin, "create", "--title", "x", "--workspace", ws, "--json")
	cmd.Env = append(cmd.Env, extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("create failed: %v (stderr=%s)", err, stderr.String())
	}
	var doc struct {
		Challenge struct {
			Reporter string `json:"reporter"`
		} `json:"challenge"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("parse stdout: %v (%s)", err, stdout.String())
	}
	return doc.Challenge.Reporter
}

// TestBinary_ReporterMatchesTestProcessActorResolution は、CGO_ENABLED=0 で
// ビルドしたバイナリの `create` が返す reporter が (a) 空でなく (b) このテスト
// プロセス自身が internal/core.resolveActor と同じ規則で解決した値と一致する
// ことを確認する（docs/features/m1-core.md §作業ログ・actor の解決規則の
// 実バイナリでの検証。runtime.GOOS でスキップしない）。
func TestBinary_ReporterMatchesTestProcessActorResolution(t *testing.T) {
	bin := buildBinary(t)
	ws := t.TempDir()
	runOK(t, bin, "init", "--workspace", ws, "--json")

	wantReporter, userCurrentOK := resolveActorLikeCore(t)
	reporter := createReporter(t, bin, ws)
	t.Logf("built-binary reporter = %q; test-process resolution = %q (user.Current ok=%v)", reporter, wantReporter, userCurrentOK)

	if reporter == "" {
		t.Fatal("reporter is empty")
	}
	if reporter != wantReporter {
		t.Fatalf("reporter = %q, want %q (same os/user.Current -> USER -> LOGNAME resolution rule as the test process)", reporter, wantReporter)
	}
}

// TestBinary_ReporterResolutionPrioritizesUserCurrentOverInjectedEnv は、
// USER・LOGNAME を偽装しても、os/user.Current() が成功する環境では reporter が
// それらの値にならないことを確認する（優先順位の確認）。go's syscall/os の
// 環境変数解決は同名キーの最後の出現を優先するため、newChildCmd が返す Env の
// 末尾に追記した USER/LOGNAME が、この子プロセス（Go バイナリ）の
// os.Getenv からは有効な上書きとして観測される。
//
// 既知の限界: userCurrentOK はこのテストプロセス（go test バイナリ、既定の cgo
// 設定でビルドされる）で計測した値であり、対象バイナリ（CGO_ENABLED=0）の
// os/user.Current() の成否そのものではない。両者が食い違う環境
// （例: cgo 版は成功するが CGO_ENABLED=0 版は失敗する）では、この事前条件つきの
// assertion が本来検証したい分岐を捉えられない可能性がある。
func TestBinary_ReporterResolutionPrioritizesUserCurrentOverInjectedEnv(t *testing.T) {
	bin := buildBinary(t)
	ws := t.TempDir()
	runOK(t, bin, "init", "--workspace", ws, "--json")

	_, userCurrentOK := resolveActorLikeCore(t)
	reporter := createReporter(t, bin, ws, "USER=injected-user", "LOGNAME=injected-logname")
	t.Logf("reporter with injected USER/LOGNAME = %q (test-process user.Current ok=%v)", reporter, userCurrentOK)

	if reporter == "" {
		t.Fatal("reporter is empty")
	}
	if userCurrentOK && (reporter == "injected-user" || reporter == "injected-logname") {
		t.Fatalf("reporter = %q: os/user.Current() succeeded for this process, so the injected USER/LOGNAME override must not win (priority order)", reporter)
	}
}

func TestBinary_NoArgsExitsTwo(t *testing.T) {
	bin := buildBinary(t)
	cmd := newChildCmd(bin)
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *exec.ExitError, got %v", err)
	}
	if exitErr.ExitCode() != 2 {
		t.Fatalf("exit code = %d, want 2", exitErr.ExitCode())
	}
}
