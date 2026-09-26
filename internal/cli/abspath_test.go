package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// scanAbsolutePaths は src を Go ソースとして解析し、環境固有の絶対パス
// （/Users/ または /home/ で始まる文字列リテラル、および gh の絶対パス
// 〈"/" で始まり basename が "gh" の文字列リテラル。例:
// "/usr/local/bin/gh"・"/opt/homebrew/bin/gh"〉）を検出する。
// AC-80・AC-106 の検査本体。
func scanAbsolutePaths(fset *token.FileSet, filename string, src []byte) ([]string, error) {
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return nil, err
	}
	var violations []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		v, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		if strings.HasPrefix(v, "/Users/") || strings.HasPrefix(v, "/home/") || isAbsoluteGHPathLiteral(v) {
			pos := fset.Position(lit.Pos())
			violations = append(violations, pos.String()+": "+v)
		}
		return true
	})
	return violations, nil
}

// isAbsoluteGHPathLiteral は v が「gh の絶対パス」（"/" で始まり、basename が
// "gh"）かどうかを判定する（AC-106。gh の絶対パスをコードに埋め込まない
// という完了条件を、"/Users/"・"/home/" 接頭辞だけでは捕まえられない
// パス〈例: "/usr/local/bin/gh"・"/opt/homebrew/bin/gh"〉についても検査
// できるようにする拡張）。裸の "gh"（先頭が "/" でない）や
// "repos/x/gh"（同じく先頭が "/" でない）は誤検出しない。
func isAbsoluteGHPathLiteral(v string) bool {
	if !strings.HasPrefix(v, "/") {
		return false
	}
	return filepath.Base(v) == "gh"
}

func TestScanAbsolutePaths_DetectsUsersPrefix(t *testing.T) {
	fset := token.NewFileSet()
	src := []byte("package example\n\nconst path = \"/Users/example/secret\"\n")
	violations, err := scanAbsolutePaths(fset, "synthetic.go", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(violations) != 1 {
		t.Fatalf("want 1 violation, got %d: %v", len(violations), violations)
	}
}

func TestScanAbsolutePaths_DetectsHomePrefix(t *testing.T) {
	fset := token.NewFileSet()
	src := []byte("package example\n\nconst path = \"/home/example/secret\"\n")
	violations, err := scanAbsolutePaths(fset, "synthetic.go", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(violations) != 1 {
		t.Fatalf("want 1 violation, got %d: %v", len(violations), violations)
	}
}

func TestScanAbsolutePaths_NoFalsePositiveOnUnrelatedPaths(t *testing.T) {
	fset := token.NewFileSet()
	src := []byte("package example\n\nconst a = \"/tmp/example\"\nconst b = \"users/example\"\nconst c = \"/etc/passwd\"\n")
	violations, err := scanAbsolutePaths(fset, "synthetic.go", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("want 0 violations, got %v", violations)
	}
}

// TestScanAbsolutePaths_DetectsAbsoluteGHPath は AC-106 の拡張本体:
// "/usr/local/bin/gh"・"/opt/homebrew/bin/gh" のような gh の絶対パスの
// 文字列リテラルを検出することを確かめる。
func TestScanAbsolutePaths_DetectsAbsoluteGHPath(t *testing.T) {
	for _, v := range []string{"/usr/local/bin/gh", "/opt/homebrew/bin/gh"} {
		fset := token.NewFileSet()
		src := []byte("package example\n\nconst ghPath = \"" + v + "\"\n")
		violations, err := scanAbsolutePaths(fset, "synthetic.go", src)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(violations) != 1 {
			t.Fatalf("%q: want 1 violation, got %d: %v", v, len(violations), violations)
		}
	}
}

// TestScanAbsolutePaths_NoFalsePositiveOnGHLikeStrings は、gh の絶対パス
// 検出の拡張が、裸の "gh"（PATH 探索に使う実行ファイル名そのもの）や
// "repos/x/gh"（先頭が "/" でない）を誤検出しないことを確かめる
// （AC-106「"gh" や "repos/x/gh" は誤検出しない」）。
func TestScanAbsolutePaths_NoFalsePositiveOnGHLikeStrings(t *testing.T) {
	fset := token.NewFileSet()
	src := []byte(`package example

const a = "gh"
const b = "repos/x/gh"
const c = "/foo/ghost"
`)
	violations, err := scanAbsolutePaths(fset, "synthetic.go", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("want 0 violations, got %v", violations)
	}
}

// nonTestGoFilePaths は root 配下（.git を除く）を再帰的に走査し、テストを
// 除く Go のソースファイルの絶対パスを列挙する（AC-80・AC-106 の走査本体。
// TestNoAbsolutePathsInNonTestSources と
// TestNonTestGoFilesIncludeAdaptersGithub の両方から使う共通ヘルパーへ
// 切り出したもの。元は前者に直書きだったが、「この走査が internal/adapters
// を含んでいる」こと自体を固定するテスト〈AC-106〉を書くために分離した）。
func nonTestGoFilePaths(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return paths
}

// TestNoAbsolutePathsInNonTestSources は AC-80 本体: テストを除く Go のコードに
// 環境固有の絶対パスが含まれないことを検査する。
func TestNoAbsolutePathsInNonTestSources(t *testing.T) {
	root := repoRoot(t)
	var allViolations []string
	for _, path := range nonTestGoFilePaths(t, root) {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		fset := token.NewFileSet()
		violations, err := scanAbsolutePaths(fset, path, src)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		allViolations = append(allViolations, violations...)
	}
	if len(allViolations) != 0 {
		t.Fatalf("environment-specific absolute paths found in non-test Go sources:\n%s", strings.Join(allViolations, "\n"))
	}
}

// TestNonTestGoFilesIncludeAdaptersGithub は AC-106 を固定する: AC-80 の
// 走査（nonTestGoFilePaths）がリポジトリ全体を対象にしており、
// internal/adapters/github の非テスト .go ファイルを実際に含んでいることを
// 確かめる（本チケット #55 で internal/adapters/github を新設した時点の
// コードで確認済み。走査範囲が将来 internal/adapters を除外する方向へ
// 変わったときにこのテストが検出する）。
func TestNonTestGoFilesIncludeAdaptersGithub(t *testing.T) {
	root := repoRoot(t)
	wantSubstring := string(filepath.Separator) + filepath.Join("internal", "adapters", "github") + string(filepath.Separator)

	var found []string
	for _, path := range nonTestGoFilePaths(t, root) {
		if strings.Contains(path, wantSubstring) {
			found = append(found, path)
		}
	}
	if len(found) == 0 {
		t.Fatalf("nonTestGoFilePaths(root) did not include any internal/adapters/github/*.go file; the AC-80 scan may no longer cover internal/adapters (want a path containing %q)", wantSubstring)
	}
}
