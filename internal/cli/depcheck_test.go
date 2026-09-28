package cli

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// storeImportPath は internal/core/internal/store のフルインポートパス。
// Go の internal 規則により、この配下は internal/core（とその配下）からしか
// import できない。このテストは、完了条件「ストアのパッケージを import して
// いるのが internal/core 配下だけであることを go list の依存関係で検査する
// テストがある」（AC-79 に対応）を、コンパイラの規則に頼るだけでなく明示的に
// 検証する。
const storeImportPath = "github.com/masanami/flywheel/internal/core/internal/store"

// sqliteImportPath は modernc.org/sqlite のフルインポートパス。internal/core
// 配下（PRAGMA・トランザクションモードの前提を握るコード、およびそのテスト
// 支援パッケージ internal/core/coretest）だけが直接触ってよい。それ以外の
// パッケージ（internal/cli・cmd/flywheel を含む）は internal/core の公開 API
// 経由でしか触らない。
const sqliteImportPath = "modernc.org/sqlite"

// corePackagePrefix は「internal/core 配下」とみなすインポートパスの接頭辞。
const corePackagePrefix = "github.com/masanami/flywheel/internal/core"

// coretestImportPath は internal/core/coretest のフルインポートパス。テスト
// 支援専用であり、本番バイナリ（cmd/flywheel）の非テスト依存に含まれては
// ならない。
const coretestImportPath = "github.com/masanami/flywheel/internal/core/coretest"

type goListPackage struct {
	ImportPath   string   `json:"ImportPath"`
	Imports      []string `json:"Imports"`      // 非テストファイルの直接 import
	TestImports  []string `json:"TestImports"`  // 同一パッケージのテストファイルの直接 import
	XTestImports []string `json:"XTestImports"` // 外部テストパッケージ（_test）の直接 import
}

// underPackagePrefix は p が prefix そのもの、または prefix 配下
// （"prefix/..." 形式）であるかを判定する。単純な strings.HasPrefix(p, prefix)
// だと、prefix が "internal/core" のとき "internal/core-extra" のような
// 無関係なパッケージも誤って「配下」と判定してしまう（レビュー指摘）。
func underPackagePrefix(p, prefix string) bool {
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

func goListJSON(t *testing.T, dir string, args ...string) []goListPackage {
	t.Helper()
	cmd := exec.Command("go", append([]string{"list", "-json"}, args...)...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go list -json %v: %v\n%s", args, err, stderr.String())
	}

	dec := json.NewDecoder(&stdout)
	var pkgs []goListPackage
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		pkgs = append(pkgs, pkg)
	}
	return pkgs
}

func TestStorePackageIsOnlyImportedFromWithinCore(t *testing.T) {
	root := repoRoot(t)
	pkgs := goListJSON(t, root, "./...")

	var violators []string
	sawStorePackageItself := false
	for _, pkg := range pkgs {
		if pkg.ImportPath == storeImportPath {
			sawStorePackageItself = true
			continue
		}
		imports := allImports(pkg)
		if !dependsOn(imports, storeImportPath) {
			continue
		}
		if !underPackagePrefix(pkg.ImportPath, corePackagePrefix) {
			violators = append(violators, pkg.ImportPath)
		}
	}
	if !sawStorePackageItself {
		t.Fatal("go list did not report the store package itself; the import path constant may be stale")
	}
	if len(violators) != 0 {
		t.Errorf("packages outside internal/core import the store package: %v", violators)
	}
}

// TestSQLiteDriverIsOnlyImportedFromWithinCore は item 10(ii) の検証:
// internal/core 配下以外のパッケージが（テストファイルを含め）
// modernc.org/sqlite を直接 import していないことを検査する。internal/cli の
// テストはかつて生の *sql.DB でフィクスチャを作っており、internal/core の
// PRAGMA・トランザクションモードの前提（file: URI・busy_timeout 等）をすり抜けて
// いた（レビュー指摘）。internal/core/coretest（internal/core 配下）に委譲する
// ことで、この import 自体を internal/core の外へ出さない。
func TestSQLiteDriverIsOnlyImportedFromWithinCore(t *testing.T) {
	root := repoRoot(t)
	// modernc.org/sqlite は外部モジュールであり、"./..." には（この module 内の
	// パッケージとしては）現れない。ここでは、この module 内のパッケージが直接
	// import しているかどうかだけを Imports/TestImports/XTestImports から見る。
	pkgs := goListJSON(t, root, "./...")

	var sawAnyDirectImport bool
	var violators []string
	for _, pkg := range pkgs {
		imports := allImports(pkg)
		if !dependsOn(imports, sqliteImportPath) {
			continue
		}
		sawAnyDirectImport = true
		if !underPackagePrefix(pkg.ImportPath, corePackagePrefix) {
			violators = append(violators, pkg.ImportPath)
		}
	}
	if !sawAnyDirectImport {
		t.Fatal("no package in this module imports modernc.org/sqlite directly; the import path constant may be stale")
	}
	if len(violators) != 0 {
		t.Errorf("packages outside internal/core import modernc.org/sqlite directly: %v", violators)
	}
}

// creackPTYImportPath は github.com/creack/pty のフルインポートパス。#11 の
// 疑似端末テストヘルパー（confirm_process_test.go）だけが使うテスト専用の
// 依存であり、本番バイナリ（cmd/flywheel）の依存グラフに含まれてはならない
// （CLAUDE.md「本番の依存の上限」。テスト専用の依存は可）。
const creackPTYImportPath = "github.com/creack/pty"

// TestProductionBinaryDoesNotDependOnCreackPTY は cmd/flywheel の本番依存
// （-test を付けない go list -deps）に github.com/creack/pty が含まれない
// ことを検査する（TestProductionBinaryDoesNotDependOnCoretest と同型）。
func TestProductionBinaryDoesNotDependOnCreackPTY(t *testing.T) {
	root := repoRoot(t)
	cmd := exec.Command("go", "list", "-deps", "./cmd/flywheel")
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go list -deps ./cmd/flywheel: %v\n%s", err, stderr.String())
	}

	deps := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	for _, d := range deps {
		if d == creackPTYImportPath {
			t.Fatalf("cmd/flywheel's production dependency graph includes %s (test-only package)", creackPTYImportPath)
		}
	}
}

// TestProductionBinaryDoesNotDependOnCoretest は item 10(iii) の検証:
// cmd/flywheel の本番依存（-test を付けない go list -deps）に
// internal/core/coretest が含まれないこと。coretest はテスト支援専用であり
// (testing パッケージを import する)、本番バイナリへ紛れ込んではならない。
func TestProductionBinaryDoesNotDependOnCoretest(t *testing.T) {
	root := repoRoot(t)
	cmd := exec.Command("go", "list", "-deps", "./cmd/flywheel")
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go list -deps ./cmd/flywheel: %v\n%s", err, stderr.String())
	}

	deps := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	for _, d := range deps {
		if d == coretestImportPath {
			t.Fatalf("cmd/flywheel's production dependency graph includes %s (test-only package)", coretestImportPath)
		}
	}
}

// invokerImportPath は #81
// （docs/features/m3-invoker-delegation.md §技術的な制約・方針「import の向き」
// 「internal/core は internal/invoker・internal/adapters を import しない」・
// 受入基準「internal/core は internal/invoker と internal/adapters を
// import しない（go list の依存関係で検査する）」）の検査対象。
// adaptersPackagePrefix は depcheck_adapters_test.go が既に定義している。
const invokerImportPath = "github.com/masanami/flywheel/internal/invoker"

// TestCoreDoesNotImportInvokerOrAdapters は AC-169 相当の検査本体:
// internal/core（テストファイルを含む）が internal/invoker・
// internal/adapters/* を import していないことを、go list の依存関係で検証する。
func TestCoreDoesNotImportInvokerOrAdapters(t *testing.T) {
	root := repoRoot(t)
	pkgs := goListJSON(t, root, "./...")

	var sawCorePackageItself bool
	var violators []string
	for _, pkg := range pkgs {
		if !underPackagePrefix(pkg.ImportPath, corePackagePrefix) {
			continue
		}
		sawCorePackageItself = true
		imports := allImports(pkg)
		if dependsOn(imports, invokerImportPath) {
			violators = append(violators, pkg.ImportPath+" -> "+invokerImportPath)
		}
		for _, imp := range imports {
			if underPackagePrefix(imp, adaptersPackagePrefix) {
				violators = append(violators, pkg.ImportPath+" -> "+imp)
			}
		}
	}
	if !sawCorePackageItself {
		t.Fatal("go list did not report any internal/core package; the prefix constant may be stale")
	}
	if len(violators) != 0 {
		t.Errorf("internal/core packages import internal/invoker or internal/adapters: %v", violators)
	}
}

func allImports(pkg goListPackage) []string {
	return append(append(append([]string{}, pkg.Imports...), pkg.TestImports...), pkg.XTestImports...)
}

func dependsOn(deps []string, target string) bool {
	for _, d := range deps {
		if d == target {
			return true
		}
	}
	return false
}
