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
// （/Users/ または /home/ で始まる文字列リテラル）を検出する。AC-80 の検査本体。
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
		if strings.HasPrefix(v, "/Users/") || strings.HasPrefix(v, "/home/") {
			pos := fset.Position(lit.Pos())
			violations = append(violations, pos.String()+": "+v)
		}
		return true
	})
	return violations, nil
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

// TestNoAbsolutePathsInNonTestSources は AC-80 本体: テストを除く Go のコードに
// 環境固有の絶対パスが含まれないことを検査する。
func TestNoAbsolutePathsInNonTestSources(t *testing.T) {
	root := repoRoot(t)
	var allViolations []string
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
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		violations, err := scanAbsolutePaths(fset, path, src)
		if err != nil {
			return err
		}
		allViolations = append(allViolations, violations...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(allViolations) != 0 {
		t.Fatalf("environment-specific absolute paths found in non-test Go sources:\n%s", strings.Join(allViolations, "\n"))
	}
}
