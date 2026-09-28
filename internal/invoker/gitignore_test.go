package invoker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// self-review 指摘（round2 CONFIRMED, round3 PLAUSIBLE）: .gitignore が
// 無いときにここで runs/ だけを含む狭い内容を新規作成すると、以後
// core.Init の writeGitignoreIfMissing が「既にファイルがある」と誤認して
// flywheel.db 等を含む完全な内容へ復元する機会を失う（D10の保護の恒久的な
// 喪失）一方、逆に何もしないと§invoker の共通の規則（runs/の除外）を
// 満たせない。core.EnsureWorkspaceGitignore（core の正本の内容）を使えば
// 両方満たせる: 無ければ core の完全な内容（flywheel.db・-wal・-shm・
// runs/）で作る。
func TestEnsureRunsGitignoreEntry_CreatesFullCoreContentIfMissing(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".flywheel"), 0o755); err != nil {
		t.Fatalf("mkdir .flywheel: %v", err)
	}
	if err := ensureRunsGitignoreEntry(ws); err != nil {
		t.Fatalf("ensureRunsGitignoreEntry: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(ws, ".flywheel", ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	for _, want := range []string{"flywheel.db", "flywheel.db-wal", "flywheel.db-shm", "runs/"} {
		if !strings.Contains(string(data), want) {
			t.Errorf(".gitignore = %q, want %q (core.EnsureWorkspaceGitignore's full content, not a narrow one)", data, want)
		}
	}
}

func TestEnsureRunsGitignoreEntry_AppendsIfMissingFromExistingFile(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".flywheel"), 0o755); err != nil {
		t.Fatalf("mkdir .flywheel: %v", err)
	}
	path := filepath.Join(ws, ".flywheel", ".gitignore")
	if err := os.WriteFile(path, []byte("flywheel.db\n"), 0o644); err != nil {
		t.Fatalf("seed .gitignore: %v", err)
	}
	if err := ensureRunsGitignoreEntry(ws); err != nil {
		t.Fatalf("ensureRunsGitignoreEntry: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !strings.Contains(string(data), "flywheel.db") || !strings.Contains(string(data), "runs/") {
		t.Errorf(".gitignore = %q, want both existing content and runs/", data)
	}
}

func TestEnsureRunsGitignoreEntry_IdempotentDoesNotDuplicate(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".flywheel"), 0o755); err != nil {
		t.Fatalf("mkdir .flywheel: %v", err)
	}
	// .gitignore が無いと ensureRunsGitignoreEntry は何もしない（上の
	// DoesNothingIfGitignoreMissing）ため、この冪等性の検査は既存の
	// .gitignore がある状態から始める。
	if err := os.WriteFile(filepath.Join(ws, ".flywheel", ".gitignore"), []byte("flywheel.db\n"), 0o644); err != nil {
		t.Fatalf("seed .gitignore: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := ensureRunsGitignoreEntry(ws); err != nil {
			t.Fatalf("ensureRunsGitignoreEntry[%d]: %v", i, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(ws, ".flywheel", ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if strings.Count(string(data), "runs/") != 1 {
		t.Errorf(".gitignore = %q, want exactly one runs/ line", data)
	}
}
