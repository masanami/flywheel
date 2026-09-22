package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRun_HelpVariantsPrintUsageAndExitZero(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}} {
		var stdout, stderr bytes.Buffer
		code := run(args, strings.NewReader(""), &stdout, &stderr, defaultCommands())
		if code != 0 {
			t.Errorf("args=%v exit=%d, want 0", args, code)
		}
		if stdout.Len() == 0 {
			t.Errorf("args=%v: usage text was not printed to stdout", args)
		}
		if stderr.Len() != 0 {
			t.Errorf("args=%v: stderr not empty: %q", args, stderr.String())
		}
	}
}

// requireErrorCode は --json での実行結果が、期待する終了コード・エラーコードで
// 終わることを検証する。exit=2 は usage_error と internal_error（スタブ）の両方が
// 共有するため、終了コードだけでは両者を区別できない（code-reviewer の指摘）。
// エラーコードまで assert することで、usage_error の検査がスタブの internal_error
// を誤って緑にしないようにする。
func requireErrorCode(t *testing.T, args []string, wantExit int, wantCode ErrorCode) {
	t.Helper()
	full := append(append([]string{}, args...), "--json")
	var stdout, stderr bytes.Buffer
	code := run(full, strings.NewReader(""), &stdout, &stderr, defaultCommands())
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

func TestRun_NoArgsIsUsageError(t *testing.T) {
	// requireErrorCode は常に --json を末尾へ追加するため nil には使えない
	// （args=["--json"] になった瞬間、rawArgs は空でなくなり "未知のコマンド" 分岐を
	// 通ってしまい、この分岐の担保にならない。確認モードのラウンド2で指摘）。
	// この分岐だけは --json 無しで直接叩き、テキスト出力とコードの両方を見る。
	var stdout, stderr bytes.Buffer
	code := run(nil, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if code != 2 {
		t.Fatalf("exit=%d, want 2 (stderr=%s)", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout should stay empty on failure, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "(usage_error)") {
		t.Fatalf("stderr = %q, want it to carry the usage_error code", stderr.String())
	}
}

func TestRun_UnknownCommandIsUsageError(t *testing.T) {
	requireErrorCode(t, []string{"nope"}, 2, CodeUsageError)
}

func TestRun_UnknownFlagIsUsageError(t *testing.T) {
	requireErrorCode(t, []string{"create", "--title", "x", "--bogus", "y"}, 2, CodeUsageError)
}

func TestRun_MissingRequiredFlagIsUsageError(t *testing.T) {
	requireErrorCode(t, []string{"create"}, 2, CodeUsageError)
}

func TestRun_MissingPositionalArgIsUsageError(t *testing.T) {
	requireErrorCode(t, []string{"show"}, 2, CodeUsageError)
}

func TestRun_ExtraPositionalArgIsUsageError(t *testing.T) {
	requireErrorCode(t, []string{"show", "C-1", "C-2"}, 2, CodeUsageError)
}

func TestRun_PlanRequiresExactlyOneOfFileOrStdin(t *testing.T) {
	requireErrorCode(t, []string{"plan", "C-1"}, 2, CodeUsageError)
	requireErrorCode(t, []string{"plan", "C-1", "--file", "x.txt", "--stdin"}, 2, CodeUsageError)
	// exactly one of --file/--stdin: parsing succeeds, then (once a workspace is
	// resolvable) tries to read the (nonexistent) file, which is a usage_error
	// (誤ったパス。#10 runPlan の規則)。plan は RequiresStore のため、ストアの
	// あるワークスペースを渡さないと store_not_found が先に出てしまう。
	ws := initializedWorkspace(t)
	requireErrorCode(t, []string{"plan", "C-1", "--file", "x.txt", "--workspace", ws}, 2, CodeUsageError)
}

func TestRun_FlagParsesRegardlessOfPositionBeforeOrAfterPositionalArg(t *testing.T) {
	var gotBefore, gotAfter Args
	cmdBefore := Command{
		Path: []string{"approve"}, MinPositional: 1, MaxPositional: 1,
		Flags: []flagDef{{Name: "hold-release", HasValue: false}},
		Run:   func(a Args) (any, error) { gotBefore = a; return nil, NewError(CodeInternalError, "stub") },
	}
	var stdout, stderr bytes.Buffer
	run([]string{"approve", "--hold-release", "C-1"}, strings.NewReader(""), &stdout, &stderr, []Command{cmdBefore})
	if len(gotBefore.Positional) != 1 || gotBefore.Positional[0] != "C-1" || !gotBefore.Bools["hold-release"] {
		t.Fatalf("flag-before-positional not parsed correctly: %+v", gotBefore)
	}

	cmdAfter := Command{
		Path: []string{"approve"}, MinPositional: 1, MaxPositional: 1,
		Flags: []flagDef{{Name: "hold-release", HasValue: false}},
		Run:   func(a Args) (any, error) { gotAfter = a; return nil, NewError(CodeInternalError, "stub") },
	}
	stdout.Reset()
	stderr.Reset()
	run([]string{"approve", "C-1", "--hold-release"}, strings.NewReader(""), &stdout, &stderr, []Command{cmdAfter})
	if len(gotAfter.Positional) != 1 || gotAfter.Positional[0] != "C-1" || !gotAfter.Bools["hold-release"] {
		t.Fatalf("flag-after-positional not parsed correctly: %+v", gotAfter)
	}
}

func TestRun_JSONSuccessWritesSingleDocumentToStdoutWithEmptyStderr(t *testing.T) {
	testCmd := Command{
		Path: []string{"widget"},
		Run: func(Args) (any, error) {
			return map[string]any{"widget": map[string]any{"id": "W-1"}}, nil
		},
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"widget", "--json"}, strings.NewReader(""), &stdout, &stderr, []Command{testCmd})
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stderr=%s)", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr not empty: %q", stderr.String())
	}
	dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (%q)", err, stdout.String())
	}
	if dec.More() {
		t.Fatalf("stdout has more than one JSON document: %q", stdout.String())
	}
	if !strings.HasSuffix(stdout.String(), "\n") {
		t.Fatalf("stdout does not end with a trailing newline: %q", stdout.String())
	}
}

func TestRun_JSONFailureWritesErrorEnvelopeToStderrWithEmptyStdout(t *testing.T) {
	testCmd := Command{
		Path: []string{"widget"},
		Run: func(Args) (any, error) {
			return nil, NewError(CodeNotFound, "widget not found")
		},
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"widget", "--json"}, strings.NewReader(""), &stdout, &stderr, []Command{testCmd})
	if code != 1 {
		t.Fatalf("exit=%d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout not empty: %q", stdout.String())
	}
	var doc struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &doc); err != nil {
		t.Fatalf("stderr is not valid JSON: %v (%q)", err, stderr.String())
	}
	if doc.Error.Code != "not_found" || doc.Error.Message != "widget not found" {
		t.Fatalf("unexpected error envelope: %+v", doc)
	}
}

func TestRun_JSONFlagHonoredEvenWhenFlagParsingFails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"create", "--bogus", "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	var doc map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &doc); err != nil {
		t.Fatalf("stderr is not valid JSON even though --json was present: %v (%q)", err, stderr.String())
	}
}

func TestRun_NilTypedErrorFromCommandDoesNotPanic(t *testing.T) {
	// Command.Run が (nil, error) を返すとき、その error の動的型が *Error で値が
	// nil（型付き nil）だと errors.As は true を返しつつ nil ポインタを詰める。
	// これをそのまま failErr へ渡すとフィールドアクセスで panic するため、
	// nil 判定して internal_error にフォールバックすることを検証する。
	testCmd := Command{
		Path: []string{"widget"},
		Run: func(Args) (any, error) {
			var e *Error
			return nil, e
		},
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"widget", "--json"}, strings.NewReader(""), &stdout, &stderr, []Command{testCmd})
	if code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	if !strings.Contains(stderr.String(), `"code":"internal_error"`) {
		t.Fatalf("stderr=%q, want internal_error", stderr.String())
	}
}

func TestRun_NonObjectSuccessPayloadBecomesInternalError(t *testing.T) {
	testCmd := Command{
		Path: []string{"widget"},
		Run:  func(Args) (any, error) { return []string{"not", "an", "object"}, nil },
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"widget", "--json"}, strings.NewReader(""), &stdout, &stderr, []Command{testCmd})
	if code != 2 {
		t.Fatalf("exit=%d, want 2 (envelope enforcement failure -> internal_error)", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout should stay empty, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), `"code":"internal_error"`) {
		t.Fatalf("stderr = %q, want internal_error", stderr.String())
	}
}

// TestRun_AllStubCommandsReturnInternalErrorUnimplemented は init・create・
// show・list・edit・log・classify・plan・submit・verify・hold・answer・
// approve・reject 以外の未実装コマンド（RequiresStore: true）を対象にする。
// init は本チケットより前（#7）で実装済みのため専用のテスト（init_test.go）で
// 検証する。create・show・list・edit・log は #9 で、classify・plan・submit・
// verify・hold は #10 で、answer・approve・reject は本チケット（#12）で
// 実装済みのため対象から外れ、それぞれ internal/cli/challenge_test.go・
// internal/cli/transition_test.go・internal/cli/approval_test.go・
// internal/cli/approval_process_test.go で検証する。
func TestRun_AllStubCommandsReturnInternalErrorUnimplemented(t *testing.T) {
	ws := initializedWorkspace(t)
	cases := [][]string{
		{"op", "add", "C-1", "--kind", "release", "--summary", "s"},
		{"status"},
	}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		full := append(append([]string{}, args...), "--workspace", ws, "--json")
		code := run(full, strings.NewReader(""), &stdout, &stderr, defaultCommands())
		if code != 2 {
			t.Errorf("args=%v exit=%d, want 2 (internal_error stub)", args, code)
			continue
		}
		if !strings.Contains(stderr.String(), `"code":"internal_error"`) {
			t.Errorf("args=%v stderr=%q, want internal_error", args, stderr.String())
		}
	}
}

func TestDefaultCommands_MatchIFAPITable(t *testing.T) {
	want := [][]string{
		{"init"}, {"create"}, {"show"}, {"list"}, {"edit"}, {"classify"}, {"plan"},
		{"submit"}, {"verify"}, {"hold"}, {"answer"}, {"approve"}, {"reject"},
		{"op", "add"}, {"status"}, {"log"},
	}
	cmds := defaultCommands()
	for _, w := range want {
		found := false
		for _, c := range cmds {
			if equalStringSlices(c.Path, w) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("command %v is not registered", w)
		}
	}
	if len(cmds) != len(want) {
		t.Errorf("defaultCommands() has %d entries, want %d (IF/API 表に無いコマンドを足していないか確認)", len(cmds), len(want))
	}

	// 上のループ＋件数比較は want と cmds の重複が無い前提でしか集合一致を
	// 保証しない。集合（map キー）として明示的に一致を取り、取りこぼしを防ぐ。
	wantSet := map[string]bool{}
	for _, w := range want {
		wantSet[strings.Join(w, " ")] = true
	}
	gotSet := map[string]bool{}
	for _, c := range cmds {
		gotSet[strings.Join(c.Path, " ")] = true
	}
	for path := range wantSet {
		if !gotSet[path] {
			t.Errorf("IF/API table command %q is not registered in defaultCommands()", path)
		}
	}
	for path := range gotSet {
		if !wantSet[path] {
			t.Errorf("defaultCommands() has %q which is not in the IF/API table", path)
		}
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// フラグの値としての "--json" は JSON モードを有効にしない（解析成功後は解析結果が正）。
func TestRun_JSONLiteralAsFlagValueDoesNotEnableJSONMode(t *testing.T) {
	testCmd := Command{
		Path:  []string{"widget"},
		Flags: []flagDef{{Name: "note", HasValue: true}},
		Run:   func(Args) (any, error) { return nil, NewError(CodeNotFound, "no such widget") },
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"widget", "--note", "--json"}, strings.NewReader(""), &stdout, &stderr, []Command{testCmd})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if json.Valid(stderr.Bytes()) {
		t.Fatalf("stderr must be human text when --json is only a flag value, got %q", stderr.String())
	}
}

// 先頭のハイフンが 3 つ以上のトークンは既知のフラグと同一視しない。
func TestRun_TripleHyphenFlagIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"status", "---json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), string(CodeUsageError)) {
		t.Fatalf("stderr = %q, want usage_error", stderr.String())
	}
}
