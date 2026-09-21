package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveBaseDir_PrefersWorkspaceFlagOverEnvAndCwd(t *testing.T) {
	t.Setenv(WorkspaceEnvVar, "/should/not/be/used")
	flagDir := t.TempDir()

	got, err := resolveBaseDir(flagDir)
	if err != nil {
		t.Fatalf("resolveBaseDir() error = %v", err)
	}
	want, _ := filepath.Abs(flagDir)
	if got != want {
		t.Fatalf("resolveBaseDir() = %q, want %q", got, want)
	}
}

func TestResolveBaseDir_FallsBackToEnvVarWhenFlagEmpty(t *testing.T) {
	envDir := t.TempDir()
	t.Setenv(WorkspaceEnvVar, envDir)

	got, err := resolveBaseDir("")
	if err != nil {
		t.Fatalf("resolveBaseDir() error = %v", err)
	}
	want, _ := filepath.Abs(envDir)
	if got != want {
		t.Fatalf("resolveBaseDir() = %q, want %q", got, want)
	}
}

func TestResolveBaseDir_FallsBackToCwdWhenFlagAndEnvEmpty(t *testing.T) {
	t.Setenv(WorkspaceEnvVar, "")
	dir := t.TempDir()
	t.Chdir(dir)

	got, err := resolveBaseDir("")
	if err != nil {
		t.Fatalf("resolveBaseDir() error = %v", err)
	}
	// macOS の t.TempDir() は /var/folders/... の symlink（/private/var/...）を
	// 経由することがあるため、os.Getwd() 自身の結果と比較する（絶対パス表現の
	// 差異を吸収する）。
	want, _ := os.Getwd()
	if got != want {
		t.Fatalf("resolveBaseDir() = %q, want %q", got, want)
	}
}

func TestFindWorkspace_FindsStoreInAncestorDirectory(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, flywheelDirName))
	mustWriteFile(t, storeDBPath(root), []byte("dummy"))

	child := filepath.Join(root, "a", "b", "c")
	mustMkdirAll(t, child)

	dir, ok := findWorkspace(child)
	if !ok {
		t.Fatal("findWorkspace() ok = false, want true")
	}
	if dir != root {
		t.Fatalf("findWorkspace() = %q, want %q", dir, root)
	}
}

func TestFindWorkspace_ReturnsFalseWhenNoStoreExistsAnywhereAbove(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "x", "y")
	mustMkdirAll(t, child)

	if _, ok := findWorkspace(child); ok {
		t.Fatal("findWorkspace() ok = true, want false (no store anywhere above)")
	}
}

func TestFindWorkspace_ChildStoreDoesNotShadowFindingItsOwnAncestor(t *testing.T) {
	// 子ディレクトリ自身にストアがあれば、それが最初に見つかる（親へは遡らない）。
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, flywheelDirName))
	mustWriteFile(t, storeDBPath(root), []byte("root store"))

	child := filepath.Join(root, "child")
	mustMkdirAll(t, filepath.Join(child, flywheelDirName))
	mustWriteFile(t, storeDBPath(child), []byte("child store"))

	dir, ok := findWorkspace(child)
	if !ok {
		t.Fatal("findWorkspace() ok = false, want true")
	}
	if dir != child {
		t.Fatalf("findWorkspace() = %q, want %q (自分自身のストアを優先する)", dir, child)
	}
}

// --- test helpers ---

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
