package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// このファイルは Issue #54「cli: flywheel ingest の骨格」の受入基準
// （docs/features/m2-github-issue-ingest.md の AC-1・AC-2・AC-13・AC-98）を検証する。
// 取り込みの本体（取得・作成・更新）は #59 の範囲であり、ここでは宣言の読み込み
// （#53 の core.LoadSourcesDeclaration・core.SelectSources）の結果を CLI の
// 終了コード・エラーコードへ写す入口だけを検証する。

// writeFakeGH は、呼び出されるたびに引数を1行としてログファイルへ記録するだけの
// 偽の `gh` 実行ファイルを t.TempDir() に置く。戻り値の dir を PATH の先頭に
// 足すことで、`ingest` が `gh` を呼んだかどうかをテストから観測できる
// （docs/features/m2-github-issue-ingest.md「偽の `gh`」の定義）。
//
// 各行は "CALL " を必ず前置する（self-review 指摘: 素の `echo "$@"` だと、
// 引数なしの呼び出しが空行になる。calls() は末尾の1要素だけを取り除く方式で
// 空行を保持するため前置が無くても数え自体は壊れないが、前置により行は常に
// 非空になり、「ファイル自体が無い＝1回も呼ばれていない」と「引数なしで
// 呼ばれた」の違いが行の中身からも一目で読み取れる二重の保険になる）。
func writeFakeGH(t *testing.T) (dir string, calls func() []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("動作環境は macOS と Linux のみ（CLAUDE.md）")
	}
	dir = t.TempDir()
	logPath := filepath.Join(dir, "gh-calls.log")
	script := "#!/bin/sh\n" + fakeGHLogAppendLine(logPath) + "exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	return dir, func() []string { return readFakeGHCallLog(t, logPath) }
}

// withFakeGHOnPATH は fake gh のディレクトリを PATH の先頭に足す。実際に
// PATH 上の `gh` を解決できるのは exec.LookPath 経由だけであり、テストの
// プロセス自身が `gh` を import することは無い。
func withFakeGHOnPATH(t *testing.T) func() []string {
	t.Helper()
	dir, calls := writeFakeGH(t)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

// writeSourcesDeclaration は ws（core.Init 済みのワークスペース）に
// .flywheel/sources.json を書く。
func writeSourcesDeclaration(t *testing.T, ws, content string) {
	t.Helper()
	path := filepath.Join(ws, ".flywheel", "sources.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write sources.json: %v", err)
	}
}

// validSourcesDeclaration はテスト用の宣言。repos には実在しないプレースホルダの
// リポジトリ名を使う（self-review 指摘: 実在するリポジトリ名を使うと、#59 で
// 取得が結線された後にこのテストが偽の gh を PATH に置き忘れた場合、本物の
// GitHub へ接続する事故が起きても気付きにくい。プレースホルダなら、万一
// 本物の `gh` が呼ばれても実在しないリポジトリとして失敗し、テストが
// ネットワーク到達性に依存して偶然パスすることもない）。
const validSourcesDeclaration = `{
  "version": 1,
  "sources": [
    {"id": "harness-repo-issues", "type": "github-issue", "repos": ["example-owner/example-repo"]}
  ]
}`

// storeBytes はワークスペース ws のストアファイルの内容を読む（前後比較用）。
func storeBytes(t *testing.T, ws string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(ws, ".flywheel", "flywheel.db"))
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	return data
}

// TestIngest_RegisteredWithCommonFlagsAndOptionalSource は、`ingest` が
// 登録表にあり、位置引数を取らず、`--source` が値ありの任意フラグであることを
// 検証する（完了条件「`flywheel ingest [--source <id>]` がコマンドの登録表にある」）。
func TestIngest_RegisteredWithCommonFlagsAndOptionalSource(t *testing.T) {
	cmd, ok := registeredCommands()["ingest"]
	if !ok {
		t.Fatal(`"ingest" is not a registered command`)
	}
	if !cmd.RequiresStore {
		t.Error("ingest must RequiresStore (store_not_found/store_too_new/store_busy を一様に扱うため)")
	}
	if cmd.MinPositional != 0 || cmd.MaxPositional != 0 {
		t.Errorf("ingest must take no positional arguments, got min=%d max=%d", cmd.MinPositional, cmd.MaxPositional)
	}
	var sourceFlag *flagDef
	for i := range cmd.Flags {
		if cmd.Flags[i].Name == "source" {
			sourceFlag = &cmd.Flags[i]
		}
	}
	if sourceFlag == nil {
		t.Fatal("ingest has no --source flag")
	}
	if !sourceFlag.HasValue {
		t.Error("--source must take a value")
	}
	if sourceFlag.Required {
		t.Error("--source must be optional")
	}
	// --dry-run は S2 のため足さない（完了条件）。
	for _, f := range cmd.Flags {
		if f.Name == "dry-run" {
			t.Error("--dry-run must not be added in S1 (S2 の範囲)")
		}
	}
}

// TestIngest_MissingDeclaration_ConfigNotFound は AC-1:
// `.flywheel/sources.json` が無いワークスペースで `flywheel ingest` を実行すると、
// 終了コード 2・`config_not_found` で終わり、ストアが変わらない。`gh` も呼ばれない。
func TestIngest_MissingDeclaration_ConfigNotFound(t *testing.T) {
	ws := initializedWorkspace(t)
	calls := withFakeGHOnPATH(t)
	before := storeBytes(t, ws)

	requireJSONErrorEnvelope(t, []string{"ingest", "--workspace", ws}, 2, CodeConfigNotFound)

	if !bytes.Equal(before, storeBytes(t, ws)) {
		t.Error("config_not_found must not change the store file")
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("gh must not be invoked, got calls: %v", got)
	}
}

// TestIngest_UnparseableDeclaration_ConfigInvalid は AC-2:
// 解釈できない JSON の宣言で `ingest` を実行すると、終了コード 2・
// `config_invalid` で終わり、ストアが変わらず、`gh` が呼ばれない。
func TestIngest_UnparseableDeclaration_ConfigInvalid(t *testing.T) {
	ws := initializedWorkspace(t)
	writeSourcesDeclaration(t, ws, `{ this is not json `)
	calls := withFakeGHOnPATH(t)
	before := storeBytes(t, ws)

	requireJSONErrorEnvelope(t, []string{"ingest", "--workspace", ws}, 2, CodeConfigInvalid)

	if !bytes.Equal(before, storeBytes(t, ws)) {
		t.Error("config_invalid must not change the store file")
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("gh must not be invoked, got calls: %v", got)
	}
}

// TestIngest_UnknownSourceFlag_ValidationFailed は AC-13:
// `flywheel ingest --source <宣言に無い id>` は、終了コード 1・
// `validation_failed` で終わり、ストアが変わらない。
func TestIngest_UnknownSourceFlag_ValidationFailed(t *testing.T) {
	ws := initializedWorkspace(t)
	writeSourcesDeclaration(t, ws, validSourcesDeclaration)
	before := storeBytes(t, ws)

	requireJSONErrorEnvelope(t, []string{"ingest", "--source", "does-not-exist", "--workspace", ws}, 1, CodeValidationFailed)

	if !bytes.Equal(before, storeBytes(t, ws)) {
		t.Error("validation_failed must not change the store file")
	}
}

// TestIngest_ExplicitEmptySourceFlag_ValidationFailed は self-review 指摘
// （code-reviewer・design-reviewer が独立に検出）の再発防止テスト:
// `--source ""`（明示的な空文字列）を「--source 省略」と混同すると、全ての
// 取り込み元が選ばれてしまい AC-13 の fail-closed（宣言に無い id を拒否する）
// から外れる。空文字列はどの宣言の id とも一致しえない（id は
// `^[a-z0-9][a-z0-9-]*$` に一致する必要がある）ため、宣言に無い id と同じ
// validation_failed になるべき。
func TestIngest_ExplicitEmptySourceFlag_ValidationFailed(t *testing.T) {
	ws := initializedWorkspace(t)
	writeSourcesDeclaration(t, ws, validSourcesDeclaration)
	before := storeBytes(t, ws)

	requireJSONErrorEnvelope(t, []string{"ingest", "--source=", "--workspace", ws}, 1, CodeValidationFailed)

	if !bytes.Equal(before, storeBytes(t, ws)) {
		t.Error("validation_failed must not change the store file")
	}
}

// TestIngest_StoreTooNew_DoesNotInvokeGH は AC-98:
// スキーマ版がバイナリの知る最新版より大きいストアでは、`ingest` も終了コード 2・
// `store_too_new` で終わり、`gh` が呼ばれない。判定順序: store_too_new は
// 宣言の読み込み・gh の呼び出しより前（RequiresStore がコマンドの Run を呼ぶ前に
// ストアを開くため）。宣言ファイルを置かない状態でも store_too_new が勝つことで、
// この順序を確認する。
func TestIngest_StoreTooNew_DoesNotInvokeGH(t *testing.T) {
	ws := initializedWorkspace(t)
	coretest.SetStoreVersion(t, ws, 999999)
	calls := withFakeGHOnPATH(t)
	before := storeBytes(t, ws)

	requireJSONErrorEnvelope(t, []string{"ingest", "--workspace", ws}, 2, CodeStoreTooNew)

	if !bytes.Equal(before, storeBytes(t, ws)) {
		t.Error("store_too_new must not change the store file")
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("gh must not be invoked, got calls: %v", got)
	}
}

// TestIngest_ValidDeclaration_CallsGHAndReturnsSourceShape は、取得と反映の
// 結線後（#59）の振る舞い: 宣言と --source の検証を通ったら、実際に gh を
// 呼び、取り込み元・リポジトリごとの結果を返す（#54 時点の「常に空の結果を
// 返す」という仮定はここで置き換える。取得・作成・更新の中身そのものの検証は
// internal/cli/ingest_wiring_test.go・internal/core の各テストが担う）。
func TestIngest_ValidDeclaration_CallsGHAndReturnsSourceShape(t *testing.T) {
	ws := initializedWorkspace(t)
	writeSourcesDeclaration(t, ws, validSourcesDeclaration)
	calls := withFakeGHRoutesOnPATH(t, []fakeGHRoute{
		fakeGHListRoute("example-owner/example-repo", 1, "[]", 0),
		{match: "api user", stdout: `{"login":"someone"}`, exit: 0},
	})

	got := runJSON(t, ws, "ingest")

	sources, ok := got["sources"].([]any)
	if !ok || len(sources) != 1 {
		t.Fatalf(`ingest --json "sources" = %v, want exactly the 1 declared source`, got)
	}
	source := findSourceByID(t, sources, "harness-repo-issues")
	repos := reposOf(t, source)
	if len(repos) != 1 || repos[0]["repo"] != "example-owner/example-repo" || repos[0]["error"] != nil {
		t.Errorf("source.repos = %v, want 1 repo (example-owner/example-repo) with error=nil", repos)
	}
	if callList := calls(); len(callList) == 0 {
		t.Errorf("gh must be invoked now that the ingest body is wired, got no calls")
	}
}

// TestIngest_ValidDeclarationWithKnownSource_Succeeds は、宣言にある id を
// --source に指定した場合も成功することを検証する（validation_failed の判定が
// 既知の id を誤って拒否しないことの確認）。
func TestIngest_ValidDeclarationWithKnownSource_Succeeds(t *testing.T) {
	ws := initializedWorkspace(t)
	writeSourcesDeclaration(t, ws, validSourcesDeclaration)
	withFakeGHRoutesOnPATH(t, []fakeGHRoute{
		fakeGHListRoute("example-owner/example-repo", 1, "[]", 0),
		{match: "api user", stdout: `{"login":"someone"}`, exit: 0},
	})

	got := runJSON(t, ws, "ingest", "--source", "harness-repo-issues")

	sources, ok := got["sources"].([]any)
	if !ok || len(sources) != 1 {
		t.Errorf(`ingest --source <known id> --json sources = %v, want exactly 1 source`, sources)
	}
}
