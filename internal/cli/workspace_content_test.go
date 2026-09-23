package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// このファイルは受入基準 2・6・7（ワークスペースとストア）のテストのうち、
// init_test.go の既存テストが件数・存在だけで確かめていた部分を、ストアの
// 中身の比較で補う（#33）。中身は CLI の公開面（list・show・log・status の
// JSON）で取り出して比べ、core の公開 API 以外に依存しない。

// storeContentSnapshot は ws のストアの中身を、読み取り専用のコマンドの JSON で
// まとめて取り出す。課題ごとの show（計画・承認・保留・不可逆操作を含む）と
// log（作業ログ全件）・list・status を 1 つの値にし、reflect.DeepEqual で
// 前後を比べられるようにする。
func storeContentSnapshot(t *testing.T, ws string) map[string]any {
	t.Helper()
	list := runJSON(t, ws, "list")
	snap := map[string]any{
		"list":   list,
		"log":    runJSON(t, ws, "log"),
		"status": runJSON(t, ws, "status"),
	}
	challenges, ok := list["challenges"].([]any)
	if !ok {
		t.Fatalf("list.challenges = %#v, want an array", list["challenges"])
	}
	for _, c := range challenges {
		id := c.(map[string]any)["id"].(string)
		snap["show "+id] = runJSON(t, ws, "show", id)
	}
	return snap
}

// requireSameStoreContent は before と after が一致しなければ、両方を JSON で
// 示して Fatal する。
func requireSameStoreContent(t *testing.T, what string, before, after map[string]any) {
	t.Helper()
	if reflect.DeepEqual(before, after) {
		return
	}
	b, _ := json.MarshalIndent(before, "", "  ")
	a, _ := json.MarshalIndent(after, "", "  ")
	t.Fatalf("%s changed:\nbefore=%s\nafter=%s", what, b, a)
}

// seedDetailedChallenge は ws に、人間記入欄・分類・計画・不可逆操作・作業ログを
// 持つ課題を 1 件作り、その ID を返す（件数だけでなく各欄が保たれることを
// 比べられるよう、空でない欄を多く持たせる）。
func seedDetailedChallenge(t *testing.T, ws, title string) string {
	t.Helper()
	created := runJSON(t, ws, "create", "--title", title, "--description", title+" description", "--done-criteria", title+" done", "--urgency", "高")
	id := created["challenge"].(map[string]any)["id"].(string)
	runJSON(t, ws, "classify", id, "--priority", "P1")
	runJSON(t, ws, "plan", id, "--file", writePlanFileForTest(t, title+" plan body"))
	runJSON(t, ws, "op", "add", id, "--kind", "external_send", "--summary", title+" op", "--ref", "https://example.invalid/"+title)
	return id
}

// storeFileContents は ws/.flywheel 配下の通常ファイルの名前と内容を返す。
func storeFileContents(t *testing.T, ws string) map[string][]byte {
	t.Helper()
	dir := filepath.Join(ws, ".flywheel")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		files[e.Name()] = data
	}
	return files
}

func storeFileNames(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestInit_ReRunKeepsTheChallengeContentUnchanged は受入基準 2
// （課題が 1 件あるワークスペースで init を再実行すると、終了コード 0 で終わり、
// その課題が変わらずに残っている）を、件数ではなく中身の比較で確かめる。
func TestInit_ReRunKeepsTheChallengeContentUnchanged(t *testing.T) {
	ws := initializedWorkspace(t)
	seedDetailedChallenge(t, ws, "keep-me")
	before := storeContentSnapshot(t, ws)

	var stdout, stderr bytes.Buffer
	code := run([]string{"init", "--workspace", ws, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stderr=%s)", code, stderr.String())
	}

	requireSameStoreContent(t, "store content after re-running init", before, storeContentSnapshot(t, ws))
}

// TestInit_InSubdirectoryLeavesParentStoreContentUnchanged は受入基準 6
// （親ディレクトリにストアがあるサブディレクトリで init を実行すると、親の
// ストアは変わらない）を、親のストアのファイルの内容と、CLI で読んだ中身の
// 両方の比較で確かめる。
func TestInit_InSubdirectoryLeavesParentStoreContentUnchanged(t *testing.T) {
	parent := initializedWorkspace(t)
	seedDetailedChallenge(t, parent, "parent")
	beforeContent := storeContentSnapshot(t, parent)
	// ファイルの比較は、読み取りのコマンドで開いた後の状態を基準にする
	// （読み取りが付随ファイルを作り直すことはあっても、それは子の init の
	// 影響ではないため）。
	beforeFiles := storeFileContents(t, parent)

	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"init", "--workspace", child, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stderr=%s)", code, stderr.String())
	}

	afterFiles := storeFileContents(t, parent)
	if !reflect.DeepEqual(storeFileNames(beforeFiles), storeFileNames(afterFiles)) {
		t.Fatalf("parent .flywheel files = %v, want %v", storeFileNames(afterFiles), storeFileNames(beforeFiles))
	}
	for name, want := range beforeFiles {
		if !bytes.Equal(afterFiles[name], want) {
			t.Errorf("parent .flywheel/%s content changed by init in the subdirectory", name)
		}
	}
	requireSameStoreContent(t, "parent store content after init in a subdirectory", beforeContent, storeContentSnapshot(t, parent))
}

// TestWorkspaceFlag_NonInitCommandsUseItOverTheStoreUnderCwd は受入基準 7
// （--workspace <dir> を指定したコマンドは、カレントディレクトリに関係なく
// <dir>/.flywheel/flywheel.db を使う）を init 以外のコマンドで確かめる。
// カレントディレクトリ（とその親）に別のストアがある状態で、読み取り
// （show・list・log・status）と変更（create・edit）の両方を --workspace 付きで
// 実行し、--workspace 側のストアが読み書きされ、カレント側のストアが
// 変わらないことを確かめる。
func TestWorkspaceFlag_NonInitCommandsUseItOverTheStoreUnderCwd(t *testing.T) {
	// 環境変数が設定されていると --workspace ではなくそちらで基点が決まる
	// 経路と区別できなくなるため、空にしておく。
	t.Setenv("FLYWHEEL_WORKSPACE", "")

	cwdWS := initializedWorkspace(t)
	cwdID := seedDetailedChallenge(t, cwdWS, "cwd-side")
	target := initializedWorkspace(t)
	targetID := seedDetailedChallenge(t, target, "target-side")
	if cwdID != targetID {
		t.Fatalf("setup: ids differ (%s vs %s); both stores should hold the same ID so show/edit can only be told apart by content", cwdID, targetID)
	}
	cwdBefore := storeContentSnapshot(t, cwdWS)
	targetBefore := storeContentSnapshot(t, target)

	// カレントはストアのあるディレクトリそのもの、とその下位ディレクトリ
	// （親へ遡る探索でカレント側のストアが見つかる位置）の両方で確かめる。
	sub := filepath.Join(cwdWS, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, cwd := range map[string]string{"store_dir": cwdWS, "store_subdir": sub} {
		t.Run(name, func(t *testing.T) {
			t.Chdir(cwd)

			// 読み取り: --workspace 側の中身が返る。
			if got := runJSON(t, target, "show", targetID); !reflect.DeepEqual(got, targetBefore["show "+targetID]) {
				t.Errorf("show = %v, want the --workspace store's %v", got, targetBefore["show "+targetID])
			}
			for _, cmd := range []string{"list", "log", "status"} {
				if got := runJSON(t, target, cmd); !reflect.DeepEqual(got, targetBefore[cmd]) {
					t.Errorf("%s = %v, want the --workspace store's %v", cmd, got, targetBefore[cmd])
				}
			}
		})
	}

	// 変更: --workspace 側に書かれ、カレント側は変わらない。
	t.Chdir(sub)
	edited := runJSON(t, target, "edit", targetID, "--title", "edited-via-workspace-flag")
	if got := edited["challenge"].(map[string]any)["title"]; got != "edited-via-workspace-flag" {
		t.Fatalf("edit returned title %v", got)
	}
	created := runJSON(t, target, "create", "--title", "created-via-workspace-flag")
	createdID := created["challenge"].(map[string]any)["id"].(string)

	requireSameStoreContent(t, "store under cwd after --workspace commands", cwdBefore, storeContentSnapshot(t, cwdWS))

	targetShow := runJSON(t, target, "show", targetID)
	if got := targetShow["challenge"].(map[string]any)["title"]; got != "edited-via-workspace-flag" {
		t.Errorf("--workspace store title = %v, want edited-via-workspace-flag", got)
	}
	if got := runJSON(t, target, "show", createdID)["challenge"].(map[string]any)["title"]; got != "created-via-workspace-flag" {
		t.Errorf("--workspace store has created challenge title %v, want created-via-workspace-flag", got)
	}
}

// TestWorkspaceFlag_NonInitCommandDoesNotFallBackToTheStoreUnderCwd は
// 受入基準 7 の裏側: --workspace で指したディレクトリにストアが無ければ、
// カレントディレクトリにストアがあっても使わず store_not_found で失敗する。
func TestWorkspaceFlag_NonInitCommandDoesNotFallBackToTheStoreUnderCwd(t *testing.T) {
	t.Setenv("FLYWHEEL_WORKSPACE", "")
	cwdWS := initializedWorkspace(t)
	seedDetailedChallenge(t, cwdWS, "cwd-side")
	cwdBefore := storeContentSnapshot(t, cwdWS)
	empty := t.TempDir()

	t.Chdir(cwdWS)
	requireErrorCode(t, []string{"list", "--workspace", empty}, 2, CodeStoreNotFound)
	requireErrorCode(t, []string{"create", "--title", "x", "--workspace", empty}, 2, CodeStoreNotFound)

	if _, err := os.Stat(filepath.Join(empty, ".flywheel")); err == nil {
		t.Error("a non-init command created a store under --workspace")
	}
	requireSameStoreContent(t, "store under cwd", cwdBefore, storeContentSnapshot(t, cwdWS))
}
