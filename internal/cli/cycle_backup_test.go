package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gitadapter "github.com/masanami/flywheel/internal/adapters/git"
	"github.com/masanami/flywheel/internal/core"
)

// このファイルは M5 S1 の cycle の最後の段 backup（docs/features/m5-tom-migration-and-switchover.md
// §書き出しのコミット）を、実物の git で検証する。core の分岐は internal/core/cycle_backup_test.go が持つ。

func backupGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// backupWorkspace は Git の作業ツリーにした、position を持つワークスペースを返す。
func backupWorkspace(t *testing.T) string {
	t.Helper()
	// adapter が起動する git も identity を必要とする（CI には無い）ので、プロセスの環境に置く。
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
		"GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com",
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
	} {
		t.Setenv(k, v)
	}
	ws := setupWorkspaceWithPosition(t)
	backupGit(t, ws, "init", "-q")
	backupGit(t, ws, "add", "-A")
	backupGit(t, ws, "commit", "-q", "-m", "base")
	return ws
}

func backupPhaseOf(t *testing.T, ws string) map[string]any {
	t.Helper()
	putRoutedFakeClaudeOnPATH(t, []fakeClaudeRoute{j1RouteMine("P1")}, "")
	doc := cycleDoc(t, ws)
	phases := doc["phases"].([]any)
	last := phases[len(phases)-1].(map[string]any)
	if last["phase"] != "backup" {
		t.Fatalf("last phase = %v, want backup", last)
	}
	return last["result"].(map[string]any)
}

func TestCycle_Backup_CommitsOnlyTheExportAndLeavesOtherChangesAlone(t *testing.T) {
	ws := backupWorkspace(t)
	if err := os.WriteFile(filepath.Join(ws, "staged.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	backupGit(t, ws, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(ws, "untracked.txt"), []byte("u"), 0o644); err != nil {
		t.Fatal(err)
	}
	// push 先を設定しても、段は push しない。
	bare := filepath.Join(t.TempDir(), "bare.git")
	backupGit(t, ws, "init", "-q", "--bare", bare)
	backupGit(t, ws, "remote", "add", "origin", bare)

	res := backupPhaseOf(t, ws)

	if res["outcome"] != "committed" {
		t.Fatalf("result = %v, want committed", res)
	}
	head := backupGit(t, ws, "rev-parse", "HEAD")
	if res["commit"] != head {
		t.Errorf("commit = %v, want HEAD %s", res["commit"], head)
	}
	if files := backupGit(t, ws, "show", "--name-only", "--format=", "HEAD"); files != ".flywheel/export.json" {
		t.Errorf("committed files = %q, want only .flywheel/export.json", files)
	}
	if subj := backupGit(t, ws, "log", "-1", "--format=%s"); subj != "chore(flywheel): backup Y-1" {
		t.Errorf("subject = %q", subj)
	}
	if st := backupGit(t, ws, "status", "--porcelain"); !strings.Contains(st, "A  staged.txt") || !strings.Contains(st, "?? untracked.txt") {
		t.Errorf("status after backup = %q, want staged.txt still staged and untracked.txt untouched", st)
	}
	if refs := backupGit(t, ws, "ls-remote", "origin"); refs != "" {
		t.Errorf("remote refs = %q, want none (backup must not push)", refs)
	}
}

func TestCycle_Backup_CommitsDeclaredMemoryDirWithTheExport(t *testing.T) {
	ws := backupWorkspace(t)
	writeAgentJSONForTest(t, ws, `{"position_file": "position.md", "memory_dir": "mem"}`)
	if err := os.MkdirAll(filepath.Join(ws, "mem"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "mem", "a.md"), []byte("m"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := backupPhaseOf(t, ws)

	if res["outcome"] != "committed" {
		t.Fatalf("result = %v, want committed", res)
	}
	files := strings.Fields(backupGit(t, ws, "show", "--name-only", "--format=", "HEAD"))
	if len(files) != 2 || files[0] != ".flywheel/export.json" || files[1] != "mem/a.md" {
		t.Errorf("committed files = %v, want export.json and mem/a.md", files)
	}
}

func TestCycle_Backup_IgnoredExportIsNotCommitted(t *testing.T) {
	ws := backupWorkspace(t)
	if err := os.WriteFile(filepath.Join(ws, ".gitignore"), []byte(".flywheel/export.json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	backupGit(t, ws, "add", ".gitignore")
	backupGit(t, ws, "commit", "-q", "-m", "ignore export")
	head := backupGit(t, ws, "rev-parse", "HEAD")

	res := backupPhaseOf(t, ws)

	if res["outcome"] != "ignored" || res["commit"] != nil || res["detail"] == nil {
		t.Errorf("result = %v, want ignored, no commit, with detail", res)
	}
	if got := backupGit(t, ws, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved to %s", got)
	}
}

func TestCycle_Backup_FailingHookIsFailedAndTheCycleStillSucceeds(t *testing.T) {
	ws := backupWorkspace(t)
	hook := filepath.Join(ws, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho nope >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	res := backupPhaseOf(t, ws) // cycleDoc は終了コード 0 でなければ失敗する

	if d, _ := res["detail"].(string); res["outcome"] != "failed" || d == "" {
		t.Errorf("result = %v, want failed with a non-empty detail", res)
	}
}

func TestCycle_Backup_SecondCycleCommitsAgainBecauseTheStoreChanged(t *testing.T) {
	ws := backupWorkspace(t)
	first := backupPhaseOf(t, ws)
	second := backupPhaseOf(t, ws)
	if first["outcome"] != "committed" || second["outcome"] != "committed" || first["commit"] == second["commit"] {
		t.Errorf("first = %v, second = %v, want two distinct commits (each cycle adds rows)", first, second)
	}
}

// 書き出しが決定的なら、ストアと記憶が変わらない 2 回目の段はコミットを作らない（実物の git）。
func TestBackup_UnchangedAgainstRealGit(t *testing.T) {
	ws := backupWorkspace(t)
	backupPhaseOf(t, ws) // 1 回目: committed
	st, err := core.OpenWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	// cycle が backup の後に足した作業ログの行を含めて、まず書き出してコミットし直す。
	st.BackupWorkspace(context.Background(), gitadapter.NewWorkspace(), &core.AgentDeclaration{}, "Y-8")
	res := st.BackupWorkspace(context.Background(), gitadapter.NewWorkspace(), &core.AgentDeclaration{}, "Y-9")
	if res.Outcome != core.BackupUnchanged || res.Commit != "" {
		t.Errorf("second backup = %+v, want unchanged", res)
	}
}
