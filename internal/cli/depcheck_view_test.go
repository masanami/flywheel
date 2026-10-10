package cli

import (
	"sort"
	"testing"
)

const (
	viewImportPath   = "github.com/masanami/flywheel/internal/view"
	serverImportPath = "github.com/masanami/flywheel/internal/server"
	cliImportPath    = "github.com/masanami/flywheel/internal/cli"
)

// TestViewImportsOnlyCore は internal/view が import してよい module 内のパッケージは
// internal/core だけであること（ストア・cli・server・adapter・invoker は不可）を、
// テストファイルを含めて go list の依存関係で検査する。
func TestViewImportsOnlyCore(t *testing.T) {
	pkgs := goListJSON(t, repoRoot(t), "./...")
	sawView := false
	var violators []string
	for _, pkg := range pkgs {
		if pkg.ImportPath != viewImportPath {
			continue
		}
		sawView = true
		for _, imp := range allImports(pkg) {
			if !underPackagePrefix(imp, "github.com/masanami/flywheel") {
				continue
			}
			if imp != corePackagePrefix && imp != viewImportPath {
				violators = append(violators, imp)
			}
		}
	}
	if !sawView {
		t.Fatal("go list did not report internal/view")
	}
	sort.Strings(violators)
	if len(violators) != 0 {
		t.Errorf("internal/view imports packages other than internal/core: %v", violators)
	}
}

// TestCoreDoesNotImportViewOrServer は internal/core 配下（テストを含む）が
// internal/view・internal/server を import しないことを検査する。
func TestCoreDoesNotImportViewOrServer(t *testing.T) {
	pkgs := goListJSON(t, repoRoot(t), "./...")
	sawCore := false
	var violators []string
	for _, pkg := range pkgs {
		if !underPackagePrefix(pkg.ImportPath, corePackagePrefix) {
			continue
		}
		sawCore = true
		if dependsOn(allImports(pkg), viewImportPath) || dependsOn(allImports(pkg), serverImportPath) {
			violators = append(violators, pkg.ImportPath)
		}
	}
	if !sawCore {
		t.Fatal("go list did not report internal/core")
	}
	if len(violators) != 0 {
		t.Errorf("internal/core imports internal/view or internal/server: %v", violators)
	}
}

// TestServerDoesNotImportCLI は internal/server が internal/cli を import しないことを
// 検査する（cli→server の向きがあるため循環する）。対象を見つけたことも確かめる。
func TestServerDoesNotImportCLI(t *testing.T) {
	sawServer := false
	for _, pkg := range goListJSON(t, repoRoot(t), "./...") {
		if !underPackagePrefix(pkg.ImportPath, serverImportPath) {
			continue
		}
		sawServer = true
		if dependsOn(allImports(pkg), cliImportPath) {
			t.Errorf("%s imports internal/cli", pkg.ImportPath)
		}
	}
	if !sawServer {
		t.Fatal("go list did not report internal/server")
	}
}
