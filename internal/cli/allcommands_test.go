package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// budgetCaseSpec は枠の額を明記した、承認済みの計画の構造化した出力（budget の対象の課題の
// 枠は、宣言を読まなくても決まる）。
const budgetCaseSpec = `{"verdict":"plan","size":"M","budget_impl_usd":50,"budget_review_usd":30}`

// createBudgetTargetForCase は着手中で承認済みの計画（版 1。実装枠 50・レビュー対応枠 30）を持つ
// 課題を作る。
func createBudgetTargetForCase(t *testing.T, ws string) string {
	t.Helper()
	id := createForCase(t, ws)
	coretest.InsertApprovedPlan(t, ws, challengeIDToInternalID(t, id), "PLAN-BODY", budgetCaseSpec)
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
	"withdraw": {needsTTY: true, setup: func(t *testing.T, ws string) []string {
		return []string{"withdraw", createForCase(t, ws), "--reason", "r"}
	}},
	"approve": {needsTTY: true, setup: func(t *testing.T, ws string) []string {
		return []string{"approve", createPlannedForCase(t, ws)}
	}},
	"reject": {needsTTY: true, setup: func(t *testing.T, ws string) []string {
		return []string{"reject", createPlannedForCase(t, ws), "--reason", "r"}
	}},
	"budget": {needsTTY: true, setup: func(t *testing.T, ws string) []string {
		return []string{"budget", createBudgetTargetForCase(t, ws), "--impl-usd", "80"}
	}},
	"op add": {setup: func(t *testing.T, ws string) []string {
		return []string{"op", "add", createForCase(t, ws), "--kind", "release", "--summary", "s"}
	}},
	"op exec": {setup: func(t *testing.T, ws string) []string {
		// op exec --json の出力の形（release の段の items の要素）が文書と照合されるよう、マージされる
		// release を 1 件置き、ID を明示して実行する（not_started の要素の形は cycle の観測が照合する）。
		_, op := prepareExecWorkspace(t, ws)
		newReleaseGH(t, execHead)
		return []string{"op", "exec", op}
	}},
	"dep add": {setup: func(t *testing.T, ws string) []string {
		id := createForCase(t, ws)
		return []string{"dep", "add", id, createForCase(t, ws)}
	}},
	"dep rm": {setup: func(t *testing.T, ws string) []string {
		id := createForCase(t, ws)
		dep := createForCase(t, ws)
		runJSON(t, ws, "dep", "add", id, dep)
		return []string{"dep", "rm", id, dep}
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
	"mark-read": {setup: func(t *testing.T, ws string) []string {
		id := createForCase(t, ws)
		bindSourceForDiscrepancyCase(t, ws, id, "o/r#1", "open", "in_policy")
		// 未読の更新（comments_count > read_comments_count）がある状態にして、
		// mark-read が実際に changed=true になる経路を通す。
		coretest.SetSourceBindingObservation(t, ws, challengeIDToInternalID(t, id), 2, "2026-09-25T08:00:00.000Z", 0, "")
		return []string{"mark-read", id}
	}},
	"runs": {setup: func(t *testing.T, ws string) []string {
		return []string{"runs", createForCase(t, ws)}
	}},
	"metrics": {setup: func(t *testing.T, ws string) []string {
		// 完了の承認・差し戻し・発生を 1 件ずつ持たせ、入れ子（by・by_field など）の要素の形も照合する。
		id := createForCase(t, ws)
		cid := challengeIDToInternalID(t, id)
		coretest.Exec(t, ws, `INSERT INTO approval (challenge_id, kind, decision, target_version, actor, channel, verification, decided_at, rejected_fields)
			VALUES (?, 'completion', 'rejected', 1, 'a', 'cli', 'tty_confirm', '2026-03-01T00:00:00.000Z', '["issues"]')`, cid)
		coretest.Exec(t, ws, `INSERT INTO approval (challenge_id, kind, decision, target_version, actor, channel, verification, decided_at)
			VALUES (?, 'completion', 'approved', 1, 'a', 'cli', 'tty_confirm', '2026-03-02T00:00:00.000Z')`, cid)
		coretest.Exec(t, ws, `INSERT INTO occurrence (challenge_id, kind, activity_id, outcome, source, created_at)
			VALUES (?, 'completion_rejected', (SELECT MAX(id) FROM activity), 'bad', 'core', '2026-03-01T00:00:00.000Z')`, cid)
		return []string{"metrics"}
	}},
	"learn": {setup: func(t *testing.T, ws string) []string {
		// 対象の条件を満たさない課題を指す learn（not_started に not_learnable。J6 は起動されず作業ログも増えない）。
		// 要素の形を照合する経路は learn_test.go の TestLearn_JSONIsOnePhaseObject。
		putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: `{"is_error":false}`})
		return []string{"learn", createForCase(t, ws)}
	}},
	"improve": {setup: func(t *testing.T, _ string) []string {
		// 起動の件数が 0 の improve（J8 を呼ばず、items は空）。要素の形を照合する経路は improve_test.go。
		putFakeClaudeOnPATH(t, fakeClaudeOpts{Stdout: `{"is_error":false}`})
		return []string{"improve"}
	}},
	"export": {setup: func(t *testing.T, ws string) []string {
		createForCase(t, ws)
		return []string{"export", "--out", filepath.Join(t.TempDir(), "export.json")}
	}},
	"import": {setup: func(t *testing.T, _ string) []string {
		// 課題を 1 件持つ別のストアの書き出しを、行の無い ws へ読み込む（作業ログの行を持たない課題にして、
		// import が作業ログを足さないことを worklog の検査が確かめられるようにする）。
		other := t.TempDir()
		if _, err := core.Init(other); err != nil {
			t.Fatalf("core.Init: %v", err)
		}
		coretest.InsertChallenge(t, other, "t")
		path := filepath.Join(t.TempDir(), "export.json")
		runJSON(t, other, "export", "--out", path)
		return []string{"import", "--in", path}
	}},
	"cycle": {setup: func(t *testing.T, ws string) []string {
		// cycle --json の出力の形（phases の各段・items・not_started・ingest の result）が
		// 文書と照合されるよう、取り込みで課題を 1 件作り、同じ周で分類・計画まで進める。
		// 空の一覧だと要素の形が 1 件も照合されない。
		writePositionFileForTest(t, ws, "pos")
		writeAgentJSONForTest(t, ws, `{"position_file": "position.md"}`)
		writeConnectorsJSONForTest(t, ws, j2ConnectorsFixture)
		writeSourcesDeclaration(t, ws, `{
  "version": 1,
  "sources": [{"id": "src", "type": "github-issue", "repos": ["o/r"], "self_assignees": ["someone"]}]
}`)
		issue := marshalFakeGHIssue(t, fakeGHIssue{Number: 1, Title: "allcommands cycle", Body: "body", Repo: "o/r"})
		withFakeGHRoutesOnPATH(t, []fakeGHRoute{
			fakeGHListRoute("o/r", 1, "["+issue+"]", 0),
			fakeGHGetIssueStatusLineRoute("o/r", 1, "HTTP/2.0 200 OK", issue, 0),
			{match: "repos/o/r/issues/1/comments?per_page=100&page=1", stdout: "[]"},
		})
		// J1 は mine、J2 は plan を返し、取り込んだ課題を同じ周で計画まで進める（items の要素の
		// 形が照合される）。not_started の要素の形は cycle_closedsets_test.go の観測が照合する。
		putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1"), j2RoutePlan(t)}, "")
		return []string{"cycle", "--trigger", "cron"}
	}},
	"run": {setup: func(t *testing.T, ws string) []string {
		// run --json の出力の形（{"phase": {…}} と phase の要素）が文書と照合されるよう、委譲の対象の
		// 課題を 1 件置き、偽の claude が J3 と委譲に応答する。
		writePositionFileForTest(t, ws, "pos")
		writeAgentJSONForTest(t, ws, `{"position_file": "position.md"}`)
		writeConnectorsJSONForTest(t, ws, runConnectorsFixture)
		slotClone(t, filepath.Join(ws, "slot-f"), "https://github.com/o/flywheel.git")
		slotClone(t, filepath.Join(ws, "slot-d"), "git@github.com:o/direct.git")
		id := newInProgressForRun(t, ws, nil)
		// 結末 questions・blocked・completed は課題を保留・検証中へ進める（作業ログに載る）ため、
		// 作業ログを増やさない run の成功経路には、課題の状態を変えない委譲の失敗（errored）を使う。
		erroredDelegate := fakeClaudeRoute{Match: fakeClaudeDelegateMatch, Tag: "DELEGATE",
			Stdout: `{"session_id":"s","is_error":true,"total_cost_usd":0.5,"result":"boom"}`}
		putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{erroredDelegate, j3Route("b")}, "")
		return []string{"run", id}
	}},
	"slot clear": {setup: func(t *testing.T, ws string) []string {
		// needs_attention のスロットを 1 件置き、slot の形が文書と照合されるようにする。
		id := coretest.InsertSlot(t, ws, "o/r", "clone", filepath.Join(ws, "clone-slot"), "needs_attention", "path is missing")
		return []string{"slot", "clear", fmt.Sprintf("SL-%d", id)}
	}},
	"ingest": {setup: func(t *testing.T, ws string) []string {
		writeSourcesDeclaration(t, ws, validSourcesDeclaration)
		// #59 で取得と反映を結線した後は、この横断テスト（jsondoc の形の照合。
		// AC-94）が実際に items 配下の要素（item の形）まで踏むよう、偽の gh に
		// 1 件の Issue を返させる（空の一覧だと item の形が一度も照合されない）。
		withFakeGHRoutesOnPATH(t, []fakeGHRoute{
			fakeGHListRoute("example-owner/example-repo", 1, fakeGHIssueListBody(t, []fakeGHIssue{
				{Number: 1, Title: "allcommands smoke", Body: "body", Repo: "example-owner/example-repo"},
			}), 0),
		})
		return []string{"ingest"}
	}},
}

// longRunningCommands は成功経路が「シグナルを受けるまで終わらない」ため、各コマンドが
// 1 回の実行で成功して返る前提の横断の列挙（成功経路・成功時の JSON・作業ログ・
// 古いストアの拒否・M1 の IF/API の表）から外すコマンド。serve（M4 S1）の検証は
// serve_test.go が子プロセスで行う。
var longRunningCommands = map[string]bool{"serve": true}

// registeredCommands は登録表の全コマンド（longRunningCommands を除く）を、Path を
// スペースで結合した名前で返す。
func registeredCommands() map[string]Command {
	out := map[string]Command{}
	for _, cmd := range defaultCommands() {
		name := strings.Join(cmd.Path, " ")
		if longRunningCommands[name] {
			continue
		}
		out[name] = cmd
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
	{"serve", "--port", "abc"},                                     // --port が 0〜65535 の整数でない（bind する前に拒否する）
	{"serve", "--port", "0", "--fleet", "/nonexistent/fleet.json"}, // --fleet と --workspace の同時指定（この列挙は --workspace を足して呼ぶ）
	{"edit", "C-1"}, // 変更する項目が 1 つも無い
	{"plan", "C-1", "--file", "/nonexistent/flywheel-plan.txt"}, // --file が読めない
	{"verify", "C-1", "--result", "met", "--question", "q"},     // met と --question の同時指定
	{"approve", "OP-1", "--hold-release"},                       // 不可逆操作の ID への --hold-release
	{"approve", "C-2", "--hold-release"},                        // 計画承認待ちの課題への --hold-release
	{"classify", "--priority", "P0"},                            // --priority に課題の ID が無い（#84。--auto は ID を省略できるため MinPositional では表せない）
	{"plan", "--stdin"},                                         // --file／--stdin に課題の ID が無い（#85。--auto は ID を省略できるため MinPositional では表せない）
	{"budget", "C-1", "--impl-usd", "x"},                        // --impl-usd が数でない（#105）
	{"verify", "--result", "met"},                               // --result に課題の ID が無い（--auto は ID を省略できるため MinPositional では表せない）
	{"verify", "--auto", "--question", "q"},                     // --auto と --question の同時指定（J5 は問いを自分で作る）
	{"approve", "C-2", "--session", "s"},                        // --relayed なしの --session・--quote（M4P26）
	{"approve", "C-2", "--relayed", "--quote", "q"},             // --relayed に --session が無い（空白だけも同じ）
	{"approve", "C-2", "--relayed", "--session", "s"},           // --relayed に --quote が無い（空白だけも同じ）
	{"reject", "OP-1", "--reason", "r", "--field", "issues"},    // 不可逆操作の ID への --field（M4P36）
	{"metrics", "--since", "2026-3-1"},                          // --since・--until が YYYY-MM-DD の日付でない（M4.5 S3）
	{"metrics", "--compare", "C-1", "--since", "2026-03-01"},    // --compare と --since・--until の同時指定（M4.5 S4）
	{"dep", "add", "C-1", "C-1"},                                // 自分自身を依存先にした（core が ErrSelfDependency を返し usage_error に写る。M4.5 S2）
	{"withdraw", "C-1", "--reason", " "},                        // --reason が空白だけ（M4.5 S4。無いときは必須フラグの欠落）
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
