package cli

import (
	"strings"
	"testing"
)

// invokerPackagePrefix は "internal/invoker" 配下とみなすインポートパスの
// 接頭辞（#81。depcheck_adapters_test.go の adaptersPackagePrefix と同型）。
const invokerPackagePrefix = "github.com/masanami/flywheel/internal/invoker"

// TestInvokerPackagesDoNotImportStoreDirectly は AC「internal/invoker は、
// ストアのパッケージを import しない（go list の依存関係で検査する）」の
// 検査本体（TestAdaptersPackagesDoNotImportStoreDirectly と同型）。
func TestInvokerPackagesDoNotImportStoreDirectly(t *testing.T) {
	root := repoRoot(t)
	pkgs := goListJSON(t, root, "./...")

	var (
		sawInvokerPackage bool
		violators         []string
	)
	for _, pkg := range pkgs {
		if !underPackagePrefix(pkg.ImportPath, invokerPackagePrefix) {
			continue
		}
		sawInvokerPackage = true

		imports := allImports(pkg)
		if dependsOn(imports, storeImportPath) {
			violators = append(violators, pkg.ImportPath+" -> "+storeImportPath)
		}
		if dependsOn(imports, sqliteImportPath) {
			violators = append(violators, pkg.ImportPath+" -> "+sqliteImportPath)
		}
	}

	if !sawInvokerPackage {
		t.Fatal("go list did not report any package under internal/invoker; the prefix constant may be stale (empty-check to avoid a vacuous pass)")
	}
	if len(violators) != 0 {
		t.Errorf("internal/invoker packages directly import the store: %s", strings.Join(violators, ", "))
	}
}

// TestInvokerNonTestImportsAreCoreAndStdlibOnly は CLAUDE.md の
// 「internal/invoker は core の判断の呼び出しIFだけに依存する」を検査する:
// internal/invoker 配下の非テストファイルの直接 import は、モジュール内なら
// internal/core だけ（internal/cli・internal/adapters・coretest 等を含まない）、
// モジュール外なら標準ライブラリだけであること
// （TestAdaptersNonTestImportsAreCoreAndStdlibOnly と同型）。
func TestInvokerNonTestImportsAreCoreAndStdlibOnly(t *testing.T) {
	root := repoRoot(t)
	pkgs := goListJSON(t, root, "./internal/invoker/...")
	if len(pkgs) == 0 {
		t.Fatal("go list reported no package under internal/invoker (empty-check to avoid a vacuous pass)")
	}

	var violators []string
	for _, pkg := range pkgs {
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
	}
	if len(violators) != 0 {
		t.Errorf("internal/invoker packages import something other than internal/core and the standard library: %s", strings.Join(violators, ", "))
	}
}
