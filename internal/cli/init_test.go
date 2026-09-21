package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masanami/flywheel/internal/core/coretest"
)

// TestInit_CreatesStoreAndExitsZero は
// AC「flywheel init を実行すると、ワークスペースに .flywheel/flywheel.db が
// 作られ、終了コード 0 で終わる」の CLI 経由の検証。
func TestInit_CreatesStoreAndExitsZero(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"init", "--workspace", dir, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stderr=%s)", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", "flywheel.db")); err != nil {
		t.Fatalf("store not created: %v", err)
	}

	var doc struct {
		Workspace string `json:"workspace"`
		StorePath string `json:"store_path"`
		Created   bool   `json:"created"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (%q)", err, stdout.String())
	}
	if !doc.Created {
		t.Error("created = false, want true on the first init")
	}
	if doc.Workspace == "" || doc.StorePath == "" {
		t.Errorf("workspace/store_path should not be empty: %+v", doc)
	}
}

// TestInit_ReRunOnWorkspaceWithAChallengeIsANoOpSuccess は
// AC「課題が 1 件あるワークスペースで flywheel init を再実行すると、終了コード 0
// で終わり、その課題が変わらずに残っている」の検証。create は未実装のため、
// 直接ストアへ課題を 1 件挿入してからワークスペースとして扱う。
func TestInit_ReRunOnWorkspaceWithAChallengeIsANoOpSuccess(t *testing.T) {
	dir := initializedWorkspace(t)
	insertTestChallenge(t, dir, "keep me")

	var stdout, stderr bytes.Buffer
	code := run([]string{"init", "--workspace", dir, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stderr=%s)", code, stderr.String())
	}

	var doc struct {
		Created bool `json:"created"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v", err)
	}
	if doc.Created {
		t.Error("created = true on a re-run, want false (idempotent)")
	}

	if got := countTestChallenges(t, dir); got != 1 {
		t.Fatalf("challenge count after re-init = %d, want 1", got)
	}
}

// TestInit_UsesWorkspaceFlagRegardlessOfCwd は
// AC「--workspace <dir> を指定したコマンドは、カレントディレクトリに関係なく
// <dir>/.flywheel/flywheel.db を使う」の検証（init について）。
func TestInit_UsesWorkspaceFlagRegardlessOfCwd(t *testing.T) {
	target := t.TempDir()
	elsewhere := t.TempDir()
	t.Chdir(elsewhere)

	var stdout, stderr bytes.Buffer
	code := run([]string{"init", "--workspace", target, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stderr=%s)", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(target, ".flywheel", "flywheel.db")); err != nil {
		t.Fatalf("store not created under --workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, ".flywheel")); err == nil {
		t.Fatal("init unexpectedly created a store under the cwd instead of --workspace")
	}
}

// TestInit_FallsBackToEnvVarWhenNoWorkspaceFlag は環境変数
// FLYWHEEL_WORKSPACE の優先順位（--workspace が無ければ使う）の検証。
func TestInit_FallsBackToEnvVarWhenNoWorkspaceFlag(t *testing.T) {
	target := t.TempDir()
	t.Setenv("FLYWHEEL_WORKSPACE", target)

	var stdout, stderr bytes.Buffer
	code := run([]string{"init", "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stderr=%s)", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(target, ".flywheel", "flywheel.db")); err != nil {
		t.Fatalf("store not created under FLYWHEEL_WORKSPACE: %v", err)
	}
}

// TestInit_DoesNotSearchAncestorsForAnExistingStore は
// AC「親ディレクトリにストアがあるサブディレクトリで init を実行すると、その
// サブディレクトリに新しいストアが作られ、親のストアは変わらない」の検証。
func TestInit_DoesNotSearchAncestorsForAnExistingStore(t *testing.T) {
	parent := initializedWorkspace(t)
	insertTestChallenge(t, parent, "parent challenge")

	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"init", "--workspace", child, "--json"}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stderr=%s)", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(child, ".flywheel", "flywheel.db")); err != nil {
		t.Fatalf("child store not created (init should not search ancestors, PD6): %v", err)
	}
	if got := countTestChallenges(t, parent); got != 1 {
		t.Fatalf("parent challenge count = %d, want 1 (unaffected by child init)", got)
	}
	if got := countTestChallenges(t, child); got != 0 {
		t.Fatalf("child challenge count = %d, want 0 (fresh store)", got)
	}
}

// TestInit_TextModeSucceedsWithoutJSON は --json 無しの出力でもクラッシュせず
// 成功することの確認（CLI 共通の「--json 無しの出力は人間向け」規約）。
func TestInit_TextModeSucceedsWithoutJSON(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"init", "--workspace", dir}, strings.NewReader(""), &stdout, &stderr, defaultCommands())
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (stderr=%s)", code, stderr.String())
	}
	if stdout.Len() == 0 {
		t.Fatal("text mode produced no output")
	}
}

// --- helpers ---
//
// insertTestChallenge・countTestChallenges は internal/core の公開 API
// （課題の CRUD）がまだ無い本チケットの時点で、フィクスチャを作る・検証する
// ためだけに使う（本番の CLI 実装はこの経路を使わない）。internal/core/coretest
// 経由にすることで、internal/cli のテストが modernc.org/sqlite や
// internal/core/internal/store（Go の internal 規則で internal/cli からは
// import できない）に直接依存しないようにする
// （internal/cli/depcheck_test.go が検査する）。

func insertTestChallenge(t *testing.T, workspace, title string) {
	t.Helper()
	coretest.InsertChallenge(t, workspace, title)
}

func countTestChallenges(t *testing.T, workspace string) int {
	t.Helper()
	return coretest.CountChallenges(t, workspace)
}
