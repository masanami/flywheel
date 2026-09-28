package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

// TestUsageText_ListsEveryRegisteredCommand は、登録表（defaultCommands()）の
// すべてのコマンドが `--help` の「コマンド:」の一覧に行として出ることを検査する
// （#72 で mark-read を登録したときに help への追記が漏れた。登録表を起点に
// するので、コマンドを足すたびに手書きの一覧を直す必要は無い）。
func TestUsageText_ListsEveryRegisteredCommand(t *testing.T) {
	lines := strings.Split(usageText, "\n")
	for _, c := range defaultCommands() {
		name := strings.Join(c.Path, " ")
		found := false
		for _, line := range lines {
			if line == "  "+name || strings.HasPrefix(line, "  "+name+" ") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("usageText does not list registered command %q", name)
		}
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

// parseIFAPISignatures は doc の「### IF / API」節の表から、コマンドの
// Path（空白区切り）ごとの書式を導く。各行の `flywheel …` から、最初の
// 引数・フラグ（<…>・[…]・(…)・--…）の手前までのトークンを Path とし、残りを
// 書式とする。1 行に `/` で並ぶ複数のコマンド（approve <OP-ID> / reject <OP-ID>）は
// それぞれ数え、同じ Path の行が複数あれば書式をすべて返す。
//
// 節の区切りは見出しレベル3以下（"#"・"##"・"###"）でだけ判定する。
// docs/features/m3-invoker-delegation.md の「### IF / API」節は、CLI の表
// （#81 が足す `runs` を含む）を「#### CLI」というレベル4の小見出しの下に
// 持つため、レベル4以上の見出しで inSection を落とすと m3 の CLI 表を
// 読み飛ばしてしまう（m1-core.md の「### IF / API」節にはレベル4の小見出しが
// 無いため、この変更は m1 の読み取り結果を変えない）。
func parseIFAPISignatures(t *testing.T, doc string) map[string][]ifAPISignature {
	t.Helper()
	out := map[string][]ifAPISignature{}
	inSection := false
	for _, line := range strings.Split(doc, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			if headingLevel(trimmed) <= 3 {
				inSection = trimmed == "### IF / API"
			}
			continue
		}
		if !inSection || !strings.HasPrefix(trimmed, "|") {
			continue
		}
		// 1 列目だけを取り出す（書式の中の \| は列の区切りではない）。
		cell := strings.SplitN(strings.ReplaceAll(trimmed[1:], `\|`, "\x00"), "|", 2)[0]
		cell = strings.ReplaceAll(cell, "\x00", `\|`)
		for _, part := range strings.Split(cell, "`") {
			part = strings.TrimSpace(part)
			if !strings.HasPrefix(part, "flywheel ") {
				continue
			}
			fields := strings.Fields(strings.TrimPrefix(part, "flywheel "))
			var path []string
			for _, tok := range fields {
				if strings.ContainsAny(tok[:1], "<[(-") {
					break
				}
				path = append(path, tok)
			}
			if len(path) == 0 {
				t.Fatalf("no command name in IF/API row: %q", line)
			}
			name := strings.Join(path, " ")
			out[name] = append(out[name], parseIFAPISignature(fields[len(path):]))
		}
	}
	if len(out) == 0 {
		t.Fatal("IF/API table not found in document")
	}
	return out
}

// headingLevel は trimmed（前後の空白を除いたMarkdownの行）の先頭の "#" の
// 連続数を返す（見出しでなければ0）。
func headingLevel(trimmed string) int {
	n := 0
	for n < len(trimmed) && trimmed[n] == '#' {
		n++
	}
	return n
}

// TestRunsCommand_RequiredArgumentsMatchM3IFAPITable は runs コマンドについて
// TestDefaultCommands_RequiredArgumentsMatchIFAPITable と同じ検査を
// docs/features/m3-invoker-delegation.md に対して行う（m3 の CLI 表は
// M3 全体〈S1〜S2〉を1つの表にまとめているため、m1 と違い表全体の完全一致は
// 要求せず、実装済みの `runs` だけを見る。§IF / API「CLI」）。
func TestRunsCommand_RequiredArgumentsMatchM3IFAPITable(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "features", "m3-invoker-delegation.md"))
	if err != nil {
		t.Fatalf("read m3 spec: %v", err)
	}
	sigs, ok := parseIFAPISignatures(t, string(data))["runs"]
	if !ok || len(sigs) == 0 {
		t.Fatal("m3 spec's IF/API table has no `runs` row")
	}
	want := sigs[0]

	cmd, ok := registeredCommands()["runs"]
	if !ok {
		t.Fatal("runs is not registered in defaultCommands()")
	}
	if cmd.MinPositional != want.minPositional {
		t.Errorf("runs: MinPositional = %d, IF/API table says %d", cmd.MinPositional, want.minPositional)
	}
	got := map[string]bool{}
	for _, f := range cmd.Flags {
		if f.Required {
			got[f.Name] = true
		}
	}
	if !reflect.DeepEqual(got, want.required) {
		t.Errorf("runs: required flags = %v, IF/API table says %v", got, want.required)
	}
}

// ifAPISignature は §IF / API の表の 1 コマンドの書式から読み取った、必須の
// 引数の宣言（[…] の外にある <…> の位置引数の数・--フラグ・(… | …) の組）。
type ifAPISignature struct {
	minPositional int
	required      map[string]bool
	oneOf         []string // 組ごとにフラグ名を "/" で結合したもの
}

// parseIFAPISignature は `flywheel <path…> <書式>` の書式部分を読む。[…] の中は
// 任意なので読み飛ばし、(… | …) の中のフラグはちょうど 1 つを要求する組とする。
func parseIFAPISignature(tokens []string) ifAPISignature {
	sig := ifAPISignature{required: map[string]bool{}}
	optionalDepth := 0
	var group []string
	inGroup, prevFlag := false, false
	for _, tok := range tokens {
		opens, closes := strings.Count(tok, "["), strings.Count(tok, "]")
		if optionalDepth == 0 && opens == 0 {
			if strings.HasPrefix(tok, "(") {
				inGroup, group = true, nil
			}
			word := strings.Trim(tok, "()")
			switch {
			case strings.HasPrefix(word, "--"):
				if inGroup {
					group = append(group, strings.TrimPrefix(word, "--"))
				} else {
					sig.required[strings.TrimPrefix(word, "--")] = true
				}
				prevFlag = true
			case strings.HasPrefix(word, "<") && !prevFlag:
				sig.minPositional++
			default:
				prevFlag = false
			}
			if inGroup && strings.HasSuffix(tok, ")") {
				inGroup = false
				sig.oneOf = append(sig.oneOf, strings.Join(group, "/"))
			}
		}
		optionalDepth += opens - closes
	}
	return sig
}

// ifAPIRequiredFlagExceptions は §IF / API の書式では必須に見えるが、省略を
// usage_error ではなく validation_failed にすると仕様で決まっているフラグ
// （commands.go の該当箇所のコメントを参照）。
var ifAPIRequiredFlagExceptions = map[string]map[string]bool{
	"hold": {"question": true},
}

// TestDefaultCommands_RequiredArgumentsMatchIFAPITable は、登録表の必須の宣言
// （MinPositional・Required・OneOfGroups）が §IF / API の書式と一致することを
// 検査する。AC-77 の「必須引数の欠落」の列挙（allcommands_test.go の
// missingRequiredVariants）は登録表の宣言から導くため、宣言から Required が
// 落ちると列挙ごと消えて気付けない。その根元を仕様書に結び付ける。
func TestDefaultCommands_RequiredArgumentsMatchIFAPITable(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "features", "m1-core.md"))
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	specSigs := parseIFAPISignatures(t, string(data))
	for _, cmd := range defaultCommands() {
		name := strings.Join(cmd.Path, " ")
		sigs, ok := specSigs[name]
		if !ok {
			continue // TestDefaultCommands_MatchIFAPITable が落とす
		}
		for _, want := range sigs {
			for flag := range ifAPIRequiredFlagExceptions[name] {
				delete(want.required, flag)
			}
			if cmd.MinPositional != want.minPositional {
				t.Errorf("%s: MinPositional = %d, IF/API table says %d", name, cmd.MinPositional, want.minPositional)
			}
			got := map[string]bool{}
			for _, f := range cmd.Flags {
				if f.Required {
					got[f.Name] = true
				}
			}
			if !reflect.DeepEqual(got, want.required) {
				t.Errorf("%s: required flags = %v, IF/API table says %v", name, got, want.required)
			}
			var gotOneOf []string
			for _, g := range cmd.OneOfGroups {
				gotOneOf = append(gotOneOf, strings.Join(g, "/"))
			}
			if !reflect.DeepEqual(gotOneOf, want.oneOf) {
				t.Errorf("%s: OneOfGroups = %v, IF/API table says %v", name, gotOneOf, want.oneOf)
			}
		}
	}
}

// TestDefaultCommands_MatchIFAPITable は登録表（defaultCommands()）のコマンドの
// 集合が、仕様書 §IF / API の表から導いた集合と一致することを検査する。
// 横断の列挙テスト（allcommands_test.go など）はすべて登録表を起点にするため、
// 登録表と仕様書をつなぐこの検査も手書きの一覧を持たない。
func TestDefaultCommands_MatchIFAPITable(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "features", "m1-core.md"))
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	wantSet := map[string]bool{}
	for name := range parseIFAPISignatures(t, string(data)) {
		wantSet[name] = true
	}
	// #81: m3-invoker-delegation.md §IF / API「CLI」の表は M3 全体（S1〜S2）の
	// コマンドを1つの表にまとめて文書化しており、本チケットが実装するのは
	// そのうち `runs` だけである（`classify --auto`・`plan --auto`・`cycle`・
	// `run`・`verify --auto`・`slot clear`・`budget` は #83〜#86 の範囲）。
	// m1-core.md と違い「表と登録表が完全一致する」前提を m3 の表全体には
	// 適用できないため、ここでは実装済みの `runs` だけを個別に足す
	// （残りの行との整合は、それぞれを実装するチケットが同種の検査を足す）。
	wantSet["runs"] = true
	gotSet := map[string]bool{}
	for _, c := range defaultCommands() {
		key := strings.Join(c.Path, " ")
		if gotSet[key] {
			t.Errorf("defaultCommands() registers %q twice", key)
		}
		gotSet[key] = true
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
