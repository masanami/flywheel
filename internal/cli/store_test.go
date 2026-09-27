package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// TestRun_CommandNotRequiringStoreNeverOpensAStore は、RequiresStore が false の
// コマンド（既存のテスト用アドホックコマンドを含む）が、ワークスペースの解決や
// ストアのオープンを一切行わないことを検証する（ゼロ値で安全側＝既存の
// cli_test.go の "widget" 系テストがこの回帰に影響されないことの担保でもある）。
func TestRun_CommandNotRequiringStoreNeverOpensAStore(t *testing.T) {
	dir := t.TempDir()
	var gotArgs Args
	testCmd := Command{
		Path: []string{"widget"},
		Run: func(a Args) (any, error) {
			gotArgs = a
			return map[string]any{"widget": map[string]any{}}, nil
		},
	}
	var stdout, stderr bytes.Buffer
	// ワークスペースが存在しないディレクトリを渡しても、RequiresStore=false なら
	// 失敗しない（ストアを開かないため store_not_found にならない）。
	code := run([]string{"widget", "--workspace", dir, "--json"}, strings.NewReader(""), &stdout, &stderr, []Command{testCmd})
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stderr=%s)", code, stderr.String())
	}
	if gotArgs.Store != nil {
		t.Fatal("Args.Store was non-nil for a RequiresStore=false command")
	}
	if _, err := os.Stat(filepath.Join(dir, ".flywheel")); err == nil {
		t.Fatal("a RequiresStore=false command unexpectedly created a .flywheel directory")
	}
}

// TestRun_CommandRequiringStoreFailsWithStoreNotFoundWhenWorkspaceHasNoStore は
// AC「ストアが見つからない場所で init 以外のコマンドを実行すると、終了コード 2・
// store_not_found で終わり、ストアが作られない」の検証。
func TestRun_CommandRequiringStoreFailsWithStoreNotFoundWhenWorkspaceHasNoStore(t *testing.T) {
	dir := t.TempDir()
	testCmd := Command{
		Path:          []string{"widget"},
		RequiresStore: true,
		Run:           func(Args) (any, error) { return map[string]any{"widget": map[string]any{}}, nil },
	}
	requireErrorCodeWithCommands(t, []string{"widget", "--workspace", dir}, 2, CodeStoreNotFound, []Command{testCmd})

	if _, err := os.Stat(filepath.Join(dir, ".flywheel")); err == nil {
		t.Fatal("store_not_found path unexpectedly created a .flywheel directory")
	}
}

// TestRun_CommandRequiringStoreOpensItAndPassesToRun は、RequiresStore=true の
// コマンドの Run に、開いたワークスペースのストアが Args.Store として渡ることを
// 検証する。
func TestRun_CommandRequiringStoreOpensItAndPassesToRun(t *testing.T) {
	dir := t.TempDir()
	if _, err := core.Init(dir); err != nil {
		t.Fatalf("core.Init() setup error = %v", err)
	}

	var gotStore *core.Store
	testCmd := Command{
		Path:          []string{"widget"},
		RequiresStore: true,
		Run: func(a Args) (any, error) {
			gotStore = a.Store
			return map[string]any{"widget": map[string]any{}}, nil
		},
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"widget", "--workspace", dir, "--json"}, strings.NewReader(""), &stdout, &stderr, []Command{testCmd})
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stderr=%s)", code, stderr.String())
	}
	if gotStore == nil {
		t.Fatal("Args.Store was nil inside Run, want an opened *core.Store")
	}
}

// TestRun_AllRequiresStoreCommandsRejectTooNewStoreForEveryRegisteredCommand は
// AC「スキーマ版がバイナリの知る最新版より大きいストアに対しては、init・list を
// 含むすべてのコマンドが終了コード 2・store_too_new で終わり、ストアのファイルが
// 変わらない（§IF / API の全コマンドを列挙して検証する）」の枠組み。
// この時点で存在するコマンド（defaultCommands()）を列挙して回す。
func TestRun_AllRequiresStoreCommandsRejectTooNewStoreForEveryRegisteredCommand(t *testing.T) {
	dir := t.TempDir()
	if _, err := core.Init(dir); err != nil {
		t.Fatalf("core.Init() setup error = %v", err)
	}
	coretest.SetStoreVersion(t, dir, 999999)

	before, err := os.ReadFile(filepath.Join(dir, ".flywheel", "flywheel.db"))
	if err != nil {
		t.Fatalf("read fixture before: %v", err)
	}

	cases := [][]string{
		{"init"},
		{"create", "--title", "t"},
		{"show", "C-1"},
		{"list"},
		{"edit", "C-1"},
		{"classify", "C-1", "--priority", "P0"},
		{"plan", "C-1", "--stdin"},
		{"submit", "C-1"},
		{"verify", "C-1", "--result", "met"},
		{"hold", "C-1", "--question", "q"},
		{"answer", "C-1", "--answer", "a"},
		{"approve", "C-1"},
		{"reject", "C-1", "--reason", "r"},
		{"op", "add", "C-1", "--kind", "release", "--summary", "s"},
		{"status"},
		{"log"},
		{"ingest"},
		{"mark-read", "C-1"},
	}

	assertCasesCoverExactlyDefaultCommandsAndRequireStoreExceptInit(t, cases)

	for _, args := range cases {
		full := append(append([]string{}, args...), "--workspace", dir, "--json")
		var stdout, stderr bytes.Buffer
		code := run(full, strings.NewReader(""), &stdout, &stderr, defaultCommands())
		if code != 2 {
			t.Errorf("args=%v exit=%d, want 2 (store_too_new) (stderr=%s)", args, code, stderr.String())
			continue
		}
		if !strings.Contains(stderr.String(), `"code":"store_too_new"`) {
			t.Errorf("args=%v stderr=%q, want store_too_new", args, stderr.String())
		}
	}

	after, err := os.ReadFile(filepath.Join(dir, ".flywheel", "flywheel.db"))
	if err != nil {
		t.Fatalf("read fixture after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("store_too_new must not change the store file across any command")
	}
}

// assertCasesCoverExactlyDefaultCommandsAndRequireStoreExceptInit は item 8(c) の
// 検証: cases の先頭トークン（コマンド Path。matchCommand の照合結果）の集合が
// defaultCommands() の Path 集合と過不足なく一致すること、かつ init 以外の
// 全登録コマンドが RequiresStore == true であることを assert する（新しい
// コマンドを defaultCommands() に足したのに、この too-new の列挙表を更新し
// 忘れる、という取りこぼしを防ぐ）。
func assertCasesCoverExactlyDefaultCommandsAndRequireStoreExceptInit(t *testing.T, cases [][]string) {
	t.Helper()
	cmds := defaultCommands()

	gotPaths := map[string]bool{}
	for _, args := range cases {
		cmd, _, ok := matchCommand(args, cmds)
		if !ok {
			t.Fatalf("case %v does not match any registered command", args)
		}
		gotPaths[strings.Join(cmd.Path, " ")] = true
	}

	wantPaths := map[string]bool{}
	for _, c := range cmds {
		key := strings.Join(c.Path, " ")
		wantPaths[key] = true
		if key != "init" && !c.RequiresStore {
			t.Errorf("command %v is registered but RequiresStore=false (only init may skip the store)", c.Path)
		}
	}

	for path := range wantPaths {
		if !gotPaths[path] {
			t.Errorf("registered command %q has no case in the too-new coverage table", path)
		}
	}
	for path := range gotPaths {
		if !wantPaths[path] {
			t.Errorf("too-new coverage table has case %q which is not a registered command", path)
		}
	}
}

// requireErrorCodeWithCommands は requireErrorCode（cli_test.go）と同じ検証を、
// defaultCommands() ではなく任意のコマンド表に対して行う版。
func requireErrorCodeWithCommands(t *testing.T, args []string, wantExit int, wantCode ErrorCode, commands []Command) {
	t.Helper()
	full := append(append([]string{}, args...), "--json")
	var stdout, stderr bytes.Buffer
	code := run(full, strings.NewReader(""), &stdout, &stderr, commands)
	if code != wantExit {
		t.Fatalf("args=%v exit=%d, want %d (stderr=%s)", args, code, wantExit, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("args=%v stdout should stay empty on failure, got %q", args, stdout.String())
	}
	var doc struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &doc); err != nil {
		t.Fatalf("args=%v stderr is not valid JSON: %v (%q)", args, err, stderr.String())
	}
	if doc.Error.Code != string(wantCode) {
		t.Fatalf("args=%v error code = %q, want %q", args, doc.Error.Code, wantCode)
	}
}
