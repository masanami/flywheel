package cli

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core"
	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは Issue #15 の完了条件「§IF / API の全コマンドについて …
// --json の成功時は標準出力に JSON 1 文書・失敗時は標準エラーに error.code と
// error.message／ID を引数に取る全コマンドは、存在しない課題の ID と不可逆操作の
// ID のそれぞれで not_found／未知のフラグ・必須引数の欠落は usage_error で
// ストアを変えない」を、登録表（defaultCommands()）から導いた全コマンドに
// ついて検証する（AC-72・AC-73・AC-77。AC-18 は idcommands_test.go が同じ
// allCommandSuccessCases を使って検証する）。
//
// 各コマンドの成功経路の準備だけは手書き（allCommandSuccessCases）だが、
// TestAllCommands_SuccessCasesCoverRegistrationTable がその一覧と登録表の
// 過不足を検査するため、コマンドを足したのにここへ載せ忘れると落ちる。
// 必須引数の欠落の組は登録表の宣言（MinPositional・Required・OneOfGroups）から
// 機械的に導く。

// commandSuccessCase は 1 コマンドの成功経路。setup はワークスペース ws を
// 必要な状態まで進め、--workspace/--json を除いた引数列を返す。ID を引数に
// 取るコマンドは、引数列の len(Path) 番目に対象 ID を置く（not_found の検査が
// そこを存在しない ID へ差し替える）。needsTTY は本人確認つき（疑似端末の子
// プロセスで実行し、ID を確認入力として書き込む）。uninitialized は setup の前に
// ワークスペースを初期化しない（init 自身）。
type commandSuccessCase struct {
	setup         func(t *testing.T, ws string) []string
	needsTTY      bool
	uninitialized bool
}

func createForCase(t *testing.T, ws string) string {
	t.Helper()
	created := runJSON(t, ws, "create", "--title", "t", "--done-criteria", "d")
	return created["challenge"].(map[string]any)["id"].(string)
}

func createWithStatusForCase(t *testing.T, ws, status string) string {
	t.Helper()
	id := createForCase(t, ws)
	coretest.SetChallengeStatus(t, ws, challengeIDToInternalID(t, id), status)
	return id
}

func createPlannedForCase(t *testing.T, ws string) string {
	t.Helper()
	id := createForCase(t, ws)
	runJSON(t, ws, "classify", id, "--priority", "P0")
	runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, "do it"))
	return id
}

// allCommandSuccessCases のキーは Command.Path をスペースで結合したもの。
var allCommandSuccessCases = map[string]commandSuccessCase{
	"init": {uninitialized: true, setup: func(_ *testing.T, _ string) []string {
		return []string{"init"}
	}},
	"create": {setup: func(_ *testing.T, _ string) []string {
		return []string{"create", "--title", "t"}
	}},
	"show": {setup: func(t *testing.T, ws string) []string {
		return []string{"show", createForCase(t, ws)}
	}},
	"list": {setup: func(t *testing.T, ws string) []string {
		createForCase(t, ws)
		return []string{"list"}
	}},
	"edit": {setup: func(t *testing.T, ws string) []string {
		return []string{"edit", createForCase(t, ws), "--title", "x"}
	}},
	"classify": {setup: func(t *testing.T, ws string) []string {
		return []string{"classify", createForCase(t, ws), "--priority", "P0"}
	}},
	"plan": {setup: func(t *testing.T, ws string) []string {
		id := createForCase(t, ws)
		runJSON(t, ws, "classify", id, "--priority", "P0")
		return []string{"plan", id, "--file", writePlanFileForTest(t, "do it")}
	}},
	"submit": {setup: func(t *testing.T, ws string) []string {
		return []string{"submit", createWithStatusForCase(t, ws, "in_progress")}
	}},
	"verify": {setup: func(t *testing.T, ws string) []string {
		return []string{"verify", createWithStatusForCase(t, ws, "verifying"), "--result", "met"}
	}},
	"hold": {setup: func(t *testing.T, ws string) []string {
		return []string{"hold", createWithStatusForCase(t, ws, "verifying"), "--question", "q"}
	}},
	"answer": {needsTTY: true, setup: func(t *testing.T, ws string) []string {
		id := createWithStatusForCase(t, ws, "verifying")
		runJSON(t, ws, "verify", id, "--result", "uncertain", "--question", "q")
		return []string{"answer", id, "--answer", "a"}
	}},
	"approve": {needsTTY: true, setup: func(t *testing.T, ws string) []string {
		return []string{"approve", createPlannedForCase(t, ws)}
	}},
	"reject": {needsTTY: true, setup: func(t *testing.T, ws string) []string {
		return []string{"reject", createPlannedForCase(t, ws), "--reason", "r"}
	}},
	"op add": {setup: func(t *testing.T, ws string) []string {
		return []string{"op", "add", createForCase(t, ws), "--kind", "release", "--summary", "s"}
	}},
	"status": {setup: func(t *testing.T, ws string) []string {
		createForCase(t, ws)
		// needs_human.discrepancies の要素の形も文書と照合されるよう、食い違いの
		// ある課題を 1 件置く（空配列だと要素の照合が 0 件で終わる）。
		bindSourceForDiscrepancyCase(t, ws, createForCase(t, ws), "o/r#1", "closed", "out_of_policy")
		return []string{"status"}
	}},
	"log": {setup: func(t *testing.T, ws string) []string {
		return []string{"log", createForCase(t, ws)}
	}},
	"ingest": {setup: func(t *testing.T, ws string) []string {
		writeSourcesDeclaration(t, ws, validSourcesDeclaration)
		// self-review 指摘: 取り込みの本体（#59）が結線された後にこの横断テストが
		// 偽の gh を置き忘れていると、本物の gh・GitHub を呼んでしまう
		// （m2-github-issue-ingest.md「go test ./... は本物の gh と GitHub を
		// 呼ばない」）。今は ingest が gh を呼ばないため無害だが、先回りして
		// 置いておく（戻り値の calls は、この横断テストの枠組みでは使わない）。
		withFakeGHOnPATH(t)
		return []string{"ingest"}
	}},
}

// registeredCommands は登録表の全コマンドを、Path をスペースで結合した名前で返す。
func registeredCommands() map[string]Command {
	out := map[string]Command{}
	for _, cmd := range defaultCommands() {
		out[strings.Join(cmd.Path, " ")] = cmd
	}
	return out
}

func sortedCommandNames(m map[string]Command) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func TestAllCommands_SuccessCasesCoverRegistrationTable(t *testing.T) {
	registered := registeredCommands()
	for name := range registered {
		if _, ok := allCommandSuccessCases[name]; !ok {
			t.Errorf("registered command %q has no entry in allCommandSuccessCases", name)
		}
	}
	for name := range allCommandSuccessCases {
		if _, ok := registered[name]; !ok {
			t.Errorf("allCommandSuccessCases has %q, which is not a registered command (stale entry?)", name)
		}
	}
}

// requireSingleJSONObject は s が末尾改行つきの JSON オブジェクト 1 文書である
// ことを検査する。
func requireSingleJSONObject(t *testing.T, name, s string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("%s: stdout is not a JSON object: %v (%q)", name, err, s)
	}
	if dec.More() {
		t.Fatalf("%s: stdout has more than one JSON document: %q", name, s)
	}
	if !strings.HasSuffix(s, "\n") {
		t.Fatalf("%s: stdout does not end with a newline: %q", name, s)
	}
	return doc
}

// requireJSONErrorEnvelope は --json つきで args を実行し、終了コード・空の
// 標準出力・標準エラーの {"error":{"code","message"}}（1 文書・message は空でない）
// を検査する。
func requireJSONErrorEnvelope(t *testing.T, args []string, wantExit int, wantCode ErrorCode) {
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
	dec := json.NewDecoder(bytes.NewReader(stderr.Bytes()))
	var doc struct {
		Error *struct {
			Code    *string `json:"code"`
			Message *string `json:"message"`
		} `json:"error"`
	}
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("args=%v stderr is not JSON: %v (%q)", args, err, stderr.String())
	}
	if dec.More() {
		t.Fatalf("args=%v stderr has more than one JSON document: %q", args, stderr.String())
	}
	if doc.Error == nil || doc.Error.Code == nil || doc.Error.Message == nil {
		t.Fatalf("args=%v stderr lacks error.code or error.message: %q", args, stderr.String())
	}
	if *doc.Error.Code != string(wantCode) {
		t.Fatalf("args=%v error.code = %q, want %q", args, *doc.Error.Code, wantCode)
	}
	if *doc.Error.Message == "" {
		t.Fatalf("args=%v error.message is empty", args)
	}
}

// AC-72: 全コマンドの成功時、--json は標準出力に JSON 1 文書を書き、標準エラーは空。
// あわせて、出力のキーが docs/features/m1-core.md の成功時の JSON の形と一致する
// ことを検査する（jsondoc_test.go）。
func TestAllCommands_JSONSuccessWritesSingleDocumentToStdout(t *testing.T) {
	doc := loadDocumentedJSON(t)
	for _, name := range sortedCommandNames(registeredCommands()) {
		tc, ok := allCommandSuccessCases[name]
		if !ok {
			continue // TestAllCommands_SuccessCasesCoverRegistrationTable が落とす
		}
		t.Run(name, func(t *testing.T) {
			ws := t.TempDir()
			if !tc.uninitialized {
				if _, err := core.Init(ws); err != nil {
					t.Fatalf("core.Init: %v", err)
				}
			}
			args := append(tc.setup(t, ws), "--workspace", ws, "--json")
			var code int
			var stdout, stderr string
			if tc.needsTTY {
				id := args[len(strings.Fields(name))]
				code, _, stdout, stderr = runConfirmedChild(t, args, id)
			} else {
				var out, errOut bytes.Buffer
				code = run(args, strings.NewReader(""), &out, &errOut, defaultCommands())
				stdout, stderr = out.String(), errOut.String()
			}
			if code != 0 {
				t.Fatalf("exit=%d, want 0 (stdout=%q stderr=%q)", code, stdout, stderr)
			}
			if stderr != "" {
				t.Errorf("stderr should be empty on success, got %q", stderr)
			}
			assertDocumentedJSON(t, doc, name, requireSingleJSONObject(t, name, stdout))
		})
	}
}

// AC-73: 全コマンドの失敗時（usage_error。コマンドの Run より前に決着する
// 経路）も、--json は標準エラーに error.code と error.message を書き、標準出力は
// 空。Run の中で決着する失敗は、ID を取るコマンドについて上の not_found の
// テストが、版が新しいストアについて
// TestRun_AllRequiresStoreCommandsRejectTooNewStoreForEveryRegisteredCommand が
// 全コマンドを検査する。
func TestAllCommands_JSONFailureWritesErrorEnvelopeToStderr(t *testing.T) {
	ws := initializedWorkspace(t)
	for _, name := range sortedCommandNames(registeredCommands()) {
		cmd := registeredCommands()[name]
		requireJSONErrorEnvelope(t, append(append([]string{}, cmd.Path...), "--no-such-flag", "--workspace", ws), 2, CodeUsageError)
	}
}

// missingRequiredVariants は登録表の宣言から「必須引数を 1 つ欠いた」引数列を
// 導く。欠落の種類は ①位置引数（MinPositional ≥ 1）②Required のフラグ
// ③OneOfGroups の組（どれも与えない）。欠いた要素以外は満たしておく。
func missingRequiredVariants(cmd Command) map[string][]string {
	positional := make([]string, cmd.MinPositional)
	for i := range positional {
		positional[i] = "C-1"
	}
	requiredFlags := func(except string) []string {
		var out []string
		for _, f := range cmd.Flags {
			if !f.Required || f.Name == except {
				continue
			}
			out = append(out, "--"+f.Name)
			if f.HasValue {
				out = append(out, "v")
			}
		}
		return out
	}
	oneOf := func(skip int) []string {
		var out []string
		for i, g := range cmd.OneOfGroups {
			if i == skip {
				continue
			}
			for _, f := range cmd.Flags {
				if f.Name == g[0] {
					out = append(out, "--"+f.Name)
					if f.HasValue {
						out = append(out, "v")
					}
				}
			}
		}
		return out
	}
	build := func(parts ...[]string) []string {
		out := append([]string{}, cmd.Path...)
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}

	variants := map[string][]string{}
	if cmd.MinPositional >= 1 {
		variants["positional"] = build(requiredFlags(""), oneOf(-1))
	}
	for _, f := range cmd.Flags {
		if f.Required {
			variants["--"+f.Name] = build(positional, requiredFlags(f.Name), oneOf(-1))
		}
	}
	for i, g := range cmd.OneOfGroups {
		variants["one of "+strings.Join(g, "/")] = build(positional, requiredFlags(""), oneOf(i))
	}
	return variants
}

// runLevelUsageErrorCases は、引数の解析を通った後（ストアを開いた後）に
// コマンドの Run が usage_error で決着する組。登録表の宣言からは導けないため
// 手書きで持ち、TestRunLevelUsageErrorCases_CoverEveryRunLevelUsageErrorSite が
// 本番コードの NewError(CodeUsageError, …) の箇所数と件数を照合する。
// C-1 は課題、OP-1 は C-1 の不可逆操作として実在させておく。
var runLevelUsageErrorCases = [][]string{
	{"edit", "C-1"}, // 変更する項目が 1 つも無い
	{"plan", "C-1", "--file", "/nonexistent/flywheel-plan.txt"}, // --file が読めない
	{"verify", "C-1", "--result", "met", "--question", "q"},     // met と --question の同時指定
	{"approve", "OP-1", "--hold-release"},                       // 不可逆操作の ID への --hold-release
	{"approve", "C-2", "--hold-release"},                        // 計画承認待ちの課題への --hold-release
}

// TestRunLevelUsageErrorCases_CoverEveryRunLevelUsageErrorSite は、cli.go（引数の
// 解析）以外の本番コードにある NewError(CodeUsageError, …) の数と
// runLevelUsageErrorCases の件数が一致することを検査する（Run の中の usage_error を
// 足したのに、ストア不変の列挙へ載せ忘れる取りこぼしを防ぐ）。
func TestRunLevelUsageErrorCases_CoverEveryRunLevelUsageErrorSite(t *testing.T) {
	fset, files := parseProductionSources(t)
	sites := 0
	for _, f := range files {
		if filepath.Base(fset.Position(f.Pos()).Filename) == "cli.go" {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || calleeName(call.Fun) != "NewError" || len(call.Args) == 0 {
				return true
			}
			if id, ok := call.Args[0].(*ast.Ident); ok && id.Name == "CodeUsageError" {
				sites++
			}
			return true
		})
	}
	if sites != len(runLevelUsageErrorCases) {
		t.Fatalf("found %d NewError(CodeUsageError, …) sites outside cli.go, but runLevelUsageErrorCases has %d cases", sites, len(runLevelUsageErrorCases))
	}
}

// AC-77: 未知のフラグ・必須引数の欠落は、終了コード 2・usage_error で終わり、
// ストアを変更しない（全コマンド。必須引数の組は登録表の宣言から導く）。
// Run の中で決着する usage_error（runLevelUsageErrorCases）も同じく検査する。
func TestAllCommands_UnknownFlagAndMissingRequiredArgumentAreUsageErrorAndLeaveStoreUnchanged(t *testing.T) {
	ws := initializedWorkspace(t)
	c1 := createForCase(t, ws) // C-1 を実在させ、欠落以外の引数が妥当な状態で検査する
	runJSON(t, ws, "op", "add", c1, "--kind", "release", "--summary", "s")
	createPlannedForCase(t, ws) // C-2（計画承認待ち）
	dbPath := filepath.Join(ws, ".flywheel", "flywheel.db")
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read store before: %v", err)
	}

	missingTested := 0
	for _, name := range sortedCommandNames(registeredCommands()) {
		cmd := registeredCommands()[name]
		requireJSONErrorEnvelope(t, append(append([]string{}, cmd.Path...), "--no-such-flag", "--workspace", ws), 2, CodeUsageError)
		for what, args := range missingRequiredVariants(cmd) {
			t.Logf("%s: missing %s: %v", name, what, args)
			requireJSONErrorEnvelope(t, append(args, "--workspace", ws), 2, CodeUsageError)
			missingTested++
		}
	}
	if missingTested == 0 {
		t.Fatal("no missing-required-argument variants were derived from the registration table")
	}
	for _, args := range runLevelUsageErrorCases {
		requireJSONErrorEnvelope(t, append(append([]string{}, args...), "--workspace", ws), 2, CodeUsageError)
	}

	after, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read store after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("usage_error must not change the store file")
	}
}
