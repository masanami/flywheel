package cli

import (
	"strings"
	"testing"
)

// gitAdapterImportPath は internal/adapters/git のフルインポートパス（#101。AC-372）。
const gitAdapterImportPath = "github.com/masanami/flywheel/internal/adapters/git"

// TestGitAdapterImportDirections は AC-372 の検査本体: internal/adapters/git が
// （1）go list に現れ（空振り防止）、（2）ストア・SQLite ドライバを直接 import せず
// （テストファイルを含む）、（3）非テストファイルの直接 import が internal/core と
// 標準ライブラリだけで、（4）internal/core 配下（テストファイルを含む）が
// internal/adapters/git を import しない。
func TestGitAdapterImportDirections(t *testing.T) {
	root := repoRoot(t)
	pkgs := goListJSON(t, root, "./...")

	var sawGit bool
	var violators []string
	for _, pkg := range pkgs {
		switch {
		case pkg.ImportPath == gitAdapterImportPath:
			sawGit = true
			imports := allImports(pkg)
			if dependsOn(imports, storeImportPath) {
				violators = append(violators, pkg.ImportPath+" -> "+storeImportPath)
			}
			if dependsOn(imports, sqliteImportPath) {
				violators = append(violators, pkg.ImportPath+" -> "+sqliteImportPath)
			}
			for _, imp := range pkg.Imports {
				switch {
				case underPackagePrefix(imp, modulePath):
					if imp != corePackagePrefix {
						violators = append(violators, pkg.ImportPath+" -> "+imp)
					}
				case strings.Contains(strings.SplitN(imp, "/", 2)[0], "."):
					violators = append(violators, pkg.ImportPath+" -> "+imp+" (non-stdlib module)")
				}
			}
		case underPackagePrefix(pkg.ImportPath, corePackagePrefix):
			if dependsOn(allImports(pkg), gitAdapterImportPath) {
				violators = append(violators, pkg.ImportPath+" -> "+gitAdapterImportPath)
			}
		}
	}
	if !sawGit {
		t.Fatalf("go list did not report %s; the import path constant may be stale (empty-check to avoid a vacuous pass)", gitAdapterImportPath)
	}
	if len(violators) != 0 {
		t.Errorf("import direction violations: %s", strings.Join(violators, ", "))
	}
}
