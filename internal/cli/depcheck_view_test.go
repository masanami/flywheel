package cli

import (
	"sort"
	"strings"
	"testing"
)

const (
	viewImportPath   = "github.com/masanami/flywheel/internal/view"
	serverImportPath = "github.com/masanami/flywheel/internal/server"
	cliImportPath    = "github.com/masanami/flywheel/internal/cli"
	flowImportPath   = "github.com/masanami/flywheel/internal/flow"
)

// TestViewImportsOnlyCore は internal/view が import してよい module 内のパッケージは
// internal/core だけであること（ストア・cli・server・adapter・invoker は不可）を、
// テストファイルを含めて go list の依存関係で検査する。
func TestViewImportsOnlyCore(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// TestFlowImportDirection は internal/flow の import の向きを、テストファイルを含めて検査する。
// internal/core（ストアを含む）・internal/invoker・internal/server・internal/view は internal/flow の配下を
// import しない（AC-9〜12）。internal/flow の配下は internal/core（ストアを含む）・internal/invoker・
// internal/server・internal/view・internal/adapters の配下・internal/cli のどれも import しない（AC-13）。
func TestFlowImportDirection(t *testing.T) {
	t.Parallel()
	forbiddenFromFlow := []string{
		corePackagePrefix, invokerImportPath, serverImportPath, viewImportPath, adaptersPackagePrefix, cliImportPath,
	}
	cannotImportFlow := map[string]string{
		corePackagePrefix: "AC-9", invokerImportPath: "AC-10", serverImportPath: "AC-11", viewImportPath: "AC-12",
		adaptersPackagePrefix: "cli 以外は import しない", modulePath + "/cmd": "cli 以外は import しない",
	}
	sawFlow := map[string]bool{}
	var violators []string
	for _, pkg := range goListJSON(t, repoRoot(t), "./...") {
		imports := allImports(pkg)
		if underPackagePrefix(pkg.ImportPath, flowImportPath) {
			sawFlow[pkg.ImportPath] = true
			for _, imp := range imports {
				if !underPackagePrefix(imp, modulePath) || underPackagePrefix(imp, flowImportPath) {
					continue
				}
				for _, f := range forbiddenFromFlow {
					if underPackagePrefix(imp, f) {
						violators = append(violators, pkg.ImportPath+" -> "+imp+" (AC-13)")
					}
				}
			}
			continue
		}
		for from, ac := range cannotImportFlow {
			if !underPackagePrefix(pkg.ImportPath, from) {
				continue
			}
			for _, imp := range imports {
				if underPackagePrefix(imp, flowImportPath) {
					violators = append(violators, pkg.ImportPath+" -> "+imp+" ("+ac+")")
				}
			}
		}
	}
	for _, want := range []string{flowImportPath + "/workflow", flowImportPath + "/runstate"} {
		if !sawFlow[want] {
			t.Fatalf("go list did not report %s", want)
		}
	}
	sort.Strings(violators)
	if len(violators) != 0 {
		t.Errorf("import direction around internal/flow is violated: %v", violators)
	}
}

// TestFlowPackagesImportOnlyTheirAllowedModules は internal/flow の各パッケージが import してよい module 外の
// パッケージを、テストファイルを除いて検査する。runstate は標準ライブラリだけ、workflow は標準ライブラリと
// gopkg.in/yaml.v3・github.com/santhosh-tekuri/jsonschema/v6 だけ（全体の上限は AC-8 の試験が見る）。
func TestFlowPackagesImportOnlyTheirAllowedModules(t *testing.T) {
	t.Parallel()
	allowed := map[string][]string{
		flowImportPath + "/runstate": nil,
		flowImportPath + "/workflow": {"gopkg.in/yaml.v3", "github.com/santhosh-tekuri/jsonschema/v6"},
	}
	saw := map[string]bool{}
	for _, pkg := range goListJSON(t, repoRoot(t), "./...") {
		ok, found := allowed[pkg.ImportPath]
		if !found {
			continue
		}
		saw[pkg.ImportPath] = true
		for _, imp := range pkg.Imports {
			first, _, _ := strings.Cut(imp, "/")
			if !strings.Contains(first, ".") || underPackagePrefix(imp, modulePath) {
				continue // 標準ライブラリと、この module 内（向きは TestFlowImportDirection が見る）
			}
			permitted := false
			for _, a := range ok {
				permitted = permitted || underPackagePrefix(imp, a)
			}
			if !permitted {
				t.Errorf("%s imports %s, which is outside its allowed set %v", pkg.ImportPath, imp, ok)
			}
		}
	}
	for want := range allowed {
		if !saw[want] {
			t.Fatalf("go list did not report %s", want)
		}
	}
}
