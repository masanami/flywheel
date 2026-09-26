package cli

import (
	"strings"
	"testing"
)

// adaptersPackagePrefix は "internal/adapters" 配下とみなすインポートパスの
// 接頭辞。
const adaptersPackagePrefix = "github.com/masanami/flywheel/internal/adapters"

// githubAdapterImportPath は internal/adapters/github のフルインポートパス
// （AC-105 の「パッケージ自体が go list に現れる（空振り防止）」の検査に使う）。
const githubAdapterImportPath = "github.com/masanami/flywheel/internal/adapters/github"

// TestAdaptersPackagesDoNotImportStoreDirectly は AC-105 の検査本体:
// internal/adapters 配下の各パッケージの直接 import（非テストファイル・
// 同一パッケージのテストファイル・外部テストパッケージのいずれも）に、
// ストアのパッケージ（internal/core/internal/store）が含まれないことを
// 確かめる。
//
// -deps（推移的依存）を使わない理由: internal/adapters/github は core の
// 公開 API（internal/core）を import してよく、core は internal/core/internal
// /store を推移的に含む。-deps でストアの有無を見ると、adapter が core 経由で
// 間接的にストアへ推移的に依存していること自体を「違反」と誤検出する
// （adapter が core を呼ぶことは設計どおりであり、ここで検査したいのは
// 「adapter がストアを直接 import していないか」だけ）。そのため go list -json
// の Imports/TestImports/XTestImports（直接 import のみ）で判定する
// （TestStorePackageIsOnlyImportedFromWithinCore と同じ手法）。
func TestAdaptersPackagesDoNotImportStoreDirectly(t *testing.T) {
	root := repoRoot(t)
	pkgs := goListJSON(t, root, "./...")

	var (
		sawAdaptersPackage bool
		sawGithubAdapter   bool
		violators          []string
	)
	for _, pkg := range pkgs {
		if !underPackagePrefix(pkg.ImportPath, adaptersPackagePrefix) {
			continue
		}
		sawAdaptersPackage = true
		if pkg.ImportPath == githubAdapterImportPath {
			sawGithubAdapter = true
		}

		imports := allImports(pkg)
		if dependsOn(imports, storeImportPath) {
			violators = append(violators, pkg.ImportPath+" -> "+storeImportPath)
		}
		if dependsOn(imports, sqliteImportPath) {
			violators = append(violators, pkg.ImportPath+" -> "+sqliteImportPath)
		}
	}

	if !sawAdaptersPackage {
		t.Fatal("go list did not report any package under internal/adapters; the prefix constant may be stale (empty-check to avoid a vacuous pass)")
	}
	if !sawGithubAdapter {
		t.Fatalf("go list did not report %s itself; the import path constant may be stale", githubAdapterImportPath)
	}
	if len(violators) != 0 {
		t.Errorf("internal/adapters packages directly import the store: %s", strings.Join(violators, ", "))
	}
}

// modulePath はこのリポジトリのモジュールパス。
const modulePath = "github.com/masanami/flywheel"

// TestAdaptersNonTestImportsAreCoreAndStdlibOnly は CLAUDE.md の
// 「internal/adapters/github は core の取得 IF だけに依存する」を検査する:
// internal/adapters 配下の非テストファイルの直接 import は、モジュール内なら
// internal/core だけ（coretest・internal/cli 等を含まない）、モジュール外なら
// 標準ライブラリだけ（先頭の要素に "." を含むパス＝外部モジュールを含まない）
// であること。
func TestAdaptersNonTestImportsAreCoreAndStdlibOnly(t *testing.T) {
	root := repoRoot(t)
	pkgs := goListJSON(t, root, "./internal/adapters/...")
	if len(pkgs) == 0 {
		t.Fatal("go list reported no package under internal/adapters (empty-check to avoid a vacuous pass)")
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
		t.Errorf("internal/adapters packages import something other than internal/core and the standard library: %s", strings.Join(violators, ", "))
	}
}
